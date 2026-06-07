package backup

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mansoor/rigger/ui/internal/executor"
)

// runBackup is the entry point for the "backup" command. target is Extra[0]
// (db | files | all), defaulting to all.
func runBackup(opts Options, cfg *wsConfig) error {
	c := newCtx(opts, cfg)

	target := "all"
	if len(opts.Extra) > 0 && opts.Extra[0] != "" {
		target = opts.Extra[0]
	}

	dateDir := opts.Timestamp
	backupRoot := filepath.Join(opts.WorkspacesDir, opts.Workspace, "backups", opts.Env)
	backupDir := filepath.Join(backupRoot, dateDir)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}

	c.info("Backup: %s → %s", opts.Env, backupDir)

	switch target {
	case "db":
		c.backupDB(dateDir, backupDir)
	case "files":
		c.backupFiles(dateDir, backupDir)
	case "all":
		c.backupDB(dateDir, backupDir)
		c.backupFiles(dateDir, backupDir)
		c.pruneOldBackups(backupRoot)
	default:
		return fmt.Errorf("unknown backup target %q (use: db | files | all)", target)
	}

	c.success("Backup complete — %s", backupDir)
	if entries, err := os.ReadDir(backupDir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil {
				fmt.Fprintf(opts.Stdout, "    %s (%s)\n", e.Name(), humanSize(fi.Size()))
			}
		}
	}
	return nil
}

// ── DB backup ────────────────────────────────────────────────────────────────────

func (c *ctx) backupDB(dateDir, backupDir string) {
	if c.cfg.Project.Type == "image" {
		foundDB := false
		for idx, img := range c.cfg.Images {
			name := strings.ToLower(img.Image)
			var dbType string
			switch {
			case strings.Contains(name, "postgres"):
				dbType = "postgres"
			case strings.Contains(name, "mysql"), strings.Contains(name, "mariadb"):
				dbType = "mysql"
			default:
				continue
			}
			foundDB = true
			c.info("Found %s service: %s", dbType, img.Name)
			if !c.sqlDump(dbType, img.Name, img.Name, dateDir, backupDir) {
				c.warn("SQL dump failed for %s — falling back to filesystem archive", img.Name)
				c.fsArchiveSvc(img.Name, img.Name, idx, dateDir, backupDir)
			}
		}
		if !foundDB {
			c.info("No recognized database containers in image stack — skipping DB backup")
		}
		return
	}

	// Custom stack — driven by the environment's database field.
	database := c.cfg.Environments[c.env].Database
	switch database {
	case "postgres":
		if !c.sqlDump("postgres", "postgres", "postgres", dateDir, backupDir) {
			c.warn("SQL dump failed — filesystem fallback not available for custom stacks")
		}
	case "mysql":
		if !c.sqlDump("mysql", "mysql", "mysql", dateDir, backupDir) {
			c.warn("SQL dump failed — filesystem fallback not available for custom stacks")
		}
	default:
		c.info("No database configured (database=%s) — skipping DB backup", database)
	}
}

// sqlDump attempts a SQL dump via the container's own tools, gzipping to a file.
// Returns true on success.
func (c *ctx) sqlDump(dbType, svc, label, dateDir, backupDir string) bool {
	sqlFile := filepath.Join(backupDir, fmt.Sprintf("%s_%s_%s_%s.sql.gz", c.project, c.env, label, dateDir))
	c.info("Attempting SQL dump (%s) from %s...", dbType, svc)

	var script string
	if dbType == "postgres" {
		script = `pg_dump -U "${POSTGRES_USER:-postgres}" "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"`
	} else {
		script = `if command -v mariadb-dump >/dev/null 2>&1; then _DUMP=mariadb-dump; ` +
			`elif command -v mysqldump >/dev/null 2>&1; then _DUMP=mysqldump; ` +
			`else echo "no dump binary found in container" >&2; exit 1; fi; ` +
			`$_DUMP -u root -p"${MYSQL_ROOT_PASSWORD}" "${MYSQL_DATABASE}"`
	}

	f, err := os.Create(sqlFile)
	if err != nil {
		return false
	}
	gz := gzip.NewWriter(f)
	runErr := c.compose(executor.Spec{Stdout: gz, Stderr: c.opts.Stderr},
		"exec", "-T", c.resolveSvc(svc), "sh", "-c", script)
	gz.Close()
	f.Close()

	if runErr != nil {
		os.Remove(sqlFile)
		return false
	}
	if fi, err := os.Stat(sqlFile); err != nil || fi.Size() == 0 {
		os.Remove(sqlFile)
		return false
	}
	c.success("SQL dump: %s", filepath.Base(sqlFile))
	return true
}

// fsArchiveSvc is the filesystem fallback for a DB service: stop it, tar its
// mounts via --volumes-from, restart it.
func (c *ctx) fsArchiveSvc(svc, label string, imgIdx int, dateDir, backupDir string) {
	fullSvc := c.resolveSvc(svc)
	container := c.prefix + "_" + svc

	c.warn("Stopping %s for a consistent filesystem snapshot...", svc)
	_ = c.compose(executor.Spec{}, "stop", fullSvc)

	archived := 0
	for _, m := range c.inspectMounts(container) {
		fsFile := filepath.Join(backupDir,
			fmt.Sprintf("%s_%s_%s_fs_%d_%s.tar.gz", c.project, c.env, label, archived, dateDir))
		if c.archiveFromContainer(container, m.Destination, fsFile) {
			c.success("Filesystem archive: %s", filepath.Base(fsFile))
			archived++
		}
	}

	c.warn("Restarting %s...", svc)
	_ = c.compose(executor.Spec{}, "start", fullSvc)

	if archived > 0 {
		c.warn("DB backup used a filesystem archive fallback — not a SQL dump.")
	} else {
		c.warn("Filesystem archive also failed — no backup created for %s", svc)
	}
}

// ── File backup ────────────────────────────────────────────────────────────────--

func (c *ctx) backupFiles(dateDir, backupDir string) {
	if c.cfg.Project.Type == "image" {
		seen := map[string]bool{}
		backed := 0
		for _, img := range c.cfg.Images {
			container := c.prefix + "_" + img.Name
			for _, m := range c.inspectMounts(container) {
				if seen[m.Destination] {
					continue
				}
				seen[m.Destination] = true
				volLabel := strings.TrimPrefix(strings.ReplaceAll(m.Destination, "/", "_"), "_")
				volFile := filepath.Join(backupDir,
					fmt.Sprintf("%s_%s_%s_%s_%s.tar.gz", c.project, c.env, img.Name, volLabel, dateDir))
				c.info("Archiving %s mount %s from %s...", m.Type, m.Destination, img.Name)
				if c.archiveFromContainer(container, m.Destination, volFile) {
					c.success("Archive: %s", filepath.Base(volFile))
					backed++
				} else {
					c.warn("Could not archive %s from %s — skipping", m.Destination, img.Name)
				}
			}
		}
		if backed == 0 {
			c.info("No volumes found to archive")
		}
		return
	}

	// Custom stack: uploads volume (+ garage if enabled).
	c.info("Archiving upload volume...")
	uploadFile := filepath.Join(backupDir, fmt.Sprintf("%s_%s_uploads_%s.tar.gz", c.project, c.env, dateDir))
	if c.archiveNamedVolumes(uploadFile, "/data", map[string]string{c.prefix + "_uploads": "/data"}, ".") {
		c.success("Uploads archive: %s", filepath.Base(uploadFile))
	} else {
		c.warn("Could not archive uploads volume")
	}

	if c.cfg.Environments[c.env].GarageEnabled {
		c.info("Archiving Garage S3 data...")
		garageFile := filepath.Join(backupDir, fmt.Sprintf("%s_%s_garage_%s.tar.gz", c.project, c.env, dateDir))
		mounts := map[string]string{
			c.prefix + "_garage_data": "/garage-data",
			c.prefix + "_garage_meta": "/garage-meta",
		}
		if c.archiveNamedVolumes(garageFile, "/", mounts, "garage-data", "garage-meta") {
			c.success("Garage archive: %s", filepath.Base(garageFile))
		} else {
			c.warn("Could not archive Garage volumes")
		}
	}
}

// pruneOldBackups keeps the newest N snapshot dirs per env, where N is the
// workspace's configured backup.retention. Snapshot dirs are named
// YYYY-MM-DD_HH-MM-SS, so a reverse lexicographic sort is chronological.
// Retention <= 0 (unset / legacy config) means "don't prune" — never delete
// data the user hasn't opted into pruning.
func (c *ctx) pruneOldBackups(backupRoot string) {
	retention := c.cfg.Backup.Retention
	if retention <= 0 {
		c.info("Retention not set — skipping prune")
		return
	}
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return
	}
	var snaps []string
	for _, e := range entries {
		if e.IsDir() {
			snaps = append(snaps, e.Name())
		}
	}
	if len(snaps) <= retention {
		return
	}
	c.info("Pruning to the %d most recent backups (have %d)...", retention, len(snaps))
	sort.Sort(sort.Reverse(sort.StringSlice(snaps)))
	for _, name := range snaps[retention:] {
		os.RemoveAll(filepath.Join(backupRoot, name))
	}
	c.success("Pruned %d old backup(s)", len(snaps)-retention)
}

// humanSize renders a byte count as a short human string.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
