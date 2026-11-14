// "Already downloaded" box: Download again (force) or Cancel.
const key = new URLSearchParams(location.search).get("k") || "";
const $ = (id) => document.getElementById(id);
let answered = false;

api.storage.session.get(key).then((got) => {
  const p = got[key];
  if (!p) {
    $("name").textContent = "(this question has expired)";
    $("again").disabled = true;
    return;
  }
  let name = p.name || p.req.filename || "";
  if (!name) {
    try { name = decodeURIComponent(new URL(p.req.url).pathname.split("/").pop()); } catch (e) { name = p.req.url; }
  }
  $("name").textContent = name || p.req.url;
});

async function answer(again) {
  answered = true;
  $("again").disabled = $("cancel").disabled = true;
  const r = await api.runtime.sendMessage({ type: "dupAnswer", key, again }).catch((e) => ({ ok: false, error: e.message }));
  if (r && r.ok) {
    window.close();
  } else {
    $("err").textContent = (r && r.error) || "Something went wrong.";
    $("cancel").disabled = false;
    $("cancel").textContent = "Close";
    $("cancel").onclick = () => window.close();
  }
}

$("again").onclick = () => answer(true);
$("cancel").onclick = () => answer(false);
window.addEventListener("beforeunload", () => {
  if (!answered) api.runtime.sendMessage({ type: "dupAnswer", key, again: false }).catch(() => {});
});
