package api

import (
	"encoding/json"
	"errors"
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

// safeEnvName guards the environment path segment before it is used to build a
// stack name or a directory path: no separators, no leading dot or dash.
var safeEnvName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// errNotInEnv means the requested container is not part of the project
// environment the request names. It maps to 404 — the container does not exist
// *here*, and whether it exists anywhere else is none of the caller's business.
var errNotInEnv = errors.New("not in this environment")

// containerExec resolves the executor for a (workspace, env) — local or the
// bound remote host — and the container the request may act on. It writes an
// error response and returns ok=false on failure.
func (h *Handler) containerExec(w http.ResponseWriter, r *http.Request) (ex executor.Executor, name string, ok bool) {
	wsName := r.PathValue("workspace")
	ws := r.PathValue("name")
	env := r.PathValue("env")
	svc := r.PathValue("service")
	if !safeContainerName.MatchString(svc) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid container name"})
		return nil, "", false
	}
	if !safeEnvName.MatchString(env) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid environment name"})
		return nil, "", false
	}
	exec, err := h.bridge.ExecForEnv(wsName, ws, env)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return nil, "", false
	}
	ref, rerr := h.resolveContainerRef(exec, wsName, ws, env, svc)
	if rerr != nil {
		status := http.StatusBadGateway
		if errors.Is(rerr, errNotInEnv) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": rerr.Error()})
		return nil, "", false
	}
	return exec, ref, true
}

// resolveContainerRef maps a UI service identifier to a container that belongs to
// the named project environment — and to nothing else.
//
// This is a security boundary. Every container-level endpoint goes through here:
// inspect, stats, top, image history, the file browser, the terminal, and the
// database and object-storage consoles. RBAC authorises the *project*; this
// decides which *container* that authorisation reaches.
//
// It used to fall back to the raw name the client sent whenever its lookup
// missed (and whenever the project config couldn't be read). A Viewer on any
// project could therefore name a container outside it: inspecting the Rigger
// container itself exposed the JWT signing secret in its environment, and the
// file endpoints could read any other workspace's database. It now returns only
// a container carrying this environment's stack label, and fails closed on every
// other path.
func (h *Handler) resolveContainerRef(ex executor.Executor, wsName, ws, env, svc string) (string, error) {
	if !safeContainerName.MatchString(svc) {
		return "", fmt.Errorf("invalid container name")
	}
	scope, err := h.containerScope(wsName, ws, env)
	if err != nil {
		return "", err
	}
	if scope.swarm {
		return resolveSwarmContainer(ex, scope.stack, svc)
	}

	// Compose: list this stack's containers and match in Go. The project label is
	// re-checked per row (see matchStackContainer), so containment never rests on
	// the daemon — local or remote — honouring a filter.
	out, err := ex.DockerOutput(executor.Spec{Args: []string{
		"ps", "-a", "--no-trunc",
		"--filter", "label=com.docker.compose.project=" + scope.stack,
		"--format", composeListingFormat,
	}})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	if id, ok := matchStackContainer(string(out), scope.stack, svc); ok {
		return id, nil
	}
	return "", fmt.Errorf("no container %q in environment %q: %w", svc, env, errNotInEnv)
}

// envScope is the stack a request may touch.
type envScope struct {
	stack string
	swarm bool
}

// containerScope resolves the stack for (workspace, project, env), failing closed:
// an unreadable config, a project without a prefix, or an environment the project
// does not have all refuse rather than guess.
//
// The environment must belong to the project because the stack name is
// "{prefix}_{env}". Key-era prefixes are "{ws}_{proj}" with alphanumeric keys, so
// a crafted env cannot reach another project's stack — but legacy name-derived
// prefixes carry no such guarantee, and checking costs nothing.
func (h *Handler) containerScope(wsName, ws, env string) (envScope, error) {
	if !safeEnvName.MatchString(env) {
		return envScope{}, fmt.Errorf("invalid environment name")
	}
	raw, err := os.ReadFile(wspath.ConfigPath(h.workspacesDir, wsName, ws))
	if err != nil {
		return envScope{}, fmt.Errorf("project %q: %w", ws, errNotInEnv)
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
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return envScope{}, fmt.Errorf("project config unreadable: %w", err)
	}
	prefix := cfg.Project.ResourcePrefix
	if prefix == "" {
		prefix = cfg.Project.Name
	}
	if prefix == "" {
		return envScope{}, fmt.Errorf("project has no resource prefix")
	}
	envCfg, known := cfg.Environments[env]
	if !known {
		// The env list is the union of config and envs/ directories (see the
		// env-list union note), so a directory-only env is real — but one the
		// project has in neither place is not.
		fi, statErr := os.Stat(wspath.EnvDir(h.workspacesDir, wsName, ws, env))
		if statErr != nil || !fi.IsDir() {
			return envScope{}, fmt.Errorf("environment %q: %w", env, errNotInEnv)
		}
	}
	return envScope{stack: prefix + "_" + env, swarm: envCfg.Deployment == "swarm"}, nil
}

// composeListingFormat emits id, names, compose project and compose service per
// container, pipe-separated. Container names cannot contain '|', and compose
// project and service names are restricted to [a-z0-9_-].
const composeListingFormat = `{{.ID}}|{{.Names}}|{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.service"}}`

// matchStackContainer picks the container a request names from a container
// listing, considering ONLY rows whose compose project label is exactly stack —
// so even a listing of every container on the host yields nothing outside it.
//
// svc may be any of the identifiers callers legitimately send:
//   - the compose service ("app") — what `docker compose ps` reports and the UI
//     sends for the terminal, file browser and inspect;
//   - the container name ("mcl_sdm_dev_mysql") — explicit container_name, which
//     managed services set;
//   - "{stack}_{service}" — the network-alias convention, which is what DB_HOST
//     holds and what the database console passes;
//   - a container id of at least 12 characters (the short-id length).
//
// Pure, so the containment rule is testable without a Docker daemon.
func matchStackContainer(listing, stack, svc string) (string, bool) {
	if stack == "" || svc == "" {
		return "", false
	}
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 4 {
			continue
		}
		id, names, project, service := f[0], f[1], f[2], f[3]
		if id == "" || project != stack {
			continue
		}
		if service != "" && (svc == service || svc == stack+"_"+service) {
			return id, true
		}
		for _, n := range strings.Split(names, ",") {
			if svc == strings.TrimPrefix(strings.TrimSpace(n), "/") {
				return id, true
			}
		}
		if len(svc) >= 12 && strings.HasPrefix(id, svc) {
			return id, true
		}
	}
	return "", false
}

// resolveSwarmContainer finds the running task container of a service in this
// stack. There is no fixed container name in Swarm — a service runs as tasks —
// so the lookup goes through the service label. The stack-namespace filter keeps
// it inside the stack even if a service name were crafted to look like another
// stack's.
func resolveSwarmContainer(ex executor.Executor, stack, svc string) (string, error) {
	full := resolveSwarmServiceName(ex, stack, svc)
	if !strings.HasPrefix(full, stack+"_") {
		return "", fmt.Errorf("no service %q in this stack: %w", svc, errNotInEnv)
	}
	out, err := ex.DockerOutput(executor.Spec{Args: []string{
		"ps", "-q",
		"--filter", "label=com.docker.stack.namespace=" + stack,
		"--filter", "label=com.docker.swarm.service.name=" + full,
	}})
	if err != nil {
		return "", fmt.Errorf("locate swarm task: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if nl := strings.IndexByte(id, '\n'); nl >= 0 {
		id = id[:nl]
	}
	if id == "" {
		return "", fmt.Errorf("no running container for service %q (service may be down): %w", svc, errNotInEnv)
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
