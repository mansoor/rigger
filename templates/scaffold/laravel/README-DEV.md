# Laravel starter (scaffolded by Rigger)

A minimal Laravel 11 app served by `php artisan serve` in production (good for
onboarding/dev/demos — swap in php-fpm + nginx for production-grade serving).

## Develop locally

```bash
composer install
cp .env.example .env
php artisan key:generate
php artisan serve
# open http://localhost:8000
```

## Deploy

This repo has **no Dockerfile** — Rigger scaffolds the production Laravel Dockerfile at
build time (installs PHP + extensions, runs `composer install`, serves on :80). Rigger
also generates an `APP_KEY` and mounts the environment's `.env` into the container.
Push your changes:

```bash
git add -A && git commit -m "my change" && git push
```

Then **Build → Deploy** the environment in Rigger. The health endpoint `/up` confirms
the app is running.
