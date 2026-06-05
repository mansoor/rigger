package remotehost

import (
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// Remote is an executor.Executor that runs docker commands on a remote host over
// SSH. It runs each command as `cd <remote-env-dir> && docker <args>`, translating
// the control plane's local working directory to the remote WORKSPACES_DIR. The
// control-plane process env (PATH/HOME/DOCKER_HOST/…) is intentionally NOT
// forwarded: the remote daemon and the pushed .env supply everything compose
// needs, and forwarding DOCKER_HOST would point the remote at the wrong socket.
type Remote struct {
	client     *Client
	localBase  string // local WORKSPACES_DIR (prefix to strip)
	remoteBase string // remote WORKSPACES_DIR (prefix to apply)
}

// NewRemote builds a Remote executor for an open client. localBase/remoteBase are
// the control-plane and remote WORKSPACES_DIR paths used to translate Spec.Dir.
func NewRemote(c *Client, localBase, remoteBase string) Remote {
	return Remote{client: c, localBase: localBase, remoteBase: remoteBase}
}

// Docker runs a streaming docker command on the remote, wiring the session to the
// spec's Stdin/Stdout/Stderr (so backup/restore's stdin/stdout pipes work).
func (r Remote) Docker(s executor.Spec) error {
	sess, err := r.client.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = s.Stdin
	sess.Stdout = s.Stdout
	sess.Stderr = s.Stderr
	return sess.Run(r.command(s))
}

// DockerOutput runs a docker command on the remote and returns its stdout.
func (r Remote) DockerOutput(s executor.Spec) ([]byte, error) {
	sess, err := r.client.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	sess.Stdin = s.Stdin
	return sess.Output(r.command(s))
}

// command renders the remote shell line for a Spec: an optional `cd <dir> &&`
// prefix followed by `docker` with each argument single-quoted.
func (r Remote) command(s executor.Spec) string {
	b := strings.Builder{}
	if dir := r.translate(s.Dir); dir != "" {
		b.WriteString("cd ")
		b.WriteString(shQuote(dir))
		b.WriteString(" && ")
	}
	b.WriteString("docker")
	for _, a := range s.Args {
		b.WriteByte(' ')
		b.WriteString(shQuote(a))
	}
	return b.String()
}

// translate maps a local env-dir path to its remote equivalent by swapping the
// workspaces-dir prefix. Paths outside localBase are passed through unchanged.
func (r Remote) translate(localDir string) string {
	if localDir == "" {
		return ""
	}
	if r.localBase != "" && strings.HasPrefix(localDir, r.localBase) {
		rel := strings.TrimPrefix(localDir, r.localBase)
		rel = strings.ReplaceAll(rel, "\\", "/") // defensive; control plane is Linux
		return strings.TrimRight(r.remoteBase, "/") + rel
	}
	return localDir
}

// RemoteDir exposes the local→remote path translation for callers that need the
// remote env dir (e.g. PushDir targets) without running a command.
func (r Remote) RemoteDir(localDir string) string { return r.translate(localDir) }

var _ executor.Executor = Remote{}
