package pipelines

import (
	"bytes"
	"strings"
	"testing"
)

// Verbatim tail of the build stage that prompted this — the warning sits under
// ~60 lines of BuildKit output and above a green tick, which is exactly why it
// went unread.
const buildTail = `#14 DONE 10.5s
=== Successfully Built! ===
✓ Built: localhost:5001/mcl_sdm-app:1.0.0-build.7-dev
⚑ ⚠ app built, but its dependencies can't run on the PHP in the image:
⚑     Fatal error: Composer detected issues in your platform: Your Composer dependencies require a PHP version ">= 8.3.0". You are running 8.2.27.
⚑   Nixpacks picks PHP from composer.json's "require": {"php": ...} constraint.
⚑   Raise it (e.g. "^8.3") and rebuild — the app will keep failing at runtime until then.
✓ Advanced 1 image pointer(s) → 1.0.0-build.7`

func TestExtractWarningsFromRealBuildOutput(t *testing.T) {
	got := extractWarnings(buildTail)
	if len(got) == 0 {
		t.Fatal("the warning was not extracted")
	}
	// The log prefix is stripped so the summary reads as a warning, not a log line.
	if !strings.HasPrefix(got[0], "⚠") {
		t.Errorf("first line should start at the marker, got %q", got[0])
	}
	joined := strings.Join(got, "\n")
	// The detail and the fix are the useful half — they must survive.
	for _, want := range []string{"8.3.0", "8.2.27", "composer.json", "Raise it"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q to be carried through, got:\n%s", want, joined)
		}
	}
	// One problem, not four.
	if n := countWarningHeadlines(got); n != 1 {
		t.Errorf("countWarningHeadlines = %d, want 1", n)
	}
	// Ordinary build output must not be dragged in.
	for _, unwanted := range []string{"#14 DONE", "Successfully Built", "Advanced 1 image"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("unrelated output leaked into the warning: %q", unwanted)
		}
	}
}

func TestExtractWarningsStripsColour(t *testing.T) {
	got := extractWarnings("\x1b[33m⚠ something\x1b[0m")
	if len(got) != 1 || got[0] != "⚠ something" {
		t.Fatalf("colour codes should be stripped, got %q", got)
	}
}

func TestExtractWarningsCleanOutput(t *testing.T) {
	if got := extractWarnings("#1 DONE\n✓ Built: x\n✓ ok\n"); len(got) != 0 {
		t.Fatalf("a clean stage must produce no warnings, got %q", got)
	}
}

func TestExtractWarningsMultiple(t *testing.T) {
	const out = `⚠ first problem
  do this to fix it
some unrelated line
⚠ second problem`
	got := extractWarnings(out)
	if n := countWarningHeadlines(got); n != 2 {
		t.Fatalf("expected 2 headlines, got %d from %q", n, got)
	}
	if strings.Contains(strings.Join(got, "\n"), "unrelated") {
		t.Error("an unindented line ends the warning; it must not be captured")
	}
}

func TestWarningSummarySilentWhenClean(t *testing.T) {
	var b bytes.Buffer
	writeWarningSummary(&b, []StageResult{{Label: "build dev", Status: "ok"}})
	if b.Len() != 0 {
		t.Fatalf("a clean run must add nothing, got %q", b.String())
	}
}

func TestWarningSummaryRepeatsAtEnd(t *testing.T) {
	var b bytes.Buffer
	writeWarningSummary(&b, []StageResult{
		{Label: "build dev", Status: "ok", Warnings: extractWarnings(buildTail)},
		{Label: "deploy dev", Status: "ok"},
	})
	s := b.String()
	if !strings.Contains(s, "1 warning") {
		t.Errorf("summary should count the warnings, got:\n%s", s)
	}
	// It has to name the stage — a run has many, and "which one" is the first
	// question anyone asks.
	if !strings.Contains(s, "build dev") {
		t.Errorf("summary should name the stage, got:\n%s", s)
	}
	if !strings.Contains(s, "8.3.0") {
		t.Errorf("summary should carry the detail, got:\n%s", s)
	}
	if strings.Contains(s, "deploy dev") {
		t.Error("a stage with no warnings should not appear in the summary")
	}
}

func TestWarningSummaryPluralises(t *testing.T) {
	var b bytes.Buffer
	writeWarningSummary(&b, []StageResult{
		{Label: "a", Warnings: []string{"⚠ one"}},
		{Label: "b", Warnings: []string{"⚠ two"}},
	})
	if !strings.Contains(b.String(), "2 warnings") {
		t.Errorf("expected a plural count, got:\n%s", b.String())
	}
}
