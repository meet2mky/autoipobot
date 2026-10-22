# autoipo

`autoipo` is a small Go daemon for a MacBook Air that polls Telegram and sends a startup notice plus a resume notice after a sleep or network gap longer than 3 minutes. It listens for `/ping` and `/status` and replies with an ACK, while ignoring stale commands older than 5 minutes unless they are `/ping` or `/status`.

The desktop app, daemon, and deployment script target macOS only. The app uses macOS process tools and file locking; building or deploying on Windows or Linux is unsupported.

## Build

```bash
export PATH="/opt/homebrew/bin:$PATH"
mkdir -p bin
(cd daemon && go vet ./... && go build -o ../bin/autoipo .)
(cd app && go vet ./... && go build -o ../bin/autoipoapp .)
(cd chrome-extension-cli && go vet ./... && go build -o ../bin/chrome-extension-cli .)
```

## Run

```bash
export TG_TOKEN="<TOKEN>"
export TG_CHAT_ID="<CHAT_ID>"
export HOST_LABEL="MacBook-Air"
./bin/autoipo
```

Do not print or commit the Telegram token, chat ID, or user records. The app stores its private configuration in `.data/env` and user records in `.data/users`; both are excluded by `.gitignore`. Deployment can also read Telegram settings from `.data/env` or the environment.

Runtime logs are created in `daemon/logs/autoipo.log`, separate from the generated binaries in `bin/`.

## Chrome website opener extension

The unpacked extension in `chrome-extension/` opens in Chrome's side panel and stays open when switching tabs. Click its toolbar icon to show or hide it. The panel can save the website URL, open it in a new tab, scan visible clickable/fillable page elements, fill a selected field, click a selected element, check the CLI connection, and show recent activity. Opening a website from the panel makes its tab the target and active tab. The side panel shows the target tab ID, and page actions always use that same tab ID; they do not fall back to another tab. If the target tab is closed, page actions stop until a fresh tab is opened from the panel. The extension requests access to all HTTP and HTTPS sites at install time. Field values are not read by the scanner, persisted, or logged. Clicks are standard programmatic DOM clicks, not simulated physical input. Its toolbar badge indicates connecting (amber), connected (green), or a scheduled retry (red). Connection failures and retry details appear in recent activity; it retries every five seconds, with a 30-second alarm fallback if the service worker is suspended. To install it, open `chrome://extensions`, enable Developer mode, select **Load unpacked**, and choose this repository's `chrome-extension` folder. Reload the extension after updating its files or permissions.

### CLI control

Build and start the local bridge:

```bash
mkdir -p bin
cd chrome-extension-cli
go build -o ../bin/chrome-extension-cli .
../bin/chrome-extension-cli serve
```

Keep `serve` running while issuing commands in another terminal:

```bash
../bin/chrome-extension-cli set-url https://example.com/
../bin/chrome-extension-cli open
../bin/chrome-extension-cli list-selectors
../bin/chrome-extension-cli page-contains '#kc-login'
```

`set-url` updates the extension's saved URL; `open` asks the extension to open that saved URL in a new tab. Chrome must be open with the extension installed. No token or pairing step is needed. The extension keeps a WebSocket connection to the local CLI bridge, which is bound only to `127.0.0.1`; browser connections are accepted only from a Chrome extension origin. Other local programs running as your user can also access the bridge.

The desktop app has a **Commands** pane for these CLI actions. `deploy.sh` freshly builds the daemon and `chrome-extension-cli`, then embeds both helpers in the desktop app binary. On launch, the app installs those bundled helpers into `bin/`; it does not need Go installed to rebuild the CLI. Start **serve** from the top card, then use the separate cards for `set-url`, `open`, `click`, `fill`, `dropdown`, `choose`, `list-selectors`, `page-contains`, `random-sleep`, and `help`. `list-selectors` scans the target page and returns the selectors plus the same kind, label, description, and context metadata shown by the extension's page scan. `page-contains <selector>` returns `true` when the target page contains a matching CSS selector and `false` otherwise. The dropdown and choose cards share the selector and loaded choices. The app stops a bridge process it started when the app exits.

The **Flows** pane is a finite-state graph builder. New flows contain required **Start**, **Success**, and **Terminal** states; add **Command** and **Qualifier** states and connect them using target selectors. Graphs may be saved while disconnected or incomplete; playback routes a missing transition to **Terminal**. A command state runs one CLI command and follows its next state on success; command errors end at **Terminal**. A qualifier expression supports `pageContains("#selector")`, `AND`, `OR`, `NOT`, parentheses, and `true`/`false`. For example: `pageContains("#kc-login") AND NOT pageContains(".dashboard")`. A true result follows the qualifier's success target. A false result is retried according to that qualifier's retry count (default `0`), then follows its fallback target; an unset target leads to **Terminal**. Each flow has a unique ID and stores its run logs in `.data/flows/<flow-id>/runs/`; set how many recent logs to retain in the builder. Define values with lines such as `@username=myusername`, then use `@username` in command arguments or qualifier expressions. Playback prompts for referenced values, prefilled from definitions, and substitutes them for that run. Quote a value when it contains spaces, for example `@display_name="My name"`.

## Clean deploy (recommended)

```bash
cd /Users/mohityadav/Desktop/autoipobot
TG_TOKEN="<TOKEN>" \
TG_CHAT_ID="<CHAT_ID>" \
HOST_LABEL="MyMac" \
./deploy.sh
```

This is the clean, repeatable install path for macOS. It stops stale app and daemon processes, freshly builds the daemon and extension CLI, embeds those builds in the desktop app, then starts the daemon and app. The standalone helper builds are also placed in `bin/`. The presence of `.data/deploy.lock` prevents overlapping deploys; the script removes it on normal exit or handled signals. If the script is force-killed, remove the stale lock file before retrying.

To stop or restart the daemon after deployment, use the controls in the desktop app.
