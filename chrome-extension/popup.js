// Side panel controller. Tab bookkeeping, activity logging and every
// automation action go through the service worker; this file renders state
// and forwards user intent. Requires shared.js and page-scan.js (loaded first).

const ui = {
  websiteForm: document.querySelector("#settings-form"),
  websiteInput: document.querySelector("#website-url"),
  saveStatus: document.querySelector("#save-status"),
  openSite: document.querySelector("#open-site"),
  openOptions: document.querySelector("#open-options"),
  bridgeStatus: document.querySelector("#bridge-status"),
  bridgeStatusText: document.querySelector("#bridge-status-text"),
  targetStatus: document.querySelector("#target-status"),
  banner: document.querySelector("#automation-banner"),
  extensionTabs: document.querySelector("#extension-tabs"),
  selectorInput: document.querySelector("#field-selector"),
  valueInput: document.querySelector("#field-value"),
  fillButton: document.querySelector("#fill-field"),
  clickButton: document.querySelector("#click-element"),
  fillStatus: document.querySelector("#fill-status"),
  dropdownSelectorInput: document.querySelector("#dropdown-selector"),
  loadPicklist: document.querySelector("#load-picklist"),
  selectChoice: document.querySelector("#select-dropdown-choice"),
  dismissDropdown: document.querySelector("#dismiss-dropdown"),
  fieldOptions: document.querySelector("#field-options"),
  picklistStatus: document.querySelector("#picklist-status"),
  scanButton: document.querySelector("#scan-page"),
  elementFilter: document.querySelector("#element-filter"),
  elementCount: document.querySelector("#element-count"),
  scanStatus: document.querySelector("#scan-status"),
  pageElements: document.querySelector("#page-elements"),
  clearLogs: document.querySelector("#clear-logs"),
  commandLogs: document.querySelector("#command-logs"),
  debuggerStatus: document.querySelector("#debugger-status"),
  releaseDebugger: document.querySelector("#release-debugger"),
  reconnectBridge: document.querySelector("#reconnect-bridge"),
  controlsStatus: document.querySelector("#controls-status"),
};

const BRIDGE_LABELS = Object.freeze({
  connected: "Bridge connected",
  connecting: "Connecting to bridge…",
  retrying: "Bridge unavailable · retrying shortly",
});
const MAX_VISIBLE_LOGS = 12;

const state = {
  busy: false,
  displayedTargetKey: "",
  scannedElements: [],
  scanTotal: 0,
  loadedChoices: [],
};

init();

function init() {
  bindEvents();
  sendToWorker({ type: "wake_bridge" }).catch(() => {});
  void loadWebsiteUrl();
  void renderStoredStatus();
  void renderLogsFromStorage();
  void refreshTargetTab();
}

// ---------------------------------------------------------------------------
// Worker messaging
// ---------------------------------------------------------------------------

async function sendToWorker(message) {
  let response;
  try {
    response = await chrome.runtime.sendMessage(message);
  } catch (error) {
    throw new Error(errorText(error, "The extension worker is not responding."));
  }
  if (!response || response.ok !== true) throw new Error(response?.error || "The action could not be completed.");
  return response;
}

function runAction(action, fields = {}) {
  return sendToWorker({ type: "automation_action", action, ...fields });
}

function errorText(error, fallback) {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return fallback;
}

function setStatus(element, text) {
  element.textContent = text;
}

function flashStatus(element, text) {
  element.textContent = text;
  window.setTimeout(() => {
    if (element.textContent === text) element.textContent = "";
  }, 1800);
}

function requireSelector(input, message) {
  const selector = input.value.trim();
  if (!selector) {
    input.setCustomValidity(message);
    input.reportValidity();
    return "";
  }
  input.setCustomValidity("");
  return selector;
}

// ---------------------------------------------------------------------------
// Event wiring
// ---------------------------------------------------------------------------

function bindEvents() {
  ui.websiteForm.addEventListener("submit", (event) => {
    event.preventDefault();
    void saveWebsiteUrl();
  });
  ui.websiteInput.addEventListener("input", () => ui.websiteInput.setCustomValidity(""));
  ui.openSite.addEventListener("click", () => void openWebsite());
  ui.openOptions.addEventListener("click", () => {
    chrome.runtime.openOptionsPage().catch((error) => setStatus(ui.controlsStatus, errorText(error, "Could not open the settings page.")));
  });
  ui.fillButton.addEventListener("click", () => void fillField());
  ui.clickButton.addEventListener("click", () => void clickSelectedElement());
  ui.loadPicklist.addEventListener("click", () => void loadPicklist());
  ui.selectChoice.addEventListener("click", () => void selectPicklistChoice());
  ui.dismissDropdown.addEventListener("click", () => void dismissDropdown());
  ui.fieldOptions.addEventListener("change", () => {
    const choice = selectedChoice();
    if (choice) setStatus(ui.picklistStatus, `Selected “${choice.label}”. Click Select choice to choose it on the page.`);
  });
  ui.scanButton.addEventListener("click", () => void scanTargetPage());
  ui.elementFilter.addEventListener("input", renderElements);
  ui.clearLogs.addEventListener("click", () => {
    sendToWorker({ type: "clear_logs" }).catch((error) => setStatus(ui.controlsStatus, errorText(error, "Could not clear the activity log.")));
  });
  ui.releaseDebugger.addEventListener("click", () => void releaseBrowserControl());
  ui.reconnectBridge.addEventListener("click", () => void reconnectBridge());
  chrome.storage.onChanged.addListener(handleStorageChange);
}

function handleStorageChange(changes, area) {
  if (area !== "local") return;
  if (changes.websiteUrl && document.activeElement !== ui.websiteInput) {
    ui.websiteInput.value = changes.websiteUrl.newValue || AUTOIPO_DEFAULTS.websiteUrl;
  }
  if (changes.bridgeState) renderBridgeStatus(changes.bridgeState.newValue);
  if (changes.automationBusy) setBusy(Boolean(changes.automationBusy.newValue));
  if (changes.debuggerTabId) renderDebuggerStatus(changes.debuggerTabId.newValue);
  if (changes.commandLogs) renderLogs(changes.commandLogs.newValue);
  if (changes.targetTabId || changes.targetTabs || changes.targetTabUrl) void refreshTargetTab();
}

// ---------------------------------------------------------------------------
// Website URL
// ---------------------------------------------------------------------------

async function loadWebsiteUrl() {
  try {
    const { websiteUrl } = await loadSettings();
    ui.websiteInput.value = websiteUrl;
  } catch (error) {
    setStatus(ui.saveStatus, errorText(error, "Could not load the saved URL."));
  }
}

function validatedWebsiteUrl() {
  const parsed = parseWebsiteUrl(ui.websiteInput.value);
  if (parsed.error) {
    ui.websiteInput.setCustomValidity(parsed.error);
    ui.websiteInput.reportValidity();
    return "";
  }
  ui.websiteInput.setCustomValidity("");
  return parsed.href;
}

async function saveWebsiteUrl() {
  const href = validatedWebsiteUrl();
  if (!href) return;
  try {
    await chrome.storage.local.set({ websiteUrl: href });
    await sendToWorker({ type: "record_event", message: `URL saved from extension UI: ${href}` });
    flashStatus(ui.saveStatus, "Saved");
  } catch (error) {
    setStatus(ui.saveStatus, errorText(error, "Could not save the URL."));
  }
}

async function openWebsite() {
  const href = validatedWebsiteUrl();
  if (!href) return;
  try {
    await sendToWorker({ type: "open_website", url: href });
    setStatus(ui.saveStatus, "");
  } catch (error) {
    setStatus(ui.saveStatus, errorText(error, "Could not open the website."));
  }
}

// ---------------------------------------------------------------------------
// Status header, busy banner, browser control
// ---------------------------------------------------------------------------

async function renderStoredStatus() {
  try {
    const stored = await chrome.storage.local.get({ bridgeState: "connecting", automationBusy: false, debuggerTabId: null });
    renderBridgeStatus(stored.bridgeState);
    setBusy(Boolean(stored.automationBusy));
    renderDebuggerStatus(stored.debuggerTabId);
  } catch {
    renderBridgeStatus("connecting");
  }
}

function renderBridgeStatus(bridgeState) {
  const known = Object.hasOwn(BRIDGE_LABELS, bridgeState) ? bridgeState : "connecting";
  ui.bridgeStatus.classList.toggle("connected", known === "connected");
  ui.bridgeStatus.classList.toggle("retrying", known === "retrying");
  ui.bridgeStatusText.textContent = BRIDGE_LABELS[known];
}

function setBusy(busy) {
  state.busy = busy;
  ui.banner.hidden = !busy;
  for (const button of document.querySelectorAll("[data-action]")) button.disabled = busy;
}

function renderDebuggerStatus(tabId) {
  ui.debuggerStatus.textContent = Number.isInteger(tabId)
    ? `Browser control is held on tab ${tabId}; it is released automatically after 90 seconds without actions.`
    : "Browser control is not held.";
}

async function releaseBrowserControl() {
  try {
    await sendToWorker({ type: "release_debugger" });
    flashStatus(ui.controlsStatus, "Browser control released.");
  } catch (error) {
    setStatus(ui.controlsStatus, errorText(error, "Could not release browser control."));
  }
}

async function reconnectBridge() {
  try {
    await sendToWorker({ type: "reconnect_bridge" });
    flashStatus(ui.controlsStatus, "Reconnecting to the bridge…");
  } catch (error) {
    setStatus(ui.controlsStatus, errorText(error, "Could not reconnect to the bridge."));
  }
}

// ---------------------------------------------------------------------------
// Target tab and extension tabs
// ---------------------------------------------------------------------------

async function refreshTargetTab() {
  try {
    const { tab, tabs } = await sendToWorker({ type: "get_target_tab" });
    renderTargetStatus(tab);
    renderExtensionTabs(Array.isArray(tabs) ? tabs : [], tab ? tab.id : null);
    const key = tab ? `${tab.id}|${tab.url}` : "";
    if (key !== state.displayedTargetKey) {
      state.displayedTargetKey = key;
      clearScanResults();
    }
  } catch (error) {
    ui.targetStatus.textContent = errorText(error, "Could not read the target tab.");
    ui.targetStatus.removeAttribute("title");
  }
}

function renderTargetStatus(tab) {
  if (!tab) {
    ui.targetStatus.textContent = "No target tab — open a website below.";
    ui.targetStatus.removeAttribute("title");
    return;
  }
  ui.targetStatus.textContent = `Target tab ${tab.id}${tab.host ? ` · ${tab.host}` : ""}`;
  ui.targetStatus.title = tab.url || "";
}

function renderExtensionTabs(tabs, selectedTabId) {
  if (!tabs.length) {
    const empty = document.createElement("li");
    empty.className = "empty-state";
    empty.textContent = "No tabs opened by the extension yet.";
    ui.extensionTabs.replaceChildren(empty);
    return;
  }
  ui.extensionTabs.replaceChildren(...tabs.map((tab) => {
    const item = document.createElement("li");
    const button = document.createElement("button");
    const selected = tab.id === selectedTabId;
    button.type = "button";
    button.className = "tab-select";
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
    button.title = tab.url || "URL is still loading";
    button.textContent = `${tab.host || tab.title || "Loading…"} · ${tab.id}`;
    button.addEventListener("click", () => void selectTab(tab.id));
    item.append(button);
    return item;
  }));
}

async function selectTab(tabId) {
  try {
    await sendToWorker({ type: "select_target_tab", tabId });
  } catch (error) {
    ui.targetStatus.textContent = errorText(error, "Could not select that tab.");
  }
}

// ---------------------------------------------------------------------------
// Manual actions
// ---------------------------------------------------------------------------

async function fillField() {
  const selector = requireSelector(ui.selectorInput, "Enter a CSS selector for the field.");
  if (!selector) return;
  setStatus(ui.fillStatus, "Typing into the target tab…");
  try {
    const result = await runAction("fill", { selector, value: ui.valueInput.value });
    setStatus(ui.fillStatus, `Text entered in tab ${result.tabId}. Use the dropdown chooser for picklists.`);
  } catch (error) {
    setStatus(ui.fillStatus, errorText(error, "Could not fill that field."));
  }
}

async function clickSelectedElement() {
  const selector = requireSelector(ui.selectorInput, "Enter a CSS selector for the element.");
  if (!selector) return;
  setStatus(ui.fillStatus, "Clicking in the target tab…");
  try {
    const result = await runAction("click", { selector });
    setStatus(ui.fillStatus, `Browser click sent to the matching element in tab ${result.tabId}.`);
  } catch (error) {
    setStatus(ui.fillStatus, errorText(error, "Could not click that element."));
  }
}

async function loadPicklist() {
  const selector = requireSelector(ui.dropdownSelectorInput, "Enter a CSS selector for the dropdown field.");
  if (!selector) return;
  ui.fieldOptions.replaceChildren(new Option("Loading choices…", ""));
  setStatus(ui.picklistStatus, "Opening the target field…");
  try {
    const { choices } = await runAction("dropdown", { selector });
    state.loadedChoices = Array.isArray(choices) ? choices : [];
    ui.fieldOptions.replaceChildren();
    if (!state.loadedChoices.length) {
      ui.fieldOptions.append(new Option("No choices detected", ""));
      setStatus(ui.picklistStatus, "No visible choices found. Open the field or enter a search value, then load choices again.");
      return;
    }
    ui.fieldOptions.append(new Option("Choose a value…", ""));
    state.loadedChoices.forEach((choice, index) => ui.fieldOptions.append(new Option(choice.label, String(index))));
    const count = state.loadedChoices.length;
    setStatus(ui.picklistStatus, `Loaded ${count} choice${count === 1 ? "" : "s"}.`);
  } catch (error) {
    ui.fieldOptions.replaceChildren(new Option("Could not load choices", ""));
    setStatus(ui.picklistStatus, errorText(error, "Could not load choices."));
  }
}

function selectedChoice() {
  if (ui.fieldOptions.value === "") return null;
  const index = Number(ui.fieldOptions.value);
  return Number.isInteger(index) ? state.loadedChoices[index] || null : null;
}

async function selectPicklistChoice() {
  const selector = requireSelector(ui.dropdownSelectorInput, "Enter a CSS selector for the dropdown field.");
  if (!selector) return;
  const choice = selectedChoice();
  if (!choice) {
    setStatus(ui.picklistStatus, "Load the choices and select one first.");
    return;
  }
  setStatus(ui.picklistStatus, "Selecting the choice on the target page…");
  try {
    const result = await runAction("choose", { selector, value: choice.value });
    setStatus(ui.picklistStatus, `Selected “${choice.label}” in tab ${result.tabId}.`);
  } catch (error) {
    setStatus(ui.picklistStatus, errorText(error, "Could not select that dropdown choice."));
  }
}

async function dismissDropdown() {
  setStatus(ui.picklistStatus, "Sending Escape to the target page…");
  try {
    const result = await runAction("press_key", { key: "Escape" });
    setStatus(ui.picklistStatus, `Escape sent to the target page in tab ${result.tabId}.`);
  } catch (error) {
    setStatus(ui.picklistStatus, errorText(error, "Could not dismiss the dropdown."));
  }
}

// ---------------------------------------------------------------------------
// Page scan
// ---------------------------------------------------------------------------

async function scanTargetPage() {
  setStatus(ui.scanStatus, "Scanning target tab…");
  try {
    const { tab } = await sendToWorker({ type: "get_target_tab", waitForLoad: true });
    if (!tab) throw new Error("No target tab is open. Open a website from this panel first.");
    const { siteHints } = await loadSettings();
    const [injection] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: inspectPageElements,
      args: [siteHints],
    });
    const result = injection?.result || {};
    state.scannedElements = Array.isArray(result.elements) ? result.elements : [];
    state.scanTotal = Number.isInteger(result.total) ? result.total : state.scannedElements.length;
    renderElements();
    const total = state.scanTotal;
    const shown = state.scannedElements.length;
    setStatus(ui.scanStatus, total === 0
      ? "No visible clickable or fillable elements found."
      : `Found ${total} element${total === 1 ? "" : "s"}${total > shown ? `; showing the first ${shown}` : ""}.`);
    sendToWorker({ type: "record_event", message: `Scanned ${tab.host || tab.url} in tab ${tab.id}: ${total} interactive elements found` }).catch(() => {});
  } catch (error) {
    setStatus(ui.scanStatus, errorText(error, "Could not scan this page."));
  }
}

function clearScanResults() {
  state.scannedElements = [];
  state.scanTotal = 0;
  renderElements();
  setStatus(ui.scanStatus, "");
}

function renderElements() {
  const query = ui.elementFilter.value.trim().toLowerCase();
  const visible = query ? state.scannedElements.filter((element) => matchesFilter(element, query)) : state.scannedElements;
  ui.pageElements.replaceChildren(...visible.map(createElementRow));
  if (!state.scannedElements.length) {
    ui.elementCount.textContent = "";
    return;
  }
  ui.elementCount.textContent = query
    ? `${visible.length} of ${state.scannedElements.length} shown`
    : `${state.scannedElements.length} shown`;
}

function matchesFilter(element, query) {
  return [element.kind, element.label, element.description, element.context, element.selector]
    .some((field) => (field || "").toLowerCase().includes(query));
}

function createElementRow(element) {
  const item = document.createElement("li");
  const summary = document.createElement("div");
  summary.className = "element-summary";
  const title = document.createElement("span");
  title.className = "element-title";
  title.textContent = `${element.kind} · ${element.label || element.description}`;
  summary.append(title);
  if (element.context) {
    const context = document.createElement("span");
    context.className = "element-context";
    context.textContent = `For: ${element.context}`;
    summary.append(context);
  }
  const selector = document.createElement("code");
  selector.className = "element-selector";
  selector.textContent = element.selector;
  selector.title = element.selector;
  const actions = document.createElement("div");
  actions.className = "element-actions";
  const name = element.label || element.description;
  if (element.kind === "Fillable") {
    actions.append(
      createRowButton("Use for text", `Use ${name} for text entry`, false, () => {
        ui.selectorInput.value = element.selector;
        ui.selectorInput.setCustomValidity("");
        setStatus(ui.fillStatus, "Selector set for text entry. Enter a value and click Fill text.");
        ui.selectorInput.focus();
      }),
      createRowButton("Use for dropdown", `Use ${name} for dropdown choices`, false, () => {
        ui.dropdownSelectorInput.value = element.selector;
        ui.dropdownSelectorInput.setCustomValidity("");
        setStatus(ui.picklistStatus, "Selector set for dropdown choices. Click Load dropdown choices.");
        ui.dropdownSelectorInput.focus();
      }),
    );
  }
  const clickLabel = element.kind === "Choice" ? "Select" : "Click";
  actions.append(createRowButton(clickLabel, `${clickLabel} ${name}`, true, () => void clickScannedElement(element)));
  item.append(summary, selector, actions);
  return item;
}

function createRowButton(text, ariaLabel, isAction, onClick) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "text-button use-selector";
  button.textContent = text;
  button.setAttribute("aria-label", ariaLabel);
  if (isAction) {
    button.dataset.action = "";
    button.disabled = state.busy;
  }
  button.addEventListener("click", onClick);
  return button;
}

async function clickScannedElement(element) {
  ui.selectorInput.value = element.selector;
  ui.selectorInput.setCustomValidity("");
  setStatus(ui.fillStatus, "Clicking in the target tab…");
  try {
    const result = await runAction("click", { selector: element.selector });
    setStatus(ui.fillStatus, `Browser click sent to ${element.label || element.description} in tab ${result.tabId}.`);
    await scanTargetPage();
  } catch (error) {
    setStatus(ui.fillStatus, errorText(error, "Could not click that element."));
  }
}

// ---------------------------------------------------------------------------
// Activity log
// ---------------------------------------------------------------------------

async function renderLogsFromStorage() {
  try {
    const { commandLogs } = await chrome.storage.local.get({ commandLogs: [] });
    renderLogs(commandLogs);
  } catch {
    renderLogs([]);
  }
}

function renderLogs(entries) {
  const logs = Array.isArray(entries) ? entries : [];
  if (!logs.length) {
    const empty = document.createElement("li");
    empty.className = "empty-state";
    empty.textContent = "No activity yet.";
    ui.commandLogs.replaceChildren(empty);
    return;
  }
  ui.commandLogs.replaceChildren(...logs.slice(0, MAX_VISIBLE_LOGS).map((entry) => {
    const item = document.createElement("li");
    const time = document.createElement("time");
    time.dateTime = entry.time;
    time.textContent = new Date(entry.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
    const message = document.createElement("span");
    message.textContent = entry.message;
    item.append(time, message);
    return item;
  }));
}
