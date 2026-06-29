package gitproviders

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/gitsync"
)

// gitConfigContents reads the temp gitconfig an http-token auth points GIT_CONFIG_GLOBAL at.
func gitConfigContents(t *testing.T, auth *gitsync.Auth) string {
	t.Helper()
	for _, e := range auth.Env {
		if path, ok := strings.CutPrefix(e, "GIT_CONFIG_GLOBAL="); ok {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read gitconfig: %v", err)
			}
			return string(b)
		}
	}
	t.Fatalf("no GIT_CONFIG_GLOBAL in auth env: %v", auth.Env)
	return ""
}

var testKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes for AES-256

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// A token provider round-trips through the store with its secret encrypted at rest
// (the raw DB column must not contain the plaintext), and BuildAuth keeps the token
// out of argv — it travels via a temp gitconfig referenced by GIT_CONFIG_GLOBAL.
func TestTokenProviderRoundTripAndAuth(t *testing.T) {
	d := openDB(t)
	created, err := Create(d, testKey, Provider{
		Name: "gh", Kind: KindToken, Host: "github.com", Username: "x-access-token",
		Secret: "ghp_supersecret", OwnerScope: WorkspaceScope("mcl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Raw column must be ciphertext, not the plaintext token.
	var enc string
	if err := d.QueryRow(`SELECT secret_enc FROM git_providers WHERE id=?`, created.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || strings.Contains(enc, "ghp_supersecret") {
		t.Fatalf("secret not encrypted at rest: %q", enc)
	}
	// List omits the secret; Get decrypts it.
	list, _ := ListForWorkspace(d, "mcl")
	if len(list) != 1 || list[0].Secret != "" || !list[0].HasSecret {
		t.Fatalf("list should expose has_secret but not the secret: %+v", list)
	}
	got, _ := Get(d, testKey, created.ID)
	if got == nil || got.Secret != "ghp_supersecret" {
		t.Fatalf("Get should decrypt the token, got %+v", got)
	}
	auth, err := got.BuildAuth("https://github.com/acme/widgets.git")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cleanup()
	joined := strings.Join(auth.Env, " ")
	if !strings.Contains(joined, "GIT_CONFIG_GLOBAL=") || !strings.Contains(joined, "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatalf("token auth must inject via GIT_CONFIG_GLOBAL, got %v", auth.Env)
	}
	for _, e := range auth.Env {
		if strings.Contains(e, "ghp_supersecret") {
			t.Fatalf("token must not appear in the git env directly: %q", e)
		}
	}
}

// The credential header binds to the EXACT repo origin so it attaches to a self-hosted
// gitea served over plain http on a non-default port (the bug that made Test fail) —
// and falls back to https://<host>/ when no repo is supplied.
func TestTokenAuthScopesToRepoOrigin(t *testing.T) {
	p := &Provider{Name: "gitea", Kind: KindToken, Host: "git.example.com", Username: "me", Secret: "tok"}

	auth, err := p.BuildAuth("http://git.example.com:3000/me/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cleanup()
	if cfg := gitConfigContents(t, auth); !strings.Contains(cfg, `[http "http://git.example.com:3000/"]`) {
		t.Fatalf("header must scope to the http :3000 origin, got:\n%s", cfg)
	}

	auth2, err := p.BuildAuth("") // no repo ⇒ fall back to the provider host over https
	if err != nil {
		t.Fatal(err)
	}
	defer auth2.Cleanup()
	if cfg := gitConfigContents(t, auth2); !strings.Contains(cfg, `[http "https://git.example.com/"]`) {
		t.Fatalf("no-repo fallback must scope to https host, got:\n%s", cfg)
	}
}

// A generated SSH provider yields a usable keypair; BuildAuth points GIT_SSH_COMMAND
// at a private-key file (not the key inline).
func TestSSHKeyGenerateAndAuth(t *testing.T) {
	priv, pub, err := GenerateSSHKey("rigger-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(priv, "OPENSSH PRIVATE KEY") || !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("bad keypair: priv=%.30q pub=%.30q", priv, pub)
	}
	p := &Provider{Name: "deploy", Kind: KindSSHKey, Secret: priv, PublicKey: pub}
	auth, err := p.BuildAuth("")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cleanup()
	joined := strings.Join(auth.Env, " ")
	if !strings.Contains(joined, "GIT_SSH_COMMAND=ssh -i ") || !strings.Contains(joined, "IdentitiesOnly=yes") {
		t.Fatalf("ssh auth must set GIT_SSH_COMMAND with -i, got %v", auth.Env)
	}
}

// appJWT signs a valid RS256 token with the app id as issuer (the GitHub App-auth
// contract). Verifiable offline against the public key.
func TestAppJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	tokStr, err := appJWT("123456", privPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tokStr, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("token not valid: %v", err)
	}
	iss, _ := parsed.Claims.GetIssuer()
	if iss != "123456" {
		t.Errorf("issuer = %q, want 123456", iss)
	}
}

// An empty Secret can't build auth (clear error, no panic).
func TestBuildAuthMissingSecret(t *testing.T) {
	if _, err := (&Provider{Kind: KindToken}).BuildAuth(""); err == nil {
		t.Fatal("expected error for token provider with no secret")
	}
	if _, err := (&Provider{Kind: KindGitHubApp}).BuildAuth(""); err == nil {
		t.Fatal("github_app should report not-yet-supported")
	}
}
