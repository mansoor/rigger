# =============================================================================
# Laravel — self-contained image, served via PHP's built-in server.
# Builds an ARBITRARY Laravel app with NO app-specific config files: installs PHP
# + extensions, runs composer install, optionally builds front-end assets, and
# serves with `php artisan serve` on :80. Good for onboarding / dev / demos —
# replace with your own php-fpm + nginx Dockerfile for production-grade serving.
# =============================================================================
FROM php:8.3-fpm-alpine

ARG BUILD_ENV=production
ARG VERSION=0.0.0
ENV APP_ENV=${BUILD_ENV} \
    PHP_MEMORY_LIMIT=512M

# Runtime libraries + PHP extensions. PECL compiles need a toolchain
# ($PHPIZE_DEPS = autoconf, gcc, make, …) — installed virtually and removed after,
# so the image stays lean.
RUN apk add --no-cache \
      libpng-dev libjpeg-turbo-dev libwebp-dev freetype-dev \
      zip unzip libzip-dev postgresql-dev oniguruma-dev git \
    && apk add --no-cache --virtual .build-deps $PHPIZE_DEPS linux-headers \
    && docker-php-ext-configure gd --with-freetype --with-jpeg --with-webp \
    && docker-php-ext-install -j"$(nproc)" \
         pdo pdo_pgsql pdo_mysql gd mbstring bcmath zip opcache pcntl \
    && pecl install redis \
    && docker-php-ext-enable redis \
    && apk del .build-deps \
    && rm -rf /tmp/pear

COPY --from=composer:2.7 /usr/bin/composer /usr/local/bin/composer

WORKDIR /var/www/html

# Application source (vendor/, node_modules/, .git, .env excluded via .dockerignore).
COPY . .

# PHP dependencies — install from the lock if present, else resolve from composer.json.
# --no-scripts skips post-install artisan hooks that may need a complete runtime/.env.
RUN if [ -f composer.json ]; then \
      composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-scripts \
      || composer update --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-scripts; \
    fi

# Front-end assets — only when the app defines a build (Vite / Laravel Mix).
RUN if [ -f package.json ]; then \
      apk add --no-cache nodejs npm \
      && npm install --no-audit --no-fund \
      && (npm run build || echo "no usable npm build script — skipping asset build"); \
    fi

# Writable dirs (best-effort; not every app ships them).
RUN chmod -R 775 storage bootstrap/cache 2>/dev/null || true

EXPOSE 80
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="laravel"

CMD ["sh", "-c", "php artisan serve --host=0.0.0.0 --port=80"]
