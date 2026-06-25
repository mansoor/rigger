package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mansoor/rigger/ui/internal/gitproviders"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// GitHub App one-click flow (Phase 12). The browser POSTs an app manifest to GitHub
// (/settings/apps/new); GitHub creates the app and redirects back to our callback
// with a temporary code, which we exchange for the app's credentials (private key +
// ids) and store as a github_app provider. The user then installs the app; GitHub
// redirects to our setup callback with the installation_id, which we record. Cloning
// then mints short-lived installation tokens (see internal/gitproviders/github.go).
//
// The callbacks are top-level browser redirects from github.com, so they can't carry
// the SPA's auth token — they're PUBLIC and bound to a short-lived random `state`
// minted for an authenticated user (standard OAuth-callback pattern).

type ghState struct {
	workspace  string
	name       string
	host       string
	providerID int64 // set for setup (install) states
	expires    time.Time
}

var (
	ghStatesMu sync.Mutex
	ghStates   = map[string]ghState{}
)

func putGHState(s ghState) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.expires = time.Now().Add(15 * time.Minute)
	ghStatesMu.Lock()
	ghStates[tok] = s
	// opportunistic GC of expired states
	for k, v := range ghStates {
		if time.Now().After(v.expires) {
			delete(ghStates, k)
		}
	}
	ghStatesMu.Unlock()
	return tok
}

func takeGHState(tok string) (ghState, bool) {
	ghStatesMu.Lock()
	defer ghStatesMu.Unlock()
	s, ok := ghStates[tok]
	if ok {
		delete(ghStates, tok) // one-time use
	}
	if ok && time.Now().After(s.expires) {
		return ghState{}, false
	}
	return s, ok
}

// originFromRequest reconstructs the public base URL the user is browsing Rigger at,
// so GitHub can redirect the browser back to our callbacks.
func originFromRequest(r *http.Request) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// POST /api/workspaces/{workspace}/git-providers/github/manifest {name, host}
// Returns the GitHub app-create URL + manifest the frontend auto-submits as a form.
func (h *Handler) StartGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	if _, err := os.Stat(wspath.WorkspaceMeta(h.workspacesDir, ws)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workspace not found"})
		return
	}
	var body struct {
		Name string `json:"name"`
		Host string `json:"host"`
	}
	_ = readJSON(r, &body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "rigger-" + ws
	}
	host := strings.TrimSpace(body.Host)
	state := putGHState(ghState{workspace: ws, name: name, host: host})
	origin := originFromRequest(r)
	manifest := gitproviders.Manifest(
		name, host,
		origin+"/api/git-providers/github/callback",
		origin+"/api/git-providers/github/setup",
		"", // webhook URL omitted — set when the host is public (auto-deploy is phase 2)
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"create_url": gitproviders.AppCreateURL(host, state),
		"manifest":   manifest,
		"state":      state,
	})
}

// GET /api/git-providers/github/callback?code=&state= — PUBLIC. Exchange the manifest
// code for the app credentials, store the provider, then redirect the browser to
// GitHub to install the app.
func (h *Handler) GitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	st, ok := takeGHState(r.URL.Query().Get("state"))
	code := r.URL.Query().Get("code")
	if !ok || code == "" {
		http.Error(w, "invalid or expired GitHub app-creation state", http.StatusBadRequest)
		return
	}
	privPEM, meta, err := gitproviders.ConvertManifestCode(st.host, code)
	if err != nil {
		http.Error(w, "GitHub app creation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	created, err := gitproviders.Create(h.db, h.cryptoKey, gitproviders.Provider{
		Name: st.name, Kind: gitproviders.KindGitHubApp, Host: st.host,
		Secret: privPEM, Meta: meta.JSON(), OwnerScope: gitproviders.WorkspaceScope(st.workspace),
	})
	if err != nil {
		http.Error(w, "store GitHub app: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Send the user to install the freshly-created app; the setup callback records the
	// installation id. Carry a one-time state bound to this provider.
	setupState := putGHState(ghState{workspace: st.workspace, host: st.host, providerID: created.ID})
	installURL := meta.InstallURL(st.host)
	if installURL == "" {
		http.Redirect(w, r, manageGitURL(st.workspace), http.StatusFound)
		return
	}
	http.Redirect(w, r, installURL+"?state="+setupState, http.StatusFound)
}

// GET /api/git-providers/github/setup?installation_id=&state= — PUBLIC. Record the
// installation id on the provider, then return the user to the Git tab.
func (h *Handler) GitHubAppSetup(w http.ResponseWriter, r *http.Request) {
	st, ok := takeGHState(r.URL.Query().Get("state"))
	instID := r.URL.Query().Get("installation_id")
	if !ok || st.providerID == 0 {
		http.Error(w, "invalid or expired GitHub install state", http.StatusBadRequest)
		return
	}
	if instID != "" {
		if p, err := gitproviders.Get(h.db, h.cryptoKey, st.providerID); err == nil && p != nil {
			m := gitproviders.ParseGitHubMeta(p.Meta)
			m.InstallationID = instID
			_ = gitproviders.UpdateMeta(h.db, st.providerID, m.JSON())
		}
	}
	http.Redirect(w, r, manageGitURL(st.workspace), http.StatusFound)
}

// POST /api/workspaces/{workspace}/git-providers/{gpid}/github/install — returns the
// GitHub install URL (with a one-time setup state) so the user can (re)install an
// already-created app and record/refresh its installation id.
func (h *Handler) GitHubAppInstallURL(w http.ResponseWriter, r *http.Request) {
	ws := r.PathValue("workspace")
	id, err := wsGitProviderID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, ok := h.ownsGitProvider(ws, id)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a provider owned by this workspace"})
		return
	}
	if p.Kind != gitproviders.KindGitHubApp {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a GitHub App provider"})
		return
	}
	m := gitproviders.ParseGitHubMeta(p.Meta)
	url := m.InstallURL(p.Host)
	if url == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this app has no slug yet — recreate it"})
		return
	}
	state := putGHState(ghState{workspace: ws, host: p.Host, providerID: id})
	writeJSON(w, http.StatusOK, map[string]string{"install_url": url + "?state=" + state})
}

// manageGitURL is the SPA Manage-Workspace Git tab the callbacks return the user to.
func manageGitURL(ws string) string {
	return "/" + ws + "/manage?tab=git"
}
