package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// safeContainerName guards the service/container path segment: docker container
// names are alphanumeric plus [_.-] and never start with a dash (which docker
// would treat as a flag). This keeps the value safe to pass as a docker argument.
var safeContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// containerExec resolves the executor for a (workspace, env) — local or the
// bound remote host — and validates the container name from the path. It writes
// an error response and returns ok=false on failure.
func (h *Handler) containerExec(w http.ResponseWriter, r *http.Request) (ex executor.Executor, name string, ok bool) {
	ws := r.PathValue("name")
	env := r.PathValue("env")
	svc := r.PathValue("service")
	if !safeContainerName.MatchString(svc) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid container name"})
		return nil, "", false
	}
	exec, err := h.bridge.ExecForEnv(ws, env)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return nil, "", false
	}
	return exec, svc, true
}

// writeDockerJSON runs a docker command that already emits JSON and streams its
// stdout through verbatim (e.g. `docker inspect`).
func (h *Handler) writeDockerJSON(w http.ResponseWriter, ex executor.Executor, args ...string) {
	out, err := ex.DockerOutput(executor.Spec{Args: args})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out) //nolint:errcheck
}

// ContainerInspect — GET .../containers/{service}/inspect.
// Returns the raw `docker inspect` JSON array; the UI slices it into the
// Overview / Network / Mounts / Health / Environment / Labels / Security tabs.
func (h *Handler) ContainerInspect(w http.ResponseWriter, r *http.Request) {
	ex, svc, ok := h.containerExec(w, r)
	if !ok {
		return
	}
	h.writeDockerJSON(w, ex, "inspect", svc)
}

// ContainerStats — GET .../containers/{service}/stats.
// One-shot live resource usage as a single JSON object.
func (h *Handler) ContainerStats(w http.ResponseWriter, r *http.Request) {
	ex, svc, ok := h.containerExec(w, r)
	if !ok {
		return
	}
	h.writeDockerJSON(w, ex, "stats", "--no-stream", "--format", "{{json .}}", svc)
}

// ContainerTop — GET .../containers/{service}/top.
// Returns the raw `docker top` table for the Processes tab.
func (h *Handler) ContainerTop(w http.ResponseWriter, r *http.Request) {
	ex, svc, ok := h.containerExec(w, r)
	if !ok {
		return
	}
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"top", svc}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": string(out)})
}

// ContainerImageHistory — GET .../containers/{service}/image-history.
// Resolves the container's image then returns `docker image history` (the Layers
// tab) as a preformatted table.
func (h *Handler) ContainerImageHistory(w http.ResponseWriter, r *http.Request) {
	ex, svc, ok := h.containerExec(w, r)
	if !ok {
		return
	}
	imgOut, err := ex.DockerOutput(executor.Spec{Args: []string{"inspect", "-f", "{{.Config.Image}}", svc}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	img := strings.TrimSpace(string(imgOut))
	if img == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not resolve container image"})
		return
	}
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"image", "history", "--no-trunc", img}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": string(out)})
}
