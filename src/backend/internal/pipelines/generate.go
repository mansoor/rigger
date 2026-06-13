package pipelines

import "fmt"

// GenerateOptions parameterises a default-pipeline draft built from a project's
// ordered environments. Envs must be in effective deploy order (low→high).
type GenerateOptions struct {
	Template   string   // "release" (default) | "hotfix"
	Envs       []string // effective-ordered environments (low→high)
	BumpPart   string   // "" → no version stage; else major|minor|patch|build
	Gate       bool     // insert a manual gate before the final promotion
	HotfixFrom string   // hotfix: env to build (default Envs[0])
	HotfixTo   string   // hotfix: promote destination (default last env)
}

// Generate returns a draft pipeline (name + stages) seeded from the environment
// topology. It persists nothing — the UI saves the draft via the normal create
// flow, so every stage stays editable. The build stage pushes to the registry
// whenever a later promote needs it (promote pulls the source image from the
// registry), so the chain is correct out of the box.
func Generate(o GenerateOptions) (string, []Stage) {
	if o.Template == "hotfix" {
		return generateHotfix(o)
	}
	return generateRelease(o)
}

// generateRelease: [version] → build@e0(+push when promoting) → deploy@e0 →
// promote e0→e1 → … → [gate] → promote e(n-1)→e(n).
func generateRelease(o GenerateOptions) (string, []Stage) {
	envs := o.Envs
	if len(envs) == 0 {
		return "Release", nil
	}
	multi := len(envs) > 1

	var stages []Stage
	if o.BumpPart != "" {
		stages = append(stages, Stage{Type: "version", Part: o.BumpPart, OnFailure: "stop"})
	}
	stages = append(stages,
		Stage{Type: "build", Env: envs[0], Push: multi, OnFailure: "stop"},
		Stage{Type: "deploy", Env: envs[0], OnFailure: "stop"},
	)
	for i := 0; i+1 < len(envs); i++ {
		if o.Gate && i == len(envs)-2 { // gate before the final (highest) promotion
			stages = append(stages, Stage{Type: "gate", Command: "Approve promotion to " + envs[i+1], OnFailure: "stop"})
		}
		stages = append(stages, Stage{Type: "push", Env: envs[i], ToEnv: envs[i+1], OnFailure: "stop"})
	}

	if multi {
		return fmt.Sprintf("Release %s→%s", envs[0], envs[len(envs)-1]), stages
	}
	return "Release " + envs[0], stages
}

// generateHotfix bypasses the chain: [version] → build@from(+push) → [gate] →
// promote from→to. Builds the artifact at the lowest env and ships it straight
// to stage/prod without deploying the intermediate tiers.
func generateHotfix(o GenerateOptions) (string, []Stage) {
	envs := o.Envs
	from := o.HotfixFrom
	if from == "" && len(envs) > 0 {
		from = envs[0]
	}
	to := o.HotfixTo
	if to == "" && len(envs) > 0 {
		to = envs[len(envs)-1]
	}
	bump := o.BumpPart
	if bump == "" {
		bump = "patch"
	}

	promote := from != to && to != ""
	stages := []Stage{
		{Type: "version", Part: bump, OnFailure: "stop"},
		{Type: "build", Env: from, Push: promote, OnFailure: "stop"},
	}
	if promote {
		if o.Gate {
			stages = append(stages, Stage{Type: "gate", Command: "Approve hotfix to " + to, OnFailure: "stop"})
		}
		stages = append(stages, Stage{Type: "push", Env: from, ToEnv: to, OnFailure: "stop"})
	} else {
		stages = append(stages, Stage{Type: "deploy", Env: from, OnFailure: "stop"})
	}

	dest := to
	if dest == "" {
		dest = from
	}
	return "Hotfix → " + dest, stages
}
