package api

import (
	"net/http"

	"github.com/mansoor/rigger/ui/internal/databases"
)

// Databases — GET /api/databases. Lists the managed database engines Rigger can
// host (with their selectable versions, port, and management capabilities), so
// the New Project wizard and Edit Project can offer an engine + version picker
// and the database surfaces know each engine's connection contract.
func (h *Handler) Databases(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, databases.Catalog())
}
