package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// DB Hosting — user management + Adminer one-click auto-login.
//
// Rigger can create a dedicated DB user per schema it provisions and remember it
// (password AES-256-GCM encrypted at rest) so the Manage Database table can show the
// user/password and offer a per-user "Adminer" link. The Adminer login endpoint
// returns a self-submitting POST form carrying the credentials + an HMAC signature
// (verified by the bind-mounted Adminer plugin against ADMINER_LOGIN_SECRET); the
// Adminer URL on its own still shows the normal login page.

// adminerDriver maps a Rigger engine id to Adminer's driver value.
func adminerDriver(engine string) string {
	if engine == "postgres" {
		return "pgsql"
	}
	return "server" // mysql / mariadb
}

// genDBPassword returns a 24-hex-char password (no quotes/backslashes to escape).
func genDBPassword() string {
	b := make([]byte, 12)
	rand.Read(b) //nolint:errcheck — crypto/rand on these platforms doesn't fail
	return hex.EncodeToString(b)
}

// ── managed_db_users store ─────────────────────────────────────────────────────

type managedDBUser struct {
	Schema   string `json:"schema"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // decrypted; only returned to operator+ reveal
}

func (h *Handler) saveManagedDBUser(workspace, project, env, engine, schema, username, password string) error {
	enc, err := crypto.Encrypt(h.cryptoKey, []byte(password))
	if err != nil {
		return err
	}
	_, err = h.db.Exec(
		`INSERT INTO managed_db_users (workspace, project, env, engine, schema_name, username, password_enc)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(workspace, project, env, username)
		 DO UPDATE SET password_enc=excluded.password_enc, schema_name=excluded.schema_name, engine=excluded.engine`,
		workspace, project, env, engine, schema, username, enc,
	)
	return err
}

// listManagedDBUsers returns the Rigger-created users for an env. Passwords are
// decrypted only when reveal is true (operator+).
func (h *Handler) listManagedDBUsers(workspace, project, env string, reveal bool) ([]managedDBUser, error) {
	rows, err := h.db.Query(
		`SELECT schema_name, username, password_enc FROM managed_db_users
		   WHERE workspace=? AND project=? AND env=? ORDER BY schema_name, username`,
		workspace, project, env,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []managedDBUser{}
	for rows.Next() {
		var u managedDBUser
		var enc string
		if err := rows.Scan(&u.Schema, &u.Username, &enc); err != nil {
			return nil, err
		}
		if reveal {
			if pw, derr := crypto.Decrypt(h.cryptoKey, enc); derr == nil {
				u.Password = string(pw)
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// getManagedDBUser returns one stored user (with decrypted password) by username.
func (h *Handler) getManagedDBUser(workspace, project, env, username string) (*managedDBUser, error) {
	var u managedDBUser
	var enc string
	err := h.db.QueryRow(
		`SELECT schema_name, username, password_enc FROM managed_db_users
		   WHERE workspace=? AND project=? AND env=? AND username=?`,
		workspace, project, env, username,
	).Scan(&u.Schema, &u.Username, &enc)
	if err != nil {
		return nil, err
	}
	pw, err := crypto.Decrypt(h.cryptoKey, enc)
	if err != nil {
		return nil, err
	}
	u.Password = string(pw)
	return &u, nil
}

// ── handlers ───────────────────────────────────────────────────────────────────

// ListDatabaseUsers — GET …/envs/{env}/database/users. Lists the users Rigger
// created on this env's managed DB (with their schema). Passwords are revealed only
// with operator+ AND ?reveal=true. viewer+.
func (h *Handler) ListDatabaseUsers(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	role := h.pipelineRole(r, workspace, project)
	if !auth.AtLeast(role, auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	reveal := r.URL.Query().Get("reveal") == "true" && auth.AtLeast(role, auth.RoleOperator)
	users, err := h.listManagedDBUsers(workspace, project, env, reveal)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "revealed": reveal})
}

// CreateDatabaseUser — POST …/envs/{env}/database/users {name, password?, schema?}.
// Creates a DB user, grants it on the given schema/database, and records it
// (encrypted). Generates a password when blank. operator+.
func (h *Handler) CreateDatabaseUser(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operator role required"})
		return
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Schema   string `json:"schema"`
	}
	if err := readJSON(r, &body); err != nil || !dbIdentRe.MatchString(strings.TrimSpace(body.Name)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 1–63 chars: a letter or underscore, then letters, digits or underscores"})
		return
	}
	name := strings.TrimSpace(body.Name)
	schema := strings.TrimSpace(body.Schema)
	if schema != "" && !dbIdentRe.MatchString(schema) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid schema name"})
		return
	}
	password := strings.TrimSpace(body.Password)
	if password == "" {
		password = genDBPassword()
	} else if strings.ContainsAny(password, "'\"\\\n\r`") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must not contain quotes, backslashes, backticks or newlines"})
		return
	}

	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !c.eng.Users {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this engine does not support user management"})
		return
	}

	var sql string
	if c.engine == "postgres" {
		var b strings.Builder
		fmt.Fprintf(&b, `CREATE USER "%s" WITH PASSWORD '%s'; `, name, password)
		fmt.Fprintf(&b, `GRANT CONNECT ON DATABASE "%s" TO "%s"; `, c.dbName, name)
		if schema != "" {
			fmt.Fprintf(&b, `GRANT USAGE, CREATE ON SCHEMA "%s" TO "%s"; `, schema, name)
			fmt.Fprintf(&b, `GRANT ALL ON ALL TABLES IN SCHEMA "%s" TO "%s"; `, schema, name)
			fmt.Fprintf(&b, `ALTER DEFAULT PRIVILEGES IN SCHEMA "%s" GRANT ALL ON TABLES TO "%s"; `, schema, name)
		} else {
			fmt.Fprintf(&b, `GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s"; `, c.dbName, name)
		}
		sql = b.String()
	} else {
		scope := schema
		if scope == "" {
			scope = c.dbName
		}
		sql = fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'; GRANT ALL ON `%s`.* TO '%s'@'%%'; FLUSH PRIVILEGES;",
			name, password, scope, name)
	}
	if out, rerr := c.run(sql, true); rerr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create user failed: " + strings.TrimSpace(string(out)+" "+rerr.Error())})
		return
	}

	if err := h.saveManagedDBUser(workspace, project, env, c.engine, schema, name, password); err != nil {
		// The DB user exists but we couldn't persist it — surface the password so it
		// isn't lost, but report the storage failure.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "user created but could not be saved: " + err.Error(), "name": name, "password": password,
		})
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		h.db.Exec( //nolint:errcheck
			"INSERT INTO audit_log (user_id, username, project, command, env) VALUES (?,?,?,?,?)",
			claims.UserID, claims.Username, h.resourcePrefix(workspace, project), "db-create-user:"+name, env,
		)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "name": name, "password": password, "schema": schema})
}

// AdminerLogin — GET …/envs/{env}/database/adminer-login?as=admin|<user>&url=<adminer base>.
// Returns a self-submitting POST form that logs into Adminer as the chosen identity
// (admin = engine superuser/root from .env; otherwise a stored Rigger-created user).
// The payload is HMAC-signed with the env's ADMINER_LOGIN_SECRET so only Rigger links
// auto-login. operator+ (it exposes DB credentials to the browser).
func (h *Handler) AdminerLogin(w http.ResponseWriter, r *http.Request) {
	workspace, project, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	if !auth.AtLeast(h.pipelineRole(r, workspace, project), auth.RoleOperator) {
		http.Error(w, "operator role required", http.StatusForbidden)
		return
	}
	target := r.URL.Query().Get("url")
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		http.Error(w, "missing or invalid url", http.StatusBadRequest)
		return
	}
	c, err := h.dbExecContext(workspace, project, env)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	username, password, dbName := "", "", c.dbName
	if as := r.URL.Query().Get("as"); as == "" || as == "admin" {
		if c.engine == "postgres" {
			username, password = c.user, c.password
		} else {
			username, password = c.mysqlCreds(false) // connect to the app's own DB as the app user
		}
	} else {
		u, gerr := h.getManagedDBUser(workspace, project, env, as)
		if gerr != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		username, password = u.Username, u.Password
		if c.engine != "postgres" && u.Schema != "" {
			dbName = u.Schema // mysql/mariadb: a "schema" is a database
		}
	}

	secret := readEnvMap(wspath.DotEnv(h.workspacesDir, workspace, project, env))["ADMINER_LOGIN_SECRET"]
	if secret == "" {
		http.Error(w, "web SQL console is not enabled for this project", http.StatusBadRequest)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"driver":   adminerDriver(c.engine),
		"server":   c.host,
		"username": username,
		"password": password,
		"db":       dbName,
		"exp":      time.Now().Add(2 * time.Minute).Unix(),
	})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Auto-submit a POST to Adminer carrying the signed payload. The Adminer plugin
	// verifies the HMAC, then fills + submits Adminer's real login form.
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Connecting…</title>
<body style="font:14px system-ui;padding:2rem;color:#888">Connecting to the database…
<form id="f" method="post" action="%s">
<input type="hidden" name="rigger_login" value="%s">
<input type="hidden" name="rigger_sig" value="%s">
</form>
<script>document.getElementById('f').submit();</script></body>`,
		html.EscapeString(target), html.EscapeString(string(payload)), sig)
}
