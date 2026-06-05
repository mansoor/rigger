// Package imagecheck queries Docker Hub to detect available image updates
// for image-stack workspaces. Results are cached and refreshed hourly by
// a background goroutine started in main.go.
package imagecheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceUpdate describes the update status of one service image.
type ServiceUpdate struct {
	Service      string `json:"service"`
	Image        string `json:"image"`
	Tag          string `json:"tag"`
	HasUpdate    bool   `json:"has_update"`
	NewerTag     string `json:"newer_tag,omitempty"`    // for pinned tags: newest available
	Indeterminate bool  `json:"indeterminate,omitempty"` // true when local digest unavailable
	Error        string `json:"error,omitempty"`
}

// CacheEntry holds check results and when they were fetched.
type CacheEntry struct {
	Results   []ServiceUpdate
	CheckedAt time.Time
}

// Cache stores image-check results keyed by "workspace/env".
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
}

func NewCache() *Cache {
	return &Cache{entries: make(map[string]*CacheEntry)}
}

func (c *Cache) Get(ws, env string) (*CacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[ws+"/"+env]
	return e, ok
}

func (c *Cache) Set(ws, env string, results []ServiceUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[ws+"/"+env] = &CacheEntry{Results: results, CheckedAt: time.Now()}
}

// Invalidate removes a cache entry so the next request triggers a fresh check.
func (c *Cache) Invalidate(ws, env string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, ws+"/"+env)
}

// ── Docker Hub API helpers ─────────────────────────────────────────────────────

func hubToken(repo string) string {
	url := fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", repo)
	resp, err := http.Get(url) //nolint:gosec,noctx
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&v) //nolint:errcheck
	return v.Token
}

func normaliseRepo(image string) string {
	if !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

// remoteDigest fetches the manifest digest for image:tag from Docker Hub.
func remoteDigest(image, tag string) string {
	repo := normaliseRepo(image)
	token := hubToken(repo)
	if token == "" {
		return ""
	}
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", repo, tag)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	resp.Body.Close()
	return resp.Header.Get("Docker-Content-Digest")
}

// localDigest gets the local image digest via docker CLI.
func localDigest(imageRef string) string {
	out, err := exec.Command("docker", "image", "inspect",
		"--format", "{{index .RepoDigests 0}}", imageRef).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if idx := strings.Index(s, "@"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// newerSemverTag queries Docker Hub tags list and returns the latest semver
// tag that sorts after currentTag, or "" if none.
func newerSemverTag(image, currentTag string) string {
	repo := normaliseRepo(image)
	token := hubToken(repo)
	if token == "" {
		return ""
	}

	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/tags/list", repo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct{ Tags []string }
	json.Unmarshal(body, &result) //nolint:errcheck

	// Filter to semver-looking tags (digits or v-prefix, with at least one dot)
	var semverTags []string
	for _, t := range result.Tags {
		norm := strings.TrimPrefix(t, "v")
		if len(norm) > 0 && norm[0] >= '0' && norm[0] <= '9' && strings.Contains(norm, ".") {
			semverTags = append(semverTags, t)
		}
	}

	// Sort numerically by each dot-separated component so "10.0" > "9.0"
	sort.Slice(semverTags, func(i, j int) bool {
		return semverLess(semverTags[i], semverTags[j])
	})

	// Find the highest tag that is greater than currentTag
	var newer string
	for _, t := range semverTags {
		if semverLess(currentTag, t) {
			newer = t // keep updating — we want the highest
		}
	}
	return newer
}

// ── Main check function ────────────────────────────────────────────────────────

// Check reads config.json for the workspace and checks each image for updates.
func Check(workspacesDir, wsName, env string) []ServiceUpdate {
	cfgPath := filepath.Join(workspacesDir, wsName, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}

	var cfg struct {
		Project struct{ Type string } `json:"project"`
		Images  []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
			Tag   string `json:"tag"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Project.Type != "image" {
		return nil
	}

	var results []ServiceUpdate
	for _, img := range cfg.Images {
		tag := img.Tag
		if tag == "" {
			tag = "latest"
		}
		fullRef := img.Image + ":" + tag
		upd := ServiceUpdate{Service: img.Name, Image: img.Image, Tag: tag}

		if tag == "latest" {
			local := localDigest(fullRef)
			remote := remoteDigest(img.Image, tag)
			switch {
			case remote == "":
				upd.Error = "could not reach registry"
			case local == "":
				// RepoDigest unavailable — image may not have been pulled from a registry,
				// or was built locally. Cannot compare digests: report as indeterminate,
				// not as "has update".
				upd.Indeterminate = true
			case local != remote:
				upd.HasUpdate = true
				upd.NewerTag = "latest (new digest)"
			}
		} else {
			newer := newerSemverTag(img.Image, tag)
			if newer != "" {
				upd.HasUpdate = true
				upd.NewerTag = newer
			}
		}

		results = append(results, upd)
	}
	return results
}

// RunBackground starts a goroutine that checks all image-stack workspaces
// every hour. Results are stored in cache.
func RunBackground(cache *Cache, workspacesDir string) {
	go func() {
		checkAll(cache, workspacesDir)
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			checkAll(cache, workspacesDir)
		}
	}()
}

// semverLess compares two version strings (e.g. "1.10.2" vs "1.9.0") numerically
// per segment so "10" > "9". Strips a leading "v" before comparing.
func semverLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(pb[i])
		}
		if na != nb {
			return na < nb
		}
	}
	return false
}

func checkAll(cache *Cache, workspacesDir string) {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsName := e.Name()
		cfgPath := filepath.Join(workspacesDir, wsName, "config.json")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		var cfg struct {
			Project      struct{ Type string }      `json:"project"`
			Environments map[string]json.RawMessage `json:"environments"`
		}
		if json.Unmarshal(data, &cfg) != nil || cfg.Project.Type != "image" {
			continue
		}
		for envName := range cfg.Environments {
			results := Check(workspacesDir, wsName, envName)
			if results != nil {
				cache.Set(wsName, envName, results)
			}
		}
	}
}
