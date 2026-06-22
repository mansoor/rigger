package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// safeContainerName guards the service/container path segment: docker container
// names are alphanumeric plus [_.-] and never start with a dash (which docker
// would treat as a flag). This keeps the value safe to pass as a docker argument.
var safeContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// containerExec resolves the executor for a (workspace, env) — local or the
// bound remote host — and validates the container name from the path. It writes
// an error response and returns ok=false on failure.
func (h *Handler) containerExec(w http.ResponseWriter, r *http.Request) (ex executor.Executor, name string, ok bool) {
	wsName := r.PathValue("workspace")
	ws := r.PathValue("name")
	env := r.PathValue("env")
	svc := r.PathValue("service")
	if !safeContainerName.MatchString(svc) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid container name"})
		return nil, "", false
	}
	exec, err := h.bridge.ExecForEnv(wsName, ws, env)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return nil, "", false
	}
	// Resolve to an actual container reference. Compose: the container_name
	// (svc as-is). Swarm: the running task's container ID (svc is a service name,
	// not a container) — see resolveContainerRef.
	ref, rerr := h.resolveContainerRef(exec, wsName, ws, env, svc)
	if rerr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": rerr.Error()})
		return nil, "", false
	}
	return exec, ref, true
}

// resolveContainerRef maps a UI service identifier to a docker container
// reference usable by inspect/exec. For compose it's the container_name (the
// value as passed). For swarm there is no fixed container name — the service
// runs as a task (e.g. test_prod_test_prod_app.1.<id>) — so we look up the
// running task's container ID on the env's node via the swarm service label.
func (h *Handler) resolveContainerRef(ex executor.Executor, wsName, ws, env, svc string) (string, error) {
	raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, ws))
	if err != nil {
		return svc, nil // best-effort: fall back to the given name
	}
	var cfg struct {
		Project struct {
			Name           string `json:"name"`
			ResourcePrefix string `json:"resource_prefix"`
		} `json:"project"`
		Environments map[string]struct {
			Deployment string `json:"deployment"`
		} `json:"environments"`
	}
	json.Unmarshal(raw, &cfg) //nolint:errcheck
	prefix := cfg.Project.ResourcePrefix
	if prefix == "" {
		prefix = cfg.Project.Name
	}
	stack := prefix + "_" + env

	if cfg.Environments[env].Deployment != "swarm" {
		// Compose: `svc` is the compose SERVICE name (what `docker compose ps`
		// reports and the UI sends), which is NOT necessarily the container_name —
		// the unified generator no longer sets container_name on app services, so
		// Docker auto-names them "{project}_{service}_N". Resolve the real container
		// id by compose labels (works for running + stopped). Fall back to the given
		// name for older deploys that DID set an explicit container_name.
		out, err := ex.DockerOutput(executor.Spec{Args: []string{
			"ps", "-aq",
			"--filter", "label=com.docker.compose.project=" + stack,
			"--filter", "label=com.docker.compose.service=" + svc,
		}})
		if err == nil {
			id := strings.TrimSpace(string(out))
			if nl := strings.IndexByte(id, '\n'); nl >= 0 {
				id = id[:nl]
			}
			if id != "" {
				return id, nil
			}
		}
		return svc, nil // fallback: explicit container_name (legacy) or best-effort
	}

	full := resolveSwarmServiceName(ex, stack, svc)
	out, err := ex.DockerOutput(executor.Spec{
		Args: []string{"ps", "-q", "--filter", "label=com.docker.swarm.service.name=" + full},
	})
	if err != nil {
		return "", fmt.Errorf("locate swarm task: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if nl := strings.IndexByte(id, '\n'); nl >= 0 {
		id = id[:nl]
	}
	if id == "" {
		return "", fmt.Errorf("no running container for service %q (service may be down)", svc)
	}
	return id, nil
}

// resolveSwarmServiceName maps a UI identifier (short name, compose key, or full
// name) to the actual swarm service name by matching the deployed service list
// (exact or "_<svc>" suffix), with a best-effort prefix fallback.
func resolveSwarmServiceName(ex executor.Executor, stack, svc string) string {
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"stack", "services", stack, "--format", "{{.Name}}"}})
	if err == nil {
		for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			n = strings.TrimSpace(n)
			if n != "" && (n == svc || strings.HasSuffix(n, "_"+svc)) {
				return n
			}
		}
	}
	if strings.HasPrefix(svc, stack+"_") {
		return svc
	}
	return stack + "_" + svc
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
	// JSON-per-line so the frontend can render a structured, collapsible layer
	// stack (instruction + size per layer) instead of a raw text table.
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"image", "history", "--no-trunc", "--format", "{{json .}}", img}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}

	type histLayer struct {
		CreatedBy string `json:"created_by"`
		CreatedAt string `json:"created_at"`
		Size      string `json:"size"`
		Comment   string `json:"comment"`
		ID        string `json:"id"`
	}
	layers := []histLayer{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var raw struct {
			CreatedBy string `json:"CreatedBy"`
			CreatedAt string `json:"CreatedAt"`
			Size      string `json:"Size"`
			Comment   string `json:"Comment"`
			ID        string `json:"ID"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		layers = append(layers, histLayer{
			CreatedBy: raw.CreatedBy, CreatedAt: raw.CreatedAt,
			Size: raw.Size, Comment: raw.Comment, ID: raw.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"layers": layers})
}
