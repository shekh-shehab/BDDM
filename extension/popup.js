// BDDM popup: videos on this page, on/off switch, connection status,
// open BDDM, sites to skip.
const BDDM = "http://127.0.0.1:9614";
const $ = (id) => document.getElementById(id);

function cleanHost(s) {
  s = String(s || "").trim().toLowerCase();
  if (!s) return "";
  try { if (s.includes("://")) s = new URL(s).hostname; } catch (e) { /* keep */ }
  return s.replace(/^\*\./, "").replace(/^www\./, "").replace(/\/.*$/, "");
}

function covers(host, ex) {
  return host === ex || host.endsWith("." + ex);
}

async function load() {
  const s = await api.storage.local.get({ enabled: true, excluded: [] });
  const excluded = Array.isArray(s.excluded) ? s.excluded : [];
  $("enabled").checked = s.enabled !== false;
  $("excluded").value = excluded.join("\n");

  const tab = await pageTab();
  let host = "";
  try { if (tab && /^https?:/i.test(tab.url)) host = new URL(tab.url).hostname.toLowerCase(); } catch (e) { /* none */ }
  if (!host) {
    $("siteName").textContent = "Not a web page";
    $("siteOn").disabled = true;
  } else {
    $("siteName").textContent = host;
    $("siteOn").checked = !excluded.some((ex) => covers(host, cleanHost(ex)));
  }
  $("siteOn").dataset.host = host;

  const r = await api.runtime.sendMessage({ type: "ping" }).catch(() => ({ ok: false }));
  setStatus(r && r.ok);
}

function setStatus(on) {
  const el = $("status");
  el.className = "status " + (on ? "on" : "off");
  el.textContent = on ? "Connected" : "Not running";
  $("offlineHelp").hidden = !!on;
}

async function saveExcluded(list) {
  const clean = [...new Set(list.map(cleanHost).filter(Boolean))];
  await api.storage.local.set({ excluded: clean });
  $("excluded").value = clean.join("\n");
  return clean;
}

$("enabled").onchange = async () => {
  await api.storage.local.set({ enabled: $("enabled").checked });
  api.runtime.sendMessage({ type: "settingsChanged" }).catch(() => {});
};

$("siteOn").onchange = async () => {
  const host = $("siteOn").dataset.host;
  if (!host) return;
  const { excluded = [] } = await api.storage.local.get({ excluded: [] });
  let list = excluded.map(cleanHost);
  if ($("siteOn").checked) {
    list = list.filter((ex) => !covers(host, ex));
  } else {
    list.push(host.replace(/^www\./, ""));
  }
  await saveExcluded(list);
};

$("save").onclick = async () => {
  await saveExcluded($("excluded").value.split(/[\n,]+/));
  $("saved").textContent = "Saved";
  setTimeout(() => ($("saved").textContent = ""), 1500);
};

$("open").onclick = async () => {
  try {
    const r = await fetch(BDDM + "/ext/show", { method: "POST" });
    if (!r.ok) throw new Error("status " + r.status);
    setStatus(true);
    window.close();
  } catch (e) {
    setStatus(false);
  }
};

// Firefox lets people switch off site access for Manifest V3 add-ons.
const ALL = { origins: ["<all_urls>"] };
async function checkPermission() {
  if (!api.permissions || !api.permissions.contains) return;
  const ok = await api.permissions.contains(ALL).catch(() => true);
  $("permRow").hidden = ok;
}
$("grant").onclick = async () => {
  await api.permissions.request(ALL).catch(() => false);
  checkPermission();
};

// ---------- videos on this page ----------
function cleanTitle(t) {
  return String(t || "").replace(/^\(\d+\)\s+/, "").replace(/\s+[-|\u2022\u00b7]\s+(YouTube|Facebook|Instagram|TikTok|Vimeo|X|Twitter|Dailymotion|Twitch|Reddit)\s*$/i, "").trim();
}
function hostName(u) {
  try { return new URL(u).hostname.replace(/^www\./, ""); } catch (e) { return ""; }
}
function videoTitle(v) {
  if (v.kind === "page") return cleanTitle(v.title) || "This video";
  if (v.kind === "embed") return cleanTitle(v.title) || "Embedded video";
  return v.label;
}
function videoSub(v) {
  if (v.kind === "page") return hostName(v.url);
  if (v.kind === "embed") return "Embedded player \u00b7 " + hostName(v.url);
  return hostName(v.url) + (v.title ? " \u00b7 " + cleanTitle(v.title) : "");
}

// The page the popup belongs to (when the popup is opened as a tab, the
// web page used last).
async function pageTab() {
  const [tab] = await api.tabs.query({ active: true, currentWindow: true });
  if (tab && /^https?:/i.test(tab.url || "")) return tab;
  const all = await api.tabs.query({ currentWindow: true });
  return all.filter((t) => /^https?:/i.test(t.url || "")).sort((a, b) => (b.lastAccessed || 0) - (a.lastAccessed || 0))[0] || tab;
}

async function loadVideos() {
  const tab = await pageTab();
  if (!tab) return;
  const r = await api.runtime.sendMessage({ type: "listVideos", tabId: tab.id }).catch(() => null);
  const list = (r && r.list) || [];
  const box = $("videos");
  box.hidden = list.length === 0;
  $("videoCount").textContent = list.length === 1 ? "1 video" : list.length + " videos";
  const ul = $("videoList");
  ul.textContent = "";
  for (const v of list) {
    const li = document.createElement("li");
    const text = document.createElement("div");
    text.className = "vtext";
    const t = document.createElement("div");
    t.className = "vtitle";
    t.textContent = videoTitle(v);
    t.title = v.url;
    const sub = document.createElement("div");
    sub.className = "hint";
    sub.textContent = videoSub(v);
    text.append(t, sub);
    const b = document.createElement("button");
    b.className = "small primary";
    b.textContent = "Download with BDDM";
    b.onclick = async () => {
      b.disabled = true;
      b.textContent = "Sending...";
      const res = await api.runtime.sendMessage({ type: "sendVideo", tabId: tab.id, url: v.url, title: v.kind === "page" ? v.title : "" }).catch((e) => ({ ok: false, error: e.message }));
      if (res && res.ok) {
        b.textContent = "Sent";
        $("videoMsg").className = "help ok";
        $("videoMsg").textContent = "Choose the quality in the BDDM window.";
        setStatus(true);
        setTimeout(() => window.close(), 1200);
      } else {
        b.disabled = false;
        b.textContent = "Download with BDDM";
        $("videoMsg").className = "help";
        $("videoMsg").textContent = (res && res.error) || "BDDM could not add this video.";
      }
      $("videoMsg").hidden = false;
    };
    li.append(text, b);
    ul.append(li);
  }
}

load();
loadVideos();
checkPermission();
