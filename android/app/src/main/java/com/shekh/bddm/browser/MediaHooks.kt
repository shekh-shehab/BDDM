package com.shekh.bddm.browser

import android.webkit.JavascriptInterface
import android.webkit.WebView
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import com.shekh.bddm.data.*
import org.json.JSONArray
import org.json.JSONObject

/**
 * Receives hints from the page hooks (all frames) and turns them into detections for one tab.
 * One bridge instance is bound to one tab id, so a hit can never land on the wrong tab.
 */
class DetectBridge(
    private val tabId: String,
    private val pageUrl: () -> String,
    private val pageTitle: () -> String,
    private val isAdUrl: (String) -> Boolean,
) {
    @JavascriptInterface
    fun found(json: String) {
        try {
            val t = json.trim()
            if (t.startsWith("[")) {
                val a = JSONArray(t)
                for (i in 0 until a.length()) handle(a.getJSONObject(i))
            } else {
                handle(JSONObject(t))
            }
        } catch (_: Exception) { }
    }

    private fun handle(o: JSONObject) {
        val type = o.optString("type")
        val url = o.optString("url").take(4000)
        val frame = o.optString("frame").take(4000)
        val top = o.optBoolean("top", true)
        val title = o.optString("title").ifBlank { pageTitle() }
        val page = pageUrl()
        when (type) {
            "mse" -> {
                // Blob/MediaSource player: the real stream is not downloadable from here,
                // so offer the page (or the frame that hosts the player) for yt-dlp.
                val target = if (top || frame.isBlank()) page else frame
                if (target.startsWith("http")) {
                    MediaDetector.add(tabId, DetectedMedia(target, MediaKind.PAGE, MediaClassifier.titleFor(target, title), page, "mse"))
                }
            }
            "iframe" -> {
                if (url.startsWith("http") && MediaClassifier.isEmbedPlayer(url) && !isAdUrl(url)) {
                    MediaDetector.add(tabId, DetectedMedia(url, MediaKind.EMBED, MediaClassifier.titleFor(url, title), page, "iframe"))
                }
            }
            "thumb" -> if (url.startsWith("http")) MediaDetector.setPageThumb(tabId, url)
            "video", "audio", "source" -> {
                if (!url.startsWith("http") || isAdUrl(url)) return
                val kind = MediaClassifier.classify(url)
                    ?: if (MediaClassifier.isSegment(url)) null
                    else if (type == "audio") MediaKind.AUDIO else MediaKind.DIRECT
                if (kind != null) MediaDetector.add(tabId, DetectedMedia(
                    url, kind, MediaClassifier.titleFor(url, title), page, type,
                    width = o.optInt("w", 0), height = o.optInt("h", 0),
                    durationSec = o.optDouble("dur", 0.0).let { if (it.isNaN() || it.isInfinite()) 0.0 else it },
                    thumb = o.optString("poster").takeIf { it.startsWith("http") } ?: "",
                ))
            }
            else -> {
                if (url.startsWith("http") && !isAdUrl(url)) MediaDetector.offer(tabId, url, page, title, type)
            }
        }
    }
}

object MediaHooks {
    const val BRIDGE_NAME = "BDDM"

    /** Installs the hook script so it runs at document start in every frame. Returns false if unsupported. */
    fun installDocumentStart(wv: WebView): Boolean {
        return try {
            if (WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
                WebViewCompat.addDocumentStartJavaScript(wv, HOOK_JS, setOf("*"))
                true
            } else false
        } catch (_: Throwable) { false }
    }

    fun inject(wv: WebView?) { wv?.evaluateJavascript(HOOK_JS, null) }
    fun rescan(wv: WebView?) { wv?.evaluateJavascript("window.__bddmScan && window.__bddmScan();", null) }

    const val HOOK_JS = """
(function(){
  if (window.__bddmHook) return; window.__bddmHook = 1;
  var sent = {};
  var MEDIA = /\.(mp4|webm|mkv|m4v|mov|flv|3gp|m3u8|mpd|mp3|m4a|aac|ogg|opus)(\?|#|${'$'})/i;
  function abs(u){ try { return new URL(u, document.baseURI).href; } catch(e){ return ''; } }
  function send(o){
    try {
      if (!o || !o.url) return;
      if (o.url.indexOf('http') !== 0 && o.type !== 'mse') return;
      var k = o.type + '|' + o.url + '|' + (o.h || 0) + '|' + Math.round(o.dur || 0); if (sent[k]) return; sent[k] = 1;
      o.title = document.title || ''; o.frame = location.href; o.top = (window === window.top);
      var b = window.BDDM; if (b && b.found) b.found(JSON.stringify(o));
    } catch(e){}
  }
  function media(u, type){ u = abs(u); if (u && MEDIA.test(u)) send({url:u, type:type}); }
  function el(e){
    try {
      var s = e.currentSrc || e.src || '';
      if (!s) return;
      if (s.indexOf('blob:') === 0) { send({url: location.href, type:'mse'}); return; }
      var d = isFinite(e.duration) ? e.duration : 0;
      send({url: abs(s), type: (e.tagName || 'video').toLowerCase(), w: e.videoWidth || 0, h: e.videoHeight || 0, dur: d, poster: e.poster ? abs(e.poster) : ''});
    } catch(x){}
  }
  try { var xo = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function(m, u){ try { media(String(u), 'xhr'); } catch(e){} return xo.apply(this, arguments); }; } catch(e){}
  try { var fo = window.fetch;
    if (fo) window.fetch = function(i){ try { media(String(i && i.url ? i.url : i), 'fetch'); } catch(e){} return fo.apply(this, arguments); }; } catch(e){}
  try { var co = URL.createObjectURL;
    URL.createObjectURL = function(o){ try { if (o && o.constructor && o.constructor.name === 'MediaSource') send({url: location.href, type:'mse'}); } catch(e){} return co.apply(this, arguments); }; } catch(e){}
  ['loadstart','loadedmetadata','play','playing','durationchange'].forEach(function(n){
    document.addEventListener(n, function(ev){ var t = ev.target; if (t && (t.tagName === 'VIDEO' || t.tagName === 'AUDIO')) el(t); }, true);
  });
  function scan(){
    try {
      document.querySelectorAll('video,audio').forEach(function(v){
        el(v);
        v.querySelectorAll('source').forEach(function(s){ if (s.src) send({url: abs(s.src), type:'source'}); });
      });
      document.querySelectorAll('meta[property="og:video"],meta[property="og:video:url"],meta[property="og:video:secure_url"],meta[name="twitter:player:stream"]').forEach(function(m){
        var c = m.getAttribute('content'); if (c) { c = abs(c); if (MEDIA.test(c)) send({url: c, type:'og'}); }
      });
      document.querySelectorAll('iframe[src]').forEach(function(f){ send({url: abs(f.src), type:'iframe'}); });
      if (window === window.top) {
        var og = document.querySelector('meta[property="og:image"],meta[name="twitter:image"]');
        if (og && og.getAttribute('content')) send({url: abs(og.getAttribute('content')), type:'thumb'});
      }
      document.querySelectorAll('a[href]').forEach(function(a){ var h = a.href || ''; if (MEDIA.test(h)) send({url: h, type:'link'}); });
      if (window.jwplayer) { try {
        var p = window.jwplayer(); var it = p && p.getPlaylistItem && p.getPlaylistItem();
        if (it) { (it.sources || []).forEach(function(s){ if (s && s.file) media(s.file, 'jw'); }); if (it.file) media(it.file, 'jw'); }
      } catch(e){} }
      if (window.videojs && window.videojs.getPlayers) { try {
        var ps = window.videojs.getPlayers();
        Object.keys(ps).forEach(function(k){ var pl = ps[k]; if (pl && pl.currentSrc) media(pl.currentSrc(), 'vjs'); });
      } catch(e){} }
    } catch(e){}
  }
  window.__bddmScan = scan;
  window.__bddmReset = function(){ sent = {}; };
  var pend = 0;
  function later(){ if (pend) return; pend = setTimeout(function(){ pend = 0; scan(); }, 300); }
  try { new MutationObserver(later).observe(document, {subtree:true, childList:true, attributes:true, attributeFilter:['src']}); } catch(e){}
  document.addEventListener('DOMContentLoaded', scan);
  window.addEventListener('load', scan);
  var n = 0; var iv = setInterval(function(){ scan(); if (++n > 40) clearInterval(iv); }, 1500);
})();
"""
}
