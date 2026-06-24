# Rigger 8e — Pluggable secret backend + managed OpenBao vault

**Status: DESIGN (not started).** Group C / post-hardening per the auth-security backlog —
build only after Docker/Swarm hardening lands. Decisions below are the agreed direction;
phasing lets the cheap, broadly-useful slice (the interface + external connect) ship before
the managed-vault sidecar.

## Problem

Today secrets live as plaintext in `config.json`'s per-env `secrets` map and in the
materialized `.env` (Swarm mode uses Docker secrets — the only no-plaintext-on-disk path).
For teams that already run a secret manager, and for **Rigger Cloud** (where "managed,
audited, rotatable secrets" is a selling point), we want Rigger to read/write secrets through
a real vault — **HashiCorp Vault, OpenBao, or any Vault-API-compatible service** — without the
rest of Rigger caring which backend is active.

We also want a zero-config "it just works" option for self-hosters and Cloud: an **internal
vault that Rigger provisions and manages itself**, so a user can turn on Vault-grade secret
storage with a checkbox and no external infrastructure.

## The key decision: managed sidecar, NOT a library embed

OpenBao is open-source (MPL-2.0) and written in Go, so embedding it in Rigger's binary is
*tempting* — but it's the wrong move. Vault/OpenBao was never architected as an embeddable
library:

- **Binary footprint explodes** — OpenBao's module graph (storage backends, auth methods,
  secret engines, cloud-KMS SDKs for auto-unseal, plugin machinery) would push Rigger's binary
  from tens of MB toward 150–400 MB, with much slower builds.
- **CVE surface** — every OpenBao dependency becomes a Rigger dependency to patch.
- **No stable in-process API** — the packages needed to run a core in-process
  (`vault.NewCore`, cluster setup) are internal/test-grade, not supported for embedding →
  version-locked and fragile.
- **You'd own seal/unseal/storage lifecycle inside Rigger's process** — a security boundary
  and a datastore living in your address space.
- **Licensing** — statically linking a large MPL-2.0 codebase into an open-core product adds
  license-hygiene burden.

Rigger is a **container orchestrator** — running and lifecycle-managing a service is its core
competency. So "internal vault" = **Rigger runs and manages an OpenBao container**, exactly
the pattern already proven by `internal/managedregistry` (one-click `registry:2`), managed
MinIO, and Mailpit. The Rigger binary stays flat; the cost is one opt-in container at runtime.

Because OpenBao keeps Vault API parity, the **client code is identical** whether it talks to
the internal OpenBao, a user's self-hosted HashiCorp Vault/OpenBao, or HCP Vault. That parity
*is* the "fully Vault-compatible" selling point — at near-zero binary cost.

| | Library embed | **Managed sidecar (chosen)** |
|---|---|---|
| Rigger binary size | +150–400 MB | **unchanged** |
| Build time / dep graph | much heavier | unchanged |
| CVE surface in Rigger process | all of OpenBao's | just the Vault *client* lib |
| Runtime cost | always in-process | one container, **only when enabled** (~50–150 MB image) |
| Seal/unseal lifecycle | owned in-process | isolated in the container; Rigger orchestrates it |
| Vault/HashiCorp-compatible | ✅ | ✅ (same API) |

## Locked decisions

- **Backend abstraction:** new `internal/secrets` package with an interface. Implementations:
  `LocalBackend` (today's `config.json` secrets map; default) and `VaultBackend` (Vault HTTP
  API client — one client, used for both internal and external).
- **Internal vs External is a toggle, not two code paths.** Both resolve to an
  `(address, token, mount, namespace)` that feeds the single `VaultBackend`.
  - **Internal** → `internal/managedvault` provisions, inits, unseals, and lifecycle-manages an
    OpenBao container (mirrors `internal/managedregistry`).
  - **External** → admin supplies connection details for an existing Vault/OpenBao/HCP Vault.
- **Vault client library only** (e.g. `github.com/hashicorp/vault/api` or the OpenBao client) —
  small, client-side. Never the server packages.
- **Scope of the toggle:** Global (admin) default, with optional per-workspace override — same
  hierarchy already used for base domain, registries, backup targets, etc.
- **`.env` still materializes at deploy in compose mode.** Vault does NOT remove plaintext
  `.env`-on-disk for compose (compose reads `env_file` off disk). Vault's win is a **centralized,
  rotatable, audited secret store with no plaintext source-of-truth in the workspace tree**;
  Swarm Docker secrets remain the only no-plaintext-on-disk runtime. Document this honestly.
- **No telemetry**, consistent with the rest of Rigger.

## Architecture

```
                       Admin / Workspace setting:  Vault = Off | Internal | External
                                                        │
        ┌───────────────────────────────────────────────┼───────────────────────────────┐
        │ Off                          Internal          │            External            │
        ▼                                   ▼             ▼                ▼
  LocalBackend                    internal/managedvault   admin-supplied  (VAULT_ADDR,
  (config.json secrets)           runs+inits+unseals      token/AppRole, namespace, mount)
        │                         an openbao container          │
        │                                   └────────── address+token ──────────┐
        │                                                                        ▼
        └──────────────────────── secrets.Backend interface ───────────► VaultBackend
                                          (Get/Set/Delete/List)          (Vault HTTP API)
                                                  │
                                          envgen / bootstrap / rotation
                                          read & materialize at deploy
```

### `internal/secrets` interface

```go
type Backend interface {
    Get(ctx, ws, env, key string) (string, error)
    Set(ctx, ws, env, key, value string) error
    Delete(ctx, ws, env, key string) error
    List(ctx, ws, env string) (map[string]string, error)
    Health(ctx) (Status, error) // backend kind + reachable + (vault) sealed state
}
```

- `LocalBackend` wraps the existing `wsconfig.Env.Secrets` + `crypto` (the at-rest-encryption
  slice, if shipped first) — no behavior change.
- `VaultBackend` maps `(ws, env, key)` to a KV-v2 path, e.g.
  `secret/data/rigger/{ws}/{env}` with `key` as a field. Mount + path prefix configurable.
- Call sites that touch secrets today (`envgen.getOut` fallback chain, `bootstrap.pinManagedSecrets`,
  rotation handler, reveal endpoint) go through the interface instead of reading the map directly.
  Resolution order at deploy stays: existing `.env` → backend → generate.

### `internal/managedvault` (Internal mode) — mirrors `internal/managedregistry`

- **Run:** start `openbao/openbao` (or `hashicorp/vault`) container, file storage on a
  persistent `rigger-vault-data` volume, exposed internal-only by default or at
  `vault.{base}` via Traefik (gated behind admin-UI auth, like other admin sidecars).
- **Init:** on first run, `operator init` → capture root token + unseal/recovery keys.
- **Unseal (the real complexity):**
  - **Cloud / KMS available** → configure **auto-unseal** via cloud KMS (AWS/GCP/Azure) or a
    Transit seal. Proper enterprise story; the Rigger Cloud differentiator.
  - **Self-host zero-touch** → Rigger stores the unseal key **encrypted with the existing
    HKDF-derived key** (`internal/crypto`) and auto-unseals on container start. Trades
    "sealed-at-rest" for no-touch UX; documented. Users wanting true sealing pick External or
    wire a KMS.
- **Token:** root token (or a scoped Rigger AppRole) stored encrypted via `internal/crypto`
  (same as remote-host SSH keys today).
- **Stop / status / GC:** lifecycle endpoints like managed registry.

### External mode

Admin/workspace form captures: `VAULT_ADDR`, auth (token **or** AppRole `role_id`/`secret_id`),
optional `namespace` (HCP/Enterprise), KV mount, path prefix. Token/secret stored encrypted.
A **Test connection** button calls `Health` (reachable + unsealed + token valid).

### Storage of backend config

- Global default in `app_settings` (`secrets_backend` = `local|internal|external`, plus
  external connection keys; secrets encrypted via `crypto`).
- Optional per-workspace override in `workspace_settings`.
- `settings.EffectiveSecretsBackend(db, wsKey)` resolver (same shape as `EffectiveBaseDomain`).

## Frontend

- **Admin → Settings → Secrets tab:** backend selector (Off / Internal / External). Internal
  shows provision/status/seal-state + start/stop. External shows the connection form + Test +
  status. Surfaces the **honest caveat** about compose `.env` materialization.
- **Manage Workspace → (Secrets):** optional override with an "inherits global" hint.
- Per-env env-var editor is unchanged in shape — secret rows already exist (8a–8d); they now
  read/write through whichever backend is effective.

## Phases

- **Phase 8e-1 — interface + LocalBackend refactor (no behavior change).** Introduce
  `internal/secrets.Backend`, wrap today's config.json/`crypto` path as `LocalBackend`, route
  all secret call sites through it. Golden parity. Independently safe; foundation for the rest.
- **Phase 8e-2 — VaultBackend + External mode.** Vault client, KV-v2 mapping, admin Secrets
  tab with External connect + Test + Health. Verifiable against any throwaway Vault/OpenBao
  dev server. This is the cheap, high-value slice — ship it before the managed sidecar.
- **Phase 8e-3 — Internal mode (managed OpenBao sidecar).** `internal/managedvault`
  run/init/unseal/stop, encrypted token storage, Traefik/internal exposure, self-host
  zero-touch auto-unseal. The "checkbox → working vault" UX.
- **Phase 8e-4 — Cloud-grade auto-unseal + per-workspace override.** KMS/Transit auto-unseal
  for Rigger Cloud; workspace-level backend override; rotation through Vault; audit surfacing.

## Verification

- **Unit:** `secrets.Backend` contract tests run against both `LocalBackend` and `VaultBackend`
  (the latter against a dev-mode OpenBao in a test container). `EffectiveSecretsBackend`
  inherit/override. KV-v2 path mapping round-trips. Golden parity for the LocalBackend refactor
  (existing config.json secrets unchanged).
- **External E2E:** point Rigger at a standalone `openbao server -dev` → set/rotate/reveal a
  secret → confirm it lands in Vault and materializes into `.env` at deploy. Repeat against a
  real HashiCorp Vault to prove API parity.
- **Internal E2E:** toggle Internal → Rigger provisions the OpenBao container, inits, unseals,
  and a deploy reads secrets from it; restart Rigger → container auto-unseals (self-host path);
  stop → container removed, data volume retained.
- `go build/vet/test`, `npm run build`, rebuild rigger.

## Related

- Supersedes the original one-paragraph 8e in `RIGGER_ROADMAP.md`.
- Reuses: `internal/crypto` (at-rest key), `internal/managedregistry` (sidecar lifecycle
  pattern), the `Effective*` settings-hierarchy pattern, the 8a–8d secret-flag UI.
- Precondition: post Docker/Swarm hardening (Group C). Pairs well with — but does not require —
  the "encrypt the config.json secrets map at rest" slice (strengthens `LocalBackend`).
