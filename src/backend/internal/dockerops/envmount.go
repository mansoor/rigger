package dockerops

// A mounted .env that the application can never read.
//
// `env_file_mount` is an absolute path INSIDE the image, and the two build
// backends put the application in different places: the Dockerfile templates use
// a webroot layout (/var/www/html), Nixpacks always builds into /app. The mount
// default is chosen when the Dockerfile template is picked, and switching a
// service to Nixpacks afterwards leaves it pointing at the old root.
//
// Nothing fails. Docker happily mounts the file at a path nothing reads, the
// container starts, and the app runs with no configuration at all — a Laravel app
// then dies on a missing APP_KEY, several layers away from the cause.
//
// So: on deploy, if a Nixpacks-built service mounts its .env outside the Nixpacks
// app root, say so. Advisory only — an app may legitimately read config from
// anywhere, so this never blocks a deploy.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mansoor/rigger/ui/internal/wsconfig"
)

// nixpacksAppRoot is where Nixpacks places the application in the built image.
const nixpacksAppRoot = "/app/"

// misplacedEnvMount returns a warning for a Nixpacks service whose .env mount
// lands outside the app root, or "" when there is nothing to say.
func misplacedEnvMount(svc wsconfig.Service) string {
	if svc.Build == nil || !svc.Build.IsNixpacks() {
		return "" // Dockerfile builds own their own layout
	}
	mount := strings.TrimSpace(svc.EnvFileMount)
	if mount == "" || strings.HasPrefix(mount, nixpacksAppRoot) {
		return ""
	}
	return fmt.Sprintf(
		"service %q builds with Nixpacks (app root %s) but mounts its .env at %s — the app will not find it there.\n"+
			"  Set the .env mount to %s.env in Edit Project → Services, then redeploy.",
		svc.Name, nixpacksAppRoot, mount, nixpacksAppRoot)
}

// warnMisplacedEnvMounts prints a warning per service whose .env can't be read.
// Takes the raw config bytes rather than dockerops' own trimmed config view, so
// this stays keyed to the canonical model. Unparseable config is ignored — the
// deploy itself will report that far more clearly.
func warnMisplacedEnvMounts(cfgBytes []byte, warn func(format string, a ...any)) {
	var cfg wsconfig.Config
	if json.Unmarshal(cfgBytes, &cfg) != nil {
		return
	}
	for _, svc := range cfg.Services {
		if msg := misplacedEnvMount(svc); msg != "" {
			warn("⚠ %s", msg)
		}
	}
}
