#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$REPO_ROOT/deploy"
TEMPLATE_PATH="$DEPLOY_DIR/com.autoipobot.plist.example"
TARGET_PLIST_PATH="$DEPLOY_DIR/com.autoipobot.plist"
TARGET_LAUNCHAGENT="$HOME/Library/LaunchAgents/com.autoipobot.plist"
DAEMON_BIN="$REPO_ROOT/daemon/autoipobot"

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

if [[ ! -f "$DAEMON_BIN" ]]; then
  echo "Building daemon binary..."
  (cd "$REPO_ROOT/daemon" && export PATH="/opt/homebrew/bin:$PATH" && go build -o autoipobot .)
fi

mkdir -p "$DEPLOY_DIR"

cat > "$TARGET_PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.autoipobot</string>
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
    <key>StandardErrorPath</key>
    <string>/tmp/autoipobot.log</string>
    <key>StandardOutPath</key>
    <string>/tmp/autoipobot.log</string>
</dict>
</plist>
EOF

if [[ "${DRY_RUN:-0}" == "1" ]]; then
  echo "Dry run enabled; not installing LaunchAgent."
  exit 0
fi

if launchctl print gui/"$(id -u)" com.autoipobot >/dev/null 2>&1; then
  echo "Stopping previous autoipobot LaunchAgent..."
  launchctl bootout gui/"$(id -u)" com.autoipobot || true
fi

mkdir -p "$(dirname "$TARGET_LAUNCHAGENT")"

if [[ -f "$TARGET_LAUNCHAGENT" ]]; then
  rm -f "$TARGET_LAUNCHAGENT"
fi

cp "$TARGET_PLIST_PATH" "$TARGET_LAUNCHAGENT"
launchctl bootstrap gui/"$(id -u)" "$TARGET_LAUNCHAGENT"
launchctl kickstart -k gui/"$(id -u)"/com.autoipobot

echo "autoipobot installed successfully."
