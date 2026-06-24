package api

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/databases"
	"github.com/mansoor/rigger/ui/internal/settings"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// databaseInfoResponse is the connection-info view for an environment's managed
// database — everything an app or operator needs to connect (internal address,
// external address when published, credentials, ready-to-paste URIs, and the raw
// connection env keys). The password is only included with ?reveal=true AND an
// operator+ role; otherwise it's masked everywhere it appears.
type connString struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type databaseInfoResponse struct {
	Engine       string            `json:"engine"` // "none" when no managed DB
	Label        string            `json:"label"`
	Version      string            `json:"version"`
	External     bool              `json:"external"`
	InternalHost string            `json:"internal_host"`
	Port         int               `json:"port"`
	Database     string            `json:"database"`
	Username     string            `json:"username"`
	HasPassword  bool              `json:"has_password"`
	Revealed     bool              `json:"revealed"`
	Password     string            `json:"password,omitempty"` // present only when revealed
	ExternalHost string            `json:"external_host,omitempty"`
	ExternalPort int               `json:"external_port,omitempty"`
	EnvKeys      map[string]string `json:"env_keys"`    // connection env-var names → values (password masked unless revealed)
	Connections  []connString      `json:"connections"` // ready-to-paste URIs (password masked unless revealed)
	Schemas      bool              `json:"schemas"`     // engine supports schema/database management (Phase 6)
	Users        bool              `json:"users"`       // engine supports user management
}

const dbMask = "••••••••"

// GetDatabaseInfo — GET /api/workspaces/{ws}/projects/{name}/envs/{env}/database[?reveal=true].
// Returns the managed database's connection details for an environment. Viewer+ to
// see (password masked); operator+ with reveal=true to see the password.
func (h *Handler) GetDatabaseInfo(w http.ResponseWriter, r *http.Request) {
	ws, name, env := r.PathValue("workspace"), r.PathValue("name"), r.PathValue("env")
	role := h.pipelineRole(r, ws, name)
	if !auth.AtLeast(role, auth.RoleViewer) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	cfg, err := wsconfig.Load(wspath.ConfigPath(h.workspacesDir, ws, name))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	ec, ok := cfg.Environments[env]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "environment not found"})
		return
	}
	// Managed deps are project-level (Eff* falls back to legacy per-env); only the
	// external-port exposure (ec.DBExternal) is per-environment.
	engine := cfg.EffDatabase(ec)
	if engine == "" || engine == "none" {
		writeJSON(w, http.StatusOK, databaseInfoResponse{Engine: "none"})
		return
	}
	eng, known := databases.Get(engine)
	if !known {
		writeJSON(w, http.StatusOK, databaseInfoResponse{Engine: engine, Label: engine})
		return
	}
	version := cfg.EffDBVersion(ec)
	if version == "" {
		version = eng.DefaultVersion
	}
	reveal := r.URL.Query().Get("reveal") == "true" && auth.AtLeast(role, auth.RoleOperator)

	dotenv := readEnvMap(wspath.DotEnv(h.workspacesDir, ws, name, env))
	// envgen writes connection vars under the engine's family prefix (POSTGRES_* or
	// MYSQL_*); read the authoritative values from the env's .env.
	pfx := eng.EnvPrefix
	hostKey, portKey := pfx+"_HOST", pfx+"_PORT"
	dbKey := pfx + "_DB"
	userKey, passKey := pfx+"_USER", pfx+"_PASSWORD"
	if pfx == "MYSQL" {
		dbKey = "MYSQL_DATABASE"
	}
	internalHost := dotenv[hostKey]
	if internalHost == "" {
		internalHost = cfg.Project.Prefix() + "_" + engine
	}
	port := eng.Port
	if v := dotenv[portKey]; v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			port = n
		}
	}
	dbName, user, pass := dotenv[dbKey], dotenv[userKey], dotenv[passKey]

	resp := databaseInfoResponse{
		Engine: engine, Label: eng.Label, Version: version, External: ec.DBExternal,
		InternalHost: internalHost, Port: port, Database: dbName, Username: user,
		HasPassword: pass != "", Revealed: reveal, Schemas: eng.Schemas, Users: eng.Users,
		EnvKeys: map[string]string{},
	}
	// shownPass is what appears in URIs / env keys: the real value only when revealed.
	shownPass := dbMask
	if reveal {
		resp.Password = pass
		shownPass = pass
	}
	resp.EnvKeys[hostKey] = internalHost
	resp.EnvKeys[portKey] = strconv.Itoa(port)
	resp.EnvKeys[dbKey] = dbName
	resp.EnvKeys[userKey] = user
	if pass != "" {
		resp.EnvKeys[passKey] = shownPass
	}
	if pfx == "MYSQL" {
		if rp := dotenv["MYSQL_ROOT_PASSWORD"]; rp != "" {
			resp.EnvKeys["MYSQL_ROOT_PASSWORD"] = func() string {
				if reveal {
					return rp
				}
				return dbMask
			}()
		}
	}

	scheme, suffix := "postgresql", ""
	switch eng.Driver {
	case "mysql":
		scheme = "mysql"
	case "mongodb":
		// The connection user is the root user, which authenticates against the
		// admin database — clients need authSource=admin.
		scheme, suffix = "mongodb", "?authSource=admin"
	}
	uri := func(host string, p int) string {
		return fmt.Sprintf("%s://%s:%s@%s:%d/%s%s", scheme, user, shownPass, host, p, dbName, suffix)
	}
	resp.Connections = []connString{{Label: "Internal URI", Value: uri(internalHost, port)}}

	if ec.DBExternal {
		extPort := port
		if v := dotenv["DB_EXTERNAL_PORT"]; v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				extPort = n
			}
		}
		extHost := h.envExternalHost(ws, name, env)
		resp.ExternalHost, resp.ExternalPort = extHost, extPort
		resp.Connections = append(resp.Connections, connString{Label: "External URI", Value: uri(extHost, extPort)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// envExternalHost resolves the host an external client would use to reach the env's
// published DB port: the env's bound remote host address if any, else the
// configured app host, else "localhost".
func (h *Handler) envExternalHost(ws, name, env string) string {
	if h.db != nil {
		if host, _ := settings.HostForEnv(h.db, h.resourcePrefix(ws, name), env); host != nil && host.Address != "" {
			return host.Address
		}
	}
	if ah := h.appSetting("app_host"); ah != "" {
		return ah
	}
	return "localhost"
}

// readEnvMap parses a KEY=VALUE .env file into a map (missing file → empty map),
// stripping surrounding quotes from values.
func readEnvMap(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if i := strings.IndexByte(line, '='); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
			if k != "" {
				out[k] = v
			}
		}
	}
	return out
}
