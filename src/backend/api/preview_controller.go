package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/previews"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// PreviewEvent is the normalized PR event the controller acts on. The Phase 3
// webhook receiver parses a provider payload into this; the controller is
// provider-agnostic from here on.
type PreviewEvent struct {
	Action   string // opened | reopened | synchronize | closed
	PRNumber int
	Branch   string // PR head ref
	SHA      string // PR head sha
	IsFork   bool   // PR opened from a fork of the repo
}

// Guard outcomes that aren't failures — a caller (and Phase 3's receiver) may
// treat these as "intentionally skipped" rather than errors.
var (
	errPreviewBranchFiltered = errors.New("branch does not match the preview branch filter")
	errPreviewForkBlocked    = errors.New("fork PRs are not auto-deployed (set auto_deploy_forks)")
	errPreviewCapReached     = errors.New("preview concurrency limit reached")
)

// previewEnvKey is the env key for a PR. The suffix is hyphen-free (pr42, not
// pr-42) so the flat routing label {ws}-{proj}-pr42 stays a single, unambiguous
// DNS label under the wildcard cert (design §4.4).
func previewEnvKey(prNumber int) string { return fmt.Sprintf("pr%d", prNumber) }

// previewExpiry returns the epoch deadline for a preview given a TTL in hours,
// or 0 (no TTL — lives until the PR closes) when ttlHours <= 0.
func previewExpiry(ttlHours int, now int64) int64 {
	if ttlHours <= 0 {
		return 0
	}
	return now + int64(ttlHours)*3600
}

// previewBranchAllowed reports whether a branch passes the optional glob filter
// ("" = allow all). Uses path.Match semantics (e.g. "feature/*").
func previewBranchAllowed(filter, branch string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	ok, err := path.Match(filter, branch)
	return err == nil && ok
}

// previewForkAllowed reports whether a fork PR may auto-deploy under the policy.
// Non-fork PRs are always allowed. For forks: "on" deploys; "off"/""/"approved"
// do not auto-deploy ("approved" requires a manual approval step, added later).
func previewForkAllowed(policy string, isFork bool) bool {
	if !isFork {
		return true
	}
	return policy == "on"
}

// trimSpace is a tiny local helper to avoid importing strings just for this.
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// DispatchPreviewEvent routes a normalized PR event to the create/update/teardown
// lifecycle, applying the per-project guards. cfg is the project's PreviewConfig
// (the caller has already confirmed it's enabled). This runs synchronously and may
// take minutes (a build) — the webhook receiver (Phase 3) calls it in a goroutine.
func (h *Handler) DispatchPreviewEvent(ws, proj string, cfg *wsconfig.PreviewConfig, ev PreviewEvent) error {
	switch ev.Action {
	case "opened", "reopened", "synchronize":
		existing, _ := previews.GetByPR(h.db, ws, proj, ev.PRNumber)
		if existing != nil && existing.Status != previews.StatusTornDown {
			// Already have a live preview for this PR → redeploy in place.
			_, err := h.updatePreview(ws, proj, cfg, existing, ev.Branch, ev.SHA)
			return err
		}
		// New preview. Apply guards before doing any work.
		if !previewBranchAllowed(cfg.BranchFilter, ev.Branch) {
			return errPreviewBranchFiltered
		}
		if !previewForkAllowed(cfg.AutoDeployForks, ev.IsFork) {
			return errPreviewForkBlocked
		}
		if cfg.MaxConcurrent > 0 {
			if n, _ := previews.CountActive(h.db, ws, proj); n >= cfg.MaxConcurrent {
				return errPreviewCapReached
			}
		}
		// A stale torn_down row would collide with the unique (ws,proj,pr) index.
		if existing != nil {
			previews.Delete(h.db, existing.ID) //nolint:errcheck
		}
		_, err := h.createPreview(ws, proj, cfg, ev.PRNumber, ev.Branch, ev.SHA)
		return err
	case "closed":
		existing, _ := previews.GetByPR(h.db, ws, proj, ev.PRNumber)
		if existing == nil {
			return nil // nothing to tear down (never built, or already gone)
		}
		return h.teardownPreview(ws, proj, existing)
	}
	return nil
}

// createPreview clones the template env into pr{n} (branch override + fresh
// secrets), records a preview row, then deploys (build+up). On deploy failure the
// row is marked failed but kept (env left up for debugging). Returns the row.
func (h *Handler) createPreview(ws, proj string, cfg *wsconfig.PreviewConfig, prNumber int, branch, sha string) (*previews.PreviewEnv, error) {
	env := previewEnvKey(prNumber)
	if err := h.cloneEnv(ws, proj, cfg.TemplateEnv, env, true, branch); err != nil {
		return nil, fmt.Errorf("clone %s→%s: %w", cfg.TemplateEnv, env, err)
	}
	now := time.Now().Unix()
	row, err := previews.Create(h.db, previews.PreviewEnv{
		Workspace: ws, Project: proj, PRNumber: prNumber, Provider: cfg.Provider,
		Branch: branch, HeadSHA: sha, EnvKey: env, Status: previews.StatusCreating,
		URL: h.previewURL(ws, proj, env), CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	if derr := h.deployPreview(ws, proj, env); derr != nil {
		row.Status = previews.StatusFailed
		previews.Update(h.db, *row) //nolint:errcheck
		h.writeBackPreview(ws, proj, row, previews.StatusFailed)
		return row, fmt.Errorf("deploy %s: %w", env, derr)
	}
	now = time.Now().Unix()
	row.Status = previews.StatusRunning
	row.LastDeployedAt = now
	row.ExpiresAt = previewExpiry(cfg.TTLHours, now)
	row.URL = h.previewURL(ws, proj, env)
	previews.Update(h.db, *row) //nolint:errcheck
	h.writeBackPreview(ws, proj, row, previews.StatusRunning)
	return row, nil
}

// updatePreview redeploys an existing preview for a new commit (synchronize): it
// re-points the env's git branch (in case the head ref changed), rebuilds + brings
// it up, and slides the TTL window. The per-PR DB volume survives — a redeploy is
// up, not down -v — so data persists across commits within the PR (design §4.3a).
func (h *Handler) updatePreview(ws, proj string, cfg *wsconfig.PreviewConfig, row *previews.PreviewEnv, branch, sha string) (*previews.PreviewEnv, error) {
	row.Status = previews.StatusUpdating
	row.Branch = branch
	row.HeadSHA = sha
	previews.Update(h.db, *row) //nolint:errcheck

	if branch != "" {
		if err := h.setEnvBranch(ws, proj, row.EnvKey, branch); err != nil {
			// Non-fatal: build still uses whatever branch is already in config.
			fmt.Fprintf(os.Stderr, "updatePreview: set branch %s/%s/%s: %v\n", ws, proj, row.EnvKey, err)
		}
	}
	if derr := h.deployPreview(ws, proj, row.EnvKey); derr != nil {
		row.Status = previews.StatusFailed
		previews.Update(h.db, *row) //nolint:errcheck
		h.writeBackPreview(ws, proj, row, previews.StatusFailed)
		return row, fmt.Errorf("redeploy %s: %w", row.EnvKey, derr)
	}
	now := time.Now().Unix()
	row.Status = previews.StatusRunning
	row.LastDeployedAt = now
	row.ExpiresAt = previewExpiry(cfg.TTLHours, now)
	row.URL = h.previewURL(ws, proj, row.EnvKey)
	previews.Update(h.db, *row) //nolint:errcheck
	h.writeBackPreview(ws, proj, row, previews.StatusRunning)
	return row, nil
}

// teardownPreview brings the preview's stack down (purging its per-PR volumes),
// removes the env directory AND its config.json entry (the env-list-union invariant
// requires both — leaving either keeps a ghost env on the workspace page), then
// deletes the preview row. Used on PR close and by the TTL reaper.
func (h *Handler) teardownPreview(ws, proj string, row *previews.PreviewEnv) error {
	// down + purge volumes + remove dir, while config.json still resolves the env.
	h.removeEnv(ws, proj, row.EnvKey, true)
	// Then drop the env from config.json so it doesn't linger via the union.
	if err := h.removeEnvFromConfig(ws, proj, row.EnvKey); err != nil {
		fmt.Fprintf(os.Stderr, "teardownPreview: drop env from config %s/%s/%s: %v\n", ws, proj, row.EnvKey, err)
	}
	h.writeBackPreview(ws, proj, row, previews.StatusTornDown)
	return previews.Delete(h.db, row.ID)
}

// writeBackPreview posts the preview's URL/status back to the PR (GitHub commit
// status + a single upserted comment) when the project has write-back enabled and
// a token configured. Best-effort and fully async — never affects the lifecycle.
// A copy of row is captured so the caller may keep mutating the original.
func (h *Handler) writeBackPreview(ws, proj string, row *previews.PreviewEnv, status string) {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, proj))
	if err != nil || cfg.Project.Preview == nil || !cfg.Project.Preview.WriteBack {
		return
	}
	repo, ok := previews.ParseGitHubRepo(cfg.Project.GitRepo)
	if !ok {
		return // non-GitHub remote — write-back unsupported in v1
	}
	enc, _ := previews.GetWritebackTokenEnc(h.db, ws, proj)
	if enc == "" {
		return
	}
	tokBytes, derr := crypto.Decrypt(h.cryptoKey, enc)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "writeBackPreview: decrypt token %s/%s: %v\n", ws, proj, derr)
		return
	}
	wb := previews.NewGitHubWriteBack(string(tokBytes), repo)
	r := *row // snapshot
	go func() {
		desc := "Rigger preview"
		if status == previews.StatusFailed {
			desc = "Preview deploy failed"
		}
		if serr := wb.PostCommitStatus(r.HeadSHA, previews.GitHubCommitState(status), r.URL, desc); serr != nil {
			fmt.Fprintf(os.Stderr, "writeBackPreview: status %s/%s PR#%d: %v\n", ws, proj, r.PRNumber, serr)
		}
		var comment string
		switch status {
		case previews.StatusRunning:
			if r.URL != "" {
				comment = fmt.Sprintf("🔎 **Preview environment** is live: %s", r.URL)
			}
		case previews.StatusFailed:
			comment = "⚠️ Preview environment deploy failed — see Rigger for logs."
		case previews.StatusTornDown:
			comment = "🧹 Preview environment torn down."
		}
		if comment != "" {
			if cerr := wb.UpsertPRComment(r.PRNumber, comment); cerr != nil {
				fmt.Fprintf(os.Stderr, "writeBackPreview: comment %s/%s PR#%d: %v\n", ws, proj, r.PRNumber, cerr)
			}
		}
	}()
}

// deployPreview runs the standard build+up for a preview env: build (custom/
// buildable projects only — the build clones the PR branch into _src), then start
// (compose up -d). Image-stack projects skip the build and let compose pull.
func (h *Handler) deployPreview(ws, proj, env string) error {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, proj))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.ProjectType() != "image" {
		var bout bytes.Buffer
		if berr := h.bridge.Run(shell.RunOptions{Workspace: ws, Project: proj, Command: "build", Env: env, Stdout: &bout, Stderr: &bout}); berr != nil {
			return fmt.Errorf("build: %v\n%s", berr, tailStr(bout.String(), 4000))
		}
	}
	var sout bytes.Buffer
	if serr := h.bridge.Run(shell.RunOptions{Workspace: ws, Project: proj, Command: "start", Env: env, Stdout: &sout, Stderr: &sout}); serr != nil {
		return fmt.Errorf("start: %v\n%s", serr, tailStr(sout.String(), 4000))
	}
	return nil
}

// previewURL resolves the public URL a preview env routes to (best-effort; "" when
// it host-binds or has no route), using the same logic the deploy + UI use.
func (h *Handler) previewURL(ws, proj, env string) string {
	data, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, ws, proj))
	if err != nil {
		return ""
	}
	base := settings.EffectiveBaseDomain(h.db, ws)
	if url, ok := composegen.EnvRouteURL(data, env, base, settings.AutoURLMode(h.db), settings.AutoURLHost(h.db)); ok {
		return url
	}
	return ""
}

// setEnvBranch rewrites a single env's per-env git.branch override in config.json
// (raw-JSON edit, preserving every other field).
func (h *Handler) setEnvBranch(ws, proj, env, branch string) error {
	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, proj)
	root, envs, err := readConfigEnvs(cfgPath)
	if err != nil {
		return err
	}
	raw, ok := envs[env]
	if !ok {
		return fmt.Errorf("env %q not found", env)
	}
	var em map[string]any
	if err := json.Unmarshal(raw, &em); err != nil {
		return err
	}
	em["git"] = map[string]any{"branch": branch}
	nb, err := json.Marshal(em)
	if err != nil {
		return err
	}
	envs[env] = nb
	return writeConfigEnvs(cfgPath, root, envs)
}

// removeEnvFromConfig deletes an env entry from config.json (raw-JSON edit). No-op
// when the env is already absent.
func (h *Handler) removeEnvFromConfig(ws, proj, env string) error {
	cfgPath := wspath.ConfigPath(h.workspacesDir, ws, proj)
	root, envs, err := readConfigEnvs(cfgPath)
	if err != nil {
		return err
	}
	if _, ok := envs[env]; !ok {
		return nil
	}
	delete(envs, env)
	return writeConfigEnvs(cfgPath, root, envs)
}

// readConfigEnvs reads config.json and returns the raw root map plus its
// environments sub-map (never nil), so callers can edit a single env verbatim.
func readConfigEnvs(cfgPath string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}
	var envs map[string]json.RawMessage
	if len(root["environments"]) > 0 {
		json.Unmarshal(root["environments"], &envs) //nolint:errcheck
	}
	if envs == nil {
		envs = map[string]json.RawMessage{}
	}
	return root, envs, nil
}

// writeConfigEnvs writes the environments sub-map back into root and persists it.
func writeConfigEnvs(cfgPath string, root, envs map[string]json.RawMessage) error {
	envsOut, err := json.Marshal(envs)
	if err != nil {
		return err
	}
	root["environments"] = envsOut
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}

// tailStr caps a (possibly large) build/deploy log to its last n bytes for an
// error message.
func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
