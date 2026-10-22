const DEFAULT_WEBSITE_URL = "https://example.com/";
const form = document.querySelector("#settings-form");
const websiteInput = document.querySelector("#website-url");
const saveStatus = document.querySelector("#save-status");
const selectorInput = document.querySelector("#field-selector");
const dropdownSelectorInput = document.querySelector("#dropdown-selector");
const valueInput = document.querySelector("#field-value");
const fieldOptions = document.querySelector("#field-options");
const picklistStatus = document.querySelector("#picklist-status");
const fillStatus = document.querySelector("#fill-status");
const scanStatus = document.querySelector("#scan-status");
const pageElementsList = document.querySelector("#page-elements");
const bridgeStatus = document.querySelector("#bridge-status");
const logsList = document.querySelector("#command-logs");
const extensionTabsList = document.querySelector("#extension-tabs");
let displayedTargetTabId = null;
let displayedTargetTabUrl = "";
let loadedChoices = [];

async function runUiAutomation(action, selector, value) {
  const response = await chrome.runtime.sendMessage({ type: "automation_action", action, selector, value });
  if (!response?.ok) throw new Error(response?.error || "The action could not be completed.");
  return response;
}

void chrome.runtime.sendMessage({ type: "wake_bridge" });
void loadSettings();
void renderActivity();
void renderTargetTabStatus();

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "sync" && changes.websiteUrl) {
    websiteInput.value = changes.websiteUrl.newValue || DEFAULT_WEBSITE_URL;
  }
  if (area === "local" && (changes.commandLogs || changes.bridgeConnected || changes.bridgeState)) {
    void renderActivity();
  }
  if (area === "local" && (changes.targetTabId || changes.targetTabs)) void renderTargetTabStatus();
});

chrome.tabs.onUpdated.addListener((tabId) => {
  void chrome.storage.local.get({ targetTabs: [], targetTabId: null }).then(({ targetTabs, targetTabId }) => {
    if (targetTabId === tabId || targetTabs.some((tab) => tab.id === tabId)) void renderTargetTabStatus();
  });
});

async function loadSettings() {
  const { websiteUrl = DEFAULT_WEBSITE_URL } = await chrome.storage.sync.get({
    websiteUrl: DEFAULT_WEBSITE_URL,
  });
  websiteInput.value = websiteUrl;
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const parsedUrl = parseWebsiteURL(websiteInput.value);
  if (!parsedUrl) return;

  await chrome.storage.sync.set({ websiteUrl: parsedUrl.href });
  await recordEvent(`URL saved from extension UI: ${parsedUrl.href}`);
  saveStatus.textContent = "Saved";
  window.setTimeout(() => {
    saveStatus.textContent = "";
  }, 1800);
});

document.querySelector("#open-site").addEventListener("click", async () => {
  const parsedUrl = parseWebsiteURL(websiteInput.value);
  if (!parsedUrl) return;
  await chrome.storage.sync.set({ websiteUrl: parsedUrl.href });
  const tab = await chrome.tabs.create({ url: parsedUrl.href, active: true });
  await registerExtensionTab(tab, true);
  await recordEvent(`Opened ${parsedUrl.href} in tab ${tab.id} from extension UI`);
});

document.querySelector("#fill-field").addEventListener("click", async () => {
  const selector = selectorInput.value.trim();
  if (!selector) {
    selectorInput.setCustomValidity("Enter a CSS selector for the field.");
    selectorInput.reportValidity();
    return;
  }
  selectorInput.setCustomValidity("");
  fillStatus.textContent = "";

  try {
    const result = await runUiAutomation("fill", selector, valueInput.value);
    fillStatus.textContent = `Text entered in tab ${result.tabId}. Use Choose from dropdown for picklists.`;
  } catch (error) {
    fillStatus.textContent = error?.message || "Could not fill that field.";
  }
});

async function registerExtensionTab(tab, select = false) {
  if (!Number.isInteger(tab?.id)) return;
  const { targetTabs = [] } = await chrome.storage.local.get({ targetTabs: [] });
  const nextTabs = targetTabs.filter((entry) => entry.id !== tab.id);
  nextTabs.push({ id: tab.id, url: tab.url || tab.pendingUrl || "", title: tab.title || "" });
  const values = { targetTabs: nextTabs };
  if (select) {
    values.targetTabId = tab.id;
    values.targetTabUrl = tab.url || tab.pendingUrl || "";
  }
  await chrome.storage.local.set(values);
}

async function selectExtensionTab(tabId) {
  const { targetTabs = [] } = await chrome.storage.local.get({ targetTabs: [] });
  const entry = targetTabs.find((tab) => tab.id === tabId);
  if (!entry) return;
  await chrome.storage.local.set({ targetTabId: tabId, targetTabUrl: entry.url || "" });
  await chrome.tabs.update(tabId, { active: true });
}

fieldOptions.addEventListener("change", () => {
  if (!fieldOptions.value) return;
  const selectedIndex = Number(fieldOptions.value);
  if (!Number.isInteger(selectedIndex) || !loadedChoices[selectedIndex]) return;
  picklistStatus.textContent = `Selected “${loadedChoices[selectedIndex].label}”. Click Select choice to choose it on the page.`;
});

document.querySelector("#load-picklist").addEventListener("click", async () => {
  const selector = dropdownSelectorInput.value.trim();
  if (!selector) {
    dropdownSelectorInput.setCustomValidity("Enter a CSS selector for the dropdown field.");
    dropdownSelectorInput.reportValidity();
    return;
  }
  dropdownSelectorInput.setCustomValidity("");
  fieldOptions.replaceChildren(new Option("Loading choices…", ""));
  picklistStatus.textContent = "Opening the target field…";

  try {
    const { choices } = await runUiAutomation("dropdown", selector);
    loadedChoices = choices;
    fieldOptions.replaceChildren();
    if (!choices.length) {
      fieldOptions.append(new Option("No choices detected", ""));
      picklistStatus.textContent = "No visible choices found. Open the field or enter a search value, then load choices again.";
      return;
    }
    fieldOptions.append(new Option("Choose a value…", ""));
    choices.forEach((choice, index) => fieldOptions.append(new Option(choice.label, String(index))));
    picklistStatus.textContent = `Loaded ${choices.length} choice${choices.length === 1 ? "" : "s"}.`;
  } catch (error) {
    fieldOptions.replaceChildren(new Option("Could not load choices", ""));
    picklistStatus.textContent = error?.message || "Could not load choices.";
  }
});

document.querySelector("#select-dropdown-choice").addEventListener("click", async () => {
  const selector = dropdownSelectorInput.value.trim();
  const index = Number(fieldOptions.value);
  const choice = Number.isInteger(index) ? loadedChoices[index] : null;
  if (!selector) {
    dropdownSelectorInput.setCustomValidity("Enter a CSS selector for the dropdown field.");
    dropdownSelectorInput.reportValidity();
    return;
  }
  if (!choice) {
    picklistStatus.textContent = "Load the choices and select one first.";
    return;
  }
  picklistStatus.textContent = "Selecting the choice on the target page…";
  try {
    const result = await runUiAutomation("choose", selector, choice.value);
    picklistStatus.textContent = `Selected “${choice.label}” in tab ${result.tabId}.`;
  } catch (error) {
    picklistStatus.textContent = error?.message || "Could not select that dropdown choice.";
  }
});

document.querySelector("#click-element").addEventListener("click", async () => {
  const selector = selectorInput.value.trim();
  if (!selector) {
    selectorInput.setCustomValidity("Enter a CSS selector for the element.");
    selectorInput.reportValidity();
    return;
  }
  selectorInput.setCustomValidity("");
  fillStatus.textContent = "";
  try {
    const result = await runUiAutomation("click", selector);
    fillStatus.textContent = `Browser click sent to matching element in tab ${result.tabId}.`;
  } catch (error) {
    fillStatus.textContent = error?.message || "Could not click that element.";
  }
});

document.querySelector("#dismiss-dropdown").addEventListener("click", async () => {
  picklistStatus.textContent = "";
  try {
    const result = await runUiAutomation("escape", "");
    picklistStatus.textContent = `Escape sent to the target page in tab ${result.tabId}.`;
  } catch (error) {
    picklistStatus.textContent = error?.message || "Could not dismiss the dropdown.";
  }
});

document.querySelector("#scan-page").addEventListener("click", () => {
  void scanTargetPage();
});

async function scanTargetPage() {
  scanStatus.textContent = "Scanning target tab…";
  pageElementsList.replaceChildren();

  try {
    const tab = await getActionTab();
    const readyTab = await waitForTargetWebsite(tab.id);
    const [result] = await chrome.scripting.executeScript({
      target: { tabId: readyTab.id },
      func: inspectPageElements,
    });
    const elements = result?.result?.elements || [];
    for (const element of elements) pageElementsList.append(createElementRow(element));
    const total = result?.result?.total || 0;
    scanStatus.textContent = total === 0
      ? "No visible clickable or fillable elements found."
      : `Found ${total} element${total === 1 ? "" : "s"}${total > elements.length ? `; showing the first ${elements.length}` : ""}.`;
    await recordEvent(`Scanned ${new URL(readyTab.url).host} in tab ${readyTab.id}: ${total} interactive elements found`);
  } catch (error) {
    scanStatus.textContent = error?.message || "Could not scan this page.";
  }
}

function createElementRow(element) {
  const item = document.createElement("li");
  const summary = document.createElement("div");
  summary.className = "element-summary";
  const description = document.createElement("span");
  description.textContent = `${element.kind} · ${element.label || element.description}`;
  let context = null;
  if (element.context) {
    context = document.createElement("span");
    context.className = "element-context";
    context.textContent = `For: ${element.context}`;
  }
  const selector = document.createElement("code");
  selector.hidden = true;
  selector.textContent = element.selector;
  const selectorToggle = document.createElement("button");
  selectorToggle.className = "text-button selector-toggle";
  selectorToggle.type = "button";
  selectorToggle.textContent = "Show selector";
  selectorToggle.setAttribute("aria-expanded", "false");
  selectorToggle.addEventListener("click", () => {
    selector.hidden = !selector.hidden;
    selectorToggle.textContent = selector.hidden ? "Show selector" : "Hide selector";
    selectorToggle.setAttribute("aria-expanded", String(!selector.hidden));
  });
  const actions = document.createElement("div");
  actions.className = "element-actions";
  if (element.kind === "Fillable") {
    const useForTextButton = document.createElement("button");
    useForTextButton.className = "text-button use-selector";
    useForTextButton.type = "button";
    useForTextButton.textContent = "Use for text";
    useForTextButton.addEventListener("click", () => {
      selectorInput.value = element.selector;
      selectorInput.setCustomValidity("");
      fillStatus.textContent = "Selector set for text entry. Enter a value and click Fill text.";
      selectorInput.focus();
      void scanTargetPage();
    });

    const useForDropdownButton = document.createElement("button");
    useForDropdownButton.className = "text-button use-selector";
    useForDropdownButton.type = "button";
    useForDropdownButton.textContent = "Use for dropdown";
    useForDropdownButton.addEventListener("click", () => {
      dropdownSelectorInput.value = element.selector;
      dropdownSelectorInput.setCustomValidity("");
      picklistStatus.textContent = "Selector set for dropdown choices. Click Load dropdown choices.";
      dropdownSelectorInput.focus();
      void scanTargetPage();
    });
    actions.append(useForTextButton, useForDropdownButton);
  }

  const clickButton = document.createElement("button");
  clickButton.className = "text-button use-selector";
  clickButton.type = "button";
  clickButton.textContent = element.kind === "Choice" ? "Select" : "Click";
  clickButton.setAttribute("aria-label", `${element.kind === "Choice" ? "Select" : "Click"} ${element.label || element.description}`);
  clickButton.addEventListener("click", () => {
    selectorInput.value = element.selector;
    selectorInput.setCustomValidity("");
    void runUiAutomation("click", element.selector).then(scanTargetPage).catch((error) => {
      fillStatus.textContent = error?.message || "Could not click that element.";
    });
  });
  actions.append(clickButton);
  summary.append(description);
  if (context) summary.append(context);
  summary.append(actions);
  item.append(summary, selectorToggle, selector);
  return item;
}

document.querySelector("#clear-logs").addEventListener("click", async () => {
  await chrome.storage.local.set({ commandLogs: [] });
});

function parseWebsiteURL(value) {
  let parsedUrl;
  try {
    parsedUrl = new URL(value.trim());
  } catch {
    websiteInput.setCustomValidity("Enter a valid website URL.");
    websiteInput.reportValidity();
    return null;
  }

  if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:") {
    websiteInput.setCustomValidity("Use an http:// or https:// website URL.");
    websiteInput.reportValidity();
    return null;
  }
  websiteInput.setCustomValidity("");
  return parsedUrl;
}

async function getActionTab() {
  const { targetTabId = null, targetTabUrl = "" } = await chrome.storage.local.get({
    targetTabId: null,
    targetTabUrl: "",
  });
  if (!Number.isInteger(targetTabId)) {
    throw new Error("No target tab is open. Open a website from this panel first.");
  }
  let targetTab;
  try {
    targetTab = await chrome.tabs.get(targetTabId);
  } catch {
    await chrome.storage.local.remove(["targetTabId", "targetTabUrl"]);
    throw new Error(`Target tab ${targetTabId} was closed. Open a fresh website from this panel to continue.`);
  }
  if (targetTab.url && /^https?:/.test(targetTab.url)) return targetTab;
  if ((targetTab.status === "loading" || !targetTab.url || targetTab.url === "about:blank") && /^https?:/.test(targetTabUrl)) {
    return { ...targetTab, url: targetTabUrl };
  }
  throw new Error(`Target tab ${targetTabId} is on ${targetTab.url || "an unknown page"}, not a regular website. Open a fresh website from this panel to continue.`);
}

async function waitForTargetWebsite(tabId) {
  const timeoutAt = Date.now() + 30000;
  while (Date.now() < timeoutAt) {
    let tab;
    try {
      tab = await chrome.tabs.get(tabId);
    } catch {
      await chrome.storage.local.remove(["targetTabId", "targetTabUrl"]);
      throw new Error(`Target tab ${tabId} was closed. Open a fresh website from this panel to continue.`);
    }
    if (tab.status !== "loading" && tab.url && /^https?:/.test(tab.url)) return tab;
    if (tab.status !== "loading" && tab.url && tab.url !== "about:blank" && !/^https?:/.test(tab.url)) {
      throw new Error(`Target tab ${tabId} finished loading ${tab.url}, which is not a regular website page.`);
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`Target tab ${tabId} did not finish loading a website within 30 seconds.`);
}

async function renderTargetTabStatus() {
  const previousTargetTabId = displayedTargetTabId;
  const { targetTabId: storedTargetTabId = null, targetTabUrl = "", targetTabs = [] } = await chrome.storage.local.get({
    targetTabId: null,
    targetTabUrl: "",
    targetTabs: [],
  });
  const openTabs = await chrome.tabs.query({});
  const openTabsById = new Map(openTabs.map((tab) => [tab.id, tab]));
  const managedTabs = targetTabs
    .filter((entry) => openTabsById.has(entry.id))
    .map((entry) => {
      const current = openTabsById.get(entry.id);
      return { id: current.id, url: current.url || current.pendingUrl || entry.url || "", title: current.title || entry.title || "" };
    })
    .filter((tab) => !/^chrome:\/\//i.test(tab.url));

  if (Number.isInteger(storedTargetTabId) && openTabsById.has(storedTargetTabId) && !managedTabs.some((tab) => tab.id === storedTargetTabId)) {
    const current = openTabsById.get(storedTargetTabId);
    const url = current.url || current.pendingUrl || targetTabUrl;
    if (!/^chrome:\/\//i.test(url)) managedTabs.push({ id: current.id, url, title: current.title || "" });
  }
  let targetTabId = storedTargetTabId;
  if (!managedTabs.length) {
    targetTabId = null;
  } else if (!managedTabs.some((tab) => tab.id === targetTabId)) {
    targetTabId = managedTabs[managedTabs.length - 1].id;
  }
  const activeEntry = managedTabs.find((tab) => tab.id === targetTabId);
  const nextTargetTabUrl = activeEntry?.url || targetTabUrl;
  renderExtensionTabs(managedTabs, targetTabId);
  if (JSON.stringify(managedTabs) !== JSON.stringify(targetTabs) || targetTabId !== storedTargetTabId || nextTargetTabUrl !== targetTabUrl) {
    const values = { targetTabs: managedTabs };
    if (targetTabId === null) {
      await chrome.storage.local.set(values);
      await chrome.storage.local.remove(["targetTabId", "targetTabUrl"]);
    } else {
      await chrome.storage.local.set({ ...values, targetTabId, targetTabUrl: nextTargetTabUrl });
    }
  }

  if (!Number.isInteger(targetTabId)) {
    displayedTargetTabId = null;
    displayedTargetTabUrl = "";
    if (previousTargetTabId !== null) {
      pageElementsList.replaceChildren();
      scanStatus.textContent = "";
    }
    return;
  }

  try {
    const tab = await chrome.tabs.get(targetTabId);
    const targetUrl = tab.url || "";
    const pageChanged = displayedTargetTabId === targetTabId && displayedTargetTabUrl !== targetUrl;
    displayedTargetTabId = targetTabId;
    displayedTargetTabUrl = targetUrl;
    if (pageChanged) {
      pageElementsList.replaceChildren();
      scanStatus.textContent = "";
    }
  } catch {
    await chrome.storage.local.remove(["targetTabId", "targetTabUrl"]);
    displayedTargetTabId = null;
    displayedTargetTabUrl = "";
  }

  if (previousTargetTabId !== targetTabId) {
    pageElementsList.replaceChildren();
    scanStatus.textContent = "";
  }
}

function renderExtensionTabs(tabs, selectedTabId) {
  extensionTabsList.replaceChildren();
  for (const tab of tabs) {
    const item = document.createElement("li");
    item.className = "extension-tab-row";
    const controls = document.createElement("div");
    controls.className = "extension-tab-controls";
    const button = document.createElement("button");
    const selected = tab.id === selectedTabId;
    button.type = "button";
    button.className = "tab-select";
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
    const url = tab.url || "";
    const prefix = url.length > 20 ? `${url.slice(0, 20)}…` : url || tab.title || `Tab ${tab.id}`;
    button.textContent = `${prefix} · ${tab.id}`;
    button.addEventListener("click", () => void selectExtensionTab(tab.id));
    const urlToggle = document.createElement("button");
    urlToggle.type = "button";
    urlToggle.className = "text-button tab-url-toggle";
    urlToggle.textContent = "Show URL";
    const fullUrl = document.createElement("code");
    fullUrl.className = "tab-full-url";
    fullUrl.hidden = true;
    fullUrl.textContent = url || "URL is still loading";
    urlToggle.addEventListener("click", () => {
      fullUrl.hidden = !fullUrl.hidden;
      urlToggle.textContent = fullUrl.hidden ? "Show URL" : "Hide URL";
    });
    controls.append(button, urlToggle);
    item.append(controls, fullUrl);
    extensionTabsList.append(item);
  }
}

function inspectPageElements() {
  const candidateSelector = [
    "a[href]", "button", "input:not([type=hidden])", "textarea", "select",
    "label",
    "[contenteditable=true]", "[role=button]", "[role=link]", "[role=menuitem]",
    "[role=checkbox]", "[role=radio]", "[role=switch]", "[role=option]", "[role=listbox] li",
    "[aria-selected=true]", "[data-value]", "[class*=option]", "[class*=suggest]",
    "[onclick]", "summary",
    "[tabindex]:not([tabindex='-1'])",
  ].join(",");
  const candidateSet = new Set(document.querySelectorAll(candidateSelector));
  for (const element of document.querySelectorAll("button, a, div, span, li")) {
    if (getComputedStyle(element).cursor !== "pointer") continue;
    const text = (element.innerText || element.textContent || "").trim();
    if (text && text.length <= 100) candidateSet.add(element);
  }
  const candidates = [...candidateSet];
  candidates.sort((first, second) => Number(isChoiceControl(second)) - Number(isChoiceControl(first)));
  const elements = [];
  let total = 0;

  for (const element of candidates) {
    const labelControl = element instanceof HTMLLabelElement ? element.control : null;
    if (element instanceof HTMLLabelElement && !labelControl) continue;
    const style = getComputedStyle(element);
    if (!element.getClientRects().length || style.display === "none" || style.visibility === "hidden" || element.disabled) continue;

    const tag = element.tagName.toLowerCase();
    const inputType = tag === "input" ? element.type.toLowerCase() : "";
    const choiceControl = isChoiceControl(element, labelControl);
    if (choiceControl && hasEquivalentChoiceAncestor(element)) continue;
    const editable = tag === "textarea" || tag === "select" || element.isContentEditable ||
      (tag === "input" && !["button", "checkbox", "file", "hidden", "image", "radio", "reset", "submit"].includes(inputType));
    const kind = choiceControl ? "Choice" : editable ? "Fillable" : "Clickable";
    const labelText = element.labels
      ? [...element.labels].map((label) => label.innerText.trim()).filter(Boolean).join(" / ")
      : "";
    const label = labelText || element.getAttribute("aria-label") || element.getAttribute("placeholder") ||
      element.getAttribute("title") || "";
    const description = tag === "input" ? `input (${inputType || "text"})` :
      (element.innerText || element.textContent || tag).replace(/\s+/g, " ").trim().slice(0, 90) || tag;

    total += 1;
    if (elements.length < 200) {
      elements.push({
        kind,
        label: label.slice(0, 90),
        description,
        context: getContextLabel(element),
        selector: buildUniqueSelector(element),
      });
    }
  }
  return { elements, total };

  function isChoiceControl(element, knownLabelControl = null) {
    const labelControl = knownLabelControl || (element instanceof HTMLLabelElement ? element.control : null);
    const tag = element.tagName.toLowerCase();
    const inputType = tag === "input" ? element.type.toLowerCase() : "";
    if ((tag === "input" && ["radio", "checkbox"].includes(inputType)) ||
      ["radio", "checkbox", "switch"].includes(element.getAttribute("role")) ||
      (labelControl && ((labelControl instanceof HTMLInputElement && ["radio", "checkbox"].includes(labelControl.type)) ||
        ["radio", "checkbox"].includes(labelControl.getAttribute("role"))))) return true;

    for (let ancestor = element.parentElement, depth = 0; ancestor && depth < 4; ancestor = ancestor.parentElement, depth += 1) {
      const groupText = (ancestor.innerText || "").replace(/\s+/g, " ");
      if (!/\bapply\s+as\b/i.test(groupText)) continue;
      const ownText = (element.innerText || element.textContent || "").trim();
      if (!ownText || ownText.length > 100 || /\bapply\s+as\b/i.test(ownText)) continue;
      const groupControls = [...ancestor.querySelectorAll("button, [role=button], a, [onclick], [tabindex], div, span")]
        .filter((candidate) => getComputedStyle(candidate).cursor === "pointer");
      if (groupControls.length >= 2 && groupControls.length <= 12) return true;
    }
    return false;
  }

  function hasEquivalentChoiceAncestor(element) {
    const ownText = (element.innerText || element.textContent || "").replace(/\s+/g, " ").trim();
    if (!ownText) return false;
    const context = getContextLabel(element);
    for (let ancestor = element.parentElement, depth = 0; ancestor && depth < 6; ancestor = ancestor.parentElement, depth += 1) {
      if (!candidateSet.has(ancestor) || !isChoiceControl(ancestor)) continue;
      const ancestorText = (ancestor.innerText || ancestor.textContent || "").replace(/\s+/g, " ").trim();
      if (ancestorText === ownText && getContextLabel(ancestor) === context) return true;
    }
    return false;
  }

  function getContextLabel(element) {
    const contextSelectors = "h1, h2, h3, h4, h5, h6, [role=heading], [class*=title], [class*=name]";
    for (let ancestor = element.parentElement, depth = 0; ancestor && depth < 8; ancestor = ancestor.parentElement, depth += 1) {
      const groupText = (ancestor.innerText || "").trim();
      if (!groupText || groupText.length > 700) continue;
      const candidates = [...ancestor.querySelectorAll(contextSelectors)];
      for (const candidate of candidates) {
        if (candidate === element || candidate.contains(element)) continue;
        const style = getComputedStyle(candidate);
        if (style.display === "none" || style.visibility === "hidden" || !candidate.getClientRects().length) continue;
        const text = (candidate.innerText || candidate.textContent || "").replace(/\s+/g, " ").trim();
        if (text.length < 4 || text.length > 120 || /^(dashboard|ipo order placement|apply as)$/i.test(text)) continue;
        return text;
      }
    }
    return "";
  }

  function buildUniqueSelector(element) {
    const tag = element.tagName.toLowerCase();
    if (element.id) {
      const idSelector = `#${CSS.escape(element.id)}`;
      if (document.querySelectorAll(idSelector).length === 1) return idSelector;
    }

    for (const attribute of ["name", "aria-label", "placeholder", "autocomplete", "data-testid", "type"]) {
      const value = element.getAttribute(attribute);
      if (!value) continue;
      const escapedValue = `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"').replaceAll("\n", "\\a ")}"`;
      const selector = `${tag}[${attribute}=${escapedValue}]`;
      try {
        if (document.querySelectorAll(selector).length === 1) return selector;
      } catch {
        // Try the next stable attribute or the structural fallback.
      }
    }

    const path = [];
    let current = element;
    while (current && current.nodeType === Node.ELEMENT_NODE) {
      let segment = current.tagName.toLowerCase();
      if (current.id && document.querySelectorAll(`#${CSS.escape(current.id)}`).length === 1) {
        segment = `#${CSS.escape(current.id)}`;
        path.unshift(segment);
        break;
      }
      let siblingIndex = 1;
      for (let sibling = current.previousElementSibling; sibling; sibling = sibling.previousElementSibling) {
        if (sibling.tagName === current.tagName) siblingIndex += 1;
      }
      segment += `:nth-of-type(${siblingIndex})`;
      path.unshift(segment);
      current = current.parentElement;
    }
    return path.join(" > ");
  }
}

async function recordEvent(message) {
  const { commandLogs = [] } = await chrome.storage.local.get({ commandLogs: [] });
  commandLogs.unshift({ message, time: new Date().toISOString() });
  await chrome.storage.local.set({ commandLogs: commandLogs.slice(0, 50) });
}

async function renderActivity() {
  const { bridgeState = "connecting", commandLogs = [] } = await chrome.storage.local.get({
    bridgeState: "connecting",
    commandLogs: [],
  });
  bridgeStatus.classList.toggle("connected", bridgeState === "connected");
  bridgeStatus.classList.toggle("retrying", bridgeState === "retrying");
  bridgeStatus.replaceChildren();
  const statusDot = document.createElement("span");
  statusDot.className = "status-dot";
  const stateLabel = {
    connected: "CLI connected",
    connecting: "Connecting to CLI…",
    retrying: "CLI unavailable · retrying shortly",
  }[bridgeState] || "Connecting to CLI…";
  bridgeStatus.append(statusDot, document.createTextNode(stateLabel));

  logsList.replaceChildren();
  if (commandLogs.length === 0) {
    const emptyState = document.createElement("li");
    emptyState.textContent = "No activity yet.";
    emptyState.className = "empty-state";
    logsList.append(emptyState);
    return;
  }
  for (const entry of commandLogs.slice(0, 12)) {
    const item = document.createElement("li");
    const time = document.createElement("time");
    time.dateTime = entry.time;
    time.textContent = new Date(entry.time).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    const message = document.createElement("span");
    message.textContent = entry.message;
    item.append(time, message);
    logsList.append(item);
  }
}
