// Package stats collects Docker and host system metrics for the dashboard.
package stats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// sampleTimeout bounds read-only sampling commands (docker stats/ps/compose ls).
// A hung container can otherwise make `docker stats` block indefinitely and
// freeze the metrics collector / dashboard polls. On timeout the command is
// killed and the cycle simply records nothing for that host.
const sampleTimeout = 15 * time.Second

// Stats is the full dashboard payload.
type Stats struct {
	Docker     DockerStats      `json:"docker"`
	Host       HostStats        `json:"host"`
	Workspaces WorkspaceSummary `json:"workspaces"`
}

type DockerStats struct {
	ServerVersion     string `json:"server_version"`
	ContainersRunning int    `json:"containers_running"`
	ContainersStopped int    `json:"containers_stopped"`
	ContainersPaused  int    `json:"containers_paused"`
	ImagesTotal       int    `json:"images_total"`
	VolumesTotal      int    `json:"volumes_total"`
	NetworksTotal     int    `json:"networks_total"`
	StorageDriver     string `json:"storage_driver"`
	DockerRootDir     string `json:"docker_root_dir"`
	Error             string `json:"error,omitempty"`
}

type HostStats struct {
	OS            string  `json:"os"`
	Arch          string  `json:"arch"`
	CPUs          int     `json:"cpus"`
	MemTotalMB    float64 `json:"mem_total_mb"`
	MemAvailMB    float64 `json:"mem_avail_mb"`
	MemUsedPct    float64 `json:"mem_used_pct"`
	DiskTotalGB   float64 `json:"disk_total_gb"`
	DiskUsedGB    float64 `json:"disk_used_gb"`
	DiskFreeGB    float64 `json:"disk_free_gb"`
	DiskUsedPct   float64 `json:"disk_used_pct"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

type WorkspaceSummary struct {
	Total      int             `json:"total"`
	ByType     map[string]int  `json:"by_type"`
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

type WorkspaceInfo struct {
	Name              string   `json:"name"`               // project key (folder/identity, unique within a workspace)
	DisplayName       string   `json:"display_name"`       // project free-form display name
	Workspace         string   `json:"workspace"`          // parent-tier workspace key
	WorkspaceName     string   `json:"workspace_name"`     // parent-tier free-form display name
	ResourcePrefix    string   `json:"resource_prefix"`    // Docker prefix ({workspaceKey}_{projectKey})
	Type              string   `json:"type"`
	Envs              []string `json:"envs"`
	ImageCount        int      `json:"image_count"`
	RunningContainers int      `json:"running_containers"`
	DiskMB            float64  `json:"disk_mb"`   // project directory size
	MemMB             float64  `json:"mem_mb"`    // sum of container RSS across all envs
}

// Collect gathers all stats from the local daemon.
func Collect(workspacesDir string) Stats {
	return CollectWith(workspacesDir, getRunningContainersByProject(), containerMemByProject(), nil)
}

// CollectWith builds the dashboard stats using caller-supplied per-project running
// and memory maps (Phase 7: the bridge merges these across hosts). The Docker and
// Host sections still describe the local control plane.
//
// diskMB supplies per-workspace directory sizes (MB). It is passed in rather than
// computed here because `du` over a bind mount can take tens of seconds — the
// bridge serves cached sizes and refreshes them off the request path. A nil map
// means "compute synchronously" (used by the standalone Collect()).
func CollectWith(workspacesDir string, running map[string]int, mem map[string]float64, diskMB map[string]float64) Stats {
	return Stats{
		Docker:     collectDocker(),
		Host:       collectHost(),
		Workspaces: collectWorkspaces(workspacesDir, running, mem, diskMB),
	}
}

// ── Docker ─────────────────────────────────────────────────────────────────────

func collectDocker() DockerStats {
	out, err := exec.Command("docker", "info", "--format", "{{json .}}").Output()
	if err != nil {
		return DockerStats{Error: "docker info failed: " + err.Error()}
	}

	var info struct {
		ServerVersion     string `json:"ServerVersion"`
		ContainersRunning int    `json:"ContainersRunning"`
		ContainersPaused  int    `json:"ContainersPaused"`
		ContainersStopped int    `json:"ContainersStopped"`
		Images            int    `json:"Images"`
		Driver            string `json:"Driver"`
		DockerRootDir     string `json:"DockerRootDir"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return DockerStats{Error: "parse error: " + err.Error()}
	}

	return DockerStats{
		ServerVersion:     info.ServerVersion,
		ContainersRunning: info.ContainersRunning,
		ContainersStopped: info.ContainersStopped,
		ContainersPaused:  info.ContainersPaused,
		ImagesTotal:       info.Images,
		VolumesTotal:      countDockerObjects("volume"),
		NetworksTotal:     countDockerObjects("network"),
		StorageDriver:     info.Driver,
		DockerRootDir:     info.DockerRootDir,
	}
}

func countDockerObjects(kind string) int {
	out, err := exec.Command("docker", kind, "ls", "-q").Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// WorkspaceDiskMB returns the disk usage of a workspace directory in MB via
// `du -sk`. This can be slow on a bind mount (Docker Desktop especially), so the
// dashboard never calls it on the request path — the bridge caches the result
// and refreshes it in the background.
func WorkspaceDiskMB(wsPath string) float64 {
	out, err := exec.Command("du", "-sk", wsPath).Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	kb, _ := strconv.ParseFloat(fields[0], 64)
	return kb / 1024.0
}

// projectByContainerID maps each running container's ID to its stack key, used to
// attribute `docker stats` rows to a workspace_env stack. Compose containers carry
// com.docker.compose.project; Swarm tasks instead carry com.docker.stack.namespace.
// Both equal "{resource_prefix}_{env}" in Rigger, so either label attributes a
// container to the same stack (compose label preferred when both are present).
func projectByContainerID(ex executor.Executor) map[string]string {
	projectByID := make(map[string]string)
	psOut, err := ex.DockerOutput(executor.Spec{Args: []string{"ps", "--format", "{{.ID}} {{.Labels}}"}, Timeout: sampleTimeout})
	if err != nil {
		return projectByID
	}
	scanner := bufio.NewScanner(strings.NewReader(string(psOut)))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " ", 2)
		if len(parts) < 2 {
			continue
		}
		id := parts[0]
		var composeProj, swarmNs string
		for _, kv := range strings.Split(parts[1], ",") {
			kv = strings.TrimSpace(kv)
			if v, ok := strings.CutPrefix(kv, "com.docker.compose.project="); ok {
				composeProj = v
			} else if v, ok := strings.CutPrefix(kv, "com.docker.stack.namespace="); ok {
				swarmNs = v
			}
		}
		if composeProj != "" {
			projectByID[id] = composeProj
		} else if swarmNs != "" {
			projectByID[id] = swarmNs
		}
	}
	return projectByID
}

// matchProject resolves a (possibly shortened) docker stats ID to a project,
// tolerating the ID-length mismatch between `docker ps` and `docker stats`.
func matchProject(projectByID map[string]string, shortID string) string {
	for id, p := range projectByID {
		if strings.HasPrefix(id, shortID) || strings.HasPrefix(shortID, id[:min(len(id), 12)]) {
			return p
		}
	}
	return ""
}

// ProjectStats is per-compose-project resource usage, aggregated across all
// containers in the stack. CPUPct is the sum of container CPU% (can exceed 100
// on multi-core hosts); MemPct is stack memory as a share of total host memory.
type ProjectStats struct {
	CPUPct     float64 `json:"cpu_pct"`
	MemMB      float64 `json:"mem_mb"`
	MemPct     float64 `json:"mem_pct"`
	NetRxBytes float64 `json:"net_rx_bytes"` // cumulative received bytes (sum across containers)
	NetTxBytes float64 `json:"net_tx_bytes"` // cumulative transmitted bytes
	Containers int     `json:"containers"`
}

// ContainerStatsByProject returns per-project CPU%/memory/network by sampling
// `docker stats --no-stream`. Used by the Phase 6 alert evaluator and the
// metrics collector. Fields are pipe-delimited so the multi-token values
// (MemUsage "x / y", NetIO "rx / tx") parse unambiguously.
func ContainerStatsByProject() map[string]ProjectStats {
	return ContainerStatsByProjectFor(executor.Local{})
}

// ContainerStatsByProjectFor is ContainerStatsByProject against a specific host's
// daemon (Phase 7 multi-host). MemPct uses the control-plane memory total, so it
// is only meaningful for the local host; MemMB (absolute) is correct everywhere.
func ContainerStatsByProjectFor(ex executor.Executor) map[string]ProjectStats {
	result := make(map[string]ProjectStats)
	projectByID := projectByContainerID(ex)
	if len(projectByID) == 0 {
		return result
	}

	out, err := ex.DockerOutput(executor.Spec{Args: []string{"stats", "--no-stream",
		"--format", "{{.ID}}|{{.CPUPerc}}|{{.MemUsage}}|{{.NetIO}}"}, Timeout: sampleTimeout})
	if err != nil {
		return result
	}

	// Host total memory (MB) for the host-relative MemPct.
	memTotalKB, _ := readMeminfo()
	hostMemMB := float64(memTotalKB) / 1024.0

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "|")
		if len(parts) < 4 {
			continue
		}
		project := matchProject(projectByID, strings.TrimSpace(parts[0]))
		if project == "" {
			continue
		}
		ps := result[project]
		ps.CPUPct += parsePercent(parts[1])
		if mf := strings.Fields(parts[2]); len(mf) > 0 {
			ps.MemMB += parseMem(mf[0]) // first token = used
		}
		rx, tx := parseNetIO(parts[3])
		ps.NetRxBytes += rx
		ps.NetTxBytes += tx
		ps.Containers++
		result[project] = ps
	}

	if hostMemMB > 0 {
		for k, ps := range result {
			ps.MemPct = ps.MemMB / hostMemMB * 100.0
			result[k] = ps
		}
	}
	return result
}

// parsePercent parses a docker stats percentage like "12.34%" → 12.34.
func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseNetIO parses a docker stats NetIO value "1.2kB / 3.4MB" → (rx, tx) bytes.
func parseNetIO(s string) (rx, tx float64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseBytes(parts[0]), parseBytes(parts[1])
}

// parseBytes parses a size like "1.2kB", "3.4MiB", "512B" → bytes. Handles both
// decimal (kB/MB/GB) and binary (KiB/MiB/GiB) units docker may emit.
func parseBytes(s string) float64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mul    float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"kiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			return v * u.mul
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// ── Live per-project stats (dashboard near-real-time table) ─────────────────────

// ProjectLive is cheap, frequently-sampled resource usage for one compose
// project. No disk (du) or docker info — only docker stats + container counts —
// so the /api/live-stats endpoint can be polled every few seconds.
type ProjectLive struct {
	Running      int      `json:"running"`       // running container count
	Total        int      `json:"total"`         // total containers (running + stopped)
	ServiceNames []string `json:"service_names"` // distinct compose service names
	CPUPct       float64  `json:"cpu_pct"`       // summed container CPU%
	MemMB        float64  `json:"mem_mb"`        // summed container memory (MB)
	NetRxBytes   float64  `json:"net_rx_bytes"`  // cumulative received bytes
	NetTxBytes   float64  `json:"net_tx_bytes"`  // cumulative transmitted bytes
}

// LiveProjectStats returns live stats keyed by compose project name. The
// frontend aggregates these to workspaces using its known {workspace}_{env}
// project names.
func LiveProjectStats() map[string]ProjectLive {
	return LiveProjectStatsFor(executor.Local{})
}

// LiveProjectStatsFor is LiveProjectStats against a specific host's daemon
// (Phase 7 multi-host).
func LiveProjectStatsFor(ex executor.Executor) map[string]ProjectLive {
	cpu := ContainerStatsByProjectFor(ex)
	running := RunningByProjectFor(ex)
	total, services := containerCountsByProjectFor(ex)

	result := make(map[string]ProjectLive)
	for p, n := range total {
		r := result[p]
		r.Total = n
		r.ServiceNames = services[p]
		result[p] = r
	}
	for p, n := range running {
		r := result[p]
		r.Running = n
		result[p] = r
	}
	for p, s := range cpu {
		r := result[p]
		r.CPUPct = s.CPUPct
		r.MemMB = s.MemMB
		r.NetRxBytes = s.NetRxBytes
		r.NetTxBytes = s.NetTxBytes
		result[p] = r
	}
	return result
}

// containerCountsByProject returns, per compose project, the total container
// count (running + stopped) and the set of distinct compose service names, via a
// single `docker ps -a` call reading project + service labels.
func containerCountsByProjectFor(ex executor.Executor) (total map[string]int, services map[string][]string) {
	total = make(map[string]int)
	services = make(map[string][]string)
	seen := make(map[string]map[string]struct{}) // project → service set

	out, err := ex.DockerOutput(executor.Spec{Args: []string{"ps", "-a",
		"--format", `{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.service"}}|{{.Label "com.docker.stack.namespace"}}|{{.Label "com.docker.swarm.service.name"}}`}})
	if err != nil {
		return total, services
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		f := strings.SplitN(scanner.Text(), "|", 4)
		for len(f) < 4 {
			f = append(f, "")
		}
		// Compose carries project/service labels; Swarm tasks carry the stack
		// namespace + swarm service name instead. Both project keys equal
		// "{resource_prefix}_{env}".
		p := strings.TrimSpace(f[0])
		svc := strings.TrimSpace(f[1])
		if p == "" {
			p = strings.TrimSpace(f[2]) // stack namespace
			svc = strings.TrimSpace(f[3])
		}
		if p == "" {
			continue
		}
		total[p]++
		if svc == "" {
			continue
		}
		// Rigger names compose services with the project (workspace_env) prefix,
		// e.g. project "test_dev" → service "test_dev_adminer". Swarm service names
		// double-prefix (<stack>_<composeKey>), so strip the prefix repeatedly to
		// reach the logical short name ("adminer"), consistent across envs.
		for strings.HasPrefix(svc, p+"_") {
			svc = strings.TrimPrefix(svc, p+"_")
		}
		if seen[p] == nil {
			seen[p] = make(map[string]struct{})
		}
		if _, ok := seen[p][svc]; !ok {
			seen[p][svc] = struct{}{}
			services[p] = append(services[p], svc)
		}
	}
	return total, services
}

// containerMemByProject returns a map of compose project → total memory in MB.
func containerMemByProject() map[string]float64 {
	return MemByProjectFor(executor.Local{})
}

// MemByProjectFor is containerMemByProject against a specific host's daemon.
func MemByProjectFor(ex executor.Executor) map[string]float64 {
	result := make(map[string]float64)

	projectByID := projectByContainerID(ex)
	if len(projectByID) == 0 {
		return result
	}

	// Get memory stats per container
	statsOut, err := ex.DockerOutput(executor.Spec{Args: []string{"stats", "--no-stream", "--format", "{{.ID}} {{.MemUsage}}"}, Timeout: sampleTimeout})
	if err != nil {
		return result
	}
	scanner := bufio.NewScanner(strings.NewReader(string(statsOut)))
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) < 2 {
			continue
		}
		project := matchProject(projectByID, parts[0])
		if project == "" {
			continue
		}
		// MemUsage is like "123MiB / 4GiB" — parse the first value
		result[project] += parseMem(parts[1])
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseMem parses Docker memory strings like "123MiB", "1.5GiB", "512KiB" → MB.
func parseMem(s string) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "GiB") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "GiB"), 64)
		return v * 1024
	}
	if strings.HasSuffix(s, "MiB") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "MiB"), 64)
		return v
	}
	if strings.HasSuffix(s, "KiB") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "KiB"), 64)
		return v / 1024
	}
	if strings.HasSuffix(s, "MB") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "MB"), 64)
		return v
	}
	if strings.HasSuffix(s, "GB") {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "GB"), 64)
		return v * 1024
	}
	return 0
}

// getRunningContainersByProject returns a map of compose project → running container count.
// Uses `docker compose ls --format json` which is reliable and doesn't require
// parsing comma-separated label strings (which breaks when values contain commas).
func getRunningContainersByProject() map[string]int {
	return RunningByProjectFor(executor.Local{})
}

// RunningByProjectFor is getRunningContainersByProject against a specific host.
func RunningByProjectFor(ex executor.Executor) map[string]int {
	result := make(map[string]int)

	out, err := ex.DockerOutput(executor.Spec{Args: []string{"compose", "ls", "--all", "--format", "json"}})
	if err != nil {
		// Fallback: parse docker ps labels if compose ls is unavailable
		return getRunningByLabelsFor(ex)
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 || string(out) == "null" {
		return result
	}

	var projects []struct {
		Name   string `json:"Name"`
		Status string `json:"Status"` // e.g. "running(2)" or "exited(1)" or "running(1), exited(1)"
	}
	if err := json.Unmarshal(out, &projects); err != nil {
		return getRunningByLabelsFor(ex)
	}

	for _, p := range projects {
		count := parseRunningCount(p.Status)
		if count > 0 {
			result[p.Name] = count
		}
	}
	// `docker compose ls` doesn't list Swarm stacks — count their running task
	// containers by the stack-namespace label and merge (compose counts untouched).
	mergeSwarmRunning(ex, result)
	return result
}

// mergeSwarmRunning adds running Swarm task containers (keyed by their stack
// namespace = {resource_prefix}_{env}) into result. Containers that already carry
// a compose-project label are skipped (already counted via `compose ls`).
func mergeSwarmRunning(ex executor.Executor, result map[string]int) {
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"ps",
		"--format", `{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.stack.namespace"}}`}})
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "|", 2)
		if strings.TrimSpace(parts[0]) != "" || len(parts) < 2 {
			continue // compose container — already counted
		}
		if ns := strings.TrimSpace(parts[1]); ns != "" {
			result[ns]++
		}
	}
}

// parseRunningCount extracts the running container count from a docker compose ls status string.
// Status examples: "running(3)", "exited(2)", "running(2), exited(1)"
func parseRunningCount(status string) int {
	// Find "running(N)" anywhere in the string
	const prefix = "running("
	idx := strings.Index(strings.ToLower(status), prefix)
	if idx < 0 {
		return 0
	}
	rest := status[idx+len(prefix):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0
	}
	return n
}

// getRunningByLabelsFor is the fallback for older Docker versions without compose ls --format json.
func getRunningByLabelsFor(ex executor.Executor) map[string]int {
	result := make(map[string]int)
	// Use .Label template to get the project label cleanly (one per line, no comma issues)
	out, err := ex.DockerOutput(executor.Spec{Args: []string{"ps",
		"--format", `{{.Label "com.docker.compose.project"}}`}})
	if err != nil {
		return result
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		project := strings.TrimSpace(scanner.Text())
		if project != "" {
			result[project]++
		}
	}
	return result
}

// ── Host ───────────────────────────────────────────────────────────────────────

// Host returns current host metrics (CPU count, memory, disk, uptime).
// Exported for the Phase 6 alert evaluator's host-level disk condition.
func Host() HostStats { return collectHost() }

func collectHost() HostStats {
	h := HostStats{
		OS:   readOSName(),
		Arch: runCmd("uname", "-m"),
		CPUs: countCPUs(),
	}

	memTotal, memAvail := readMeminfo()
	h.MemTotalMB = float64(memTotal) / 1024.0
	h.MemAvailMB = float64(memAvail) / 1024.0
	if memTotal > 0 {
		h.MemUsedPct = float64(memTotal-memAvail) / float64(memTotal) * 100.0
	}

	// Disk usage of the host filesystem. Inside the container "/" is the
	// container's own root (overlay), which does not reliably reflect the host
	// disk. Operators bind-mount the host root and point HOST_FS_PATH at it
	// (see docker-compose.yml: `/:/host:ro` + HOST_FS_PATH=/host) so disk stats
	// and the disk_above_pct alert measure the real host. Falls back to "/" when
	// the mount is absent (older deployments) so we never report 0 silently.
	diskPath := os.Getenv("HOST_FS_PATH")
	if diskPath == "" {
		diskPath = "/"
	}
	var stat syscall.Statfs_t
	err := syscall.Statfs(diskPath, &stat)
	if err != nil && diskPath != "/" {
		err = syscall.Statfs("/", &stat) // host mount missing — fall back
	}
	if err == nil {
		bsize := uint64(stat.Bsize)
		total := stat.Blocks * bsize
		// df's three numbers: used counts the reserved blocks (Blocks-Bfree),
		// available excludes them (Bavail). Percentage is used/(used+avail) — see
		// DiskPercent for why this must match the remote collector exactly.
		avail := stat.Bavail * bsize
		used := (stat.Blocks - stat.Bfree) * bsize
		h.DiskTotalGB = float64(total) / 1e9
		h.DiskUsedGB = float64(used) / 1e9
		h.DiskFreeGB = float64(avail) / 1e9
		h.DiskUsedPct = DiskPercent(float64(used), float64(avail))
	}

	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			h.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
		}
	}

	return h
}

func readOSName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "Linux"
}

func countCPUs() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "processor") {
			count++
		}
	}
	return count
}

// readMeminfo returns MemTotal and MemAvailable in kB.
func readMeminfo() (total, available int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			available = val
		}
	}
	return
}

func runCmd(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── Workspaces ─────────────────────────────────────────────────────────────────

func collectWorkspaces(workspacesDir string, projectContainers map[string]int, projectMemory map[string]float64, diskMB map[string]float64) WorkspaceSummary {
	summary := WorkspaceSummary{
		ByType:     make(map[string]int),
		Workspaces: []WorkspaceInfo{},
	}

	wsEntries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return summary
	}

	// Workspace → Project → Environment: iterate each workspace's projects/ dir.
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		wsName := we.Name()
		projEntries, perr := os.ReadDir(filepath.Join(workspacesDir, wsName, "projects"))
		if perr != nil {
			continue // not a workspace dir (no projects/) — skip
		}
		// Parent-tier display name from workspace.json (falls back to the key).
		wsDisplay := wsName
		if md, err := os.ReadFile(filepath.Join(workspacesDir, wsName, "workspace.json")); err == nil {
			var m struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(md, &m) == nil && m.Name != "" {
				wsDisplay = m.Name
			}
		}

		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			projName := pe.Name()
			cfgPath := filepath.Join(workspacesDir, wsName, "projects", projName, "config.json")
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				continue
			}

			var cfg struct {
				Project struct {
					Name           string `json:"name"`
					Type           string `json:"type"`
					ResourcePrefix string `json:"resource_prefix"`
				} `json:"project"`
				Images       []json.RawMessage          `json:"images"`
				Environments map[string]json.RawMessage `json:"environments"`
			}
			if json.Unmarshal(data, &cfg) != nil {
				continue
			}
			projDisplay := cfg.Project.Name
			if projDisplay == "" {
				projDisplay = projName
			}

			wsType := cfg.Project.Type
			if wsType == "" {
				wsType = "custom"
			}
			prefix := cfg.Project.ResourcePrefix
			if prefix == "" {
				prefix = wsName + "_" + projName
			}

			envNames := make([]string, 0, len(cfg.Environments))
			for k := range cfg.Environments {
				envNames = append(envNames, k)
			}

			// Aggregate per-env metrics across all environments. The compose
			// project label is "{resource_prefix}_{env}".
			runningTotal := 0
			memTotal := 0.0
			for _, env := range envNames {
				key := prefix + "_" + env
				runningTotal += projectContainers[key]
				memTotal += projectMemory[key]
			}

			// Disk size: prefer the caller-supplied (cached) value; fall back to a
			// synchronous du only when no map was provided (nil = standalone Collect).
			var diskVal float64
			if diskMB != nil {
				diskVal = diskMB[wsName+"/"+projName]
			} else {
				diskVal = WorkspaceDiskMB(filepath.Join(workspacesDir, wsName, "projects", projName))
			}

			summary.Total++
			summary.ByType[wsType]++
			summary.Workspaces = append(summary.Workspaces, WorkspaceInfo{
				Name:              projName,
				DisplayName:       projDisplay,
				Workspace:         wsName,
				WorkspaceName:     wsDisplay,
				ResourcePrefix:    prefix,
				Type:              wsType,
				Envs:              envNames,
				ImageCount:        len(cfg.Images),
				RunningContainers: runningTotal,
				DiskMB:            diskVal,
				MemMB:             memTotal,
			})
		}
	}
	return summary
}
