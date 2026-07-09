# =============================================================================
# Node.js — Production multi-stage Dockerfile
# Stage 1: Install all deps + build
# Stage 2: Lean runtime with production deps only
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM node:20-alpine AS builder

ARG BUILD_ENV=production
ENV NODE_ENV=production

WORKDIR /app

# Install dependencies first (layer cache)
COPY package.json package-lock.json* ./
RUN npm ci --prefer-offline

# Copy source and build
COPY . .
RUN npm run build 2>/dev/null || true   # skip if no build script

# Prune devDependencies for the next stage
RUN npm prune --omit=dev

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM node:20-alpine AS runtime

ARG BUILD_ENV=production
ARG VERSION=0.0.0

ENV NODE_ENV=production \
    PORT=3000

# Create non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Copy the built application from the builder: package.json, the pruned production
# node_modules, and whatever the build produced (dist/ build/ src/). A single COPY of
# the whole app tree is used because Docker COPY has no conditional — the previous
# per-directory `COPY … 2>/dev/null || true` lines were invalid (COPY is not a shell,
# so those trailing tokens were parsed as extra, nonexistent sources and failed).
COPY --from=builder --chown=appuser:appgroup /app ./

EXPOSE 3000

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="nodejs-backend"

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:3000/health || exit 1

CMD ["node", "dist/index.js"]
