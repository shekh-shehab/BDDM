// BDDM: hold Alt while clicking a link to let Chrome download it itself
// (Alt+click is also Chrome's own "download this link" shortcut).
(() => {
  document.addEventListener("click", (e) => {
    if (!e.altKey) return;
    const a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
    if (!a) return;
    try {
      api.runtime.sendMessage({ type: "skipNext" }).catch(() => {});
    } catch (err) {
      // The extension was reloaded; nothing to do.
    }
  }, true);
})();

// Google Drive: Drive draws its own right-click menu, and its file list has no
// real links. Remember which file(s) the user right-clicked (Drive list and
// grid items carry data-id="<file or folder id>"), so "Download with BDDM"
// sends that file instead of the address of the page.
(() => {
  if (location.hostname !== "drive.google.com" || window !== window.top) return;
  const ID_RE = /^[A-Za-z0-9_-]{10,}$/;
  const MAX_ITEMS = 20;
  const FRESH = 2 * 60 * 1000; // a right-click is used for two minutes
  let last = null; // { items: [{id, folder}], at }

  const idOf = (el) => {
    const id = el && el.getAttribute ? el.getAttribute("data-id") : "";
    return id && ID_RE.test(id) ? id : "";
  };

  function itemOf(el) {
    for (let e = el; e && e !== document.documentElement; e = e.parentElement) {
      if (idOf(e)) return e;
    }
    return null;
  }

  // A hint only; the background script asks Drive what the id really is.
  function looksLikeFolder(el) {
    const parts = [el.getAttribute("aria-label") || "", el.getAttribute("data-tooltip") || ""];
    const marks = el.querySelectorAll("[aria-label],[data-tooltip]");
    for (let i = 0; i < marks.length && i < 30; i++) {
      parts.push(marks[i].getAttribute("aria-label") || "", marks[i].getAttribute("data-tooltip") || "");
    }
    return /\b(shared )?folder\b/i.test(parts.join(" "));
  }

  function selectedItems() {
    const out = [];
    const seen = new Set();
    for (const e of document.querySelectorAll('[data-id][aria-selected="true"], [data-id][aria-checked="true"]')) {
      const id = idOf(e);
      // Not the folder tree on the left (it marks the open folder as selected).
      if (e.closest('[role="tree"], [role="navigation"], nav')) continue;
      if (id && !seen.has(id)) {
        seen.add(id);
        out.push(e);
      }
    }
    return out;
  }

  const describe = (els) => els.slice(0, MAX_ITEMS).map((e) => ({ id: idOf(e), folder: looksLikeFolder(e) }));

  function record(ev) {
    const it = itemOf(ev.target);
    if (!it) {
      last = { items: [], at: Date.now() };
      return;
    }
    const sel = selectedItems();
    const isSel = sel.some((e) => idOf(e) === idOf(it));
    // Right-click on one of several selected items: all of them.
    last = { items: describe(isSel && sel.length > 1 ? sel : [it]), at: Date.now() };
  }

  // On window, in the capture phase, added before Drive's own scripts run:
  // this sees the right-click even when Drive shows its own menu.
  window.addEventListener("contextmenu", record, true);
  window.addEventListener("mousedown", (e) => { if (e.button === 2) record(e); }, true);

  try {
    api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
      if (!msg || msg.type !== "driveTarget") return false;
      // A zip download from Drive's own menu can take minutes to prepare, so
      // the background may ask for an older right-click (maxAge).
      const maxAge = typeof msg.maxAge === "number" ? msg.maxAge : FRESH;
      let items = last && Date.now() - last.at < maxAge ? last.items : [];
      let from = "click";
      // Drive zips what is selected (a right-click selects the item too).
      if (msg.preferSelection) {
        const sel = describe(selectedItems());
        if (sel.length) { items = sel; from = "selection"; }
      }
      if (!items.length) {
        // Nothing under the mouse (for example the file preview is open):
        // the selected file(s) in the list.
        items = describe(selectedItems());
        from = "selection";
      }
      sendResponse({ items, from, page: location.href, at: last ? last.at : 0 });
      return false;
    });
  } catch (err) {
    // The extension was reloaded; nothing to do.
  }
})();
