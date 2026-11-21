package api

// Moving existing environments off MinIO's withdrawn images.
//
// MinIO's own image repositories (minio/minio and minio/mc, on Docker Hub and
// quay.io) stopped serving pulls, so an environment generated before v0.1.49
// can't start on a host without a cached copy, and Update fails at its pull
// step. New environments are generated with Pigsty's community fork
// (composegen.MinIOImage). This boot step points existing ones at it too: the
// image references in each environment's generated docker-compose.yml, and in
// config.json for services a project runs directly (the MinIO stack template).
// Only the repository changes; tags are kept. It runs on every boot and is
// idempotent, so a compose file restored from an older backup is fixed at the
// next start. Remote hosts get the rewritten file with the environment's next
// deploy, which pushes the env dir first.

import (
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/mansoor/rigger/ui/internal/composegen"
	"github.com/mansoor/rigger/ui/internal/wspath"
)

// The upstream image, by any registry spelling: minio/minio, quay.io/minio/mc,
// docker.io/minio/minio. Submatch: "minio" or "mc".
const upstreamMinIORepo = `(?:(?:docker\.io|index\.docker\.io|registry-1\.docker\.io|quay\.io)/)?minio/(minio|mc)`

// upstreamMinIOComposeRe matches a compose `image:` line naming an upstream MinIO
// image. The reference must end the value, so minio/minio-operator and the like
// are left alone, and so are commented-out lines.
var upstreamMinIOComposeRe = regexp.MustCompile(`(?m)^([ \t]*image:[ \t]*["']?)` + upstreamMinIORepo +
	`((?::[^\s"'#]+)?)(["']?[ \t]*(?:#.*)?)$`)

// upstreamMinIOConfigRe matches the same images as a config.json "image" value.
var upstreamMinIOConfigRe = regexp.MustCompile(`("image"\s*:\s*")` + upstreamMinIORepo + `((?::[^"\s]+)?)(")`)

var minioFork = map[string]string{"minio": composegen.MinIOImage, "mc": composegen.MinIOClientImage}

// rewriteMinIOImages points every upstream MinIO image reference re matches at
// the fork, keeping the tag, and reports how many it changed. Both patterns
// capture (prefix, repo, tag, suffix).
func rewriteMinIOImages(content []byte, re *regexp.Regexp) ([]byte, int) {
	n := 0
	out := re.ReplaceAllFunc(content, func(match []byte) []byte {
		m := re.FindSubmatch(match)
		n++
		repl := append([]byte{}, m[1]...)
		repl = append(repl, minioFork[string(m[2])]...)
		repl = append(repl, m[3]...)
		return append(repl, m[4]...)
	})
	return out, n
}

// MigrateMinIOImages points every existing environment at the MinIO fork. See
// the file comment.
func (h *Handler) MigrateMinIOImages() {
	wsEntries, err := os.ReadDir(h.workspacesDir)
	if err != nil {
		return
	}
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		ws := we.Name()
		projEntries, err := os.ReadDir(wspath.ProjectsDir(h.workspacesDir, ws))
		if err != nil {
			continue
		}
		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			proj := pe.Name()
			if n := rewriteMinIOFile(wspath.ConfigPath(h.workspacesDir, ws, proj), upstreamMinIOConfigRe); n > 0 {
				log.Printf("minio: %s/%s config.json: %d image reference(s) moved to the %s fork", ws, proj, n, composegen.MinIOImage)
			}
			envEntries, err := os.ReadDir(wspath.EnvsDir(h.workspacesDir, ws, proj))
			if err != nil {
				continue
			}
			for _, ee := range envEntries {
				if !ee.IsDir() {
					continue
				}
				path := filepath.Join(wspath.EnvDir(h.workspacesDir, ws, proj, ee.Name()), "docker-compose.yml")
				if n := rewriteMinIOFile(path, upstreamMinIOComposeRe); n > 0 {
					log.Printf("minio: %s/%s/%s: %d image reference(s) moved to the %s fork", ws, proj, ee.Name(), n, composegen.MinIOImage)
				}
			}
		}
	}
}

// rewriteMinIOFile rewrites one file in place — through a temporary file and a
// rename, so a crash can't leave config.json half-written — and returns how many
// references it changed. A missing or unreadable file changes nothing.
func rewriteMinIOFile(path string, re *regexp.Regexp) int {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	out, n := rewriteMinIOImages(content, re)
	if n == 0 {
		return 0
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".minio-migrate.tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		log.Printf("minio: %s: %v", path, err)
		return 0
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		log.Printf("minio: %s: %v", path, err)
		return 0
	}
	return n
}
