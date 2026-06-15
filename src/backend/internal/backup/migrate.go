package backup

import "fmt"

// runMigrate copies one environment's DATA into another within the same project
// (e.g. refresh staging from prod). It is built entirely on the existing
// backup/restore primitives:
//
//  1. (safety) back up the TARGET so the overwrite is reversible — unless skipped.
//  2. back up the SOURCE.
//  3. restore the source snapshot INTO the target (cross-env restore).
//
// opts.Env is the TARGET, opts.SourceEnv is the SOURCE. The source is never
// modified — only read. All three steps share opts.Timestamp, so the source and
// safety snapshots use the same dir name (they live under different env dirs, so
// no collision) and step 3 restores exactly the snapshot step 2 produced.
func runMigrate(opts Options, cfg *wsConfig) error {
	src, tgt := opts.SourceEnv, opts.Env
	if src == "" || tgt == "" {
		return fmt.Errorf("migrate requires both a source and a target environment")
	}
	if src == tgt {
		return fmt.Errorf("migrate source and target must differ (both %q)", src)
	}
	ts := opts.Timestamp
	if ts == "" {
		return fmt.Errorf("migrate requires a timestamp")
	}

	// Shared logger via a target-scoped ctx (writes to opts.Stdout).
	c := newCtx(opts, cfg)
	c.info("Migrate data: %s → %s (project %s)", src, tgt, c.project)

	// 1 — safety backup of the target (reversibility).
	if !opts.SkipTargetBackup {
		c.info("Step 1/3 — Safety backup of target %q", tgt)
		bopts := opts
		bopts.Command = "backup"
		bopts.Env = tgt
		bopts.SourceEnv = ""
		bopts.Extra = []string{"all"}
		if err := runBackup(bopts, cfg); err != nil {
			return fmt.Errorf("safety backup of target %q failed (migration aborted, target untouched): %w", tgt, err)
		}
	} else {
		c.warn("Step 1/3 — Skipping target safety backup (requested) — this is NOT reversible")
	}

	// 2 — back up the source (what we'll copy over).
	c.info("Step 2/3 — Backing up source %q", src)
	sopts := opts
	sopts.Command = "backup"
	sopts.Env = src
	sopts.SourceEnv = ""
	sopts.Extra = []string{"all"}
	if err := runBackup(sopts, cfg); err != nil {
		return fmt.Errorf("backup of source %q failed: %w", src, err)
	}

	// 3 — restore the source snapshot into the target's resources.
	c.info("Step 3/3 — Restoring source data into target %q", tgt)
	ropts := opts
	ropts.Command = "restore"
	ropts.Env = tgt
	ropts.SourceEnv = src
	ropts.Extra = []string{ts}
	if err := runRestore(ropts, cfg); err != nil {
		return fmt.Errorf("restore into target %q failed: %w", tgt, err)
	}

	c.success("Migration complete: %s data is now in %s", src, tgt)
	return nil
}
