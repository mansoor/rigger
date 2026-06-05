package stats

import "testing"

// fakeRunner returns a canned probe output, simulating the SSH round-trip.
type fakeRunner struct{ out string }

func (f fakeRunner) RunCombined(string) (string, error) { return f.out, nil }

const sampleProbe = `@@DOCKERINFO@@
{"ServerVersion":"26.1.0","ContainersRunning":3,"ContainersPaused":0,"ContainersStopped":2,"Images":7,"Driver":"overlay2","DockerRootDir":"/var/lib/docker"}
@@VOL@@
vol_a
vol_b
@@NET@@
bridge
host
none
@@OSREL@@
NAME="Ubuntu"
PRETTY_NAME="Ubuntu 22.04.4 LTS"
VERSION_ID="22.04"
@@ARCH@@
x86_64
@@CPUS@@
4
@@MEMINFO@@
MemTotal:        8174204 kB
MemFree:         1234567 kB
MemAvailable:    4096000 kB
@@DF@@
Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/sda1         51474044  20589616  28851000      42% /
@@UPTIME@@
123456.78 654321.00
@@END@@
`

func TestCollectRemote(t *testing.T) {
	d, h := CollectRemote(fakeRunner{out: sampleProbe})

	if d.Error != "" {
		t.Fatalf("unexpected docker error: %s", d.Error)
	}
	if d.ServerVersion != "26.1.0" {
		t.Errorf("server_version = %q, want 26.1.0", d.ServerVersion)
	}
	if d.ContainersRunning != 3 || d.ContainersStopped != 2 {
		t.Errorf("containers running/stopped = %d/%d, want 3/2", d.ContainersRunning, d.ContainersStopped)
	}
	if d.ImagesTotal != 7 {
		t.Errorf("images = %d, want 7", d.ImagesTotal)
	}
	if d.VolumesTotal != 2 {
		t.Errorf("volumes = %d, want 2", d.VolumesTotal)
	}
	if d.NetworksTotal != 3 {
		t.Errorf("networks = %d, want 3", d.NetworksTotal)
	}
	if d.StorageDriver != "overlay2" {
		t.Errorf("storage_driver = %q, want overlay2", d.StorageDriver)
	}

	if h.OS != "Ubuntu 22.04.4 LTS" {
		t.Errorf("os = %q, want Ubuntu 22.04.4 LTS", h.OS)
	}
	if h.Arch != "x86_64" {
		t.Errorf("arch = %q, want x86_64", h.Arch)
	}
	if h.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", h.CPUs)
	}
	// MemTotal 8174204 kB → ~7982 MB; used% = (total-avail)/total
	if h.MemTotalMB < 7900 || h.MemTotalMB > 8100 {
		t.Errorf("mem_total_mb = %.1f, want ~7982", h.MemTotalMB)
	}
	wantMemPct := float64(8174204-4096000) / 8174204 * 100
	if diff := h.MemUsedPct - wantMemPct; diff > 0.1 || diff < -0.1 {
		t.Errorf("mem_used_pct = %.2f, want %.2f", h.MemUsedPct, wantMemPct)
	}
	// Disk: 51474044 KiB blocks → ~52.7 GB; used% ≈ 40
	if h.DiskTotalGB < 52 || h.DiskTotalGB > 53 {
		t.Errorf("disk_total_gb = %.2f, want ~52.7", h.DiskTotalGB)
	}
	if h.DiskUsedPct < 39 || h.DiskUsedPct > 41 {
		t.Errorf("disk_used_pct = %.2f, want ~40", h.DiskUsedPct)
	}
	if h.UptimeSeconds != 123456.78 {
		t.Errorf("uptime = %f, want 123456.78", h.UptimeSeconds)
	}
}

func TestCollectRemoteDockerDown(t *testing.T) {
	// docker info empty (daemon unreachable) but host probes succeed.
	const probe = `@@DOCKERINFO@@
@@VOL@@
@@NET@@
@@OSREL@@
PRETTY_NAME="Alpine Linux v3.20"
@@ARCH@@
aarch64
@@CPUS@@
2
@@MEMINFO@@
MemTotal:        2048000 kB
MemAvailable:    1024000 kB
@@DF@@
Filesystem     1024-blocks    Used Available Capacity Mounted on
overlay           10485760 5242880   5242880      50% /
@@UPTIME@@
500.0 0
@@END@@`
	d, h := CollectRemote(fakeRunner{out: probe})
	if d.Error == "" {
		t.Error("expected docker error when info is empty")
	}
	if h.OS != "Alpine Linux v3.20" || h.CPUs != 2 {
		t.Errorf("host parse failed: os=%q cpus=%d", h.OS, h.CPUs)
	}
	if h.DiskUsedPct < 49 || h.DiskUsedPct > 51 {
		t.Errorf("disk_used_pct = %.2f, want ~50", h.DiskUsedPct)
	}
}
