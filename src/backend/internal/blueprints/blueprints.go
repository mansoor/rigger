// Package blueprints holds the per-stack knowledge the repo detector maps a
// framework onto: the default service shape (listen port, healthcheck, whether
// the app terminates HTTP itself or needs an nginx front) and the Dockerfile
// template used for the no-repo scaffold case (deferred picker). It is pure data;
// the filesystem identification lives in internal/detect, which looks blueprints
// up by ID.
package blueprints

import "fmt"

// DBFacts are the resolved connection facts for the active environment's managed
// database. envgen fills these from the env's db toggle + generated credentials
// and hands them to a blueprint's EnvVars so each framework gets the keys it
// actually reads.
type DBFacts struct {
	Engine   string // "mysql" | "postgres"
	Host     string
	Port     string
	Name     string
	User     string
	Password string
}

// RedisFacts are the resolved connection facts for the active env's redis.
type RedisFacts struct {
	Host string
	Port string
}

// urlScheme returns the URL scheme most ORMs/drivers accept for the engine.
func (f DBFacts) urlScheme() string {
	if f.Engine == "postgres" {
		return "postgresql"
	}
	return "mysql"
}

// url builds a connection URL (scheme://user:pass@host:port/name). An empty
// scheme defaults to urlScheme(); callers pass an explicit scheme where the
// framework's driver differs (e.g. Rails wants "mysql2").
func (f DBFacts) url(scheme string) string {
	if scheme == "" {
		scheme = f.urlScheme()
	}
	return fmt.Sprintf("%s://%s:%s@%s:%s/%s", scheme, f.User, f.Password, f.Host, f.Port, f.Name)
}

// jdbc builds a JDBC URL for JVM stacks (no embedded credentials).
func (f DBFacts) jdbc() string {
	eng := "mysql"
	if f.Engine == "postgres" {
		eng = "postgresql"
	}
	return fmt.Sprintf("jdbc:%s://%s:%s/%s", eng, f.Host, f.Port, f.Name)
}

func (r RedisFacts) url() string { return fmt.Sprintf("redis://%s:%s", r.Host, r.Port) }

// Blueprint is one stack's defaults.
type Blueprint struct {
	ID       string // "laravel", "nodejs", "spring", …
	Label    string
	Language string // php | node | java | python | go | dotnet | ruby | static

	// App-service defaults.
	Port        string // container port the app listens on
	Healthcheck string // default healthcheck command (a starting guess; user edits)
	WebRouted   bool   // the app itself is the web entry (terminates HTTP)

	// NeedsNginx: the app does NOT speak HTTP (e.g. php-fpm) and needs an nginx
	// front service. NginxConf names templates/nginx/<x>.conf to render.
	NeedsNginx bool
	NginxConf  string

	// Template names templates/dockerfiles/<x> for the no-repo scaffold case.
	// Unused for repo-scanned services (those build from the repo's own Dockerfile).
	Template string

	// EnvVars declares how this framework reads managed-dependency connection
	// info. Given the active env's db/redis facts (either may be nil when the dep
	// is disabled), it returns the framework-specific env keys+values to write
	// into the generated .env — e.g. Laravel → DB_HOST/DB_DATABASE/…, Rails →
	// DATABASE_URL, Spring → SPRING_DATASOURCE_URL. nil ⇒ the stack has no
	// standard convention (the user maps it via per-service env vars). This is
	// where the per-framework env contract lives, keeping envgen language-agnostic.
	EnvVars func(db *DBFacts, redis *RedisFacts) map[string]string
}

// ── Per-framework env contracts ──────────────────────────────────────────────
// Each returns the env keys its framework reads, filled from the active deps.

// envLaravel: discrete DB_* (driver "mysql"/"pgsql") + REDIS_HOST/PORT.
func envLaravel(db *DBFacts, redis *RedisFacts) map[string]string {
	m := map[string]string{}
	if db != nil {
		driver := "mysql"
		if db.Engine == "postgres" {
			driver = "pgsql"
		}
		m["DB_CONNECTION"] = driver
		m["DB_HOST"] = db.Host
		m["DB_PORT"] = db.Port
		m["DB_DATABASE"] = db.Name
		m["DB_USERNAME"] = db.User
		m["DB_PASSWORD"] = db.Password
	}
	if redis != nil {
		m["REDIS_HOST"] = redis.Host
		m["REDIS_PORT"] = redis.Port
	}
	return m
}

// envURL: DATABASE_URL + REDIS_URL — the common convention for Node ORMs
// (Prisma/Sequelize), Django (dj-database-url), and FastAPI.
func envURL(db *DBFacts, redis *RedisFacts) map[string]string {
	m := map[string]string{}
	if db != nil {
		m["DATABASE_URL"] = db.url("")
	}
	if redis != nil {
		m["REDIS_URL"] = redis.url()
	}
	return m
}

// envRails: DATABASE_URL with Ruby's "mysql2" driver scheme + REDIS_URL.
func envRails(db *DBFacts, redis *RedisFacts) map[string]string {
	m := map[string]string{}
	if db != nil {
		scheme := "mysql2"
		if db.Engine == "postgres" {
			scheme = "postgresql"
		}
		m["DATABASE_URL"] = db.url(scheme)
	}
	if redis != nil {
		m["REDIS_URL"] = redis.url()
	}
	return m
}

// envSpring: Spring Boot's SPRING_DATASOURCE_* (JDBC URL) + SPRING_DATA_REDIS_*.
func envSpring(db *DBFacts, redis *RedisFacts) map[string]string {
	m := map[string]string{}
	if db != nil {
		m["SPRING_DATASOURCE_URL"] = db.jdbc()
		m["SPRING_DATASOURCE_USERNAME"] = db.User
		m["SPRING_DATASOURCE_PASSWORD"] = db.Password
	}
	if redis != nil {
		m["SPRING_DATA_REDIS_HOST"] = redis.Host
		m["SPRING_DATA_REDIS_PORT"] = redis.Port
	}
	return m
}

// envDotnet: ASP.NET ConnectionStrings__* (double-underscore config binding).
func envDotnet(db *DBFacts, redis *RedisFacts) map[string]string {
	m := map[string]string{}
	if db != nil {
		m["ConnectionStrings__DefaultConnection"] = fmt.Sprintf(
			"Server=%s;Port=%s;Database=%s;User Id=%s;Password=%s;",
			db.Host, db.Port, db.Name, db.User, db.Password)
	}
	if redis != nil {
		m["ConnectionStrings__Redis"] = fmt.Sprintf("%s:%s", redis.Host, redis.Port)
	}
	return m
}

// registry is keyed by blueprint ID.
var registry = map[string]Blueprint{
	"laravel": {
		ID: "laravel", Label: "Laravel (PHP-FPM)", Language: "php",
		Port: "9000", Healthcheck: "php -r 'exit(0);' 2>/dev/null || exit 1",
		WebRouted: false, NeedsNginx: true, NginxConf: "laravel", Template: "laravel",
		EnvVars: envLaravel,
	},
	"nodejs": {
		ID: "nodejs", Label: "Node.js (API)", Language: "node",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "nodejs",
		EnvVars: envURL,
	},
	"nextjs": {
		ID: "nextjs", Label: "Next.js (SSR)", Language: "node",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "nextjs",
		EnvVars: envURL,
	},
	"static": {
		ID: "static", Label: "Static SPA", Language: "static",
		Port: "80", Healthcheck: "wget -qO- http://localhost/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "react",
		// Static SPAs have no server-side DB access; nothing to wire.
	},
	"spring": {
		ID: "spring", Label: "Spring Boot (Java)", Language: "java",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/actuator/health >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "spring",
		EnvVars: envSpring,
	},
	"django": {
		ID: "django", Label: "Django / FastAPI (Python)", Language: "python",
		Port: "8000", Healthcheck: "wget -qO- http://localhost:8000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "django",
		EnvVars: envURL,
	},
	"go": {
		ID: "go", Label: "Go", Language: "go",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "go",
		EnvVars: envURL, // Go has no std convention; DATABASE_URL is the common pick.
	},
	"dotnet": {
		ID: "dotnet", Label: ".NET (C#)", Language: "dotnet",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "dotnet",
		EnvVars: envDotnet,
	},
	"rails": {
		ID: "rails", Label: "Ruby on Rails", Language: "ruby",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "rails",
		EnvVars: envRails,
	},
}

// Get returns the blueprint for id, and whether it exists.
func Get(id string) (Blueprint, bool) {
	b, ok := registry[id]
	return b, ok
}

// All returns every blueprint (unordered).
func All() []Blueprint {
	out := make([]Blueprint, 0, len(registry))
	for _, b := range registry {
		out = append(out, b)
	}
	return out
}
