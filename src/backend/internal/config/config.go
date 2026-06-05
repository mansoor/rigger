package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	// Server
	ListenAddr string
	// Paths
	ToolkitRoot   string
	WorkspacesDir string
	TemplatesDir  string
	DataDir       string
	// RemoteWorkspacesDir is the WORKSPACES_DIR path on remote hosts (Phase 7).
	// Defaults to the local WorkspacesDir since remote hosts typically mirror it.
	RemoteWorkspacesDir string
	// Auth
	JWTSecret     string
	JWTExpiry     int
	RefreshExpiry int
	// Notifications
	// AppriseURL is the base URL of an Apprise API sidecar. When empty (the
	// default) notifications are delivered in-process via the embedded
	// apprise-go library; set it to opt into the sidecar instead.
	AppriseURL string
}

func Load() *Config {
	toolkit := getenv("TOOLKIT_ROOT", "/toolkit")
	workspaces := getenv("WORKSPACES_DIR", filepath.Join(toolkit, "workspaces"))
	return &Config{
		ListenAddr:          getenv("LISTEN_ADDR", ":8080"),
		ToolkitRoot:         toolkit,
		WorkspacesDir:       workspaces,
		TemplatesDir:        getenv("TEMPLATES_DIR", filepath.Join(toolkit, "templates")),
		DataDir:             getenv("DATA_DIR", "/data"),
		RemoteWorkspacesDir: getenv("REMOTE_WORKSPACES_DIR", workspaces),
		JWTSecret:     getenv("JWT_SECRET", "change-me-in-production"),
		JWTExpiry:     15,
		RefreshExpiry: 7,
		AppriseURL:    getenv("APPRISE_URL", ""),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
