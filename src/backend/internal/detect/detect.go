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
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/blueprints"
	yaml "go.yaml.in/yaml/v3"
)

// Service mirrors a config.json services[] entry (the unified model).
type Service struct {
	Name           string   `json:"name"`
	Build          *Build   `json:"build,omitempty"`
	Image          string   `json:"image,omitempty"`
	ImageFrom      string   `json:"image_from,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	Command        string   `json:"command,omitempty"`
	Port           string   `json:"port,omitempty"`
	HostPort       string   `json:"host_port,omitempty"`
	WebRouted      bool     `json:"web_routed,omitempty"`
	Subdomain      string   `json:"subdomain,omitempty"`
	Healthcheck    string   `json:"healthcheck,omitempty"`
	EnvFile        bool     `json:"env_file,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Volumes        []string `json:"volumes,omitempty"`
	Restart        string   `json:"restart,omitempty"`
	ConfigTemplate string   `json:"config_template,omitempty"`
}

// Build is a build service's source spec.
type Build struct {
	Context    string `json:"context,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Template   string `json:"template,omitempty"`
}

// Draft is the detection result the wizard pre-fills from.
type Draft struct {
	Services []Service `json:"services"`
	Database string    `json:"database"` // none | postgres | mysql
	Redis    bool      `json:"redis"`
	Garage   bool      `json:"garage"`
	Detected string    `json:"detected"` // primary stack label, for display
	Notes    []string  `json:"notes"`    // human-readable detection notes
}

// Detect scans a repo directory and returns a draft service graph.
func Detect(repoDir string) Draft {
	d := Draft{Services: []Service{}, Database: "none", Notes: []string{}}

	// 1. An existing compose file is authoritative.
	if cf := findFirst(repoDir, "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"); cf != "" {
		if fromCompose(repoDir, cf, &d) {
			d.Detected = "docker-compose"
			d.Notes = append([]string{"Detected " + filepath.Base(cf) + " — mapped its services."}, d.Notes...)
			detectManagedDeps(repoDir, &d)
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
	return d
}

// ── Compose ──────────────────────────────────────────────────────────────────

type composeFile struct {
	Services map[string]composeSvc `yaml:"services"`
}
type composeSvc struct {
	Image     string    `yaml:"image"`
	Build     yaml.Node `yaml:"build"`
	Command   yaml.Node `yaml:"command"`
	Ports     []string  `yaml:"ports"`
	DependsOn yaml.Node `yaml:"depends_on"`
	Volumes   []string  `yaml:"volumes"`
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
	for _, name := range sortedKeys(cf.Services) {
		cs := cf.Services[name]
		// Recognised data services become managed-dependency toggles, not services.
		if role := dbRole(cs.Image); role != "" {
			applyManagedDep(role, d)
			continue
		}
		s := Service{Name: dnsName(name), Restart: "unless-stopped", EnvFile: true}
		if !cs.Build.IsZero() {
			ctx := composeBuildContext(cs.Build)
			s.Build = &Build{Context: ctx}
			// Identify the framework in the build context so the service carries
			// its blueprint id (build.template) — envgen reads that to emit the
			// framework's env contract (DB_*/DATABASE_URL/…). Compose alone
			// doesn't tell us the stack; the manifests in the context dir do.
			if id, ok := identify(filepath.Join(repoDir, filepath.FromSlash(strings.TrimPrefix(ctx, "./")))); ok {
				s.Build.Template = id
			}
		} else if cs.Image != "" {
			s.Image, s.Tag = splitImage(cs.Image)
			s.EnvFile = false
		}
		if c := scalarOrJoin(cs.Command); c != "" {
			s.Command = c
		}
		if hp, cp := firstPort(cs.Ports); cp != "" {
			s.Port = cp
			if hp != "" {
				s.HostPort = hp
			}
		}
		s.DependsOn = filterDeps(nodeToStrings(cs.DependsOn), cf.Services)
		d.Services = append(d.Services, s)
	}
	return len(d.Services) > 0
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

func composeBuildContext(n yaml.Node) string {
	// build: "." (scalar) or build: { context: "./api" }
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var m struct {
		Context string `yaml:"context"`
	}
	_ = n.Decode(&m)
	if m.Context == "" {
		return "."
	}
	return m.Context
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
