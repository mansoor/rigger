#!/usr/bin/env bash
# =============================================================================
# Rigger teardown — remove the Rigger control plane (and its state) from a host.
#
# Usage:
#   ./uninstall.sh                       # remove Rigger stack, volumes, install dir, images
#   RIGGER_DIR=/opt/rigger ./uninstall.sh
#   ./uninstall.sh --all                 # ALSO wipe EVERY other container/volume/image on
#                                         #   this host (dedicated test boxes only)
#   ./uninstall.sh --yes                 # skip the typed confirmation (automation)
#
# RIGGER_DIR is the install root — the directory that CONTAINS src/. It is the
# SAME path you set as RIGGER_HOST_DIR in src/.env. Default: ~/rigger.
# =============================================================================
set -uo pipefail

RIGGER_DIR="${RIGGER_DIR:-$HOME/rigger}"
NUKE=0
ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --all)  NUKE=1 ;;
    --yes|-y) ASSUME_YES=1 ;;
    *) echo "Unknown option: $arg" >&2; exit 2 ;;
  esac
done

RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

# docker may need sudo
D="docker"; docker info &>/dev/null || D="sudo docker"

# confirm <prompt> <required-answer> — read from the terminal even when the
# script is piped; aborts unless the user types the exact answer (or --yes).
confirm() {
  local prompt="$1" want="$2" ans=""
  [[ "$ASSUME_YES" == "1" ]] && return 0
  if [[ ! -e /dev/tty ]]; then
    echo -e "${RED}No terminal for confirmation. Re-run with --yes to proceed non-interactively.${RESET}" >&2
    exit 1
  fi
  read -r -p "$prompt" ans < /dev/tty
  [[ "$ans" == "$want" ]]
}

echo -e "${BOLD}${CYAN}Rigger teardown${RESET}"
echo -e "  Install dir: ${BOLD}${RIGGER_DIR}${RESET}"
echo ""
echo -e "${YELLOW}This will permanently remove:${RESET}"
echo -e "  • The Rigger stack: containers ${BOLD}rigger, rigger-traefik, rigger-socket-proxy${RESET}"
echo -e "  • Its volumes ${BOLD}(incl. the database, admin account, secrets, and TLS certs)${RESET}"
echo -e "  • The ${BOLD}traefik_net${RESET} network, the Rigger images, the ${BOLD}rigger${RESET} CLI, and ${BOLD}${RIGGER_DIR}${RESET}"
if [[ "$NUKE" == "1" ]]; then
  echo ""
  echo -e "${RED}${BOLD}  ⚠  --all: this ALSO deletes EVERY other container, volume, image and${RESET}"
  echo -e "${RED}${BOLD}     network on this Docker host — including anything NOT created by Rigger.${RESET}"
  echo -e "${RED}${BOLD}     Only do this on a host dedicated to Rigger testing.${RESET}"
fi
echo ""

if [[ "$NUKE" == "1" ]]; then
  if ! confirm "$(echo -e "Type ${BOLD}wipe everything${RESET} to erase ALL Docker data on this host: ")" "wipe everything"; then
    echo -e "${RED}Aborted — nothing was removed.${RESET}"; exit 1
  fi
else
  if ! confirm "$(echo -e "Type ${BOLD}yes${RESET} to remove Rigger and its data: ")" "yes"; then
    echo -e "${RED}Aborted — nothing was removed.${RESET}"; exit 1
  fi
fi

echo -e "${CYAN}[teardown]${RESET} Removing the Rigger stack…"

# 1. Bring the Rigger stack down WITH its named volumes.
if [[ -f "$RIGGER_DIR/src/docker-compose.yml" ]]; then
  ( cd "$RIGGER_DIR/src" && $D compose down -v --remove-orphans ) || true
fi

# 2. Belt-and-suspenders for when the compose file is already gone.
$D rm -f rigger rigger-traefik rigger-socket-proxy rigger-registry 2>/dev/null || true
for v in rigger_rigger-data rigger_traefik-certs rigger_rigger-dynamic \
         rigger-data traefik-certs rigger-dynamic \
         rigger-registry-data rigger-registry-auth; do
  $D volume rm -f "$v" 2>/dev/null || true
done

# 3. Remove the shared Traefik network (external ⇒ compose 'down' leaves it).
#    No-ops if app containers are still attached (clear those with --all).
$D network rm traefik_net 2>/dev/null || true

# 4. Remove the Rigger images (published + any locally-built tag).
$D images 'ghcr.io/mansoor/rigger' -q | sort -u | xargs -r $D rmi -f 2>/dev/null || true
$D images 'rigger-rigger' -q | sort -u | xargs -r $D rmi -f 2>/dev/null || true

# 5. Remove the CLI symlink + install dir.
(sudo rm -f /usr/local/bin/rigger 2>/dev/null) || rm -f /usr/local/bin/rigger 2>/dev/null || true
rm -rf "$RIGGER_DIR"

# 6. --all: full host wipe (everything Docker, not just Rigger).
if [[ "$NUKE" == "1" ]]; then
  echo -e "${CYAN}[teardown]${RESET} --all: pruning ALL Docker containers/volumes/images/networks…"
  $D ps -aq | xargs -r $D rm -f 2>/dev/null || true
  $D system prune -a --volumes -f 2>/dev/null || true
fi

echo -e "${BOLD}${CYAN}[teardown] Done.${RESET} Re-install with install.sh for a clean run."

# If the caller is standing inside the (now-deleted) install dir, their shell's
# CWD no longer exists — the next git/docker command would fail with
# "getcwd: cannot access parent directories". A script can't change the parent
# shell's directory, so warn loudly to cd out first.
case "${PWD:-}/" in
  "$RIGGER_DIR"|"$RIGGER_DIR"/*)
    echo ""
    echo -e "${YELLOW}${BOLD}  ⚠  Your current directory ($RIGGER_DIR) was just removed.${RESET}"
    echo -e "${YELLOW}     Run ${BOLD}cd /${RESET}${YELLOW} (or any existing dir) before re-installing, or the next${RESET}"
    echo -e "${YELLOW}     command will fail with 'getcwd: cannot access parent directories'.${RESET}"
    ;;
esac
