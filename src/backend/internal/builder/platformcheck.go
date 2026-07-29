package builder

// Post-build platform verification for PHP/Composer apps.
//
// Nixpacks installs dependencies with `composer install --ignore-platform-reqs`.
// That is deliberate on its part — it lets a build finish even when the platform
// isn't fully resolved yet — but it means the build goes GREEN when a dependency
// requires a newer PHP (or an extension) than the one Nixpacks selected. Nothing
// says a word until the first request, which dies with:
//
//	Fatal error: Composer detected issues in your platform: Your Composer
//	dependencies require a PHP version ">= 8.3.0". You are running 8.2.27.
//
// Composer already writes the authoritative check into every install as
// vendor/composer/platform_check.php, so rather than re-deriving the answer from
// composer.json we simply run its file inside the image we just built. That
// covers missing extensions and any future platform requirement for free, and it
// can never disagree with what the app will do at runtime.
//
// Advisory only: it prints and moves on. A build that produced a real image is
// still a build, and the operator may be mid-upgrade.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// noComposerMarker distinguishes "this image has no Composer install" (nothing to
// check, stay silent) from "the check ran and passed" (also silent). Without it
// an empty result would be ambiguous.
const noComposerMarker = "__RIGGER_NO_COMPOSER__"

// platformCheckScript runs Composer's guard from wherever the app landed —
// Nixpacks builds into /app, the Dockerfile templates use a webroot layout. Its
// own output is the message we want, so nothing here reformats it.
const platformCheckScript = `for d in /app /var/www/html; do
  if [ -f "$d/vendor/composer/platform_check.php" ]; then php "$d/vendor/composer/platform_check.php" 2>&1; exit 0; fi
done
echo ` + noComposerMarker

// isComposerApp reports whether the build context is a PHP/Composer project.
// Checked on disk BEFORE starting a container: the vast majority of builds are
// not PHP, and running a throwaway container after every single one — including
// over SSH to a remote build host — would be a real cost for nothing.
func isComposerApp(ctxDir string) bool {
	_, err := os.Stat(filepath.Join(ctxDir, "composer.json"))
	return err == nil
}

// platformIssues runs the guard inside imgTag and returns Composer's complaint,
// or "" when the image is fine, has no Composer install, or can't be inspected.
//
// Errors are swallowed on purpose: this runs after a successful build, and a
// docker hiccup here must not turn a good build into a failure.
func (o *Options) platformIssues(imgTag string) string {
	out, err := executor.Default(o.Exec).DockerOutput(executor.Spec{
		Args: []string{"run", "--rm", "--entrypoint", "sh", imgTag, "-c", platformCheckScript},
	})
	// A failing guard exits non-zero (E_USER_ERROR ⇒ 255), so err is expected on a
	// real mismatch — the output is what matters, not the exit status.
	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(text, noComposerMarker) {
		return ""
	}
	if !strings.Contains(text, "Composer detected issues") {
		// Something else went wrong (image won't start, no php on PATH). Not our
		// business to report — a broken image will announce itself at deploy.
		_ = err
		return ""
	}
	return text
}

// warnPlatformMismatch prints an actionable warning when the freshly-built image
// can't actually run its own dependencies. No-op for anything that isn't a
// Composer project.
func (o *Options) warnPlatformMismatch(svcName, imgTag, ctxDir string, nixpacks bool) {
	if !isComposerApp(ctxDir) {
		return
	}
	issues := o.platformIssues(imgTag)
	if issues == "" {
		return
	}
	o.info("⚠ %s built, but its dependencies can't run on the PHP in the image:", svcName)
	for _, line := range strings.Split(issues, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			o.info("    %s", line)
		}
	}
	if nixpacks {
		// Nixpacks picks the PHP package from composer.json's own constraint, so that
		// is where the fix belongs — and it is usually just out of date relative to
		// what the dependencies grew to need.
		o.info("  Nixpacks picks PHP from composer.json's \"require\": {\"php\": ...} constraint.")
		o.info("  Raise it (e.g. \"^8.3\") and rebuild — the app will keep failing at runtime until then.")
	} else {
		o.info("  Raise the PHP version in the service's Dockerfile and rebuild.")
	}
}
