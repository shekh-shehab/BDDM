// Size a compact BDDM popup to its content.
//
// The window is opened at a default size that is usually bigger than the
// content. A page cannot learn its content size from html.scrollHeight or the
// body width: those are never smaller than the current window, so the window
// could only grow. Instead the page is laid out with automatic height (and,
// when the page asks for it, at its natural width) and the body is measured.
//
// A page can set on <body>:
//   data-fit-min / data-fit-max  limits for the natural content width (px).
// Without data-fit-max the window keeps its width and only the height fits.
(function () {
  function unlock(el, saved) {
    if (!el || !el.style) return;
    saved.push([el, el.style.cssText]);
    el.style.height = "auto";
    el.style.minHeight = "0";
    el.style.maxHeight = "none";
    el.style.flex = "none";
    el.style.overflow = "visible";
  }
  function num(v) {
    var n = parseFloat(v);
    return isFinite(n) && n > 0 ? n : 0;
  }
  function measure() {
    var html = document.documentElement;
    var body = document.body;
    if (!body) return { width: 0, height: 0 };
    var saved = [];
    unlock(html, saved);
    unlock(body, saved);
    body.querySelectorAll("main, header, footer, .window, #fitRoot").forEach(function (el) {
      unlock(el, saved);
    });
    var minW = num(body.getAttribute("data-fit-min"));
    var maxW = num(body.getAttribute("data-fit-max"));
    var w;
    if (maxW > 0) {
      body.style.width = "max-content";
      void body.offsetWidth;
      var natural = Math.ceil(body.getBoundingClientRect().width);
      w = Math.min(maxW, Math.max(minW, natural));
      body.style.width = w + "px";
      void body.offsetWidth;
      // Something wider than the chosen width: grow, up to the limit.
      w = Math.min(maxW, Math.max(w, Math.ceil(body.scrollWidth || 0)));
      body.style.width = w + "px";
    } else {
      w = Math.ceil(Math.max(body.scrollWidth || 0, body.getBoundingClientRect().width || 0));
    }
    void body.offsetHeight;
    var h = Math.ceil(Math.max(body.scrollHeight || 0, body.getBoundingClientRect().height || 0));
    for (var i = saved.length - 1; i >= 0; i--) saved[i][0].style.cssText = saved[i][1];
    return {
      width: w,
      height: h,
      title: document.title || "",
      path: location.pathname || ""
    };
  }
  var timer = 0;
  var lastKey = "";
  function report() {
    var m = measure();
    if (m.width < 80 || m.height < 80) return;
    document.documentElement.setAttribute("data-bddm-fit", m.width + "x" + m.height);
    var key = m.width + "x" + m.height + ":" + m.title;
    if (key === lastKey) return;
    lastKey = key;
    fetch("/api/window/fit", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(m)
    }).catch(function () {});
  }
  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(report, 60);
  }
  window.bddmMeasureFit = measure;
  window.bddmFitWindow = schedule;
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", schedule);
  } else {
    schedule();
  }
  window.addEventListener("load", schedule);
})();
