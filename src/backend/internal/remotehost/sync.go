package remotehost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PushDir tar+gzips localDir and extracts it into remoteDir on the host, creating
// remoteDir first. Entries in skip (relative paths, e.g. ".env") are omitted —
// used to preserve a host-authoritative .env when shipping a regenerated env dir.
func (c *Client) PushDir(localDir, remoteDir string, skip ...string) error {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	cmd := fmt.Sprintf("mkdir -p %s && tar -xzf - -C %s", shQuote(remoteDir), shQuote(remoteDir))
	if err := sess.Start(cmd); err != nil {
		return err
	}

	tarErr := writeTarGz(stdin, localDir, skip)
	_ = stdin.Close() // signal EOF so the remote tar finishes
	waitErr := sess.Wait()

	if tarErr != nil {
		return fmt.Errorf("archive %s: %w", localDir, tarErr)
	}
	if waitErr != nil {
		return fmt.Errorf("remote extract into %s: %w: %s", remoteDir, waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// PullDir streams a tar+gzip of remoteDir from the host and extracts it into
// localDir (creating it). Used by migration to bring a remote backup home.
func (c *Client) PullDir(remoteDir, localDir string) error {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	cmd := fmt.Sprintf("tar -czf - -C %s .", shQuote(remoteDir))
	if err := sess.Start(cmd); err != nil {
		return err
	}
	extractErr := extractTarGz(stdout, localDir)
	waitErr := sess.Wait()

	if waitErr != nil {
		return fmt.Errorf("remote archive %s: %w: %s", remoteDir, waitErr, strings.TrimSpace(stderr.String()))
	}
	if extractErr != nil {
		return fmt.Errorf("extract into %s: %w", localDir, extractErr)
	}
	return nil
}

// writeTarGz walks root and writes a gzip-compressed tar of its regular files and
// directories to w. Paths in skip (relative to root, slash-separated) are omitted.
func writeTarGz(w io.Writer, root string, skip []string) error {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[filepath.ToSlash(s)] = true
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if skipSet[relSlash] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Only regular files and directories are shipped (env dirs have no symlinks).
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = relSlash
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})

	// Close in order; surface the first error.
	twErr := tw.Close()
	gzErr := gz.Close()
	if walkErr != nil {
		return walkErr
	}
	if twErr != nil {
		return twErr
	}
	return gzErr
}

// extractTarGz reads a gzip-compressed tar from r and writes its entries under
// dest. Entries are sanitized against path traversal outside dest.
func extractTarGz(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue // reject traversal / absolute paths
		}
		target := filepath.Join(dest, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // env dirs are small, trusted
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
