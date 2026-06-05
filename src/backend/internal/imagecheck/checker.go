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
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceUpdate describes the update status of one service image.
type ServiceUpdate struct {
	Service       string `json:"service"`
	Image         string `json:"image"`
	Tag           string `json:"tag"`
	HasUpdate     bool   `json:"has_update"`               // actionable: registry has a newer digest for the SAME tag (a one-click Update clears it)
	NewerTag      string `json:"newer_tag,omitempty"`      // describes the digest drift (e.g. "11 (new digest)")
	NewerStable   string `json:"newer_stable,omitempty"`   // informational: a higher STABLE version tag exists (drives the version-available alert, NOT the badge)
	Indeterminate bool   `json:"indeterminate,omitempty"`  // true when local digest unavailable (e.g. locally-built image)
	Error         string `json:"error,omitempty"`
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
	// Accept manifest-list / OCI-index types first so multi-arch images return the
	// SAME top-level digest that `docker` records locally in RepoDigests; otherwise
	// a strict registry could hand back a single-arch digest that never matches.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
	}, ", "))
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

// newerStableTag queries the registry's tag list and returns the highest STABLE
// version tag that is genuinely newer than currentTag, or "" if none.
//
// "Stable" means a pure numeric dotted version (optionally v-prefixed) with no
// suffix — so pre-releases and OS/vendor variants (-rc, -beta, -ubi, -alpine,
// -noble, …) are all excluded. "Newer" is judged at currentTag's precision: a
// tag that merely adds more components to the same line (11 → 11.4.3) is NOT
// newer, because a rolling tag like `11` already tracks the latest 11.x; only a
// higher value at the current precision counts (11 → 12.x, 11.4 → 11.5/12.x).
func newerStableTag(image, currentTag string) string {
	cur := parseVersion(currentTag)
	if cur == nil {
		return "" // current tag isn't a pure version (e.g. has a -variant suffix) — skip
	}

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

	var best []int
	var bestStr string
	for _, t := range result.Tags {
		cand := parseVersion(t)
		if cand == nil { // not a pure stable version — skip
			continue
		}
		if !versionNewerAtPrecision(cur, cand) {
			continue
		}
		if bestStr == "" || versionLess(best, cand) {
			best, bestStr = cand, t
		}
	}
	return bestStr
}

// parseVersion parses a pure numeric dotted tag ("11", "11.4.3", optional "v"
// prefix) into its integer components, or returns nil if any part is non-numeric
// (which also rejects pre-release/variant suffixes like "13.0.1-ubi10-rc").
func parseVersion(tag string) []int {
	s := strings.TrimPrefix(tag, "v")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// versionNewerAtPrecision reports whether cand is a higher version than cur,
// comparing only up to cur's number of components. So with cur="11": "12.0.2" is
// newer but "11.4.3" is not (same major, already tracked by the rolling tag).
func versionNewerAtPrecision(cur, cand []int) bool {
	for i := 0; i < len(cur); i++ {
		c := 0
		if i < len(cand) {
			c = cand[i]
		}
		if c != cur[i] {
			return c > cur[i]
		}
	}
	return false
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

		// Actionable signal: does the registry have a newer digest for the SAME
		// configured tag? This is the only thing a one-click Update (pull the same
		// tag + recreate) can act on, and it self-clears once pulled. Works for
		// "latest" and pinned rolling tags (e.g. mariadb:11) alike.
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
			upd.NewerTag = tag + " (new digest)"
		}

		// Informational signal: is a higher STABLE version pinned-able? This does
		// NOT set HasUpdate (the Update button can't bump the pin) — it drives the
		// "newer stable version available" alert so the user is notified without a
		// badge that never clears. Skipped for "latest" (no version to compare).
		if tag != "latest" {
			if s := newerStableTag(img.Image, tag); s != "" {
				upd.NewerStable = s
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

// versionLess compares two parsed versions numerically per component so
// [1,10,2] > [1,9,0]. Missing trailing components are treated as zero.
func versionLess(a, b []int) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var na, nb int
		if i < len(a) {
			na = a[i]
		}
		if i < len(b) {
			nb = b[i]
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
