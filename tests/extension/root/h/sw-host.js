// Worker host: installs the chrome stub, imports the real service worker and
// bridges runtime messages / storage changes / eval requests from the page.
importScripts("chrome-stub.js");
self.__stub.peers.send = (message) => self.postMessage(message);

let importError = null;
try {
  importScripts("../ext/service-worker.js");
} catch (error) {
  importError = `${error && error.name}: ${error && error.message}`;
}
self.postMessage({ kind: "ready", importError });

self.addEventListener("message", (event) => {
  const data = event.data;
  if (data.kind === "storage") {
    self.__stub.applyChanges(data.area, data.changes, false);
    return;
  }
  if (data.kind === "runtime_message") {
    const listeners = chrome.runtime.onMessage.listeners;
    if (!listeners.length) {
      self.postMessage({ kind: "response", id: data.id, response: undefined });
      return;
    }
    let responded = false;
    const sendResponse = (response) => {
      if (responded) return;
      responded = true;
      self.postMessage({ kind: "response", id: data.id, response });
    };
    for (const listener of listeners) {
      const keepOpen = listener(data.message, { id: chrome.runtime.id }, sendResponse);
      if (keepOpen !== true && !responded) sendResponse(undefined);
    }
    return;
  }
  if (data.kind === "eval") {
    let promise;
    try {
      promise = Promise.resolve(new Function("stub", `return (async () => {${data.code}\n})();`)(self.__stub));
    } catch (error) {
      promise = Promise.reject(error);
    }
    promise.then(
      (result) => self.postMessage({ kind: "response", id: data.id, response: { ok: true, result } }),
      (error) => self.postMessage({ kind: "response", id: data.id, response: { ok: false, error: `${error && error.name}: ${error && error.message}` } }),
    );
  }
});
