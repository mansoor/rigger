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

// StorageFacts are the resolved facts for the active env's object/file storage. The two
// backends are INDEPENDENT (both may be on):
//   - Local: persist files to a local volume (no S3 fields).
//   - S3:    managed MinIO; frameworks that speak S3 (e.g. Laravel's flysystem s3 driver)
//     map the S3 fields onto their AWS_*-style keys. Region is a placeholder most SDKs
//     require but MinIO ignores.
// DefaultDisk is the framework's default filesystem disk ("s3" when MinIO is on — S3 wins
// when both are enabled — else "local").
type StorageFacts struct {
	Local       bool
	S3          bool
	DefaultDisk string // "s3" | "local"
	KeyID       string
	SecretKey   string
	Bucket      string
	Endpoint    string
	Region      string
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

// httpHealth builds a healthcheck that probes an HTTP endpoint, trying wget then
// curl — many official images ship only one (or, like node-slim, neither, which
// is why node stacks use nodeHealth instead). Runs under compose's CMD-SHELL.
func httpHealth(port, path string) string {
	url := "http://localhost:" + port + path
	return "wget -qO- " + url + " >/dev/null 2>&1 || curl -fsS " + url + " >/dev/null 2>&1"
}

// nodeHealth builds a healthcheck using node itself — the one interpreter
// guaranteed present in a node image (node-slim has neither wget nor curl, so
// the wget probe silently fails and marks the container unhealthy though it
// serves fine). Single quotes inside the -e arg avoid CMD-SHELL quote nesting.
func nodeHealth(port string) string {
	return "node -e \"require('http').get('http://localhost:" + port +
		"/',r=>process.exit(r.statusCode<500?0:1)).on('error',()=>process.exit(1))\""
}

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
	// info. Given the active env's db/redis/storage facts (any may be nil when the
	// dep is disabled), it returns the framework-specific env keys+values to write
	// into the generated .env — e.g. Laravel → DB_HOST/DB_DATABASE/… + AWS_*/FILESYSTEM_DISK,
	// Rails → DATABASE_URL, Spring → SPRING_DATASOURCE_URL. nil ⇒ the stack has no
	// standard convention (the user maps it via per-service env vars). This is
	// where the per-framework env contract lives, keeping envgen language-agnostic.
	EnvVars func(db *DBFacts, redis *RedisFacts, storage *StorageFacts) map[string]string

	// ServiceEnv is static, per-service environment the framework needs to run
	// correctly in a container — seeded onto the service itself (its compose
	// `environment:`), NOT the shared .env, so it never leaks to other services.
	// e.g. Next.js standalone reads $HOSTNAME as the bind address, and Docker
	// defaults it to the container id → the server fails with getaddrinfo
	// EAI_AGAIN; forcing HOSTNAME=0.0.0.0 makes it bind all interfaces.
	ServiceEnv map[string]string
}

// ── Per-framework env contracts ──────────────────────────────────────────────
// Each returns the env keys its framework reads, filled from the active deps.

// envLaravel: discrete DB_* (driver "mysql"/"pgsql") + REDIS_HOST/PORT, plus the
// storage contract — FILESYSTEM_DISK=local for a local volume, or the AWS_* / s3
// contract so an app wires to managed MinIO object storage.
func envLaravel(db *DBFacts, redis *RedisFacts, storage *StorageFacts) map[string]string {
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
	if storage != nil {
		// S3 (MinIO): Laravel's flysystem "s3" disk reads AWS_*; path-style + a custom
		// endpoint point it at MinIO instead of real AWS. Emitted whenever MinIO is on,
		// even if local is also on (the app can use both disks).
		if storage.S3 {
			m["AWS_ACCESS_KEY_ID"] = storage.KeyID
			m["AWS_SECRET_ACCESS_KEY"] = storage.SecretKey
			m["AWS_DEFAULT_REGION"] = storage.Region
			m["AWS_BUCKET"] = storage.Bucket
			m["AWS_ENDPOINT"] = storage.Endpoint
			m["AWS_USE_PATH_STYLE_ENDPOINT"] = "true"
		}
		// Default filesystem disk: s3 when MinIO is on (wins when both), else local.
		if storage.DefaultDisk != "" {
			m["FILESYSTEM_DISK"] = storage.DefaultDisk
		}
	}
	return m
}

// envURL: DATABASE_URL + REDIS_URL — the common convention for Node ORMs
// (Prisma/Sequelize), Django (dj-database-url), and FastAPI.
func envURL(db *DBFacts, redis *RedisFacts, _ *StorageFacts) map[string]string {
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
func envRails(db *DBFacts, redis *RedisFacts, _ *StorageFacts) map[string]string {
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
func envSpring(db *DBFacts, redis *RedisFacts, _ *StorageFacts) map[string]string {
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
func envDotnet(db *DBFacts, redis *RedisFacts, _ *StorageFacts) map[string]string {
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
		ID: "laravel", Label: "Laravel (PHP)", Language: "php",
		// The scaffolded Laravel image is self-contained and serves via `php artisan
		// serve` on :80 (no separate nginx/php-fpm) — see templates/dockerfiles/laravel.
		Port: "80", Healthcheck: "php -r 'exit(0);' 2>/dev/null || exit 1",
		WebRouted: true, NeedsNginx: false, NginxConf: "", Template: "laravel",
		EnvVars: envLaravel,
	},
	"nodejs": {
		ID: "nodejs", Label: "Node.js (API)", Language: "node",
		Port: "3000", Healthcheck: nodeHealth("3000"),
		WebRouted: true, Template: "nodejs",
		EnvVars: envURL,
	},
	"nextjs": {
		ID: "nextjs", Label: "Next.js (SSR)", Language: "node",
		Port: "3000", Healthcheck: nodeHealth("3000"),
		WebRouted: true, Template: "nextjs",
		EnvVars: envURL,
		// Next.js standalone binds to $HOSTNAME, which Docker sets to the
		// container id; force 0.0.0.0 so the server starts.
		ServiceEnv: map[string]string{"HOSTNAME": "0.0.0.0"},
	},
	"static": {
		ID: "static", Label: "Static SPA", Language: "static",
		Port: "80", Healthcheck: httpHealth("80", "/"),
		WebRouted: true, Template: "react",
		// Static SPAs have no server-side DB access; nothing to wire.
	},
	"spring": {
		ID: "spring", Label: "Spring Boot (Maven)", Language: "java",
		Port: "8080", Healthcheck: httpHealth("8080", "/actuator/health"),
		WebRouted: true, Template: "spring",
		EnvVars: envSpring,
	},
	// Gradle Spring Boot: same runtime contract as Maven, only the build tool (and
	// thus the scaffolded Dockerfile's build stage) differs.
	"spring-gradle": {
		ID: "spring-gradle", Label: "Spring Boot (Gradle)", Language: "java",
		Port: "8080", Healthcheck: httpHealth("8080", "/actuator/health"),
		WebRouted: true, Template: "spring-gradle",
		EnvVars: envSpring,
	},
	"django": {
		ID: "django", Label: "Django / FastAPI (Python)", Language: "python",
		Port: "8000", Healthcheck: httpHealth("8000", "/"),
		WebRouted: true, Template: "django",
		EnvVars: envURL,
	},
	"go": {
		ID: "go", Label: "Go", Language: "go",
		Port: "8080", Healthcheck: httpHealth("8080", "/"),
		WebRouted: true, Template: "go",
		EnvVars: envURL, // Go has no std convention; DATABASE_URL is the common pick.
	},
	"dotnet": {
		ID: "dotnet", Label: ".NET (C#)", Language: "dotnet",
		Port: "8080", Healthcheck: httpHealth("8080", "/"),
		WebRouted: true, Template: "dotnet",
		EnvVars: envDotnet,
	},
	"rails": {
		ID: "rails", Label: "Ruby on Rails", Language: "ruby",
		Port: "3000", Healthcheck: httpHealth("3000", "/"),
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
