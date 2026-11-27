package com.shekh.bddm.browser

import android.annotation.SuppressLint
import android.content.Intent
import android.graphics.Bitmap
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.*
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlin.coroutines.resume
import org.json.JSONArray
import org.json.JSONObject
import androidx.compose.ui.graphics.asImageBitmap

class BrowserActivity : ComponentActivity() {
    lateinit var store: BrowserStore
    var webView: WebView? = null
    var customView: View? = null
    var customCallback: WebChromeClient.CustomViewCallback? = null
    var hideSwitcher: (() -> Unit)? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setFlags(
            android.view.WindowManager.LayoutParams.FLAG_HARDWARE_ACCELERATED,
            android.view.WindowManager.LayoutParams.FLAG_HARDWARE_ACCELERATED,
        )
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
                if (customView != null) {
                    customCallback?.onCustomViewHidden()
                    customView = null
                    customCallback = null
                    return
                }
                val closeSw = hideSwitcher
                if (closeSw != null) { closeSw(); return }
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
    var iframeUrls by remember { mutableStateOf(listOf<String>()) }
    var probing by remember { mutableStateOf(false) }
    var showDetected by remember { mutableStateOf(false) }
    var showTabs by remember { mutableStateOf(false) }
    var incognitoSpace by remember { mutableStateOf(false) }
    var showShieldMenu by remember { mutableStateOf(false) }
    var snack by remember { mutableStateOf<String?>(null) }
    var fullscreenTick by remember { mutableStateOf(0) }
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
    val snackHost = remember { SnackbarHostState() }
    LaunchedEffect(snack) {
        val msg = snack ?: return@LaunchedEffect
        snackHost.showSnackbar(msg)
        snack = null
    }
    androidx.compose.runtime.DisposableEffect(showTabs) {
        act.hideSwitcher = if (showTabs) {{ showTabs = false }} else null
        onDispose { act.hideSwitcher = null }
    }

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
        Box(Modifier.fillMaxSize()) {
        Scaffold(snackbarHost = { SnackbarHost(snackHost) }) { pad ->
            Column(Modifier.fillMaxSize().padding(pad).background(MaterialTheme.colorScheme.background)) {
                // Address bar + shield + tab count (item 101: no "N blocked" bar)
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
                        IconButton(onClick = { showShieldMenu = true }) {
                            Icon(Icons.Default.VerifiedUser, "Ad blocker", tint = if (adOn) SuccessDark else MutedDark)
                        }
                        DropdownMenu(showShieldMenu, { showShieldMenu = false }) {
                            val host = AdBlocker.hostOf(tab.url)
                            Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp).widthIn(min = 240.dp)) {
                                Text(if (blocked > 0) "$blocked blocked on this page" else "No requests blocked", fontWeight = FontWeight.Medium)
                                if (host != null) {
                                    Text(host, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                    Row(Modifier.fillMaxWidth().padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                                        Text("Block ads on this site", modifier = Modifier.weight(1f), fontSize = 14.sp)
                                        Switch(checked = adOn, onCheckedChange = {
                                            adOn = it
                                            prefs.setAdblockFor(host, it)
                                            act.webView?.reload()
                                        })
                                    }
                                }
                            }
                        }
                    }
                    IconButton(onClick = {
                        captureCurrent(act, tab.id)
                        incognitoSpace = tab.incognito
                        showTabs = true
                    }) {
                        Box(
                            Modifier.size(26.dp).clip(RoundedCornerShape(6.dp))
                                .border(1.5.dp, MaterialTheme.colorScheme.onSurface, RoundedCornerShape(6.dp)),
                            contentAlignment = Alignment.Center,
                        ) {
                            Text("${tabs.size}", fontSize = 11.sp, fontWeight = FontWeight.Bold)
                        }
                    }
                    IconButton(onClick = { showMenu = true }) { Icon(Icons.Default.MoreVert, "Menu") }
                    DropdownMenu(showMenu, { showMenu = false }) {
                        DropdownMenuItem(text = { Text("New tab") }, onClick = {
                            showMenu = false; store.addTab(); idx = store.currentIndex(); address = ""; tabTick++
                        })
                        DropdownMenuItem(text = { Text("New incognito tab") }, onClick = {
                            showMenu = false; store.addTab(incognito = true); idx = store.currentIndex(); address = ""; tabTick++
                        })
                        DropdownMenuItem(text = { Text("Tabs") }, onClick = {
                            showMenu = false; captureCurrent(act, tab.id); incognitoSpace = tab.incognito; showTabs = true
                        })
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
                Box(Modifier.weight(1f)) {
                    key(tab.id, tabTick) {
                        AndroidView(factory = { c ->
                            WebView(c).apply {
                                act.webView = this
                                layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                                settings.javaScriptEnabled = true
                                settings.domStorageEnabled = true
                                settings.databaseEnabled = true
                                settings.mediaPlaybackRequiresUserGesture = false
                                settings.allowFileAccess = false
                                settings.loadsImagesAutomatically = true
                                settings.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
                                settings.userAgentString = FilterEngine.chromeLikeUa(settings.userAgentString)
                                setLayerType(View.LAYER_TYPE_HARDWARE, null)
                                cookie(tab.incognito)
                                addJavascriptInterface(DetectBridge { list -> detected = list; iframeUrls = list.filter { it.subtitle == "iframe" }.map { it.url } }, "BDDM")
                                webViewClient = object : WebViewClient() {
                                    override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                                        val pageHost = AdBlocker.hostOf(tab.url) ?: request.url.host ?: ""
                                        return ad.intercept(request, pageHost)
                                    }
                                    override fun onPageStarted(view: WebView?, url: String?, favicon: Bitmap?) {
                                        ad.resetPageCount()
                                        blocked = 0
                                        detected = emptyList()
                                        iframeUrls = emptyList()
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
                                        view?.postDelayed({
                                            val bmp = view.let { TabThumbs.capture(it) }
                                            if (bmp != null) TabThumbs.saveThumb(act, tab.id, bmp)
                                        }, 400)
                                    }
                                    override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                                        val u = request?.url?.toString() ?: return false
                                        // about:blank, blob: and data: are used by JW Player and other embeds.
                                        if (u.startsWith("http") || u.startsWith("blob:") || u.startsWith("data:") ||
                                            u.startsWith("about:") || u.startsWith("javascript:")) return false
                                        return true
                                    }
                                }
                                webChromeClient = object : WebChromeClient() {
                                    override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: android.os.Message?): Boolean {
                                        if (prefs.blockPopups && !isUserGesture) return false
                                        if (prefs.blockPopups && isDialog) return false
                                        return false
                                    }
                                    override fun onReceivedTitle(view: WebView?, title: String?) {
                                        if (!title.isNullOrBlank()) tab.title = title
                                    }
                                    override fun onReceivedIcon(view: WebView?, icon: Bitmap?) {
                                        if (icon != null) TabThumbs.saveFavicon(act, tab.id, icon)
                                    }
                                    override fun onShowCustomView(view: View?, callback: CustomViewCallback?) {
                                        act.customView = view
                                        act.customCallback = callback
                                        fullscreenTick++
                                    }
                                    override fun onHideCustomView() {
                                        act.customView = null
                                        act.customCallback?.onCustomViewHidden()
                                        act.customCallback = null
                                        fullscreenTick++
                                    }
                                    override fun onPermissionRequest(request: PermissionRequest?) {
                                        val req = request ?: return
                                        val grant = req.resources.filter { it == PermissionRequest.RESOURCE_PROTECTED_MEDIA_ID }
                                        if (grant.isNotEmpty()) req.grant(grant.toTypedArray()) else req.deny()
                                    }
                                }
                                loadUrl(tab.url.ifBlank { "about:blank" })
                            }
                        }, modifier = Modifier.fillMaxSize(), update = { wv -> act.webView = wv })
                    }
                    FloatingActionButton(
                        onClick = {
                            if (probing) return@FloatingActionButton
                            if (detected.isNotEmpty()) {
                                showDetected = true
                            } else {
                                probing = true
                                scope.launch {
                                    findVideosOnPage(act.webView, tab.url, tab.title, iframeUrls)
                                    probing = false
                                }
                            }
                        },
                        modifier = Modifier.align(Alignment.BottomEnd).padding(16.dp, 16.dp),
                        containerColor = TextAccentDark,
                    ) {
                        Box {
                            if (probing) {
                                CircularProgressIndicator(Modifier.size(22.dp), strokeWidth = 2.dp, color = Color.White)
                            } else {
                                Icon(Icons.Default.Download, "Download")
                            }
                            if (detected.isNotEmpty() && !probing) {
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
            val thumbs = tabs.associate { it.id to TabThumbs.loadThumb(ctx, it.id) }
            val favs = tabs.associate { it.id to TabThumbs.loadFavicon(ctx, it.id) }
            TabSwitcher(
                tabs = tabs.toList(),
                currentId = tab.id,
                incognitoSpace = incognitoSpace,
                thumbs = thumbs,
                favicons = favs,
                onSelect = { tSel ->
                    val i = tabs.indexOfFirst { it.id == tSel.id }
                    if (i >= 0) {
                        idx = i; store.setCurrent(i); address = tSel.url; tabTick++
                    }
                    showTabs = false
                },
                onClose = { tSel ->
                    TabThumbs.delete(ctx, tSel.id)
                    store.closeTab(tSel.id)
                    idx = store.currentIndex()
                    address = store.tabs.getOrNull(idx)?.url ?: ""
                    tabTick++
                },
                onNewTab = { incog ->
                    store.addTab(incognito = incog)
                    idx = store.currentIndex()
                    address = ""
                    tabTick++
                    showTabs = false
                },
                onCloseAll = {
                    val gone = store.tabs.filter { it.incognito == incognitoSpace }
                    gone.forEach { TabThumbs.delete(ctx, it.id) }
                    store.closeAll(incognitoOnly = incognitoSpace)
                    idx = store.currentIndex()
                    address = store.tabs.getOrNull(idx)?.url ?: ""
                    tabTick++
                    showTabs = false
                },
                onCloseOthers = {
                    store.tabs.filter { it.id != tab.id }.forEach { TabThumbs.delete(ctx, it.id) }
                    store.closeOthers(tab.id)
                    idx = store.currentIndex()
                    tabTick++
                },
                onDismiss = { showTabs = false },
                onToggleIncognitoSpace = { incognitoSpace = !incognitoSpace },
                onMessage = { snack = it },
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
        val fs = act.customView
        if (fs != null && fullscreenTick >= 0) {
            AndroidView(factory = {
                (fs.parent as? ViewGroup)?.removeView(fs)
                fs
            }, modifier = Modifier.fillMaxSize(), update = {})
        }
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
    cm.setAcceptCookie(true)
    // Third-party cookies are required for embedded players (JW Player, YouTube embeds).
    // History is still not recorded for incognito tabs.
    cm.setAcceptThirdPartyCookies(this, true)
    if (incognito) {
        settings.cacheMode = WebSettings.LOAD_NO_CACHE
    }
    settings.domStorageEnabled = true
}

private fun captureCurrent(act: BrowserActivity, tabId: String) {
    val wv = act.webView ?: return
    val bmp = TabThumbs.capture(wv) ?: return
    TabThumbs.saveThumb(act, tabId, bmp)
}

private suspend fun findVideosOnPage(wv: WebView?, pageUrl: String, pageTitle: String, knownIframes: List<String>) {
    val urls = LinkedHashSet<String>()
    if (pageUrl.startsWith("http")) urls.add(pageUrl)
    knownIframes.filter { it.startsWith("http") }.forEach { urls.add(it) }
    val fromJs = kotlinx.coroutines.suspendCancellableCoroutine<List<String>> { cont ->
        val view = wv
        if (view == null) { cont.resume(emptyList()); return@suspendCancellableCoroutine }
        view.post {
            view.evaluateJavascript(IFRAME_JS) { raw ->
                val list = parseJsStringList(raw)
                if (cont.isActive) cont.resume(list)
            }
        }
    }
    fromJs.filter { it.startsWith("http") }.forEach { urls.add(it) }
    var any = false
    if (!YtDlp.ready) {
        urls.forEach { u ->
            PendingQueue.add(PendingDownload(u, pageTitle.ifBlank { u }, "Page", kind = PendingDownload.Kind.Video))
            any = true
        }
        return
    }
    for (u in urls) {
        try {
            val info = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { YtDlp.probe(u) }
            if (info.formats.isNotEmpty() || info.title.isNotBlank()) {
                PendingQueue.add(
                    PendingDownload(
                        url = u,
                        title = info.title.ifBlank { pageTitle }.ifBlank { u },
                        subtitle = info.site + if (info.duration.isNotBlank()) " · ${info.duration}" else "",
                        kind = PendingDownload.Kind.Video,
                        formats = info.formats,
                    )
                )
                any = true
            }
        } catch (_: Throwable) {}
    }
    if (!any && pageUrl.startsWith("http")) {
        PendingQueue.add(PendingDownload(pageUrl, pageTitle.ifBlank { pageUrl }, "Page", kind = PendingDownload.Kind.Video))
    }
}

private fun parseJsStringList(raw: String?): List<String> {
    if (raw.isNullOrBlank() || raw == "null") return emptyList()
    var s = raw.trim()
    if (s.length >= 2 && s.first() == '"' && s.last() == '"') {
        s = org.json.JSONTokener(s).nextValue()?.toString() ?: s
        // If the WebView already returned a JSON array literal, nextValue yields the array.
        // Fall through to JSONArray below on the original or decoded form.
    }
    val json = if (s.startsWith("[")) s else raw.trim().trim('"')
    return try {
        val a = org.json.JSONArray(json)
        (0 until a.length()).mapNotNull { i -> a.optString(i).takeIf { u -> u.isNotBlank() } }
    } catch (_: Exception) {
        try {
            val a = org.json.JSONArray(raw)
            (0 until a.length()).mapNotNull { i -> a.optString(i).takeIf { u -> u.isNotBlank() } }
        } catch (_: Exception) { emptyList() }
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
    document.querySelectorAll('iframe[src]').forEach(function(f){
      add(f.src, document.title, 'iframe');
    });
    if (window.BDDM && BDDM.found) BDDM.found(JSON.stringify(out.slice(0, 25)));
  } catch(e){}
})();
"""

private const val IFRAME_JS = """
(function(){
  try {
    var a=[];
    document.querySelectorAll('iframe[src]').forEach(function(f){ if(f.src) a.push(f.src); });
    return JSON.stringify(a.slice(0, 20));
  } catch(e){ return "[]"; }
})();
"""
