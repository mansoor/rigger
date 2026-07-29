package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// stubExec returns canned output and counts calls. The package's `recorder`
// can't drive this — platformIssues is all about what the command PRINTS.
type stubExec struct {
	out   string
	calls int
}

func (s *stubExec) Docker(executor.Spec) error { s.calls++; return nil }
func (s *stubExec) DockerOutput(executor.Spec) ([]byte, error) {
	s.calls++
	// A failing guard exits non-zero, so return an error alongside the output —
	// the real path must key on the text, not the status.
	return []byte(s.out), nil
}

// The check must not cost a container start for the many builds that aren't PHP.
func TestIsComposerApp(t *testing.T) {
	dir := t.TempDir()
	if isComposerApp(dir) {
		t.Fatal("a directory with no composer.json is not a Composer app")
	}
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isComposerApp(dir) {
		t.Fatal("composer.json present should mark it a Composer app")
	}
}

// The script has to find the app wherever the backend put it: Nixpacks uses
// /app, the Dockerfile templates a webroot.
func TestPlatformCheckScriptCoversBothRoots(t *testing.T) {
	for _, root := range []string{"/app", "/var/www/html"} {
		if !strings.Contains(platformCheckScript, root) {
			t.Errorf("script should probe %s", root)
		}
	}
	if !strings.Contains(platformCheckScript, noComposerMarker) {
		t.Error("script must emit the marker so 'no composer' is distinguishable from 'passed'")
	}
}

// Real Composer output, verbatim from the mcl_sdm image.
const composerIssue = `
Fatal error: Composer detected issues in your platform: Your Composer dependencies require a PHP version ">= 8.3.0". You are running 8.2.27. in /app/vendor/composer/platform_check.php on line 26`

func TestPlatformIssuesClassification(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool // treated as a real platform mismatch
	}{
		{"real composer mismatch", composerIssue, true},
		{"passed (composer prints nothing)", "", false},
		{"no composer install", noComposerMarker, false},
		{"unrelated failure", "sh: php: not found", false},
		{"image won't start", "docker: Error response from daemon: no such image", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Options{Exec: &stubExec{out: tc.out}}
			got := o.platformIssues("img:tag")
			if (got != "") != tc.want {
				t.Fatalf("platformIssues(%q) = %q, want mismatch=%v", tc.out, got, tc.want)
			}
			if tc.want && !strings.Contains(got, "8.3.0") {
				t.Errorf("the message should carry Composer's own detail, got %q", got)
			}
		})
	}
}

// A non-Composer context must not run anything at all.
func TestWarnPlatformMismatchSkipsNonComposer(t *testing.T) {
	fe := &stubExec{out: composerIssue}
	o := &Options{Exec: fe}
	o.warnPlatformMismatch("backend", "img:tag", t.TempDir(), true)
	if fe.calls != 0 {
		t.Fatalf("expected no docker calls for a non-Composer build, got %d", fe.calls)
	}
}
