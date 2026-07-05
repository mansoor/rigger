# =============================================================================
# Ruby on Rails — production multi-stage Dockerfile (starter; edit me)
# Stage 1: install gems + precompile assets.  Stage 2: lean runtime.
# =============================================================================

ARG RUBY_VERSION=3.3-slim

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM ruby:${RUBY_VERSION} AS builder

WORKDIR /app

# Build toolchain + client libs for native gems (pg/mysql2) and JS asset builds.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential git libpq-dev libyaml-dev pkg-config nodejs npm \
    && rm -rf /var/lib/apt/lists/*

# Gem layer (cache-friendly): resolve before copying the full source.
COPY Gemfile Gemfile.lock ./
RUN bundle config set --local without 'development test' \
    && bundle install --jobs 4 --retry 3

COPY . .

# Precompile assets. SECRET_KEY_BASE_DUMMY lets Rails 7.1+ boot without a real key.
RUN SECRET_KEY_BASE_DUMMY=1 bundle exec rails assets:precompile 2>/dev/null || true

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM ruby:${RUBY_VERSION} AS runtime

# Runtime client libs only (no compilers). tzdata for correct time zones.
RUN apt-get update && apt-get install -y --no-install-recommends \
      libpq5 curl tzdata \
    && rm -rf /var/lib/apt/lists/*

ENV RAILS_ENV=production \
    RACK_ENV=production \
    RAILS_LOG_TO_STDOUT=1 \
    RAILS_SERVE_STATIC_FILES=1 \
    PORT=3000 \
    BUNDLE_WITHOUT="development:test"

WORKDIR /app

# Copy the resolved gems and the built app from the builder stage.
COPY --from=builder /usr/local/bundle /usr/local/bundle
COPY --from=builder /app /app

RUN useradd -m -u 10001 appuser && chown -R appuser /app
USER appuser

ARG VERSION=0.0.0
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="rails-app"

EXPOSE 3000

# Rails 7.1+ exposes /up; fall back to / for older apps.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD curl -fsS "http://localhost:${PORT}/up" >/dev/null 2>&1 \
      || curl -fsS "http://localhost:${PORT}/" >/dev/null 2>&1 || exit 1

# Bind to 0.0.0.0 so the container is reachable. Run DB migrations at deploy via
# Rigger's pre-deploy command (e.g. `bin/rails db:migrate`), not here.
CMD ["sh", "-c", "bundle exec rails server -b 0.0.0.0 -p ${PORT}"]
