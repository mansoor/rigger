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
      zip unzip libzip-dev postgresql-dev oniguruma-dev icu-dev git \
    && apk add --no-cache --virtual .build-deps $PHPIZE_DEPS linux-headers \
    && docker-php-ext-configure gd --with-freetype --with-jpeg --with-webp \
    && docker-php-ext-install -j"$(nproc)" \
         pdo pdo_pgsql pdo_mysql gd intl mbstring bcmath zip opcache pcntl \
    && pecl install redis \
    && docker-php-ext-enable redis \
    && apk del .build-deps \
    && rm -rf /tmp/pear

COPY --from=composer:2.7 /usr/bin/composer /usr/local/bin/composer

WORKDIR /var/www/html

# Application source (node_modules/, .git, .env excluded via .dockerignore; a shipped
# vendor/ is KEPT so CodeCanyon apps build offline).
COPY . .

# PHP dependencies — if the app already ships a vendor/ (CodeCanyon / marketplace
# apps bundle one), skip composer ENTIRELY so the build needs no network (this is
# what made `composer update` time out on api.github.com). Otherwise install from
# the lock if present, else resolve from composer.json. --no-scripts skips
# post-install artisan hooks that may need a complete runtime/.env.
RUN if [ -f vendor/autoload.php ]; then \
      echo "vendor/ present — skipping composer (app ships its PHP dependencies)"; \
    elif [ -f composer.json ]; then \
      composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-scripts \
      || composer update --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-scripts; \
    fi

# Front-end assets — only when the app defines a build (Vite / Laravel Mix).
# Best-effort: a registry timeout or missing build script must not fail the image
# (CodeCanyon apps ship pre-built assets under public/, which .dockerignore keeps).
RUN if [ -f package.json ]; then \
      apk add --no-cache nodejs npm \
      && ( npm install --no-audit --no-fund && ( npm run build || echo "no usable npm build script — skipping asset build" ) \
           || echo "npm install/build unavailable (offline or no deps) — keeping any shipped assets" ); \
    fi

# Laravel needs these dirs present + writable at runtime — recreate them (the
# .dockerignore strips their contents, and the app may not ship empty dirs).
RUN mkdir -p storage/framework/cache storage/framework/sessions storage/framework/views \
             storage/logs bootstrap/cache \
    && chmod -R 777 storage bootstrap/cache

EXPOSE 80
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="laravel"

# --no-reload (Laravel 11+) stops `php artisan serve` from auto-restarting whenever
# .env changes — otherwise an app that writes its own .env (a CodeCanyon installer
# saving DB creds / INSTALLED=true) restarts the server mid-request and the next
# request hits a ~1s gap → 502 at the Traefik edge. Version-guarded so apps on older
# Laravel (no such flag) still start. After an in-app .env write, Restart to apply it.
CMD ["sh", "-c", "FLAGS=''; php artisan serve --help 2>/dev/null | grep -q -- '--no-reload' && FLAGS='--no-reload'; exec php artisan serve --host=0.0.0.0 --port=80 $FLAGS"]
