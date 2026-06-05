# Rename Plan: DADS → Rigger

**Purpose:** Rename the project from **DADS** to **Rigger** across docs and code. This is a standalone plan produced from a full repo scan on 2026-06-04 (HEAD `d3b02af`, branch `develop`). Feed this to a session, fill in the **Decisions** below, then execute the **Plan**.

> **Do NOT start a blind find-replace.** A few `dads` occurrences are state-affecting (encryption key derivation, Docker volume, Go module path) and need the decisions answered first.

---

## 0. DECISIONS REQUIRED (fill these in before executing)

| # | Decision | Options / recommendation | Your answer |
|---|----------|--------------------------|-------------|
| 1 | **Go module path** | `github.com/<org>/rigger/ui` (rename GitHub repo too) **or** `github.com/rigger/ui`. Current: `github.com/dads/ui`. | I'll use a new repo after the rename, https://github.com/mansoor/rigger, I also don't want to carry any history, just use the code after rename operation as baseline code |
| 2 | **GitHub repo rename?** | Rename `github.com/mansoor/dads` → `…/rigger` (updates install/clone/raw URLs), or keep old repo. | A new repo https://github.com/mansoor/rigger is already created, just use that and leave the existing dads repo as is |
| 3 | **Crypto HKDF string** `"dads/phase7/host-ssh-key/v1"` | **Recommended: KEEP as-is** (opaque, never shown; changing it makes all stored SSH host keys + managed key undecryptable). Or change + re-enter all host keys on a fresh DB. | Rename it, I'll delete and add host again |
| 4 | **Docker volume `dads-data`** | **Keep the name** to preserve the existing SQLite DB on running installs, **or** rename (and migrate/recreate the volume — loses users/hosts/settings). | rename it, I can start with a new instance of the tool |
| 5 | **Docker project/container/image names** (`name: dads`, `container_name: dads`, image `dads-dads`) | Rename to `rigger` (clean; orphans old containers, harmless), or keep. | Create new as rigger, we can delete orpan old containers later |
| 6 | **`DADS_*` env vars** | Rename to `RIGGER_*`, or keep `DADS_*` (optionally with `RIGGER_*` aliases). | Rename to RIGGER_*, I'll drop all existing workspaces and re-create to perform more testing after rename |
| 7 | **Tagline** | "DADS" = backronym "Docker App Deployment Simplified". "Rigger" isn't — provide a new tagline or keep the descriptive sentence. | Rig once. Deploy anywhere |
| 8 | **Capitalization** | Suggest: **Rigger** (branding/title-case), `rigger` (binary/container/module/CLI). | rigger |
| 9 | **Managed-key comment** `dads-managed` (appears in remote `authorized_keys`) | Rename to `rigger-managed`, or keep. | Yes, rigger-managed |

---

## 1. SCOPE SUMMARY

- **Backend Go:** ~136 matches / 42 files — but **92 are the module import path** `github.com/dads/ui` (36 files). Remaining ~44 are branding/logs/comments.
- **Frontend:** ~41 matches / 8 files (branding + Settings "Remote Hosts" copy).
- **Root/infra/docs:** `install.sh`, `src/docker-compose.yml`, `src/.env.example`, `README.md`, `DADS_ROADMAP.md`, 3 CLI wrappers, 2 image assets, `.dockerignore`.
- Container-internal paths `/toolkit`, `/toolkit/workspaces` are **not** branded — no change.

---

## 2. INVENTORY (by category)

### A. HIGH-RISK — gated on decisions
- **Module path** `github.com/dads/ui` → `src/backend/go.mod` (module line) + every `import` across **36 files**. (Decision #1.)
- **Crypto HKDF label** `src/backend/internal/crypto/crypto.go:21` `const hkdfInfo = "dads/phase7/host-ssh-key/v1"`. (Decision #3 — recommend keep.)
- **Docker identifiers** `src/docker-compose.yml`: `name: dads` (L3), `container_name: dads` (L14), volume `dads-data` (L31, + `volumes:` block), image is `dads-dads` (derived from project name). (Decisions #4, #5.)
- **Repo/clone/raw URLs** `github.com/mansoor/dads(.git)` — `install.sh:26`, `README.md` (install one-liner + manual clone). (Decision #2.)

### B. CLI / binary / command
- Rename files: `dads.sh`, `dads.ps1`, `dads.bat` → `rigger.*` (repo root).
- Binary subcommand dispatch: `src/backend/cmd/server/main.go:29` (`os.Args[1] == "init-workspace"`); docs reference `docker exec dads dads init-workspace`.
- Symlink target: `install.sh:340` installs `/usr/local/bin/dads` (note pre-existing quirk: it symlinks `${DADS_DIR}/src.sh`, not `dads.sh` — verify/fix during rename).
- Config dir `~/.dads` + `DADS_CONFIG_DIR`: `dads.sh:20`, `dads.ps1:6,17`.

### C. Env-var prefix `DADS_*` (Decision #6)
`DADS_PORT`, `DADS_DIR`, `DADS_BRANCH`, `DADS_REPO`, `DADS_DOMAIN`, `DADS_CONFIG_DIR` — in `install.sh`, `src/docker-compose.yml` (`${DADS_PORT}`, `DADS_DOMAIN` label block L46–54), `src/.env.example` (L4 `DADS_PORT`, L17 `DADS_DOMAIN`), `README.md` env tables, `dads.sh`/`dads.ps1`.
- Note: `src/backend/internal/config/config.go` reads `DADS_*`? (No — it reads `LISTEN_ADDR`, `WORKSPACES_DIR`, `REMOTE_WORKSPACES_DIR`, `JWT_SECRET`, etc.; `DADS_PORT`/`DADS_DOMAIN` are compose/install-level only. Confirm during execution.)

### D. Branding / user-visible (cosmetic, safe)
- Frontend: `src/frontend/index.html:6` `<title>DADS — Docker App Deployment Simplified</title>`; branding text in `Layout.jsx`, `LoginPage.jsx`, `SetupPage.jsx`.
- Image assets: `src/frontend/public/dads-icon.png`, `dads-logo.png` — rename + update refs (Layout uses `/dads-icon.png`; check `index.html` favicon).
- Backend log: `src/backend/cmd/server/main.go:443` `"DADS UI listening on %s"`.
- Notifications: `src/backend/internal/alerts/evaluator.go:271` ("DADS resolved: "), `:277` ("DADS alert: "); `src/backend/api/notification_handlers.go:107-108` ("DADS test notification" + body).
- Generated `.env` header: `src/backend/internal/envgen/envgen.go:154,208` ("Auto-generated by DADS").
- Managed-key comment: `src/backend/api/hosts_handlers.go:50` `GenerateSSHKeypair("dads-managed")`; `src/backend/internal/crypto/keygen.go:12` comment. (Decision #9.)
- Misc comments/errors (cosmetic): `backup_handlers.go:414`, `housekeeping_handlers.go:361,393,448,465`, `db.go:234`, `stats.go:369`, `ToolsPage.jsx:12,14,206`, `notify/apprise.go:14`, `dockerops`/`builder`/`workspace` comments.

### E. Docs / config
- `README.md` — title, branding, env tables, container/volume names, paths; **anchor** `#21-dads--web-interface` (TOC L31 + heading L661 + cross-ref L215). Renaming the heading changes the anchor — update all three.
- `DADS_ROADMAP.md` — **rename the file** (e.g. `RIGGER_ROADMAP.md`) + content "DADS" + `~/.dads` (L112). The handoff/README reference this filename — update those refs.
- `.dockerignore:23` pattern `DADS_*.md` — update if the roadmap file is renamed (keeps it out of the image).
- `docs/handoff/*.md` — historical snapshots; **leave as-is**.

### F. No change
- `/toolkit`, `/toolkit/workspaces` (container-internal, unbranded).
- `~/.claude/.../memory/*.md` — outside the repo (update separately if desired).

---

## 3. EXECUTION PLAN (suggested order)

Work on a dedicated branch: `git checkout -b rename/rigger`.

1. **Module path (biggest mechanical step).**
   - `cd src/backend && go mod edit -module <new-module>` (per Decision #1).
   - Replace import prefix repo-wide: every `github.com/dads/ui` → `<new-module>`. (Use an editor's project find-replace or `gofmt -r` is not enough — these are import strings; a scoped text replace across `src/backend/**/*.go` is fine.)
   - `go build ./...` and `go test ./...` to confirm.
2. **Docker/compose** (Decisions #4, #5): `src/docker-compose.yml` project/container/image names; keep or rename `dads-data` per #4.
3. **Env vars** (Decision #6): `install.sh`, `docker-compose.yml`, `.env.example`, wrappers, README.
4. **CLI wrappers**: rename `dads.{sh,ps1,bat}` → `rigger.*`; update `~/.dads`/`DADS_CONFIG_DIR`; fix the `install.sh` symlink (and the `src.sh` quirk).
5. **Crypto string** (Decision #3): leave `crypto.go:21` unless #3 says change.
6. **Branding/cosmetic** (category D): title, assets (rename png + refs), logs, notification titles, .env header, managed-key comment, comments/errors.
7. **Docs** (category E): README (incl. anchor fixes), rename roadmap file + refs, `.dockerignore` pattern.
8. **Verify** (section 4), commit per logical group, open PR.

---

## 4. VERIFICATION CHECKLIST

- `cd src/backend && go build ./... && go vet ./... && go test ./...` — green.
- Throwaway build container pattern: `golang:1.25-alpine`, `go mod tidy`, stub `cmd/server/dist/index.html` for the `//go:embed all:dist`.
- Frontend compiles (Vite — no error overlay).
- `grep -ri dads <repo> --exclude-dir=node_modules --exclude-dir=.git` returns only intentional leftovers (e.g. kept HKDF string, historical handoffs).
- Redeploy: `cd src && docker compose up --build -d` — app boots, login works, **DB intact** (if `dads-data` kept), hosts/settings preserved.
- If `dads-data` was renamed: confirm the migration/fresh-DB expectation.

---

## 5. GOTCHAS

- **Module rename ≠ behavior change**, but it touches 36 files — do it first and build before anything else.
- **HKDF string is load-bearing for secrets** — changing it silently breaks decryption of stored SSH keys (Decision #3).
- **`dads-data` volume holds the SQLite DB** — renaming orphans it (Decision #4).
- **README anchor** `#21-dads--web-interface` changes when the heading is renamed — fix TOC + cross-references together.
- PowerShell here-strings break on embedded quotes — commit via `git commit -F <msgfile>`.
- CRLF working tree: `gofmt -l` flags CRLF files (benign); the Docker build runs `go mod tidy` (go.sum not committed).
- Compose project name is pinned via `name:` in `src/docker-compose.yml`; the image tag (`dads-dads`) derives from it.
