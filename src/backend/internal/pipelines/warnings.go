package pipelines

// Surfacing warnings from stages that SUCCEEDED.
//
// A stage can finish green and still have told you something important — a build
// that produces a working image but whose dependencies can't run on the PHP in
// it, say. That warning is printed mid-stage, under a hundred lines of BuildKit
// output, above a green "✓ build dev ok". In practice nobody reads it: the run
// is green, so the log stays collapsed.
//
// So warnings are extracted from a successful stage's output, attached to the
// result, and repeated in a block at the END of the run — where the eye actually
// lands. The UI can then mark the stage too, without the log being expanded.

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ansi strips colour codes so a warning reads cleanly when repeated in the
// summary (stage output carries the escapes the terminal view renders).
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// warnMarker is the convention every Rigger subsystem already uses for an
// advisory line. Matching the marker rather than a per-subsystem pattern means a
// new warning anywhere shows up here for free.
const warnMarker = "⚠"

// logPrefixes are the markers Rigger's own output helpers stamp on every line
// they emit. They have to come off before indentation means anything: the
// builder's info() prefixes its continuation lines too, so "⚑   Raise it…" is an
// indented detail line wearing a log prefix, not a new top-level message.
var logPrefixes = []string{"⚑", "✓", "⟳", "✗", "■", "⏸", "⚠"}

// stripLogPrefix removes one leading marker (and the space after it), returning
// the remainder and whether anything was stripped.
func stripLogPrefix(line string) string {
	s := strings.TrimLeft(line, " \t")
	for _, p := range logPrefixes {
		if strings.HasPrefix(s, p+" ") {
			// Keep the leading spaces of the remainder — that's the indentation
			// that distinguishes a continuation from a new message.
			return strings.TrimPrefix(s, p+" ")
		}
	}
	return line
}

// extractWarnings pulls the ⚠ lines out of a stage's captured output.
//
// Continuation lines are kept: a warning is usually a headline plus indented
// detail (what's wrong, then how to fix it), and the fix is the useful half.
// A continuation is an indented line directly following a warning — indented
// AFTER its log prefix is removed.
func extractWarnings(output string) []string {
	var out []string
	inWarning := false
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(ansi.ReplaceAllString(raw, ""), " \t\r")
		if strings.TrimSpace(line) == "" {
			inWarning = false
			continue
		}
		if i := strings.Index(line, warnMarker); i >= 0 {
			// Start at the marker so the summary reads as a warning, not a log line.
			out = append(out, line[i:])
			inWarning = true
			continue
		}
		body := stripLogPrefix(line)
		if inWarning && (strings.HasPrefix(body, " ") || strings.HasPrefix(body, "\t")) {
			out = append(out, strings.TrimRight(body, " "))
			continue
		}
		inWarning = false
	}
	return out
}

// countWarningHeadlines counts the warnings themselves, ignoring their
// continuation lines — so "1 warning" means one problem, not one problem
// described over four lines.
func countWarningHeadlines(warnings []string) int {
	n := 0
	for _, w := range warnings {
		if strings.Contains(w, warnMarker) {
			n++
		}
	}
	return n
}

// writeWarningSummary repeats every stage warning at the end of a run. No-op
// when the run was clean, so a normal pipeline gains nothing to scroll past.
func writeWarningSummary(out io.Writer, results []StageResult) {
	total := 0
	for _, r := range results {
		total += countWarningHeadlines(r.Warnings)
	}
	if total == 0 {
		return
	}

	noun := "warning"
	if total > 1 {
		noun = "warnings"
	}
	fmt.Fprintf(out, "\n\033[1;33m━━ %d %s ━━\033[0m\n", total, noun)
	fmt.Fprintf(out, "\033[33mThe pipeline succeeded, but these need attention:\033[0m\n")
	for _, r := range results {
		if len(r.Warnings) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n\033[33m%s\033[0m\n", r.Label)
		for _, w := range r.Warnings {
			fmt.Fprintf(out, "\033[33m  %s\033[0m\n", strings.TrimSpace(w))
		}
	}
}
