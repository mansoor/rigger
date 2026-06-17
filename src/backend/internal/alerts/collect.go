package alerts

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

// containerInfo is the subset of `docker compose ps` we need for the
// container_down and restart_count conditions.
type containerInfo struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

// isCompletedInit reports a one-shot init job (service name ends in "_init", e.g.
// minio_init creating the S3 bucket) that ran and exited 0. That's success, not a
// down/partial container — exclude it from every alert condition so a finished init
// doesn't fire container_down / stack_partial. A still-running or non-zero-exit init
// is NOT a completed init, so a stuck/failed init still surfaces.
func isCompletedInit(c containerInfo) bool {
	return strings.HasSuffix(c.Service, "_init") &&
		strings.EqualFold(c.State, "exited") && c.ExitCode == 0
}

// projectContainers runs `docker compose ps --all` for one env and returns its
// containers. Mirrors api.GetContainers so the evaluator sees the same state the
// UI does (--all is essential — without it stopped containers are invisible).
func projectContainers(project, composePath string) []containerInfo {
	out, err := exec.Command("docker", "compose",
		"-p", project,
		"-f", composePath,
		"ps", "--all", "--format", "json",
	).Output()
	if err != nil {
		return nil
	}
	var containers []containerInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var c containerInfo
		if json.Unmarshal([]byte(line), &c) == nil && !isCompletedInit(c) {
			containers = append(containers, c)
		}
	}
	return containers
}

// isDownState reports whether a container State counts as "down" for the
// container_down condition. Running and restarting are healthy-enough; anything
// else (exited, dead, created, paused) is down.
func isDownState(state string) bool {
	switch strings.ToLower(state) {
	case "running", "restarting":
		return false
	default:
		return true
	}
}

// oomKilledContainers inspects the given container IDs and returns the names of
// those whose last run was killed by the OOM killer (State.OOMKilled). A very
// common home-lab failure: a container hitting its memory limit and getting
// reaped. The flag reflects the most recent exit, so it's caught while the
// container is down or shortly after a restart.
func oomKilledContainers(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"inspect", "--format", "{{.State.OOMKilled}} {{.Name}}"}, ids...)
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil
	}
	var killed []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "true" {
			killed = append(killed, strings.TrimPrefix(fields[1], "/"))
		}
	}
	return killed
}

// maxRestartCount inspects the given container IDs and returns the highest
// RestartCount and the name of the container that holds it.
func maxRestartCount(ids []string) (int, string) {
	if len(ids) == 0 {
		return 0, ""
	}
	args := append([]string{"inspect", "--format", "{{.RestartCount}} {{.Name}}"}, ids...)
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return 0, ""
	}
	maxN, maxName := 0, ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if n > maxN {
			maxN = n
			maxName = strings.TrimPrefix(fields[1], "/")
		}
	}
	return maxN, maxName
}
