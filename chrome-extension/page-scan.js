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
