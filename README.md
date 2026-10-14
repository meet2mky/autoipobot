# autoipo

`autoipo` is a small Go daemon for a MacBook Air that polls Telegram and sends a startup notice plus a resume notice after a sleep or network gap longer than 3 minutes. It listens for `/ping` and `/status` and replies with an ACK, while ignoring stale commands older than 5 minutes unless they are `/ping` or `/status`.

## Build

```bash
cd daemon
export PATH="/opt/homebrew/bin:$PATH"
go mod init autoipo  # only needed once if the module file is missing

gofmt -l .
go vet ./...
go build -o autoipo .
```

## Run

```bash
export TG_TOKEN="<TOKEN>"
export TG_CHAT_ID="<CHAT_ID>"
export HOST_LABEL="MacBook-Air"
cd daemon
./autoipo
```

Do not print or commit the Telegram token or chat ID. Keep values in the environment or a local `.env` file excluded by `.gitignore`.

Runtime logs are created automatically next to the installed binary in a `logs/autoipo.log` file, so there is no checked-in log file to manage.

## Clean deploy (recommended)

```bash
cd /Users/mohityadav/Desktop/autoipobot
TG_TOKEN="<TOKEN>" \
TG_CHAT_ID="<CHAT_ID>" \
HOST_LABEL="MyMac" \
./deploy.sh
```

This is the clean, repeatable install path for macOS. It rebuilds the daemon, removes any previous LaunchAgent state, kills stale daemon processes, clears the old log file, and reinstalls the service.

If you want to force a full reset manually before the script:

```bash
launchctl bootout gui/"$(id -u)" /Users/"$(whoami)"/Library/LaunchAgents/com.autoipo.plist 2>/dev/null || true
pkill -f "/Users/$(whoami)/Desktop/autoipobot/daemon/autoipo" 2>/dev/null || true
rm -f /Users/"$(whoami)"/Desktop/autoipobot/daemon/logs/autoipo.log 2>/dev/null || true
```

Then run the same `./deploy.sh` command again.

## LaunchAgent install

```bash
cp deploy/com.autoipo.plist.example deploy/com.autoipo.plist
# edit the plist with your real token, chat ID, and host label
launchctl bootstrap gui/"$(id -u)" deploy/com.autoipo.plist
```

To remove it later:

```bash
launchctl bootout gui/"$(id -u)" /Library/LaunchAgents/com.autoipo.plist
```
