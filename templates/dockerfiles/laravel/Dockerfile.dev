# =============================================================================
# Laravel (PHP-FPM) — Development Dockerfile
# Includes: Xdebug, dev dependencies, hot-reload support
# Source code is bind-mounted — no COPY needed
# =============================================================================

FROM php:8.3-fpm-alpine

ARG BUILD_ENV=development
ENV APP_ENV=local \
    APP_DEBUG=true \
    PHP_MEMORY_LIMIT=512M

RUN apk add --no-cache \
      libpng-dev libjpeg-turbo-dev libwebp-dev \
      freetype-dev zip libzip-dev \
      postgresql-dev oniguruma-dev \
      git curl nodejs npm \
    && docker-php-ext-configure gd \
         --with-freetype --with-jpeg --with-webp \
    && docker-php-ext-install -j"$(nproc)" \
         pdo pdo_pgsql pdo_mysql \
         gd mbstring bcmath zip opcache pcntl \
    && pecl install xdebug redis \
    && docker-php-ext-enable xdebug redis

# Xdebug config
COPY docker/php/xdebug.ini /usr/local/etc/php/conf.d/xdebug.ini

# Install Composer
COPY --from=composer:2.7 /usr/bin/composer /usr/local/bin/composer

WORKDIR /app

# Install dependencies inside the container on startup via entrypoint
COPY docker/entrypoint-dev.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 9000

ENTRYPOINT ["/entrypoint.sh"]
CMD ["php-fpm"]
