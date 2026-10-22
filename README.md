# autoipo

`autoipo` automates IPO applications on bank portals from a MacBook and lets you drive it from Telegram. It has four parts that share one Go module:

| Part | Path | What it does |
|---|---|---|
| Daemon | `daemon/` → `bin/autoipo` | Long-polls Telegram, answers `/status`, runs and schedules flows, sends results with an end-of-run screenshot back to the chat. |
| Desktop app | `app/` → `bin/autoipoapp` | Fyne UI + menu-bar item: daemon control, live logs, manual browser commands, the flow builder, users and settings. |
| Extension bridge | `chrome-extension-cli/` → `bin/chrome-extension-cli` | Loopback HTTP/WebSocket bridge between Go programs and the Chrome extension, plus a CLI client. |
| Chrome extension | `chrome-extension/` | Side panel + service worker that performs page actions with trusted input through the DevTools protocol. |

Shared code lives in `internal/` (flow engine, bridge protocol/client, users, secrets, settings, Telegram client, daemon control, scheduling, paths).

```
Telegram ⇄ daemon ──┐
                    ├─► flow engine ─► bridge client ─HTTP─► chrome-extension-cli serve ─WS─► extension ─CDP─► bank tab
desktop app ────────┘
```

> macOS is the supported deployment target (`deploy.sh`, Keychain, menu-bar app). Everything except the Fyne UI also builds and tests on Linux, which is what CI uses.

## Quick start (macOS)

```bash
git clone <repo> autoipobot && cd autoipobot
./deploy.sh            # builds, asks for the Telegram token/chat ID on first run, starts daemon + app
```

Then load the extension: `chrome://extensions` → Developer mode → **Load unpacked** → choose `chrome-extension/`. Reload it after every update of that folder.

`deploy.sh` is idempotent: it stops running processes (via the daemon's PID file), runs `go vet`, builds the daemon and bridge, embeds both into the desktop app (`-tags bundled`), saves any `TG_TOKEN` / `TG_CHAT_ID` / `HOST_LABEL` passed in the environment, and starts the daemon and the app. Use `DRY_RUN=1` to only build.

## Build and test anywhere (Make reference)

`make` (or `make help`) lists every target. The ones you will use:

| Target | What it does |
|---|---|
| `make check` | Everything CI runs: `fmt-check` + `vet` + `test` + `extension-check` + `extension-test` |
| `make build` | `bin/autoipo`, `bin/chrome-extension-cli`, then `bin/autoipoapp` with both helpers embedded (`-tags bundled`) |
| `make deploy` | macOS: stop running processes, vet, build, save/prompt Telegram settings, start daemon + app (`./deploy.sh`) |
| `make test` / `make test-race` | `go test` for `internal/`, `daemon/`, `chrome-extension-cli/`, `app/internal/` (optionally with `-race`) |
| `make vet` | `go vet` of the native packages plus a `GOOS=js GOARCH=wasm` type-check of the Fyne app (`vet-app`) |
| `make fmt` / `make fmt-check` | Format all Go files / fail if anything is unformatted |
| `make extension-check` | `node --check` on every extension script (skipped if Node is not installed) |
| `make extension-test` | 130+ behavioural checks of the real service worker, side panel and options page in headless Chrome (`tests/extension/`; skipped if no Chrome) |
| `make bundle` | Copies built helpers into `app/bundled/` for the embed (used by `build`) |
| `make clean` | Removes `bin/`, `app/bundled`, `app/tmp` |
| `make all` | `check` then `build` |

Variables (environment or `make VAR=value …`):

| Variable | Used by | Meaning |
|---|---|---|
| `TG_TOKEN`, `TG_CHAT_ID`, `HOST_LABEL` | `deploy` | Saved via `bin/autoipo configure` when set (token → Keychain, the rest → `.data/env`). Not required: the first deploy prompts interactively; later deploys reuse the saved settings. |
| `DRY_RUN=1` | `deploy` | Build only, start nothing |
| `SKIP_VET=1` | `deploy` | Skip `go vet` before building |
| `VERSION` | `build`, `deploy` | Version string stamped into the binaries (default `git describe --tags --always --dirty`) |
| `GO` | all Go targets | Go toolchain to use (default `go`) |
| `CHROME` | `extension-test` | Chrome/Chromium binary (default: auto-detected) |

Typical workflows:

```bash
make check                                   # before committing
make build && ./bin/autoipo version          # local binaries
TG_TOKEN=… TG_CHAT_ID=… HOST_LABEL=MacBook make deploy   # first macOS deploy, non-interactive
make deploy                                  # every later redeploy
```

`go test ./...` from a fresh clone works for every package except the Fyne UI itself, which needs OpenGL/X11 headers to compile natively; `make vet-app` type-checks it with `GOOS=js GOARCH=wasm` instead, and the macOS CI job builds it for real. The app also builds without `-tags bundled`, in which case it uses whatever helpers are already in `bin/`.


## Configuration and data

All mutable state is under `.data/` (git-ignored, mode 0700):

| File | Contents |
|---|---|
| `env` | `TG_CHAT_ID`, `HOST_LABEL` (the token is **not** stored here) |
| `users` | user records without passwords (`users.bak` is the previous version) |
| `flows.json`, `flows/<id>/runs/*.log|jpg` | flows, run transcripts and end-of-run screenshots |
| `schedules.json` | scheduled runs |
| `bridge.token` | random session token required for `POST /command` on the bridge |
| `autoipo.pid`, `*.lock`, `daemon-status.json`, `last-run.json`, `telegram.offset` | runtime state |

Secrets (bot token, user passwords) are stored in the **macOS Keychain** (service `autoipo`) through the `security` tool, never on the command line. On other systems, or with `AUTOIPO_SECRETS=file`, they go to `.data/secrets.json` (0600). Tokens found in an old `.data/env` or passwords in an old `users` file are migrated automatically.

Environment overrides: `AUTOIPO_HOME` (repository root), `AUTOIPO_BRIDGE_PORT` (default 18765), `AUTOIPO_EXTENSION_ID` (pin the bridge to one extension ID), `AUTOIPO_SECRETS=file|keychain`, `TG_TOKEN` / `TG_CHAT_ID` / `HOST_LABEL` (take precedence over stored settings).

## Daemon

```bash
bin/autoipo              # run (single instance; writes .data/autoipo.pid)
bin/autoipo configure    # save TG_TOKEN/TG_CHAT_ID/HOST_LABEL from the environment
bin/autoipo check        # verify settings and the token (getMe)
bin/autoipo status       # daemon, bridge and last-run status
```

The daemon long-polls Telegram (30 s), persists its update offset, backs off on network errors, and logs to `daemon/logs/autoipo.log` with size-based rotation. It sends `<label> BOOT` on start and `<label> RESUMED after …` after a sleep/network gap longer than 3 minutes (detected with the wall clock, so closed-lid sleep counts). Commands older than 5 minutes are ignored except `/ping`, `/status`, `/help`. Only the configured chat is honoured.

Telegram commands:

| Command | Effect |
|---|---|
| `/ping` | liveness + uptime |
| `/status` | daemon uptime/last poll, bridge + extension connectivity, active run, last run result, pending schedules |
| `/flows`, `/users` | list flows / users (PANs masked, no credentials) |
| `/run <flow> [user]` | run a flow, optionally as a stored user (name or PAN); the result is posted back with a screenshot of the final page (on success and on failure) |
| `/stop` | cancel the running flow |
| `/schedule <flow> <user\|-> <time>` | schedule a run (`HH:MM`, `YYYY-MM-DD HH:MM`, `+15m`, `+2h`) |
| `/schedules`, `/unschedule <id>` | list / cancel scheduled runs |
| `/screenshot` | capture the target tab |
| `/help` | command list |

Only one flow runs at a time across the daemon and the app (a shared run lock).

## Desktop app

Closing the window hides it; the menu-bar item (Start/Stop/Restart daemon, Show window, Quit) stays available.

- **Status** — daemon (running, PID, uptime, last poll), bridge/extension connectivity, active run, last run, pending schedules; Start/Stop/Restart.
- **Commands** — talks to the bridge directly: bridge start/stop, set-url/open, click/fill/press-key/dropdown+choose, list-selectors/page-contains/read-text/screenshot, with a searchable output history. Manual commands run immediately (no artificial delay).
- **Flows** — graph builder with lint warnings (unreachable states, missing targets, invalid expressions), live run transcript with **Stop**, run logs, duplicate, schedule. **Play** lets you pick a stored user whose fields prefill `@name @pan @bank @user_id @password @email @phone`.
- **Users** — masked list with reveal/copy, add/edit/delete; passwords live in the Keychain.
- **Logs** — daemon log tail (follow + filter) and app events.
- **Settings** — host label, token, chat ID, **Test connection**, restart prompt.

### Flows

A flow is a finite-state graph: **Start → … → Success | Terminal**. Command states run one command (`serve`, `set-url`, `open`, `click`, `fill`, `dropdown`, `choose`, `press-key`, `read-text`, `list-selectors`, `page-contains`, `screenshot`, `random-sleep`) and follow their next state; a command error ends at Terminal. `read-text` can store its output as `@variable` ("save output as") for later states. Qualifier states evaluate an expression with retries:

```
pageContains("#kc-login") AND NOT pageContains(".dashboard")
waitFor("#otp-field", 20) OR true
```

Arguments and expressions may reference `@placeholders`; defaults are defined on the Start state as `@name=value` lines (quote values with spaces). Playback inserts a random 2–4 s pause between commands. Every run ends with a screenshot of the page (both Success and Terminal; cancelled runs skip it) saved next to the run log and delivered to Telegram as the result message. Run logs and screenshots are kept per flow (`runs to keep`) and the last result is shown in `/status` and the Status tab.

## Bridge and CLI

```bash
bin/chrome-extension-cli serve                      # keep running; binds 127.0.0.1:18765
bin/chrome-extension-cli set-url https://example.com/
bin/chrome-extension-cli open
bin/chrome-extension-cli list-selectors --json
bin/chrome-extension-cli page-contains '#kc-login'
bin/chrome-extension-cli screenshot --out shot.png
bin/chrome-extension-cli status
```

The bridge accepts WebSocket connections only from a `chrome-extension://` origin and requires the session token from `.data/bridge.token` for `POST /command` (the CLI, app and daemon read it automatically). Pending commands fail immediately when the extension disconnects. `--json` prints raw results.

## Chrome extension

The side panel can save/open the target URL, scan page elements (with a filter), fill/click/choose, show the target tab and recent activity, and release browser control. Actions use `chrome.debugger` (DevTools protocol) so clicks and keystrokes are trusted input; Chrome shows its "is debugging this browser" bar while a session is attached (released after 90 s idle, on tab close, or on demand). The options page configures the bridge port, typing speed (default 40–120 ms per character, or instant), default URL and the site-specific hints used by the scanner. Field values are never logged.

Behavioural notes: native `<select>` elements are chosen programmatically (value/label match, then `input` + `change` events) because the OS popup cannot be driven with synthetic keys; autocomplete dropdowns are clicked with trusted input. `press-key Enter`/`Space` are sent as `keyDown`+text so forms submit implicitly. The extension is version 2.0.0 (protocol v2) — reload it after updating.

`tests/extension/` contains a headless-Chrome harness that loads the real service worker, side panel and options page under a `chrome.*` stub and runs 130+ behavioural checks: `make extension-test` (needs Chrome/Chromium; `CHROME=/path/to/chrome` to override).

## Security notes

- Bank passwords and the bot token are in the Keychain; `.data/` contains no secrets except the local bridge session token.
- The bridge is loopback-only; other processes running as your user can still reach it (that is the same trust boundary as your Keychain and `.data`).
- Flow transcripts redact fill values and password-like placeholders. End-of-run screenshots can show whatever the bank page displays (account numbers, balances); they live in `.data/` (0600) and are posted to your Telegram chat.

## Troubleshooting

- *"another autoipo daemon instance is already running"* — `bin/autoipo status`; remove a stale `.data/autoipo.pid` only if the process is gone.
- *Bridge rejected the request (401)* — the bridge was started with a different `.data/bridge.token`; restart `serve` from the app.
- *Extension not connected* — reload the extension, check the options page port matches `AUTOIPO_BRIDGE_PORT`.
- *Keychain prompts or errors* — run with `AUTOIPO_SECRETS=file` to fall back to the encrypted-at-rest-by-FileVault file store.
