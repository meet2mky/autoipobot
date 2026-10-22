// Options page controller. Settings persist to storage.local; the connection
// status is read-only and mirrors what the service worker publishes.
// Requires shared.js (loaded first).

const ui = {
  form: document.querySelector("#settings-form"),
  bridgePort: document.querySelector("#bridge-port"),
  bridgePortFeedback: document.querySelector("#bridge-port-feedback"),
  typingMin: document.querySelector("#typing-min"),
  typingMax: document.querySelector("#typing-max"),
  typingHuman: document.querySelector("#typing-human"),
  typingFeedback: document.querySelector("#typing-feedback"),
  choiceGroupPattern: document.querySelector("#choice-group-pattern"),
  choiceGroupFeedback: document.querySelector("#choice-group-feedback"),
  ignoredContextPattern: document.querySelector("#ignored-context-pattern"),
  ignoredContextFeedback: document.querySelector("#ignored-context-feedback"),
  websiteUrl: document.querySelector("#website-url"),
  websiteUrlFeedback: document.querySelector("#website-url-feedback"),
  saveStatus: document.querySelector("#save-status"),
  restoreDefaults: document.querySelector("#restore-defaults"),
  connectionStatus: document.querySelector("#connection-status"),
  connectionStatusText: document.querySelector("#connection-status-text"),
  connectionDetail: document.querySelector("#connection-detail"),
  reconnectBridge: document.querySelector("#reconnect-bridge"),
  reconnectStatus: document.querySelector("#reconnect-status"),
};

const CONNECTION_LABELS = Object.freeze({
  connected: "Connected to the AutoIPO bridge",
  connecting: "Connecting to the AutoIPO bridge…",
  retrying: "Bridge unavailable · retrying shortly",
});

init();

function init() {
  ui.form.addEventListener("submit", (event) => {
    event.preventDefault();
    void saveSettings();
  });
  ui.restoreDefaults.addEventListener("click", () => {
    fillForm({
      websiteUrl: AUTOIPO_DEFAULTS.websiteUrl,
      bridgePort: AUTOIPO_DEFAULTS.bridgePort,
      typing: AUTOIPO_DEFAULTS.typing,
      siteHints: AUTOIPO_DEFAULTS.siteHints,
    });
    validateForm();
    setFeedback(ui.saveStatus, "Defaults restored; click Save settings to keep them.", "");
  });
  ui.reconnectBridge.addEventListener("click", () => void reconnectBridge());
  for (const input of [ui.bridgePort, ui.typingMin, ui.typingMax, ui.choiceGroupPattern, ui.ignoredContextPattern, ui.websiteUrl]) {
    input.addEventListener("input", validateForm);
  }
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area !== "local") return;
    if (changes.bridgeState || changes.bridgeDetail || changes.bridgePort) void renderConnectionStatus();
  });
  void loadForm();
  void renderConnectionStatus();
}

function errorText(error, fallback) {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return fallback;
}

/** Shows feedback text with a tone of "ok", "error" or "" (neutral). */
function setFeedback(element, text, tone) {
  element.textContent = text;
  element.classList.toggle("ok", tone === "ok");
  element.classList.toggle("error", tone === "error");
}

// ---------------------------------------------------------------------------
// Form state
// ---------------------------------------------------------------------------

async function loadForm() {
  try {
    fillForm(await loadSettings());
  } catch (error) {
    setFeedback(ui.saveStatus, errorText(error, "Could not load the saved settings."), "error");
    fillForm({
      websiteUrl: AUTOIPO_DEFAULTS.websiteUrl,
      bridgePort: AUTOIPO_DEFAULTS.bridgePort,
      typing: AUTOIPO_DEFAULTS.typing,
      siteHints: AUTOIPO_DEFAULTS.siteHints,
    });
  }
  validateForm();
}

function fillForm(settings) {
  ui.bridgePort.value = String(settings.bridgePort);
  ui.typingMin.value = String(settings.typing.minMs);
  ui.typingMax.value = String(settings.typing.maxMs);
  ui.typingHuman.checked = settings.typing.humanLike;
  ui.choiceGroupPattern.value = settings.siteHints.choiceGroupPattern;
  ui.ignoredContextPattern.value = settings.siteHints.ignoredContextPattern;
  ui.websiteUrl.value = settings.websiteUrl;
}

/** Validates every field, updates the feedback spans and returns the parsed settings or null. */
function validateForm() {
  let valid = true;

  const port = Number(ui.bridgePort.value);
  if (ui.bridgePort.value.trim() === "" || !Number.isInteger(port) || port < 1 || port > 65535) {
    setFeedback(ui.bridgePortFeedback, "Enter a whole number between 1 and 65535.", "error");
    valid = false;
  } else {
    setFeedback(ui.bridgePortFeedback, "", "");
  }

  const minMs = Number(ui.typingMin.value);
  const maxMs = Number(ui.typingMax.value);
  const delayValid = (text, value) => text.trim() !== "" && Number.isInteger(value) && value >= 0 && value <= TYPING_DELAY_LIMIT_MS;
  if (!delayValid(ui.typingMin.value, minMs) || !delayValid(ui.typingMax.value, maxMs)) {
    setFeedback(ui.typingFeedback, `Delays must be whole numbers between 0 and ${TYPING_DELAY_LIMIT_MS} ms.`, "error");
    valid = false;
  } else if (minMs > maxMs) {
    setFeedback(ui.typingFeedback, "The minimum delay must not exceed the maximum delay.", "error");
    valid = false;
  } else {
    setFeedback(ui.typingFeedback, "", "");
  }

  const choiceGroup = validateHintPattern(ui.choiceGroupPattern.value);
  renderPatternFeedback(ui.choiceGroupFeedback, choiceGroup);
  const ignoredContext = validateHintPattern(ui.ignoredContextPattern.value);
  renderPatternFeedback(ui.ignoredContextFeedback, ignoredContext);
  if (!choiceGroup.ok || !ignoredContext.ok) valid = false;

  const website = parseWebsiteUrl(ui.websiteUrl.value);
  if (website.error) {
    setFeedback(ui.websiteUrlFeedback, website.error, "error");
    valid = false;
  } else {
    setFeedback(ui.websiteUrlFeedback, "", "");
  }

  if (!valid) return null;
  return {
    bridgePort: port,
    typing: { minMs, maxMs, humanLike: ui.typingHuman.checked },
    siteHints: {
      choiceGroupPattern: ui.choiceGroupPattern.value,
      ignoredContextPattern: ui.ignoredContextPattern.value,
    },
    websiteUrl: website.href,
  };
}

function renderPatternFeedback(element, validation) {
  if (!validation.ok) {
    setFeedback(element, `Invalid pattern: ${validation.error}`, "error");
  } else if (validation.empty) {
    setFeedback(element, "Empty: this heuristic is disabled.", "");
  } else {
    setFeedback(element, "Valid pattern.", "ok");
  }
}

async function saveSettings() {
  const settings = validateForm();
  if (!settings) {
    setFeedback(ui.saveStatus, "Fix the highlighted fields before saving.", "error");
    return;
  }
  try {
    await chrome.storage.local.set(settings);
    setFeedback(ui.saveStatus, "Saved", "ok");
    window.setTimeout(() => {
      if (ui.saveStatus.textContent === "Saved") setFeedback(ui.saveStatus, "", "");
    }, 2000);
  } catch (error) {
    setFeedback(ui.saveStatus, errorText(error, "Could not save the settings."), "error");
  }
}

// ---------------------------------------------------------------------------
// Connection status
// ---------------------------------------------------------------------------

async function renderConnectionStatus() {
  try {
    const stored = await chrome.storage.local.get({ bridgeState: "connecting", bridgeDetail: "", bridgePort: AUTOIPO_DEFAULTS.bridgePort });
    const known = Object.hasOwn(CONNECTION_LABELS, stored.bridgeState) ? stored.bridgeState : "connecting";
    ui.connectionStatus.classList.toggle("connected", known === "connected");
    ui.connectionStatus.classList.toggle("retrying", known === "retrying");
    ui.connectionStatusText.textContent = `${CONNECTION_LABELS[known]} (port ${normalizeBridgePort(stored.bridgePort)})`;
    ui.connectionDetail.textContent = typeof stored.bridgeDetail === "string" ? stored.bridgeDetail : "";
  } catch (error) {
    ui.connectionStatusText.textContent = errorText(error, "Could not read the connection status.");
  }
}

async function reconnectBridge() {
  try {
    const response = await chrome.runtime.sendMessage({ type: "reconnect_bridge" });
    if (!response || response.ok !== true) throw new Error(response?.error || "The extension worker did not accept the request.");
    setFeedback(ui.reconnectStatus, "Reconnecting…", "ok");
  } catch (error) {
    setFeedback(ui.reconnectStatus, errorText(error, "Could not reach the extension worker."), "error");
  }
}
