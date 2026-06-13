package workspace

import "testing"

// A "database" stack seeds NO application services — just the managed DB, driven
// by the PROJECT-level database + db_version (composegen emits it). Managed deps are
// project-level now (consistent across envs); the env block carries only db_external.
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
	project := cfg["project"].(map[string]any)
	if project["type"] != "database" {
		t.Fatalf("project type = %v, want database", project["type"])
	}
	if project["database"] != "mariadb" {
		t.Fatalf("project database = %v, want mariadb", project["database"])
	}
	if project["db_version"] != "10.11" {
		t.Fatalf("project db_version = %v, want 10.11", project["db_version"])
	}
	// Engine/version must NOT be written per-env anymore.
	envs, _ := cfg["environments"].(map[string]any)
	dev, _ := envs["dev"].(map[string]any)
	if dev == nil {
		t.Fatal("missing dev env")
	}
	if _, ok := dev["database"]; ok {
		t.Fatalf("env database should not be set (deps are project-level), got %v", dev["database"])
	}
}

// A custom stack writes its managed dependencies at PROJECT level (engine/version/
// redis/garage), NOT per-env. Only per-env DBExternal lands on the env block.
func TestBuildConfigCustomStackProjectLevelDeps(t *testing.T) {
	cfg, err := buildConfig(CreateRequest{
		Workspace: "ws", Name: "App", Key: "app", Type: "custom",
		Database: "postgres", DBVersion: "16-alpine", Redis: true, DBExternal: true,
		Envs: []EnvRequest{{Name: "dev"}, {Name: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	project := cfg["project"].(map[string]any)
	if project["database"] != "postgres" || project["db_version"] != "16-alpine" || project["redis_enabled"] != true {
		t.Fatalf("project deps wrong: %+v", project)
	}
	if _, ok := project["garage_enabled"]; ok {
		t.Fatalf("garage should be omitted when not requested: %+v", project)
	}
	envs := cfg["environments"].(map[string]any)
	for _, name := range []string{"dev", "prod"} {
		ev := envs[name].(map[string]any)
		if _, ok := ev["database"]; ok {
			t.Fatalf("%s: engine must NOT be per-env, got %v", name, ev["database"])
		}
		if _, ok := ev["redis_enabled"]; ok {
			t.Fatalf("%s: redis must NOT be per-env, got %v", name, ev["redis_enabled"])
		}
		// DBExternal is per-env (requested here, so present on both).
		if ev["db_external"] != true {
			t.Fatalf("%s: db_external should be set per-env, got %v", name, ev["db_external"])
		}
	}
}

// A managed dependency surfaces as a synthetic (managed) service row so the DB
// appears in the Services list — derived from the project-level engine, fallback to
// legacy per-env, and flagged managed so the UI hides delete.
func TestManagedDepServicesDerivesRows(t *testing.T) {
	// Project-level engine + redis.
	c := &Config{}
	c.Project.Database = "postgres"
	c.Project.Redis = true
	rows := managedDepServices(c)
	got := map[string]bool{}
	for _, s := range rows {
		if !s.Managed {
			t.Fatalf("row %q should be flagged managed", s.Name)
		}
		got[s.Name] = true
	}
	if !got["postgres"] || !got["redis"] {
		t.Fatalf("want postgres+redis derived rows, got %v", got)
	}

	// Legacy per-env engine (project empty) still derives a row.
	legacy := &Config{Environments: map[string]EnvConfig{"dev": {Database: "mysql"}}}
	if rows := managedDepServices(legacy); len(rows) != 1 || rows[0].Name != "mysql" {
		t.Fatalf("legacy per-env engine should derive a mysql row, got %v", rows)
	}

	// A real service of the same name suppresses the synthetic row.
	withReal := &Config{Services: []ConfigService{{Name: "postgres"}}}
	withReal.Project.Database = "postgres"
	if rows := managedDepServices(withReal); len(rows) != 0 {
		t.Fatalf("real postgres service should suppress the synthetic row, got %v", rows)
	}
}
