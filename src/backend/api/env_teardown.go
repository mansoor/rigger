package api

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/mansoor/rigger/ui/internal/shell"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// removeEnv fully tears down and deletes a single environment: it brings the
// stack down (optionally purging its data volumes) while config.json still
// resolves the env, then removes the env's directory.
//
// This is the shared form of the inline teardown that PutConfig has always run
// for environments dropped from config.json (down → overwrite config →
// os.RemoveAll(envs/<env>), the 122ad60 orphan-cleanup that honors the
// env-list-union invariant). PutConfig calls it with purgeVolumes=false so a
// user-driven env delete keeps its data volumes — deleting an env must not
// destroy data. The preview controller calls it with purgeVolumes=true so the
// per-PR DB/Redis/storage volumes don't accumulate one set per closed PR.
//
// Teardown is best-effort: a `down` failure is logged but does not abort the
// directory removal, so a half-gone env still gets cleaned up rather than
// lingering on the workspace page (visible in config-or-dir, un-actionable).
// The caller is responsible for any config.json / DB-record changes; this
// helper only touches the running stack and the on-disk env dir.
func (h *Handler) removeEnv(ws, proj, env string, purgeVolumes bool) {
	if env == "" || strings.ContainsAny(env, "/\\.") {
		return
	}
	var out bytes.Buffer
	if err := h.bridge.Run(shell.RunOptions{
		Workspace:    ws,
		Project:      proj,
		Command:      "down",
		Env:          env,
		PurgeVolumes: purgeVolumes,
		Stdout:       &out,
		Stderr:       &out,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "removeEnv: tear down %s/%s/%s: %v\n%s", ws, proj, env, err, out.String())
	}
	os.RemoveAll(wspath.EnvDir(h.workspacesDir, ws, proj, env)) //nolint:errcheck
}
