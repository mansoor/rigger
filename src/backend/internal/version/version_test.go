package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

const base = `{
  "project": { "name": "myapp", "registry": "reg",
    "version": { "major": 1, "minor": 2, "patch": 3, "build": 4 } },
  "environments": { "prod": { "domain": "x" } }
}`

func readVersion(t *testing.T, path string) (maj, min, pat, bld int) {
	t.Helper()
	data, _ := os.ReadFile(path)
	var doc struct {
		Project struct {
			Version struct{ Major, Minor, Patch, Build int } `json:"version"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	v := doc.Project.Version
	return v.Major, v.Minor, v.Patch, v.Build
}

func TestCurrent(t *testing.T) {
	p := writeCfg(t, base)
	got, err := Current(p)
	if err != nil || got != "1.2.3-build.4" {
		t.Fatalf("Current = %q, %v; want 1.2.3-build.4", got, err)
	}
}

func TestBumpParts(t *testing.T) {
	cases := []struct {
		part string
		want string
		v    [4]int
	}{
		{"build", "1.2.3-build.5", [4]int{1, 2, 3, 5}},
		{"patch", "1.2.4-build.0", [4]int{1, 2, 4, 0}},
		{"minor", "1.3.0-build.0", [4]int{1, 3, 0, 0}},
		{"major", "2.0.0-build.0", [4]int{2, 0, 0, 0}},
	}
	for _, tc := range cases {
		p := writeCfg(t, base)
		got, err := Bump(p, tc.part)
		if err != nil || got != tc.want {
			t.Errorf("Bump(%s) = %q, %v; want %q", tc.part, got, err, tc.want)
		}
		maj, min, pat, bld := readVersion(t, p)
		if [4]int{maj, min, pat, bld} != tc.v {
			t.Errorf("Bump(%s) on-disk = %v, want %v", tc.part, [4]int{maj, min, pat, bld}, tc.v)
		}
	}
}

func TestBumpUnknownPart(t *testing.T) {
	p := writeCfg(t, base)
	if _, err := Bump(p, "epoch"); err == nil {
		t.Error("Bump(epoch) = nil error, want error")
	}
}

func TestSet(t *testing.T) {
	p := writeCfg(t, base)
	got, err := Set(p, "5.6.7-build.8")
	if err != nil || got != "5.6.7-build.8" {
		t.Fatalf("Set = %q, %v; want 5.6.7-build.8", got, err)
	}
	if maj, min, pat, bld := readVersion(t, p); [4]int{maj, min, pat, bld} != [4]int{5, 6, 7, 8} {
		t.Errorf("Set on-disk = %v, want [5 6 7 8]", [4]int{maj, min, pat, bld})
	}
}

func TestSetInvalid(t *testing.T) {
	p := writeCfg(t, base)
	for _, bad := range []string{"1.2.3", "1.2.3-4", "v1.2.3-build.4", "1.2-build.4"} {
		if _, err := Set(p, bad); err == nil {
			t.Errorf("Set(%q) = nil error, want error", bad)
		}
	}
}

func TestWritePreservesOtherData(t *testing.T) {
	p := writeCfg(t, base)
	if _, err := Bump(p, "minor"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	var doc struct {
		Project struct {
			Name     string `json:"name"`
			Registry string `json:"registry"`
		} `json:"project"`
		Environments map[string]any `json:"environments"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Project.Name != "myapp" || doc.Project.Registry != "reg" {
		t.Errorf("project fields lost: %+v", doc.Project)
	}
	if _, ok := doc.Environments["prod"]; !ok {
		t.Error("environments.prod lost after version write")
	}
}
