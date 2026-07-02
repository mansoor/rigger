// Package detect statically inspects a checked-out source repository and drafts a
// unified service graph (the same shape config.json services[] uses) for the user
// to review and edit. It NEVER executes repo code — pure file reads. Detection is
// advisory: Rigger proposes, the user approves.
//
// Signals, strongest first: an existing docker-compose.yml, then Dockerfile(s)
// (monorepo-aware), then language manifests (mapped to internal/blueprints),
// then a Procfile (workers), then dependency/env hints for managed dependencies.
package detect

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mansoor/rigger/ui/internal/blueprints"
	yaml "go.yaml.in/yaml/v3"
)

// Service mirrors a config.json services[] entry (the unified model). JSON tags
// match composegen's Service so the draft flows straight into config.json and the
// generated compose (extra_ports, healthcheck_config, build args/dockerfile, …).
type Service struct {
	Name              string             `json:"name"`
	Build             *Build             `json:"build,omitempty"`
	Image             string             `json:"image,omitempty"`
	ImageFrom         string             `json:"image_from,omitempty"`
	Tag               string             `json:"tag,omitempty"`
	Command           string             `json:"command,omitempty"`
	Port              string             `json:"port,omitempty"`
	HostPort          string             `json:"host_port,omitempty"`
	ExtraPorts        []string           `json:"extra_ports,omitempty"`
	WebRouted         bool               `json:"web_routed,omitempty"`
	Subdomain         string             `json:"subdomain,omitempty"`
	Healthcheck       string             `json:"healthcheck,omitempty"`
	HealthcheckConfig *HealthcheckConfig `json:"healthcheck_config,omitempty"`
	EnvFile           bool               `json:"env_file,omitempty"`
	DependsOn         []string           `json:"depends_on,omitempty"`
	// DependsOnConditions preserves an imported compose's per-dependency `condition:`
	// (long form). Without this the condition is silently downgraded to service_started
	// on regeneration — breaking apps that gate on service_healthy / a one-shot migrate.
	DependsOnConditions map[string]string `json:"depends_on_conditions,omitempty"`
	Volumes             []string          `json:"volumes,omitempty"`
	Restart             string            `json:"restart,omitempty"`
	ConfigTemplate      string            `json:"config_template,omitempty"`
	EnvVars             map[string]string `json:"env_vars,omitempty"`
	Links               []ServiceLink     `json:"links,omitempty"`
}

// ServiceLink mirrors composegen's ServiceLink — a declared dependency on another
// service's in-network URL, injected as an env var. The detector leaves this empty
// today (users add links in Edit Project); the field exists so links round-trip
// through the scan draft straight into config.json.
type ServiceLink struct {
	Service string `json:"service"`
	EnvVar  string `json:"env_var"`
	Port    string `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
	Scheme  string `json:"scheme,omitempty"`
}

// HealthcheckConfig mirrors composegen's per-service healthcheck timing.
type HealthcheckConfig struct {
	Interval    string `json:"interval,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
	Retries     string `json:"retries,omitempty"`
	StartPeriod string `json:"start_period,omitempty"`
}

// applyBlueprintServiceEnv copies a blueprint's static per-service env onto the
// service (e.g. Next.js HOSTNAME=0.0.0.0) so it lands in the service's own
// compose environment.
func applyBlueprintServiceEnv(s *Service, bp blueprints.Blueprint) {
	if len(bp.ServiceEnv) == 0 {
		return
	}
	if s.EnvVars == nil {
		s.EnvVars = map[string]string{}
	}
	for k, v := range bp.ServiceEnv {
		if _, ok := s.EnvVars[k]; !ok {
			s.EnvVars[k] = v
		}
	}
}

// Build is a build service's source spec.
type Build struct {
	Context    string            `json:"context,omitempty"`
	Dockerfile string            `json:"dockerfile,omitempty"`
	Template   string            `json:"template,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// Draft is the detection result the wizard pre-fills from.
type Draft struct {
	Services      []Service         `json:"services"`
	Database      string            `json:"database"`             // none | postgres | mysql
	DBVersion     string            `json:"db_version,omitempty"` // image tag captured from compose (e.g. 16-alpine)
	Redis         bool              `json:"redis"`
	ObjectStorage string            `json:"object_storage,omitempty"` // ""/none | local | minio (detected)
	Detected      string            `json:"detected"`                 // primary stack label, for display
	Notes         []string          `json:"notes"`                    // human-readable detection notes
	EnvVars       map[string]string `json:"env_vars,omitempty"`       // seeded from .env.example for the env's .env
	// ManagedCandidates lists detected containers Rigger CAN manage (postgres/mysql/
	// redis). The default draft above already chose "managed" (dropped the container,
	// set the flag, rebased host refs); each candidate carries the verbatim raw
	// service + the per-env-var rewrites so the wizard can offer "keep your own
	// container" and reverse the managed choice without re-deriving anything.
	ManagedCandidates []ManagedCandidate `json:"managed_candidates,omitempty"`
	// ProfileOmitted lists services skipped because they're gated behind a compose
	// `profiles:` (not started by a default `up`). Surfaced so the user can opt to
	// include them.
	ProfileOmitted []OmittedService `json:"profile_omitted,omitempty"`
	// SeedCandidates lists bundled SQL dumps found in the source (e.g. a CodeCanyon
	// app's database.sql + demo variants). The user picks one in the wizard to import
	// into the managed database on first deploy (see the v3 DB-seed hook). Advisory:
	// detection only finds them; the choice + auto-import toggle live in the UI.
	SeedCandidates []SeedCandidate `json:"seed_candidates,omitempty"`
	// HasPreDeploy is set when the imported compose ALREADY runs a one-shot
	// release/migrate service before the app starts (a service gated on via
	// service_completed_successfully, or a run-once container with a migrate-like
	// command). PreDeployService names it. The UI uses this to advise the user not to
	// also set a Rigger pre-deploy command — composegen likewise won't synthesize a
	// duplicate (see composegen.applyPreDeploy).
	HasPreDeploy     bool   `json:"has_predeploy,omitempty"`
	PreDeployService string `json:"predeploy_service,omitempty"`
}

// SeedCandidate is a bundled SQL dump offered for import into the managed database.
// Path is relative to the source root; Bytes is its on-disk size (used to rank the
// real dump above tiny stubs and to show a human size in the picker).
type SeedCandidate struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// ManagedCandidate is a detected dependency offered as a choice: use Rigger's
// managed service (default), or keep the user's own container from the compose file.
type ManagedCandidate struct {
	Role         string        `json:"role"`          // postgres | mysql | redis
	DetectedName string        `json:"detected_name"` // the compose service name (dns-safe), e.g. "db"
	ManagedName  string        `json:"managed_name"`  // Rigger's managed service name, e.g. "postgres"
	Image        string        `json:"image,omitempty"`
	Tag          string        `json:"tag,omitempty"`
	DBVersion    string        `json:"db_version,omitempty"` // version the managed catalog would use
	RawService   Service       `json:"raw_service"`          // verbatim container (for "keep own")
	Rewrites     []HostRewrite `json:"rewrites,omitempty"`   // host refs the managed choice rewrote (to reverse)
}

// HostRewrite records one env value the managed choice rewrote onto the managed
// service host (e.g. ...@db:5432... → ...@postgres:5432...). Service is "" for an
// env-level (.env) value. Carries both forms so the wizard can flip either way.
type HostRewrite struct {
	Service  string `json:"service"`
	Key      string `json:"key"`
	Original string `json:"original"`
	Managed  string `json:"managed"`
}

// OmittedService is a compose service skipped due to a `profiles:` gate, with the
// full parsed service so the user can opt to include it in services[].
type OmittedService struct {
	Name     string   `json:"name"`
	Profiles []string `json:"profiles"`
	Service  Service  `json:"service"`
}

// Detect scans a repo directory and returns a draft service graph.
func Detect(repoDir string) Draft {
	d := Draft{Services: []Service{}, Database: "none", Notes: []string{}}
	// Seed env vars from .env.example so the env's .env carries the app's expected
	// keys (and ${VAR:-default} refs in compose `environment:` resolve at deploy).
	d.EnvVars = parseDotenv(repoDir)

	// 1. An existing compose file is authoritative.
	if cf := findFirst(repoDir, "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"); cf != "" {
		if fromCompose(repoDir, cf, &d) {
			d.Detected = "docker-compose"
			d.Notes = append([]string{"Detected " + filepath.Base(cf) + " — mapped its services."}, d.Notes...)
			detectManagedDeps(repoDir, &d)
			detectSeedDumps(repoDir, &d)
			return d
		}
	}

	// 2. Dockerfile(s) — root and common monorepo subdirs → one build service each.
	dfDirs := findDockerfileDirs(repoDir)
	if len(dfDirs) > 0 {
		for _, rel := range dfDirs {
			addBuildService(repoDir, rel, &d, true)
		}
		d.Detected = "Dockerfile" + plural(len(dfDirs))
		d.Notes = append(d.Notes, noteCount(len(dfDirs), "Dockerfile"))
	} else if id, ok := identify(repoDir); ok {
		// 3. No Dockerfile — identify the framework from manifests at the root.
		addFrameworkService(repoDir, ".", "app", id, &d)
		if bp, ok := blueprints.Get(id); ok {
			d.Detected = bp.Label
			d.Notes = append(d.Notes, "Detected "+bp.Label+" from project manifests (no Dockerfile — a starter will be scaffolded).")
		}
	} else {
		d.Notes = append(d.Notes, "Couldn't identify a stack automatically — add services manually.")
	}

	// 4. Procfile → worker/scheduler services that reuse the primary app image.
	detectProcfile(repoDir, &d)

	// 5. Managed-dependency hints.
	detectManagedDeps(repoDir, &d)
	// 6. Bundled SQL dumps the user may want to seed the managed DB with.
	detectSeedDumps(repoDir, &d)
	return d
}

// seedDirs are the directories (besides the repo root) where a bundled SQL dump
// is conventionally shipped. seedExcludeDirs are subtrees that hold Laravel
// migration/seeder/factory CODE — small `.sql` is rare there but exclude them so a
// stray fixture never masquerades as the database dump.
var seedDirs = map[string]bool{
	"database": true, "db": true, "sql": true, "install": true,
	"_install": true, "setup": true, "dump": true, "dumps": true,
}
var seedExcludeDirs = map[string]bool{
	"database/migrations": true, "database/factories": true, "database/seeders": true,
	"db/migrations": true,
}

// seedMinBytes is the floor below which a `.sql` is treated as a stub (a single
// migration / fixture), not a real database dump worth offering to import.
const seedMinBytes = 8 * 1024

// detectSeedDumps finds bundled SQL dumps in the source (repo root or a known
// seed dir), excluding migration/seeder code and tiny stubs. CodeCanyon-style apps
// ship the database as e.g. database.sql (+ demo variants); the wizard offers the
// largest as the default import. Pure file reads — never executed.
func detectSeedDumps(repoDir string, d *Draft) {
	var cands []SeedCandidate
	_ = filepath.WalkDir(repoDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relOrEmpty(repoDir, p))
		if rel == "" || rel == "." {
			return nil
		}
		if e.IsDir() {
			switch e.Name() {
			case "vendor", "node_modules", ".git", "tests", "test":
				return fs.SkipDir
			}
			if seedExcludeDirs[rel] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".sql") {
			return nil
		}
		// Location rule: at the repo root, or directly/indirectly under a known seed dir.
		seg := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			seg = rel[:i]
		} else {
			seg = "" // root file (no slash)
		}
		if seg != "" && !seedDirs[seg] {
			return nil
		}
		info, ierr := e.Info()
		if ierr != nil || info.Size() < seedMinBytes {
			return nil
		}
		cands = append(cands, SeedCandidate{Path: rel, Bytes: info.Size()})
		return nil
	})
	if len(cands) == 0 {
		return
	}
	// Largest first (the real dump; demo variants follow), ties broken by path for
	// deterministic ordering.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Bytes != cands[j].Bytes {
			return cands[i].Bytes > cands[j].Bytes
		}
		return cands[i].Path < cands[j].Path
	})
	if len(cands) > 10 {
		cands = cands[:10]
	}
	d.SeedCandidates = cands
	d.Notes = append(d.Notes, fmt.Sprintf(
		"Found %d SQL dump%s — choose one to import into the managed database.", len(cands), plural(len(cands))))
}

// relOrEmpty returns the slash-free relative path of p under base, or "" if it
// can't be computed (defensive — WalkDir paths are always under base).
func relOrEmpty(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return ""
	}
	return rel
}

// ── Compose ──────────────────────────────────────────────────────────────────

type composeFile struct {
	Services map[string]composeSvc `yaml:"services"`
}
type composeSvc struct {
	Image       string    `yaml:"image"`
	Build       yaml.Node `yaml:"build"`
	Command     yaml.Node `yaml:"command"`
	Ports       []string  `yaml:"ports"`
	DependsOn   yaml.Node `yaml:"depends_on"`
	Volumes     []string  `yaml:"volumes"`
	Environment yaml.Node `yaml:"environment"`
	Healthcheck yaml.Node `yaml:"healthcheck"`
	Profiles    []string  `yaml:"profiles"`
	Restart     string    `yaml:"restart"`
}

// DetectComposeBytes parses pasted docker-compose.yml content (no repo on disk) into a
// Draft for the IMAGE-stack importer. foldManaged is FALSE here: the image stack has no
// managed-dependency concept, so EVERY service (postgres/redis included) is kept as a
// plain image entry rather than folded into a managed dep (which would silently drop it).
// Build-context framework ID is skipped (no files to read).
func DetectComposeBytes(data []byte) (Draft, error) {
	d := Draft{Database: "none", ObjectStorage: "none", Detected: "compose (pasted)"}
	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return d, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(cf.Services) == 0 {
		return d, fmt.Errorf("no services found — expected a top-level `services:` map")
	}
	composeIntoDraft(&d, "", cf, false)
	return d, nil
}

func fromCompose(repoDir, path string, d *Draft) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cf composeFile
	if yaml.Unmarshal(raw, &cf) != nil || len(cf.Services) == 0 {
		return false
	}
	composeIntoDraft(d, repoDir, cf, true)
	return len(d.Services) > 0
}

// composeIntoDraft maps a parsed compose file into the draft: managed-dep candidates,
// profile-gated services, app services, host-rebasing, and web-entry selection. Shared
// by fromCompose (repo scan, foldManaged=true) and DetectComposeBytes (pasted content
// for the image stack, foldManaged=false → every service stays a plain image entry).
func composeIntoDraft(d *Draft, repoDir string, cf composeFile, foldManaged bool) {
	var skippedProfiles []string
	// renames maps a dropped DB/cache service's compose name (e.g. "db") to the
	// managed service's name in Rigger's generated compose (e.g. "postgres"), so we
	// can repoint hardcoded host references in other services' env (e.g. a
	// DATABASE_URL of ...@db:5432/...) onto the host that actually resolves.
	renames := map[string]string{}
	for _, name := range sortedKeys(cf.Services) {
		cs := cf.Services[name]
		// Profile-gated services aren't started by a default `docker compose up`
		// (e.g. an optional geocoder). Keep them OUT of the graph by default, but
		// surface the full parsed service so the user can opt to include it. (Image-stack
		// paste keeps everything — the user pasted it, so include it as a plain image.)
		if foldManaged && len(cs.Profiles) > 0 {
			skippedProfiles = append(skippedProfiles, name)
			d.ProfileOmitted = append(d.ProfileOmitted, OmittedService{
				Name: dnsName(name), Profiles: cs.Profiles, Service: composeToService(repoDir, name, cs, cf.Services),
			})
			continue
		}
		// Recognised data services CAN become a managed dependency. Default to that
		// (drop the container, set the flag, capture the version, rebase host refs
		// below), but record the candidate + verbatim service so the wizard can offer
		// "keep your own container" and reverse the choice. Skipped for the image stack
		// (foldManaged=false), which has no managed deps — keep the data service as-is.
		if role := dbRole(cs.Image); foldManaged && role != "" {
			applyManagedDep(role, d)
			img, tag := splitImage(cs.Image)
			if tag == "latest" {
				tag = ""
			}
			if tag != "" && d.DBVersion == "" {
				d.DBVersion = tag
			}
			// The managed service is generated under its role name (postgres/mysql/
			// redis); remember to repoint references to this dropped service's host.
			managed := managedServiceName(role, d)
			if name != managed {
				renames[name] = managed
			}
			d.ManagedCandidates = append(d.ManagedCandidates, ManagedCandidate{
				Role: role, DetectedName: dnsName(name), ManagedName: managed,
				Image: img, Tag: tag, DBVersion: tag,
				RawService: composeToService(repoDir, name, cs, cf.Services),
			})
			continue
		}
		d.Services = append(d.Services, composeToService(repoDir, name, cs, cf.Services))
	}
	if len(skippedProfiles) > 0 {
		d.Notes = append(d.Notes, "Skipped profile-gated service(s): "+strings.Join(skippedProfiles, ", ")+" (not started by default — include them in the review if you want them).")
	}
	// Repoint hardcoded DB/cache host references onto the managed service names.
	if n := rebaseManagedHosts(d, renames); n > 0 {
		var pairs []string
		for old, neu := range renames {
			pairs = append(pairs, old+"→"+neu)
		}
		sort.Strings(pairs)
		d.Notes = append(d.Notes, fmt.Sprintf("Repointed %d host reference(s) onto managed service name(s): %s.", n, strings.Join(pairs, ", ")))
	}
	pickWebEntry(d)
	detectPreDeploy(d)
}

// detectPreDeploy flags an imported compose that ALREADY implements a pre-deploy /
// migrate gate, so the UI can advise against a duplicate and composegen skips synthesis.
// Two signals: a service another service waits on with service_completed_successfully
// (an explicit gate), or a run-once container whose command looks like a migration.
func detectPreDeploy(d *Draft) {
	gated := map[string]bool{}
	for _, s := range d.Services {
		for dep, cond := range s.DependsOnConditions {
			if cond == "service_completed_successfully" {
				gated[dep] = true
			}
		}
	}
	for _, s := range d.Services {
		if gated[s.Name] {
			d.HasPreDeploy, d.PreDeployService = true, s.Name
			return
		}
	}
	for _, s := range d.Services {
		if strings.EqualFold(s.Restart, "no") && looksLikeMigrate(s.Command) {
			d.HasPreDeploy, d.PreDeployService = true, s.Name
			return
		}
	}
}

// looksLikeMigrate reports whether a command resembles a DB migration / release step.
func looksLikeMigrate(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, kw := range []string{"migrate", "migration", "db:push", "liquibase", "flyway", "alembic"} {
		if strings.Contains(c, kw) {
			return true
		}
	}
	return false
}

// composeToService maps one compose service to the unified Service model. Shared by
// the app-service path, the managed-dependency raw service (for "keep own"), and the
// profile-omitted service (for opt-in include). all is the full services map, for
// resolving depends_on. Behaviour for app services is unchanged from the inline
// mapping it replaced.
func composeToService(repoDir, name string, cs composeSvc, all map[string]composeSvc) Service {
	s := Service{Name: dnsName(name), Restart: "unless-stopped", EnvFile: true}
	// Environment (map or list form) — kept literal so ${VAR:-default} still
	// interpolates at deploy against the seeded .env. Copied first so compose
	// values win over any blueprint defaults applied below.
	for k, v := range nodeToEnvMap(cs.Environment) {
		if s.EnvVars == nil {
			s.EnvVars = map[string]string{}
		}
		s.EnvVars[k] = v
	}
	if !cs.Build.IsZero() {
		ctx, dockerfile, args := composeBuild(cs.Build)
		s.Build = &Build{Context: ctx, Dockerfile: dockerfile, Args: args}
		// Identify the framework in the build context so the service carries its
		// blueprint id (build.template) — envgen reads that to emit the framework's
		// env contract. Compose alone doesn't tell us the stack; the manifests do.
		if id, ok := identify(filepath.Join(repoDir, filepath.FromSlash(strings.TrimPrefix(ctx, "./")))); ok {
			s.Build.Template = id
			if bp, ok := blueprints.Get(id); ok {
				applyBlueprintServiceEnv(&s, bp) // fills only keys compose didn't set
			}
		}
	} else if cs.Image != "" {
		s.Image, s.Tag = splitImage(cs.Image)
		s.EnvFile = false
	}
	if c := scalarOrJoin(cs.Command); c != "" {
		s.Command = c
	}
	// Ports: first container port → Port (+HostPort); the rest → ExtraPorts (raw).
	if ports := normalizePorts(cs.Ports); len(ports) > 0 {
		hp, cp := splitPort(ports[0])
		s.Port = cp
		if hp != "" {
			s.HostPort = hp
		}
		s.ExtraPorts = append(s.ExtraPorts, ports[1:]...)
	}
	if hc, hcfg := parseHealthcheck(cs.Healthcheck); hc != "" {
		s.Healthcheck = hc
		s.HealthcheckConfig = hcfg
	}
	s.Volumes = cs.Volumes
	s.DependsOn = filterDeps(nodeToStrings(cs.DependsOn), all)
	s.DependsOnConditions = filterConditions(nodeToConditions(cs.DependsOn), all)
	// Preserve an explicit restart policy (e.g. a one-shot migrate's "no"); default to
	// the long-running policy when unset, as before.
	if cs.Restart != "" {
		s.Restart = cs.Restart
	}
	normalizeUploadedBuild(&s, repoDir)
	return s
}

// normalizeUploadedBuild rewrites a build service whose compose build.context is a
// dev-harness artifact into a clean "build from the repo root with a scaffolded
// Dockerfile". The canonical case is Laravel Sail: an app's docker-compose.yml builds
// from ./vendor/laravel/sail/runtimes/<v>, but CodeCanyon/marketplace apps ship NO
// vendor/, so that context can't build. Triggers when the context is under
// vendor//node_modules/, doesn't exist in the source, or the service is Sail-flagged.
// Also strips Sail's dev-only env and the whole-repo source bind (which would overlay
// the built image with un-built source).
func normalizeUploadedBuild(s *Service, repoDir string) {
	if s.Build == nil {
		return
	}
	// Only rewrite a build context that is clearly a dependency/dev-harness artifact —
	// under vendor//node_modules/, or a Laravel Sail service. A legitimate monorepo
	// subdir context (./api, ./backend) is left untouched.
	ctx := filepath.ToSlash(strings.TrimPrefix(s.Build.Context, "./"))
	underDep := strings.HasPrefix(ctx, "vendor/") || strings.Contains(ctx, "/vendor/") ||
		strings.HasPrefix(ctx, "node_modules/") || strings.Contains(ctx, "/node_modules/")
	sail := s.EnvVars["LARAVEL_SAIL"] != "" || strings.Contains(ctx, "laravel/sail")
	if !(underDep || sail) {
		return
	}
	// Build from the repo root with the detected framework's scaffolded Dockerfile.
	id, _ := identify(repoDir)
	s.Build = &Build{Context: ".", Template: id} // Dockerfile scaffolded at build time
	if bp, ok := blueprints.Get(id); ok {
		applyBlueprintServiceEnv(s, bp)
	}
	for _, k := range []string{"LARAVEL_SAIL", "XDEBUG_MODE", "XDEBUG_CONFIG", "WWWUSER", "WWWGROUP", "IGNITION_LOCAL_SITES_PATH"} {
		delete(s.EnvVars, k)
	}
	// Drop a whole-repo source bind (e.g. ".:/var/www/html") — dev-only; it would
	// overlay the built image with the un-built source.
	var vols []string
	for _, v := range s.Volumes {
		if h := volHostPart(v); h == "." || h == "./" {
			continue
		}
		vols = append(vols, v)
	}
	s.Volumes = vols
}

func volHostPart(v string) string {
	if i := strings.Index(v, ":"); i >= 0 {
		return v[:i]
	}
	return v
}

// ── Dockerfiles ──────────────────────────────────────────────────────────────

// findDockerfileDirs returns repo-relative dirs that contain a Dockerfile: the
// root, then one level of common monorepo parents (apps/*, services/*, …).
func findDockerfileDirs(repoDir string) []string {
	var out []string
	if fileExists(filepath.Join(repoDir, "Dockerfile")) {
		out = append(out, ".")
	}
	for _, parent := range []string{"apps", "services", "packages", "cmd", "src"} {
		entries, err := os.ReadDir(filepath.Join(repoDir, parent))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && fileExists(filepath.Join(repoDir, parent, e.Name(), "Dockerfile")) {
				out = append(out, parent+"/"+e.Name())
			}
		}
	}
	// Also common single-service dirs at the root.
	for _, parent := range []string{"backend", "frontend", "api", "web", "server", "app"} {
		if fileExists(filepath.Join(repoDir, parent, "Dockerfile")) {
			out = append(out, parent)
		}
	}
	return dedup(out)
}

// addBuildService adds a build service for a Dockerfile-bearing dir, enriching
// port/healthcheck/web-routing from the framework identified in that dir.
func addBuildService(repoDir, rel string, d *Draft, fromDockerfile bool) {
	name := serviceNameFor(rel)
	id, _ := identify(filepath.Join(repoDir, rel))
	addFrameworkService(repoDir, rel, name, id, d)
	// Override the port from the Dockerfile's EXPOSE when present.
	if fromDockerfile {
		if p := dockerfileExpose(filepath.Join(repoDir, rel, "Dockerfile")); p != "" {
			d.Services[len(d.Services)-1].Port = p
		}
	}
}

// addFrameworkService appends a build service shaped by the blueprint for id (or
// a generic build service when id is unknown). When the framework needs an nginx
// front (php-fpm), a paired nginx service is added.
func addFrameworkService(repoDir, contextRel, name, id string, d *Draft) {
	bp, ok := blueprints.Get(id)
	s := Service{
		Name:    dnsName(name),
		Build:   &Build{Context: contextRel},
		EnvFile: true,
		Restart: "unless-stopped",
	}
	if id != "" {
		s.Build.Template = id
	}
	if ok {
		s.Port = bp.Port
		s.Healthcheck = bp.Healthcheck
		s.WebRouted = bp.WebRouted && !bp.NeedsNginx
		applyBlueprintServiceEnv(&s, bp)
	} else {
		s.WebRouted = true // unknown framework: assume it serves HTTP; user edits
	}
	d.Services = append(d.Services, s)

	if ok && bp.NeedsNginx {
		d.Services = append(d.Services, Service{
			Name:           dnsName(name + "-nginx"),
			Image:          "nginx",
			Tag:            "1.25-alpine",
			WebRouted:      true,
			Port:           "80",
			DependsOn:      []string{dnsName(name)},
			ConfigTemplate: bp.NginxConf,
			Volumes:        []string{"./nginx.conf:/etc/nginx/conf.d/default.conf:ro"},
			Restart:        "unless-stopped",
		})
		d.Notes = append(d.Notes, bp.Label+" uses PHP-FPM — added an nginx front service.")
	}
}

// ── Procfile (worker/scheduler) ──────────────────────────────────────────────

var procLineRE = regexp.MustCompile(`(?m)^([a-zA-Z0-9_-]+):\s*(.+)$`)

func detectProcfile(repoDir string, d *Draft) {
	raw, err := os.ReadFile(filepath.Join(repoDir, "Procfile"))
	if err != nil {
		return
	}
	app := firstBuildServiceName(d)
	if app == "" {
		return
	}
	for _, m := range procLineRE.FindAllStringSubmatch(string(raw), -1) {
		kind, cmd := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if kind == "web" || kind == "release" {
			continue // web = the app service already; release = a one-off
		}
		d.Services = append(d.Services, Service{
			Name: dnsName(kind), ImageFrom: app, Command: cmd, EnvFile: true, Restart: "unless-stopped",
		})
	}
	if len(procLineRE.FindAllString(string(raw), -1)) > 0 {
		d.Notes = append(d.Notes, "Procfile detected — added worker/scheduler services reusing the app image.")
	}
}

// ── Managed dependencies ─────────────────────────────────────────────────────

func detectManagedDeps(repoDir string, d *Draft) {
	blob := strings.ToLower(readAny(repoDir,
		".env.example", ".env.sample", ".env", "docker-compose.yml", "compose.yaml",
		"composer.json", "package.json", "requirements.txt", "go.mod", "Gemfile", "pom.xml"))
	if d.Database == "none" {
		switch {
		case strings.Contains(blob, "postgres") || strings.Contains(blob, "psycopg") || strings.Contains(blob, "pgx") || strings.Contains(blob, "pg_"):
			d.Database = "postgres"
			d.Notes = append(d.Notes, "Postgres reference found — suggested the Postgres managed dependency.")
		case strings.Contains(blob, "mysql") || strings.Contains(blob, "mariadb"):
			d.Database = "mysql"
			d.Notes = append(d.Notes, "MySQL/MariaDB reference found — suggested the MySQL managed dependency.")
		}
	}
	if !d.Redis && (strings.Contains(blob, "redis") || strings.Contains(blob, "ioredis")) {
		d.Redis = true
		d.Notes = append(d.Notes, "Redis reference found — suggested the Redis managed dependency.")
	}
}

func applyManagedDep(role string, d *Draft) {
	switch role {
	case "postgres", "mysql":
		if d.Database == "none" {
			d.Database = role
		}
	case "redis":
		d.Redis = true
	}
}

// managedServiceName returns the compose service name Rigger generates for a
// managed dependency role, matching internal/composegen (postgres / mysql /
// redis). mysql and mariadb both surface as the "mysql" engine.
func managedServiceName(role string, d *Draft) string {
	switch role {
	case "postgres":
		return "postgres"
	case "mysql":
		if d.Database == "mariadb" {
			return "mariadb"
		}
		return "mysql"
	case "redis":
		return "redis"
	}
	return role
}

// rebaseManagedHosts rewrites references to dropped DB/cache service hostnames
// (renames: oldName→managedName) inside every service's env values and the
// env-level seeded env vars, so e.g. a scanned DATABASE_URL of ...@db:5432/...
// points at the managed service host that actually resolves. Returns the number
// of values changed.
func rebaseManagedHosts(d *Draft, renames map[string]string) int {
	if len(renames) == 0 {
		return 0
	}
	// Deterministic rename order so the recorded rewrites + notes are stable.
	olds := make([]string, 0, len(renames))
	for old := range renames {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	changed := 0
	apply := func(svc string, m map[string]string) {
		for k, v := range m {
			nv := v
			matched := ""
			for _, old := range olds {
				before := nv
				nv = rebaseHost(nv, old, renames[old])
				if nv != before {
					matched = renames[old]
				}
			}
			if nv != v {
				m[k] = nv
				changed++
				// Record so the wizard can reverse this value if the user keeps their
				// own container. Attributed to the managed name that changed it.
				recordRewrite(d, matched, HostRewrite{Service: svc, Key: k, Original: v, Managed: nv})
			}
		}
	}
	for i := range d.Services {
		apply(d.Services[i].Name, d.Services[i].EnvVars)
	}
	apply("", d.EnvVars) // env-level (.env) values — Service "" marks them
	return changed
}

// recordRewrite attaches a host rewrite to the candidate whose managed service name
// caused it, so the wizard's "keep own" reversal can restore the original value.
func recordRewrite(d *Draft, managedName string, r HostRewrite) {
	for i := range d.ManagedCandidates {
		if d.ManagedCandidates[i].ManagedName == managedName {
			d.ManagedCandidates[i].Rewrites = append(d.ManagedCandidates[i].Rewrites, r)
			return
		}
	}
}

// rebaseHost rewrites the host token old→neu inside an env value: a bare host
// value, or the host position of a URL/DSN (after "@" or "//"). Substrings of
// longer hostnames are left untouched.
func rebaseHost(v, old, neu string) string {
	if old == "" || old == neu {
		return v
	}
	if v == old {
		return neu
	}
	for _, sep := range []string{"@", "//"} {
		v = strings.ReplaceAll(v, sep+old+":", sep+neu+":")
		v = strings.ReplaceAll(v, sep+old+"/", sep+neu+"/")
	}
	return v
}

// dbRole classifies a compose service image as a managed dependency, or "".
func dbRole(image string) string {
	l := strings.ToLower(image)
	switch {
	case strings.Contains(l, "postgres"):
		return "postgres"
	case strings.Contains(l, "mysql"), strings.Contains(l, "mariadb"):
		return "mysql"
	case strings.Contains(l, "redis"):
		return "redis"
	}
	return ""
}

// ── Framework identification ─────────────────────────────────────────────────

func identify(dir string) (string, bool) {
	switch {
	case fileExists(filepath.Join(dir, "artisan")) || (fileExists(filepath.Join(dir, "composer.json")) && strings.Contains(readFile(filepath.Join(dir, "composer.json")), "laravel")):
		return "laravel", true
	case fileExists(filepath.Join(dir, "package.json")):
		pkg := strings.ToLower(readFile(filepath.Join(dir, "package.json")))
		switch {
		case strings.Contains(pkg, `"next"`):
			return "nextjs", true
		case (strings.Contains(pkg, "vite") || strings.Contains(pkg, "react-scripts") || strings.Contains(pkg, "@angular/core") || strings.Contains(pkg, `"vue"`)) &&
			!strings.Contains(pkg, "express") && !strings.Contains(pkg, "fastify") && !strings.Contains(pkg, "@nestjs"):
			return "static", true
		default:
			return "nodejs", true
		}
	case fileExists(filepath.Join(dir, "pom.xml")) || fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")):
		return "spring", true
	case fileExists(filepath.Join(dir, "manage.py")) || fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "requirements.txt")):
		return "django", true
	case fileExists(filepath.Join(dir, "go.mod")):
		return "go", true
	case globExists(dir, "*.csproj") || globExists(dir, "*.sln"):
		return "dotnet", true
	case fileExists(filepath.Join(dir, "Gemfile")):
		return "rails", true
	}
	return "", false
}

// ── helpers ──────────────────────────────────────────────────────────────────

var exposeRE = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

func dockerfileExpose(path string) string {
	if m := exposeRE.FindStringSubmatch(readFile(path)); m != nil {
		return m[1]
	}
	return ""
}

func serviceNameFor(rel string) string {
	if rel == "." || rel == "" {
		return "app"
	}
	return filepath.Base(rel)
}

func firstBuildServiceName(d *Draft) string {
	for _, s := range d.Services {
		if s.Build != nil {
			return s.Name
		}
	}
	return ""
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

func dnsName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	s = nonDNS.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "svc"
	}
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	return s
}

func splitImage(ref string) (image, tag string) {
	// Split on the last colon that isn't part of a registry host:port.
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func firstPort(ports []string) (host, container string) {
	for _, p := range ports {
		p = strings.Trim(p, `"' `)
		parts := strings.Split(p, ":")
		switch len(parts) {
		case 1:
			return "", parts[0]
		case 2:
			return parts[0], parts[1]
		default: // ip:host:container
			return parts[len(parts)-2], parts[len(parts)-1]
		}
	}
	return "", ""
}

// composeBuild extracts context/dockerfile/args from a compose `build:` node, which
// is either a scalar ("./api") or a map ({context, dockerfile, args}).
func composeBuild(n yaml.Node) (context, dockerfile string, args map[string]string) {
	if n.Kind == yaml.ScalarNode {
		return n.Value, "", nil
	}
	var m struct {
		Context    string    `yaml:"context"`
		Dockerfile string    `yaml:"dockerfile"`
		Args       yaml.Node `yaml:"args"`
	}
	_ = n.Decode(&m)
	context = m.Context
	if context == "" {
		context = "."
	}
	if a := nodeToEnvMap(m.Args); len(a) > 0 {
		args = a
	}
	return context, m.Dockerfile, args
}

// nodeToEnvMap parses a compose `environment:`/build `args:` node — either map form
// (KEY: value) or list form (- KEY=value) — into a string map. Non-string scalars
// (numbers, bools) are stringified; values are kept literal (incl. ${VAR:-default}).
func nodeToEnvMap(n yaml.Node) map[string]string {
	out := map[string]string{}
	if n.IsZero() {
		return out
	}
	var m map[string]interface{}
	if n.Decode(&m) == nil && len(m) > 0 {
		for k, v := range m {
			if v == nil {
				out[k] = ""
			} else {
				out[k] = fmt.Sprintf("%v", v)
			}
		}
		return out
	}
	var list []string
	if n.Decode(&list) == nil {
		for _, item := range list {
			if i := strings.IndexByte(item, '='); i > 0 {
				out[strings.TrimSpace(item[:i])] = item[i+1:]
			} else if t := strings.TrimSpace(item); t != "" {
				out[t] = ""
			}
		}
	}
	return out
}

// parseHealthcheck maps a compose healthcheck node to the bare test command (which
// composegen re-wraps in CMD-SHELL) plus its timing. Returns "" when absent/disabled.
func parseHealthcheck(n yaml.Node) (string, *HealthcheckConfig) {
	if n.IsZero() {
		return "", nil
	}
	var hc struct {
		Test        yaml.Node `yaml:"test"`
		Interval    string    `yaml:"interval"`
		Timeout     string    `yaml:"timeout"`
		Retries     int       `yaml:"retries"`
		StartPeriod string    `yaml:"start_period"`
		Disable     bool      `yaml:"disable"`
	}
	if n.Decode(&hc) != nil || hc.Disable {
		return "", nil
	}
	test := healthcheckTest(hc.Test)
	if test == "" {
		return "", nil
	}
	cfg := &HealthcheckConfig{Interval: hc.Interval, Timeout: hc.Timeout, StartPeriod: hc.StartPeriod}
	if hc.Retries > 0 {
		cfg.Retries = itoa(hc.Retries)
	}
	return test, cfg
}

// healthcheckTest unwraps the test field: a scalar string, or a list like
// ["CMD-SHELL", "<cmd>"] / ["CMD", "<bin>", "<arg>"…]. "NONE" disables it.
func healthcheckTest(n yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var list []string
	if n.Decode(&list) == nil && len(list) > 0 {
		switch list[0] {
		case "NONE":
			return ""
		case "CMD-SHELL", "CMD":
			if len(list) > 1 {
				return strings.Join(list[1:], " ")
			}
		}
		return strings.Join(list, " ")
	}
	return ""
}

// normalizePorts trims quotes/whitespace and drops empties.
func normalizePorts(ports []string) []string {
	var out []string
	for _, p := range ports {
		if p = strings.Trim(p, `"' `); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitPort returns (hostPort, containerPort) from "h:c", "ip:h:c", or "c". It splits
// only on colons OUTSIDE ${...}, so a compose port spec with an env-var default
// (e.g. "${APP_PORT:-80}:80") isn't mangled by the colon inside ${...}.
func splitPort(p string) (host, container string) {
	parts := splitColonOutsideBraces(strings.Trim(p, `"' `))
	switch len(parts) {
	case 1:
		return "", parts[0]
	case 2:
		return parts[0], parts[1]
	default:
		return parts[len(parts)-2], parts[len(parts)-1]
	}
}

// splitColonOutsideBraces splits s on ':' but ignores colons inside ${...} — compose
// port/value specs use env-var defaults like ${APP_PORT:-80} whose inner colon must
// not be treated as a host:container break.
func splitColonOutsideBraces(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

var ingressImageRE = regexp.MustCompile(`nginx|caddy|traefik|httpd|haproxy`)

// pickWebEntry chooses a web entry when none was already marked (compose imports
// rarely set one): the service publishing 80/443, else an ingress-like image, else
// the first build service. The user can change it in the wizard / Edit Project.
func pickWebEntry(d *Draft) {
	for _, s := range d.Services {
		if s.WebRouted {
			return
		}
	}
	idx := -1
	for i, s := range d.Services {
		if portsInclude(s, "80") || portsInclude(s, "443") {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i, s := range d.Services {
			if s.Image != "" && ingressImageRE.MatchString(strings.ToLower(s.Image)) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		for i, s := range d.Services {
			if s.Build != nil {
				idx = i
				break
			}
		}
	}
	if idx >= 0 {
		d.Services[idx].WebRouted = true
		if d.Services[idx].Port == "" {
			d.Services[idx].Port = "80"
		}
		d.Notes = append(d.Notes, "Set "+d.Services[idx].Name+" as the web entry — change it on the next step if wrong.")
	}
}

func portsInclude(s Service, container string) bool {
	if s.Port == container {
		return true
	}
	for _, e := range s.ExtraPorts {
		if _, c := splitPort(e); c == container {
			return true
		}
	}
	return false
}

// parseDotenv reads .env.example / .env.sample into a KEY→VALUE map (best-effort)
// to seed the environment's .env so ${VAR} refs resolve and secret-fill applies.
func parseDotenv(repoDir string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(readFile(findFirst(repoDir, ".env.example", ".env.sample")), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if i := strings.IndexByte(line, '='); i > 0 {
			k := strings.TrimSpace(line[:i])
			if k != "" {
				out[k] = parseDotenvValue(line[i+1:])
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseDotenvValue extracts the value from the RHS of a `KEY=` line, handling the two
// dotenv shapes so an inline comment never leaks into the value (which would then get
// double-quoted downstream when it picks up the comment's spaces):
//   - Quoted ("…" or '…'): the value is the quoted content; anything after the closing
//     quote (e.g. a trailing comment) is discarded, and a '#' inside the quotes is literal.
//   - Unquoted: an inline comment begins at a '#' that FOLLOWS whitespace — cut there. A
//     '#' with no leading space stays part of the value (e.g. a URL fragment).
func parseDotenvValue(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if q := s[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(s[1:], q); end >= 0 {
			return s[1 : 1+end]
		}
		return s[1:] // unterminated quote — best-effort: drop the opening quote
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func scalarOrJoin(n yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var list []string
	if n.Decode(&list) == nil {
		return strings.Join(list, " ")
	}
	return ""
}

func nodeToStrings(n yaml.Node) []string {
	var list []string
	if n.Decode(&list) == nil {
		return list
	}
	var m map[string]any
	if n.Decode(&m) == nil {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	return nil
}

// filterDeps drops compose dependencies that map to managed deps and resolves to
// dns-safe names of app services that survived.
func filterDeps(deps []string, all map[string]composeSvc) []string {
	var out []string
	for _, dep := range deps {
		if cs, ok := all[dep]; ok && dbRole(cs.Image) != "" {
			continue // managed dep — referenced via env, not depends_on here
		}
		out = append(out, dnsName(dep))
	}
	return out
}

// nodeToConditions parses the long-form depends_on map
// (`{ dep: { condition: service_healthy } }`) into dep→condition. The short list form
// has no conditions and decodes to nil here.
func nodeToConditions(n yaml.Node) map[string]string {
	var m map[string]struct {
		Condition string `yaml:"condition"`
	}
	if n.Decode(&m) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		if v.Condition != "" {
			out[k] = v.Condition
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// filterConditions mirrors filterDeps for the condition map: drop managed deps (which
// leave depends_on entirely) and dns-name the surviving keys.
func filterConditions(conds map[string]string, all map[string]composeSvc) map[string]string {
	if len(conds) == 0 {
		return nil
	}
	out := map[string]string{}
	for dep, cond := range conds {
		if cs, ok := all[dep]; ok && dbRole(cs.Image) != "" {
			continue
		}
		out[dnsName(dep)] = cond
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func findFirst(dir string, names ...string) string {
	for _, n := range names {
		if p := filepath.Join(dir, n); fileExists(p) {
			return p
		}
	}
	return ""
}

func readAny(dir string, names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(readFile(filepath.Join(dir, n)))
		b.WriteByte('\n')
	}
	return b.String()
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func globExists(dir, pattern string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(m) > 0
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(m map[string]composeSvc) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort to avoid importing sort for one use is overkill; use sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func noteCount(n int, what string) string {
	if n == 1 {
		return "Detected a " + what + " — added a build service."
	}
	return "Detected " + itoa(n) + " " + what + "s — added a build service each (monorepo)."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
