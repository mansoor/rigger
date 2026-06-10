// Package blueprints holds the per-stack knowledge the repo detector maps a
// framework onto: the default service shape (listen port, healthcheck, whether
// the app terminates HTTP itself or needs an nginx front) and the Dockerfile
// template used for the no-repo scaffold case (deferred picker). It is pure data;
// the filesystem identification lives in internal/detect, which looks blueprints
// up by ID.
package blueprints

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
}

// registry is keyed by blueprint ID.
var registry = map[string]Blueprint{
	"laravel": {
		ID: "laravel", Label: "Laravel (PHP-FPM)", Language: "php",
		Port: "9000", Healthcheck: "php -r 'exit(0);' 2>/dev/null || exit 1",
		WebRouted: false, NeedsNginx: true, NginxConf: "laravel", Template: "laravel",
	},
	"nodejs": {
		ID: "nodejs", Label: "Node.js (API)", Language: "node",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "nodejs",
	},
	"nextjs": {
		ID: "nextjs", Label: "Next.js (SSR)", Language: "node",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "nextjs",
	},
	"static": {
		ID: "static", Label: "Static SPA", Language: "static",
		Port: "80", Healthcheck: "wget -qO- http://localhost/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "react",
	},
	"spring": {
		ID: "spring", Label: "Spring Boot (Java)", Language: "java",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/actuator/health >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "spring",
	},
	"django": {
		ID: "django", Label: "Django / FastAPI (Python)", Language: "python",
		Port: "8000", Healthcheck: "wget -qO- http://localhost:8000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "django",
	},
	"go": {
		ID: "go", Label: "Go", Language: "go",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "go",
	},
	"dotnet": {
		ID: "dotnet", Label: ".NET (C#)", Language: "dotnet",
		Port: "8080", Healthcheck: "wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "dotnet",
	},
	"rails": {
		ID: "rails", Label: "Ruby on Rails", Language: "ruby",
		Port: "3000", Healthcheck: "wget -qO- http://localhost:3000/ >/dev/null 2>&1 || exit 1",
		WebRouted: true, Template: "rails",
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
