package backupsync

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/mansoor/rigger/ui/internal/settings"
)

type sftpSyncer struct {
	cfg settings.SFTPConfig
}

func newSFTP(cfg settings.SFTPConfig) (*sftpSyncer, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("sftp: host is required")
	}
	if cfg.Username == "" {
		return nil, fmt.Errorf("sftp: username is required")
	}
	return &sftpSyncer{cfg: cfg}, nil
}

// dial opens an SSH+SFTP session. The caller must close both returned clients.
// Host-key verification is intentionally relaxed (InsecureIgnoreHostKey): the
// destination is a user-configured backup target on their own infrastructure.
func (s *sftpSyncer) dial() (*ssh.Client, *sftp.Client, error) {
	var auth []ssh.AuthMethod
	switch s.cfg.AuthType {
	case "key":
		signer, err := ssh.ParsePrivateKey([]byte(s.cfg.PrivateKey))
		if err != nil {
			return nil, nil, fmt.Errorf("parse private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	default: // "password" (or unset)
		auth = append(auth, ssh.Password(s.cfg.Password))
	}

	port := s.cfg.Port
	if port == 0 {
		port = 22
	}
	conf := &ssh.ClientConfig{
		User:            s.cfg.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         15 * time.Second,
	}

	conn, err := ssh.Dial("tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(port)), conf)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	sc, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sftp session: %w", err)
	}
	return conn, sc, nil
}

func (s *sftpSyncer) Test(ctx context.Context) error {
	conn, sc, err := s.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer sc.Close()

	rp := s.cfg.RemotePath
	if rp == "" {
		rp = "."
	}
	if err := sc.MkdirAll(rp); err != nil {
		return fmt.Errorf("remote path %q not writable: %w", rp, err)
	}
	return nil
}

func (s *sftpSyncer) UploadDir(ctx context.Context, localDir, keyPrefix string) (Result, error) {
	conn, sc, err := s.dial()
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	defer sc.Close()

	files, err := walkFiles(localDir)
	if err != nil {
		return Result{}, err
	}
	base := path.Join(s.cfg.RemotePath, keyPrefix)

	var res Result
	for _, rel := range files {
		remote := path.Join(base, filepath.ToSlash(rel))
		if err := sc.MkdirAll(path.Dir(remote)); err != nil {
			return res, fmt.Errorf("mkdir %s: %w", path.Dir(remote), err)
		}
		n, err := uploadOne(sc, filepath.Join(localDir, rel), remote)
		if err != nil {
			return res, fmt.Errorf("upload %s: %w", rel, err)
		}
		res.Files++
		res.Bytes += n
	}
	return res, nil
}

func uploadOne(sc *sftp.Client, local, remote string) (int64, error) {
	src, err := os.Open(local)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := sc.Create(remote)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	return io.Copy(dst, src)
}
