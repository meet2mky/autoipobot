// Harness driver: scanner + shared.js unit checks, then the real service
// worker (in a Worker under the chrome stub), then the real side panel.
const results = [];
const check = (name, condition, detail) => {
  results.push(`${condition ? "PASS" : "FAIL"} ${name}${condition ? "" : ` -- ${detail === undefined ? "" : String(detail)}`}`);
};
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const readText = (url) => {
  const xhr = new XMLHttpRequest();
  xhr.open("GET", url, false);
  xhr.send();
  return xhr.responseText;
};
async function waitFor(predicate, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await predicate();
    if (value) return value;
    await sleep(20);
  }
  throw new Error(`Timed out waiting for ${label}`);
}

window.addEventListener("error", (event) => results.push(`PAGE ERROR ${event.message} (${event.filename}:${event.lineno})`));
window.addEventListener("unhandledrejection", (event) => results.push(`PAGE REJECTION ${event.reason && (event.reason.stack || event.reason.message || event.reason)}`));

let worker;
let nextId = 1;
const pending = new Map();
const storageLog = [];

function startWorker() {
  return new Promise((resolve) => {
    worker = new Worker("sw-host.js");
    worker.addEventListener("error", (event) => {
      results.push(`WORKER ERROR ${event.message} (${event.filename}:${event.lineno})`);
      resolve(`worker error: ${event.message}`);
    });
    worker.addEventListener("message", (event) => {
      const data = event.data;
      if (data.kind === "ready") {
        resolve(data.importError);
        return;
      }
      if (data.kind === "storage") {
        if (data.area === "local") {
          for (const [key, change] of Object.entries(data.changes)) storageLog.push({ key, value: change.remove ? undefined : change.newValue });
        }
        window.__stub.applyChanges(data.area, data.changes, false);
        return;
      }
      if (data.kind === "response") {
        const resolver = pending.get(data.id);
        if (resolver) {
          pending.delete(data.id);
          resolver(data.response);
        }
      }
    });
    window.__stub.peers.send = (message) => worker.postMessage(message);
    window.__stub.peers.request = (message) => request({ kind: "runtime_message", message });
  });
}

function request(payload) {
  return new Promise((resolve) => {
    const id = nextId++;
    pending.set(id, resolve);
    worker.postMessage({ ...payload, id });
  });
}

const send = (message) => window.__stub.peers.request(message);

async function evalInWorker(code) {
  const response = await request({ kind: "eval", code });
  if (!response.ok) throw new Error(`eval failed: ${response.error} :: ${code.slice(0, 80)}`);
  return response.result;
}

const commands = () => evalInWorker("return stub.debugger._state.commands.map((c) => [c.method, c.params])");
const local = () => window.__stub.areas.local.data;

async function runScannerChecks() {
  const scan = inspectPageElements({ ...AUTOIPO_DEFAULTS.siteHints });
  const byId = (id) => scan.elements.find((element) => element.selector === `#${id}`);
  check("scan: found elements", scan.total >= scan.elements.length && scan.elements.length > 0, JSON.stringify(scan).slice(0, 200));
  check("scan: text input fillable with label", byId("f-name")?.kind === "Fillable" && byId("f-name")?.label === "Full name", JSON.stringify(byId("f-name")));
  check("scan: select fillable", byId("f-bank")?.kind === "Fillable", JSON.stringify(byId("f-bank")));
  check("scan: button clickable", byId("f-submit")?.kind === "Clickable" && byId("f-submit")?.description === "Submit", JSON.stringify(byId("f-submit")));
  check("scan: apply-as spans are choices with default hints", byId("opt-self")?.kind === "Choice" && byId("opt-minor")?.kind === "Choice", JSON.stringify([byId("opt-self"), byId("opt-minor")]));
  check("scan: context ignores 'Apply as' heading", byId("f-name")?.context === "", JSON.stringify(byId("f-name")));
  check("scan: radio is a choice", scan.elements.some((element) => element.kind === "Choice" && element.description === "input (radio)"));
  check("scan: hidden input excluded", !scan.elements.some((element) => element.description === "input (hidden)"));
  const noHints = inspectPageElements({ choiceGroupPattern: "", ignoredContextPattern: "" });
  const noById = (id) => noHints.elements.find((element) => element.selector === `#${id}`);
  check("scan: without choice hint spans are clickable", noById("opt-self")?.kind === "Clickable", JSON.stringify(noById("opt-self")));
  check("scan: without ignore hint context is 'Apply as'", noById("f-name")?.context === "Apply as", JSON.stringify(noById("f-name")));
  let invalidScan = null;
  try {
    invalidScan = inspectPageElements({ choiceGroupPattern: "(", ignoredContextPattern: "[" });
  } catch (error) {
    check("scan: invalid patterns must not throw", false, error.message);
  }
  if (invalidScan) check("scan: invalid patterns behave like disabled hints", invalidScan.elements.length === noHints.elements.length, `${invalidScan.elements.length} vs ${noHints.elements.length}`);
  check("scan: undefined hints accepted", inspectPageElements(undefined).elements.length > 0);

  check("shared: normalizeTyping defaults", JSON.stringify(normalizeTyping(undefined)) === JSON.stringify({ minMs: 40, maxMs: 120, humanLike: true }));
  check("shared: normalizeTyping clamps and orders", JSON.stringify(normalizeTyping({ minMs: 500, maxMs: 10, humanLike: "yes" })) === JSON.stringify({ minMs: 500, maxMs: 500, humanLike: true }));
  check("shared: normalizeBridgePort", normalizeBridgePort("70000") === 18765 && normalizeBridgePort(8080) === 8080 && normalizeBridgePort("1234") === 1234);
  check("shared: validateHintPattern", validateHintPattern("(").ok === false && validateHintPattern("").ok === true && validateHintPattern("a+").ok === true);
  check("shared: parseWebsiteUrl", parseWebsiteUrl("ftp://x").error !== undefined && parseWebsiteUrl(" https://a.b/c ").href === "https://a.b/c");
  check("shared: normalizeSiteHints keeps empty strings", normalizeSiteHints({ choiceGroupPattern: "" }).choiceGroupPattern === "" && normalizeSiteHints({}).ignoredContextPattern === AUTOIPO_DEFAULTS.siteHints.ignoredContextPattern);
}

async function runWorkerChecks() {
  const importError = await startWorker();
  check("worker: imported without error", importError === null, importError);
  if (importError !== null) return false;

  await waitFor(() => storageLog.some((entry) => entry.key === "bridgeState"), 5000, "bridgeState");
  await sleep(50);
  check("worker: initial bridge state retrying (no bridge)", local().bridgeState === "retrying", local().bridgeState);
  check("worker: automationBusy reset to false", local().automationBusy === false);
  check("worker: badge shows retry", await evalInWorker("return stub.action.badge.text") === "!");
  check("worker: reconnect alarm scheduled", await evalInWorker("return stub.alarms.alarms.has('bridge-reconnect')"));
  check("worker: side panel behaviour set", await evalInWorker("return stub.calls.some((c) => c.api === 'sidePanel.setPanelBehavior')"));

  await evalInWorker("stub.areas.sync.data.websiteUrl = 'https://legacy.example/'; delete stub.areas.local.data.websiteUrl; await migrateLegacySettings();");
  check("worker: migrated sync websiteUrl to local and removed sync copy",
    await evalInWorker("return stub.areas.local.data.websiteUrl") === "https://legacy.example/" && await evalInWorker("return stub.areas.sync.data.websiteUrl") === undefined);

  check("worker: unknown message rejected", (await send({ type: "nope" })).ok === false);
  let r = await send({ type: "get_target_tab" });
  check("worker: get_target_tab empty", r.ok === true && r.tab === null && Array.isArray(r.tabs) && r.tabs.length === 0, JSON.stringify(r));
  r = await send({ type: "open_website", url: "https://example.com/path" });
  check("worker: open_website", r.ok === true && r.tabId === 101, JSON.stringify(r));
  r = await send({ type: "get_target_tab" });
  check("worker: target tab after open", r.tab?.id === 101 && r.tab?.host === "example.com" && r.tabs.length === 1 && r.tabs[0].url === "https://example.com/path", JSON.stringify(r));
  check("worker: websiteUrl stored", await evalInWorker("return stub.areas.local.data.websiteUrl") === "https://example.com/path");
  r = await send({ type: "open_website", url: "not a url" });
  check("worker: open_website rejects invalid", r.ok === false && /valid/i.test(r.error), JSON.stringify(r));

  await send({ type: "record_event", message: "hello log" });
  let logs = await evalInWorker("return stub.areas.local.data.commandLogs");
  check("worker: record_event stored newest first", logs[0].message === "hello log" && typeof logs[0].time === "string", JSON.stringify(logs.slice(0, 2)));
  await Promise.all(Array.from({ length: 60 }, (_, index) => send({ type: "record_event", message: `bulk ${index}` })));
  logs = await evalInWorker("return stub.areas.local.data.commandLogs");
  check("worker: log capped at 50 without losing concurrent writes", logs.length === 50 && logs[0].message === "bulk 59", `${logs.length} ${logs[0]?.message}`);
  await send({ type: "clear_logs" });
  check("worker: clear_logs", (await evalInWorker("return stub.areas.local.data.commandLogs")).length === 0);

  await evalInWorker(`
    stub.scripting._canned.getElementActionInfo = (selector) => ({ ok: true, x: 10, y: 20, tag: selector === "#sel" ? "select" : "input", element: "input[type=text]" });
    stub.scripting._canned.pageContainsSelector = (selector) => ({ exists: selector === "#yes" });
    stub.scripting._canned.readElementText = () => ({ text: "hello" });
    stub.scripting._canned.readNativeSelectOptions = () => ({ choices: [{ label: "One", value: "1" }] });
    stub.scripting._canned.selectNativeOption = () => ({ label: "One", value: "1" });
    stub.scripting._canned.getAutocompleteOption = () => ({ option: null, options: [] });
    stub.scripting._canned.inspectPageElements = () => ({ elements: [{ kind: "Fillable", label: "a", description: "input (text)", context: "", selector: "#a" }], total: 3 });
    stub.debugger._state.handlers["Page.captureScreenshot"] = () => ({ data: "iVBORw0KGgo=" });
  `);

  r = await send({ type: "automation_action", action: "click", selector: "#a" });
  let cmds = await commands();
  check("worker: click ok", r.ok === true && r.tabId === 101, JSON.stringify(r));
  check("worker: click dispatched mouse events", cmds.filter((c) => c[0] === "Input.dispatchMouseEvent").map((c) => c[1].type).join(",") === "mouseMoved,mousePressed,mouseReleased", JSON.stringify(cmds));
  check("worker: debugger attached once and recorded",
    (await evalInWorker("return stub.calls.filter((c) => c.api === 'debugger.attach').length")) === 1 && (await evalInWorker("return stub.areas.local.data.debuggerTabId")) === 101);
  check("worker: idle alarm armed for 1.5 min", await evalInWorker("return stub.alarms.alarms.get('debugger-idle')?.delayInMinutes") === 1.5);
  await send({ type: "automation_action", action: "click", selector: "#a" });
  check("worker: second click did not re-attach", (await evalInWorker("return stub.calls.filter((c) => c.api === 'debugger.attach').length")) === 1);
  r = await send({ type: "automation_action", action: "click", selector: "" });
  check("worker: click requires selector", r.ok === false && /selector/i.test(r.error), JSON.stringify(r));

  await evalInWorker("stub.debugger._state.commands.length = 0");
  r = await send({ type: "automation_action", action: "press_key", key: "Enter" });
  cmds = await commands();
  check("worker: Enter is keyDown with text + keyUp (keyCode 13)",
    r.ok && cmds.length === 2 && cmds[0][1].type === "keyDown" && cmds[0][1].text === "\r" && cmds[0][1].windowsVirtualKeyCode === 13 && cmds[1][1].type === "keyUp" && cmds[1][1].code === "Enter", JSON.stringify(cmds));
  await evalInWorker("stub.debugger._state.commands.length = 0");
  r = await send({ type: "automation_action", action: "press_key", key: "arrowdown" });
  cmds = await commands();
  check("worker: ArrowDown is rawKeyDown keyCode 40 (case-insensitive name)",
    r.ok && cmds[0][1].type === "rawKeyDown" && cmds[0][1].windowsVirtualKeyCode === 40 && cmds[0][1].key === "ArrowDown" && r.output === "pressed ArrowDown", JSON.stringify(cmds) + JSON.stringify(r));
  await evalInWorker("stub.debugger._state.commands.length = 0");
  r = await send({ type: "automation_action", action: "press_key", key: "x" });
  cmds = await commands();
  check("worker: single char uses insertText", r.ok && cmds.length === 1 && cmds[0][0] === "Input.insertText" && cmds[0][1].text === "x", JSON.stringify(cmds));
  r = await send({ type: "automation_action", action: "press_key", key: "F5" });
  check("worker: unsupported key rejected", r.ok === false && /Unsupported key/.test(r.error), JSON.stringify(r));
  r = await send({ type: "automation_action", action: "press_key", key: " " });
  check("worker: space key", r.ok && r.output === "pressed Space", JSON.stringify(r));
  r = await send({ type: "automation_action", action: "press_key", key: "" });
  check("worker: empty key rejected", r.ok === false, JSON.stringify(r));

  await evalInWorker("stub.debugger._state.commands.length = 0; await chrome.storage.local.set({ typing: { minMs: 0, maxMs: 0, humanLike: false } });");
  r = await send({ type: "automation_action", action: "fill", selector: "#a", value: "héllo" });
  cmds = await commands();
  let inserts = cmds.filter((c) => c[0] === "Input.insertText");
  const keyEvents = cmds.filter((c) => c[0] === "Input.dispatchKeyEvent");
  check("worker: bulk fill uses one insertText", r.ok && inserts.length === 1 && inserts[0][1].text === "héllo", JSON.stringify(inserts));
  check("worker: fill clears with Ctrl+A (65) and Backspace (8)",
    keyEvents.length === 4 && keyEvents[0][1].key === "a" && keyEvents[0][1].modifiers === 2 && keyEvents[0][1].windowsVirtualKeyCode === 65 && keyEvents[2][1].key === "Backspace" && keyEvents[2][1].windowsVirtualKeyCode === 8, JSON.stringify(keyEvents));
  await evalInWorker("stub.debugger._state.commands.length = 0; await chrome.storage.local.set({ typing: { minMs: 0, maxMs: 1, humanLike: true } });");
  r = await send({ type: "automation_action", action: "fill", selector: "#a", value: "ab😀" });
  cmds = await commands();
  inserts = cmds.filter((c) => c[0] === "Input.insertText");
  check("worker: human-like fill inserts per code point", r.ok && inserts.map((c) => c[1].text).join("|") === "a|b|😀", JSON.stringify(inserts));
  logs = await evalInWorker("return stub.areas.local.data.commandLogs");
  check("worker: fill values never logged", !JSON.stringify(logs).includes("héllo") && !JSON.stringify(logs).includes("ab😀") && logs.some((entry) => /Filled .* value omitted/.test(entry.message)), JSON.stringify(logs.slice(0, 3)));

  r = await send({ type: "automation_action", action: "dropdown", selector: "#sel" });
  check("worker: dropdown on select returns choices", r.ok && r.choices?.[0]?.label === "One", JSON.stringify(r));
  r = await send({ type: "automation_action", action: "choose", selector: "#sel", value: "One" });
  check("worker: choose on select", r.ok === true, JSON.stringify(r));
  r = await send({ type: "automation_action", action: "page_contains", selector: "#yes" });
  check("worker: page_contains true", r.ok && r.output === "true", JSON.stringify(r));
  r = await send({ type: "automation_action", action: "page_contains", selector: "#no" });
  check("worker: page_contains false", r.ok && r.output === "false", JSON.stringify(r));
  r = await send({ type: "automation_action", action: "read_text", selector: "#x" });
  check("worker: read_text", r.ok && r.output === "hello", JSON.stringify(r));
  r = await send({ type: "automation_action", action: "list_selectors" });
  check("worker: list_selectors", r.ok && r.elements.length === 1 && r.total === 3, JSON.stringify(r));
  const hintsPassed = await evalInWorker("return stub.calls.filter((c) => c.api === 'scripting.executeScript' && c.args[1] === 'inspectPageElements').at(-1).args[2]");
  check("worker: list_selectors passes site hints", JSON.stringify(hintsPassed) === JSON.stringify([AUTOIPO_DEFAULTS.siteHints]), JSON.stringify(hintsPassed));
  r = await send({ type: "automation_action", action: "screenshot" });
  check("worker: screenshot", r.ok && r.screenshot === "iVBORw0KGgo=", JSON.stringify(r));
  check("worker: popup actions never set automationBusy", !storageLog.some((entry) => entry.key === "automationBusy" && entry.value === true));

  await evalInWorker("stub.debugger._state.commands.length = 0; stub.debugger._state.delayNext = 300;");
  await Promise.all([
    send({ type: "automation_action", action: "press_key", key: "Escape" }),
    send({ type: "automation_action", action: "press_key", key: "Tab" }),
  ]);
  const timeline = await evalInWorker("return stub.debugger._state.commands.map((c) => [c.params.key, c.start, c.end])");
  check("worker: actions serialized (Tab starts after Escape finished)",
    timeline.length === 4 && timeline[0][0] === "Escape" && timeline[2][0] === "Tab" && timeline[2][1] >= timeline[1][2], JSON.stringify(timeline));

  await evalInWorker("stub.health.available = true; await connectBridge({ force: true });");
  await waitFor(() => evalInWorker("return stub.sockets.length > 0"), 5000, "socket");
  await evalInWorker("stub.sockets.at(-1)._open();");
  await waitFor(() => evalInWorker("return stub.areas.local.data.bridgeState === 'connected'"), 5000, "connected");
  check("worker: socket url uses default port", await evalInWorker("return stub.sockets.at(-1).url") === "ws://127.0.0.1:18765/ws");
  check("worker: connected badge", await evalInWorker("return stub.action.badge.text") === "✓");
  check("worker: connected log entry", (await evalInWorker("return stub.areas.local.data.commandLogs"))[0].message.includes("Connected to the AutoIPO bridge on port 18765"));

  const sentCount = () => evalInWorker("return stub.sockets.at(-1).sent.length");
  const lastSent = async () => JSON.parse(await evalInWorker("return stub.sockets.at(-1).sent.at(-1)"));
  const receive = async (payload) => {
    const before = await sentCount();
    await evalInWorker(`stub.sockets.at(-1)._receive(${JSON.stringify(JSON.stringify(payload))});`);
    await waitFor(async () => (await sentCount()) > before, 5000, `ack for ${payload.type}`);
    return lastSent();
  };
  let ack = await receive({ type: "heartbeat" });
  check("bridge: heartbeat ack", ack.type === "heartbeat_ack", JSON.stringify(ack));
  ack = await receive({ type: "set_url", id: "c1", url: "https://foo.example/x" });
  check("bridge: set_url ack + stored", ack.type === "ack" && ack.id === "c1" && ack.ok === true && (await evalInWorker("return stub.areas.local.data.websiteUrl")) === "https://foo.example/x", JSON.stringify(ack));
  const busyBefore = storageLog.filter((entry) => entry.key === "automationBusy" && entry.value === true).length;
  ack = await receive({ type: "screenshot", id: "c2" });
  check("bridge: screenshot ack", ack.ok === true && ack.screenshot === "iVBORw0KGgo=" && ack.tab_id === 101 && ack.options === undefined, JSON.stringify(ack).slice(0, 200));
  await sleep(50);
  check("bridge: automationBusy toggled for bridge action", storageLog.filter((entry) => entry.key === "automationBusy" && entry.value === true).length === busyBefore + 1 && local().automationBusy === false, JSON.stringify(storageLog.filter((entry) => entry.key === "automationBusy")));
  ack = await receive({ type: "bogus", id: "c3" });
  check("bridge: unsupported command", ack.ok === false && /Unsupported command/.test(ack.error) && ack.id === "c3", JSON.stringify(ack));
  ack = await receive({ type: "read_text", id: "c4", selector: "#x" });
  check("bridge: read_text ack", ack.ok && ack.output === "hello" && ack.tab_id === 101, JSON.stringify(ack));
  ack = await receive({ type: "dropdown", id: "c5", selector: "#sel" });
  check("bridge: dropdown ack uses choices (no legacy options)", ack.ok && Array.isArray(ack.choices) && ack.choices[0].value === "1" && ack.options === undefined && ack.output === "1 choices", JSON.stringify(ack));
  ack = await receive({ type: "list_selectors", id: "c6" });
  check("bridge: list_selectors ack", ack.ok && ack.elements.length === 1 && ack.output === "3 interactive elements found", JSON.stringify(ack));
  ack = await receive({ type: "page_contains", id: "c7", selector: "#yes" });
  check("bridge: page_contains ack", ack.ok && ack.output === "true", JSON.stringify(ack));
  ack = await receive({ type: "press_key", id: "c8", key: "Escape" });
  check("bridge: press_key ack", ack.ok && ack.output === "pressed Escape", JSON.stringify(ack));
  ack = await receive({ type: "fill", id: "c9", selector: "#a", value: "secret-value" });
  check("bridge: fill ack", ack.ok === true && ack.tab_id === 101, JSON.stringify(ack));
  logs = await evalInWorker("return stub.areas.local.data.commandLogs");
  check("bridge: fill value redacted in logs", !JSON.stringify(logs).includes("secret-value") && logs.some((entry) => entry.message.includes("[value redacted]")), JSON.stringify(logs.slice(0, 4)));
  ack = await receive({ type: "click", id: "c9b", selector: "" });
  check("bridge: click without selector fails cleanly", ack.ok === false && /selector/i.test(ack.error), JSON.stringify(ack));
  ack = await receive({ type: "open", id: "c10" });
  check("bridge: open ack", ack.ok && ack.tab_id === 102 && ack.output === "opened tab 102", JSON.stringify(ack));
  const beforeInvalid = await sentCount();
  await evalInWorker("stub.sockets.at(-1)._receive('{not json');");
  await sleep(100);
  check("bridge: invalid JSON produces no ack", (await sentCount()) === beforeInvalid);
  ack = await receive({ type: "click", id: "c11", selector: "#a" });
  check("bridge: click on new target tab switches the debugger session",
    ack.ok && ack.tab_id === 102 && (await evalInWorker("return stub.calls.filter((c) => c.api === 'debugger.detach').map((c) => c.args[0])")).includes(101) && (await evalInWorker("return stub.areas.local.data.debuggerTabId")) === 102, JSON.stringify(ack));

  await evalInWorker("debuggerSession.lastUsedAt = Date.now() - 100000; stub.alarms.onAlarm.dispatch({ name: 'debugger-idle' });");
  await waitFor(() => evalInWorker("return stub.areas.local.data.debuggerTabId === null"), 5000, "idle detach");
  check("worker: idle alarm released the debugger", (await evalInWorker("return stub.debugger._state.attached.size")) === 0);
  await send({ type: "automation_action", action: "press_key", key: "Escape" });
  await evalInWorker("stub.alarms.alarms.delete('debugger-idle'); stub.alarms.onAlarm.dispatch({ name: 'debugger-idle' });");
  await sleep(50);
  check("worker: idle alarm re-armed when recently used", (await evalInWorker("return stub.alarms.alarms.has('debugger-idle')")) && (await evalInWorker("return stub.debugger._state.attached.size")) === 1);
  r = await send({ type: "release_debugger" });
  check("worker: release_debugger", r.ok && (await evalInWorker("return stub.debugger._state.attached.size")) === 0 && (await evalInWorker("return stub.areas.local.data.debuggerTabId")) === null);
  await send({ type: "automation_action", action: "press_key", key: "Escape" });
  await evalInWorker("stub.debugger._state.attached.clear(); stub.debugger.onDetach.dispatch({ tabId: 102 }, 'target_closed');");
  await sleep(50);
  check("worker: onDetach resets the session", (await evalInWorker("return debuggerSession.tabId")) === null && (await evalInWorker("return stub.areas.local.data.debuggerTabId")) === null);
  await evalInWorker("stub.debugger._state.attached.add(102); await chrome.storage.local.set({ debuggerTabId: 102 }); await restoreDebuggerSession();");
  check("worker: restored session marked adopted", await evalInWorker("return debuggerSession.adopted === true && debuggerSession.tabId === 102"));
  const attachesBefore = await evalInWorker("return stub.calls.filter((c) => c.api === 'debugger.attach').length");
  r = await send({ type: "automation_action", action: "press_key", key: "Escape" });
  check("worker: adopted session reused without attach", r.ok && (await evalInWorker("return stub.calls.filter((c) => c.api === 'debugger.attach').length")) === attachesBefore && (await evalInWorker("return debuggerSession.adopted")) === false, JSON.stringify(r));

  await evalInWorker("stub.tabs._tabs.list.find((t) => t.id === 102).url = 'chrome://newtab/'; stub.tabs.onUpdated.dispatch(102, { url: 'chrome://newtab/' }, { id: 102, url: 'chrome://newtab/', status: 'complete' });");
  await waitFor(() => evalInWorker("return stub.areas.local.data.debuggerTabId === null"), 5000, "release on navigation");
  check("worker: navigation to internal URL releases the debugger", (await evalInWorker("return stub.debugger._state.attached.size")) === 0);
  r = await send({ type: "get_target_tab" });
  check("worker: internal tab dropped, falls back to tab 101", r.tab?.id === 101 && r.tabs.length === 1, JSON.stringify(r));

  const socketsBefore = await evalInWorker("return stub.sockets.length");
  await evalInWorker("await chrome.storage.local.set({ bridgePort: 23456 });");
  await waitFor(() => evalInWorker(`return stub.sockets.length > ${socketsBefore}`), 5000, "reconnect socket");
  check("worker: port change reconnects with the new port",
    await evalInWorker("return stub.sockets.at(-1).url") === "ws://127.0.0.1:23456/ws" && await evalInWorker("return stub.calls.filter((c) => c.api === 'fetch').at(-1).args[0]") === "http://127.0.0.1:23456/health");
  await evalInWorker("stub.sockets.at(-1)._open();");
  r = await send({ type: "reconnect_bridge" });
  await waitFor(() => evalInWorker(`return stub.sockets.length > ${socketsBefore + 1}`), 5000, "reconnect again");
  check("worker: reconnect_bridge opens a new socket", r.ok === true);
  await evalInWorker("stub.sockets.at(-1)._open();");
  await waitFor(() => evalInWorker("return stub.areas.local.data.bridgeState === 'connected'"), 5000, "connected again");
  await evalInWorker("stub.health.available = false; stub.sockets.at(-1).close(1006, 'gone');");
  await waitFor(() => evalInWorker("return stub.areas.local.data.bridgeState === 'retrying'"), 5000, "retrying after close");
  check("worker: close schedules the reconnect alarm", await evalInWorker("return stub.alarms.alarms.has('bridge-reconnect')"));

  await evalInWorker("stub.tabs._remove(101);");
  await sleep(50);
  r = await send({ type: "get_target_tab" });
  check("worker: removed tab clears the target", r.tab === null && r.tabs.length === 0, JSON.stringify(r));
  r = await send({ type: "automation_action", action: "click", selector: "#a" });
  check("worker: action without target errors", r.ok === false && /No target tab/.test(r.error), JSON.stringify(r));
  return true;
}

async function loadScript(src) {
  await new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = src;
    script.onload = resolve;
    script.onerror = () => reject(new Error(`${src} failed to load`));
    document.head.append(script);
  });
}

async function runPopupChecks() {
  const html = readText("../ext/popup.html");
  const doc = new DOMParser().parseFromString(html, "text/html");
  check("popup: title and heading are AutoIPO", doc.title === "AutoIPO" && doc.querySelector("h1").textContent === "AutoIPO");
  const scripts = [...doc.querySelectorAll("script")].map((script) => script.getAttribute("src"));
  check("popup: page-scan.js loads before popup.js", scripts.indexOf("page-scan.js") >= 0 && scripts.indexOf("page-scan.js") < scripts.indexOf("popup.js"), scripts.join(","));
  document.body.append(doc.querySelector("main"));
  const errorsBefore = results.length;
  await loadScript("../ext/popup.js");
  await sleep(200);
  check("popup: loaded without page errors", !results.slice(errorsBefore).some((line) => line.startsWith("PAGE")), results.slice(errorsBefore).join(" | "));
  const text = (selector) => document.querySelector(selector).textContent.trim();
  check("popup: bridge status text", text("#bridge-status-text") === "Bridge unavailable · retrying shortly", text("#bridge-status-text"));
  check("popup: no target tab status", text("#target-status").startsWith("No target tab"), text("#target-status"));
  check("popup: website url loaded", document.querySelector("#website-url").value === "https://foo.example/x", document.querySelector("#website-url").value);

  document.querySelector("#website-url").value = "https://example.com/open";
  document.querySelector("#open-site").click();
  await waitFor(() => text("#target-status").startsWith("Target tab 103"), 5000, "target after open");
  check("popup: target status shows id and host", text("#target-status") === "Target tab 103 · example.com", text("#target-status"));
  await waitFor(() => document.querySelector("#extension-tabs .tab-select"), 5000, "tab row");
  const tabButton = document.querySelector("#extension-tabs .tab-select");
  check("popup: tab row has full URL tooltip and selected state", tabButton.title === "https://example.com/open" && tabButton.classList.contains("selected") && tabButton.getAttribute("aria-pressed") === "true", `${tabButton.title} ${tabButton.className}`);

  document.querySelector("#scan-page").click();
  await waitFor(() => document.querySelectorAll("#page-elements li").length > 0, 5000, "scan rows");
  const rows = document.querySelectorAll("#page-elements li");
  const firstCode = rows[0].querySelector("code.element-selector");
  check("popup: scan rows show selector code with tooltip", firstCode && !firstCode.hidden && firstCode.title === firstCode.textContent, firstCode && firstCode.outerHTML);
  check("popup: no selector/URL toggles remain", !document.querySelector(".selector-toggle, .tab-url-toggle"));
  const total = rows.length;
  document.querySelector("#element-filter").value = "full name";
  document.querySelector("#element-filter").dispatchEvent(new Event("input"));
  const filtered = document.querySelectorAll("#page-elements li");
  check("popup: filter narrows rows and shows count", filtered.length >= 1 && filtered.length < total && text("#element-count") === `${filtered.length} of ${total} shown`, `${filtered.length}/${total} ${text("#element-count")}`);
  const nameRow = [...filtered].find((row) => row.querySelector("code").textContent === "#f-name");
  const useForText = nameRow ? [...nameRow.querySelectorAll("button")].find((button) => button.textContent === "Use for text") : null;
  if (useForText) useForText.click();
  check("popup: Use for text fills the selector input", Boolean(useForText) && document.querySelector("#field-selector").value === "#f-name", document.querySelector("#field-selector").value);
  document.querySelector("#element-filter").value = "";
  document.querySelector("#element-filter").dispatchEvent(new Event("input"));

  await evalInWorker("stub.health.available = true; await connectBridge({ force: true });");
  await waitFor(() => evalInWorker("return stub.sockets.at(-1).readyState === 0"), 5000, "fresh socket");
  await evalInWorker("stub.sockets.at(-1)._open();");
  await waitFor(() => text("#bridge-status-text") === "Bridge connected", 5000, "popup connected status");
  check("popup: bridge status updates to connected", document.querySelector("#bridge-status").classList.contains("connected"));
  await evalInWorker("stub.debugger._state.delayNext = 400;");
  const busyRun = evalInWorker("stub.sockets.at(-1)._receive(JSON.stringify({ type: 'screenshot', id: 'b1' }));");
  await waitFor(() => !document.querySelector("#automation-banner").hidden, 5000, "banner");
  check("popup: busy banner shown and action buttons disabled", document.querySelector("#fill-field").disabled && document.querySelector("#scan-page").disabled && document.querySelector("#release-debugger").disabled && !document.querySelector("#clear-logs").disabled && !document.querySelector("#reconnect-bridge").disabled);
  const rowAction = [...document.querySelectorAll("#page-elements button")].find((button) => button.textContent === "Click" || button.textContent === "Select");
  check("popup: row action buttons disabled while busy", Boolean(rowAction) && rowAction.disabled);
  await busyRun;
  await waitFor(() => document.querySelector("#automation-banner").hidden, 5000, "banner hidden");
  check("popup: banner hidden and buttons enabled afterwards", !document.querySelector("#fill-field").disabled);
  await waitFor(() => /held on tab 103/.test(text("#debugger-status")), 5000, "debugger status");
  check("popup: debugger status shows tab", /held on tab 103/.test(text("#debugger-status")), text("#debugger-status"));
  document.querySelector("#release-debugger").click();
  await waitFor(() => text("#debugger-status") === "Browser control is not held.", 5000, "released status");
  check("popup: release button releases control", (await evalInWorker("return stub.debugger._state.attached.size")) === 0);

  document.querySelector("#field-selector").value = "#a";
  document.querySelector("#field-value").value = "typed";
  document.querySelector("#fill-field").click();
  await waitFor(() => text("#fill-status").startsWith("Text entered in tab 103"), 5000, "fill status");
  check("popup: fill status", text("#fill-status").startsWith("Text entered in tab 103"), text("#fill-status"));
  document.querySelector("#dropdown-selector").value = "#sel";
  document.querySelector("#load-picklist").click();
  await waitFor(() => text("#picklist-status") === "Loaded 1 choice.", 5000, "picklist");
  const options = document.querySelector("#field-options");
  options.value = "0";
  options.dispatchEvent(new Event("change"));
  document.querySelector("#select-dropdown-choice").click();
  await waitFor(() => text("#picklist-status").startsWith("Selected “One” in tab 103"), 5000, "choose status");
  check("popup: dropdown load + choose", true);
  document.querySelector("#dismiss-dropdown").click();
  await waitFor(() => text("#picklist-status").startsWith("Escape sent"), 5000, "escape status");
  check("popup: dismiss sends Escape", (await commands()).some((c) => c[0] === "Input.dispatchKeyEvent" && c[1].key === "Escape"));

  await waitFor(() => document.querySelectorAll("#command-logs li:not(.empty-state)").length > 0, 5000, "logs");
  document.querySelector("#clear-logs").click();
  await waitFor(() => document.querySelector("#command-logs .empty-state"), 5000, "logs cleared");
  check("popup: clear logs via worker", (await evalInWorker("return stub.areas.local.data.commandLogs")).length === 0);
  check("popup: popup never wrote commandLogs directly", !window.__stub.calls.some((call) => call.api === "storage.local.set" && Object.hasOwn(call.args[0], "commandLogs")));
  document.querySelector("#reconnect-bridge").click();
  await waitFor(() => text("#controls-status").startsWith("Reconnecting"), 5000, "reconnect status");
  check("popup: reconnect button", true);
  document.querySelector("#open-options").click();
  await sleep(20);
  check("popup: settings button opens options", window.__stub.calls.some((call) => call.api === "runtime.openOptionsPage"));
}

async function runOptionsChecks() {
  const frame = document.createElement("iframe");
  frame.src = "options.html";
  frame.style.width = "800px";
  frame.style.height = "600px";
  const received = new Promise((resolve) => {
    window.addEventListener("message", (event) => {
      if (event.data && event.data.kind === "options-results") resolve(event.data.results);
    });
  });
  document.body.append(frame);
  const lines = await Promise.race([received, sleep(15000).then(() => ["FAIL options: no results from iframe"])]);
  results.push(...lines);
}

async function main() {
  await runScannerChecks();
  const workerOk = await runWorkerChecks();
  if (workerOk) await runPopupChecks();
  await runOptionsChecks();
}

main()
  .catch((error) => results.push(`FATAL ${error && (error.stack || error.message || error)}`))
  .finally(async () => {
    const passed = results.filter((line) => line.startsWith("PASS")).length;
    const failed = results.length - passed;
    const text = `${results.join("\n")}\n\nSUMMARY: ${passed} passed, ${failed} failed/other`;
    document.querySelector("#results").textContent = text;
    document.title = "HARNESS DONE";
  });
