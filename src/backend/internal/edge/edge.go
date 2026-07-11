// Package edge provisions a self-contained Traefik "edge" on a REMOTE host so
// web-routed workloads deployed there can actually be reached.
//
// Rigger's routing model assumes a single Traefik on the control plane joined to a
// shared external `traefik_net`. A freshly registered remote host is just a bare
// Docker engine: it has neither, so a routed stack fails `compose up` with "network
// traefik_net not found" and, even if forced up, nothing routes its URL. This
// package reproduces the control-plane edge trio (traefik + docker-API socket-proxy
// + friendly fallback page) on the remote host over SSH, and creates `traefik_net`
// there. Because a routed app's auto-URL already embeds the remote host's own IP
// (…10.10.10.55.nip.io), once that host runs Traefik on :80 the existing router
// labels route locally on the host — no control-plane-reaches-remote problem.
//
// Mode-aware: on a standalone engine it deploys a bridge `traefik_net` +
// providers.docker + container labels via `docker compose`; on a Swarm manager it
// deploys an overlay `traefik_net` + providers.swarm + deploy.labels via `docker
// stack deploy`. The mode is chosen from the REMOTE host's probed swarm state, not
// the control plane's. See the remote-host-workload-gaps analysis (G1/G7).
package edge

import (
	"embed"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/traefikcfg"
)

//go:embed assets/docker-proxy.nginx.conf assets/fallback.default.conf assets/fallback.html
var assets embed.FS

// Pinned images (mirror the control-plane edge in src/docker-compose.yml).
const (
	TraefikImage = "traefik:v3.4"
	NginxImage   = "nginx:1.27-alpine"

	// StackName is the compose project / swarm stack name for the remote edge.
	StackName = "rigger-edge"
	// Network is the shared routing network app stacks attach to (external).
	Network = "traefik_net"

	traefikContainer = "rigger-edge-traefik"
)

// FileWriter is the subset of *remotehost.Client used to stage edge files. Kept as
// an interface so the package is unit-testable without a live SSH connection.
type FileWriter interface {
	WriteFile(path string, data []byte, mode int) error
}

// Options configures how the edge is provisioned on a host.
type Options struct {
	// Swarm ⇒ the remote host is a Swarm manager: overlay/attachable traefik_net,
	// providers.swarm, deploy.labels, and `docker stack deploy`. Otherwise a
	// standalone engine: bridge traefik_net, providers.docker, container labels,
	// `docker compose up`.
	Swarm bool
	// ACMEEmail seeds the Let's Encrypt resolver in the generated traefik.yml.
	// Empty is fine for the HTTP-only v1 (certs unused until a router requests one).
	ACMEEmail string
}

// Result reports what Ensure did, for surfacing to the operator.
type Result struct {
	AlreadyRunning bool // edge Traefik was already up → nothing changed
	NetworkCreated bool // traefik_net was created on the host
	Deployed       bool // the edge stack was (re)deployed
}

// EdgeDir returns the remote absolute directory the edge stack is staged in — a
// sibling of the remote workspaces dir (…/workspaces → …/edge). Falls back to a
// well-known path when the base is empty.
func EdgeDir(remoteWorkspacesDir string) string {
	base := strings.TrimRight(strings.ReplaceAll(remoteWorkspacesDir, "\\", "/"), "/")
	if base == "" {
		return "/opt/rigger/edge"
	}
	return path.Join(path.Dir(base), "edge")
}

// Ensure makes the edge present and running on the host reachable via ex (a remote
// executor.Executor) with files staged via fw. It is idempotent and fast on the
// happy path: when the edge Traefik is already running it returns immediately. out
// (may be nil) receives the deploy command's streamed output.
func Ensure(ex executor.Executor, fw FileWriter, remoteWorkspacesDir string, opts Options, out io.Writer) (Result, error) {
	var res Result
	// Fast path: Traefik already running ⇒ traefik_net exists (it uses it) and files
	// were staged on a prior run. One cheap SSH round-trip when healthy.
	if running, _ := traefikRunning(ex, opts.Swarm); running {
		res.AlreadyRunning = true
		return res, nil
	}
	edgeDir := EdgeDir(remoteWorkspacesDir)
	if !networkExists(ex) {
		if err := createNetwork(ex, opts.Swarm); err != nil {
			return res, fmt.Errorf("create %s on host: %w", Network, err)
		}
		res.NetworkCreated = true
	}
	if err := stageFiles(fw, edgeDir, opts); err != nil {
		return res, err
	}
	if err := deploy(ex, edgeDir, opts, out); err != nil {
		return res, fmt.Errorf("deploy edge stack: %w", err)
	}
	res.Deployed = true
	return res, nil
}

// Status reports whether the edge Traefik is running on the host.
func Status(ex executor.Executor, swarm bool) (running bool, err error) {
	return traefikRunning(ex, swarm)
}

// traefikRunning checks for a running edge Traefik. Standalone: a running container
// named rigger-edge-traefik. Swarm: a service in the rigger-edge stack.
func traefikRunning(ex executor.Executor, swarm bool) (bool, error) {
	if swarm {
		out, err := ex.DockerOutput(executor.Spec{Args: []string{
			"stack", "services", "--format", "{{.Name}}", StackName,
		}})
		if err != nil {
			return false, err
		}
		return strings.Contains(string(out), StackName+"_traefik"), nil
	}
	out, err := ex.DockerOutput(executor.Spec{Args: []string{
		"ps", "--filter", "name=^/" + traefikContainer + "$",
		"--filter", "status=running", "--format", "{{.ID}}",
	}})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// networkExists reports whether traefik_net already exists on the host.
func networkExists(ex executor.Executor) bool {
	_, err := ex.DockerOutput(executor.Spec{Args: []string{
		"network", "inspect", Network, "--format", "{{.Name}}",
	}})
	return err == nil
}

// createNetwork creates traefik_net — bridge for standalone, overlay/attachable for
// a Swarm manager (so app stacks and the edge can attach across the mesh).
func createNetwork(ex executor.Executor, swarm bool) error {
	args := []string{"network", "create"}
	if swarm {
		args = append(args, "-d", "overlay", "--attachable")
	}
	args = append(args, Network)
	if _, err := ex.DockerOutput(executor.Spec{Args: args}); err != nil {
		// A concurrent create (e.g. two deploys racing) is not an error.
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// deploy brings the edge stack up: `docker compose up -d` (standalone) or `docker
// stack deploy` (swarm), run from the staged edge dir on the host.
func deploy(ex executor.Executor, edgeDir string, opts Options, out io.Writer) error {
	var spec executor.Spec
	if opts.Swarm {
		spec = executor.Spec{
			Args: []string{"stack", "deploy", "-c", "docker-compose.yml", StackName},
			Dir:  edgeDir, Stdout: out, Stderr: out,
		}
	} else {
		spec = executor.Spec{
			Args: []string{"compose", "-p", StackName, "-f", "docker-compose.yml", "up", "-d", "--remove-orphans"},
			Dir:  edgeDir, Stdout: out, Stderr: out,
		}
	}
	return ex.Docker(spec)
}

// stageFiles writes every edge file into edgeDir on the host: the generated compose,
// the Traefik static config, the socket-proxy + fallback assets, and the shared
// rigger-loading middleware the app routers reference.
func stageFiles(fw FileWriter, edgeDir string, opts Options) error {
	files := map[string][]byte{
		path.Join(edgeDir, "docker-compose.yml"):            []byte(composeYAML(edgeDir, opts)),
		path.Join(edgeDir, "traefik.yml"):                   []byte(traefikcfg.Generate(traefikcfg.Options{ACMEEmail: opts.ACMEEmail, Swarm: opts.Swarm})),
		path.Join(edgeDir, "dynamic", "rigger-loading.yml"): []byte(loadingMiddleware(opts.Swarm)),
		path.Join(edgeDir, "docker-proxy.nginx.conf"):       asset("docker-proxy.nginx.conf"),
		path.Join(edgeDir, "fallback.default.conf"):         asset("fallback.default.conf"),
		path.Join(edgeDir, "fallback.html"):                 asset("fallback.html"),
	}
	for p, data := range files {
		if err := fw.WriteFile(p, data, 0o644); err != nil {
			return fmt.Errorf("stage %s: %w", path.Base(p), err)
		}
	}
	return nil
}

func asset(name string) []byte {
	b, _ := assets.ReadFile("assets/" + name) //nolint:errcheck // embedded, always present
	return b
}

// loadingMiddleware renders the Traefik file-provider definition for the shared
// "rigger-loading" errors middleware every app router references as
// rigger-loading@file. The backend service reference carries the provider suffix
// matching this host's mode (@docker for standalone, @swarm for a Swarm manager).
func loadingMiddleware(swarm bool) string {
	provider := "docker"
	if swarm {
		provider = "swarm"
	}
	return fmt.Sprintf(`# Auto-generated by Rigger — remote edge. Do not edit.
# Shared errors middleware: when an app's backend is down (502/503/504 while it's
# still starting or crash-looping), serve the friendly retry page instead of a bare
# gateway error. App routers reference this as rigger-loading@file.
http:
  middlewares:
    rigger-loading:
      errors:
        status:
          - "502-504"
        service: rigger-fallback@%s
        query: "/"
`, provider)
}

// composeYAML renders the edge stack's docker-compose.yml. Bind sources use the
// host-absolute edgeDir so both `docker compose` and `docker stack deploy` (which
// does not resolve relative bind paths) work. In swarm mode routing labels move to
// deploy.labels, container_name is dropped (swarm forbids it), traefik_net is
// external overlay, and the traefik/socket-proxy tasks are pinned to a manager node
// (they must bind :80/:443 and read the manager's Docker API).
func composeYAML(edgeDir string, opts Options) string {
	var b strings.Builder
	b.WriteString("# Auto-generated by Rigger — remote Traefik edge. Do not edit by hand.\n")
	b.WriteString("name: " + StackName + "\n\n")

	b.WriteString("networks:\n")
	b.WriteString("  " + Network + ":\n    external: true\n\n")

	b.WriteString("volumes:\n  edge-certs:\n\n")

	b.WriteString("services:\n\n")

	// ── traefik ──
	b.WriteString("  traefik:\n")
	b.WriteString("    image: " + TraefikImage + "\n")
	if !opts.Swarm {
		b.WriteString("    container_name: " + traefikContainer + "\n")
		b.WriteString("    restart: unless-stopped\n")
		b.WriteString("    depends_on:\n      - socket-proxy\n")
	}
	b.WriteString("    ports:\n      - \"80:80\"\n      - \"443:443\"\n")
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s/traefik.yml:/etc/traefik/traefik.yml:ro\n", edgeDir)
	fmt.Fprintf(&b, "      - %s/dynamic:/dynamic:ro\n", edgeDir)
	b.WriteString("      - edge-certs:/certs\n")
	b.WriteString("    networks:\n      - " + Network + "\n")
	if opts.Swarm {
		b.WriteString("    deploy:\n      placement:\n        constraints:\n          - node.role == manager\n")
	}
	b.WriteString("\n")

	// ── socket-proxy (Docker API version shim; read-only socket) ──
	b.WriteString("  socket-proxy:\n")
	b.WriteString("    image: " + NginxImage + "\n")
	if !opts.Swarm {
		b.WriteString("    container_name: rigger-edge-socket-proxy\n")
		b.WriteString("    restart: unless-stopped\n")
	}
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s/docker-proxy.nginx.conf:/etc/nginx/nginx.conf:ro\n", edgeDir)
	b.WriteString("      - /var/run/docker.sock:/var/run/docker.sock:ro\n")
	b.WriteString("    networks:\n      - " + Network + "\n")
	if opts.Swarm {
		b.WriteString("    deploy:\n      placement:\n        constraints:\n          - node.role == manager\n")
	}
	b.WriteString("\n")

	// ── fallback (lowest-priority catch-all "app starting" page) ──
	b.WriteString("  fallback:\n")
	b.WriteString("    image: " + NginxImage + "\n")
	if !opts.Swarm {
		b.WriteString("    container_name: rigger-edge-fallback\n")
		b.WriteString("    restart: unless-stopped\n")
	}
	b.WriteString("    volumes:\n")
	fmt.Fprintf(&b, "      - %s/fallback.default.conf:/etc/nginx/conf.d/default.conf:ro\n", edgeDir)
	fmt.Fprintf(&b, "      - %s/fallback.html:/usr/share/nginx/html/index.html:ro\n", edgeDir)
	b.WriteString("    networks:\n      - " + Network + "\n")
	labels := []string{
		"traefik.enable=true",
		"traefik.http.routers.rigger-fallback.rule=PathPrefix(`/`)",
		"traefik.http.routers.rigger-fallback.entrypoints=web",
		"traefik.http.routers.rigger-fallback.priority=1",
		"traefik.http.routers.rigger-fallback.service=rigger-fallback",
		"traefik.http.services.rigger-fallback.loadbalancer.server.port=80",
	}
	if opts.Swarm {
		// Swarm reads router labels from deploy.labels, and the service port label
		// must live there too; also pin the task to a manager (traefik_net overlay).
		b.WriteString("    deploy:\n")
		b.WriteString("      placement:\n        constraints:\n          - node.role == manager\n")
		b.WriteString("      labels:\n")
		for _, l := range labels {
			fmt.Fprintf(&b, "        - \"%s\"\n", l)
		}
	} else {
		b.WriteString("    labels:\n")
		for _, l := range labels {
			fmt.Fprintf(&b, "      - \"%s\"\n", l)
		}
	}
	b.WriteString("\n")

	return b.String()
}
