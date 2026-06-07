package backup

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// runRestore is the entry point for the "restore" command. The snapshot date is
// Extra[0].
func runRestore(opts Options, cfg *wsConfig) error {
	c := newCtx(opts, cfg)

	if len(opts.Extra) == 0 || opts.Extra[0] == "" {
		return fmt.Errorf("restore requires a snapshot date")
	}
	snapshot := opts.Extra[0]
	backupDir := filepath.Join(wspath.EnvBackupsDir(opts.WorkspacesDir, opts.Workspace, opts.Project, opts.Env), snapshot)
	if fi, err := os.Stat(backupDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("backup snapshot not found: %s", backupDir)
	}

	c.info("Restore: %s ← %s", opts.Env, snapshot)

	// Step 1 — stop the stack (containers are kept so --volumes-from can reach
	// their mounts during the data restore).
	c.info("Step 1/3 — Stopping stack")
	_ = c.compose(executor.Spec{}, "stop")
	c.success("Stack stopped")

	// Step 2 — restore data. Failures are counted (not swallowed) so the caller
	// can surface a real failure instead of a false "success".
	c.info("Step 2/3 — Restoring data")
	failures := c.restoreDB(snapshot, backupDir)
	failures += c.restoreFiles(snapshot, backupDir)

	// Step 3 — start the stack.
	c.info("Step 3/3 — Starting stack")
	if err := c.compose(executor.Spec{}, "up", "-d"); err != nil {
		return fmt.Errorf("start stack: %w", err)
	}

	if failures > 0 {
		// Surface the failure so a migration/restore job is marked failed rather
		// than reporting success after losing data.
		return fmt.Errorf("restore finished with %d failure(s) — data may be incomplete (see log above)", failures)
	}
	c.success("Environment restored successfully from %s", snapshot)
	return nil
}

// isDBImage reports whether an image reference is a recognised database engine.
func isDBImage(image string) bool {
	n := strings.ToLower(image)
	return strings.Contains(n, "postgres") || strings.Contains(n, "mysql") || strings.Contains(n, "mariadb")
}

// ── DB restore ───────────────────────────────────────────────────────────────────

// restoreDB restores SQL dumps. It returns the number of restore attempts that
// failed (0 = clean; "nothing to restore" is not a failure).
func (c *ctx) restoreDB(snapshot, backupDir string) int {
	fails := 0
	if c.cfg.Project.Type == "image" {
		for _, img := range c.cfg.Images {
			dump := findDump(backupDir, img.Name)
			if dump == "" {
				continue
			}
			name := strings.ToLower(img.Image)
			switch {
			case strings.Contains(name, "postgres"):
				if err := c.restorePostgres(img.Name, c.envOr("POSTGRES_USER", "postgres"), c.envOr("POSTGRES_DB", c.project), dump); err != nil {
					fails++
				}
			case strings.Contains(name, "mysql"), strings.Contains(name, "mariadb"):
				rootPass := c.envOr("MYSQL_ROOT_PASSWORD", "")
				if rootPass == "" {
					c.warn("MYSQL_ROOT_PASSWORD not set — cannot restore %s", img.Name)
					fails++
					continue
				}
				if err := c.restoreMySQL(img.Name, rootPass, c.envOr("MYSQL_DATABASE", c.project), dump); err != nil {
					fails++
				}
			}
		}
		return fails
	}

	// Custom stack.
	switch c.cfg.Environments[c.env].Database {
	case "postgres":
		dump := findDump(backupDir, "postgres")
		if dump == "" {
			c.warn("No PostgreSQL dump found in snapshot — skipping DB restore")
			return 0
		}
		if err := c.restorePostgres("postgres", c.envOr("POSTGRES_USER", "postgres"), c.envOr("POSTGRES_DB", c.project), dump); err != nil {
			fails++
		}
	case "mysql":
		dump := findDump(backupDir, "mysql")
		if dump == "" {
			c.warn("No MySQL dump found in snapshot — skipping DB restore")
			return 0
		}
		rootPass := c.envOr("MYSQL_ROOT_PASSWORD", "")
		if rootPass == "" {
			c.warn("MYSQL_ROOT_PASSWORD not set — cannot restore DB")
			return 1
		}
		if err := c.restoreMySQL("mysql", rootPass, c.envOr("MYSQL_DATABASE", c.project), dump); err != nil {
			fails++
		}
	default:
		c.info("No database configured — skipping DB restore")
	}
	return fails
}

// waitReady polls a `compose exec -T <full> <probe...>` until it exits zero (the
// service accepts connections) or the time budget is exhausted. It replaces a
// fixed sleep that raced DB startup on cold/loaded daemons and intermittently
// failed restores. Probe output is discarded.
func (c *ctx) waitReady(full string, probe ...string) bool {
	args := append([]string{"exec", "-T", full}, probe...)
	for i := 0; i < 30; i++ { // ~60s budget (30 × 2s)
		if c.compose(executor.Spec{}, args...) == nil {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func (c *ctx) restorePostgres(svc, user, db, dumpFile string) error {
	full := c.resolveSvc(svc)
	c.info("Starting service: %s", svc)
	_ = c.compose(executor.Spec{}, "up", "-d", full)

	c.info("Waiting for %s to accept connections...", svc)
	if !c.waitReady(full, "sh", "-c", `pg_isready -U "$1" -q 2>/dev/null || psql -U "$1" -c 'SELECT 1' >/dev/null 2>&1`, "_", user) {
		c.warn("%s did not accept connections in time — attempting restore anyway", svc)
	}

	c.info("Dropping and recreating schema in %s...", db)
	_ = c.compose(executor.Spec{}, "exec", "-T", full, "psql", "-U", user, "-d", db,
		"-c", "DROP SCHEMA public CASCADE; CREATE SCHEMA public;")

	c.info("Restoring dump: %s", filepath.Base(dumpFile))
	if err := c.pipeDumpInto(dumpFile, "exec", "-T", full, "psql", "-U", user, db); err != nil {
		c.warn("PostgreSQL restore reported an error for %s: %v", svc, err)
		return err
	}
	c.success("PostgreSQL restore complete: %s", svc)
	return nil
}

func (c *ctx) restoreMySQL(svc, rootPass, db, dumpFile string) error {
	full := c.resolveSvc(svc)
	c.info("Starting service: %s", svc)
	_ = c.compose(executor.Spec{}, "up", "-d", full)

	c.info("Waiting for %s to accept connections...", svc)
	probe := `mariadb-admin ping -uroot -p"$1" --silent 2>/dev/null || ` +
		`mysqladmin ping -uroot -p"$1" --silent 2>/dev/null || ` +
		`mariadb -uroot -p"$1" -e 'SELECT 1' >/dev/null 2>&1 || ` +
		`mysql -uroot -p"$1" -e 'SELECT 1' >/dev/null 2>&1`
	if !c.waitReady(full, "sh", "-c", probe, "_", rootPass) {
		c.warn("%s did not accept connections in time — attempting restore anyway", svc)
	}

	c.info("Restoring dump: %s", filepath.Base(dumpFile))
	// Newer mariadb images ship the `mariadb` client and dropped the legacy
	// `mysql` symlink, so detect inside the container instead of hardcoding it.
	client := `if command -v mariadb >/dev/null 2>&1; then exec mariadb "$@"; ` +
		`elif command -v mysql >/dev/null 2>&1; then exec mysql "$@"; ` +
		`else echo "no mariadb/mysql client found in container" >&2; exit 1; fi`
	if err := c.pipeDumpInto(dumpFile, "exec", "-T", full, "sh", "-c", client, "_", "-u", "root", "-p"+rootPass, db); err != nil {
		c.warn("MySQL/MariaDB restore reported an error for %s: %v", svc, err)
		return err
	}
	c.success("MySQL/MariaDB restore complete: %s", svc)
	return nil
}

// pipeDumpInto gunzips a .sql.gz and streams it to a `compose exec` command's
// stdin (execArgs are the args after `compose -p … -f …`).
func (c *ctx) pipeDumpInto(dumpFile string, execArgs ...string) error {
	f, err := os.Open(dumpFile)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	return c.compose(executor.Spec{Stdin: gz, Stdout: c.opts.Stdout, Stderr: c.opts.Stderr}, execArgs...)
}

// ── File / volume restore ──────────────────────────────────────────────────────────

// restoreFiles re-hydrates non-database file data. It mirrors how the backup was
// taken: image stacks archive each container's mounts via --volumes-from (works
// for bind mounts and named volumes alike), so the restore extracts back through
// the container the same way; custom stacks use named volumes. DB data dirs are
// skipped here — they are restored from the SQL dump in restoreDB. Returns the
// number of failures.
func (c *ctx) restoreFiles(snapshot, backupDir string) int {
	if c.cfg.Project.Type == "image" {
		return c.restoreContainerMounts(snapshot, backupDir)
	}
	return c.restoreNamedVolumes(snapshot, backupDir)
}

// restoreContainerMounts restores image-stack file archives by extracting them
// back into the originating container's mount via --volumes-from — symmetric with
// backupFiles' archiveFromContainer, so it is agnostic to bind-mount vs named
// volume and to host/container path differences. DB services are skipped (their
// data comes from the SQL dump).
func (c *ctx) restoreContainerMounts(snapshot, backupDir string) int {
	fails, restored := 0, 0
	for _, img := range c.cfg.Images {
		if isDBImage(img.Image) {
			continue // DB data dir is restored from the SQL dump
		}
		container := c.prefix + "_" + img.Name
		for _, m := range c.inspectMounts(container) {
			volLabel := strings.TrimPrefix(strings.ReplaceAll(m.Destination, "/", "_"), "_")
			archive := fmt.Sprintf("%s_%s_%s_%s_%s.tar.gz", c.project, c.env, img.Name, volLabel, snapshot)
			path := filepath.Join(backupDir, archive)
			if _, err := os.Stat(path); err != nil {
				continue // no archive for this mount in this snapshot
			}
			c.info("Restoring files: %s:%s ← %s", img.Name, m.Destination, archive)
			if err := c.extractIntoContainer(container, m.Destination, path); err != nil {
				c.warn("File restore failed for %s:%s: %v", img.Name, m.Destination, err)
				fails++
				continue
			}
			c.success("Files restored: %s:%s", img.Name, m.Destination)
			restored++
		}
	}
	if restored == 0 && fails == 0 {
		c.info("No file archives to restore")
	}
	return fails
}

// restoreNamedVolumes restores custom-stack archives into their named volumes.
// Returns the number of failures.
func (c *ctx) restoreNamedVolumes(snapshot, backupDir string) int {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return 0
	}
	fails, restored := 0, 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		// {project}_{env}_{label}_{snapshot}.tar.gz → label
		base := strings.TrimSuffix(name, ".tar.gz")
		base = strings.TrimPrefix(base, c.project+"_"+c.env+"_")
		volLabel := strings.TrimSuffix(base, "_"+snapshot)
		fullVol := c.prefix + "_" + volLabel

		if !c.volumeExists(fullVol) {
			c.warn("Volume %s not found — skipping (stack may not be deployed yet)", fullVol)
			continue
		}
		c.info("Restoring volume: %s ← %s", fullVol, name)
		if err := c.extractIntoVolume(fullVol, filepath.Join(backupDir, name)); err != nil {
			c.warn("Volume restore failed for %s: %v", fullVol, err)
			fails++
			continue
		}
		c.success("Volume restored: %s", fullVol)
		restored++
	}
	if restored == 0 && fails == 0 {
		c.info("No volume archives found in snapshot")
	}
	return fails
}

// extractIntoContainer wipes a path inside a container's mount and unpacks a
// .tar.gz archive into it via `docker run --volumes-from <c> alpine tar`. The
// archive is streamed via stdin, so it works regardless of host/container path
// differences and whether the mount is a bind or a named volume.
func (c *ctx) extractIntoContainer(container, destPath, archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	script := fmt.Sprintf("cd '%s' && find . -mindepth 1 -delete 2>/dev/null || true; tar xzf -", destPath)
	return c.dexec(executor.Spec{
		Args:   []string{"run", "--rm", "-i", "--volumes-from", container, "alpine:3", "sh", "-c", script},
		Stdin:  f,
		Stderr: c.opts.Stderr,
	})
}

// extractIntoVolume wipes a named volume and unpacks a .tar.gz archive into it.
// The archive is streamed via stdin (read inside this container) rather than
// bind-mounted, so it works regardless of host/container path differences.
func (c *ctx) extractIntoVolume(fullVol, archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.dexec(executor.Spec{
		Args: []string{"run", "--rm", "-i", "-v", fullVol + ":/data", "alpine:3",
			"sh", "-c", `cd /data && find . -mindepth 1 -delete 2>/dev/null || true; tar xzf -`},
		Stdin:  f,
		Stderr: c.opts.Stderr,
	})
}

// findDump returns the first *_<label>_*.sql.gz file in dir (sorted), or "".
func findDump(dir, label string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var matches []string
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, "_"+label+"_") && strings.HasSuffix(n, ".sql.gz") {
			matches = append(matches, n)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return filepath.Join(dir, matches[0])
}
