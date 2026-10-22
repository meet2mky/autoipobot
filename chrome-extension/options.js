const DEFAULT_WEBSITE_URL = "https://example.com/";
const form = document.querySelector("#settings-form");
const websiteInput = document.querySelector("#website-url");
const saveStatus = document.querySelector("#save-status");

chrome.storage.sync.get({ websiteUrl: DEFAULT_WEBSITE_URL }, ({ websiteUrl }) => {
  websiteInput.value = websiteUrl;
});
form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const websiteUrl = websiteInput.value.trim();
  let parsedUrl;

  try {
    parsedUrl = new URL(websiteUrl);
  } catch {
    websiteInput.setCustomValidity("Enter a valid website URL.");
    websiteInput.reportValidity();
    return;
  }

  websiteInput.setCustomValidity("");
  if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:") {
    websiteInput.setCustomValidity("Use an http:// or https:// website URL.");
    websiteInput.reportValidity();
    return;
  }

  await chrome.storage.sync.set({ websiteUrl: parsedUrl.href });
  saveStatus.textContent = "Saved";
  window.setTimeout(() => {
    saveStatus.textContent = "";
  }, 2000);
});
