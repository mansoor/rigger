package databases

import "testing"

func TestCatalogDefaultsMatchLegacy(t *testing.T) {
	// These defaults MUST match the previously-hardcoded versions so existing
	// generated compose stays byte-identical (golden tests depend on it).
	if got := DefaultVersion("postgres"); got != "15-alpine" {
		t.Errorf("postgres default = %q, want 15-alpine", got)
	}
	if got := DefaultVersion("mysql"); got != "8.0" {
		t.Errorf("mysql default = %q, want 8.0", got)
	}
}

func TestGetAndKnown(t *testing.T) {
	if !IsKnown("mariadb") {
		t.Fatal("mariadb should be known")
	}
	if IsKnown("oracle") {
		t.Fatal("oracle should be unknown")
	}
	e, ok := Get("mariadb")
	if !ok || e.Image != "mariadb" || e.Driver != "mysql" || e.EnvPrefix != "MYSQL" || e.Port != 3306 {
		t.Fatalf("mariadb engine wrong: %+v", e)
	}
}

func TestResolveVersion(t *testing.T) {
	if got := ResolveVersion("postgres", "16-alpine"); got != "16-alpine" {
		t.Errorf("known version not preserved: %q", got)
	}
	if got := ResolveVersion("postgres", ""); got != "15-alpine" {
		t.Errorf("blank → default: %q", got)
	}
	if got := ResolveVersion("postgres", "bogus"); got != "15-alpine" {
		t.Errorf("unknown → default: %q", got)
	}
	if got := ResolveVersion("oracle", "19c"); got != "19c" {
		t.Errorf("unknown engine passes through: %q", got)
	}
}
