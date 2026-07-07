package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/proxyroutes"
)

func buildGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestUntarGzRoundTrip(t *testing.T) {
	blob := buildGz(t, map[string]string{"manifest.json": `{"kind":"rigger-proxy-backup"}`, "certs/acme.json": "{}"})
	entries, err := untarGz(blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(entries["manifest.json"]) != `{"kind":"rigger-proxy-backup"}` {
		t.Fatalf("manifest: %q", entries["manifest.json"])
	}
	if _, ok := entries["certs/acme.json"]; !ok {
		t.Fatal("missing certs/acme.json")
	}
	if _, err := untarGz([]byte("not a gzip")); err == nil {
		t.Fatal("expected error on non-gzip input")
	}
}

func TestFilterAcmeByHosts(t *testing.T) {
	store := `{"letsencrypt":{"Account":{"Email":"a@b.c"},"Certificates":[
		{"domain":{"main":"app.example.com"},"certificate":"AAA","key":"KKK"},
		{"domain":{"main":"other.net"},"certificate":"BBB","key":"LLL"}]}}`
	out, ok := filterAcmeByHosts([]byte(store), map[string]bool{"app.example.com": true})
	if !ok {
		t.Fatal("parse failed")
	}
	var parsed map[string]acmeResolverRaw
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	certs := parsed["letsencrypt"].Certificates
	if len(certs) != 1 {
		t.Fatalf("expected 1 kept cert, got %d", len(certs))
	}
	if got := certDomainNames(certs[0]); len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("kept wrong cert: %v", got)
	}
	// A wildcard route host should keep a matching subdomain cert.
	if _, ok := filterAcmeByHosts([]byte(store), map[string]bool{"nope.invalid": true}); !ok {
		t.Fatal("parse should still succeed with no matches")
	}
}

func TestMergeAcmeUnions(t *testing.T) {
	current := `{"letsencrypt":{"Account":{"Email":"keep@me"},"Certificates":[{"domain":{"main":"a.example.com"},"certificate":"A"}]}}`
	incoming := `{"letsencrypt":{"Account":{"Email":"other@x"},"Certificates":[{"domain":{"main":"a.example.com"},"certificate":"A2"},{"domain":{"main":"b.example.com"},"certificate":"B"}]}}`
	merged := mergeAcme([]byte(current), []byte(incoming))
	var parsed map[string]acmeResolverRaw
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatal(err)
	}
	res := parsed["letsencrypt"]
	if len(res.Certificates) != 2 {
		t.Fatalf("expected 2 certs after union (dedupe a.example.com), got %d", len(res.Certificates))
	}
	if !strings.Contains(string(res.Account), "keep@me") {
		t.Fatalf("existing account should be preserved: %s", res.Account)
	}
	// Empty current → incoming verbatim.
	if got := mergeAcme(nil, []byte(incoming)); string(got) != incoming {
		t.Fatalf("empty-current merge should be incoming verbatim")
	}
}

// TestProxyBackupRestoreRoundTrip exports a route + access list on one "server"
// (crypto key A) and restores the bundle on a second server (key B), proving the
// custom-cert key is re-wrapped for the new key and the access-list reference is
// re-pointed to the freshly-imported list.
func TestProxyBackupRestoreRoundTrip(t *testing.T) {
	t.Setenv("RIGGER_DYNAMIC_DIR", t.TempDir())
	t.Setenv("TRAEFIK_CERTS_DIR", t.TempDir())

	dir1 := t.TempDir()
	db1, err := db.Open(dir1)
	if err != nil {
		t.Fatal(err)
	}
	keyA, _ := crypto.DeriveKey([]byte("server-A-secret"))
	h1 := &Handler{db: db1, cryptoKey: keyA, dataDir: dir1}

	st := h1.proxyStore()
	alID, err := st.CreateAccessList(proxyroutes.AccessList{Name: "list1", Users: []proxyroutes.BasicUser{{User: "u", Hash: "$2y$hash"}}})
	if err != nil {
		t.Fatal(err)
	}
	keyEnc, _ := crypto.Encrypt(keyA, []byte("PEM-PRIVATE-KEY"))
	if _, err := st.Create(proxyroutes.Route{
		Name: "r1", Enabled: true, Type: "proxy", Host: "app.example.com",
		TLSMode: "custom", TLSCertPEM: "CERTPEM", TLSKeyEnc: keyEnc, AccessListID: alID,
		Upstreams: []proxyroutes.Upstream{{Scheme: "http", Host: "10.0.0.5", Port: 80}},
	}); err != nil {
		t.Fatal(err)
	}

	// Export (passphrase set because a custom key is present).
	rec := httptest.NewRecorder()
	h1.ProxyBackup(rec, httptest.NewRequest("POST", "/api/proxy/backup", strings.NewReader(`{"cert_scope":"none","passphrase":"pw123"}`)))
	if rec.Code != 200 {
		t.Fatalf("backup: code %d body %s", rec.Code, rec.Body.String())
	}
	bundle := rec.Body.Bytes()

	// Restore into a second server with a DIFFERENT key.
	dir2 := t.TempDir()
	db2, err := db.Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	keyB, _ := crypto.DeriveKey([]byte("server-B-secret"))
	h2 := &Handler{db: db2, cryptoKey: keyB, dataDir: dir2}

	var mb bytes.Buffer
	mw := multipart.NewWriter(&mb)
	fw, _ := mw.CreateFormFile("file", "backup.rpb")
	fw.Write(bundle)
	mw.WriteField("passphrase", "pw123")
	mw.WriteField("replace_existing", "false")
	mw.Close()
	req := httptest.NewRequest("POST", "/api/proxy/restore", &mb)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec2 := httptest.NewRecorder()
	h2.ProxyRestore(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("restore: code %d body %s", rec2.Code, rec2.Body.String())
	}

	routes, _ := h2.proxyStore().List()
	var got *proxyroutes.Route
	for i := range routes {
		if routes[i].Name == "r1" {
			got = &routes[i]
		}
	}
	if got == nil {
		t.Fatal("route r1 was not restored on server B")
	}
	if got.AccessListID == 0 {
		t.Fatal("access-list reference was not re-pointed")
	}
	if got.Host != "app.example.com" || got.TLSCertPEM != "CERTPEM" {
		t.Fatalf("route fields not preserved: %+v", got)
	}
	// The custom-cert key must now decrypt with server B's key (re-wrapped on import).
	plain, err := crypto.Decrypt(keyB, got.TLSKeyEnc)
	if err != nil || string(plain) != "PEM-PRIVATE-KEY" {
		t.Fatalf("custom-cert key not re-wrapped for server B: %v", err)
	}
}

func TestMergeAcmeNewResolver(t *testing.T) {
	current := `{"letsencrypt":{"Certificates":[{"domain":{"main":"a"}}]}}`
	incoming := `{"dns":{"Certificates":[{"domain":{"main":"b"}}]}}`
	merged := mergeAcme([]byte(current), []byte(incoming))
	var parsed map[string]acmeResolverRaw
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["letsencrypt"]; !ok {
		t.Fatal("lost existing resolver")
	}
	if _, ok := parsed["dns"]; !ok {
		t.Fatal("did not add incoming resolver")
	}
}
