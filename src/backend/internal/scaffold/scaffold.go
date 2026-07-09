// Package scaffold materializes a minimal, guaranteed-to-build starter application
// for a "start from a stack template" (blueprint) project. Starters are bundled under
// templates/scaffold/{id}/ and copied verbatim — deterministic and offline, unlike
// running each framework's own generator. The generated tree is deliberately
// Dockerfile-less: the build path scaffolds the blueprint Dockerfile into the context
// at build time, so the repo stays clean app-code-only and Rigger owns the plumbing.
package scaffold

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DevInstructions are the local-development commands shown to the user after create,
// so they can clone the pushed repo and run the app on their own machine.
type DevInstructions struct {
	Install string `json:"install"` // one-time dependency install (+ any setup)
	Run     string `json:"run"`     // start the dev server
}

// instructions is the single source of truth for per-framework local-dev commands,
// keyed by blueprint id (matches templates/scaffold/{id}/ and the Dockerfile template).
var instructions = map[string]DevInstructions{
	"laravel": {Install: "composer install && cp .env.example .env && php artisan key:generate", Run: "php artisan serve"},
	"nodejs":  {Install: "npm install", Run: "npm run dev"},
	"react":   {Install: "npm install", Run: "npm run dev"},
	"django":  {Install: "pip install -r requirements.txt && python manage.py migrate", Run: "python manage.py runserver"},
	"go":      {Install: "go mod download", Run: "go run ."},
}

// Supported reports whether a bundled starter exists for the blueprint id.
func Supported(blueprintID string) bool {
	_, ok := instructions[blueprintID]
	return ok
}

// Instructions returns the local-dev commands for a blueprint id (zero value if none).
func Instructions(blueprintID string) DevInstructions {
	return instructions[blueprintID]
}

// Generate copies the bundled starter for blueprintID from templatesDir/scaffold/{id}/
// into destDir (created if absent). Returns false (no error) when no starter is bundled
// for that id, so callers can skip cleanly for not-yet-supported frameworks.
func Generate(templatesDir, blueprintID, destDir string) (bool, error) {
	if strings.TrimSpace(blueprintID) == "" {
		return false, nil
	}
	src := filepath.Join(templatesDir, "scaffold", blueprintID)
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return false, err
	}
	if err := copyTree(src, destDir); err != nil {
		return false, err
	}
	return true, nil
}

// copyTree recursively copies the contents of src into dst, preserving the regular-file
// mode bits (so an executable like artisan stays executable). Directories are created
// as needed; symlinks and other special files are skipped defensively.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue // skip symlinks / devices / sockets
		}
		if err := copyFile(s, d, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
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

var appKeyLine = regexp.MustCompile(`(?m)^APP_KEY=.*$`)

// NewLaravelAppKey returns a fresh, correctly-shaped Laravel key: base64:<32 bytes>.
func NewLaravelAppKey() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return "base64:" + base64.StdEncoding.EncodeToString(buf)
}

// EnsureLaravelAppKey fills a fresh, correctly-shaped APP_KEY into the scaffold's
// .env.example when it is blank, so the pushed repo (used for local dev via
// `cp .env.example .env`) has a working key. No-op if there is no .env.example or the
// key is already set. The deployed container gets its APP_KEY from the project's env.
func EnsureLaravelAppKey(destDir string) error {
	envPath := filepath.Join(destDir, ".env.example")
	b, err := os.ReadFile(envPath)
	if err != nil {
		return nil // no .env.example → nothing to do
	}
	content := string(b)
	m := appKeyLine.FindString(content)
	if m != "" && strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(m, "APP_KEY="), "base64:")) != "" {
		return nil // already has a real key
	}
	key := "APP_KEY=" + NewLaravelAppKey()
	if m != "" {
		content = appKeyLine.ReplaceAllString(content, key)
	} else {
		content = strings.TrimRight(content, "\n") + "\n" + key + "\n"
	}
	return os.WriteFile(envPath, []byte(content), 0o644)
}

// TemplateID extracts the blueprint id from a project's services graph — the first
// build service's build.template. services is the raw []map[string]any from config.
func TemplateID(services []map[string]any) string {
	for _, svc := range services {
		b, ok := svc["build"].(map[string]any)
		if !ok {
			continue
		}
		if t, _ := b["template"].(string); t != "" {
			return t
		}
	}
	return ""
}
