// Package gitproviders stores a workspace's Git provider connections (the
// credentials Rigger uses to clone PRIVATE repositories) and turns one into a
// gitsync.Auth applied to a clone. It mirrors the Docker-registries store
// (owner_scope + a global grants pool) but encrypts the secret material at rest
// with internal/crypto — the secret never lives plaintext in the DB, config.json,
// or logs.
//
// Phase 1 kinds: "token" (HTTPS PAT) and "ssh_key" (deploy key). "github_app"
// (manifest flow + short-lived installation tokens) is reserved for phase 2.
package gitproviders

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/gitsync"
	"golang.org/x/crypto/ssh"
)

// Kind is the credential mechanism.
type Kind string

const (
	KindToken     Kind = "token"      // HTTPS personal-access-token / deploy token
	KindSSHKey    Kind = "ssh_key"    // SSH deploy key (Rigger-generated or pasted)
	KindGitHubApp Kind = "github_app" // phase 2 — GitHub App installation tokens
)

// Provider is one stored connection. Secret is the decrypted credential material
// (PAT, or SSH private-key PEM) and is populated only by Get — List* leave it empty.
type Provider struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Kind       Kind      `json:"kind"`
	Host       string    `json:"host,omitempty"`
	Username   string    `json:"username,omitempty"`
	PublicKey  string    `json:"public_key,omitempty"` // ssh deploy public key (non-secret)
	Meta       string    `json:"meta,omitempty"`       // kind-specific non-secret json
	OwnerScope string    `json:"owner_scope"`
	HasSecret  bool      `json:"has_secret"`
	Secret     string    `json:"-"` // decrypted; never serialized
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WorkspaceScope returns the owner_scope string for a workspace-owned provider.
func WorkspaceScope(wsKey string) string { return "ws:" + wsKey }

// ── store ────────────────────────────────────────────────────────────────────

func scan(rows interface{ Scan(...any) error }) (Provider, error) {
	var p Provider
	var host, user, pub, meta string
	err := rows.Scan(&p.ID, &p.Name, &p.Kind, &host, &user, &p.HasSecret, &pub, &meta, &p.OwnerScope, &p.CreatedAt, &p.UpdatedAt)
	p.Host, p.Username, p.PublicKey, p.Meta = host, user, pub, meta
	return p, err
}

const listCols = `id, name, kind, host, username, (secret_enc <> '') AS has_secret, public_key, meta, owner_scope, created_at, updated_at`

// List returns every provider (no secrets). Admin-scoped callers.
func List(d *db.DB) ([]Provider, error) {
	rows, err := d.Query(`SELECT ` + listCols + ` FROM git_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// ListForWorkspace returns a workspace's own providers plus any global provider
// granted to it (or to '*'). Mirrors settings.ListRegistriesForWorkspace.
func ListForWorkspace(d *db.DB, wsKey string) ([]Provider, error) {
	rows, err := d.Query(`SELECT `+listCols+` FROM git_providers p
		WHERE p.owner_scope = ?
		   OR (p.owner_scope = 'global' AND EXISTS(
		         SELECT 1 FROM global_git_provider_grants g
		         WHERE g.provider_id = p.id AND g.workspace IN (?, '*')))
		ORDER BY name`, WorkspaceScope(wsKey), wsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

func collect(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]Provider, error) {
	out := []Provider{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one provider WITH its decrypted secret (for BuildAuth/Verify), or
// nil when not found. key is the crypto key (settings JWT-derived).
func Get(d *db.DB, key []byte, id int64) (*Provider, error) {
	var p Provider
	var host, user, pub, meta, enc string
	err := d.QueryRow(`SELECT id, name, kind, host, username, public_key, meta, secret_enc, owner_scope, created_at, updated_at
		FROM git_providers WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Kind, &host, &user, &pub, &meta, &enc, &p.OwnerScope, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Host, p.Username, p.PublicKey, p.Meta = host, user, pub, meta
	p.HasSecret = enc != ""
	if enc != "" {
		dec, derr := crypto.Decrypt(key, enc)
		if derr != nil {
			return nil, fmt.Errorf("decrypt provider secret: %w", derr)
		}
		p.Secret = string(dec)
	}
	return &p, nil
}

// Create stores a provider, encrypting the secret. ownerScope "" → global.
func Create(d *db.DB, key []byte, p Provider) (*Provider, error) {
	if p.OwnerScope == "" {
		p.OwnerScope = "global"
	}
	enc := ""
	if p.Secret != "" {
		var err error
		if enc, err = crypto.Encrypt(key, []byte(p.Secret)); err != nil {
			return nil, err
		}
	}
	res, err := d.Exec(`INSERT INTO git_providers (name, kind, host, username, secret_enc, public_key, meta, owner_scope)
		VALUES (?,?,?,?,?,?,?,?)`,
		p.Name, string(p.Kind), p.Host, p.Username, enc, p.PublicKey, p.Meta, p.OwnerScope)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return Get(d, key, id)
}

// Update edits mutable fields. An empty secret keeps the stored one; an empty meta
// keeps the stored meta (so an unrelated edit never clobbers a github_app's meta).
func Update(d *db.DB, key []byte, id int64, name, host, username, secret, meta string) (*Provider, error) {
	sets := []string{"name=?", "host=?", "username=?"}
	args := []any{name, host, username}
	if meta != "" {
		sets = append(sets, "meta=?")
		args = append(args, meta)
	}
	if secret != "" {
		enc, err := crypto.Encrypt(key, []byte(secret))
		if err != nil {
			return nil, err
		}
		sets = append(sets, "secret_enc=?")
		args = append(args, enc)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, id)
	if _, err := d.Exec(`UPDATE git_providers SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...); err != nil {
		return nil, err
	}
	return Get(d, key, id)
}

// Delete removes a provider (grants cascade).
func Delete(d *db.DB, id int64) error {
	_, err := d.Exec(`DELETE FROM git_providers WHERE id=?`, id)
	return err
}

// UpdateMeta replaces the non-secret meta json (e.g. recording a GitHub App's
// installation_id after the user installs it).
func UpdateMeta(d *db.DB, id int64, meta string) error {
	_, err := d.Exec(`UPDATE git_providers SET meta=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, meta, id)
	return err
}

// Grants returns the workspaces a global provider is shared with.
func Grants(d *db.DB, id int64) ([]string, error) {
	rows, err := d.Query(`SELECT workspace FROM global_git_provider_grants WHERE provider_id=? ORDER BY workspace`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetGrants replaces the share list for a global provider.
func SetGrants(d *db.DB, id int64, workspaces []string) error {
	if _, err := d.Exec(`DELETE FROM global_git_provider_grants WHERE provider_id=?`, id); err != nil {
		return err
	}
	for _, w := range workspaces {
		if strings.TrimSpace(w) == "" {
			continue
		}
		if _, err := d.Exec(`INSERT OR IGNORE INTO global_git_provider_grants (provider_id, workspace) VALUES (?,?)`, id, w); err != nil {
			return err
		}
	}
	return nil
}

// ── SSH deploy key generation ────────────────────────────────────────────────

// GenerateSSHKey returns a fresh ed25519 deploy keypair: the private key as an
// OpenSSH PEM (store encrypted), and the public key in authorized_keys form (show
// the user to paste into the provider as a read-only deploy key).
func GenerateSSHKey(comment string) (privPEM, pubAuthorized string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(blk)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// ── per-clone auth ───────────────────────────────────────────────────────────

// authScope returns the git `http.<url>` scope the credential header is bound to. It
// prefers the EXACT origin of the target repo (scheme://host[:port]/) so the header
// attaches to the real request — including self-hosted gitea/GitLab served over plain
// http or on a non-default port — and never leaks to a different origin. Git only
// applies an http.<url> extraheader when scheme, host AND port match, so a hardcoded
// "https://host/" scope silently drops the header for an http:// (or :3000) repo.
// Falls back to https://<host>/ when the repo isn't known yet, or to an unscoped
// (global) header when neither repo nor host is set.
func authScope(repo, host string) string {
	if r := strings.TrimSpace(repo); r != "" {
		if u, err := url.Parse(r); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
			return u.Scheme + "://" + u.Host + "/"
		}
	}
	if host != "" {
		return "https://" + host + "/"
	}
	return ""
}

// httpsTokenAuth builds a gitsync.Auth that injects an HTTPS Basic credential via a
// temp gitconfig http.extraheader (referenced by GIT_CONFIG_GLOBAL) — keeping the
// token out of argv, the URL, and logs. scope is the git http.<url> the header binds
// to (see authScope); "" emits a global, unscoped header. Shared by token + github_app.
func httpsTokenAuth(user, token, scope string) (*gitsync.Auth, error) {
	dir, err := os.MkdirTemp("", "rigger-gitcfg-")
	if err != nil {
		return nil, err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	section := "[http]"
	if scope != "" {
		section = fmt.Sprintf("[http %q]", scope)
	}
	cfg := section + "\n\textraheader = Authorization: Basic " + basic + "\n"
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &gitsync.Auth{
		Env:     []string{"GIT_CONFIG_GLOBAL=" + cfgPath, "GIT_CONFIG_NOSYSTEM=1"},
		Cleanup: func() { _ = os.RemoveAll(dir) },
	}, nil
}

// BuildAuth materializes the provider's credential into a gitsync.Auth, writing any
// secret to a 0600 temp file referenced only via the git child's environment (never
// argv/URL/logs). The caller MUST invoke the returned Cleanup after the git ops. repo
// is the target repository URL (may be "") — token/github_app providers scope their
// auth header to its exact origin so it attaches to http/non-standard-port hosts.
func (p *Provider) BuildAuth(repo string) (*gitsync.Auth, error) {
	switch p.Kind {
	case KindToken:
		if p.Secret == "" {
			return nil, fmt.Errorf("git provider %q has no token", p.Name)
		}
		user := p.Username
		if user == "" {
			user = "x-access-token" // works for GitHub; GitLab/Bitbucket accept any user with a PAT
		}
		return httpsTokenAuth(user, p.Secret, authScope(repo, p.Host))
	case KindGitHubApp:
		// Mint a fresh 1-hour installation token and use it as the HTTPS clone
		// credential (x-access-token). Never persisted.
		m := ParseGitHubMeta(p.Meta)
		token, err := mintInstallationToken(p.Host, m.AppID, p.Secret, m.InstallationID)
		if err != nil {
			return nil, err
		}
		return httpsTokenAuth("x-access-token", token, authScope(repo, p.Host))
	case KindSSHKey:
		if p.Secret == "" {
			return nil, fmt.Errorf("git provider %q has no SSH key", p.Name)
		}
		dir, err := os.MkdirTemp("", "rigger-gitssh-")
		if err != nil {
			return nil, err
		}
		keyPath := filepath.Join(dir, "id")
		pem := p.Secret
		if !strings.HasSuffix(pem, "\n") {
			pem += "\n"
		}
		if err := os.WriteFile(keyPath, []byte(pem), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		// IdentitiesOnly so only this key is offered; accept-new + /dev/null known-hosts
		// is TOFU (host-key pinning is a later pass).
		sshCmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null", keyPath)
		return &gitsync.Auth{
			Env:     []string{"GIT_SSH_COMMAND=" + sshCmd},
			Cleanup: func() { _ = os.RemoveAll(dir) },
		}, nil
	default:
		return nil, fmt.Errorf("unknown git provider kind %q", p.Kind)
	}
}

// Verify checks the credential against a repo with `git ls-remote` (read access).
// repo must be an HTTPS URL for token providers / an SSH URL for ssh_key providers.
func (p *Provider) Verify(repo string) error {
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("a repository URL is required to test access")
	}
	auth, err := p.BuildAuth(repo)
	if err != nil {
		return err
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", repo)
	cmd.Env = append(os.Environ(), auth.Env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("access test failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
