# =============================================================================
# React (Vite) — Production multi-stage Dockerfile
# Stage 1: Install deps + build static assets
# Stage 2: Serve via Nginx (no Node in production)
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM node:20-alpine AS builder

ARG BUILD_ENV=production
ARG VITE_API_URL=""

WORKDIR /app

COPY package.json package-lock.json* yarn.lock* pnpm-lock.yaml* ./
RUN \
  if [ -f package-lock.json ]; then npm ci --prefer-offline; \
  elif [ -f yarn.lock ]; then yarn --frozen-lockfile; \
  elif [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm i --frozen-lockfile; \
  else npm install; \
  fi

COPY . .

ENV NODE_ENV=production \
    VITE_API_URL=${VITE_API_URL}

RUN npm run build

# ── Stage 2: Nginx static server ─────────────────────────────────────────────
FROM nginx:1.25-alpine AS runtime

ARG VERSION=0.0.0

# Copy compiled assets
COPY --from=builder /app/dist /usr/share/nginx/html

# SPA routing: all 404s → index.html
COPY docker/nginx-spa.conf /etc/nginx/conf.d/default.conf

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="react-frontend"

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget -qO- http://localhost/index.html || exit 1

CMD ["nginx", "-g", "daemon off;"]
