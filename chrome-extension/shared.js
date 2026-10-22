// Settings defaults and normalisers shared by the service worker, the side
// panel and the options page. This file is loaded as a classic script (side
// panel, options page) and through importScripts (service worker), so it only
// declares globals and must not use ES modules.

/** Default values for every user-editable setting kept in storage.local. */
const AUTOIPO_DEFAULTS = Object.freeze({
  websiteUrl: "https://example.com/",
  bridgePort: 18765,
  typing: Object.freeze({ minMs: 40, maxMs: 120, humanLike: true }),
  siteHints: Object.freeze({
    choiceGroupPattern: "\\bapply\\s+as\\b",
    ignoredContextPattern: "^(dashboard|ipo order placement|apply as)$",
  }),
});

/** Upper bound for a per-character typing delay, in milliseconds. */
const TYPING_DELAY_LIMIT_MS = 5000;

/**
 * Returns a valid TCP port, falling back to the default bridge port when the
 * stored value is missing or out of range.
 */
function normalizeBridgePort(value) {
  const port = Number(value);
  if (Number.isInteger(port) && port >= 1 && port <= 65535) return port;
  return AUTOIPO_DEFAULTS.bridgePort;
}

/**
 * Returns a complete typing configuration: integer delays clamped to
 * [0, TYPING_DELAY_LIMIT_MS] with minMs <= maxMs, and a boolean humanLike flag.
 */
function normalizeTyping(value) {
  const source = value && typeof value === "object" ? value : {};
  const clampDelay = (candidate, fallback) => {
    const number = Number(candidate);
    if (candidate === null || candidate === undefined || candidate === "" || !Number.isFinite(number)) return fallback;
    return Math.min(TYPING_DELAY_LIMIT_MS, Math.max(0, Math.round(number)));
  };
  const minMs = clampDelay(source.minMs, AUTOIPO_DEFAULTS.typing.minMs);
  const maxMs = Math.max(minMs, clampDelay(source.maxMs, AUTOIPO_DEFAULTS.typing.maxMs));
  const humanLike = typeof source.humanLike === "boolean" ? source.humanLike : AUTOIPO_DEFAULTS.typing.humanLike;
  return { minMs, maxMs, humanLike };
}

/**
 * Returns the site hint patterns as strings. Empty strings are preserved so a
 * user can deliberately disable a heuristic; invalid patterns are kept here
 * and ignored by the scanner, which compiles them defensively.
 */
function normalizeSiteHints(value) {
  const source = value && typeof value === "object" ? value : {};
  return {
    choiceGroupPattern: typeof source.choiceGroupPattern === "string"
      ? source.choiceGroupPattern
      : AUTOIPO_DEFAULTS.siteHints.choiceGroupPattern,
    ignoredContextPattern: typeof source.ignoredContextPattern === "string"
      ? source.ignoredContextPattern
      : AUTOIPO_DEFAULTS.siteHints.ignoredContextPattern,
  };
}

/**
 * Validates a site hint pattern. Returns {ok: true} for empty or compilable
 * patterns and {ok: false, error} when the regular expression is invalid.
 */
function validateHintPattern(source) {
  if (typeof source !== "string" || !source.trim()) return { ok: true, empty: true };
  try {
    return { ok: Boolean(new RegExp(source, "i")), empty: false };
  } catch (error) {
    return { ok: false, empty: false, error: error instanceof Error ? error.message : String(error) };
  }
}

/**
 * Parses an http(s) website URL. Returns {href} on success or {error} with a
 * message suitable for form validation.
 */
function parseWebsiteUrl(value) {
  let parsed;
  try {
    parsed = new URL(String(value ?? "").trim());
  } catch {
    return { error: "Enter a valid website URL." };
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    return { error: "Use an http:// or https:// website URL." };
  }
  return { href: parsed.href };
}

/** Reads every setting from storage.local and returns normalised values. */
async function loadSettings() {
  const stored = await chrome.storage.local.get({
    websiteUrl: AUTOIPO_DEFAULTS.websiteUrl,
    bridgePort: AUTOIPO_DEFAULTS.bridgePort,
    typing: AUTOIPO_DEFAULTS.typing,
    siteHints: AUTOIPO_DEFAULTS.siteHints,
  });
  return {
    websiteUrl: typeof stored.websiteUrl === "string" && stored.websiteUrl ? stored.websiteUrl : AUTOIPO_DEFAULTS.websiteUrl,
    bridgePort: normalizeBridgePort(stored.bridgePort),
    typing: normalizeTyping(stored.typing),
    siteHints: normalizeSiteHints(stored.siteHints),
  };
}
