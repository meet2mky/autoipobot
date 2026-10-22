// AutoIPO Browser Bridge — background service worker.
//
// Sections, in file order:
//   1. Constants and in-memory state
//   2. Activity log (the only writer of storage.local.commandLogs)
//   3. Settings migration (storage.sync → storage.local)
//   4. Connection manager (health check → WebSocket, reconnect, badge)
//   5. Message routers (bridge commands and side panel / options messages)
//   6. Target tab tracking
//   7. Debugger session (attach once per tab, idle detach, serialised actions)
//   8. Automation actions and trusted input helpers
//   9. Functions injected into the target page
//  10. Event registration and bootstrap

importScripts("shared.js", "page-scan.js");

// ---------------------------------------------------------------------------
// 1. Constants and in-memory state
// ---------------------------------------------------------------------------

const RECONNECT_ALARM = "bridge-reconnect";
const DEBUGGER_IDLE_ALARM = "debugger-idle";
const RECONNECT_DELAY_MS = 5000;
const RECONNECT_ALARM_DELAY_MINUTES = 0.5;
const MIN_ALARM_DELAY_MINUTES = 0.5;
const DEBUGGER_IDLE_MS = 90000;
const FAILURE_LOG_INTERVAL_MS = 60000;
const HEALTH_CHECK_TIMEOUT_MS = 2000;
const WS_HANDSHAKE_TIMEOUT_MS = 10000;
const TARGET_TAB_LOAD_TIMEOUT_MS = 30000;
const ELEMENT_WAIT_TIMEOUT_MS = 15000;
const AUTOCOMPLETE_WAIT_TIMEOUT_MS = 4000;
const POLL_INTERVAL_MS = 250;
const MAX_LOG_ENTRIES = 50;
const MAX_LOG_MESSAGE_LENGTH = 500;
const MODIFIER_CTRL = 2;
const MODIFIER_META = 4;

const BRIDGE_COMMAND_TYPES = new Set([
  "set_url", "open", "click", "fill", "dropdown", "choose",
  "list_selectors", "page_contains", "press_key", "read_text", "screenshot",
]);
const SELECTOR_ACTIONS = new Set(["click", "fill", "dropdown", "choose", "page_contains", "read_text"]);
const RETRYABLE_ELEMENT_ERRORS = new Set([
  "No field matches that selector.",
  "That element is not visible.",
  "That element cannot be interacted with.",
]);

const BADGES = Object.freeze({
  connecting: { text: "...", color: "#b7791f", title: "AutoIPO: connecting to the bridge" },
  connected: { text: "✓", color: "#16834a", title: "AutoIPO: connected to the bridge" },
  retrying: { text: "!", color: "#c0392b", title: "AutoIPO: bridge unavailable; retrying shortly" },
});

// Named keys accepted by press_key. `text` marks keys that must also produce a
// char event (keypress) so that implicit form submission and space activation
// behave as they do for a physical key press.
const KEY_DEFINITIONS = Object.freeze({
  Enter: { name: "Enter", key: "Enter", code: "Enter", keyCode: 13, text: "\r" },
  Tab: { name: "Tab", key: "Tab", code: "Tab", keyCode: 9 },
  Escape: { name: "Escape", key: "Escape", code: "Escape", keyCode: 27 },
  Backspace: { name: "Backspace", key: "Backspace", code: "Backspace", keyCode: 8 },
  ArrowUp: { name: "ArrowUp", key: "ArrowUp", code: "ArrowUp", keyCode: 38 },
  ArrowDown: { name: "ArrowDown", key: "ArrowDown", code: "ArrowDown", keyCode: 40 },
  ArrowLeft: { name: "ArrowLeft", key: "ArrowLeft", code: "ArrowLeft", keyCode: 37 },
  ArrowRight: { name: "ArrowRight", key: "ArrowRight", code: "ArrowRight", keyCode: 39 },
  Space: { name: "Space", key: " ", code: "Space", keyCode: 32, text: " " },
  Home: { name: "Home", key: "Home", code: "Home", keyCode: 36 },
  End: { name: "End", key: "End", code: "End", keyCode: 35 },
});
const KEY_ALIASES = Object.freeze({
  esc: "Escape", return: "Enter", up: "ArrowUp", down: "ArrowDown", left: "ArrowLeft", right: "ArrowRight", spacebar: "Space",
});
const KEY_LOOKUP = new Map();
for (const definition of Object.values(KEY_DEFINITIONS)) KEY_LOOKUP.set(definition.name.toLowerCase(), definition);
for (const [alias, name] of Object.entries(KEY_ALIASES)) KEY_LOOKUP.set(alias, KEY_DEFINITIONS[name]);

let bridgeSocket = null;
let connectPromise = null;
let reconnectRequested = false;
let reconnectTimer = 0;
let lastBridgeState = "";
let lastBridgeStateKey = "";
let initializePromise = null;
let logQueue = Promise.resolve();
let tabStateQueue = Promise.resolve();
let automationQueue = Promise.resolve();
let automationActive = false;
const debuggerSession = { tabId: null, lastUsedAt: 0, adopted: false };

function errorMessage(error) {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  if (error && typeof error.message === "string") return error.message;
  return String(error);
}

/** Returns a rejection handler that logs instead of crashing the worker. */
function logFailure(context) {
  return (error) => console.warn(`Could not ${context}:`, errorMessage(error));
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function randomBetween(min, max) {
  return min + Math.floor(Math.random() * (max - min + 1));
}

function isHttpUrl(url) {
  return /^https?:/i.test(url || "");
}

function isInternalUrl(url) {
  return /^(chrome|chrome-extension|devtools|edge|view-source):/i.test(url || "");
}

function hostOf(url) {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

// ---------------------------------------------------------------------------
// 2. Activity log
// ---------------------------------------------------------------------------

/** Appends an entry to commandLogs (newest first, capped at MAX_LOG_ENTRIES). */
function recordEvent(message) {
  const text = String(message).slice(0, MAX_LOG_MESSAGE_LENGTH);
  return enqueueLogWrite((commandLogs) => {
    commandLogs.unshift({ message: text, time: new Date().toISOString() });
    return commandLogs.slice(0, MAX_LOG_ENTRIES);
  });
}

function clearEventLog() {
  return enqueueLogWrite(() => []);
}

function enqueueLogWrite(mutate) {
  logQueue = logQueue
    .then(async () => {
      const { commandLogs } = await chrome.storage.local.get({ commandLogs: [] });
      const next = mutate(Array.isArray(commandLogs) ? commandLogs : []);
      await chrome.storage.local.set({ commandLogs: next });
    })
    .catch(logFailure("update the activity log"));
  return logQueue;
}

// ---------------------------------------------------------------------------
// 3. Settings migration
// ---------------------------------------------------------------------------

/** Moves websiteUrl from storage.sync to storage.local once, then drops the sync copy. */
async function migrateLegacySettings() {
  try {
    const { websiteUrl } = await chrome.storage.local.get({ websiteUrl: "" });
    if (!websiteUrl) {
      const legacy = await chrome.storage.sync.get({ websiteUrl: "" });
      if (typeof legacy.websiteUrl === "string" && legacy.websiteUrl) {
        await chrome.storage.local.set({ websiteUrl: legacy.websiteUrl });
        await recordEvent("Moved the saved website URL from synced to local storage");
      }
    }
    await chrome.storage.sync.remove("websiteUrl");
  } catch (error) {
    console.warn("Could not migrate legacy settings:", errorMessage(error));
  }
}

// ---------------------------------------------------------------------------
// 4. Connection manager
// ---------------------------------------------------------------------------

/**
 * Connects to the bridge unless a connection is open or in progress. With
 * {force: true} the current socket is closed first (used after a port change
 * or an explicit reconnect request).
 */
function connectBridge(options = {}) {
  const force = options.force === true;
  if (connectPromise) {
    if (force) reconnectRequested = true;
    return connectPromise;
  }
  if (force) {
    closeBridgeSocket("reconnecting");
  } else if (bridgeSocket && (bridgeSocket.readyState === WebSocket.OPEN || bridgeSocket.readyState === WebSocket.CONNECTING)) {
    return Promise.resolve();
  }
  connectPromise = establishBridgeConnection()
    .catch((error) => {
      console.warn("Bridge connection attempt failed:", errorMessage(error));
      scheduleReconnect();
    })
    .finally(() => {
      connectPromise = null;
      if (reconnectRequested) {
        reconnectRequested = false;
        void connectBridge({ force: true });
      }
    });
  return connectPromise;
}

async function establishBridgeConnection() {
  clearReconnectTimers();
  if (lastBridgeState !== "retrying") await setBridgeState("connecting", "Connecting to the AutoIPO bridge…");
  const port = await getBridgePort();
  const health = await checkBridgeHealth(port);
  if (!health.available) {
    await setBridgeState("retrying", `Bridge health check on port ${port} failed (${health.detail}); retrying in 5 seconds.`);
    scheduleReconnect();
    return;
  }
  openBridgeSocket(port);
}

async function getBridgePort() {
  try {
    const { bridgePort } = await chrome.storage.local.get({ bridgePort: AUTOIPO_DEFAULTS.bridgePort });
    return normalizeBridgePort(bridgePort);
  } catch (error) {
    console.warn("Could not read the bridge port:", errorMessage(error));
    return AUTOIPO_DEFAULTS.bridgePort;
  }
}

async function checkBridgeHealth(port) {
  try {
    const response = await fetch(`http://127.0.0.1:${port}/health`, {
      cache: "no-store",
      signal: AbortSignal.timeout(HEALTH_CHECK_TIMEOUT_MS),
    });
    if (response.ok) return { available: true, detail: "" };
    const body = (await response.text()).trim().slice(0, 120);
    return { available: false, detail: `/health returned HTTP ${response.status}${body ? `: ${body}` : ""}` };
  } catch (error) {
    // A refused loopback probe is expected while the bridge is stopped.
    return { available: false, detail: errorMessage(error) || "loopback request failed" };
  }
}

function openBridgeSocket(port) {
  let socket;
  try {
    socket = new WebSocket(`ws://127.0.0.1:${port}/ws`);
  } catch (error) {
    void setBridgeState("retrying", `Could not open a WebSocket to port ${port} (${errorMessage(error)}); retrying in 5 seconds.`);
    scheduleReconnect();
    return;
  }
  bridgeSocket = socket;
  // A handshake that never completes would otherwise block retries forever.
  const handshakeTimer = setTimeout(() => {
    if (bridgeSocket === socket && socket.readyState === WebSocket.CONNECTING) socket.close();
  }, WS_HANDSHAKE_TIMEOUT_MS);
  socket.addEventListener("open", () => {
    clearTimeout(handshakeTimer);
    if (bridgeSocket !== socket) return;
    clearReconnectTimers();
    void setBridgeState("connected", `Connected to the AutoIPO bridge on port ${port}`);
  });
  socket.addEventListener("message", (event) => {
    if (bridgeSocket !== socket) return;
    handleBridgeMessage(socket, event.data).catch(logFailure("handle a bridge message"));
  });
  socket.addEventListener("close", (event) => {
    clearTimeout(handshakeTimer);
    if (bridgeSocket !== socket) return;
    bridgeSocket = null;
    const reason = event.reason ? `: ${event.reason}` : "";
    void setBridgeState("retrying", `Bridge disconnected (code ${event.code}${reason}); retrying in 5 seconds.`);
    scheduleReconnect();
  });
  socket.addEventListener("error", () => {
    if (bridgeSocket === socket) socket.close();
  });
}

function closeBridgeSocket(reason) {
  const socket = bridgeSocket;
  bridgeSocket = null;
  if (!socket) return;
  try {
    socket.close(1000, reason);
  } catch (error) {
    console.warn("Could not close the bridge socket:", errorMessage(error));
  }
}

function sendToBridge(socket, payload) {
  if (socket.readyState !== WebSocket.OPEN) return;
  try {
    socket.send(JSON.stringify(payload));
  } catch (error) {
    console.warn("Could not send to the bridge:", errorMessage(error));
  }
}

/** Retries in 5 s while the worker is alive, with an alarm fallback if it is suspended. */
function scheduleReconnect() {
  clearTimeout(reconnectTimer);
  reconnectTimer = setTimeout(() => {
    void connectBridge();
  }, RECONNECT_DELAY_MS);
  chrome.alarms.create(RECONNECT_ALARM, { delayInMinutes: RECONNECT_ALARM_DELAY_MINUTES }).catch(logFailure("schedule the reconnect alarm"));
}

function clearReconnectTimers() {
  clearTimeout(reconnectTimer);
  reconnectTimer = 0;
  chrome.alarms.clear(RECONNECT_ALARM).catch(logFailure("clear the reconnect alarm"));
}

/** Publishes the connection state to storage and the toolbar badge, logging transitions. */
async function setBridgeState(state, detail) {
  const key = `${state}|${detail}`;
  if (key !== lastBridgeStateKey) {
    lastBridgeStateKey = key;
    lastBridgeState = state;
    const badge = BADGES[state];
    try {
      await chrome.storage.local.set({ bridgeState: state, bridgeConnected: state === "connected", bridgeDetail: detail });
      await chrome.action.setBadgeText({ text: badge.text });
      await chrome.action.setBadgeBackgroundColor({ color: badge.color });
      await chrome.action.setTitle({ title: badge.title });
    } catch (error) {
      console.warn("Could not publish the bridge state:", errorMessage(error));
    }
  }
  await logBridgeState(state, detail);
}

async function logBridgeState(state, detail) {
  try {
    if (state === "connected") {
      await chrome.storage.local.set({ lastBridgeFailureLogAt: 0 });
      await recordEvent(detail);
      return;
    }
    if (state !== "retrying") return;
    const { lastBridgeFailureLogAt } = await chrome.storage.local.get({ lastBridgeFailureLogAt: 0 });
    if (Date.now() - lastBridgeFailureLogAt < FAILURE_LOG_INTERVAL_MS) return;
    await chrome.storage.local.set({ lastBridgeFailureLogAt: Date.now() });
    await recordEvent(detail);
  } catch (error) {
    console.warn("Could not log the bridge state:", errorMessage(error));
  }
}

// ---------------------------------------------------------------------------
// 5. Message routers
// ---------------------------------------------------------------------------

/** Handles one WebSocket frame from the bridge (protocol v2). */
async function handleBridgeMessage(socket, rawMessage) {
  let message;
  try {
    message = JSON.parse(rawMessage);
  } catch {
    message = null;
  }
  if (!message || typeof message !== "object" || typeof message.type !== "string") {
    await recordEvent("Received an invalid bridge message");
    return;
  }
  if (message.type === "heartbeat") {
    sendToBridge(socket, { type: "heartbeat_ack" });
    return;
  }
  await recordEvent(`[Request from CLI bridge] -> ${formatBridgeCommand(message)}`);
  const ack = { type: "ack", id: typeof message.id === "string" ? message.id : "" };
  Object.assign(ack, await executeBridgeCommand(message));
  await recordEvent(`[Response from Extension worker] <- ${summarizeAck(message, ack)}`);
  sendToBridge(socket, ack);
}

async function executeBridgeCommand(message) {
  try {
    if (!BRIDGE_COMMAND_TYPES.has(message.type)) throw new Error(`Unsupported command: ${message.type}`);
    // set_url only writes a setting; it is serialised but does not show the busy banner.
    const result = await enqueueAutomation(() => runBridgeCommand(message), { fromBridge: message.type !== "set_url" });
    return { ok: true, ...result };
  } catch (error) {
    return { ok: false, error: errorMessage(error) };
  }
}

async function runBridgeCommand(message) {
  switch (message.type) {
    case "set_url": {
      const parsed = parseWebsiteUrl(message.url);
      if (parsed.error) throw new Error(parsed.error);
      await chrome.storage.local.set({ websiteUrl: parsed.href });
      return { output: `saved ${parsed.href}` };
    }
    case "open": {
      const tab = await openWebsiteTab();
      return { output: `opened tab ${tab.id}`, tab_id: tab.id };
    }
    default: {
      const result = await runAutomationAction({
        action: message.type,
        selector: message.selector,
        value: message.value,
        key: message.key,
      });
      return toAckFields(result);
    }
  }
}

/** Maps an internal action result onto the protocol v2 ack fields. */
function toAckFields(result) {
  const fields = { tab_id: result.tabId };
  if (typeof result.output === "string") fields.output = result.output;
  if (result.elements) {
    fields.elements = result.elements;
    fields.output = `${result.total} interactive elements found`;
  }
  if (result.choices) {
    fields.choices = result.choices;
    fields.output = `${result.choices.length} choices`;
  }
  if (result.screenshot) fields.screenshot = result.screenshot;
  return fields;
}

/** Renders a bridge command for the activity log; fill values are never included. */
function formatBridgeCommand(message) {
  const quote = (value) => JSON.stringify(typeof value === "string" ? value : "");
  switch (message.type) {
    case "set_url":
      return `set-url ${quote(message.url)}`;
    case "fill":
      return `fill ${quote(message.selector)} [value redacted]`;
    case "choose":
      return `choose ${quote(message.selector)} ${quote(message.value)}`;
    case "click":
    case "dropdown":
    case "page_contains":
    case "read_text":
      return `${message.type.replaceAll("_", "-")} ${quote(message.selector)}`;
    case "press_key":
      return `press-key ${quote(message.key)}`;
    case "open":
    case "list_selectors":
    case "screenshot":
      return message.type.replaceAll("_", "-");
    default:
      return message.type || "unknown";
  }
}

function summarizeAck(message, ack) {
  if (!ack.ok) return `FAIL ${ack.error}`;
  if (message.type === "screenshot") return `OK screenshot captured (${Math.round((ack.screenshot || "").length * 0.75 / 1024)} KB)`;
  if (message.type === "read_text") return `OK read ${(ack.output || "").length} characters`;
  return ack.output ? `OK ${ack.output}` : "OK";
}

/** Handles messages from the side panel and the options page. */
async function handleRuntimeMessage(message) {
  switch (message.type) {
    case "wake_bridge":
      void connectBridge();
      return { ok: true };
    case "reconnect_bridge":
      await recordEvent("Reconnect requested from the extension UI");
      void connectBridge({ force: true });
      return { ok: true };
    case "get_target_tab": {
      if (message.waitForLoad === true) await waitForTargetTab();
      const snapshot = await reconcileTargetTabs();
      return { ok: true, tab: snapshot.tab, tabs: snapshot.tabs };
    }
    case "select_target_tab":
      await selectTargetTab(message.tabId);
      return { ok: true };
    case "open_website": {
      const tab = await openWebsiteTab(message.url);
      return { ok: true, tabId: tab.id };
    }
    case "automation_action": {
      const result = await enqueueAutomation(() => runAutomationAction(message), { fromBridge: false });
      return { ok: true, ...result };
    }
    case "record_event":
      if (typeof message.message === "string" && message.message.trim()) await recordEvent(message.message.trim());
      return { ok: true };
    case "clear_logs":
      await clearEventLog();
      return { ok: true };
    case "release_debugger":
      await enqueueAutomation(() => releaseDebugger("released from the side panel"), { fromBridge: false });
      return { ok: true };
    default:
      return { ok: false, error: `Unknown message type: ${message.type}` };
  }
}

// ---------------------------------------------------------------------------
// 6. Target tab tracking
// ---------------------------------------------------------------------------

async function readTabState() {
  const stored = await chrome.storage.local.get({ targetTabId: null, targetTabUrl: "", targetTabs: [] });
  const targetTabs = Array.isArray(stored.targetTabs) ? stored.targetTabs : [];
  return {
    targetTabId: Number.isInteger(stored.targetTabId) ? stored.targetTabId : null,
    targetTabUrl: typeof stored.targetTabUrl === "string" ? stored.targetTabUrl : "",
    targetTabs: targetTabs
      .filter((entry) => entry && Number.isInteger(entry.id))
      .map((entry) => ({
        id: entry.id,
        url: typeof entry.url === "string" ? entry.url : "",
        title: typeof entry.title === "string" ? entry.title : "",
      })),
  };
}

/**
 * Runs a read-modify-write of the target tab bookkeeping. Mutations are
 * serialised so concurrent tab events cannot lose updates; storage is only
 * written when the state actually changed. Resolves with the final state.
 */
function updateTabState(mutator) {
  const run = tabStateQueue.then(async () => {
    const state = await readTabState();
    const before = JSON.stringify(state);
    await mutator(state);
    if (JSON.stringify(state) !== before) await persistTabState(state);
    return state;
  });
  tabStateQueue = run.then(() => undefined, () => undefined);
  return run;
}

async function persistTabState(state) {
  if (state.targetTabId === null) {
    await chrome.storage.local.set({ targetTabs: state.targetTabs });
    await chrome.storage.local.remove(["targetTabId", "targetTabUrl"]);
    return;
  }
  await chrome.storage.local.set({ targetTabs: state.targetTabs, targetTabId: state.targetTabId, targetTabUrl: state.targetTabUrl });
}

function registerTabInState(state, tab, url, select) {
  state.targetTabs = state.targetTabs.filter((entry) => entry.id !== tab.id);
  state.targetTabs.push({ id: tab.id, url, title: tab.title || "" });
  if (select) {
    state.targetTabId = tab.id;
    state.targetTabUrl = url;
  }
}

function forgetTabInState(state, tabId) {
  state.targetTabs = state.targetTabs.filter((entry) => entry.id !== tabId);
  if (state.targetTabId !== tabId) return;
  const next = state.targetTabs[state.targetTabs.length - 1] || null;
  state.targetTabId = next ? next.id : null;
  state.targetTabUrl = next ? next.url : "";
}

function describeTabEntry(entry) {
  if (!entry) return null;
  return { id: entry.id, url: entry.url, title: entry.title, host: hostOf(entry.url) };
}

/** Drops closed or internal tabs, repairs the selection and returns the result. */
async function reconcileTargetTabs() {
  const openTabs = await chrome.tabs.query({});
  const openById = new Map(openTabs.map((tab) => [tab.id, tab]));
  const state = await updateTabState((draft) => {
    const managed = [];
    for (const entry of draft.targetTabs) {
      const current = openById.get(entry.id);
      if (!current) continue;
      const url = current.url || current.pendingUrl || entry.url;
      if (isInternalUrl(url)) continue;
      managed.push({ id: entry.id, url, title: current.title || entry.title });
    }
    const target = draft.targetTabId === null ? undefined : openById.get(draft.targetTabId);
    if (target && !managed.some((entry) => entry.id === target.id)) {
      const url = target.url || target.pendingUrl || draft.targetTabUrl;
      if (!isInternalUrl(url)) managed.push({ id: target.id, url, title: target.title || "" });
    }
    draft.targetTabs = managed;
    const selected = managed.find((entry) => entry.id === draft.targetTabId) || managed[managed.length - 1] || null;
    draft.targetTabId = selected ? selected.id : null;
    draft.targetTabUrl = selected ? selected.url : "";
  });
  const selected = state.targetTabs.find((entry) => entry.id === state.targetTabId) || null;
  return { tab: describeTabEntry(selected), tabs: state.targetTabs.map(describeTabEntry) };
}

/** Opens url (or the saved website URL) in a new tab and makes it the target tab. */
async function openWebsiteTab(requestedUrl) {
  let href;
  if (requestedUrl !== undefined) {
    const parsed = parseWebsiteUrl(requestedUrl);
    if (parsed.error) throw new Error(parsed.error);
    href = parsed.href;
    await chrome.storage.local.set({ websiteUrl: href });
  } else {
    const parsed = parseWebsiteUrl((await loadSettings()).websiteUrl);
    if (parsed.error) throw new Error(`The saved website URL is unusable: ${parsed.error}`);
    href = parsed.href;
  }
  const tab = await chrome.tabs.create({ url: href, active: true });
  await updateTabState((state) => {
    registerTabInState(state, tab, href, true);
  });
  await recordEvent(`Opened ${href} in tab ${tab.id}`);
  return tab;
}

async function selectTargetTab(tabId) {
  if (!Number.isInteger(tabId)) throw new Error("A tab id is required.");
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch {
    throw new Error(`Tab ${tabId} is no longer open.`);
  }
  const url = tab.url || tab.pendingUrl || "";
  if (isInternalUrl(url)) throw new Error(`Tab ${tabId} is not a regular website page.`);
  await updateTabState((state) => {
    registerTabInState(state, tab, url, true);
  });
  await chrome.tabs.update(tabId, { active: true });
  await recordEvent(`Selected tab ${tabId} as the target tab`);
}

/** Resolves the target tab once it has finished loading an http(s) page. */
async function waitForTargetTab() {
  const { targetTabId } = await readTabState();
  if (!Number.isInteger(targetTabId)) throw new Error("No target tab is open. Open a website from the AutoIPO panel first.");
  const deadline = Date.now() + TARGET_TAB_LOAD_TIMEOUT_MS;
  let wokeDiscardedTab = false;
  for (;;) {
    let tab;
    try {
      tab = await chrome.tabs.get(targetTabId);
    } catch {
      await updateTabState((state) => {
        forgetTabInState(state, targetTabId);
      });
      throw new Error(`Target tab ${targetTabId} was closed. Open a fresh website from the AutoIPO panel to continue.`);
    }
    const url = tab.url || tab.pendingUrl || "";
    if (tab.status !== "loading" && tab.status !== "unloaded" && isHttpUrl(url)) return tab;
    if (tab.status !== "loading" && url && url !== "about:blank" && !isHttpUrl(url)) {
      throw new Error(`Target tab ${tab.id} finished loading ${url}, which is not a regular website page.`);
    }
    if ((tab.discarded || tab.status === "unloaded") && !wokeDiscardedTab) {
      wokeDiscardedTab = true;
      await chrome.tabs.update(targetTabId, { active: true });
    }
    if (Date.now() >= deadline) throw new Error(`Target tab ${targetTabId} did not finish loading a website within 30 seconds.`);
    await sleep(POLL_INTERVAL_MS);
  }
}

// ---------------------------------------------------------------------------
// 7. Debugger session
// ---------------------------------------------------------------------------

/**
 * Attaches the DevTools debugger to tabId if it is not attached already.
 * A session is kept until it idles for DEBUGGER_IDLE_MS, the tab closes or
 * navigates away from http(s), or the user releases it.
 */
async function acquireDebugger(tabId) {
  if (debuggerSession.tabId !== null && debuggerSession.tabId !== tabId) {
    await releaseDebugger("switching to another tab");
  }
  if (debuggerSession.tabId === tabId) {
    if (!debuggerSession.adopted || await probeDebugger(tabId)) {
      debuggerSession.adopted = false;
      touchDebugger();
      return;
    }
    resetDebuggerSession();
  }
  try {
    await chrome.debugger.attach({ tabId }, "1.3");
  } catch (error) {
    // The worker may have been restarted while an earlier session was still
    // attached; adopt it if commands go through, otherwise surface the error.
    if (!(await probeDebugger(tabId))) {
      throw new Error(`Could not take browser control of tab ${tabId}: ${errorMessage(error)}`);
    }
  }
  debuggerSession.tabId = tabId;
  debuggerSession.adopted = false;
  touchDebugger();
  await chrome.storage.local.set({ debuggerTabId: tabId }).catch(logFailure("record the debugger tab"));
  await recordEvent(`Took browser control of tab ${tabId}`);
}

async function probeDebugger(tabId) {
  try {
    await chrome.debugger.sendCommand({ tabId }, "Runtime.evaluate", { expression: "1", returnByValue: true });
    return true;
  } catch {
    return false;
  }
}

/** Marks the session as used and (re)arms the idle alarm. */
function touchDebugger() {
  debuggerSession.lastUsedAt = Date.now();
  chrome.alarms.create(DEBUGGER_IDLE_ALARM, { delayInMinutes: DEBUGGER_IDLE_MS / 60000 }).catch(logFailure("schedule the debugger idle alarm"));
}

async function sendDebuggerCommand(method, params = {}) {
  const tabId = debuggerSession.tabId;
  if (tabId === null) throw new Error("Browser control is not active for the target tab.");
  try {
    return await chrome.debugger.sendCommand({ tabId }, method, params);
  } catch (error) {
    throw new Error(`${method} failed: ${errorMessage(error)}`);
  }
}

async function releaseDebugger(reason) {
  const tabId = debuggerSession.tabId;
  if (tabId === null) return;
  resetDebuggerSession();
  try {
    await chrome.debugger.detach({ tabId });
  } catch {
    // Already detached (tab closed, DevTools opened or Chrome dropped it).
  }
  await recordEvent(`Released browser control of tab ${tabId} (${reason})`);
}

function resetDebuggerSession() {
  debuggerSession.tabId = null;
  debuggerSession.lastUsedAt = 0;
  debuggerSession.adopted = false;
  chrome.alarms.clear(DEBUGGER_IDLE_ALARM).catch(logFailure("clear the debugger idle alarm"));
  chrome.storage.local.set({ debuggerTabId: null }).catch(logFailure("clear the debugger tab"));
}

/** Adopts a session recorded by a previous worker instance so it can be reused or released. */
async function restoreDebuggerSession() {
  try {
    const { debuggerTabId } = await chrome.storage.local.get({ debuggerTabId: null });
    if (!Number.isInteger(debuggerTabId)) return;
    debuggerSession.tabId = debuggerTabId;
    debuggerSession.adopted = true;
    touchDebugger();
  } catch (error) {
    console.warn("Could not restore the debugger session:", errorMessage(error));
  }
}

async function handleDebuggerIdleAlarm() {
  if (debuggerSession.tabId === null) return;
  const idleFor = Date.now() - debuggerSession.lastUsedAt;
  if (automationActive || idleFor < DEBUGGER_IDLE_MS) {
    const remainingMinutes = Math.max(MIN_ALARM_DELAY_MINUTES, (DEBUGGER_IDLE_MS - idleFor) / 60000);
    await chrome.alarms.create(DEBUGGER_IDLE_ALARM, { delayInMinutes: remainingMinutes });
    return;
  }
  await enqueueAutomation(async () => {
    if (debuggerSession.tabId !== null && Date.now() - debuggerSession.lastUsedAt >= DEBUGGER_IDLE_MS) {
      await releaseDebugger("idle for 90 seconds");
    }
  }, { fromBridge: false });
}

/**
 * Serialises every automation action (bridge and side panel) so two actions
 * never overlap. Bridge-originated actions publish automationBusy for the UI.
 */
function enqueueAutomation(task, { fromBridge }) {
  const run = automationQueue.then(async () => {
    automationActive = true;
    if (fromBridge) await chrome.storage.local.set({ automationBusy: true }).catch(logFailure("publish the busy state"));
    try {
      return await task();
    } finally {
      automationActive = false;
      if (debuggerSession.tabId !== null) touchDebugger();
      if (fromBridge) await chrome.storage.local.set({ automationBusy: false }).catch(logFailure("clear the busy state"));
    }
  });
  automationQueue = run.then(() => undefined, () => undefined);
  return run;
}

// ---------------------------------------------------------------------------
// 8. Automation actions and trusted input helpers
// ---------------------------------------------------------------------------

/**
 * Executes one automation action against the target tab. Results carry
 * tabId plus action-specific fields (elements/total, choices, output,
 * screenshot). Must be called through enqueueAutomation.
 */
async function runAutomationAction(request) {
  const action = typeof request.action === "string" ? request.action : "";
  const selector = typeof request.selector === "string" ? request.selector.trim() : "";
  const value = typeof request.value === "string" ? request.value : "";
  if (SELECTOR_ACTIONS.has(action) && !selector) throw new Error("A CSS selector is required.");
  const tab = await waitForTargetTab();
  switch (action) {
    case "list_selectors":
      return listSelectors(tab.id);
    case "page_contains":
      return pageContains(tab.id, selector);
    case "read_text":
      return readText(tab.id, selector);
    case "screenshot":
      return captureScreenshot(tab);
    case "press_key":
      return pressKey(tab.id, request.key);
    case "click":
      return clickElement(tab.id, selector);
    case "fill":
      return fillElement(tab.id, selector, value);
    case "dropdown":
      return loadDropdownChoices(tab.id, selector);
    case "choose":
      return chooseDropdownValue(tab.id, selector, value);
    default:
      throw new Error(`Unsupported automation action: ${action || "(none)"}`);
  }
}

async function runInTab(tabId, func, args = []) {
  const [injection] = await chrome.scripting.executeScript({ target: { tabId }, func, args });
  return injection ? injection.result : undefined;
}

async function listSelectors(tabId) {
  const { siteHints } = await loadSettings();
  const result = await runInTab(tabId, inspectPageElements, [siteHints]);
  const elements = Array.isArray(result?.elements) ? result.elements : [];
  const total = Number.isInteger(result?.total) ? result.total : elements.length;
  await recordEvent(`Found ${total} interactive elements in tab ${tabId}`);
  return { tabId, elements, total };
}

async function pageContains(tabId, selector) {
  const result = await runInTab(tabId, pageContainsSelector, [selector]);
  if (result?.error) throw new Error(result.error);
  return { tabId, output: String(Boolean(result?.exists)) };
}

async function readText(tabId, selector) {
  const result = await runInTab(tabId, readElementText, [selector]);
  if (result?.error) throw new Error(result.error);
  return { tabId, output: typeof result?.text === "string" ? result.text : "" };
}

async function captureScreenshot(tab) {
  await acquireDebugger(tab.id);
  if (!tab.active) {
    // Hidden tabs do not paint; bring the target forward so the capture succeeds.
    await chrome.tabs.update(tab.id, { active: true });
    await sleep(POLL_INTERVAL_MS);
  }
  const result = await sendDebuggerCommand("Page.captureScreenshot", { format: "png" });
  if (typeof result?.data !== "string" || !result.data) throw new Error("The page did not return a screenshot.");
  await recordEvent(`Captured a screenshot of tab ${tab.id}`);
  return { tabId: tab.id, screenshot: result.data };
}

async function pressKey(tabId, keyName) {
  const key = resolveKey(keyName);
  await acquireDebugger(tabId);
  if (key.insertText !== undefined) {
    await sendDebuggerCommand("Input.insertText", { text: key.insertText });
  } else {
    await dispatchKey(key);
  }
  await recordEvent(`Pressed ${key.name} in tab ${tabId}`);
  return { tabId, output: `pressed ${key.name}` };
}

async function clickElement(tabId, selector) {
  const info = await locateElement(tabId, selector, "click");
  await acquireDebugger(tabId);
  await dispatchClick(info.x, info.y);
  await recordEvent(`Clicked ${info.element} matching ${selector} in tab ${tabId}`);
  return { tabId };
}

async function fillElement(tabId, selector, value) {
  const info = await locateElement(tabId, selector, "text");
  const { typing } = await loadSettings();
  await acquireDebugger(tabId);
  await dispatchClick(info.x, info.y);
  await clearFocusedField();
  await typeText(value, typing);
  await recordEvent(`Filled ${info.element} matching ${selector} in tab ${tabId}; value omitted`);
  return { tabId };
}

async function loadDropdownChoices(tabId, selector) {
  const info = await locateElement(tabId, selector, "click");
  let choices;
  if (info.tag === "select") {
    const result = await runInTab(tabId, readNativeSelectOptions, [selector]);
    choices = Array.isArray(result?.choices) ? result.choices : [];
  } else {
    await acquireDebugger(tabId);
    await dispatchClick(info.x, info.y);
    choices = await waitForAutocompleteOptions(tabId, selector);
  }
  await recordEvent(`Loaded ${choices.length} dropdown choices for ${selector} in tab ${tabId}`);
  return { tabId, choices };
}

async function chooseDropdownValue(tabId, selector, value) {
  const info = await locateElement(tabId, selector, "choose");
  if (info.tag === "select") {
    const result = await runInTab(tabId, selectNativeOption, [selector, value]);
    if (result?.error) throw new Error(result.error);
    await recordEvent(`Selected a choice in ${selector} in tab ${tabId}`);
    return { tabId };
  }
  await acquireDebugger(tabId);
  await dispatchClick(info.x, info.y);
  const option = await waitForAutocompleteChoice(tabId, selector, value);
  if (!option) throw new Error("The requested choice did not appear in the dropdown. Load the choices again after opening it.");
  await chooseAutocompleteOption(option);
  await recordEvent(`Selected a dropdown choice for ${selector} in tab ${tabId}`);
  return { tabId };
}

/** Polls the page until the selector resolves to a single interactable element. */
async function locateElement(tabId, selector, mode) {
  const deadline = Date.now() + ELEMENT_WAIT_TIMEOUT_MS;
  for (;;) {
    let result;
    try {
      result = await runInTab(tabId, getElementActionInfo, [selector, mode]);
    } catch (error) {
      const detail = errorMessage(error);
      if (Date.now() >= deadline || /cannot access|no tab with id/i.test(detail)) {
        throw new Error(`The target page could not be inspected: ${detail}`);
      }
      await sleep(POLL_INTERVAL_MS);
      continue;
    }
    if (result?.ok) return result;
    const message = result?.error || "The element could not be inspected.";
    if (!RETRYABLE_ELEMENT_ERRORS.has(message) || Date.now() >= deadline) throw new Error(message);
    await sleep(POLL_INTERVAL_MS);
  }
}

async function waitForAutocompleteOptions(tabId, selector) {
  const deadline = Date.now() + AUTOCOMPLETE_WAIT_TIMEOUT_MS;
  while (Date.now() < deadline) {
    const result = await runInTab(tabId, getAutocompleteOption, [selector, ""]);
    if (result?.options?.length) return result.options;
    await sleep(150);
  }
  return [];
}

async function waitForAutocompleteChoice(tabId, selector, desiredValue) {
  const deadline = Date.now() + AUTOCOMPLETE_WAIT_TIMEOUT_MS;
  while (Date.now() < deadline) {
    const result = await runInTab(tabId, getAutocompleteOption, [selector, desiredValue]);
    if (result?.option) return result.option;
    await sleep(150);
  }
  return null;
}

async function chooseAutocompleteOption(option) {
  if (option.x !== undefined && option.y !== undefined) {
    await dispatchClick(option.x, option.y);
    return;
  }
  for (let index = 0; index <= option.index; index += 1) await dispatchKey(KEY_DEFINITIONS.ArrowDown);
  await dispatchKey(KEY_DEFINITIONS.Enter);
}

/** Resolves a press_key name to a key definition or a single printable character. */
function resolveKey(rawName) {
  const raw = typeof rawName === "string" ? rawName : "";
  if (raw === " ") return KEY_DEFINITIONS.Space;
  const name = raw.trim();
  if (!name) throw new Error("A key name is required.");
  const definition = KEY_LOOKUP.get(name.toLowerCase());
  if (definition) return definition;
  if ([...name].length === 1 && !/\p{C}/u.test(name)) return { name, insertText: name };
  throw new Error(`Unsupported key "${name}". Use Enter, Tab, Escape, Backspace, ArrowUp, ArrowDown, ArrowLeft, ArrowRight, Space, Home, End, or a single character.`);
}

async function dispatchClick(x, y) {
  await sendDebuggerCommand("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
  await sendDebuggerCommand("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await sendDebuggerCommand("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
}

/**
 * Presses and releases a key. Keys without text use rawKeyDown; keys with
 * text use keyDown so Blink also emits the char/keypress event.
 */
async function dispatchKey(key, options = {}) {
  const base = {
    key: key.key,
    code: key.code,
    windowsVirtualKeyCode: key.keyCode,
    nativeVirtualKeyCode: key.keyCode,
    modifiers: options.modifiers || 0,
  };
  const down = key.text ? { ...base, type: "keyDown", text: key.text, unmodifiedText: key.text } : { ...base, type: "rawKeyDown" };
  if (options.commands) down.commands = options.commands;
  await sendDebuggerCommand("Input.dispatchKeyEvent", down);
  await sendDebuggerCommand("Input.dispatchKeyEvent", { ...base, type: "keyUp" });
}

/** Select-all (Meta+A on macOS, Ctrl+A elsewhere) followed by Backspace. */
async function clearFocusedField() {
  const mac = isMacPlatform();
  await dispatchKey({ key: "a", code: "KeyA", keyCode: 65 }, {
    modifiers: mac ? MODIFIER_META : MODIFIER_CTRL,
    commands: mac ? ["selectAll"] : undefined,
  });
  await dispatchKey(KEY_DEFINITIONS.Backspace);
}

function isMacPlatform() {
  const platform = navigator.userAgentData?.platform ?? navigator.platform ?? "";
  return /mac/i.test(platform);
}

async function typeText(text, typing) {
  if (!text) return;
  if (!typing.humanLike) {
    await sendDebuggerCommand("Input.insertText", { text });
    return;
  }
  const characters = [...text];
  for (let index = 0; index < characters.length; index += 1) {
    await sendDebuggerCommand("Input.insertText", { text: characters[index] });
    if (index < characters.length - 1) await sleep(randomBetween(typing.minMs, typing.maxMs));
  }
}

// ---------------------------------------------------------------------------
// 9. Functions injected into the target page (self-contained by necessity)
// ---------------------------------------------------------------------------

function getElementActionInfo(selector, mode) {
  let matches;
  try {
    matches = document.querySelectorAll(selector);
  } catch {
    return { ok: false, error: "That CSS selector is invalid." };
  }
  if (!matches.length) return { ok: false, error: "No field matches that selector." };
  if (matches.length > 1) return { ok: false, error: `That selector matches ${matches.length} elements; make it more specific.` };
  const element = matches[0];
  if (!(element instanceof HTMLElement)) return { ok: false, error: "That element cannot be interacted with." };
  const tag = element.tagName.toLowerCase();
  const inputType = tag === "input" ? element.type : "";
  if (mode !== "click" && (element.disabled || element.readOnly)) return { ok: false, error: "That field is disabled or read-only." };
  if (mode !== "click" && tag === "input") {
    const unsupportedTypes = ["button", "checkbox", "file", "hidden", "image", "radio", "reset", "submit"];
    if (unsupportedTypes.includes(inputType)) return { ok: false, error: `Input type "${inputType}" is not supported.` };
  }
  if (mode === "text" && !(tag === "input" || tag === "textarea" || element.isContentEditable)) {
    return { ok: false, error: "Text filling supports inputs, textareas, and editable elements. Use the dropdown chooser for picklists." };
  }
  if (mode === "choose" && tag !== "input" && tag !== "select") {
    return { ok: false, error: "Dropdown selection supports a select element or a text input with visible choices." };
  }
  if (element.disabled) return { ok: false, error: "That element cannot be interacted with." };
  element.scrollIntoView({ block: "center", inline: "center" });
  const rect = element.getBoundingClientRect();
  if (!rect.width || !rect.height) return { ok: false, error: "That element is not visible." };
  const point = pickPoint();
  return { ok: true, x: point.x, y: point.y, tag, element: tag === "input" ? `input[type=${inputType || "text"}]` : tag };

  // Prefers a point inside the viewport where the element itself is hit-tested,
  // so sticky headers or overlays do not swallow the click.
  function pickPoint() {
    const viewportWidth = document.documentElement.clientWidth || window.innerWidth;
    const viewportHeight = document.documentElement.clientHeight || window.innerHeight;
    let fallback = null;
    for (const [fx, fy] of [[0.5, 0.5], [0.25, 0.5], [0.75, 0.5], [0.5, 0.25], [0.5, 0.75]]) {
      const x = rect.left + rect.width * fx;
      const y = rect.top + rect.height * fy;
      if (x < 0 || y < 0 || x >= viewportWidth || y >= viewportHeight) continue;
      if (!fallback) fallback = { x, y };
      const hit = document.elementFromPoint(x, y);
      if (hit && (hit === element || element.contains(hit) || hit.contains(element))) return { x, y };
    }
    return fallback || { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  }
}

function pageContainsSelector(selector) {
  try {
    return { exists: document.querySelector(selector) !== null };
  } catch (error) {
    return { error: `Invalid CSS selector: ${error.message}` };
  }
}

function readElementText(selector) {
  let element;
  try {
    element = document.querySelector(selector);
  } catch (error) {
    return { error: `Invalid CSS selector: ${error.message}` };
  }
  if (!element) return { error: "No element matches that selector." };
  const tag = element.tagName.toLowerCase();
  if (tag === "input" || tag === "textarea" || tag === "select") return { text: String(element.value ?? "").trim() };
  return { text: (element.innerText || element.textContent || "").trim() };
}

function readNativeSelectOptions(selector) {
  let element;
  try {
    element = document.querySelector(selector);
  } catch {
    return { choices: [] };
  }
  if (!(element instanceof HTMLSelectElement)) return { choices: [] };
  return {
    choices: [...element.options].filter((option) => !option.disabled).map((option) => ({
      label: (option.label || option.textContent || option.value).trim(),
      value: option.value,
    })),
  };
}

// Selects a native <select> option by value or label and fires the input and
// change events frameworks listen to (the approach used by browser automation
// tools; native popups cannot be driven reliably on every platform).
function selectNativeOption(selector, requested) {
  let element;
  try {
    element = document.querySelector(selector);
  } catch {
    return { error: "That CSS selector is invalid." };
  }
  if (!(element instanceof HTMLSelectElement)) return { error: "That element is not a select element." };
  const wanted = String(requested ?? "").trim();
  const labelOf = (option) => (option.label || option.textContent || "").trim();
  const options = [...element.options];
  const lowered = wanted.toLocaleLowerCase();
  const option = options.find((item) => item.value === wanted) ||
    options.find((item) => labelOf(item) === wanted) ||
    options.find((item) => item.value.toLocaleLowerCase() === lowered || labelOf(item).toLocaleLowerCase() === lowered);
  if (!option) return { error: `No option label or value matches "${wanted}".` };
  if (option.disabled) return { error: `The option "${labelOf(option)}" is disabled.` };
  element.focus();
  element.selectedIndex = option.index;
  element.dispatchEvent(new Event("input", { bubbles: true }));
  element.dispatchEvent(new Event("change", { bubbles: true }));
  return { label: labelOf(option), value: option.value };
}

function getAutocompleteOption(selector, desiredValue) {
  let input;
  try {
    input = document.querySelector(selector);
  } catch {
    return { option: null, options: [] };
  }
  if (!(input instanceof HTMLInputElement)) return { option: null, options: [] };
  const options = [];
  const seen = new Set();
  const datalistId = input.getAttribute("list");
  if (datalistId) {
    const datalist = document.getElementById(datalistId);
    if (datalist instanceof HTMLDataListElement) {
      options.push(...[...datalist.options].map((option) => ({ label: option.label || option.value, value: option.value || option.label })));
    }
  }
  const controlledIds = (input.getAttribute("aria-controls") || "").split(/\s+/).filter(Boolean);
  const controlledLists = controlledIds.map((id) => document.getElementById(id)).filter(Boolean);
  const roots = controlledLists.length
    ? controlledLists
    : input.getAttribute("aria-expanded") === "true" ? [...document.querySelectorAll('[role="listbox"]')] : [];
  for (const root of roots) {
    const rootStyle = getComputedStyle(root);
    if (rootStyle.display === "none" || rootStyle.visibility === "hidden" || !root.getClientRects().length) continue;
    collectVisibleOptions(root, '[role="option"], [data-value], li, [class*="option"], [class*="suggest"]');
  }
  if (!options.some((option) => option.x !== undefined)) {
    for (let ancestor = input.parentElement, depth = 0; ancestor && depth < 4; ancestor = ancestor.parentElement, depth += 1) {
      if (!/(auto|suggest|option|dropdown|list|pick)/.test(`${ancestor.id} ${ancestor.className}`.toLowerCase())) continue;
      collectVisibleOptions(ancestor, 'li, [data-value], [class*="option"], [class*="suggest"]');
    }
  }
  const query = String(desiredValue ?? "").trim().toLocaleLowerCase();
  const listed = options.filter((option) => option.label).map(({ label, value }) => ({ label, value }));
  if (!query) return { option: null, options: listed };
  const normalized = options.map((option) => `${option.label} ${option.value || ""}`.trim().toLocaleLowerCase());
  let index = normalized.findIndex((candidate) => candidate === query);
  if (index < 0) index = normalized.findIndex((candidate) => candidate.startsWith(query));
  if (index < 0) index = normalized.findIndex((candidate) => candidate.includes(query));
  return { option: index < 0 ? null : { index, ...options[index] }, options: listed };

  function collectVisibleOptions(root, optionSelector) {
    for (const option of root.querySelectorAll(optionSelector)) {
      if (seen.has(option)) continue;
      seen.add(option);
      const style = getComputedStyle(option);
      const rect = option.getBoundingClientRect();
      if (!rect.width || !rect.height || style.display === "none" || style.visibility === "hidden") continue;
      const text = (option.innerText || option.textContent || "").trim();
      options.push({
        label: text || option.getAttribute("data-value") || "",
        value: option.getAttribute("data-value") || text,
        x: rect.left + rect.width / 2,
        y: rect.top + rect.height / 2,
      });
    }
  }
}

// ---------------------------------------------------------------------------
// 10. Event registration and bootstrap
// ---------------------------------------------------------------------------

chrome.runtime.onInstalled.addListener(() => {
  void initialize();
});

chrome.runtime.onStartup.addListener(() => {
  void connectBridge();
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!message || typeof message.type !== "string" || sender.id !== chrome.runtime.id) return false;
  handleRuntimeMessage(message)
    .then((response) => sendResponse(response))
    .catch((error) => sendResponse({ ok: false, error: errorMessage(error) }));
  return true;
});

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === RECONNECT_ALARM) void connectBridge();
  if (alarm.name === DEBUGGER_IDLE_ALARM) handleDebuggerIdleAlarm().catch(logFailure("handle the debugger idle alarm"));
});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== "local" || !changes.bridgePort) return;
  void recordEvent(`Bridge port set to ${normalizeBridgePort(changes.bridgePort.newValue)}; reconnecting`);
  void connectBridge({ force: true });
});

chrome.tabs.onRemoved.addListener((tabId) => {
  updateTabState((state) => {
    forgetTabInState(state, tabId);
  }).catch(logFailure("forget a closed tab"));
  if (debuggerSession.tabId === tabId) resetDebuggerSession();
});

chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  if (changeInfo.url && debuggerSession.tabId === tabId && !isHttpUrl(changeInfo.url)) {
    releaseDebugger("the tab left the website").catch(logFailure("release the debugger"));
  }
  if (!changeInfo.url && !changeInfo.title && changeInfo.status !== "complete") return;
  updateTabState((state) => {
    const url = tab.url || tab.pendingUrl || "";
    if (isInternalUrl(url)) {
      forgetTabInState(state, tabId);
      return;
    }
    const entry = state.targetTabs.find((item) => item.id === tabId);
    if (!entry) return;
    entry.url = url || entry.url;
    entry.title = tab.title || entry.title;
    if (state.targetTabId === tabId) state.targetTabUrl = entry.url;
  }).catch(logFailure("track a tab update"));
});

chrome.tabs.onCreated.addListener((tab) => {
  if (!Number.isInteger(tab.openerTabId) || !Number.isInteger(tab.id)) return;
  const url = tab.url || tab.pendingUrl || "";
  if (isInternalUrl(url)) return;
  updateTabState((state) => {
    if (!state.targetTabs.some((entry) => entry.id === tab.openerTabId)) return;
    if (state.targetTabs.some((entry) => entry.id === tab.id)) return;
    state.targetTabs.push({ id: tab.id, url, title: tab.title || "" });
  }).catch(logFailure("track a new tab"));
});

chrome.debugger.onDetach.addListener((source, reason) => {
  if (source.tabId !== debuggerSession.tabId) return;
  resetDebuggerSession();
  void recordEvent(`Browser control of tab ${source.tabId} ended (${reason})`);
});

chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch(logFailure("enable the side panel toolbar action"));

/** One-time startup work for this worker instance. */
function initialize() {
  if (!initializePromise) {
    initializePromise = (async () => {
      await migrateLegacySettings();
      await chrome.storage.local.set({ automationBusy: false }).catch(logFailure("reset the busy state"));
      await restoreDebuggerSession();
      await connectBridge();
    })().catch((error) => console.warn("Initialisation failed:", errorMessage(error)));
  }
  return initializePromise;
}

void initialize();
