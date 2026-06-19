package api

import (
	"net/http"

	"github.com/mansoor/rigger/ui/internal/buildinfo"
)

// GET /api/version — the running Rigger build version + commit. Authenticated;
// surfaced in the UI footer and (later) the Admin → Updates section. The value
// is baked in at build time via -ldflags (see internal/buildinfo); a plain
// source build reports "dev".
func (h *Handler) GetVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": buildinfo.Version,
		"commit":  buildinfo.Commit,
	})
}
