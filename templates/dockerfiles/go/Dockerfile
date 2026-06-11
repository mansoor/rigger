# =============================================================================
# Go — production multi-stage Dockerfile (starter; edit for your app)
# Stage 1: build a static binary.  Stage 2: minimal runtime.
# Assumes a module at the build context root with a main package (./ or ./cmd/*).
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Module cache layer
COPY go.mod go.sum* ./
RUN go mod download

# Build a static binary (CGO off so it runs on a scratch/alpine base)
COPY . .
ARG VERSION=0.0.0
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app .

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates wget && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /out/app ./app

ENV PORT=8080
EXPOSE 8080

ARG VERSION=0.0.0
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="go-app"

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1

CMD ["./app"]
