importScripts("page-scan.js");

const DEFAULT_WEBSITE_URL = "https://example.com/";
const BRIDGE_URL = "ws://127.0.0.1:18765/ws";
const RECONNECT_ALARM = "bridge-reconnect";
const RECONNECT_DELAY_MS = 5000;
const RECONNECT_ALARM_DELAY_MINUTES = 0.5;
let bridgeSocket;
let connectionAttempt = 0;
let reconnectTimer;
let connectionCheckInProgress = false;
let logQueue = Promise.resolve();

void chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch((error) => {
  console.error("Could not enable the side panel toolbar action:", error);
});

chrome.runtime.onInstalled.addListener(() => {
  void chrome.storage.local.remove("bridgeToken");
  connectBridge();
});
chrome.runtime.onStartup.addListener(connectBridge);
chrome.tabs.onRemoved.addListener((tabId) => {
  void chrome.storage.local.get({ targetTabId: null, targetTabs: [] }).then(({ targetTabId, targetTabs }) => {
    const remainingTabs = targetTabs.filter((tab) => tab.id !== tabId);
    if (targetTabId !== tabId) return chrome.storage.local.set({ targetTabs: remainingTabs });
    const nextTab = remainingTabs[remainingTabs.length - 1];
    if (nextTab) {
      return chrome.storage.local.set({ targetTabs: remainingTabs, targetTabId: nextTab.id, targetTabUrl: nextTab.url || "" });
    }
    return chrome.storage.local.set({ targetTabs: [] }).then(() => chrome.storage.local.remove(["targetTabId", "targetTabUrl"]));
  });
});
chrome.tabs.onCreated.addListener((tab) => {
  if (!Number.isInteger(tab.openerTabId) || !Number.isInteger(tab.id)) return;
  if (isChromeInternalURL(tab.url || tab.pendingUrl || "")) return;
  void chrome.storage.local.get({ targetTabs: [] }).then(({ targetTabs }) => {
    if (!targetTabs.some((entry) => entry.id === tab.openerTabId) || targetTabs.some((entry) => entry.id === tab.id)) return;
    targetTabs.push({ id: tab.id, url: tab.url || tab.pendingUrl || "", title: tab.title || "" });
    return chrome.storage.local.set({ targetTabs });
  });
});
chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  if (!changeInfo.url && !changeInfo.title && changeInfo.status !== "complete") return;
  void chrome.storage.local.get({ targetTabs: [], targetTabId: null }).then(({ targetTabs, targetTabId }) => {
    if (isChromeInternalURL(tab.url || tab.pendingUrl || "")) {
      const remainingTabs = targetTabs.filter((item) => item.id !== tabId);
      if (targetTabId !== tabId) return chrome.storage.local.set({ targetTabs: remainingTabs });
      const nextTab = remainingTabs[remainingTabs.length - 1];
      if (nextTab) return chrome.storage.local.set({ targetTabs: remainingTabs, targetTabId: nextTab.id, targetTabUrl: nextTab.url || "" });
      return chrome.storage.local.set({ targetTabs: [] }).then(() => chrome.storage.local.remove(["targetTabId", "targetTabUrl"]));
    }
    const entry = targetTabs.find((item) => item.id === tabId);
    if (!entry) return;
    entry.url = tab.url || entry.url;
    entry.title = tab.title || entry.title;
    const values = { targetTabs };
    if (targetTabId === tabId) values.targetTabUrl = entry.url;
    return chrome.storage.local.set(values);
  });
});

function isChromeInternalURL(url) {
  return /^chrome:\/\//i.test(url);
}
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message?.type === "wake_bridge") connectBridge();
  if (message?.type === "automation_action") {
    executeAutomationAction(message.action, message.selector, message.value)
      .then(sendResponse)
      .catch(error => sendResponse({ ok: false, error: error?.message || String(error) }));
    return true;
  }
});
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === RECONNECT_ALARM) connectBridge();
});

connectBridge();

async function connectBridge() {
  if (bridgeSocket && (bridgeSocket.readyState === WebSocket.OPEN || bridgeSocket.readyState === WebSocket.CONNECTING)) {
    return;
  }
  if (connectionCheckInProgress) return;
  connectionCheckInProgress = true;
  clearTimeout(reconnectTimer);
  void chrome.alarms.clear(RECONNECT_ALARM);
  connectionAttempt += 1;
  void setBridgeState("connecting");

  const healthCheck = await checkBridgeHealth();
  if (!healthCheck.available) {
    await setBridgeState("retrying", `CLI bridge health check failed (${healthCheck.detail}); retrying in 5 seconds.`);
    connectionCheckInProgress = false;
    scheduleReconnect();
    return;
  }
  connectionCheckInProgress = false;

  const socket = new WebSocket(BRIDGE_URL);
  bridgeSocket = socket;

  socket.addEventListener("open", () => {
    if (bridgeSocket !== socket) return;
    connectionAttempt = 0;
    void chrome.alarms.clear(RECONNECT_ALARM);
    void setBridgeState("connected", "Connected to CLI bridge");
  });

  socket.addEventListener("message", (event) => {
    if (bridgeSocket !== socket) return;
    void handleBridgeMessage(socket, event.data);
  });

  socket.addEventListener("close", (event) => {
    if (bridgeSocket !== socket) return;
    bridgeSocket = undefined;
    const reason = event.reason ? `: ${event.reason}` : "";
    void setBridgeState("retrying", `Bridge disconnected (code ${event.code}${reason}). Retrying in 5 seconds.`);
    scheduleReconnect();
  });

  socket.addEventListener("error", () => {
    if (bridgeSocket !== socket) return;
    socket.close();
  });
}

async function checkBridgeHealth() {
  try {
    const response = await fetch("http://127.0.0.1:18765/health", {
      cache: "no-store",
      signal: AbortSignal.timeout(2000),
    });
    if (response.ok) return { available: true };
    const responseDetail = (await response.text()).trim();
    return {
      available: false,
      detail: `/health returned HTTP ${response.status}${responseDetail ? `: ${responseDetail}` : ""}`,
    };
  } catch (error) {
    // A refused loopback probe is expected while the CLI bridge is stopped.
    return { available: false, detail: error?.message || "loopback request failed" };
  }
}

function scheduleReconnect() {
  reconnectTimer = setTimeout(connectBridge, RECONNECT_DELAY_MS);
  void chrome.alarms.create(RECONNECT_ALARM, { delayInMinutes: RECONNECT_ALARM_DELAY_MINUTES });
}

async function setBridgeState(state, logMessage) {
  const badge = {
    connecting: { text: "...", color: "#b7791f", title: "Connecting to CLI bridge" },
    connected: { text: "✓", color: "#16834a", title: "Connected to CLI bridge" },
    retrying: { text: "!", color: "#c0392b", title: "CLI bridge unavailable; retrying shortly" },
  }[state];
  const { lastBridgeFailureLogAt = 0 } = await chrome.storage.local.get({ lastBridgeFailureLogAt: 0 });
  await chrome.storage.local.set({ bridgeState: state, bridgeConnected: state === "connected" });
  await chrome.action.setBadgeText({ text: badge.text });
  await chrome.action.setBadgeBackgroundColor({ color: badge.color });
  await chrome.action.setTitle({ title: badge.title });
  if (state === "connected") {
    await chrome.storage.local.set({ lastBridgeFailureLogAt: 0 });
    await recordEvent(logMessage);
  } else if (state === "retrying" && logMessage && Date.now() - lastBridgeFailureLogAt >= 60000) {
    await chrome.storage.local.set({ lastBridgeFailureLogAt: Date.now() });
    await recordEvent(logMessage);
  }
}

async function handleBridgeMessage(socket, rawMessage) {
  let message;
  try {
    message = JSON.parse(rawMessage);
  } catch {
    await recordEvent("Received an invalid bridge message");
    return;
  }

  if (message.type === "heartbeat") {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "heartbeat_ack" }));
    }
    return;
  }

  await recordEvent(`[Request from CLI bridge] -> ${formatBridgeCommand(message)}`);
  const ack = { type: "ack", id: message.id, ok: true };
  try {
    if (message.type === "set_url") {
      await chrome.storage.sync.set({ websiteUrl: message.url });
    } else if (message.type === "open") {
      const { websiteUrl = DEFAULT_WEBSITE_URL } = await chrome.storage.sync.get({
        websiteUrl: DEFAULT_WEBSITE_URL,
      });
      const tab = await chrome.tabs.create({ url: websiteUrl, active: true });
      await chrome.storage.local.set({ targetTabId: tab.id, targetTabUrl: websiteUrl });
      ack.output = `opened tab ${tab.id}`;
    } else if (["click", "fill", "dropdown", "choose", "list_selectors", "page_contains"].includes(message.type)) {
      const result = await executeAutomationAction(message.type, message.selector, message.value);
      if (result.elements) ack.elements = result.elements;
      if (result.total !== undefined) ack.output = `${result.total} interactive elements found`;
      if (result.output !== undefined) ack.output = result.output;
      if (result.choices) {
        ack.options = result.choices.map(choice => `${choice.label}${choice.value ? ` [${choice.value}]` : ""}`);
        ack.output = `${result.choices.length} choices`;
      }
    } else {
      throw new Error(`Unsupported command: ${message.type}`);
    }
  } catch (error) {
    ack.ok = false;
    ack.error = String(error);
    await recordEvent(`[Response from Extension worker] <- FAIL ${ack.error}`);
  }

  if (ack.ok) await recordEvent(`[Response from Extension worker] <- OK${ack.output ? ` ${ack.output}` : ""}`);

  if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(ack));
}

function formatBridgeCommand(message) {
  if (message.type === "set_url") return `set-url ${JSON.stringify(message.url || "")}`;
  if (message.type === "fill") return `fill ${JSON.stringify(message.selector || "")} [value redacted]`;
  if (message.type === "click" || message.type === "dropdown") return `${message.type} ${JSON.stringify(message.selector || "")}`;
  if (message.type === "choose") return `choose ${JSON.stringify(message.selector || "")} ${JSON.stringify(message.value || "")}`;
  if (message.type === "list_selectors") return "list-selectors";
  if (message.type === "page_contains") return `page-contains ${JSON.stringify(message.selector || "")}`;
  return message.type || "unknown";
}

async function executeAutomationAction(action, selector, value = "") {
  if (action !== "list_selectors" && (typeof selector !== "string" || (action !== "escape" && !selector.trim()))) throw new Error("A CSS selector is required.");
  const tabId = await getAutomationTargetTab();
  if (action === "list_selectors") {
    const [scan] = await chrome.scripting.executeScript({ target: { tabId }, func: inspectPageElements });
    const result = scan?.result || { elements: [], total: 0 };
    await recordEvent(`[Response from Extension worker] <- OK list-selectors found ${result.total} interactive elements in tab ${tabId}`);
    return { ok: true, tabId, elements: result.elements, total: result.total };
  }
  if (action === "page_contains") {
    const [scan] = await chrome.scripting.executeScript({
      target: { tabId },
      func: (cssSelector) => {
        try { return { exists: document.querySelector(cssSelector) !== null }; }
        catch (error) { return { error: `Invalid CSS selector: ${error.message}` }; }
      },
      args: [selector],
    });
    if (scan?.result?.error) throw new Error(scan.result.error);
    return { ok: true, tabId, output: String(Boolean(scan?.result?.exists)) };
  }
  if (action === "escape") {
    await withAutomationDebugger(tabId, () => dispatchAutomationKey(tabId, "Escape", "Escape"));
    return { ok: true, tabId };
  }
  if (action === "choose") {
    const [nativeChoice] = await chrome.scripting.executeScript({
      target: { tabId },
      func: (cssSelector, requested) => {
        let element;
        try { element = document.querySelector(cssSelector); } catch { return null; }
        if (!(element instanceof HTMLSelectElement)) return null;
        const option = [...element.options].find(item => item.value === requested || (item.label || item.textContent || "").trim() === requested);
        return option ? { value: option.value } : { error: `No option label or value matches "${requested}".` };
      },
      args: [selector, value],
    });
    if (nativeChoice?.result?.error) throw new Error(nativeChoice.result.error);
    if (nativeChoice?.result?.value !== undefined) value = nativeChoice.result.value;
  }
  const injection = await waitForElementActionInfo(
    tabId,
    selector,
    action === "fill" ? "text" : action === "choose" ? "choose" : "click",
    value,
    15000,
  );
  if (!injection?.result?.ok) throw new Error(injection?.result?.error || "The element could not be inspected.");
  const info = injection.result;

  if (action === "dropdown") {
    let choices;
    if (info.tag === "select") {
      const [choiceResult] = await chrome.scripting.executeScript({ target: { tabId }, func: readNativeSelectOptions, args: [selector] });
      choices = choiceResult?.result?.choices || [];
    } else {
      choices = await withAutomationDebugger(tabId, async () => {
        await dispatchAutomationClick(tabId, info.x, info.y);
        return await waitForAutocompleteOptions(tabId, selector, 4000);
      });
    }
    await recordEvent(`Loaded ${choices.length} dropdown choices for ${selector} in tab ${tabId}`);
    return { ok: true, tabId, choices };
  }

  if (action === "click") {
    await withAutomationDebugger(tabId, () => dispatchAutomationClick(tabId, info.x, info.y));
    await recordEvent(`Clicked ${info.element} matching ${selector} in tab ${tabId}`);
    return { ok: true, tabId };
  }

  if (action === "fill") {
    await withAutomationDebugger(tabId, async () => {
      await dispatchAutomationClick(tabId, info.x, info.y);
      await clearAutomationField(tabId);
      const characters = [...value];
      for (let index = 0; index < characters.length; index += 1) {
        await chrome.debugger.sendCommand({ tabId }, "Input.insertText", { text: characters[index] });
        if (index < characters.length - 1) {
          await new Promise(resolve => setTimeout(resolve, randomTypingDelay()));
        }
      }
    });
    await recordEvent(`Filled ${info.element} matching ${selector} in tab ${tabId}; value omitted`);
    return { ok: true, tabId };
  }

  if (action === "choose") {
    await withAutomationDebugger(tabId, async () => {
      await dispatchAutomationClick(tabId, info.x, info.y);
      if (info.optionIndex !== undefined) {
        await dispatchAutomationKey(tabId, "Home", "Home");
        for (let index = 0; index < info.optionIndex; index += 1) await dispatchAutomationKey(tabId, "ArrowDown", "ArrowDown");
        await dispatchAutomationKey(tabId, "Enter", "Enter");
      } else {
        const option = await waitForAutocompleteChoice(tabId, selector, value, 4000);
        if (!option) throw new Error("The selected value did not appear in the target dropdown. Load choices again after opening it.");
        await chooseAutocompleteOption(tabId, option);
      }
    });
    await recordEvent(`Selected dropdown choice for ${selector} in tab ${tabId}`);
    return { ok: true, tabId };
  }
  throw new Error(`Unsupported automation action: ${action}`);
}

async function waitForElementActionInfo(tabId, selector, action, value, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let injection;
  do {
    [injection] = await chrome.scripting.executeScript({
      target: { tabId },
      func: getElementActionInfo,
      args: [selector, action, value],
    });
    if (injection?.result?.ok) return injection;
    const error = injection?.result?.error || "The element could not be inspected.";
    if (error === "That CSS selector is invalid." || !isRetryableElementError(error) || Date.now() >= deadline) {
      return injection;
    }
    await new Promise(resolve => setTimeout(resolve, 250));
  } while (Date.now() < deadline);
  return injection;
}

function isRetryableElementError(error) {
  return error === "No field matches that selector." ||
    error === "That element is not visible." ||
    error === "That element cannot be interacted with.";
}

async function getAutomationTargetTab() {
  const { targetTabId, targetTabUrl = "" } = await chrome.storage.local.get({ targetTabId: null, targetTabUrl: "" });
  if (!Number.isInteger(targetTabId)) throw new Error("No target tab is open. Open a website from this panel first.");
  let tab;
  try { tab = await chrome.tabs.get(targetTabId); }
  catch { throw new Error(`Target tab ${targetTabId} was closed. Open a fresh website from this panel to continue.`); }
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    if (tab.status !== "loading" && tab.url && /^https?:/.test(tab.url)) return tab.id;
    if (tab.status !== "loading" && tab.url && tab.url !== "about:blank" && !/^https?:/.test(tab.url)) {
      throw new Error(`Target tab ${tab.id} finished loading ${tab.url}, which is not a regular website page.`);
    }
    await new Promise(resolve => setTimeout(resolve, 250));
    try { tab = await chrome.tabs.get(targetTabId); }
    catch { throw new Error(`Target tab ${targetTabId} was closed. Open a fresh website from this panel to continue.`); }
    if ((!tab.url || tab.url === "about:blank") && /^https?:/.test(targetTabUrl)) tab.url = targetTabUrl;
  }
  throw new Error(`Target tab ${targetTabId} did not finish loading a website within 30 seconds.`);
}

async function withAutomationDebugger(tabId, action) {
  const target = { tabId };
  await chrome.debugger.attach(target, "1.3");
  try { return await action(); }
  finally { await chrome.debugger.detach(target).catch(() => {}); }
}

async function dispatchAutomationClick(tabId, x, y) {
  const target = { tabId };
  await chrome.debugger.sendCommand(target, "Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
  await chrome.debugger.sendCommand(target, "Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await chrome.debugger.sendCommand(target, "Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
}

async function dispatchAutomationKey(tabId, key, code) {
  const target = { tabId };
  await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "rawKeyDown", key, code });
  await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "keyUp", key, code });
}

async function clearAutomationField(tabId) {
  const selectAllModifier = navigator.platform.includes("Mac") ? 4 : 2;
  await chrome.debugger.sendCommand({ tabId }, "Input.dispatchKeyEvent", { type: "rawKeyDown", key: "a", code: "KeyA", modifiers: selectAllModifier });
  await chrome.debugger.sendCommand({ tabId }, "Input.dispatchKeyEvent", { type: "keyUp", key: "a", code: "KeyA", modifiers: selectAllModifier });
  await dispatchAutomationKey(tabId, "Backspace", "Backspace");
}

function randomTypingDelay() { return Math.floor(Math.random() * 1001); }

function getElementActionInfo(selector, action, desiredValue) {
  let matches;
  try { matches = document.querySelectorAll(selector); }
  catch { return { ok: false, error: "That CSS selector is invalid." }; }
  if (!matches.length) return { ok: false, error: "No field matches that selector." };
  if (matches.length > 1) return { ok: false, error: `That selector matches ${matches.length} elements; make it more specific.` };
  const element = matches[0];
  const tag = element.tagName.toLowerCase();
  if (action !== "click" && (element.disabled || element.readOnly)) return { ok: false, error: "That field is disabled or read-only." };
  if ((action === "text" || action === "choose") && tag === "input") {
    const unsupportedTypes = new Set(["button", "checkbox", "file", "hidden", "image", "radio", "reset", "submit"]);
    if (unsupportedTypes.has(element.type)) return { ok: false, error: `Input type "${element.type}" is not supported.` };
  }
  let optionIndex;
  if (action === "choose" && tag === "select") {
    optionIndex = [...element.options].findIndex(option => option.value === desiredValue);
    if (optionIndex < 0) return { ok: false, error: "No option has that exact value." };
  } else if (action === "text" && !(tag === "input" || tag === "textarea" || element.isContentEditable)) {
    return { ok: false, error: "Text filling supports inputs, textareas, and editable elements. Use Choose from dropdown for picklists." };
  } else if (action === "choose" && tag !== "input") {
    return { ok: false, error: "Dropdown selection supports a select element or a text input with visible choices." };
  }
  if (!(element instanceof HTMLElement) || element.disabled) return { ok: false, error: "That element cannot be interacted with." };
  element.scrollIntoView({ block: "center", inline: "center" });
  const rect = element.getBoundingClientRect();
  if (!rect.width || !rect.height) return { ok: false, error: "That element is not visible." };
  return { ok: true, x: rect.left + rect.width / 2, y: rect.top + rect.height / 2, tag,
    ...(optionIndex === undefined ? {} : { optionIndex }),
    element: tag === "input" ? `input[type=${element.type}]` : tag };
}

function readNativeSelectOptions(selector) {
  const element = document.querySelector(selector);
  if (!(element instanceof HTMLSelectElement)) return { choices: [] };
  return { choices: [...element.options].filter(option => !option.disabled).map(option => ({
    label: (option.label || option.textContent || option.value).trim(), value: option.value,
  })) };
}

async function waitForAutocompleteOptions(tabId, selector, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const [result] = await chrome.scripting.executeScript({ target: { tabId }, func: getAutocompleteOption, args: [selector, ""] });
    if (result?.result?.options?.length) return result.result.options;
    await new Promise(resolve => setTimeout(resolve, 150));
  }
  return [];
}

async function waitForAutocompleteChoice(tabId, selector, desiredValue, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const [result] = await chrome.scripting.executeScript({ target: { tabId }, func: getAutocompleteOption, args: [selector, desiredValue] });
    if (result?.result?.option) return result.result.option;
    await new Promise(resolve => setTimeout(resolve, 150));
  }
  return null;
}

function getAutocompleteOption(selector, desiredValue) {
  const input = document.querySelector(selector);
  if (!(input instanceof HTMLInputElement)) return { option: null, options: [] };
  const options = [];
  const datalistId = input.getAttribute("list");
  if (datalistId) {
    const datalist = document.getElementById(datalistId);
    if (datalist instanceof HTMLDataListElement) options.push(...[...datalist.options].map(option => ({ label: option.label || option.value, value: option.value || option.label })));
  }
  const controlledIds = (input.getAttribute("aria-controls") || "").split(/\s+/).filter(Boolean);
  const controlledLists = controlledIds.map(id => document.getElementById(id)).filter(Boolean);
  const roots = controlledLists.length ? controlledLists : input.getAttribute("aria-expanded") === "true" ? [...document.querySelectorAll('[role="listbox"]')] : [];
  const seen = new Set();
  for (const root of roots) {
    const rootStyle = getComputedStyle(root);
    if (rootStyle.display === "none" || rootStyle.visibility === "hidden" || !root.getClientRects().length) continue;
    for (const option of root.querySelectorAll('[role="option"], [data-value], li, [class*="option"], [class*="suggest"]')) {
      if (seen.has(option)) continue;
      seen.add(option);
      const style = getComputedStyle(option), rect = option.getBoundingClientRect();
      if (!rect.width || !rect.height || style.display === "none" || style.visibility === "hidden") continue;
      options.push({ label: (option.innerText || option.textContent || "").trim() || option.getAttribute("data-value") || "",
        value: option.getAttribute("data-value") || (option.innerText || option.textContent || "").trim(), x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 });
    }
  }
  if (!options.some(option => option.x !== undefined)) {
    for (let ancestor = input.parentElement, depth = 0; ancestor && depth < 4; ancestor = ancestor.parentElement, depth += 1) {
      if (!/(auto|suggest|option|dropdown|list|pick)/.test(`${ancestor.id} ${ancestor.className}`.toLowerCase())) continue;
      for (const option of ancestor.querySelectorAll('li, [data-value], [class*="option"], [class*="suggest"]')) {
        if (seen.has(option)) continue;
        seen.add(option);
        const style = getComputedStyle(option), rect = option.getBoundingClientRect();
        if (!rect.width || !rect.height || style.display === "none" || style.visibility === "hidden") continue;
        options.push({ label: (option.innerText || option.textContent || "").trim() || option.getAttribute("data-value") || "",
          value: option.getAttribute("data-value") || (option.innerText || option.textContent || "").trim(), x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 });
      }
    }
  }
  const query = desiredValue.trim().toLocaleLowerCase();
  if (!query) return { option: null, options: options.filter(option => option.label).map(({ label, value }) => ({ label, value })) };
  const normalized = options.map(option => `${option.label} ${option.value || ""}`.trim().toLocaleLowerCase());
  let index = normalized.findIndex(option => option === query);
  if (index < 0) index = normalized.findIndex(option => option.startsWith(query));
  if (index < 0) index = normalized.findIndex(option => option.includes(query));
  return { option: index < 0 ? null : { index, ...options[index] }, options: options.map(({ label, value }) => ({ label, value })) };
}

async function chooseAutocompleteOption(tabId, option) {
  if (option.x !== undefined && option.y !== undefined) return dispatchAutomationClick(tabId, option.x, option.y);
  for (let index = 0; index <= option.index; index += 1) await dispatchAutomationKey(tabId, "ArrowDown", "ArrowDown");
  await dispatchAutomationKey(tabId, "Enter", "Enter");
}

async function recordEvent(message) {
  logQueue = logQueue.catch(() => {}).then(async () => {
    const { commandLogs = [] } = await chrome.storage.local.get({ commandLogs: [] });
    commandLogs.unshift({ message, time: new Date().toISOString() });
    await chrome.storage.local.set({ commandLogs: commandLogs.slice(0, 50) });
  });
  return logQueue;
}
