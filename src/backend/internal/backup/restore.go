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
)

// runRestore is the entry point for the "restore" command. The snapshot date is
// Extra[0].
func runRestore(opts Options, cfg *wsConfig) error {
	c := newCtx(opts, cfg)

	if len(opts.Extra) == 0 || opts.Extra[0] == "" {
		return fmt.Errorf("restore requires a snapshot date")
	}
	snapshot := opts.Extra[0]
	backupDir := filepath.Join(opts.WorkspacesDir, opts.Workspace, "backups", opts.Env, snapshot)
	if fi, err := os.Stat(backupDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("backup snapshot not found: %s", backupDir)
	}

	c.info("Restore: %s ← %s", opts.Env, snapshot)

	// Step 1 — stop the stack.
	c.info("Step 1/3 — Stopping stack")
	_ = c.compose(executor.Spec{}, "stop")
	c.success("Stack stopped")

	// Step 2 — restore data.
	c.info("Step 2/3 — Restoring data")
	c.restoreDB(snapshot, backupDir)
	c.restoreVolumes(snapshot, backupDir)

	// Step 3 — start the stack.
	c.info("Step 3/3 — Starting stack")
	if err := c.compose(executor.Spec{}, "up", "-d"); err != nil {
		return fmt.Errorf("start stack: %w", err)
	}
	c.success("Environment restored successfully from %s", snapshot)
	return nil
}

// ── DB restore ───────────────────────────────────────────────────────────────────

func (c *ctx) restoreDB(snapshot, backupDir string) {
	if c.cfg.Project.Type == "image" {
		for _, img := range c.cfg.Images {
			dump := findDump(backupDir, img.Name)
			if dump == "" {
				continue
			}
			name := strings.ToLower(img.Image)
			switch {
			case strings.Contains(name, "postgres"):
				c.restorePostgres(img.Name, c.envOr("POSTGRES_USER", "postgres"), c.envOr("POSTGRES_DB", c.project), dump)
			case strings.Contains(name, "mysql"), strings.Contains(name, "mariadb"):
				rootPass := c.envOr("MYSQL_ROOT_PASSWORD", "")
				if rootPass == "" {
					c.warn("MYSQL_ROOT_PASSWORD not set — skipping %s", img.Name)
					continue
				}
				c.restoreMySQL(img.Name, rootPass, c.envOr("MYSQL_DATABASE", c.project), dump)
			}
		}
		return
	}

	// Custom stack.
	switch c.cfg.Environments[c.env].Database {
	case "postgres":
		dump := findDump(backupDir, "postgres")
		if dump == "" {
			c.warn("No PostgreSQL dump found in snapshot — skipping DB restore")
			return
		}
		c.restorePostgres("postgres", c.envOr("POSTGRES_USER", "postgres"), c.envOr("POSTGRES_DB", c.project), dump)
	case "mysql":
		dump := findDump(backupDir, "mysql")
		if dump == "" {
			c.warn("No MySQL dump found in snapshot — skipping DB restore")
			return
		}
		c.restoreMySQL("mysql", c.envOr("MYSQL_ROOT_PASSWORD", ""), c.envOr("MYSQL_DATABASE", c.project), dump)
	default:
		c.info("No database configured — skipping DB restore")
	}
}

func (c *ctx) restorePostgres(svc, user, db, dumpFile string) {
	full := c.resolveSvc(svc)
	c.info("Starting service: %s", svc)
	_ = c.compose(executor.Spec{}, "up", "-d", full)
	time.Sleep(4 * time.Second)

	c.info("Dropping and recreating schema in %s...", db)
	_ = c.compose(executor.Spec{}, "exec", "-T", full, "psql", "-U", user, "-d", db,
		"-c", "DROP SCHEMA public CASCADE; CREATE SCHEMA public;")

	c.info("Restoring dump: %s", filepath.Base(dumpFile))
	if err := c.pipeDumpInto(dumpFile, "exec", "-T", full, "psql", "-U", user, db); err != nil {
		c.warn("PostgreSQL restore reported an error for %s: %v", svc, err)
		return
	}
	c.success("PostgreSQL restore complete: %s", svc)
}

func (c *ctx) restoreMySQL(svc, rootPass, db, dumpFile string) {
	full := c.resolveSvc(svc)
	c.info("Starting service: %s", svc)
	_ = c.compose(executor.Spec{}, "up", "-d", full)
	time.Sleep(4 * time.Second)

	c.info("Restoring dump: %s", filepath.Base(dumpFile))
	if err := c.pipeDumpInto(dumpFile, "exec", "-T", full, "mysql", "-u", "root", "-p"+rootPass, db); err != nil {
		c.warn("MySQL/MariaDB restore reported an error for %s: %v", svc, err)
		return
	}
	c.success("MySQL/MariaDB restore complete: %s", svc)
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

// ── Volume restore ───────────────────────────────────────────────────────────────

func (c *ctx) restoreVolumes(snapshot, backupDir string) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}
	restored := 0
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
			continue
		}
		c.success("Volume restored: %s", fullVol)
		restored++
	}
	if restored == 0 {
		c.info("No volume archives found in snapshot")
	}
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
