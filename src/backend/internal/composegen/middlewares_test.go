package composegen

import (
	"strings"
	"testing"
)

// TestRouterMiddlewaresAttached verifies env RouterMiddlewares (workspace access list + plugins)
// are prepended to app-router chains, and that an empty list changes nothing (golden parity).
func TestRouterMiddlewaresAttached(t *testing.T) {
	cfg := []byte(`{"project":{"name":"app"},"services":[{"name":"web","image":"nginx","web_routed":true,"port":"80"}],"environments":{"dev":{"domain":"","traefik_enabled":true}}}`)

	base, err := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "example.com"})
	if err != nil {
		t.Fatalf("generate base: %v", err)
	}
	if strings.Contains(string(base), "acl-") || strings.Contains(string(base), "proxy-waf@file") {
		t.Errorf("no extra middlewares expected when RouterMiddlewares empty:\n%s", base)
	}

	out, err := GenerateRouted(cfg, "dev", RouteOpts{BaseDomain: "example.com", RouterMiddlewares: []string{"acl-acme-5-auth@file", "proxy-waf@file"}})
	if err != nil {
		t.Fatalf("generate with mw: %v", err)
	}
	if !strings.Contains(string(out), "acl-acme-5-auth@file,proxy-waf@file,rigger-loading@file") {
		t.Errorf("expected env middlewares prepended to the app router chain:\n%s", out)
	}
}
