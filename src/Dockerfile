# =============================================================================
# Build context is the REPO ROOT (see docker-compose.yml). The runtime image
# bakes in templates/ (seed for fresh installs); all toolkit logic now lives in
# the Go binary — the bash scripts were removed (Phase 6.5 finish).
# =============================================================================

# =============================================================================
# Stage 0 — Fetch the Nixpacks CLI (optional alternative build backend). The CLI
# is a small static Rust binary; the heavy Nix bits are pulled at `nixpacks build`
# time (like any base image), not baked here. Pin the version in one place.
# =============================================================================
FROM alpine:3.19 AS nixpacks
ARG NIXPACKS_VERSION=1.41.0
RUN apk add --no-cache curl tar \
    && arch="$(uname -m)" \
    && case "$arch" in \
         x86_64) a=x86_64 ;; \
         aarch64|arm64) a=aarch64 ;; \
         *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/railwayapp/nixpacks/releases/download/v${NIXPACKS_VERSION}/nixpacks-v${NIXPACKS_VERSION}-${a}-unknown-linux-musl.tar.gz" \
         -o /tmp/nixpacks.tar.gz \
    && tar -xzf /tmp/nixpacks.tar.gz -C /usr/local/bin nixpacks \
    && chmod +x /usr/local/bin/nixpacks

# =============================================================================
# Stage 1 — Build React frontend
# =============================================================================
FROM node:20-alpine AS frontend-builder

WORKDIR /app/frontend
COPY src/frontend/package*.json ./
RUN npm install

COPY src/frontend/ ./
RUN npm run build
# Output lands in ../backend/cmd/server/dist (per vite.config.js)


# =============================================================================
# Stage 2 — Build Go binary
# =============================================================================
FROM golang:1.25-alpine AS go-builder

RUN apk add --no-cache git

WORKDIR /app/backend

# Copy all source first so go mod tidy can resolve imports
COPY src/backend/ ./
# Copy the compiled frontend so embed.FS can include it
COPY --from=frontend-builder /app/backend/cmd/server/dist ./cmd/server/dist

# go.sum is not committed — generate it here from the full source tree
RUN go mod tidy

# Version stamping (self-update Phase 0): CI passes the release tag + short SHA;
# a plain build leaves buildinfo's defaults ("dev").
ARG RIGGER_VERSION=dev
ARG RIGGER_COMMIT=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w \
      -X github.com/mansoor/rigger/ui/internal/buildinfo.Version=${RIGGER_VERSION} \
      -X github.com/mansoor/rigger/ui/internal/buildinfo.Commit=${RIGGER_COMMIT}" \
    -o /rigger ./cmd/server


# =============================================================================
# Stage 3 — Minimal runtime image
# =============================================================================
FROM alpine:3.19

# Runtime tools the Go binary shells out to:
#   docker-cli         — docker ps / pull / inspect / stack / service
#   docker-cli-compose — 'docker compose' v2 plugin (lifecycle, exec, backup dumps)
#   docker-cli-buildx  — 'docker buildx build' (BuildKit) so app Dockerfiles using
#                        RUN --mount=type=cache/secret/bind, heredocs, etc. build
#                        (the legacy builder rejects --mount). See internal/builder.
#   coreutils          — du (per-workspace disk stats)
#   ca-certificates    — TLS to the Docker Hub registry API (image-update checks)
#   tzdata             — local timestamps
# Note: SQL dumps (pg_dump/mysqldump) run INSIDE the db container via
# `docker compose exec`, so no db client is needed here. The former bash-only
# deps (bash, jq, openssl, git) were dropped along with the scripts.
RUN apk add --no-cache \
    coreutils \
    ca-certificates \
    tzdata \
    git \
    openssh-client \
    docker-cli \
    docker-cli-compose \
    docker-cli-buildx

COPY --from=go-builder /rigger /usr/local/bin/rigger

# Nixpacks CLI — the optional per-service build backend (build.method=nixpacks) and
# the auto-fallback for languages Rigger doesn't template. Builds by shelling out to
# the docker-cli above, so no extra daemon is needed.
COPY --from=nixpacks /usr/local/bin/nixpacks /usr/local/bin/nixpacks

# Bake templates/ into the image as a seed (Dockerfile/nginx/stack templates the
# Go bootstrapper reads). docker-compose.yml bind-mounts ../templates and
# ../workspaces as rw DATA, overlaying this baked copy at runtime.
COPY templates/         /toolkit/templates/

# Persistent data directory (SQLite DB lives here)
VOLUME ["/data"]

EXPOSE 8080

ENV LISTEN_ADDR=":8080" \
    TOOLKIT_ROOT="/toolkit" \
    WORKSPACES_DIR="/toolkit/workspaces" \
    DATA_DIR="/data"

ENTRYPOINT ["/usr/local/bin/rigger"]
