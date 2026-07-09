# React + Vite starter (scaffolded by Rigger)

A minimal Vite single-page app served in production by Nginx.

## Develop locally

```bash
npm install
npm run dev
# open http://localhost:5173
```

## Deploy

This repo has **no Dockerfile** — Rigger scaffolds the production React Dockerfile at
build time (Vite build → static assets served by Nginx). The SPA Nginx config lives at
`docker/nginx-spa.conf` (the Dockerfile copies it). Push your changes:

```bash
git add -A && git commit -m "my change" && git push
```

Then **Build → Deploy** the environment in Rigger.
