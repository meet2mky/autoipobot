// Minimal chrome.* stub for exercising the extension in headless Chrome.
// Works in a page (window) and in a dedicated worker. Not part of the extension.
(function installChromeStub(global) {
  const isWorker = typeof window === "undefined";
  const calls = [];
  const record = (api, args) => calls.push({ api, args: structuredClone(args) });

  function makeEvent() {
    const listeners = [];
    return {
      listeners,
      addListener(fn) { listeners.push(fn); },
      removeListener(fn) { const index = listeners.indexOf(fn); if (index >= 0) listeners.splice(index, 1); },
      hasListener(fn) { return listeners.includes(fn); },
      dispatch(...args) { return listeners.map((fn) => fn(...args)); },
    };
  }

  const peers = { send: null, request: null };
  const areas = {};
  const storageOnChanged = makeEvent();

  function applyChanges(areaName, changes, broadcast) {
    const area = areas[areaName];
    const fired = {};
    for (const [key, change] of Object.entries(changes)) {
      const oldValue = area.data[key];
      if (change.remove) {
        if (!Object.hasOwn(area.data, key)) continue;
        delete area.data[key];
        fired[key] = { oldValue: structuredClone(oldValue) };
      } else {
        area.data[key] = structuredClone(change.newValue);
        fired[key] = { oldValue: structuredClone(oldValue), newValue: structuredClone(change.newValue) };
      }
    }
    if (!Object.keys(fired).length) return;
    if (broadcast && peers.send) peers.send({ kind: "storage", area: areaName, changes: structuredClone(changes) });
    setTimeout(() => storageOnChanged.dispatch(fired, areaName), 0);
  }

  function makeStorageArea(name) {
    const data = {};
    const area = {
      data,
      async get(defaults) {
        if (defaults === null || defaults === undefined) return structuredClone(data);
        if (typeof defaults === "string") return Object.hasOwn(data, defaults) ? { [defaults]: structuredClone(data[defaults]) } : {};
        if (Array.isArray(defaults)) {
          const out = {};
          for (const key of defaults) if (Object.hasOwn(data, key)) out[key] = structuredClone(data[key]);
          return out;
        }
        const out = {};
        for (const [key, value] of Object.entries(defaults)) out[key] = Object.hasOwn(data, key) ? structuredClone(data[key]) : structuredClone(value);
        return out;
      },
      async set(values) {
        record(`storage.${name}.set`, [values]);
        applyChanges(name, Object.fromEntries(Object.entries(values).map(([key, value]) => [key, { newValue: value }])), true);
      },
      async remove(keys) {
        const list = Array.isArray(keys) ? keys : [keys];
        record(`storage.${name}.remove`, [list]);
        applyChanges(name, Object.fromEntries(list.map((key) => [key, { remove: true }])), true);
      },
      async clear() {
        applyChanges(name, Object.fromEntries(Object.keys(data).map((key) => [key, { remove: true }])), true);
      },
    };
    areas[name] = area;
    return area;
  }

  const tabs = { list: [], nextId: 101 };
  const tabsApi = {
    onRemoved: makeEvent(),
    onUpdated: makeEvent(),
    onCreated: makeEvent(),
    onActivated: makeEvent(),
    async query() { return structuredClone(tabs.list); },
    async get(id) {
      const tab = tabs.list.find((item) => item.id === id);
      if (!tab) throw new Error(`No tab with id: ${id}.`);
      return structuredClone(tab);
    },
    async create({ url, active }) {
      const tab = { id: tabs.nextId++, url, pendingUrl: url, title: "", active: Boolean(active), status: "complete", windowId: 1 };
      tabs.list.push(tab);
      record("tabs.create", [url]);
      return structuredClone(tab);
    },
    async update(id, props) {
      const tab = tabs.list.find((item) => item.id === id);
      if (!tab) throw new Error(`No tab with id: ${id}.`);
      Object.assign(tab, props);
      record("tabs.update", [id, props]);
      return structuredClone(tab);
    },
    _tabs: tabs,
    _remove(id) {
      const index = tabs.list.findIndex((item) => item.id === id);
      if (index >= 0) tabs.list.splice(index, 1);
      tabsApi.onRemoved.dispatch(id, { windowId: 1, isWindowClosing: false });
    },
  };

  const debuggerState = { attached: new Set(), commands: [], delayNext: 0, failAttach: false, handlers: {} };
  const debuggerApi = {
    onDetach: makeEvent(),
    onEvent: makeEvent(),
    async attach({ tabId }) {
      if (debuggerState.failAttach || debuggerState.attached.has(tabId)) {
        throw new Error(`Another debugger is already attached to the tab with id: ${tabId}.`);
      }
      debuggerState.attached.add(tabId);
      record("debugger.attach", [tabId]);
    },
    async detach({ tabId }) {
      if (!debuggerState.attached.has(tabId)) throw new Error(`Debugger is not attached to the tab with id: ${tabId}.`);
      debuggerState.attached.delete(tabId);
      record("debugger.detach", [tabId]);
    },
    async sendCommand({ tabId }, method, params) {
      if (!debuggerState.attached.has(tabId)) throw new Error(`Debugger is not attached to the tab with id: ${tabId}.`);
      const entry = { tabId, method, params: structuredClone(params ?? null), start: Date.now(), end: 0 };
      debuggerState.commands.push(entry);
      if (debuggerState.delayNext > 0) {
        const delay = debuggerState.delayNext;
        debuggerState.delayNext = 0;
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      entry.end = Date.now();
      const handler = debuggerState.handlers[method];
      return handler ? handler(params) : {};
    },
    _state: debuggerState,
  };

  const scriptingApi = {
    _canned: {},
    async executeScript({ target, func, args = [] }) {
      record("scripting.executeScript", [target.tabId, func.name, args]);
      if (!isWorker) return [{ result: func(...args) }];
      const canned = scriptingApi._canned[func.name];
      if (canned === undefined) throw new Error(`No canned result for ${func.name}`);
      return [{ result: typeof canned === "function" ? canned(...args) : structuredClone(canned) }];
    },
  };

  const alarmsApi = {
    onAlarm: makeEvent(),
    alarms: new Map(),
    async create(name, info) { alarmsApi.alarms.set(name, info); record("alarms.create", [name, info]); },
    async clear(name) { record("alarms.clear", [name]); return alarmsApi.alarms.delete(name); },
    async get(name) { return alarmsApi.alarms.get(name); },
  };

  const runtimeApi = {
    id: "stub-extension-id",
    onInstalled: makeEvent(),
    onStartup: makeEvent(),
    onMessage: makeEvent(),
    async sendMessage(message) {
      if (!peers.request) throw new Error("Could not establish connection. Receiving end does not exist.");
      return peers.request(message);
    },
    async openOptionsPage() { record("runtime.openOptionsPage", []); },
  };

  const actionApi = {
    badge: {},
    async setBadgeText({ text }) { actionApi.badge.text = text; },
    async setBadgeBackgroundColor({ color }) { actionApi.badge.color = color; },
    async setTitle({ title }) { actionApi.badge.title = title; },
  };

  const chromeStub = {
    runtime: runtimeApi,
    storage: { local: makeStorageArea("local"), sync: makeStorageArea("sync"), onChanged: storageOnChanged },
    tabs: tabsApi,
    debugger: debuggerApi,
    scripting: scriptingApi,
    alarms: alarmsApi,
    action: actionApi,
    sidePanel: { async setPanelBehavior(options) { record("sidePanel.setPanelBehavior", [options]); } },
  };

  const stub = { calls, peers, applyChanges, areas, chrome: chromeStub, tabs: tabsApi, debugger: debuggerApi, scripting: scriptingApi, alarms: alarmsApi, runtime: runtimeApi, action: actionApi, isWorker };

  if (isWorker) {
    const sockets = [];
    class FakeWebSocket extends EventTarget {
      constructor(url) {
        super();
        this.url = url;
        this.readyState = 0;
        this.sent = [];
        sockets.push(this);
        record("WebSocket", [url]);
      }
      send(data) { this.sent.push(data); }
      close(code = 1000, reason = "") {
        if (this.readyState === 3) return;
        this.readyState = 3;
        setTimeout(() => this.dispatchEvent(Object.assign(new Event("close"), { code, reason })), 0);
      }
      _open() { this.readyState = 1; this.dispatchEvent(new Event("open")); }
      _receive(data) { this.dispatchEvent(new MessageEvent("message", { data })); }
    }
    FakeWebSocket.CONNECTING = 0;
    FakeWebSocket.OPEN = 1;
    FakeWebSocket.CLOSING = 2;
    FakeWebSocket.CLOSED = 3;
    global.WebSocket = FakeWebSocket;
    stub.sockets = sockets;
    stub.health = { available: false };
    const realFetch = global.fetch.bind(global);
    global.fetch = async (url, init) => {
      if (String(url).includes("/health")) {
        record("fetch", [String(url)]);
        if (!stub.health.available) throw new TypeError("Failed to fetch");
        return new Response(null, { status: 204 });
      }
      return realFetch(url, init);
    };
  }

  global.chrome = chromeStub;
  global.__stub = stub;
})(globalThis);
