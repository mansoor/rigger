#!/usr/bin/env bash
# =============================================================================
# rigger — thin host-side CLI for Rigger (Phase 6.5d)
#
# Talks to the Rigger server's HTTP API. Stores a session (refresh cookie + URL)
# under ~/.rigger so the daily commands need no re-auth for 7 days.
#
#   rigger login [url]                          authenticate, store a session
#   rigger list                                 list workspaces
#   rigger <cmd> <workspace> <env> [args...]    run a workspace command, streaming
#       cmds: start stop down restart update ps logs refresh backup restore init version
#
#   rigger start   myapp prod
#   rigger logs    myapp prod web
#   rigger backup  myapp prod db
#   rigger restore myapp prod 2026-01-02_03-04-05
# =============================================================================
set -euo pipefail

CONFIG_DIR="${RIGGER_CONFIG_DIR:-$HOME/.rigger}"
COOKIES="$CONFIG_DIR/cookies"
URL_FILE="$CONFIG_DIR/url"

die() { echo "rigger: $*" >&2; exit 1; }

cmd_login() {
  local url="${1:-}"
  [[ -n "$url" ]] || read -rp "Server URL [http://localhost:8080]: " url
  url="${url:-http://localhost:8080}"
  local user pass
  read -rp "Username: " user
  read -rsp "Password: " pass; echo
  mkdir -p "$CONFIG_DIR"; chmod 700 "$CONFIG_DIR" 2>/dev/null || true
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -c "$COOKIES" -X POST "$url/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$user\",\"password\":\"$pass\"}")
  [[ "$code" == "200" ]] || die "login failed (HTTP $code)"
  printf '%s' "$url" > "$URL_FILE"
  chmod 600 "$COOKIES" "$URL_FILE" 2>/dev/null || true
  echo "Logged in to $url"
}

require_session() {
  [[ -f "$URL_FILE" && -f "$COOKIES" ]] || die "not logged in — run: rigger login"
  URL="$(cat "$URL_FILE")"
}

# Exchange the stored refresh cookie for a fresh 15-min access token.
access_token() {
  local resp tok
  resp=$(curl -s -b "$COOKIES" -c "$COOKIES" -X POST "$URL/api/auth/refresh")
  tok=$(printf '%s' "$resp" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [[ -n "$tok" ]] || die "session expired — run: rigger login"
  printf '%s' "$tok"
}

# Build a JSON array from the remaining args.
json_array() {
  local out="[" first=1 a
  for a in "$@"; do
    [[ $first -eq 1 ]] && first=0 || out+=","
    out+="\"$a\""
  done
  printf '%s]' "$out"
}

cmd_action() {
  local action="$1" ws="${2:-}" env="${3:-}"
  shift 3 2>/dev/null || true
  [[ -n "$ws" && -n "$env" ]] || die "usage: rigger $action <workspace> <env> [args]"
  require_session
  local token extra
  token="$(access_token)"
  extra="$(json_array "$@")"
  curl -sN -X POST "$URL/api/workspaces/$ws/envs/$env/action" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d "{\"command\":\"$action\",\"extra\":$extra}"
}

cmd_list() {
  require_session
  local token json
  token="$(access_token)"
  json="$(curl -s "$URL/api/workspaces" -H "Authorization: Bearer $token")"
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$json" | jq -r '.[].name'
  else
    # Fallback without jq: each workspace object has "name" immediately
    # followed by "path" — a unique anchor that nested name fields lack.
    printf '%s' "$json" | grep -o '"name":"[^"]*","path"' | sed 's/"name":"//; s/","path"//'
  fi
}

usage() {
  sed -n '3,18p' "$0" | sed 's/^# \{0,1\}//'
}

main() {
  local cmd="${1:-help}"
  shift 2>/dev/null || true
  case "$cmd" in
    login) cmd_login "$@" ;;
    list)  cmd_list ;;
    help|-h|--help) usage ;;
    start|stop|down|restart|update|ps|logs|refresh|backup|restore|init|version)
      cmd_action "$cmd" "$@" ;;
    *) echo "rigger: unknown command '$cmd'"; usage; exit 1 ;;
  esac
}

main "$@"
