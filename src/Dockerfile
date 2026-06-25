# =============================================================================
# Build context is the REPO ROOT (see docker-compose.yml). The runtime image
# bakes in templates/ (seed for fresh installs); all toolkit logic now lives in
# the Go binary — the bash scripts were removed (Phase 6.5 finish).
# =============================================================================

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
    docker-cli-compose

COPY --from=go-builder /rigger /usr/local/bin/rigger

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
