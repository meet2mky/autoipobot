#!/usr/bin/env bash
# Clean, repeatable macOS deploy: stop running autoipo processes, build the
# daemon and extension bridge, embed them into the desktop app, configure the
# Telegram settings if needed, then start the daemon and the app.
#
# Usage:
#   ./deploy.sh                       # interactive first-run configuration if needed
#   TG_TOKEN=… TG_CHAT_ID=… HOST_LABEL=… ./deploy.sh
#   DRY_RUN=1 ./deploy.sh             # build only
#   SKIP_VET=1 ./deploy.sh            # skip go vet
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM="$(uname -s 2>/dev/null || echo unknown)"
if [[ "$PLATFORM" != "Darwin" ]]; then
  echo "deploy.sh supports macOS only (detected: $PLATFORM)." >&2
  exit 1
fi

export PATH="/opt/homebrew/bin:/usr/local/go/bin:$PATH"
if ! command -v go >/dev/null 2>&1; then
  echo "go is not installed or not on PATH." >&2
  exit 1
fi

DATA_DIR="$REPO_ROOT/.data"
BIN_DIR="$REPO_ROOT/bin"
LOG_DIR="$REPO_ROOT/daemon/logs"
BUNDLE_DIR="$REPO_ROOT/app/bundled"
LOCK_FILE="$DATA_DIR/deploy.lock"
DAEMON_BIN="$BIN_DIR/autoipo"
CLI_BIN="$BIN_DIR/chrome-extension-cli"
APP_BIN="$BIN_DIR/autoipoapp"
PID_FILE="$DATA_DIR/autoipo.pid"

mkdir -p "$DATA_DIR" "$BIN_DIR" "$LOG_DIR"
chmod 700 "$DATA_DIR"

if ! (umask 077; set -o noclobber; printf '%s\n' "$$" > "$LOCK_FILE") 2>/dev/null; then
  echo "Another deploy.sh invocation is running (lock file exists: $LOCK_FILE)." >&2
  exit 1
fi
cleanup() {
  rm -rf "$BUNDLE_DIR"
  rm -f "$LOCK_FILE"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# --- helpers -----------------------------------------------------------------

# stop_by_pidfile sends SIGTERM to the PID recorded by the daemon and waits.
stop_by_pidfile() {
  [[ -f "$PID_FILE" ]] || return 1
  local pid
  pid="$(tr -d '[:space:]' < "$PID_FILE")"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -KILL "$pid" 2>/dev/null || true
  return 0
}

# stop_by_name falls back to the exact process name (older deployments).
stop_by_name() {
  local name="$1"
  pkill -TERM -x "$name" 2>/dev/null || true
  for _ in $(seq 1 50); do
    pgrep -x "$name" >/dev/null 2>&1 || return 0
    sleep 0.1
  done
  pkill -KILL -x "$name" 2>/dev/null || true
}

# --- stop --------------------------------------------------------------------

echo "Stopping running autoipo processes..."
stop_by_name "autoipoapp"
stop_by_pidfile || stop_by_name "autoipo"
stop_by_name "chrome-extension-cli"

# --- build -------------------------------------------------------------------

cd "$REPO_ROOT"
if [[ "${SKIP_VET:-0}" != "1" ]]; then
  echo "Vetting..."
  go vet ./internal/... ./daemon/... ./chrome-extension-cli/... ./app/...
fi

VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
LDFLAGS="-s -w -X main.version=$VERSION"

echo "Building daemon and extension bridge ($VERSION)..."
go build -trimpath -ldflags "$LDFLAGS" -o "$DAEMON_BIN" ./daemon
go build -trimpath -ldflags "$LDFLAGS" -o "$CLI_BIN" ./chrome-extension-cli

echo "Embedding helpers into the desktop app..."
rm -rf "$BUNDLE_DIR"
mkdir -p "$BUNDLE_DIR"
cp "$DAEMON_BIN" "$BUNDLE_DIR/autoipo"
cp "$CLI_BIN" "$BUNDLE_DIR/chrome-extension-cli"
chmod 755 "$BUNDLE_DIR"/*
go build -trimpath -tags bundled -ldflags "$LDFLAGS" -o "$APP_BIN" ./app

if [[ "${DRY_RUN:-0}" == "1" ]]; then
  echo "Dry run enabled; binaries were built but nothing was configured or started."
  exit 0
fi

# --- configure ---------------------------------------------------------------

# The daemon keeps the bot token in the macOS Keychain and chat ID / host
# label in .data/env. Values passed in the environment are saved first; if
# the configuration is still incomplete, ask interactively.
if [[ -n "${TG_TOKEN:-}" || -n "${TG_CHAT_ID:-}" || -n "${HOST_LABEL:-}" ]]; then
  echo "Saving Telegram settings from the environment..."
  "$DAEMON_BIN" configure
fi

if ! "$DAEMON_BIN" check >/dev/null 2>&1; then
  echo "Telegram settings are incomplete or invalid."
  printf 'Enter Telegram bot token: '
  read -r -s TG_TOKEN
  printf '\n'
  printf 'Enter Telegram chat ID: '
  read -r TG_CHAT_ID
  printf 'Enter host label [MyMac]: '
  read -r HOST_LABEL
  HOST_LABEL="${HOST_LABEL:-MyMac}"
  export TG_TOKEN TG_CHAT_ID HOST_LABEL
  "$DAEMON_BIN" configure
  "$DAEMON_BIN" check
fi
unset TG_TOKEN

# --- start -------------------------------------------------------------------

echo "Starting autoipo daemon..."
nohup "$DAEMON_BIN" >/dev/null 2>&1 </dev/null &
disown $! 2>/dev/null || true

echo "Launching desktop app..."
nohup "$APP_BIN" >/dev/null 2>&1 </dev/null &
disown $! 2>/dev/null || true

echo "Done. Logs: $LOG_DIR/autoipo.log"
