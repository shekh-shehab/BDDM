// compat.js: one small shim so the same code runs in Chrome, Edge, Brave
// (chrome.* with promises, Manifest V3) and Firefox (browser.*).
// eslint-disable-next-line no-var
var api = globalThis.browser || globalThis.chrome;
// eslint-disable-next-line no-var
var IS_FIREFOX = String(api.runtime.getURL("")).startsWith("moz-extension:");
