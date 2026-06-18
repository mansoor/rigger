package previews

import (
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestWebhookTokenHashingAndLookup(t *testing.T) {
	d := openTestDB(t)

	w, err := CreateWebhook(d, Webhook{Workspace: "mcl", Project: "web", Secret: "s3cr3t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Token == "" {
		t.Fatal("create must return the raw token once")
	}
	if w.Provider != "github" {
		t.Errorf("default provider = %q, want github", w.Provider)
	}

	// The raw token resolves the webhook; the hash (not the raw token) is stored.
	got, err := GetWebhookByToken(d, w.Token)
	if err != nil {
		t.Fatalf("lookup by token: %v", err)
	}
	if got.ID != w.ID || got.Secret != "s3cr3t" || got.Workspace != "mcl" {
		t.Errorf("looked-up webhook mismatch: %+v", got)
	}
	if got.Token != "" {
		t.Error("stored webhook must not expose the raw token on read")
	}

	// A wrong token doesn't resolve.
	if _, err := GetWebhookByToken(d, "deadbeef"); err == nil {
		t.Error("expected not-found for a bad token")
	}

	// List scopes to the project.
	list, err := ListWebhooks(d, "mcl", "web")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v (len %d), err %v", list, len(list), err)
	}

	// Delete removes it; disabled/deleted tokens no longer resolve.
	if err := DeleteWebhook(d, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetWebhookByToken(d, w.Token); err == nil {
		t.Error("expected not-found after delete")
	}
}

func TestPreviewEnvCRUD(t *testing.T) {
	d := openTestDB(t)

	p, err := Create(d, PreviewEnv{
		Workspace: "mcl", Project: "web", PRNumber: 42, Provider: "github",
		Branch: "feature/x", HeadSHA: "abc123", EnvKey: "pr42",
		URL: "https://mcl-web-pr42.example.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 || p.Status != StatusCreating || p.CreatedAt == 0 {
		t.Fatalf("unexpected created preview: %+v", p)
	}

	// GetByPR finds it; an unknown PR returns (nil, nil), not an error.
	got, err := GetByPR(d, "mcl", "web", 42)
	if err != nil || got == nil || got.EnvKey != "pr42" {
		t.Fatalf("GetByPR = %+v, err %v", got, err)
	}
	if miss, err := GetByPR(d, "mcl", "web", 99); err != nil || miss != nil {
		t.Errorf("GetByPR(missing) = %+v, err %v; want nil,nil", miss, err)
	}

	// Update mutable fields.
	p.Status = StatusRunning
	p.HeadSHA = "def456"
	p.LastDeployedAt = 1000
	p.ExpiresAt = 2000
	p.LastRunID = 7
	if err := Update(d, *p); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = Get(d, p.ID)
	if got.Status != StatusRunning || got.HeadSHA != "def456" || got.LastRunID != 7 ||
		got.ExpiresAt != 2000 || got.LastDeployedAt != 1000 {
		t.Errorf("update not persisted: %+v", got)
	}

	// CountActive counts non-torn-down previews.
	if n, _ := CountActive(d, "mcl", "web"); n != 1 {
		t.Errorf("CountActive = %d, want 1", n)
	}
}

func TestListExpired(t *testing.T) {
	d := openTestDB(t)

	// no TTL → never reaped
	if _, err := Create(d, PreviewEnv{Workspace: "mcl", Project: "web", PRNumber: 1, EnvKey: "pr1", Status: StatusRunning, ExpiresAt: 0}); err != nil {
		t.Fatal(err)
	}
	// expired
	if _, err := Create(d, PreviewEnv{Workspace: "mcl", Project: "web", PRNumber: 2, EnvKey: "pr2", Status: StatusRunning, ExpiresAt: 100}); err != nil {
		t.Fatal(err)
	}
	// not yet expired
	if _, err := Create(d, PreviewEnv{Workspace: "mcl", Project: "web", PRNumber: 3, EnvKey: "pr3", Status: StatusRunning, ExpiresAt: 5000}); err != nil {
		t.Fatal(err)
	}
	// already torn down → excluded even though expired
	if _, err := Create(d, PreviewEnv{Workspace: "mcl", Project: "web", PRNumber: 4, EnvKey: "pr4", Status: StatusTornDown, ExpiresAt: 100}); err != nil {
		t.Fatal(err)
	}

	exp, err := ListExpired(d, 1000)
	if err != nil {
		t.Fatalf("ListExpired: %v", err)
	}
	if len(exp) != 1 || exp[0].PRNumber != 2 {
		t.Fatalf("ListExpired(now=1000) = %+v, want only PR 2", exp)
	}
}
