package dockerops

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// Docker Swarm secret lifecycle (Phase 8). Swarm stores secret values encrypted
// at rest in the Raft log and delivers them to tasks in-memory (tmpfs) — so for
// swarm deployments Rigger never has to encrypt secrets itself. Secrets are
// immutable, so a value change is a new version (see rotation); the generated
// compose references each secret as `external: true`.

// SecretName builds the Swarm secret name for one key version, e.g.
// "myapp_prod_POSTGRES_PASSWORD_v1". The compose file mounts it at the bare key
// path (/run/secrets/<KEY>) so the version never leaks into the container.
func SecretName(project, env, key string, version int) string {
	if version < 1 {
		version = 1
	}
	return fmt.Sprintf("%s_%s_%s_v%d", project, env, key, version)
}

// SecretExists reports whether a Swarm secret with this exact name exists.
func SecretExists(ex executor.Executor, name string) bool {
	out, err := executor.Default(ex).DockerOutput(executor.Spec{
		Args: []string{"secret", "inspect", "--format", "{{.ID}}", name},
	})
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// EnsureSecret creates the named Swarm secret from value (idempotent: a no-op if
// it already exists, since secrets are immutable). The value is passed on stdin
// so it never appears in the process argument list.
func EnsureSecret(ex executor.Executor, name, value string) error {
	if SecretExists(ex, name) {
		return nil
	}
	err := executor.Default(ex).Docker(executor.Spec{
		Args:  []string{"secret", "create", name, "-"},
		Stdin: strings.NewReader(value),
	})
	if err != nil {
		return fmt.Errorf("docker secret create %s: %w (is Swarm active on the target — `docker swarm init`?)", name, err)
	}
	return nil
}

// RemoveSecret deletes a Swarm secret. Fails if the secret is still in use by a
// running service — callers redeploy onto the new version first.
func RemoveSecret(ex executor.Executor, name string) error {
	return executor.Default(ex).Docker(executor.Spec{
		Args: []string{"secret", "rm", name},
	})
}
