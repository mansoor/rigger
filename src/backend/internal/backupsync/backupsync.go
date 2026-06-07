// Package backupsync uploads local backup snapshot files to a remote target
// (S3-compatible object storage or SFTP). It is decoupled from backup
// execution: callers point it at a finished snapshot directory and a key
// prefix, and it mirrors the directory to the target.
package backupsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/mansoor/rigger/ui/internal/settings"
)

// Result summarizes an upload.
type Result struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Syncer uploads backup snapshot files to a remote target.
type Syncer interface {
	// Test verifies connectivity, credentials, and that the destination is
	// usable (bucket exists / remote path is writable).
	Test(ctx context.Context) error
	// UploadDir mirrors every regular file under localDir to the target,
	// rooted at keyPrefix (joined with the target's own prefix/remote path).
	UploadDir(ctx context.Context, localDir, keyPrefix string) (Result, error)
}

// New builds a Syncer from a backup target's stored config.
func New(t settings.BackupTarget) (Syncer, error) {
	switch t.Type {
	case "s3":
		var cfg settings.S3Config
		if err := json.Unmarshal(t.Config, &cfg); err != nil {
			return nil, fmt.Errorf("invalid s3 config: %w", err)
		}
		return newS3(cfg)
	case "sftp":
		var cfg settings.SFTPConfig
		if err := json.Unmarshal(t.Config, &cfg); err != nil {
			return nil, fmt.Errorf("invalid sftp config: %w", err)
		}
		return newSFTP(cfg)
	default:
		return nil, fmt.Errorf("unsupported backup target type %q", t.Type)
	}
}

// walkFiles returns the relative paths (slash-normalized later) of every
// regular file under dir.
func walkFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}
