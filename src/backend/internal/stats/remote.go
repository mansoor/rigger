package stats

import (
	"encoding/json"
	"strconv"
	"strings"
)

// RemoteRunner runs a shell command on a remote host and returns its combined
// output. It is satisfied by *remotehost.Client; kept as an interface here so the
// stats package does not import remotehost (avoiding an import cycle).
type RemoteRunner interface {
	RunCombined(cmd string) (string, error)
}

// remoteProbe gathers every data source collectDocker/collectHost read locally,
// in a single SSH round-trip. Each sub-command's stderr is dropped so it can't
// corrupt the section markers; empty sections are handled by the parsers.
const remoteProbe = `echo '@@DOCKERINFO@@'; docker info --format '{{json .}}' 2>/dev/null
echo '@@VOL@@'; docker volume ls -q 2>/dev/null
echo '@@NET@@'; docker network ls -q 2>/dev/null
echo '@@OSREL@@'; cat /etc/os-release 2>/dev/null
echo '@@ARCH@@'; uname -m 2>/dev/null
echo '@@CPUS@@'; nproc 2>/dev/null
echo '@@MEMINFO@@'; cat /proc/meminfo 2>/dev/null
echo '@@DF@@'; df -kP / 2>/dev/null
echo '@@UPTIME@@'; cat /proc/uptime 2>/dev/null
echo '@@END@@'`

// CollectRemote gathers Docker and host metrics from a remote host over SSH,
// parsing the same fields the local collectors produce. A failed SSH call yields
// a DockerStats carrying the error and a zero HostStats.
func CollectRemote(r RemoteRunner) (DockerStats, HostStats) {
	out, err := r.RunCombined(remoteProbe)
	if err != nil && strings.TrimSpace(out) == "" {
		return DockerStats{Error: "remote probe failed: " + err.Error()}, HostStats{}
	}
	sec := splitSections(out)
	return parseRemoteDocker(sec["DOCKERINFO"], sec["VOL"], sec["NET"]),
		parseRemoteHost(sec["OSREL"], sec["ARCH"], sec["CPUS"], sec["MEMINFO"], sec["DF"], sec["UPTIME"])
}

// splitSections slices the probe output on `@@NAME@@` marker lines, returning a
// map of section name → body (everything up to the next marker).
func splitSections(out string) map[string]string {
	sections := map[string]string{}
	var cur string
	var buf []string
	flush := func() {
		if cur != "" {
			sections[cur] = strings.Join(buf, "\n")
		}
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "@@") && strings.HasSuffix(t, "@@") && len(t) > 4 {
			flush()
			cur = t[2 : len(t)-2]
			buf = buf[:0]
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return sections
}

func parseRemoteDocker(info, vol, net string) DockerStats {
	info = strings.TrimSpace(info)
	if info == "" {
		return DockerStats{Error: "docker not reachable on host"}
	}
	var d struct {
		ServerVersion     string `json:"ServerVersion"`
		ContainersRunning int    `json:"ContainersRunning"`
		ContainersPaused  int    `json:"ContainersPaused"`
		ContainersStopped int    `json:"ContainersStopped"`
		Images            int    `json:"Images"`
		Driver            string `json:"Driver"`
		DockerRootDir     string `json:"DockerRootDir"`
	}
	if err := json.Unmarshal([]byte(info), &d); err != nil {
		return DockerStats{Error: "parse remote docker info: " + err.Error()}
	}
	return DockerStats{
		ServerVersion:     d.ServerVersion,
		ContainersRunning: d.ContainersRunning,
		ContainersStopped: d.ContainersStopped,
		ContainersPaused:  d.ContainersPaused,
		ImagesTotal:       d.Images,
		VolumesTotal:      countNonEmptyLines(vol),
		NetworksTotal:     countNonEmptyLines(net),
		StorageDriver:     d.Driver,
		DockerRootDir:     d.DockerRootDir,
	}
}

func parseRemoteHost(osrel, arch, cpus, meminfo, df, uptime string) HostStats {
	h := HostStats{
		OS:   osPretty(osrel),
		Arch: strings.TrimSpace(arch),
		CPUs: atoiTrim(cpus),
	}
	total, avail := parseMeminfoKB(meminfo)
	h.MemTotalMB = float64(total) / 1024.0
	h.MemAvailMB = float64(avail) / 1024.0
	if total > 0 {
		h.MemUsedPct = float64(total-avail) / float64(total) * 100.0
	}
	parseDF(df, &h)
	if f := strings.Fields(uptime); len(f) > 0 {
		h.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
	}
	return h
}

// osPretty extracts PRETTY_NAME from /etc/os-release content, defaulting to "Linux".
func osPretty(osrel string) string {
	for _, line := range strings.Split(osrel, "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "Linux"
}

// parseMeminfoKB reads MemTotal/MemAvailable (kB) from /proc/meminfo content.
func parseMeminfoKB(meminfo string) (total, avail int64) {
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	return
}

// parseDF reads `df -kP /` output (1024-byte blocks) into disk fields. The data
// row is the last non-empty line, tolerating a wrapped long filesystem name.
func parseDF(df string, h *HostStats) {
	lines := strings.Split(strings.TrimSpace(df), "\n")
	if len(lines) < 2 {
		return
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return
	}
	const k = 1024.0
	blocks := atofTrim(f[1])
	used := atofTrim(f[2])
	avail := atofTrim(f[3])
	h.DiskTotalGB = blocks * k / 1e9
	h.DiskUsedGB = used * k / 1e9
	h.DiskFreeGB = avail * k / 1e9
	if blocks > 0 {
		h.DiskUsedPct = used / blocks * 100.0
	}
}

func countNonEmptyLines(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func atoiTrim(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atofTrim(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }
