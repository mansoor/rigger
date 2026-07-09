# Django starter (scaffolded by Rigger)

A minimal Django project (package `config`) served by gunicorn in production.

## Develop locally

```bash
python -m venv .venv && source .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt
python manage.py migrate
python manage.py runserver
# open http://localhost:8000
```

## Deploy

This repo has **no Dockerfile** — Rigger scaffolds the production Python Dockerfile at
build time and serves `config.wsgi:application` with gunicorn on `$PORT`. Push:

```bash
git add -A && git commit -m "my change" && git push
```

Then **Build → Deploy** the environment in Rigger. Override the WSGI module with the
`WSGI_MODULE` env var if you rename the project package.
