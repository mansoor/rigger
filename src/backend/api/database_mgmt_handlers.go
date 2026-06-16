package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/databases"
	"github.com/mansoor/rigger/ui/internal/executor"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Phase 6 — safe database management. A small, engine-specific surface for the
// common structural op: list databases/schemas (with table count + size) and
// create a new one. Runs the engine's client INSIDE the DB container via
// `docker exec`, so no port needs to be exposed. Deliberately NOT a free-form SQL
// console — only list + create. Listing is viewer+, creating is operator+.

// dbIdentRe guards schema/database names against injection: DDL identifiers can't
// be parameterised, so we allow only a safe identifier shape.
var dbIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

type dbSchema struct {
	Name   string `json:"name"`
	Tables int    `json:"tables"`
	Bytes  int64  `json:"bytes"`
}

// dbExecCtx carries everything needed to run a client command in the DB container.
type dbExecCtx struct {
	engine    string
	eng       databases.Engine
	exec      executor.Executor
	container string // resolved docker container ref (compose container_name / swarm task)
	host      string // network-resolvable DB host on the compose net (what Adminer connects to)
	dbName    string
	user      string
	rootPass  string // mysql/mariadb root password (for privileged ops)
	password  string // app user password (postgres)
}

// dbExecContext resolves the managed DB's container + credentials for an env.
func (h *Handler) dbExecContext(workspace, project, env string) (*dbExecCtx, error) {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, workspace, project))
	if err != nil {
		return nil, fmt.Errorf("project not found")
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		return nil, fmt.Errorf("environment not found")
	}
	engine := cfg.EffDatabase(ec) // project-level (falls back to legacy per-env)
	if engine == "" || engine == "none" {
		return nil, fmt.Errorf("no managed database in this environment")
	}
	eng, known := databases.Get(engine)
	if !known {
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	dotenv := readEnvMap(wspath.DotEnv(h.workspacesDir, workspace, project, env))
	pfx := eng.EnvPrefix
	container := dotenv[pfx+"_HOST"] // == composegen container_name / service key
	if container == "" {
		container = cfg.Project.Prefix() + "_" + engine
	}
	dbName := dotenv[pfx+"_DB"]
	if pfx == "MYSQL" {
		dbName = dotenv["MYSQL_DATABASE"]
	}
	ex, err := h.bridge.ExecForEnv(workspace, project, env)
	if err != nil {
		return nil, fmt.Errorf("reach environment host: %w", err)
	}
	ref, err := h.resolveContainerRef(ex, workspace, project, env, container)
	if err != nil {
		return nil, err
	}
	return &dbExecCtx{
		engine: engine, eng: eng, exec: ex, container: ref, host: container, dbName: dbName,
		user: dotenv[pfx+"_USER"], rootPass: dotenv["MYSQL_ROOT_PASSWORD"], password: dotenv[pfx+"_PASSWORD"],
	}, nil
}

// run executes the engine client in the container with the given SQL, capturing
// stdout. For postgres it connects as the app user over the local socket; for
// mysql/mariadb as root (MYSQL_PWD passed via the container env, not the cmdline).
func (c *dbExecCtx) run(sql string) ([]byte, error) {
	var args []string
	if c.engine == "postgres" {
		args = []string{"exec", "-e", "PGPASSWORD=" + c.password, c.container,
			"psql", "-U", c.user, "-d", c.dbName, "-t", "-A", "-F", "|", "-c", sql}
	} else {
		args = []string{"exec", "-e", "MYSQL_PWD=" + c.rootPass, c.container,
			"mysql", "-uroot", "-N", "-B", "-e", sql}
	}
	return c.exec.DockerOutput(executor.Spec{Args: args})
}

// countTables returns the number of user tables in the application database — the
// "is it empty?" signal the DB-seed hook uses (import only into an empty DB). System
// schemas are excluded. A connection error propagates (so callers can treat the DB as
// not-yet-ready vs genuinely empty).
func (c *dbExecCtx) countTables() (int, error) {
	var sql string
	if c.engine == "postgres" {
		sql = `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace ` +
			`WHERE c.relkind IN ('r','p') AND n.nspname NOT IN ('pg_catalog','information_schema') ` +
			`AND n.nspname NOT LIKE 'pg\_%'`
	} else {
		sql = fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='%s'", c.dbName)
	}
	out, err := c.run(sql)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return 0, fmt.Errorf("unexpected table-count output: %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// importSQL streams a SQL dump into the engine client over stdin (no temp file in the
// container), running it against the application database. progress (stdout+stderr) is
// streamed to out. mysql/mariadb connect as root; postgres as the app user with
// ON_ERROR_STOP so a broken dump fails loudly instead of half-importing.
func (c *dbExecCtx) importSQL(r io.Reader, out io.Writer) error {
	var args []string
	if c.engine == "postgres" {
		args = []string{"exec", "-i", "-e", "PGPASSWORD=" + c.password, c.container,
			"psql", "-U", c.user, "-d", c.dbName, "-v", "ON_ERROR_STOP=1"}
	} else {
		args = []string{"exec", "-i", "-e", "MYSQL_PWD=" + c.rootPass, c.container,
			"mysql", "-uroot", c.dbName}
	}
	return c.exec.Docker(executor.Spec{Args: args, Stdin: r, Stdout: out, Stderr: out})
}

// waitDBReady polls until the DB answers a query (it's accepting connections and the
// app database exists) or timeout elapses — the managed container may still be
// initializing right after `compose up -d`.
func (c *dbExecCtx) waitDBReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if _, err := c.countTables(); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("database not ready after %s: %w", timeout, last)
		}
		time.Sleep(2 * time.Second)
	}
}

// ListDatabaseSchemas — GET …/envs/{env}/database/schemas. Lists schemas
// (postgres) or databases (mysql/mariadb) with table count + on-disk size. viewer+.
func (h *Handler) ListDatabaseSchemas(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var sql, sep string
	if c.engine == "postgres" {
		sep = "|"
		sql = `SELECT n.nspname, COUNT(c.oid) FILTER (WHERE c.relkind IN ('r','p')), ` +
			`COALESCE(SUM(pg_total_relation_size(c.oid)),0) ` +
			`FROM pg_namespace n LEFT JOIN pg_class c ON c.relnamespace=n.oid ` +
			`WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_%' ` +
			`GROUP BY n.nspname ORDER BY n.nspname`
	} else {
		sep = "\t"
		sql = `SELECT s.schema_name, COUNT(t.table_name), COALESCE(SUM(t.data_length+t.index_length),0) ` +
			`FROM information_schema.schemata s ` +
			`LEFT JOIN information_schema.tables t ON t.table_schema=s.schema_name ` +
			`WHERE s.schema_name NOT IN ('mysql','information_schema','performance_schema','sys') ` +
			`GROUP BY s.schema_name ORDER BY s.schema_name`
	}
	out, err := c.run(sql)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "query failed (is the database running?): " + strings.TrimSpace(string(out)+" "+err.Error())})
		return
	}
	schemas := []dbSchema{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, sep)
		if len(f) < 3 {
			continue
		}
		tables, _ := strconv.Atoi(strings.TrimSpace(f[1]))
		bytes, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		schemas = append(schemas, dbSchema{Name: strings.TrimSpace(f[0]), Tables: tables, Bytes: bytes})
	}
	unit := "schema" // postgres: schemas within the database
	if c.engine != "postgres" {
		unit = "database" // mysql/mariadb: each "schema" is a database
	}
	writeJSON(w, http.StatusOK, map[string]any{"engine": c.engine, "unit": unit, "schemas": schemas})
}

// CreateDatabaseSchema — POST …/envs/{env}/database/schemas {name}. Creates a
// schema (postgres) or database (mysql/mariadb). operator+.
func (h *Handler) CreateDatabaseSchema(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || !dbIdentRe.MatchString(strings.TrimSpace(body.Name)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 1–63 chars: a letter or underscore, then letters, digits or underscores"})
		return
	}
	name := strings.TrimSpace(body.Name)
	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var sql string
	if c.engine == "postgres" {
		sql = fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS "%s"`, name)
	} else {
		sql = fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", name)
	}
	if out, err := c.run(sql); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create failed: " + strings.TrimSpace(string(out)+" "+err.Error())})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(workspace, project), "db-create-schema:"+name, env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "name": name})
}

// DeleteDatabaseSchema — DELETE …/envs/{env}/database/schemas/{schema}?confirm={schema}.
// Drops a schema (postgres, CASCADE) or database (mysql/mariadb). Destructive, so it
// requires the typed-name confirmation to match, and refuses the primary application
// database / the postgres "public" schema. Also clears any stored users for it.
// operator+.
func (h *Handler) DeleteDatabaseSchema(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	name := strings.TrimSpace(r.PathValue("schema"))
	if !dbIdentRe.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid name"})
		return
	}
	if r.URL.Query().Get("confirm") != name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirmation name does not match"})
		return
	}
	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if name == c.dbName || (c.engine == "postgres" && name == "public") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refusing to delete the primary application database"})
		return
	}
	var sql string
	if c.engine == "postgres" {
		sql = fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, name)
	} else {
		sql = fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name)
	}
	if out, derr := c.run(sql); derr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "delete failed: " + strings.TrimSpace(string(out)+" "+derr.Error())})
		return
	}
	// Forget any Rigger-created users recorded for this schema (the DB users may
	// still exist but their schema is gone; leave them in the DB, drop our record).
	h.db.Exec(`DELETE FROM managed_db_users WHERE workspace=? AND project=? AND env=? AND schema_name=?`, //nolint:errcheck
		workspace, project, env, name)
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(workspace, project), "db-delete-schema:"+name, env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// SeedDatabase — POST …/envs/{env}/database/seed {force}. Imports the project's
// configured SQL dump (_source/seed.sql) into the managed database. Refuses a
// non-empty database with 409 unless force=true (the dump may re-create/overwrite).
// operator+. This is the manual counterpart to the auto-on-first-deploy hook.
func (h *Handler) SeedDatabase(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &body)

	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, workspace, project))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project not found"})
		return
	}
	if cfg.SeedSpec() == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no database seed is configured for this project"})
		return
	}
	f, err := os.Open(wspath.SeedFile(h.workspacesDir, workspace, project))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "seed file not found — re-upload the source with a database dump"})
		return
	}
	defer f.Close()

	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := c.waitDBReady(60 * time.Second); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	n, err := c.countTables()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if n > 0 && !body.Force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("database already has %d table(s) — pass force to import anyway", n), "tables": n})
		return
	}
	var out bytes.Buffer
	if err := c.importSQL(f, &out); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "import failed: " + strings.TrimSpace(out.String()+" "+err.Error())})
		return
	}
	after, _ := c.countTables()
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(workspace, project), "db-seed", env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "imported", "tables": after})
}

// maybeAutoSeed imports the project's configured SQL dump after a successful deploy,
// but ONLY when auto-import is enabled AND the application database is currently empty
// (zero tables) — so a fresh deploy is seeded once and a redeploy never clobbers data.
// A teardown that drops the DB volume makes it empty again, so it re-seeds naturally.
// Best-effort: every outcome is reported to out; a failure never affects the deploy.
func (h *Handler) maybeAutoSeed(workspace, project, env string, out io.Writer) {
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, workspace, project))
	if err != nil {
		return
	}
	spec := cfg.SeedSpec()
	if spec == nil || !spec.Auto {
		return
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		return
	}
	if eng := cfg.EffDatabase(ec); eng == "" || eng == "none" {
		return
	}
	f, err := os.Open(wspath.SeedFile(h.workspacesDir, workspace, project))
	if err != nil {
		fmt.Fprintf(out, "\n\033[33m⚠ auto-seed skipped: seed file not found\033[0m\n")
		return
	}
	defer f.Close()
	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		fmt.Fprintf(out, "\n\033[33m⚠ auto-seed skipped: %s\033[0m\n", err.Error())
		return
	}
	if err := c.waitDBReady(90 * time.Second); err != nil {
		fmt.Fprintf(out, "\n\033[33m⚠ auto-seed skipped: %s\033[0m\n", err.Error())
		return
	}
	n, err := c.countTables()
	if err != nil {
		fmt.Fprintf(out, "\n\033[33m⚠ auto-seed skipped: %s\033[0m\n", err.Error())
		return
	}
	if n > 0 {
		fmt.Fprintf(out, "\n\033[36mℹ database already has %d table(s) — skipping auto-seed.\033[0m\n", n)
		return
	}
	fmt.Fprintf(out, "\n\033[36m▶ importing database seed (%s)...\033[0m\n", spec.File)
	if err := c.importSQL(f, out); err != nil {
		fmt.Fprintf(out, "\033[33m⚠ seed import failed (deploy is unaffected): %s\033[0m\n", err.Error())
		return
	}
	after, _ := c.countTables()
	fmt.Fprintf(out, "\033[32m✓ database seeded — %d table(s).\033[0m\n", after)
}
