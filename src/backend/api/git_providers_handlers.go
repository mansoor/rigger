package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/gitproviders"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Workspace-scoped Git provider connections (Phase 12) — the credentials Rigger
// uses to clone PRIVATE repos. Mirrors the workspace Docker-registries handlers:
// a workspace sees its own providers (owner_scope='ws:{key}') plus any global one
// granted to it, and may edit/delete only its own. Secrets are write-only: they are
// stored encrypted (internal/crypto) and never returned.

func wsGitProviderID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("gpid"), 10, 64)
}

// ownsGitProvider loads a provider and reports whether it is private to the workspace.
func (h *Handler) ownsGitProvider(wsKey string, id int64) (*gitproviders.Provider, bool) {
	p, err := gitproviders.Get(h.db, h.cryptoKey, id)
	if err != nil || p == nil {
		return nil, false
	}
	return p, p.OwnerScope == gitproviders.WorkspaceScope(wsKey)
}

// GET /api/workspaces/{workspace}/git-providers — the workspace's provider pool.
func (h *Handler) ListWorkspaceGitProviders(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	ps, err := gitproviders.ListForWorkspace(h.db, ws)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

type gitProviderBody struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`     // token | ssh_key
	Host     string `json:"host"`     // optional (e.g. github.com, gitlab.example.com)
	Username string `json:"username"` // optional token user
	Secret   string `json:"secret"`   // PAT, or an SSH private key (ssh_key); blank ssh_key ⇒ generate
}

// POST /api/workspaces/{workspace}/git-providers — create a provider private to the
// workspace. For kind=ssh_key with no secret, Rigger generates a deploy keypair and
// returns the public key for the user to add to their provider.
func (h *Handler) CreateWorkspaceGitProvider(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body gitProviderBody
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	p := gitproviders.Provider{
		Name:       strings.TrimSpace(body.Name),
		Kind:       gitproviders.Kind(body.Kind),
		Host:       strings.TrimSpace(body.Host),
		Username:   strings.TrimSpace(body.Username),
		Secret:     body.Secret,
		OwnerScope: gitproviders.WorkspaceScope(ws),
	}
	switch p.Kind {
	case gitproviders.KindToken:
		if strings.TrimSpace(body.Secret) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a token is required for a token provider"})
			return
		}
	case gitproviders.KindSSHKey:
		// Generate a deploy keypair when the user didn't paste their own private key.
		if strings.TrimSpace(body.Secret) == "" {
			priv, pub, gerr := gitproviders.GenerateSSHKey("rigger-" + ws)
			if gerr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": gerr.Error()})
				return
			}
			p.Secret, p.PublicKey = priv, pub
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be 'token' or 'ssh_key'"})
		return
	}
	created, err := gitproviders.Create(h.db, h.cryptoKey, p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT /api/workspaces/{workspace}/git-providers/{gpid} — edit a workspace-owned provider.
func (h *Handler) UpdateWorkspaceGitProvider(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsGitProviderID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if _, ok := h.ownsGitProvider(ws, id); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a provider owned by this workspace"})
		return
	}
	var body gitProviderBody
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	updated, err := gitproviders.Update(h.db, h.cryptoKey, id, strings.TrimSpace(body.Name), strings.TrimSpace(body.Host), strings.TrimSpace(body.Username), body.Secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /api/workspaces/{workspace}/git-providers/{gpid} — delete a workspace-owned provider.
func (h *Handler) DeleteWorkspaceGitProvider(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsGitProviderID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if _, ok := h.ownsGitProvider(ws, id); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a provider owned by this workspace"})
		return
	}
	if err := gitproviders.Delete(h.db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /api/workspaces/{workspace}/git-providers/{gpid}/test {repo} — verify access by
// `git ls-remote` against the given repo using the provider's credentials.
func (h *Handler) TestWorkspaceGitProvider(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsGitProviderID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Visible to the workspace (own or granted) — load with secret for the test.
	p, perr := gitproviders.Get(h.db, h.cryptoKey, id)
	if perr != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found"})
		return
	}
	if p.OwnerScope != gitproviders.WorkspaceScope(ws) && p.OwnerScope != "global" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "provider not available to this workspace"})
		return
	}
	var body struct {
		Repo string `json:"repo"`
	}
	_ = readJSON(r, &body)
	if err := p.Verify(strings.TrimSpace(body.Repo)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
