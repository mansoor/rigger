# Go starter (scaffolded by Rigger)

A minimal `net/http` server that listens on `$PORT` (default `8080`) and returns JSON.

## Develop locally

```bash
go mod download
go run .
# open http://localhost:8080
```

## Deploy

This repo has **no Dockerfile** — Rigger scaffolds the production Go Dockerfile
(static binary → alpine) at build time. Just push your changes:

```bash
git add -A && git commit -m "my change" && git push
```

Then **Build → Deploy** the environment in Rigger. The container listens on `$PORT`
(Rigger sets it), and the web entry is routed automatically.
