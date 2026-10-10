# autoipobot

`autoipobot` is a small Go daemon for a MacBook Air that polls Telegram and sends a startup notice plus a resume notice after a sleep or network gap longer than 3 minutes. It listens for `/ping` and `/status` and replies with an ACK, while ignoring stale commands older than 5 minutes unless they are `/ping` or `/status`.

## Build

```bash
cd daemon
export PATH="/opt/homebrew/bin:$PATH"
go mod init autoipobot  # only needed once if the module file is missing

gofmt -l .
go vet ./...
go build -o autoipobot .
```

## Run

```bash
export TG_TOKEN="<TOKEN>"
export TG_CHAT_ID="<CHAT_ID>"
export HOST_LABEL="MacBook-Air"
cd daemon
./autoipobot
```

Do not print or commit the Telegram token or chat ID. Keep values in the environment or a local `.env` file excluded by `.gitignore`.

## LaunchAgent install

```bash
cp deploy/com.autoipobot.plist.example deploy/com.autoipobot.plist
# edit the plist with your real token, chat ID, and host label
launchctl bootstrap gui/"$(id -u)" deploy/com.autoipobot.plist
```

To remove it later:

```bash
launchctl bootout gui/"$(id -u)" /Library/LaunchAgents/com.autoipobot.plist
```
