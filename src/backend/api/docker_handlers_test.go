package api

import "testing"

func TestSplitInfoLine(t *testing.T) {
	osType, opsys, ver, id := splitInfoLine("linux|Ubuntu 22.04.3 LTS|24.0.7|ABCD:EFGH")
	if osType != "linux" || opsys != "Ubuntu 22.04.3 LTS" || ver != "24.0.7" || id != "ABCD:EFGH" {
		t.Fatalf("unexpected parse: %q %q %q %q", osType, opsys, ver, id)
	}
	// Short line (missing trailing fields) must not panic.
	if o, _, _, _ := splitInfoLine("linux"); o != "linux" {
		t.Fatalf("short line: %q", o)
	}
}

func TestParseDockerProbe(t *testing.T) {
	out := "@@INFO@@\nlinux|Ubuntu 22.04|24.0.7|ID123\n@@SUDO@@\nyes\n@@END@@\n"
	p := parseDockerProbe(out)
	if p.osType != "linux" || p.operatingSystem != "Ubuntu 22.04" || p.serverVersion != "24.0.7" || p.daemonID != "ID123" {
		t.Fatalf("info parse: %+v", p)
	}
	if !p.sudoOK {
		t.Fatal("expected sudoOK for 'yes'")
	}
	if parseDockerProbe("@@INFO@@\nlinux|x|1|i\n@@SUDO@@\nno\n@@END@@\n").sudoOK {
		t.Fatal("'no' must not be sudoOK")
	}
	if !parseDockerProbe("@@INFO@@\nlinux|x|1|i\n@@SUDO@@\nroot\n@@END@@\n").sudoOK {
		t.Fatal("'root' must be sudoOK")
	}
}

func TestParseDockerPreflight(t *testing.T) {
	out := "@@INFO@@\nlinux|Ubuntu 22.04|24.0.7|ID9\n@@TOOLS@@\ncurl\n@@SUDO@@\nyes\n@@END@@\n"
	pf := parseDockerPreflight(out)
	if !pf.hasDownloader || !pf.sudoOK || pf.desktop || pf.osType != "linux" || pf.daemonID != "ID9" {
		t.Fatalf("preflight: %+v", pf)
	}
	// Docker Desktop must be flagged (and not updatable).
	d := parseDockerPreflight("@@INFO@@\nlinux|Docker Desktop|24.0.7|ID9\n@@TOOLS@@\n@@SUDO@@\nno\n@@END@@\n")
	if !d.desktop {
		t.Fatal("expected desktop flag")
	}
	if d.hasDownloader {
		t.Fatal("no tools listed → hasDownloader must be false")
	}
}

func TestParseUpdateStatus(t *testing.T) {
	out := "@@LOG@@\nfetching...\nRIGGER_UPDATE_DONE rc=0\n@@VER@@\n25.0.1\n@@END@@\n"
	logText, ver := parseUpdateStatus(out)
	if ver != "25.0.1" {
		t.Fatalf("version: %q", ver)
	}
	if want := "RIGGER_UPDATE_DONE rc=0"; !contains(logText, want) {
		t.Fatalf("log missing sentinel: %q", logText)
	}
}

func TestIsDockerDesktop(t *testing.T) {
	if !isDockerDesktop("Docker Desktop") || !isDockerDesktop("docker desktop 4.x") {
		t.Fatal("should match Docker Desktop")
	}
	if isDockerDesktop("Ubuntu 22.04.3 LTS") {
		t.Fatal("real distro must not match")
	}
}

func TestEngineUpdateAvailable(t *testing.T) {
	if !engineUpdateAvailable("v27.3.1", "24.0.7") {
		t.Fatal("newer engine should be available")
	}
	if engineUpdateAvailable("v24.0.7", "24.0.7") {
		t.Fatal("same version is not an update")
	}
	// Equal versions written differently must NOT report an update.
	if engineUpdateAvailable("v29.6.1", "29.6.1") {
		t.Fatal("v29.6.1 vs 29.6.1 are equal — no update")
	}
	// A non-semver tag must never be guessed as an update (no string-differ fallback).
	if engineUpdateAvailable("docker-v29.6.1", "29.6.1") {
		t.Fatal("unparseable tag must not report an update")
	}
	if engineUpdateAvailable("", "24.0.7") || engineUpdateAvailable("v27.0.0", "") {
		t.Fatal("empty inputs must be false")
	}
}

func TestNormalizeDockerTag(t *testing.T) {
	if got := normalizeDockerTag("docker-v29.6.1"); got != "v29.6.1" {
		t.Fatalf("normalize moby tag: %q", got)
	}
	if got := normalizeDockerTag("v27.3.1"); got != "v27.3.1" {
		t.Fatalf("plain tag unchanged: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
