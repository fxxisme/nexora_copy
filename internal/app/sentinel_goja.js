(() => {
  const input = __sentinelInput;
  const storage = new Map([['oai/apps/lightweight-web/composerDraft/owner.v2', '']]);
  const location = {
    href: 'https://chatgpt.com/', origin: 'https://chatgpt.com', hostname: 'chatgpt.com',
    host: 'chatgpt.com', pathname: '/', protocol: 'https:', search: '', hash: '',
    toString() { return this.href; },
  };
  const page = {
    location,
    history: { length: 2, state: null, pushState() {}, replaceState() {}, back() {}, forward() {} },
    navigator: {
      userAgent: input.user_agent, vendor: 'Google Inc.', platform: 'Win32',
      hardwareConcurrency: 8, deviceMemory: 32, maxTouchPoints: 0,
      language: 'zh-CN', languages: ['zh-CN', 'zh;q=0.9', 'en;q=0.8'],
      cookieEnabled: true, storage: { estimate: async () => ({ quota: 0, usage: 0 }), persisted: async () => false },
      webdriver: undefined,
    },
    screen: { width: 800, height: 600, availWidth: 800, availHeight: 600,
      availLeft: 0, availTop: 0, colorDepth: 24, pixelDepth: 24 },
    innerWidth: 1280, innerHeight: 657, devicePixelRatio: 1,
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, String(value)),
      removeItem: key => storage.delete(key),
      key: index => [...storage.keys()][index] ?? null,
      get length() { return storage.size; },
    },
    performance: { now: () => __performanceNow(), memory: { jsHeapSizeLimit: 4395630592 } },
    crypto: { getRandomValues(array) {
      const bytes = __randomBytes(array.byteLength);
      const view = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
      view.set(bytes);
      return array;
    } },
  };
  function createElement(tagName) {
    const attributes = new Map();
    const children = [];
    return {
      tagName: String(tagName).toUpperCase(), style: {}, dataset: {}, children, hidden: false,
      appendChild(child) { children.push(child); return child; },
      removeChild(child) {
        const index = children.indexOf(child);
        if (index >= 0) children.splice(index, 1);
        return child;
      },
      remove() {},
      setAttribute(name, value) { attributes.set(String(name), String(value)); },
      getAttribute(name) { return attributes.get(String(name)) ?? null; },
      hasAttribute(name) { return attributes.has(String(name)); },
      getBoundingClientRect() {
        return { x: 0, y: 0, width: 1280, height: 800, top: 0, left: 0, right: 1280, bottom: 800 };
      },
      getContext() { return null; },
    };
  }
  page.document = {
    scripts: input.scripts.map(src => ({ src })), location,
    documentElement: { dataset: {}, getAttribute: () => null },
    createElement, head: createElement('head'), body: createElement('body'),
  };
  Object.assign(globalThis, page);
  globalThis.window = globalThis;
  globalThis.self = globalThis;
})();
