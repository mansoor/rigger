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
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
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
		if json.Unmarshal([]byte(line), &c) == nil {
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
