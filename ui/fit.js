// Size a compact BDDM popup to its content. Temporarily unlocks height:100%
// and flex-grow so scrollHeight is the content, not the current window.
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
  function measure() {
    const html = document.documentElement;
    const body = document.body;
    if (!body) return { width: 0, height: 0 };
    const saved = [];
    unlock(html, saved);
    unlock(body, saved);
    body.querySelectorAll("main, header, footer, .window, #fitRoot").forEach(function (el) {
      unlock(el, saved);
    });
    void body.offsetHeight;
    const w = Math.ceil(Math.max(
      body.scrollWidth || 0,
      html.scrollWidth || 0,
      body.getBoundingClientRect().width || 0
    ));
    const h = Math.ceil(Math.max(
      body.scrollHeight || 0,
      html.scrollHeight || 0,
      body.getBoundingClientRect().height || 0
    ));
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
  window.bddmFitWindow = schedule;
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", schedule);
  } else {
    schedule();
  }
  window.addEventListener("load", schedule);
})();
