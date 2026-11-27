package com.shekh.bddm.browser

import android.annotation.SuppressLint
import android.content.Intent
import android.graphics.Bitmap
import android.os.Bundle
import android.view.ViewGroup
import android.webkit.*
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import com.shekh.bddm.BDDMApp
import com.shekh.bddm.data.*
import com.shekh.bddm.service.DownloadService
import com.shekh.bddm.ui.BDDMTheme
import com.shekh.bddm.ui.ConfirmQueueSheet
import com.shekh.bddm.ui.MutedDark
import com.shekh.bddm.ui.SuccessDark
import com.shekh.bddm.ui.TextAccentDark
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

class BrowserActivity : ComponentActivity() {
    lateinit var store: BrowserStore
    var webView: WebView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        store = BrowserStore(this, BDDMApp.instance.prefs)
        store.autoCloseOldTabs()
        val startUrl = intent?.getStringExtra("query")
            ?: intent?.getStringExtra(android.app.SearchManager.QUERY)
            ?: intent?.dataString
        if (!startUrl.isNullOrBlank()) {
            val url = if (startUrl.startsWith("http")) startUrl else BDDMApp.instance.prefs.searchEngine + android.net.Uri.encode(startUrl)
            val cur = store.tabs.getOrNull(store.currentIndex())
            if (cur == null || cur.url.isNotBlank()) store.addTab(url = url) else { cur.url = url; store.saveTabs() }
        }
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                val wv = webView
                if (wv != null && wv.canGoBack()) wv.goBack() else finish()
            }
        })
        setContent { BrowserRoot(this) }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        val url = intent.dataString ?: intent.getStringExtra("query")
        if (!url.isNullOrBlank()) {
            store.addTab(url = if (url.startsWith("http")) url else BDDMApp.instance.prefs.searchEngine + android.net.Uri.encode(url))
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
@Composable
fun BrowserRoot(act: BrowserActivity) {
    val prefs = BDDMApp.instance.prefs
    val store = act.store
    var tabTick by remember { mutableStateOf(0) }
    val tabs = store.tabs
    var idx by remember { mutableStateOf(store.currentIndex()) }
    val tab = tabs.getOrNull(idx) ?: tabs.first()
    var address by remember { mutableStateOf(tab.url) }
    var detected by remember { mutableStateOf(listOf<PendingDownload>()) }
    var showDetected by remember { mutableStateOf(false) }
    var showTabs by remember { mutableStateOf(false) }
    var showMenu by remember { mutableStateOf(false) }
    var showBookmarks by remember { mutableStateOf(false) }
    var showHistory by remember { mutableStateOf(false) }
    var blocked by remember { mutableStateOf(0) }
    var adOn by remember { mutableStateOf(prefs.adblockOnFor(AdBlocker.hostOf(tab.url) ?: "")) }
    val pending by PendingQueue.items.collectAsState()
    val sheetOpen by PendingQueue.open.collectAsState()
    val scope = rememberCoroutineScope()
    val ctx = LocalContext.current
    val ad = BDDMApp.instance.adBlocker

    fun load(raw: String) {
        val text = raw.trim()
        val url = when {
            text.startsWith("http://") || text.startsWith("https://") -> text
            text.contains(".") && !text.contains(' ') -> "https://$text"
            text.isBlank() -> return
            else -> prefs.searchEngine + android.net.Uri.encode(text)
        }
        address = url
        tab.url = url
        store.touch(tab)
        act.webView?.loadUrl(url)
    }

    BDDMTheme(prefs.theme) {
        Scaffold { pad ->
            Column(Modifier.fillMaxSize().padding(pad).background(MaterialTheme.colorScheme.background)) {
                // Vivaldi-style tab strip
                Row(Modifier.fillMaxWidth().padding(horizontal = 6.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    LazyRow(Modifier.weight(1f), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                        items(tabs, key = { it.id }) { t ->
                            val i = tabs.indexOf(t)
                            Surface(
                                shape = RoundedCornerShape(10.dp, 10.dp, 0.dp, 0.dp),
                                color = if (i == idx) MaterialTheme.colorScheme.surface else MaterialTheme.colorScheme.surfaceVariant,
                                modifier = Modifier.widthIn(max = 160.dp).clickable {
                                    idx = i; store.setCurrent(i); address = t.url; tabTick++
                                    act.webView?.loadUrl(t.url.ifBlank { "about:blank" })
                                }
                            ) {
                                Row(Modifier.padding(8.dp, 6.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Text(t.title.ifBlank { "New tab" }, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                                    Icon(Icons.Default.Close, "Close", modifier = Modifier.size(14.dp).clickable {
                                        store.closeTab(t.id)
                                        idx = store.currentIndex()
                                        tabTick++
                                    })
                                }
                            }
                        }
                    }
                    IconButton(onClick = { store.addTab(); idx = store.currentIndex(); address = ""; tabTick++; act.webView?.loadUrl("about:blank") }) {
                        Icon(Icons.Default.Add, "New tab")
                    }
                }
                // Address bar + shield
                Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = { act.webView?.goBack() }) { Icon(Icons.Default.ArrowBack, "Back") }
                    OutlinedTextField(
                        value = address,
                        onValueChange = { address = it },
                        modifier = Modifier.weight(1f).height(52.dp),
                        singleLine = true,
                        textStyle = LocalTextStyle.current.copy(fontSize = 13.sp),
                        shape = RoundedCornerShape(24.dp),
                        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go),
                        keyboardActions = KeyboardActions(onGo = { load(address) }),
                    )
                    Box {
                        IconButton(onClick = {
                            val host = AdBlocker.hostOf(tab.url) ?: return@IconButton
                            adOn = !prefs.adblockOnFor(host)
                            prefs.setAdblockFor(host, adOn)
                            act.webView?.reload()
                        }) { Icon(Icons.Default.VerifiedUser, "Ad blocker", tint = if (adOn) SuccessDark else MutedDark) }
                        if (blocked > 0) {
                            Text("$blocked", fontSize = 9.sp, fontWeight = FontWeight.Bold,
                                color = Color(0xFF052E16),
                                modifier = Modifier.align(Alignment.TopEnd).clip(CircleShape).background(SuccessDark).padding(horizontal = 4.dp, vertical = 1.dp))
                        }
                    }
                    IconButton(onClick = { showMenu = true }) { Icon(Icons.Default.MoreVert, "Menu") }
                    DropdownMenu(showMenu, { showMenu = false }) {
                        DropdownMenuItem(text = { Text("New incognito tab") }, onClick = {
                            showMenu = false; store.addTab(incognito = true); idx = store.currentIndex(); address = ""; tabTick++
                        })
                        DropdownMenuItem(text = { Text("Tabs overview") }, onClick = { showMenu = false; showTabs = true })
                        DropdownMenuItem(text = { Text("Bookmarks") }, onClick = { showMenu = false; showBookmarks = true })
                        DropdownMenuItem(text = { Text("History") }, onClick = { showMenu = false; showHistory = true })
                        DropdownMenuItem(text = { Text("Bookmark this page") }, onClick = {
                            showMenu = false; store.addBookmark(tab.title, tab.url)
                        })
                        DropdownMenuItem(text = { Text("Share") }, onClick = {
                            showMenu = false
                            val send = Intent(Intent.ACTION_SEND).apply { type = "text/plain"; putExtra(Intent.EXTRA_TEXT, tab.url) }
                            ctx.startActivity(Intent.createChooser(send, "Share"))
                        })
                    }
                }
                val host = AdBlocker.hostOf(tab.url)
                if (prefs.adBlocker && host != null && tab.url.startsWith("http")) {
                    Row(
                        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp)
                            .clip(RoundedCornerShape(12.dp)).background(Color(0xFF1D3A2D)).padding(8.dp, 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(Icons.Default.VerifiedUser, null, tint = SuccessDark, modifier = Modifier.size(18.dp))
                        Spacer(Modifier.width(8.dp))
                        Column(Modifier.weight(1f)) {
                            Text("$blocked blocked", color = SuccessDark, fontWeight = FontWeight.SemiBold, fontSize = 12.sp)
                            Text(if (adOn) "Ad blocker on for $host" else "Ad blocker off for $host", color = SuccessDark, fontSize = 11.sp)
                        }
                        Switch(checked = adOn, onCheckedChange = {
                            adOn = it; prefs.setAdblockFor(host, it); act.webView?.reload()
                        })
                    }
                }
                Box(Modifier.weight(1f)) {
                    key(tab.id, tabTick) {
                        AndroidView(factory = { c ->
                            WebView(c).apply {
                                act.webView = this
                                layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                                settings.javaScriptEnabled = true
                                settings.domStorageEnabled = !tab.incognito
                                settings.mediaPlaybackRequiresUserGesture = false
                                settings.allowFileAccess = false
                                settings.mixedContentMode = WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE
                                settings.userAgentString = settings.userAgentString.replace("; wv", "")
                                cookie(tab.incognito)
                                addJavascriptInterface(DetectBridge { list -> detected = list }, "BDDM")
                                webViewClient = object : WebViewClient() {
                                    override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                                        val pageHost = AdBlocker.hostOf(tab.url) ?: request.url.host ?: ""
                                        return ad.intercept(request, pageHost)
                                    }
                                    override fun onPageStarted(view: WebView?, url: String?, favicon: Bitmap?) {
                                        ad.resetPageCount()
                                        blocked = 0
                                        detected = emptyList()
                                        if (url != null && url.startsWith("http")) {
                                            address = url
                                            tab.url = url
                                        }
                                    }
                                    override fun onPageFinished(view: WebView?, url: String?) {
                                        blocked = ad.blockedOnPage.get()
                                        tab.blocked = blocked
                                        if (url != null && url.startsWith("http")) {
                                            tab.title = view?.title ?: url
                                            tab.url = url
                                            address = url
                                            store.touch(tab)
                                            if (!tab.incognito) store.addHistory(tab.title, url)
                                        }
                                        view?.evaluateJavascript(DETECT_JS, null)
                                        // Always offer the page itself on known video sites.
                                        if (url != null && YtDlp.isSitePage(url)) {
                                            val already = detected.any { it.url == url }
                                            if (!already) {
                                                detected = listOf(PendingDownload(url, view?.title ?: url, "Page video", kind = PendingDownload.Kind.Video)) + detected
                                            }
                                        }
                                    }
                                    override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                                        val u = request?.url?.toString() ?: return false
                                        if (u.startsWith("http")) return false
                                        return true
                                    }
                                }
                                webChromeClient = object : WebChromeClient() {
                                    override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: android.os.Message?): Boolean {
                                        if (prefs.blockPopups && !isUserGesture) return false
                                        if (prefs.blockPopups && isDialog) return false
                                        return false // open in a new tab via onCreateWindow is complex; block popups
                                    }
                                    override fun onReceivedTitle(view: WebView?, title: String?) {
                                        if (!title.isNullOrBlank()) tab.title = title
                                    }
                                }
                                loadUrl(tab.url.ifBlank { "about:blank" })
                            }
                        }, modifier = Modifier.fillMaxSize(), update = { wv -> act.webView = wv })
                    }
                    if (detected.isNotEmpty()) {
                        FloatingActionButton(
                            onClick = { showDetected = true },
                            modifier = Modifier.align(Alignment.BottomEnd).padding(16.dp, 16.dp),
                            containerColor = TextAccentDark,
                        ) {
                            Box {
                                Icon(Icons.Default.Download, "Download")
                                Text("${detected.size}", fontSize = 10.sp, fontWeight = FontWeight.Bold,
                                    modifier = Modifier.align(Alignment.TopEnd).offset(8.dp, (-8).dp)
                                        .clip(CircleShape).background(Color.White).padding(horizontal = 5.dp))
                            }
                        }
                    }
                }
            }
        }
        if (showDetected) {
            DetectedSheet(detected, onDismiss = { showDetected = false }, onDownload = { item ->
                showDetected = false
                scope.launch {
                    var ready = item
                    if (item.kind == PendingDownload.Kind.Video && item.formats.isEmpty() && YtDlp.ready) {
                        ready = try {
                            val info = withContext(Dispatchers.IO) { YtDlp.probe(item.url) }
                            item.copy(title = info.title, formats = info.formats, subtitle = info.site + if (info.duration.isNotBlank()) " · ${info.duration}" else "")
                        } catch (_: Throwable) { item }
                    }
                    PendingQueue.add(ready)
                }
            }, onDownloadAll = {
                showDetected = false
                detected.forEach { PendingQueue.add(it) }
            })
        }
        if (sheetOpen && pending.isNotEmpty()) {
            ConfirmQueueSheet(
                items = pending,
                onChange = { PendingQueue.update(it) },
                onDismiss = { PendingQueue.close() },
                onConfirm = { later ->
                    val selected = pending.filter { it.selected }
                    PendingQueue.close()
                    selected.forEach { item ->
                        when (item.kind) {
                            PendingDownload.Kind.Video -> Downloads.startVideo(ctx, item, later)
                            else -> Downloads.startHttp(item.url, later)
                        }
                    }
                },
            )
        }
        if (showTabs) {
            AlertDialog(
                onDismissRequest = { showTabs = false },
                title = { Text("Tabs") },
                text = {
                    LazyColumn {
                        items(tabs.size) { i ->
                            val t = tabs[i]
                            ListItem(
                                headlineContent = { Text(t.title.ifBlank { "New tab" }, maxLines = 1) },
                                supportingContent = { Text(if (t.incognito) "Incognito · ${t.url}" else t.url, maxLines = 1, fontSize = 12.sp) },
                                modifier = Modifier.clickable {
                                    idx = i; store.setCurrent(i); address = t.url; tabTick++; showTabs = false
                                    act.webView?.loadUrl(t.url.ifBlank { "about:blank" })
                                },
                                trailingContent = {
                                    IconButton(onClick = { store.closeTab(t.id); idx = store.currentIndex(); tabTick++ }) {
                                        Icon(Icons.Default.Close, null)
                                    }
                                },
                            )
                        }
                    }
                },
                confirmButton = { TextButton(onClick = { showTabs = false }) { Text("Close") } },
            )
        }
        if (showBookmarks) {
            val marks = store.bookmarks()
            AlertDialog(
                onDismissRequest = { showBookmarks = false },
                title = { Text("Bookmarks") },
                text = {
                    if (marks.isEmpty()) Text("No bookmarks yet.")
                    else LazyColumn {
                        items(marks.size) { i ->
                            val b = marks[i]
                            ListItem(
                                headlineContent = { Text(b.title, maxLines = 1) },
                                supportingContent = { Text(b.url, maxLines = 1, fontSize = 12.sp) },
                                modifier = Modifier.clickable { load(b.url); showBookmarks = false },
                                trailingContent = {
                                    IconButton(onClick = { store.removeBookmark(b.id); showBookmarks = false; showBookmarks = true }) {
                                        Icon(Icons.Default.Delete, null)
                                    }
                                },
                            )
                        }
                    }
                },
                confirmButton = { TextButton(onClick = { showBookmarks = false }) { Text("Close") } },
            )
        }
        if (showHistory) {
            val hist = store.history()
            AlertDialog(
                onDismissRequest = { showHistory = false },
                title = { Text("History") },
                text = {
                    if (hist.isEmpty()) Text("No history yet.")
                    else LazyColumn {
                        items(hist.size) { i ->
                            val h = hist[i]
                            ListItem(
                                headlineContent = { Text(h.title, maxLines = 1) },
                                supportingContent = { Text(h.url, maxLines = 1, fontSize = 12.sp) },
                                modifier = Modifier.clickable { load(h.url); showHistory = false },
                            )
                        }
                    }
                },
                confirmButton = { TextButton(onClick = { showHistory = false }) { Text("Close") } },
                dismissButton = { TextButton(onClick = { store.clearHistory(); showHistory = false }) { Text("Clear") } },
            )
        }
    }
}

@Composable
private fun DetectedSheet(items: List<PendingDownload>, onDismiss: () -> Unit, onDownload: (PendingDownload) -> Unit, onDownloadAll: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Detected on this page") },
        text = {
            LazyColumn {
                items(items.size) { i ->
                    val p = items[i]
                    ListItem(
                        headlineContent = { Text(p.title, maxLines = 1) },
                        supportingContent = { Text(p.subtitle.ifBlank { p.url }, maxLines = 2, fontSize = 12.sp) },
                        trailingContent = { TextButton(onClick = { onDownload(p) }) { Text("Download") } },
                    )
                }
            }
        },
        confirmButton = { TextButton(onClick = onDownloadAll) { Text("Add all") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Close") } },
    )
}

private fun WebView.cookie(incognito: Boolean) {
    val cm = CookieManager.getInstance()
    // CookieManager is process-wide, so do not turn cookies off globally for incognito.
    cm.setAcceptThirdPartyCookies(this, !incognito)
    if (incognito) {
        settings.cacheMode = WebSettings.LOAD_NO_CACHE
        settings.domStorageEnabled = false
    }
}

class DetectBridge(val onList: (List<PendingDownload>) -> Unit) {
    @android.webkit.JavascriptInterface
    fun found(json: String) {
        try {
            val a = JSONArray(json)
            val list = ArrayList<PendingDownload>()
            for (i in 0 until a.length()) {
                val o = a.getJSONObject(i)
                val url = o.optString("url")
                if (url.isBlank()) continue
                val title = o.optString("title").ifBlank { url.substringAfterLast('/') }
                val kind = if (YtDlp.looksLikeMedia(url) || YtDlp.isHlsOrDash(url)) PendingDownload.Kind.Video else PendingDownload.Kind.File
                list.add(PendingDownload(url, title, o.optString("type"), kind = kind))
            }
            android.os.Handler(android.os.Looper.getMainLooper()).post { onList(list) }
        } catch (_: Exception) {}
    }
}

private const val DETECT_JS = """
(function(){
  try {
    var out = [];
    var seen = {};
    function add(u, title, type){
      if(!u || seen[u]) return;
      if(u.indexOf('blob:')===0 || u.indexOf('data:')===0) return;
      seen[u]=1;
      out.push({url:u, title:title||document.title||'', type:type||''});
    }
    document.querySelectorAll('video,audio,source').forEach(function(el){
      add(el.currentSrc||el.src, document.title, (el.tagName||'').toLowerCase());
      if(el.querySelectorAll) el.querySelectorAll('source').forEach(function(s){ add(s.src, document.title, s.type); });
    });
    if (window.performance && performance.getEntriesByType) {
      performance.getEntriesByType('resource').forEach(function(r){
        var n=(r.name||'').toLowerCase();
        if (/\.(mp4|webm|mkv|m3u8|mpd|m4a|mp3)(\?|$)/.test(n)) add(r.name, document.title, 'resource');
      });
    }
    document.querySelectorAll('a[href]').forEach(function(a){
      var h=(a.href||'').toLowerCase();
      if (/\.(mp4|webm|mkv|m3u8|mpd)(\?|$)/.test(h)) add(a.href, a.textContent||document.title, 'link');
    });
    if (window.BDDM && BDDM.found) BDDM.found(JSON.stringify(out.slice(0, 25)));
  } catch(e){}
})();
"""
