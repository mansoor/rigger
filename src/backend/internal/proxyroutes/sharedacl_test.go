package proxyroutes

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/db"
)

func TestRenderSharedACLs(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := NewStore(d)
	wsID, err := st.CreateAccessList(AccessList{
		Name: "office", Workspace: "acme", PassAuth: false,
		Users:   []BasicUser{{User: "bob", Hash: "$2y$zz"}},
		Rules:   []AccessRule{{Action: "allow", Address: "10.0.0.0/8"}, {Action: "deny", Address: "1.2.3.4"}},
		GeoMode: "block", Countries: []string{"RU"},
	})
	if err != nil {
		t.Fatalf("create ws list: %v", err)
	}
	// A global (Proxy Service) list — must NOT be rendered as a shared fragment.
	if _, err := st.CreateAccessList(AccessList{Name: "glob", Users: []BasicUser{{User: "a", Hash: "$x"}}}); err != nil {
		t.Fatalf("create global list: %v", err)
	}

	dir := t.TempDir()
	if err := RenderSharedACLs(d, dir, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	prefix := "acl-acme-" + strconv.FormatInt(wsID, 10)
	b, err := os.ReadFile(filepath.Join(dir, prefix+".yml"))
	if err != nil {
		t.Fatalf("expected ws acl fragment: %v", err)
	}
	y := string(b)
	for _, sub := range []string{prefix + "-auth:", "removeHeader: true", `"bob:$2y$zz"`, prefix + "-ipallow:", `"10.0.0.0/8"`, prefix + "-geo:", "blockedCountries:", `"RU"`} {
		if !strings.Contains(y, sub) {
			t.Errorf("ws acl fragment missing %q\n%s", sub, y)
		}
	}
	if strings.Contains(y, "1.2.3.4") {
		t.Errorf("deny address leaked into the allow-list:\n%s", y)
	}
	// No global fragment on disk (global lists are inlined per-route, not shared).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "global") {
			t.Errorf("global list must not render as a shared fragment: %s", e.Name())
		}
	}

	// Scope isolation: ByScope("acme") sees the ws list; ByScope("") sees only the global one.
	if ws, _ := st.ListAccessListsByScope("acme"); len(ws) != 1 || ws[0].Name != "office" {
		t.Errorf("ByScope(acme) = %+v", ws)
	}
	if gl, _ := st.ListAccessListsByScope(""); len(gl) != 1 || gl[0].Name != "glob" {
		t.Errorf("ByScope(global) = %+v", gl)
	}

	// Middleware names for composegen attach (geo on vs off).
	a, _, _ := st.GetAccessList(wsID)
	names := SharedACLMiddlewareNames(a, true)
	if len(names) != 3 || names[0] != prefix+"-auth@file" {
		t.Errorf("unexpected mw names: %v", names)
	}
	if n := SharedACLMiddlewareNames(a, false); len(n) != 2 {
		t.Errorf("geo-disabled should drop the geo middleware: %v", n)
	}
}
