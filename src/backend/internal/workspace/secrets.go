package workspace

import "github.com/mansoor/rigger/ui/internal/envgen"

// GenerateSmartDefaults takes a template's default_env_vars map and replaces any
// placeholder values with auto-generated secure defaults.
//
// The secret-key rules live in internal/envgen (the same logic env-gen.sh used),
// so create-time generation and later .env (re)generation stay in lock-step. This
// is the create-time pass: no existing .env to preserve from.
func GenerateSmartDefaults(envs map[string]string) map[string]string {
	out := make(map[string]string, len(envs))
	for k, v := range envs {
		out[k] = envgen.ResolveImageValue(k, v, nil, envgen.CryptoRand)
	}
	return out
}
