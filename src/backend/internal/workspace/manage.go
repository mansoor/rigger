package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/wspath"
)

// RenameWorkspace updates a workspace's free-form display name in workspace.json.
// The key (folder name) is immutable and never changes here.
func RenameWorkspace(workspacesDir, key, displayName string) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("invalid workspace key %q", key)
	}
	displayName = strings.TrimSpace(displayName)
	if !validDisplayName.MatchString(displayName) {
		return fmt.Errorf("invalid workspace name %q: 1–32 chars, letters/digits/space/dash/underscore", displayName)
	}
	marker := wspath.WorkspaceMeta(workspacesDir, key)
	if _, err := os.Stat(marker); err != nil {
		return fmt.Errorf("workspace %q not found", key)
	}
	meta, _ := json.MarshalIndent(map[string]any{"name": displayName, "key": key}, "", "  ")
	return os.WriteFile(marker, meta, 0644)
}

// MoveProject relocates a project's folder from one workspace to another. The
// project's resource_prefix is intentionally left UNCHANGED — it is the immutable
// Docker identity for the project's running containers and data volumes, so the
// move is a pure folder relocation (no Docker work, no DB changes, since all
// project-scoped DB rows are keyed by the unchanged resource_prefix).
//
// If the destination already has a project with the same key, the moved project's
// KEY (folder + config.project.key, i.e. its URL identity) is suffixed to stay
// unique within the destination; its resource_prefix still does not change.
// Returns the (possibly new) project key in the destination.
func MoveProject(workspacesDir, srcWs, projKey, destWs string) (string, error) {
	src := wspath.ProjectDir(workspacesDir, srcWs, projKey)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("project %q not found in workspace %q", projKey, srcWs)
	}
	if err := os.MkdirAll(wspath.ProjectsDir(workspacesDir, destWs), 0755); err != nil {
		return "", fmt.Errorf("prepare destination: %w", err)
	}

	newKey := projKey
	if _, err := os.Stat(wspath.ProjectDir(workspacesDir, destWs, newKey)); err == nil {
		for i := 2; i < 1000; i++ {
			cand := projKey + strconv.Itoa(i)
			if !validKey.MatchString(cand) {
				continue
			}
			if _, e := os.Stat(wspath.ProjectDir(workspacesDir, destWs, cand)); os.IsNotExist(e) {
				newKey = cand
				break
			}
		}
		if newKey == projKey {
			return "", fmt.Errorf("could not find a free key for %q in %q", projKey, destWs)
		}
	}

	dest := wspath.ProjectDir(workspacesDir, destWs, newKey)
	if err := os.Rename(src, dest); err != nil {
		return "", fmt.Errorf("move project: %w", err)
	}
	if newKey != projKey {
		// Update only the URL/folder key; resource_prefix stays as-is.
		if err := setProjectKey(filepath.Join(dest, "config.json"), newKey); err != nil {
			return newKey, fmt.Errorf("update project key after move: %w", err)
		}
	}
	return newKey, nil
}

// setProjectKey rewrites config.project.key without touching anything else
// (resource_prefix, name, etc. are preserved).
func setProjectKey(cfgPath, newKey string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if proj, ok := cfg["project"].(map[string]any); ok {
		proj["key"] = newKey
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}

// ProjectKeys returns the project keys (folder names) in a workspace.
func ProjectKeys(workspacesDir, wsKey string) []string {
	entries, err := os.ReadDir(wspath.ProjectsDir(workspacesDir, wsKey))
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	return keys
}
