package backupsync

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/mansoor/rigger/ui/internal/settings"
)

type s3Syncer struct {
	cfg    settings.S3Config
	client *minio.Client
}

func newS3(cfg settings.S3Config) (*s3Syncer, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}

	// Accept endpoints with or without a scheme; the scheme (if present) wins
	// over UseSSL. minio-go wants a bare host[:port].
	endpoint := cfg.Endpoint
	secure := cfg.UseSSL
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "http://"), false
	}
	endpoint = strings.TrimSuffix(endpoint, "/")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	return &s3Syncer{cfg: cfg, client: client}, nil
}

func (s *s3Syncer) Test(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket %q not found (create it first)", s.cfg.Bucket)
	}
	return nil
}

func (s *s3Syncer) UploadDir(ctx context.Context, localDir, keyPrefix string) (Result, error) {
	files, err := walkFiles(localDir)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, rel := range files {
		key := path.Join(s.cfg.PathPrefix, keyPrefix, filepath.ToSlash(rel))
		info, err := s.client.FPutObject(ctx, s.cfg.Bucket, key, filepath.Join(localDir, rel),
			minio.PutObjectOptions{ContentType: "application/octet-stream"})
		if err != nil {
			return res, fmt.Errorf("upload %s: %w", rel, err)
		}
		res.Files++
		res.Bytes += info.Size
	}
	return res, nil
}
