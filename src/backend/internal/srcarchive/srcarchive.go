// Package srcarchive extracts an uploaded application source archive
// (.zip / .tar / .tar.gz) into an environment's source directory
// (envs/{env}/_src), so an uploaded project builds through the exact same
// _src → build pipeline a git checkout uses (see internal/gitsync). It NEVER
// executes archive contents — pure file writes — and is hardened against the
// usual archive attacks (path traversal / zip-slip, symlink escapes, zip bombs).
package srcarchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mansoor/rigger/ui/internal/gitsync"
)

// Guard rails. The byte cap bounds a decompression bomb (we count bytes actually
// written, never trusting the archive's declared sizes); the entry cap bounds a
// many-tiny-files bomb.
// var (not const) so tests can lower them; production values bound a decompression
// bomb (bytes actually written, never trusting declared sizes) and a many-files bomb.
var (
	maxTotalBytes = int64(2) << 30 // 2 GiB uncompressed
	maxEntries    = 200_000
)

// ExtractToSrc extracts archivePath into SrcDir(envDir) (envs/{env}/_src) so an
// uploaded source feeds the same _src → build pipeline a git checkout does. It skips
// re-extraction when the stored archive is UNCHANGED since the last extraction
// (recorded in a sibling .src-stamp): re-extracting an unchanged tree every build is
// needless and very slow on network / Windows bind mounts (where even `du` times out
// for a large source). Replace-source writes a new archive ⇒ a new stamp ⇒ re-extract.
func ExtractToSrc(envDir, archivePath string, out io.Writer) (string, error) {
	if out == nil {
		out = io.Discard
	}
	src := gitsync.SrcDir(envDir)
	stampPath := filepath.Join(envDir, ".src-stamp")
	want := archiveStamp(archivePath)

	if want != "" && dirHasFiles(src) {
		if got, _ := os.ReadFile(stampPath); string(got) == want {
			// Say what this does NOT do. An uploaded project has no repo to pull
			// from, so a rebuild cannot pick up a source edit — and someone
			// rebuilding to apply a change reads a bare "unchanged ✓" as progress.
			fmt.Fprintf(out, "✓ Uploaded source unchanged — reusing existing checkout\n")
			fmt.Fprintf(out, "  (this project's source is an uploaded archive, so a rebuild can't pick up source\n")
			fmt.Fprintf(out, "   edits — use Edit Project → Services → Replace Source to upload a new version)\n")
			return src, nil
		}
	}

	fmt.Fprintf(out, "⟳ Extracting uploaded source → %s\n", filepath.Base(src))
	_ = os.RemoveAll(src)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", err
	}
	if err := Extract(archivePath, src); err != nil {
		_ = os.RemoveAll(src) // don't leave a half-extracted tree
		return "", err
	}
	if want != "" {
		_ = os.WriteFile(stampPath, []byte(want), 0o644)
	}
	return src, nil
}

// Stamp returns a cheap content-identity for an archive (size + mod time) — the same
// signal ExtractToSrc uses to detect a Replace-source. Exported for the build
// "if changed" mode (change-detection of an uploaded source). Empty if it can't stat.
func Stamp(archivePath string) string { return archiveStamp(archivePath) }

// archiveStamp identifies an archive by size + mod time (enough to detect a
// Replace-source). Empty if the archive can't be stat'd.
func archiveStamp(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

func dirHasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// Extract unpacks a .zip or .tar(.gz) archive into destDir. The format is sniffed
// from magic bytes (not the filename). Protections: rejects entries escaping
// destDir; skips symlinks/hardlinks/devices and macOS resource forks; caps total
// bytes + entry count; drops any committed `.env` (keeps `.env.example`). A single
// common top-level directory is stripped so destDir is the app root.
func Extract(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	magic := make([]byte, 4)
	_, _ = io.ReadFull(f, magic)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	lim := &limits{bytes: maxTotalBytes, entries: maxEntries}
	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")) || bytes.HasPrefix(magic, []byte("PK\x05\x06")):
		if err := extractZip(archivePath, destDir, lim); err != nil {
			return err
		}
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b: // gzip
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		if err := extractTar(tar.NewReader(gz), destDir, lim); err != nil {
			return err
		}
	default: // assume uncompressed tar
		if err := extractTar(tar.NewReader(f), destDir, lim); err != nil {
			return err
		}
	}
	return stripSingleRoot(destDir)
}

// limits tracks remaining byte/entry budget across an extraction.
type limits struct {
	bytes   int64
	entries int
}

func (l *limits) entry() error {
	l.entries--
	if l.entries < 0 {
		return fmt.Errorf("archive has too many entries (>%d)", maxEntries)
	}
	return nil
}

func extractZip(archivePath, destDir string, lim *limits) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if err := lim.entry(); err != nil {
			return err
		}
		if skipEntry(zf.Name) {
			continue
		}
		mode := zf.Mode()
		if mode&fs.ModeSymlink != 0 || !(mode.IsRegular() || zf.FileInfo().IsDir()) {
			continue // skip symlinks, devices, fifos, etc.
		}
		target, ok := safeTarget(destDir, zf.Name)
		if !ok {
			return fmt.Errorf("unsafe path in archive: %q", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeFile(target, rc, lim)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(tr *tar.Reader, destDir string, lim *limits) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if err := lim.entry(); err != nil {
			return err
		}
		if skipEntry(hdr.Name) {
			continue
		}
		target, ok := safeTarget(destDir, hdr.Name)
		if !ok {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, lim); err != nil {
				return err
			}
		default:
			continue // symlinks (TypeSymlink/TypeLink), devices, fifos — skipped
		}
	}
}

// writeFile creates target (and parents) and copies r into it, enforcing the byte
// budget so a decompression bomb can't fill the disk.
func writeFile(target string, r io.Reader, lim *limits) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	w, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer w.Close()
	n, err := io.Copy(w, &limitedReader{r: r, lim: lim})
	_ = n
	return err
}

// limitedReader decrements the shared byte budget as it reads, erroring out the
// moment the cap is exceeded (counts bytes actually produced, not declared sizes).
type limitedReader struct {
	r   io.Reader
	lim *limits
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	n, err := lr.r.Read(p)
	if n > 0 {
		lr.lim.bytes -= int64(n)
		if lr.lim.bytes < 0 {
			return n, fmt.Errorf("archive exceeds %d bytes uncompressed", maxTotalBytes)
		}
	}
	return n, err
}

// safeTarget cleans an archive entry name and joins it under destDir, rejecting any
// path that would escape destDir (zip-slip). Returns the absolute target + ok.
func safeTarget(destDir, name string) (string, bool) {
	clean := path.Clean("/" + strings.ReplaceAll(name, `\`, "/")) // force-rooted, kills ".." traversal
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", false
	}
	target := filepath.Join(destDir, filepath.FromSlash(rel))
	// Final containment check (defence in depth).
	prefix := destDir + string(os.PathSeparator)
	if target != destDir && !strings.HasPrefix(target, prefix) {
		return "", false
	}
	return target, true
}

// skipEntry drops macOS resource forks and committed real `.env` files (a scanned
// repo's .env would leak secrets / clobber Rigger's generated env). `.env.example`
// and `.env.sample` are kept — the detector seeds the env's .env from them.
func skipEntry(name string) bool {
	slash := strings.ReplaceAll(name, `\`, "/")
	if slash == "__MACOSX" || strings.HasPrefix(slash, "__MACOSX/") || strings.Contains(slash, "/__MACOSX/") {
		return true
	}
	if path.Base(strings.TrimSuffix(slash, "/")) == ".env" {
		return true
	}
	return false
}

// stripSingleRoot promotes the contents of a lone top-level directory up into
// destDir, so an archive that wraps everything in one folder (e.g. CodeCanyon's
// `app-name/…`) leaves destDir as the actual app root. No-op otherwise.
func stripSingleRoot(destDir string) error {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return nil
	}
	inner := filepath.Join(destDir, entries[0].Name())
	children, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	for _, c := range children {
		from := filepath.Join(inner, c.Name())
		to := filepath.Join(destDir, c.Name())
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return os.Remove(inner)
}
