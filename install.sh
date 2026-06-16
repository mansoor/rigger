#!/usr/bin/env bash
# =============================================================================
# Rigger — Rig once. Deploy anywhere
# One-line installer
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh | bash
#
# Environment variable overrides (prefix the one-liner):
#   RIGGER_DIR=/opt/rigger        — where to clone the repo   (default: ~/rigger)
#   RIGGER_PORT=8080            — UI host port               (default: 8080)
#   RIGGER_BRANCH=main          — git branch to install      (default: main)
#   RIGGER_REPO=<url>           — git clone URL              (default: GitHub HTTPS)
#   ACME_EMAIL=you@email.com  — Let's Encrypt contact email
#   SKIP_DOCKER=1             — skip Docker installation check
#
# Example with overrides:
#   curl -sSL https://raw.githubusercontent.com/mansoor/rigger/main/install.sh \
#     | RIGGER_DIR=/opt/rigger RIGGER_PORT=9090 ACME_EMAIL=admin@example.com bash
# =============================================================================

set -euo pipefail

# ── Configuration ─────────────────────────────────────────────────────────────

RIGGER_REPO="${RIGGER_REPO:-https://github.com/mansoor/rigger.git}"
RIGGER_DIR="${RIGGER_DIR:-$HOME/rigger}"
RIGGER_PORT="${RIGGER_PORT:-8080}"
RIGGER_BRANCH="${RIGGER_BRANCH:-main}"
ACME_EMAIL="${ACME_EMAIL:-}"
SKIP_DOCKER="${SKIP_DOCKER:-0}"

# ── Colour helpers ─────────────────────────────────────────────────────────────

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${CYAN}[Rigger]${RESET}  $*"; }
success() { echo -e "${GREEN}[Rigger]${RESET}  $*"; }
warn()    { echo -e "${YELLOW}[Rigger]${RESET}  $*"; }
die()     { echo -e "${RED}[Rigger]${RESET}  ERROR: $*" >&2; exit 1; }
step()    { echo -e "\n${BOLD}${CYAN}▶ $*${RESET}"; }

# ── OS detection ──────────────────────────────────────────────────────────────

detect_os() {
  if [[ "$OSTYPE" == "darwin"* ]]; then
    echo "macos"
  elif [[ -f /etc/os-release ]]; then
    # shellcheck source=/dev/null
    source /etc/os-release
    case "${ID:-}" in
      ubuntu|debian|linuxmint|pop)  echo "debian" ;;
      centos|rhel|almalinux|rocky|fedora|amzn) echo "rhel" ;;
      arch|manjaro)                  echo "arch" ;;
      alpine)                        echo "alpine" ;;
      *)                             echo "unknown" ;;
    esac
  else
    echo "unknown"
  fi
}

OS=$(detect_os)
info "Detected OS: ${OS}"

# ── Privilege helper ──────────────────────────────────────────────────────────

# Use sudo only when not already root
maybe_sudo() {
  if [[ "$(id -u)" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

# ── Dependency checks & installation ─────────────────────────────────────────

need() { command -v "$1" &>/dev/null; }

install_pkg_debian() {
  maybe_sudo apt-get update -qq
  maybe_sudo apt-get install -y -qq "$@"
}

install_pkg_rhel() {
  if need dnf; then
    maybe_sudo dnf install -y -q "$@"
  else
    maybe_sudo yum install -y -q "$@"
  fi
}

install_pkg_arch() {
  maybe_sudo pacman -Sy --noconfirm --needed "$@"
}

install_pkg_alpine() {
  maybe_sudo apk add --no-cache "$@"
}

install_pkg() {
  case "$OS" in
    debian) install_pkg_debian "$@" ;;
    rhel)   install_pkg_rhel   "$@" ;;
    arch)   install_pkg_arch   "$@" ;;
    alpine) install_pkg_alpine "$@" ;;
    macos)
      if need brew; then
        brew install "$@" 2>/dev/null || true
      else
        warn "Homebrew not found. Install it from https://brew.sh then re-run."
      fi
      ;;
    *) warn "Unknown OS — cannot auto-install $*. Please install manually." ;;
  esac
}

step "Checking required tools"

# git
if ! need git; then
  info "Installing git…"
  install_pkg git
fi
success "git $(git --version | awk '{print $3}')"

# curl
if ! need curl; then
  info "Installing curl…"
  install_pkg curl
fi
success "curl $(curl --version | head -1 | awk '{print $2}')"

# jq
if ! need jq; then
  info "Installing jq…"
  install_pkg jq
fi
success "jq $(jq --version)"

# openssl
if ! need openssl; then
  info "Installing openssl…"
  install_pkg openssl
fi
success "openssl $(openssl version | awk '{print $2}')"

# bash 4+ (macOS ships bash 3.2)
BASH_MAJ=$(bash --version | head -1 | grep -oE '[0-9]+\.[0-9]+' | head -1 | cut -d. -f1)
if [[ "${BASH_MAJ:-3}" -lt 4 && "$OS" == "macos" ]]; then
  warn "macOS ships Bash 3.2. Some Rigger scripts work best with Bash 4+."
  warn "Run: brew install bash"
fi

# ── Docker ────────────────────────────────────────────────────────────────────

if [[ "$SKIP_DOCKER" != "1" ]]; then
  step "Checking Docker"

  if ! need docker; then
    info "Docker not found — installing…"
    case "$OS" in
      debian|rhel)
        # Official convenience script — works on Debian/Ubuntu/CentOS/RHEL/Fedora
        curl -fsSL https://get.docker.com | maybe_sudo sh
        ;;
      arch)
        install_pkg docker
        maybe_sudo systemctl enable --now docker
        ;;
      alpine)
        install_pkg docker
        maybe_sudo rc-update add docker default
        maybe_sudo service docker start
        ;;
      macos)
        die "Docker Desktop is required on macOS. Download from https://www.docker.com/products/docker-desktop"
        ;;
      *)
        die "Cannot auto-install Docker on this OS. Install from https://docs.docker.com/get-docker/ then re-run."
        ;;
    esac

    # Add current user to the docker group (Linux only)
    if [[ "$OS" != "macos" ]] && [[ "$(id -u)" -ne 0 ]]; then
      maybe_sudo usermod -aG docker "$USER" 2>/dev/null || true
      warn "You have been added to the 'docker' group. You may need to log out and back in for this to take effect."
      warn "For this session, subsequent docker commands will use sudo automatically."
    fi

    # Start Docker daemon (Linux)
    if [[ "$OS" != "macos" ]]; then
      if need systemctl; then
        maybe_sudo systemctl enable --now docker 2>/dev/null || true
      fi
    fi
  fi

  # Verify Docker is reachable
  if ! docker info &>/dev/null; then
    if [[ "$(id -u)" -ne 0 ]]; then
      # Try with sudo as fallback before failing
      if sudo docker info &>/dev/null; then
        warn "Docker requires sudo for this session. Consider logging out/in to apply group membership."
        DOCKER_CMD="sudo docker"
      else
        die "Docker daemon is not running. Start it with: sudo systemctl start docker"
      fi
    else
      die "Docker daemon is not running. Start it with: systemctl start docker"
    fi
  else
    DOCKER_CMD="docker"
  fi

  DOCKER_VER=$($DOCKER_CMD version --format '{{.Server.Version}}' 2>/dev/null || echo "unknown")
  success "Docker ${DOCKER_VER}"

  # ── Docker Compose v2 ──────────────────────────────────────────────────────

  step "Checking Docker Compose v2"

  if ! $DOCKER_CMD compose version &>/dev/null; then
    info "Docker Compose v2 plugin not found — installing…"
    case "$OS" in
      debian)
        install_pkg docker-compose-plugin
        ;;
      rhel)
        install_pkg docker-compose-plugin 2>/dev/null || \
          install_pkg docker-compose 2>/dev/null || true
        ;;
      arch)
        install_pkg docker-compose
        ;;
      alpine)
        install_pkg docker-compose
        ;;
      *)
        # Fallback: download the binary directly
        COMPOSE_VER=$(curl -sSf "https://api.github.com/repos/docker/compose/releases/latest" \
          | grep '"tag_name"' | head -1 | grep -oE 'v[0-9.]+')
        COMPOSE_ARCH=$(uname -m | sed 's/x86_64/x86_64/;s/aarch64/aarch64/;s/armv7l/armv7/')
        COMPOSE_URL="https://github.com/docker/compose/releases/download/${COMPOSE_VER}/docker-compose-linux-${COMPOSE_ARCH}"
        PLUGIN_DIR="${HOME}/.docker/cli-plugins"
        mkdir -p "$PLUGIN_DIR"
        curl -sSfL "$COMPOSE_URL" -o "${PLUGIN_DIR}/docker-compose"
        chmod +x "${PLUGIN_DIR}/docker-compose"
        ;;
    esac
  fi

  if ! $DOCKER_CMD compose version &>/dev/null; then
    die "Docker Compose v2 is required but could not be installed. See https://docs.docker.com/compose/install/"
  fi

  COMPOSE_VER=$($DOCKER_CMD compose version --short 2>/dev/null || echo "installed")
  success "Docker Compose v${COMPOSE_VER}"
else
  DOCKER_CMD="docker"
fi

# ── Clone or update repo ──────────────────────────────────────────────────────

step "Setting up Rigger in ${RIGGER_DIR}"

if [[ -d "${RIGGER_DIR}/.git" ]]; then
  info "Repository already exists — pulling latest changes on branch ${RIGGER_BRANCH}…"
  git -C "$RIGGER_DIR" fetch --quiet origin
  git -C "$RIGGER_DIR" checkout --quiet "${RIGGER_BRANCH}"
  git -C "$RIGGER_DIR" pull --quiet --ff-only origin "${RIGGER_BRANCH}" || \
    warn "Could not fast-forward; your local changes may conflict."
  success "Repository updated"
else
  info "Cloning ${RIGGER_REPO} → ${RIGGER_DIR}…"
  git clone --quiet --branch "${RIGGER_BRANCH}" "${RIGGER_REPO}" "${RIGGER_DIR}"
  success "Repository cloned"
fi

# ── Generate configuration ────────────────────────────────────────────────────

ENV_FILE="${RIGGER_DIR}/src/.env"

# Detect this host's primary LAN IP so apps deployed locally get a reachable
# "App host" out of the box. The running server lives in a container and can only
# see its 172.x bridge IP, so it can't detect this itself — we capture it here on
# the host and hand it to the server via RIGGER_APP_HOST (seeded into the app_host
# setting on first boot; editable later in Settings → General).
#
# Prefer the source IP of the route to the internet — that's the interface other
# machines reach this host on, and it skips docker0/bridge IPs that `hostname -I`
# can list first. Fall back to the first `hostname -I` address.
HOST_IP=$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1)
[[ -z "$HOST_IP" ]] && HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
HOST_IP="${HOST_IP:-}"

if [[ ! -f "$ENV_FILE" ]]; then
  step "Generating configuration"

  JWT_SECRET=$(openssl rand -hex 32)

  cat > "$ENV_FILE" <<EOF
# Auto-generated by Rigger installer — $(date -u '+%Y-%m-%d %H:%M UTC')

# Port to expose the Rigger UI on the host
RIGGER_PORT=${RIGGER_PORT}

# JWT signing secret — do not share or commit this value
JWT_SECRET=${JWT_SECRET}

# Let's Encrypt contact email (required for SSL certificates on workspace domains)
ACME_EMAIL=${ACME_EMAIL:-your@email.com}

# This host's LAN/public address — seeds the "App host" used to build app links
# and magic-DNS (sslip/nip) URLs for locally-deployed envs. Detected at install;
# edit in Settings → General if wrong (e.g. a public IP or a different interface).
RIGGER_APP_HOST=${HOST_IP}
EOF

  success "Configuration written to ${ENV_FILE}"
  [[ -z "$ACME_EMAIL" ]] && warn "ACME_EMAIL not set — edit ${ENV_FILE} before using SSL certificates"
else
  info "Configuration file already exists — skipping generation"
  # Ensure RIGGER_PORT is up to date if user passed a custom port
  if [[ "$RIGGER_PORT" != "8080" ]]; then
    sed -i.bak "s/^RIGGER_PORT=.*/RIGGER_PORT=${RIGGER_PORT}/" "$ENV_FILE" && rm -f "${ENV_FILE}.bak"
  fi
fi

# ── Docker network ────────────────────────────────────────────────────────────

step "Ensuring traefik_net network exists"

if ! $DOCKER_CMD network inspect traefik_net &>/dev/null; then
  $DOCKER_CMD network create traefik_net
  success "Created traefik_net network"
else
  success "traefik_net network already exists"
fi

# ── Build and start ───────────────────────────────────────────────────────────

step "Building and starting Rigger"

# The toolkit scripts are baked into the image (Phase 6.5d) — the build context
# is the repo root, so no separate scripts mount is needed.
cd "${RIGGER_DIR}/src"
$DOCKER_CMD compose up --build -d

# ── Install the `rigger` CLI wrapper ────────────────────────────────────────────

step "Installing the rigger CLI"

if [[ -f "${RIGGER_DIR}/rigger.sh" ]]; then
  chmod +x "${RIGGER_DIR}/rigger.sh"
  if maybe_sudo ln -sf "${RIGGER_DIR}/rigger.sh" /usr/local/bin/rigger 2>/dev/null; then
    success "Installed 'rigger' CLI to /usr/local/bin/rigger"
  else
    warn "Could not install to /usr/local/bin — run Rigger commands with: ${RIGGER_DIR}/rigger.sh"
  fi
fi

# ── Post-install summary ──────────────────────────────────────────────────────

HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}') || HOST_IP="localhost"

echo ""
echo -e "${GREEN}${BOLD}╔══════════════════════════════════════════════════════════╗${RESET}"
echo -e "${GREEN}${BOLD}║          Rigger installed successfully!                    ║${RESET}"
echo -e "${GREEN}${BOLD}╚══════════════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  ${BOLD}Local access:${RESET}     http://localhost:${RIGGER_PORT}"
echo -e "  ${BOLD}Network access:${RESET}   http://${HOST_IP}:${RIGGER_PORT}"
echo ""
echo -e "  ${BOLD}Installation:${RESET}     ${RIGGER_DIR}"
echo -e "  ${BOLD}Configuration:${RESET}    ${ENV_FILE}"
echo ""
echo -e "  On first visit, you will be prompted to create an admin account."
echo ""
echo -e "  ${YELLOW}Note:${RESET} Traefik is running and listening on ports 80 and 443."
echo -e "  Ensure these ports are available if you plan to use domain routing."
echo ""
echo -e "  ${BOLD}CLI:${RESET}              rigger login  →  rigger start <workspace> <env>   (run 'rigger help')"
echo ""
echo -e "  ${BOLD}Useful commands:${RESET}"
echo -e "    Stop:    cd ${RIGGER_DIR}/src && docker compose down"
echo -e "    Start:   cd ${RIGGER_DIR}/src && docker compose up -d"
echo -e "    Update:  cd ${RIGGER_DIR} && git pull && cd src && docker compose up --build -d"
echo -e "    Logs:    cd ${RIGGER_DIR}/src && docker compose logs -f rigger"
echo ""
