// BDDM theme (item 78): Settings > Theme is System (follow Windows), Light
// or Dark. Loaded in <head> of every BDDM window so the chosen theme
// applies before the page draws, and again at once when it changes.
// A ?scheme=dark|light in the address (screenshots, tests) always wins.
(function () {
  "use strict";
  var root = document.documentElement;
  var forced = new URLSearchParams(location.search).get("scheme");
  if (forced !== "dark" && forced !== "light") forced = "";
  var mq = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  var KEY = "bddm-theme";
  var mode = "system";
  var posted = "";

  function norm(m) { return m === "light" || m === "dark" ? m : "system"; }
  try { mode = norm(localStorage.getItem(KEY)); } catch (e) { /* storage off */ }

  function effective() {
    if (forced) return forced;
    if (mode !== "system") return mode;
    return mq && mq.matches ? "dark" : "light";
  }

  function apply() {
    if (forced) root.dataset.scheme = forced;
    else if (mode !== "system") root.dataset.scheme = mode;
    else delete root.dataset.scheme;
    var eff = effective();
    // Tell the WebView2 window so its title bar matches.
    if (eff !== posted && window.chrome && window.chrome.webview) {
      try { window.chrome.webview.postMessage("bddm:theme:" + eff); posted = eff; } catch (e) { /* ignore */ }
    }
  }

  window.bddmTheme = function () { return mode; };
  window.bddmEffectiveScheme = effective;
  window.bddmSetTheme = function (m) {
    m = norm(m);
    if (m !== mode) {
      mode = m;
      try { localStorage.setItem(KEY, m); } catch (e) { /* ignore */ }
    }
    apply();
  };

  function refresh() {
    if (!window.fetch) return;
    fetch("/api/theme", { cache: "no-store" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) { if (j && norm(j.theme) !== mode) window.bddmSetTheme(j.theme); })
      .catch(function () { /* BDDM not reachable */ });
  }

  apply();
  if (mq) {
    if (mq.addEventListener) mq.addEventListener("change", apply);
    else if (mq.addListener) mq.addListener(apply);
  }
  window.addEventListener("storage", function (e) {
    if (e.key === KEY) { mode = norm(e.newValue); apply(); }
  });
  window.addEventListener("focus", refresh);
  document.addEventListener("visibilitychange", function () { if (!document.hidden) refresh(); });
  refresh();
  setInterval(refresh, 1500);
})();
