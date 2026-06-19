package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/config"
	"github.com/mansoor/rigger/ui/internal/workspace"
	"github.com/mansoor/rigger/ui/internal/wsconfig"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// runInitWorkspace is the host-side `rigger init-workspace` subcommand — the Go
// replacement for the legacy init_workspace.sh. It is non-interactive: given a
// workspace name and a prepared config.json (the same artifact the web wizard
// produces), it writes the workspace and bootstraps every environment natively
// in Go (envgen + composegen + Dockerfile/nginx install). Interactive workspace
// creation is now handled by the web UI.
//
// Usage:
//
//	rigger init-workspace -name myapp -config ./config.json
//	cat config.json | rigger init-workspace -name myapp -config -
func runInitWorkspace(args []string) int {
	fs := flag.NewFlagSet("init-workspace", flag.ContinueOnError)
	workspaceName := fs.String("workspace", "default", "parent workspace name (lowercase, digits, hyphens)")
	name := fs.String("name", "", "project name (lowercase, digits, hyphens)")
	cfgPath := fs.String("config", "", "path to a config.json ('-' for stdin)")
	wsDir := fs.String("workspaces", "", "workspaces directory (default: from WORKSPACES_DIR/TOOLKIT_ROOT)")
	tmplDir := fs.String("templates", "", "templates directory (default: from TEMPLATES_DIR/TOOLKIT_ROOT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "usage: rigger init-workspace -workspace <ws> -name <project> -config <config.json|->")
		return 2
	}

	cfg := config.Load()
	workspacesDir := *wsDir
	if workspacesDir == "" {
		workspacesDir = cfg.WorkspacesDir
	}
	templatesDir := *tmplDir
	if templatesDir == "" {
		templatesDir = cfg.TemplatesDir
	}

	// Read the config.json (file or stdin).
	var data []byte
	var err error
	if *cfgPath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*cfgPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		return 1
	}

	// Validate it parses and enumerate environments.
	parsed, err := wsconfig.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid config.json: %v\n", err)
		return 1
	}
	envs := parsed.EnvNames()
	if len(envs) == 0 {
		fmt.Fprintln(os.Stderr, "config.json defines no environments")
		return 1
	}

	if err := workspace.EnsureWorkspace(workspacesDir, *workspaceName, ""); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	wsRoot := wspath.ProjectDir(workspacesDir, *workspaceName, *name)
	if _, statErr := os.Stat(wsRoot); statErr == nil {
		fmt.Fprintf(os.Stderr, "project %q already exists at %s\n", *name, wsRoot)
		return 1
	}
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create project dir: %v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(wsRoot, "config.json"), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write config.json: %v\n", err)
		os.RemoveAll(wsRoot) //nolint:errcheck
		return 1
	}

	fmt.Printf("Created workspace %q at %s\n", *name, wsRoot)
	failed := false
	for _, env := range envs {
		fmt.Printf("\nBootstrapping environment: %s\n", env)
		// No DB in the CLI path → no system-registry resolution; pass "" so bootstrap
		// uses the project's own config.json registry (today's behavior).
		if err := workspace.Bootstrap(workspacesDir, templatesDir, *workspaceName, *name, env, false, "", "", os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "✗ bootstrap %s: %v\n", env, err)
			failed = true
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "\nWorkspace created with errors — check output above.")
		return 1
	}
	fmt.Printf("\n✓ Workspace %q is ready.\n", *name)
	return 0
}
