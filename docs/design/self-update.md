# Rigger self-update (published images + in-app updater)

**Status: DESIGN LOCKED 2026-06-19.** Decisions below are settled; build is phased.

## Problem

Today updating Rigger means: SSH to the host → `cd src && git pull && docker compose up --build -d`.
That needs the source tree + a local build on every box. We want **prebuilt images** in a
registry and an **in-app updater** (Admin → Updates) that shows the running version, checks for
a newer release, shows its changelog, and applies it with one click.

## Locked decisions

- **Registry:** GHCR, **public** — `ghcr.io/mansoor/rigger`. Keyless pull on the user's box.
- **Channels:** **stable tags only.** A release = a semver git tag `vX.Y.Z`; the updater tracks
  the newest tag. "Pushed to develop" ≠ "released". (No edge/`:develop` channel for now.)
- **Apply mode:** **manual button only.** Admin sees "update available" + changelog → clicks
  Apply. No auto-update, no surprise restarts.
- **Install script:** keep the same guided UX (prereq checks: Docker, compose plugin, etc.) but
  replace `git pull + build` with **pull + run the published image**, defaulting to the latest
  stable tag, overridable with a CLI flag (e.g. `--tag vX.Y.Z`).

## Versioning

- New `internal/buildinfo` package: `var Version = "dev"`, `var Commit = ""`. Injected at build
  time via `-ldflags "-X …/internal/buildinfo.Version=$VER -X …/internal/buildinfo.Commit=$SHA"`.
  Local source builds stay `"dev"`.
- `GET /api/version` (authenticated) → `{version, commit}`. Surfaced in the UI (sidebar footer +
  the Updates section).

## Self-update mechanism (the tricky part)

A container can't `docker compose up -d` **itself** — the command dies when its own container
stops. Reuse the existing one-off-container pattern (`internal/managedregistry`, the ACME `lego`
runner): Rigger **spawns a detached helper** (docker-CLI image, docker socket mounted) that runs
`docker compose pull rigger && docker compose up -d rigger` against the install's compose project,
then exits. The helper outlives Rigger's restart, so the swap completes. On reboot the additive
`addColumn` migrations run automatically → forward updates are safe.

Wrinkles:
- **Host path to the compose project.** Rigger is containerized; the compose file lives on the
  host. Record the host install dir at install time (`RIGGER_HOST_DIR`) so the helper can bind-mount
  it (same class of fix as `RIGGER_BIND_ROOT`).
- **Rollback.** Record the previous image tag before swapping; expose a "Rollback" action that
  pins back. A failed boot on the new tag is recoverable.
- **Guard.** Refuse to apply while a deploy/build/pipeline run is in flight (or warn).

## Update check

`GET /api/updates/check` (admin) queries the **GitHub Releases API** for the latest `vX.Y.Z`
(gives the version AND changelog/release notes for free), compares to `buildinfo.Version`, and
returns `{current, latest, update_available, notes, published_at}`. Cached briefly. No telemetry
is sent — it's a read of the public releases list.

## Phases

- **Phase 0 — version awareness** (foundation): `buildinfo` + ldflags in Dockerfile + compose
  build arg + `GET /api/version` + show current version in the UI. Independently useful.
- **Phase 1 — publish images** ✅ BUILT (commit pending tag): `.github/workflows/release.yml`
  builds + pushes `ghcr.io/mansoor/rigger:{version}` + `:latest` on a `v*` tag (passing
  `RIGGER_VERSION`/`RIGGER_COMMIT`). The compose `rigger` service now sets BOTH `image:`
  (`ghcr.io/mansoor/rigger:${RIGGER_IMAGE_TAG:-latest}`) and `build:` — they coexist, so
  `compose pull` fetches the published image while `compose up --build` still builds from source
  (no separate override file needed). `install.sh` pulls the image (build fallback via
  `RIGGER_BUILD=1` or pull failure), honors `RIGGER_IMAGE_TAG`, keeps all prereq checks.
  ⚠️ The GHCR package must be set **public** once after the first tag push.
- **Phase 2 — check for updates:** `GET /api/updates/check` (GitHub Releases) + Admin → Updates
  section: current version, Check button, "update available" badge + changelog.
- **Phase 3 — apply update:** detached-helper pull+recreate, prior-tag record, Apply button +
  in-flight-run guard + Rollback.
- **Install script (lands with Phase 1):** rewrite `install.sh` to pull/run the image (keep prereq
  checks), default latest tag, `--tag` override.

## Critical files

- `internal/buildinfo/buildinfo.go` (new), `src/Dockerfile` (ARG + ldflags), `src/docker-compose.yml`
  (build arg; later `image:`), `api/*` (version + updates endpoints + apply via one-off container,
  mirroring `internal/managedregistry`), `cmd/server/main.go` (routes), `.github/workflows/release.yml`
  (new), `install.sh` (pull instead of build), frontend `lib/api.js` + an Admin Updates section +
  sidebar version footer.

## Verification

- Phase 0: `/api/version` returns the ldflag value (and `dev` for a plain `go build`); UI shows it.
- Phase 1: tag `vX.Y.Z` → workflow publishes the image; `docker pull ghcr.io/mansoor/rigger:vX.Y.Z`
  works; a fresh install via `install.sh` pulls + runs without a local build.
- Phase 3: from an older image, Apply pulls the newer tag, the helper recreates the container,
  migrations run, UI reconnects on the new version; Rollback returns to the prior tag.
