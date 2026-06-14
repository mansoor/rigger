package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/envgen"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// Bootstrap scaffolds (or re-scaffolds) a single environment inside a workspace,
// porting scripts/bootstrap.sh natively to Go. It generates, under
// workspacesDir/<name>/envs/<env>/:
//
//	.env / .env.example  — via envgen (existing secrets preserved unless regenEnv)
//	docker-compose.yml   — via composegen
//	backend/Dockerfile + .dockerignore, frontend/… , nginx.conf, garage.toml
//	                     — custom stacks only, from templatesDir
//
// Progress is written to out. templatesDir is the toolkit's templates/ directory.
func Bootstrap(workspacesDir, templatesDir, workspaceName, name, env string, regenEnv bool, baseDomain string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	wsRoot := wspath.ProjectDir(workspacesDir, workspaceName, name)
	cfgPath := wspath.ConfigPath(workspacesDir, workspaceName, name)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg, err := wsconfig.Parse(data)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.ValidateEnv(env); err != nil {
		return err
	}
	e := cfg.Environments[env]

	outDir := filepath.Join(wsRoot, "envs", env)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	fmt.Fprintf(out, "Bootstrapping '%s'...\n", env)

	// ── 1. .env / .env.example ──────────────────────────────────────────────────
	envFile := filepath.Join(outDir, ".env")
	if _, statErr := os.Stat(envFile); statErr != nil || regenEnv {
		if err := writeEnv(cfg, env, outDir, envFile); err != nil {
			return err
		}
		fmt.Fprintf(out, "  .env generated\n")
	} else {
		fmt.Fprintf(out, "  .env exists — skipping (regen not requested)\n")
	}

	writeCompose := func() error {
		// Read the just-written .env so services that set env_file_mount get it
		// embedded as a compose config (best-effort; missing ⇒ no mount).
		envContent, _ := os.ReadFile(envFile)
		content, err := composegen.GenerateRouted(data, env, composegen.RouteOpts{BaseDomain: baseDomain, EnvFile: string(envContent)})
		if err != nil {
			return fmt.Errorf("generate compose: %w", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "docker-compose.yml"), content, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "  docker-compose.yml generated\n")
		return nil
	}

	project := cfg.Project.Name
	prefix := cfg.Project.Prefix() + "_" + env

	// 2. docker-compose.yml
	if err := writeCompose(); err != nil {
		return err
	}

	// 3. A Dockerfile per build service, scaffolded from its blueprint template —
	//    UNLESS the project has a source repo, in which case the repo's own
	//    Dockerfiles are used (cloned into envs/{env}/_src at build time). A build
	//    service with no template and no repo expects a user-supplied Dockerfile.
	if cfg.SourceRepo() == "" {
		for _, svc := range cfg.BuildServices() {
			tmpl := svc.Build.Template
			if tmpl == "" {
				continue
			}
			tmplDir := filepath.Join(templatesDir, "dockerfiles", tmpl)
			if !isDir(tmplDir) {
				return fmt.Errorf("no Dockerfile template %q for service %q: %s", tmpl, svc.Name, tmplDir)
			}
			if err := installDockerfile(tmplDir, filepath.Join(outDir, svc.ContextDir()), env); err != nil {
				return err
			}
			fmt.Fprintf(out, "  %s Dockerfile (%s) installed\n", svc.Name, tmpl)
		}
	} else {
		fmt.Fprintf(out, "  source repo configured — Dockerfiles come from the repo at build time\n")
	}

	// 4. nginx.conf for any service that fronts the app (config_template set).
	for _, svc := range cfg.Services {
		if svc.ConfigTemplate == "" {
			continue
		}
		if err := renderNginx(templatesDir, outDir, svc.ConfigTemplate, e.Domain, prefix, project, env); err != nil {
			return err
		}
		fmt.Fprintf(out, "  nginx.conf rendered (%s)\n", svc.ConfigTemplate)
	}

	// 5. garage.toml (if the managed Garage dependency is enabled)
	if cfg.EffGarage(e) {
		if err := os.WriteFile(filepath.Join(outDir, "garage.toml"), []byte(garageTOML(e.Domain)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "  garage.toml generated\n")
	}

	// 6. adminer-login.php (if an Adminer web-SQL service is present). It is
	// bind-mounted into Adminer's plugins-enabled/ dir: the URL on its own shows the
	// stock login page; it auto-logs-in only when the Manage Database UI POSTs a
	// Rigger-signed payload (the plugin fills + submits Adminer's own login form, so
	// the POST carries Adminer's valid CSRF token; creds never touch the URL).
	if hasServiceNamed(cfg, "adminer") {
		if err := os.WriteFile(filepath.Join(outDir, "adminer-login.php"), []byte(adminerLoginPHP()), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "  adminer-login.php generated\n")
	}

	fmt.Fprintf(out, "Environment '%s' bootstrapped\n", env)
	return nil
}

// writeEnv generates .env and .env.example for env, preserving existing secrets.
func writeEnv(cfg *wsconfig.Config, env, outDir, envFile string) error {
	var existing map[string]string
	if cur, err := os.ReadFile(envFile); err == nil {
		existing = envgen.ParseEnv(cur)
	}
	envOut, exampleOut, err := envgen.Generate(cfg, env, existing, envgen.CryptoRand)
	if err != nil {
		return err
	}
	if err := os.WriteFile(envFile, []byte(envOut), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, ".env.example"), []byte(exampleOut), 0o644)
}

// installDockerfile copies the Dockerfile (preferring Dockerfile.dev for dev) and
// the matching .dockerignore from a template dir into dest.
func installDockerfile(tmplDir, dest, env string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	dfSrc := filepath.Join(tmplDir, "Dockerfile")
	if env == "dev" {
		if dev := filepath.Join(tmplDir, "Dockerfile.dev"); fileExists(dev) {
			dfSrc = dev
		}
	}
	if err := copyFile(dfSrc, filepath.Join(dest, "Dockerfile"), 0o644); err != nil {
		return fmt.Errorf("copy Dockerfile: %w", err)
	}
	copyDockerignore(tmplDir, dest, env)
	return nil
}

// copyDockerignore copies the env-appropriate .dockerignore (dev uses the loose
// .dockerignore.dev), falling back to the other variant. Missing files are
// non-fatal (mirrors bootstrap.sh).
func copyDockerignore(tmplDir, dest, env string) {
	var preferred, fallback string
	if env == "dev" {
		preferred = filepath.Join(tmplDir, ".dockerignore.dev")
		fallback = filepath.Join(tmplDir, ".dockerignore")
	} else {
		preferred = filepath.Join(tmplDir, ".dockerignore")
		fallback = filepath.Join(tmplDir, ".dockerignore.dev")
	}
	src := preferred
	if !fileExists(src) {
		src = fallback
	}
	if fileExists(src) {
		_ = copyFile(src, filepath.Join(dest, ".dockerignore"), 0o644)
	}
}

// renderNginx renders templates/nginx/<backend>.conf, substituting the workspace
// placeholders, into outDir/nginx.conf.
func renderNginx(templatesDir, outDir, backend, domain, prefix, project, env string) error {
	tmpl := filepath.Join(templatesDir, "nginx", backend+".conf")
	src, err := os.ReadFile(tmpl)
	if err != nil {
		return fmt.Errorf("nginx template not found: %s", tmpl)
	}
	r := strings.NewReplacer(
		"{{DOMAIN}}", domain,
		"{{PREFIX}}", prefix,
		"{{PROJECT}}", project,
		"{{ENV}}", env,
	)
	return os.WriteFile(filepath.Join(outDir, "nginx.conf"), []byte(r.Replace(string(src))), 0o644)
}

// garageTOML renders the garage.toml content (bootstrap.sh heredoc).
func garageTOML(domain string) string {
	return fmt.Sprintf(`metadata_dir = "/meta"
data_dir     = "/data"
db_engine    = "lmdb"
replication_factor = 1

[rpc_bind_addr]
addr = "0.0.0.0:3901"

[s3_api]
s3_region     = "garage"
api_bind_addr = "0.0.0.0:3900"
root_domain   = ".s3.%s"

[s3_web]
bind_addr     = "0.0.0.0:3902"
root_domain   = ".web.%s"
index         = "index.html"
error_document = "404.html"

[admin]
api_bind_addr = "0.0.0.0:3903"
`, domain, domain)
}

// hasServiceNamed reports whether the project's service graph contains a service
// with the given name (e.g. "adminer").
func hasServiceNamed(cfg *wsconfig.Config, name string) bool {
	for _, s := range cfg.Services {
		if s.Name == name {
			return true
		}
	}
	return false
}

// adminerLoginPHP renders an Adminer plugin (dropped into plugins-enabled/) that
// powers Rigger's one-click auto-login. The Adminer URL on its own shows the normal
// login page; this only acts when Rigger's Manage Database UI POSTs a signed payload
// (rigger_login + rigger_sig = HMAC-SHA256 of the JSON using ADMINER_LOGIN_SECRET
// from the container env). On a valid payload the plugin populates $_POST['auth'] at
// load time (before Adminer reads it), so Adminer performs a normal login on this
// request — server-side, with no injected JavaScript (Adminer's CSP would block an
// un-nonced inline script) and no CSRF token needed (Adminer's login form has none).
// Credentials travel only in the POST body, never the URL.
func adminerLoginPHP() string {
	return `<?php
// Generated by Rigger — do not edit. Adminer one-click auto-login plugin.
(function () {
	if (empty($_POST['rigger_login']) || empty($_POST['rigger_sig'])) { return; }
	$secret = getenv('ADMINER_LOGIN_SECRET');
	if ($secret === false || $secret === '') { return; }
	$raw = (string) $_POST['rigger_login'];
	if (!hash_equals(hash_hmac('sha256', $raw, $secret), (string) $_POST['rigger_sig'])) { return; }
	$d = json_decode($raw, true);
	if (!is_array($d) || empty($d['exp']) || (int) $d['exp'] < time()) { return; }
	// Drive Adminer's normal login flow.
	$_POST['auth'] = [
		'driver'   => (string) ($d['driver'] ?? 'server'),
		'server'   => (string) ($d['server'] ?? ''),
		'username' => (string) ($d['username'] ?? ''),
		'password' => (string) ($d['password'] ?? ''),
		'db'       => (string) ($d['db'] ?? ''),
	];
})();
return new class {};
`
}

// ── small fs helpers ────────────────────────────────────────────────────────────

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
