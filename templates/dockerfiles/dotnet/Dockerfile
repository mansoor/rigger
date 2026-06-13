# =============================================================================
# .NET (ASP.NET Core, C#) — production multi-stage Dockerfile (starter; edit me)
# Stage 1: restore + publish.  Stage 2: ASP.NET runtime.
# Replace App.dll / the .csproj path with your project's.
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM mcr.microsoft.com/dotnet/sdk:8.0 AS builder

WORKDIR /src

# Copy the whole project and restore. (We intentionally don't pre-copy only
# *.csproj/*.sln for a cache layer: a glob that matches nothing fails the build,
# and single-project apps often have no .sln.)
COPY . .
RUN dotnet restore

ARG VERSION=0.0.0
RUN dotnet publish -c Release -o /app/publish \
      -p:Version=${VERSION} --no-restore

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM mcr.microsoft.com/dotnet/aspnet:8.0 AS runtime

# wget for the healthcheck (the aspnet image is Debian-based).
RUN apt-get update && apt-get install -y --no-install-recommends wget \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -u 10001 appuser

WORKDIR /app
COPY --from=builder /app/publish ./

# Bind to all interfaces on $PORT.
ENV ASPNETCORE_URLS=http://0.0.0.0:8080 \
    PORT=8080
EXPOSE 8080

ARG VERSION=0.0.0
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="dotnet-app"

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://localhost:8080/ >/dev/null 2>&1 || exit 1

# Replace App.dll with your published entry assembly.
CMD ["dotnet", "App.dll"]
