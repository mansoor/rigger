package workspace

import "testing"

// A "database" stack seeds NO application services — just the managed DB, driven
// by the env's database + db_version (composegen emits it).
func TestBuildConfigDatabaseStack(t *testing.T) {
	cfg, err := buildConfig(CreateRequest{
		Workspace: "ws", Name: "Cache DB", Key: "cachedb", Type: "database",
		Database: "mariadb", DBVersion: "10.11",
		Envs: []EnvRequest{{Name: "dev"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if svcs, _ := cfg["services"].([]map[string]any); len(svcs) != 0 {
		t.Fatalf("database stack should seed no app services, got %d", len(svcs))
	}
	envs, _ := cfg["environments"].(map[string]any)
	dev, _ := envs["dev"].(map[string]any)
	if dev == nil {
		t.Fatal("missing dev env")
	}
	if dev["database"] != "mariadb" {
		t.Fatalf("env database = %v, want mariadb", dev["database"])
	}
	if dev["db_version"] != "10.11" {
		t.Fatalf("env db_version = %v, want 10.11", dev["db_version"])
	}
	if cfg["project"].(map[string]any)["type"] != "database" {
		t.Fatalf("project type = %v, want database", cfg["project"].(map[string]any)["type"])
	}
}
