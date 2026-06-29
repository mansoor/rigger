package proxyroutes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderEnvMiddlewares(t *testing.T) {
	const ws, proj, env = "acme", "shop", "prod"
	prefix := "appmw-acme-shop-prod"

	// Inline IP allow-list + GeoIP block + block-exploits, no access list attached.
	cfg := []byte(`{"environments":{"prod":{
		"ip_mode":"allow","ip_cidrs":"10.0.0.0/8, 192.168.1.0/24",
		"geo_mode":"block","geo_countries":"ru, cn","block_exploits":true}}}`)

	dir := t.TempDir()
	names, err := RenderEnvMiddlewares(dir, ws, proj, env, cfg, true)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []string{prefix + "-ipallow@file", prefix + "-geo@file", prefix + "-sec@file"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", names, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, prefix+".yml"))
	if err != nil {
		t.Fatalf("expected fragment: %v", err)
	}
	y := string(b)
	for _, sub := range []string{
		prefix + "-ipallow:", `"10.0.0.0/8"`, `"192.168.1.0/24"`,
		prefix + "-geo:", "blockedCountries:", `"RU"`, `"CN"`, // codes upper-cased
		prefix + "-sec:", "frameDeny: true", "contentTypeNosniff: true",
	} {
		if !strings.Contains(y, sub) {
			t.Errorf("fragment missing %q\n%s", sub, y)
		}
	}

	// GeoIP plugin disabled instance-wide → geo middleware is dropped.
	if n, _ := RenderEnvMiddlewares(dir, ws, proj, env, cfg, false); len(n) != 2 || n[1] != prefix+"-sec@file" {
		t.Errorf("geo-disabled names = %v (want ipallow + sec)", n)
	}

	// Access list attached → inline IP/Geo are skipped; only block-exploits applies.
	withList := []byte(`{"environments":{"prod":{"access_list_id":7,
		"ip_mode":"allow","ip_cidrs":"10.0.0.0/8","geo_mode":"block","geo_countries":"ru","block_exploits":true}}}`)
	if n, _ := RenderEnvMiddlewares(dir, ws, proj, env, withList, true); len(n) != 1 || n[0] != prefix+"-sec@file" {
		t.Errorf("with access list, names = %v (want only sec)", n)
	}

	// Nothing enforceable → returns nil and prunes any stale fragment.
	off := []byte(`{"environments":{"prod":{}}}`)
	if n, _ := RenderEnvMiddlewares(dir, ws, proj, env, off, true); n != nil {
		t.Errorf("expected nil names when nothing set, got %v", n)
	}
	if _, err := os.Stat(filepath.Join(dir, prefix+".yml")); !os.IsNotExist(err) {
		t.Errorf("stale fragment should have been pruned (err=%v)", err)
	}
}
