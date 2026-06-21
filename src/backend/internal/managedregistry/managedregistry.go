// Package managedregistry runs a Rigger-managed Docker registry (registry:2) as an
// infra sidecar on the Rigger host's daemon — the one-click alternative to pointing
// at an existing/cloud registry (image-distribution Phase 2). Rigger drives it
// through the docker socket (like the out-of-band ACME lego runner), so no change to
// Rigger's own compose stack is needed.
//
// Reachability has two modes, decided by whether an apps base domain is configured:
//   - base domain set → the registry is fronted by rigger-traefik at
//     registry.{base} over HTTPS (ACME), pullable by every Swarm node. Genuinely
//     one-click for a cluster.
//   - no base domain → the registry publishes a host port (HTTP, insecure). Fine
//     for a LOCAL single-node deploy; NOT reachable by a remote multi-node Swarm.
//
// Auth is htpasswd (bcrypt): Rigger writes the htpasswd file into a small named
// volume via a one-off helper container, and the registry mounts it read-only. The
// build host auto-`docker login`s with the stored credentials (registry-auth path).
package managedregistry

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/mansoor/rigger/ui/internal/executor"
)

const (
	// Container is the fixed name of the managed registry container.
	Container = "rigger-registry"
	// DataVolume backs the registry's blob/metadata store (survives Down).
	DataVolume = "rigger-registry-data"
	// AuthVolume holds the htpasswd file (rigger writes; registry mounts ro).
	AuthVolume = "rigger-registry-auth"
	// Network is the shared Traefik network the registry attaches to (so Traefik
	// can route registry.{base} to it). Same external network as the app stacks.
	Network = "traefik_net"
	// internalPort is registry:2's listen port inside the container.
	internalPort = "5000"
	// DefaultPort is the host port published when there's no base domain (local HTTP).
	DefaultPort = "5000"
	// Username is the single account in the generated htpasswd.
	Username = "rigger"
	// EntryName is the docker_registries display name used for the managed registry
	// entry, so the admin handler can find/upsert it across runs.
	EntryName = "Rigger managed registry"
	// Subdomain label fronted by Traefik when a base domain is configured.
	Subdomain = "registry"

	defaultImage  = "registry:2"
	defaultHelper = "busybox:1.36"
	htpasswdPath  = "/auth/htpasswd"
	configPath    = "/etc/docker/registry/config.yml" // registry:2 default config (for GC)
)

// Manager drives the managed-registry container on the Rigger host.
type Manager struct {
	Exec   executor.Executor // runs docker on the rigger host (local daemon)
	Image  string            // registry image (default registry:2)
	Helper string            // tiny image used to write the htpasswd file
}

// New builds a Manager bound to exec (Local when nil).
func New(exec executor.Executor) *Manager {
	return &Manager{Exec: executor.Default(exec), Image: defaultImage, Helper: defaultHelper}
}

func (m *Manager) image() string {
	if m.Image != "" {
		return m.Image
	}
	return defaultImage
}

func (m *Manager) helper() string {
	if m.Helper != "" {
		return m.Helper
	}
	return defaultHelper
}

// Config controls how the registry is exposed.
type Config struct {
	// BaseDomain, when set, fronts the registry via Traefik at registry.{base} over
	// HTTPS. Empty ⇒ publish a host port (local HTTP).
	BaseDomain string
	// DNSProvider ("cloudflare"|"") picks the ACME resolver for the Traefik router:
	// the DNS-01 wildcard resolver when set, else per-host HTTP-01 (letsencrypt).
	DNSProvider string
	// Port is the published host port when there's no base domain (default 5000).
	Port string
}

func (c Config) port() string {
	if strings.TrimSpace(c.Port) != "" {
		return strings.TrimSpace(c.Port)
	}
	return DefaultPort
}

// Host returns the registry hostname Traefik routes (registry.{base}); "" when no
// base domain is set.
func (c Config) Host() string {
	b := strings.TrimSpace(c.BaseDomain)
	if b == "" {
		return ""
	}
	return Subdomain + "." + b
}

// URL is the registry reference used as the image-tag prefix and stored on the
// registries entry: registry.{base} when a base domain is set (HTTPS via Traefik),
// else localhost:{port} (local HTTP, single-node only). Scheme-less by design — it
// is concatenated as "{url}/{image}" everywhere (wsconfig.ImageTag).
func (c Config) URL() string {
	if h := c.Host(); h != "" {
		return h
	}
	return "localhost:" + c.port()
}

// HTTPS reports whether the configured URL is served over TLS (base-domain mode).
func (c Config) HTTPS() bool { return c.Host() != "" }

// HtpasswdLine builds a single bcrypt htpasswd entry ("user:hash") for the registry.
func HtpasswdLine(username, password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return username + ":" + string(hash), nil
}

// runArgs builds the `docker run` argument list for the registry container (pure, so
// it is unit-tested). cfg decides Traefik labels (base domain) vs a published port.
func (m *Manager) runArgs(cfg Config) []string {
	args := []string{
		"run", "-d",
		"--name", Container,
		"--restart", "unless-stopped",
		"--network", Network,
		"-v", DataVolume + ":/var/lib/registry",
		"-v", AuthVolume + ":/auth:ro",
		"-e", "REGISTRY_AUTH=htpasswd",
		"-e", "REGISTRY_AUTH_HTPASSWD_REALM=Rigger Registry",
		"-e", "REGISTRY_AUTH_HTPASSWD_PATH=" + htpasswdPath,
		// Required so `registry garbage-collect` can actually delete unreferenced blobs.
		"-e", "REGISTRY_STORAGE_DELETE_ENABLED=true",
	}
	if cfg.Host() != "" {
		// Fronted by Traefik over HTTPS — no published host port.
		resolver := "letsencrypt"
		if strings.TrimSpace(cfg.DNSProvider) != "" {
			resolver = "dns" // DNS-01 wildcard resolver (covers *.{base})
		}
		args = append(args,
			"-l", "traefik.enable=true",
			"-l", "traefik.http.routers."+Container+".rule=Host(`"+cfg.Host()+"`)",
			"-l", "traefik.http.routers."+Container+".entrypoints=websecure",
			"-l", "traefik.http.routers."+Container+".tls=true",
			"-l", "traefik.http.routers."+Container+".tls.certresolver="+resolver,
			"-l", "traefik.http.services."+Container+".loadbalancer.server.port="+internalPort,
		)
	} else {
		// No base domain — publish a host port (local HTTP; single-node only).
		args = append(args, "-p", cfg.port()+":"+internalPort)
	}
	args = append(args, m.image())
	return args
}

// writeHtpasswd writes the htpasswd line into AuthVolume via a one-off helper
// container, piping the content on stdin so the bcrypt hash's '$' chars need no
// shell-escaping.
func (m *Manager) writeHtpasswd(line string) error {
	spec := executor.Spec{
		Args: []string{
			"run", "--rm", "-i",
			"-v", AuthVolume + ":/auth",
			m.helper(), "sh", "-c", "cat > " + htpasswdPath,
		},
		Stdin: strings.NewReader(line + "\n"),
	}
	if out, err := m.Exec.DockerOutput(spec); err != nil {
		return fmt.Errorf("write htpasswd: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Running reports whether the managed registry container is up.
func (m *Manager) Running() bool {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"inspect", "-f", "{{.State.Running}}", Container},
	})
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Exists reports whether the container exists at all (running or stopped).
func (m *Manager) Exists() bool {
	_, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"inspect", "-f", "{{.Name}}", Container},
	})
	return err == nil
}

// hostPortInUse reports whether a container OTHER than the managed registry already
// publishes the given host port on the daemon (best-effort; false on query error).
func (m *Manager) hostPortInUse(port string) bool {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"ps", "--filter", "publish=" + port, "--format", "{{.Names}}"},
	})
	if err != nil {
		return false
	}
	for _, name := range strings.Fields(string(out)) {
		if name != Container { // our own container reusing its port is fine
			return true
		}
	}
	return false
}

// FreeHostPort returns `preferred` if no other container publishes it, otherwise the
// first higher port that's free (bounded scan). Lets local-mode coexist with an
// existing registry already on :5000 instead of failing with "port allocated".
func (m *Manager) FreeHostPort(preferred string) string {
	if !m.hostPortInUse(preferred) {
		return preferred
	}
	base, err := strconv.Atoi(preferred)
	if err != nil {
		base = 5000
	}
	for i := 1; i < 50; i++ {
		cand := strconv.Itoa(base + i)
		if !m.hostPortInUse(cand) {
			return cand
		}
	}
	return preferred // give up; the run will surface the bind error
}

// Up (re)creates the registry container: it writes the htpasswd, removes any prior
// container (keeping the data volume), then runs the new one. The caller supplies
// the credentials so a restart can reuse the existing password.
func (m *Manager) Up(cfg Config, username, password string) error {
	line, err := HtpasswdLine(username, password)
	if err != nil {
		return err
	}
	if err := m.writeHtpasswd(line); err != nil {
		return err
	}
	// Remove any existing container first (idempotent re-run); keep the data volume.
	_ = m.Exec.Docker(executor.Spec{Args: []string{"rm", "-f", Container}})
	var buf bytes.Buffer
	if err := m.Exec.Docker(executor.Spec{Args: m.runArgs(cfg), Stdout: &buf, Stderr: &buf}); err != nil {
		return fmt.Errorf("run registry: %s", strings.TrimSpace(buf.String()))
	}
	return nil
}

// Down stops and removes the container but KEEPS the data + auth volumes, so a
// later Up restores the same images and credentials.
func (m *Manager) Down() error {
	var buf bytes.Buffer
	if err := m.Exec.Docker(executor.Spec{Args: []string{"rm", "-f", Container}, Stdout: &buf, Stderr: &buf}); err != nil {
		return fmt.Errorf("stop registry: %s", strings.TrimSpace(buf.String()))
	}
	return nil
}

// GC runs `registry garbage-collect` inside the container to reclaim space from
// deleted/overwritten tags. Requires the container to be running (delete enabled).
func (m *Manager) GC() (string, error) {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"exec", Container, "bin/registry", "garbage-collect", configPath},
	})
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("garbage-collect failed (is the registry running?): %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DiskUsage returns a human-readable size of the registry data volume (e.g. "42M"),
// or "" when it can't be measured.
func (m *Manager) DiskUsage() string {
	out, err := m.Exec.DockerOutput(executor.Spec{
		Args: []string{"run", "--rm", "-v", DataVolume + ":/v:ro", m.helper(), "du", "-sh", "/v"},
	})
	if err != nil {
		return ""
	}
	// `du -sh` prints "<size>\t/v"; take the first field.
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0]
	}
	return ""
}
