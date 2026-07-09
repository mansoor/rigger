package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/scaffold"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// sendWriter adapts a send(string) callback to io.Writer so streamed git/scaffold
// output can flow through the create WebSocket. Used by CreateWorkspace.
type sendWriter struct{ send func(string) }

func (s sendWriter) Write(p []byte) (int, error) { s.send(string(p)); return len(p), nil }

// scaffoldInfoResp describes a blueprint project's generated starter for the post-create
// panel: what was generated, whether it was pushed, and the clone/dev commands.
type scaffoldInfoResp struct {
	Framework    string `json:"framework"`
	Available    bool   `json:"available"`     // a _scaffold dir exists (zip is downloadable)
	GitRepo      string `json:"git_repo"`      // "" when not pushed
	Pushed       bool   `json:"pushed"`        // repo configured → scaffold was pushed to it
	CloneCmd     string `json:"clone_cmd"`     // "" when not pushed
	InstallCmd   string `json:"install_cmd"`
	RunCmd       string `json:"run_cmd"`
}

// ScaffoldInfo — GET /api/workspaces/{workspace}/projects/{name}/scaffold-info.
// Returns the starter-code metadata + local-dev instructions for a scaffolded project.
func (h *Handler) ScaffoldInfo(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	framework := ""
	for _, svc := range cfg.BuildServices() {
		if svc.Build != nil && svc.Build.Template != "" {
			framework = svc.Build.Template
			break
		}
	}
	ins := scaffold.Instructions(framework)
	scaffDir := wspath.ScaffoldDir(h.workspacesDir, ws, name)
	available := false
	if info, serr := os.Stat(scaffDir); serr == nil && info.IsDir() {
		available = true
	}
	repo := strings.TrimSpace(cfg.SourceRepo())
	resp := scaffoldInfoResp{
		Framework:  framework,
		Available:  available,
		GitRepo:    repo,
		Pushed:     repo != "" && available,
		InstallCmd: ins.Install,
		RunCmd:     ins.Run,
	}
	if resp.Pushed {
		resp.CloneCmd = "git clone " + repo
	}
	writeJSON(w, http.StatusOK, resp)
}

// ScaffoldZip — GET /api/workspaces/{workspace}/projects/{name}/scaffold.zip.
// Streams the generated starter tree as a zip (excluding any .git/), so the developer
// can grab the code even without a git provider configured.
func (h *Handler) ScaffoldZip(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("workspace"), r.PathValue("name")
	if !auth.AtLeast(h.pipelineRole(r, ws, name), auth.RoleViewer) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	scaffDir := wspath.ScaffoldDir(h.workspacesDir, ws, name)
	info, err := os.Stat(scaffDir)
	if err != nil || !info.IsDir() {
		http.Error(w, "no scaffold for this project", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-scaffold.zip"`, name))

	zw := zip.NewWriter(w)
	defer zw.Close()
	// Walk the tree; skip .git/ (a push leaves one behind) and non-regular files.
	_ = filepath.WalkDir(scaffDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(scaffDir, path)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil // skip unreadable file, keep going
		}
		defer f.Close()
		zf, cerr := zw.Create(rel)
		if cerr != nil {
			return nil
		}
		_, _ = io.Copy(zf, f)
		return nil
	})
}
