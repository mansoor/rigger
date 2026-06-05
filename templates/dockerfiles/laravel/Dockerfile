# =============================================================================
# Laravel (PHP-FPM) — Production multi-stage Dockerfile
# Stage 1: Composer install + asset build (no host PHP/Node required)
# Stage 2: Lean runtime image
# =============================================================================

# ── Stage 1: Composer dependencies ────────────────────────────────────────────
FROM composer:2.7 AS composer-deps

WORKDIR /app

# Copy dependency manifests first for layer caching
COPY composer.json composer.lock ./

RUN composer install \
      --no-dev \
      --no-scripts \
      --no-interaction \
      --prefer-dist \
      --optimize-autoloader \
    && composer clear-cache

# ── Stage 2: Node/NPM build (optional — for Vite / Mix assets) ───────────────
FROM node:20-alpine AS node-build

WORKDIR /app

COPY package.json package-lock.json* ./
RUN npm ci --prefer-offline

COPY . .
# Copy vendor from composer stage so any npm scripts that need PHP stubs work
COPY --from=composer-deps /app/vendor ./vendor

RUN npm run build

# ── Stage 3: Runtime (PHP-FPM) ───────────────────────────────────────────────
FROM php:8.3-fpm-alpine AS runtime

ARG BUILD_ENV=production
ARG VERSION=0.0.0
ENV APP_ENV=${BUILD_ENV} \
    PHP_MEMORY_LIMIT=256M \
    PHP_MAX_EXECUTION_TIME=60

# Install PHP extensions + runtime deps
RUN apk add --no-cache \
      libpng-dev libjpeg-turbo-dev libwebp-dev \
      freetype-dev zip libzip-dev \
      postgresql-dev oniguruma-dev \
      nginx supervisor \
    && docker-php-ext-configure gd \
         --with-freetype --with-jpeg --with-webp \
    && docker-php-ext-install -j"$(nproc)" \
         pdo pdo_pgsql pdo_mysql \
         gd mbstring bcmath zip opcache pcntl \
    && pecl install redis \
    && docker-php-ext-enable redis \
    && rm -rf /tmp/pear

# PHP config
COPY docker/php/php.ini /usr/local/etc/php/conf.d/custom.ini
COPY docker/php/www.conf /usr/local/etc/php-fpm.d/www.conf

WORKDIR /app

# Copy application code
COPY --chown=www-data:www-data . .

# Copy vendor from composer stage
COPY --from=composer-deps --chown=www-data:www-data /app/vendor ./vendor

# Copy compiled assets from node stage
COPY --from=node-build --chown=www-data:www-data /app/public/build ./public/build

# Laravel optimisation
RUN php artisan config:cache \
    && php artisan route:cache \
    && php artisan view:cache \
    && php artisan storage:link \
    && chmod -R 775 storage bootstrap/cache \
    && chown -R www-data:www-data storage bootstrap/cache

EXPOSE 9000

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="laravel-backend"

USER www-data
CMD ["php-fpm"]
