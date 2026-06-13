package pipelines

import "testing"

// types returns a compact "type[:env→to]" trace for asserting stage order.
func trace(stages []Stage) []string {
	out := make([]string, len(stages))
	for i, s := range stages {
		switch s.Type {
		case "push":
			out[i] = "push:" + s.Env + "→" + s.ToEnv
		case "version":
			out[i] = "version:" + s.Part
		case "build":
			out[i] = "build:" + s.Env
			if s.Push {
				out[i] += "+push"
			}
			if s.Part != "" {
				out[i] += "#" + s.Part
			}
		default:
			out[i] = s.Type + ":" + s.Env
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGenerateReleaseChain(t *testing.T) {
	name, stages := Generate(GenerateOptions{
		Template: "release", Envs: []string{"dev", "staging", "prod"}, BumpPart: "minor", Gate: true,
	})
	want := []string{
		"build:dev+push#minor", "deploy:dev",
		"push:dev→staging", "gate:", "push:staging→prod",
	}
	if !eq(trace(stages), want) {
		t.Errorf("release chain = %v, want %v", trace(stages), want)
	}
	if name != "Release dev→prod" {
		t.Errorf("name = %q", name)
	}
}

func TestGenerateReleaseSingleEnvNoPush(t *testing.T) {
	// One env ⇒ no promote, so the build must NOT push (nothing pulls it).
	_, stages := Generate(GenerateOptions{Template: "release", Envs: []string{"dev"}})
	want := []string{"build:dev", "deploy:dev"}
	if !eq(trace(stages), want) {
		t.Errorf("single-env = %v, want %v", trace(stages), want)
	}
}

func TestGenerateReleaseNoBumpNoGate(t *testing.T) {
	_, stages := Generate(GenerateOptions{Template: "release", Envs: []string{"dev", "prod"}})
	want := []string{"build:dev+push", "deploy:dev", "push:dev→prod"}
	if !eq(trace(stages), want) {
		t.Errorf("no-bump = %v, want %v", trace(stages), want)
	}
}

func TestGenerateHotfixBypass(t *testing.T) {
	name, stages := Generate(GenerateOptions{
		Template: "hotfix", Envs: []string{"dev", "staging", "prod"}, Gate: true,
	})
	// Skips staging entirely; default bump=patch; default from=dev, to=prod.
	want := []string{"build:dev+push#patch", "gate:", "push:dev→prod"}
	if !eq(trace(stages), want) {
		t.Errorf("hotfix = %v, want %v", trace(stages), want)
	}
	if name != "Hotfix → prod" {
		t.Errorf("name = %q", name)
	}
}

func TestGenerateHotfixExplicitTarget(t *testing.T) {
	_, stages := Generate(GenerateOptions{
		Template: "hotfix", Envs: []string{"dev", "staging", "prod"},
		HotfixFrom: "dev", HotfixTo: "staging", BumpPart: "build",
	})
	want := []string{"build:dev+push#build", "push:dev→staging"}
	if !eq(trace(stages), want) {
		t.Errorf("hotfix-explicit = %v, want %v", trace(stages), want)
	}
}
