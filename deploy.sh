#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM="$(uname -s 2>/dev/null || echo unknown)"
if [[ "$PLATFORM" != "Darwin" ]]; then
  echo "deploy.sh supports macOS only (detected: $PLATFORM)." >&2
  exit 1
fi

LOCK_FILE="$REPO_ROOT/.data/deploy.lock"
mkdir -p "$REPO_ROOT/.data"
if ! (umask 077; set -o noclobber; printf '%s\n' "$$" > "$LOCK_FILE") 2>/dev/null; then
  echo "Another deploy.sh invocation is running (lock file exists: $LOCK_FILE)." >&2
  exit 1
fi
trap 'rm -f "$LOCK_FILE"' EXIT
trap 'exit 1' HUP INT TERM

BIN_DIR="$REPO_ROOT/bin"
LOG_DIR="$REPO_ROOT/daemon/logs"
DAEMON_BIN="$BIN_DIR/autoipo"
CLI_BIN="$BIN_DIR/chrome-extension-cli"
APP_BIN="$BIN_DIR/autoipoapp"
LOG_PATH="$LOG_DIR/autoipo.log"
BUNDLE_DIR="$REPO_ROOT/app/bundled"
trap 'rm -rf "$BUNDLE_DIR"; rm -f "$LOCK_FILE"' EXIT

if [[ -f "$REPO_ROOT/.data/env" ]]; then
  while IFS='=' read -r key val || [[ -n "$key" ]]; do
    key="$(echo "$key" | tr -d ' \r\n')"
    val="$(echo "$val" | tr -d '\r\n')"
    [[ "$key" =~ ^#.*$ || -z "$key" ]] && continue
    if [[ -z "${!key:-}" ]]; then
      export "$key=$val"
    fi
  done < "$REPO_ROOT/.data/env"
fi

if [[ -z "${TG_TOKEN:-}" ]]; then
  printf 'Enter Telegram bot token: '
  read -r -s TG_TOKEN
  printf '\n'
fi

if [[ -z "${TG_CHAT_ID:-}" ]]; then
  printf 'Enter Telegram chat ID: '
  read -r -s TG_CHAT_ID
  printf '\n'
fi

if [[ -z "${HOST_LABEL:-}" ]]; then
  printf 'Enter host label: '
  read -r HOST_LABEL
  printf '\n'
fi

if [[ -z "${TG_TOKEN}" || -z "${TG_CHAT_ID}" || -z "${HOST_LABEL}" ]]; then
  echo "Missing required deployment values." >&2
  exit 1
fi

echo "Removing stale app state..."
rm -f "$REPO_ROOT/deploy/com.autoipo.plist" "$REPO_ROOT/deploy/com.autoipo.plist.example"
rmdir "$REPO_ROOT/deploy" 2>/dev/null || true

stop_processes() {
  local process_name="$1"
  local attempt

  pkill -TERM -x "$process_name" 2>/dev/null || true
  for attempt in {1..50}; do
    if ! pgrep -x "$process_name" >/dev/null 2>&1; then
      return
    fi
    sleep 0.1
  done

  pkill -KILL -x "$process_name" 2>/dev/null || true
}

# Stop the running app and daemon before replacing their binaries.
echo "Stopping running autoipo processes..."
stop_processes "autoipoapp"
stop_processes "autoipo"
stop_processes "autoipobot"

rm -f "$LOG_PATH"
rm -rf "$REPO_ROOT/app/.tmp" 2>/dev/null || true
rm -f "$REPO_ROOT/daemon/autoipo" "$REPO_ROOT/app/autoipoapp" "$REPO_ROOT/autoipoapp" "$REPO_ROOT/chrome-extension-cli/chrome-extension-cli" 2>/dev/null || true
rm -rf "$BUNDLE_DIR"

mkdir -p "$BIN_DIR" "$LOG_DIR" "$BUNDLE_DIR"

echo "Building daemon and extension CLI binaries..."
(cd "$REPO_ROOT/daemon" && export PATH="/opt/homebrew/bin:$PATH" && go build -o "$DAEMON_BIN" .)
(cd "$REPO_ROOT/chrome-extension-cli" && export PATH="/opt/homebrew/bin:$PATH" && go build -o "$CLI_BIN" .)
cp "$DAEMON_BIN" "$BUNDLE_DIR/autoipo"
cp "$CLI_BIN" "$BUNDLE_DIR/chrome-extension-cli"
chmod 755 "$BUNDLE_DIR/autoipo" "$BUNDLE_DIR/chrome-extension-cli"
(cd "$REPO_ROOT/app" && export PATH="/opt/homebrew/bin:$PATH" && go build -o "$APP_BIN" .)

if [[ "${DRY_RUN:-0}" == "1" ]]; then
  echo "Dry run enabled; daemon and desktop app were built but not started."
  exit 0
fi

echo "Starting autoipo..."
cd "$REPO_ROOT/daemon"
nohup env TG_TOKEN="$TG_TOKEN" TG_CHAT_ID="$TG_CHAT_ID" HOST_LABEL="$HOST_LABEL" "$DAEMON_BIN" >/dev/null 2>&1 </dev/null &
DAEMON_PID=$!
disown "$DAEMON_PID"
cd "$REPO_ROOT"
echo "Launching desktop app..."
"$APP_BIN" >/dev/null 2>&1 </dev/null &
APP_PID=$!
disown "$APP_PID"
exit 0
