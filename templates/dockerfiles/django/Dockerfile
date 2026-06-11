# =============================================================================
# Python (Django / FastAPI / Flask) — production Dockerfile (starter; edit me)
# Serves over gunicorn. For ASGI (FastAPI), swap the CMD for a uvicorn worker.
# =============================================================================

FROM python:3.12-slim AS runtime

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PORT=8000 \
    PIP_NO_CACHE_DIR=1

# wget for the healthcheck; build-essential only if a dep needs compiling.
RUN apt-get update && apt-get install -y --no-install-recommends wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Dependency layer (cache-friendly)
COPY requirements.txt* pyproject.toml* ./
RUN if [ -f requirements.txt ]; then pip install -r requirements.txt; \
    elif [ -f pyproject.toml ]; then pip install .; fi

# App source
COPY . .

# Collect static for Django (no-op / harmless elsewhere)
RUN python manage.py collectstatic --noinput 2>/dev/null || true

RUN useradd -m -u 10001 appuser && chown -R appuser /app
USER appuser

ARG VERSION=0.0.0
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="python-app"

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -qO- http://localhost:8000/ >/dev/null 2>&1 || exit 1

# Django (WSGI). Replace <project> with your settings module's package, or for
# FastAPI use:  uvicorn app.main:app --host 0.0.0.0 --port 8000
CMD ["sh", "-c", "gunicorn --bind 0.0.0.0:${PORT} ${WSGI_MODULE:-config.wsgi:application}"]
