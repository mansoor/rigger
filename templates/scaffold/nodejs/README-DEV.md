# Node.js starter (scaffolded by Rigger)

A minimal, zero-dependency `http` server that listens on `$PORT` (default `3000`)
with a `/health` endpoint.

## Develop locally

```bash
npm install
npm run dev
# open http://localhost:3000
```

## Deploy

This repo has **no Dockerfile** — Rigger scaffolds the production Node Dockerfile at
build time (it runs `npm run build`, which copies `src/` → `dist/`, then serves
`dist/index.js`). Push your changes:

```bash
git add -A && git commit -m "my change" && git push
```

Then **Build → Deploy** the environment in Rigger.
