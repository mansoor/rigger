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

# Copy built application and pruned modules
COPY --from=builder --chown=appuser:appgroup /app/package.json ./
COPY --from=builder --chown=appuser:appgroup /app/node_modules ./node_modules
COPY --from=builder --chown=appuser:appgroup /app/dist ./dist 2>/dev/null || true
COPY --from=builder --chown=appuser:appgroup /app/build ./build 2>/dev/null || true
# For apps that run src directly (e.g., tsc compiled to dist)
COPY --from=builder --chown=appuser:appgroup /app/src ./src 2>/dev/null || true

EXPOSE 3000

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="nodejs-backend"

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:3000/health || exit 1

CMD ["node", "dist/index.js"]
