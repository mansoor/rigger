// Package geoipdb manages the offline GeoIP database the Traefik geoblock plugin reads
// (IP2Location LITE DB1, IPv6 BIN). Rigger downloads/refreshes it to a shared volume
// (mounted at Traefik's /geoip) from a free IP2Location LITE download token pasted in the
// UI — the last manual asset for the GeoIP plugin. See docs/design/proxy-service.md.
package geoipdb

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the BIN the geoblock plugin expects; it must match proxyroutes geoDBPath's
// basename (/geoip/IP2LOCATION-LITE-DB1.IPV6.BIN).
const FileName = "IP2LOCATION-LITE-DB1.IPV6.BIN"

// DefaultDir is the shared GeoIP volume mount (rw in rigger, ro in traefik at /geoip).
const DefaultDir = "/geoip"

// Dir returns where the BIN is written; override via RIGGER_GEOIP_DIR for tests/dev.
func Dir() string {
	if d := strings.TrimSpace(os.Getenv("RIGGER_GEOIP_DIR")); d != "" {
		return d
	}
	return DefaultDir
}

// Path is the absolute BIN path.
func Path() string { return filepath.Join(Dir(), FileName) }

// Status reports whether the DB is present plus its size and mod time.
type Status struct {
	Present bool      `json:"present"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Stat returns the current DB status (best-effort; absent file → Present:false).
func Stat() Status {
	fi, err := os.Stat(Path())
	if err != nil {
		return Status{}
	}
	return Status{Present: true, Size: fi.Size(), ModTime: fi.ModTime()}
}

// downloadURL builds the IP2Location LITE download URL for the DB1 IPv6 BIN.
func downloadURL(token string) string {
	return "https://www.ip2location.com/download/?token=" + strings.TrimSpace(token) + "&file=DB1LITEBINIPV6"
}

// Download fetches the IP2Location LITE DB1 (IPv6, BIN) zip with the given token, extracts
// the .BIN, and writes it atomically to Path(). A bad token / quota returns the server's
// (non-zip) message as the error so the user can act on it.
func Download(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("an IP2Location LITE download token is required")
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", Dir(), err)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL(token))
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20)) // 512MB ceiling
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed (HTTP %d): %s", resp.StatusCode, snippet(body))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		// IP2Location returns a plain-text reason (e.g. NO PERMISSION / quota) when the
		// token is bad — surface it instead of an opaque "not a zip".
		return fmt.Errorf("unexpected response (check the token / daily quota): %s", snippet(body))
	}
	for _, f := range zr.File {
		if !strings.EqualFold(filepath.Base(f.Name), FileName) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s in archive: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		tmp := Path() + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return fmt.Errorf("write db: %w", err)
		}
		if err := os.Rename(tmp, Path()); err != nil {
			return fmt.Errorf("install db: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%s not found in the downloaded archive", FileName)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		s = "(empty response)"
	}
	return s
}
