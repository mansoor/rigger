# Product and codebase review — November 2026

A full review of Rigger against what homelabbers and small businesses expect from a
self-hosted PaaS, plus everything in the code that is unfinished or fragile.

- **Reviewed:** 2026-11-09, `develop` at `f2296ab`
- **Method:** findings were checked against source, not docs. Items marked **verified** were
  proven by running something; the rest were confirmed by reading code.
- **Companion:** [`GO_LIVE_PLAN.md`](GO_LIVE_PLAN.md) turns these findings into launch gates.
  [`STATUS.md`](STATUS.md) remains the running record of verification state.

Literal TODOs are nearly absent — five in the whole codebase — because this project records
unfinished work in docs rather than comments. So most gaps below came from probing behaviour.

---

## 1. Fix first

Four issues that outrank everything else in this document. 1.4 was found while remediating 1.1.

### 1.1 Any Viewer could get root on the host — critical, **fixed in v0.1.46 (`a66235c`)**

Every container-level endpoint — inspect, stats, the file browser, the terminal, the database and
object-storage consoles — resolves its target through one function. RBAC authorises the
*project*; that function decides which *container* the authorisation reaches. When its lookup
missed, or when the project config could not be read, it fell back to the container name the
client supplied. Because read endpoints need only Viewer, a Viewer on any project could reach
containers outside it, including Rigger's own control plane, whose environment holds the JWT
signing secret. That led to a forged superadmin session and, through the Docker socket, root on
the host.

Demonstrated on the original code before fixing; both fallbacks returned the foreign name with no
error.

**The fix** resolves containers only within the requesting environment's stack, re-checking the
stack label on every candidate, fails closed on every path that used to guess, and returns 404
for anything outside the stack. Regression tests drive the resolver with a fake Docker that
returns every container on the host; a live check against 82 real containers confirmed the
project's own services resolve and all of Rigger's control-plane containers are refused.

**Still to do on every install:** upgrade. Do **not** rotate `JWT_SECRET` by hand — it is also the
key that encrypts every stored credential (see 1.4), so changing it makes them all undecryptable.
Where untrusted accounts had access, the safe response is to rotate the stored credentials at their
source (new SSH keypairs per host, new git and DNS tokens, new SMTP password), and to rotate
`JWT_SECRET` only once 1.4 provides a re-encryption path. Publish a security advisory with the
release, since every released version is affected.

### 1.2 The one-line installer returns 404 — verified

`README.md` and `install.sh` point at `raw.githubusercontent.com/mansoor/rigger/main/install.sh`.
There is no `main` branch; `develop` is the default. The same path on `develop` returns 200. The
first command every new user runs fails.

**Fix:** create a `main` branch that tracks releases (a stable channel), or point the installer at
the latest release tag.

### 1.3 SQLite has never had foreign keys or WAL — verified

[`db.go:18`](../src/backend/internal/db/db.go) opens the database with
`?_journal_mode=WAL&_foreign_keys=on` — the parameter spelling of the *mattn* SQLite driver. This
project uses *modernc*, which silently ignores those parameters:

```
current DSN -> journal_mode=delete  foreign_keys=0
modernc DSN -> journal_mode=wal     foreign_keys=1   (?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1))
```

None of the 15 `REFERENCES … ON DELETE` clauses has ever fired. A startup sweep already cleans
orphans in 6 of those relationships, with a comment blaming SQLite's default; the other 9 keep
accumulating orphaned rows.

**Fix:** switch to modernc's `_pragma=` syntax and add `busy_timeout`. Before enabling foreign keys,
run `PRAGMA foreign_key_check` and delete or repair the orphans it reports — otherwise existing
orphans will start rejecting unrelated writes.

### 1.4 The JWT secret is also the encryption key for every stored credential — verified

[`handlers.go:120`](../src/backend/api/handlers.go) derives the AES-256 key that encrypts data at
rest from `JWT_SECRET` ([`crypto.go`](../src/backend/internal/crypto/crypto.go), HKDF). That key
protects host SSH private keys, git provider tokens and app keys, workspace DNS tokens, proxy TLS
private keys, preview webhook tokens, the SMTP password and managed database user passwords.

Two consequences:

- **One leak is total compromise.** Anyone who obtains the signing secret — as 1.1 allowed — can
  both forge sessions and decrypt every stored credential, including root-equivalent SSH keys to
  every remote host.
- **There is no recovery path.** Rotating `JWT_SECRET` makes every stored credential
  undecryptable, so the one secret that most needs rotating after an incident cannot be rotated.

**Fix:** give data encryption its own key (a dedicated secret, or a wrapped data key stored with
the database), with a one-time migration that decrypts every encrypted column with the old key and
re-encrypts it with the new one inside a transaction, after a backup. Then add a rotation command
for both keys. This is the prerequisite for a complete response to 1.1.

---

## 2. Robustness

| Gap | Evidence | Impact |
|---|---|---|
| No HTTP server timeouts | bare `http.ListenAndServe` ([`main.go:1321`](../src/backend/cmd/server/main.go)) | Slow clients can exhaust connections. Port 9999 is published on every interface. |
| No graceful shutdown | no signal handling anywhere | A self-update or `docker stop` kills in-flight deploys, builds, backups and pipelines mid-run. |
| Background tasks unprotected from panics | one `recover()` in the whole codebase | A panic in any of the ~8 background schedulers (backups, housekeeping, alerts, metrics, previews, maintenance, image checks, cert renewal) takes down the control plane. |
| No lock per environment | no deploy serialization found | A manual deploy, a pipeline and a scheduled backup can run on one environment at once — for example a database dump during a migration. |
| No CPU or memory limits on Compose services | limits are only *displayed*, as "unlimited" | One leaking app can exhaust host memory and take every other app, and Rigger, down with it. |
| No disaster recovery for Rigger itself | nothing backs up `rigger.db` | Lose that disk and you lose users, hosts, SSH keys, git providers and secrets. Remote apps keep running but can no longer be managed. |
| Off-site backups unencrypted | only proxy backups take a passphrase | Full database dumps sit in plaintext on S3 or SFTP. |
| Login token in a URL | `/api/events?token=` ([`useDockerEvents.js:24`](../src/frontend/src/hooks/useDockerEvents.js)) | The session JWT lands in reverse-proxy access logs. Use a short-lived, single-purpose ticket. |
| Whole host filesystem mounted | `/:/host:ro` ([`docker-compose.yml:39`](../src/docker-compose.yml)), used for one disk-usage call | Turns any file-read bug into a read of the whole host, as 1.1 demonstrates. Measure host disk from a throwaway container instead. |
| Thin tests where it matters | `internal/auth` has none (JWT, passwords, TOTP, RBAC); the frontend has none; no end-to-end suite | 41 of 53 Go packages have tests, but not the most security-critical one. Nothing exercises install → deploy → upgrade as a user would. |
| No committed dependency lockfile | `go.sum` is gitignored; the image build regenerates it with `go mod tidy` ([`Dockerfile`](../src/Dockerfile)) | Builds are not reproducible from the repository, and dependency resolution can drift between releases. Commit `go.sum` and build with `go mod download` plus `go mod verify`. |

---

## 3. Unfinished work in the code

- `forward_auth` is reserved in the config model but not implemented; only basic auth works.
- The REST API can start a pipeline but cannot report its status
  ([`apiv1_handlers.go:304`](../src/backend/api/apiv1_handlers.go): "poll status via the UI for
  now"), so CI systems cannot wait on a run.
- Preview environments: webhook handling and PR write-back are GitHub-only in code
  ([`writeback.go:32`](../src/backend/internal/previews/writeback.go)); the "shared" database mode is
  not implemented ([`preview_controller.go:166`](../src/backend/api/preview_controller.go)).
- The GitHub App is used only for cloning; previews and auto-deploy through it are "phase 2"
  ([`github.go:123`](../src/backend/internal/gitproviders/github.go)).
- Pipeline `test` stages are rejected on Swarm environments
  ([`swarm.go:62`](../src/backend/internal/dockerops/swarm.go)).
- Remote hosts get `localhost:5001` written into their `.env` (remote-host gap G2), which breaks
  `update`, Swarm deploys and separate build hosts.
- Image update checks only work with registries that allow anonymous pulls, and read the deployed
  digest from the control plane's daemon — so remote environments always report *indeterminate*.
- Response caching (Souin) is hard-disabled because the plugin crashes Traefik.
- The enhancements backlog still lists #13 (RabbitMQ as a managed engine) as open; it shipped.

---

## 4. Requirement gaps — homelabbers

| Need | Rigger today | Why it matters |
|---|---|---|
| ARM64 images | amd64 only ([`release.yml:57`](../.github/workflows/release.yml)) | Raspberry Pi, Apple Silicon and Ampere machines are common in homelabs. The Nixpacks installer already handles ARM, so this is mostly a CI change plus testing. |
| A large app catalog | 37 stacks | Coolify and Umbrel ship catalogs in the hundreds. An easy way to contribute templates closes the gap over time. |
| GPU passthrough | none | Jellyfin, Plex, Immich, Ollama, Frigate. |
| Scheduled auto-updates with rollback | detected, applied by hand | The Watchtower use case done safely: update inside a set window, roll back if the healthcheck fails. |
| SSO and forward-auth (Authentik, Authelia, Pocket ID) | neither | Protect apps, and Rigger itself, with the identity provider people already run. |
| Tailscale / WireGuard access | Cloudflare Tunnel only | Tailscale is the usual "no open ports" choice in homelabs. |
| Uptime checks on app URLs | container state only | A running container does not mean a working site. |
| Per-app scheduled jobs (cron) | background workers only | For backups, cleanups and scheduled scripts inside an app. |
| Saved, searchable logs | live view only | Without them, "what happened at 3am?" cannot be answered. |

## 5. Requirement gaps — small businesses

| Need | Rigger today | Note |
|---|---|---|
| SSO (Google Workspace, Entra, Okta) | none | Usually expected once there is a team. |
| Require 2FA for everyone | optional per user | Auditors and cyber-insurers ask for it. |
| Encrypted backups and point-in-time recovery | plaintext dumps only | Dump-only recovery loses everything since the last backup. |
| No downtime during Compose deploys | containers are recreated, so a brief outage; rolling updates only on Swarm | A health-checked blue/green switch would close it. |
| Disaster recovery for Rigger itself | none | The control plane is a single point of failure. |
| Public status page | none | Pairs naturally with uptime checks. |
| Automation | API cannot report pipeline status; no CLI beyond `rigger.sh`; no Terraform | Needed to plug Rigger into existing CI/CD. |
| Quotas and usage reports per workspace | none | Needed when several teams or clients share hosts. |
| A documentation site | one README | Onboarding, troubleshooting, upgrade notes. |

---

## 6. User experience

- **A setup checklist after install:** domain and Let's Encrypt email, git provider, backup target,
  notification channel, first deploy. Today setup ends at "create admin".
- **Errors that explain themselves.** The fixes from the last cycle — stale networks, WebSocket
  close codes, stage warnings, the PHP platform check — are a pattern worth applying wherever an
  error is still a bare exit code or "could not reach".
- **One-click "export everything"** for disaster recovery.
- **Bulk actions across the fleet:** update, back up or prune many projects or hosts at once.
- **A visual check of every screen behind login** after the v0.1.38 control refactor — never done.

---

## 7. Where Rigger already leads

Real environments with exact-image promotion, approval-gated pipelines, preview environments, a
catalog of managed databases wired into app environments, multi-host without an agent,
maintenance mode, a proxy service with a firewall and geo-blocking, 2FA with recovery codes,
scoped API keys, and fleet-wide cleanup and disk alerts. Most homelab-oriented tools have few of
these. The gaps above are mostly unglamorous platform work rather than missing features.

---

## 8. Suggested order

1. The fix-first items. 1.1 was the urgent one and is fixed; 1.4 (separate the encryption key,
   with a re-encryption migration) is next, because it completes the response to 1.1.
2. Robustness: timeouts, graceful shutdown, panic protection, the per-environment lock, auth
   tests, backing up Rigger's own database.
3. ARM64 images and the installer fix — the two biggest barriers to adoption.
4. Encrypted backups, resource limits, uptime checks, scheduled updates.
5. SSO and forward-auth, then growing the catalog.

The go-live plan sequences these as launch gates.
