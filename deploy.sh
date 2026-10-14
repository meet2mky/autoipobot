#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$REPO_ROOT/deploy"
LOG_DIR="$REPO_ROOT/daemon/logs"
TEMPLATE_PATH="$DEPLOY_DIR/com.autoipo.plist.example"
TARGET_PLIST_PATH="$DEPLOY_DIR/com.autoipo.plist"
TARGET_LAUNCHAGENT="$HOME/Library/LaunchAgents/com.autoipo.plist"
WINDOWS_RUNNER="$DEPLOY_DIR/autoipo-run.cmd"
WINDOWS_TASK_NAME="autoipo"
DAEMON_BIN="$REPO_ROOT/daemon/autoipo"
WINDOWS_DAEMON_BIN="$REPO_ROOT/daemon/autoipo.exe"
LOG_PATH="$LOG_DIR/autoipo.log"
PLATFORM="$(uname -s 2>/dev/null || echo unknown)"

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

echo "Building daemon binary..."
if [[ "$PLATFORM" == "Darwin" ]]; then
  (cd "$REPO_ROOT/daemon" && export PATH="/opt/homebrew/bin:$PATH" && go build -o autoipo .)
elif [[ "$PLATFORM" =~ ^(MINGW|MSYS|CYGWIN|Windows_NT) ]]; then
  (cd "$REPO_ROOT/daemon" && go build -o autoipo.exe .)
else
  echo "Unsupported platform: $PLATFORM" >&2
  exit 1
fi

mkdir -p "$DEPLOY_DIR" "$LOG_DIR"

if [[ "$PLATFORM" == "Darwin" ]]; then
  cat > "$TARGET_PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.autoipo</string>
    <key>ProgramArguments</key>
    <array>
        <string>$DAEMON_BIN</string>
    </array>
    <key>WorkingDirectory</key>
    <string>$REPO_ROOT/daemon</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>TG_TOKEN</key>
        <string>${TG_TOKEN}</string>
        <key>TG_CHAT_ID</key>
        <string>${TG_CHAT_ID}</string>
        <key>HOST_LABEL</key>
        <string>${HOST_LABEL}</string>
    </dict>
</dict>
</plist>
EOF

  if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo "Dry run enabled; not installing LaunchAgent."
    exit 0
  fi

  mkdir -p "$(dirname "$TARGET_LAUNCHAGENT")"

  if [[ -f "$TARGET_LAUNCHAGENT" ]] || launchctl print gui/"$(id -u)" com.autoipo >/dev/null 2>&1; then
    echo "Stopping previous autoipo LaunchAgent..."
    launchctl bootout gui/"$(id -u)" "$TARGET_LAUNCHAGENT" 2>/dev/null || true
    launchctl unload -w "$TARGET_LAUNCHAGENT" 2>/dev/null || true
    launchctl remove gui/"$(id -u)"/com.autoipo >/dev/null 2>&1 || true
  fi

  while read -r pid; do
    kill "$pid" 2>/dev/null || true
  done < <(ps -axo pid,command | awk '$2 ~ /autoipo/ {print $1}')

  if [[ -f "$TARGET_LAUNCHAGENT" ]]; then
    rm -f "$TARGET_LAUNCHAGENT"
  fi

  rm -f "$LOG_PATH"

  cp "$TARGET_PLIST_PATH" "$TARGET_LAUNCHAGENT"
  launchctl bootstrap gui/"$(id -u)" "$TARGET_LAUNCHAGENT"
  launchctl kickstart -k gui/"$(id -u)"/com.autoipo

  echo "autoipo installed successfully on macOS."
  exit 0
fi

if [[ "$PLATFORM" =~ ^(MINGW|MSYS|CYGWIN|Windows_NT) ]]; then
  cat > "$WINDOWS_RUNNER" <<EOF
@echo off
set TG_TOKEN=${TG_TOKEN}
set TG_CHAT_ID=${TG_CHAT_ID}
set HOST_LABEL=${HOST_LABEL}
"%~dp0..\daemon\autoipo.exe"
EOF

  if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo "Dry run enabled; not installing Windows scheduled task."
    exit 0
  fi

  if command -v schtasks >/dev/null 2>&1; then
    taskkill /F /IM autoipo.exe 2>/dev/null || true
    schtasks /Delete /TN "$WINDOWS_TASK_NAME" /F 2>/dev/null || true
    schtasks /Create /TN "$WINDOWS_TASK_NAME" /SC ONLOGON /RL HIGHEST /TR "\"$WINDOWS_RUNNER\"" /F
    echo "autoipo installed successfully on Windows."
    exit 0
  fi

  echo "schtasks is required on Windows to install the auto-start task." >&2
  exit 1
fi

echo "Unsupported platform: $PLATFORM" >&2
exit 1
