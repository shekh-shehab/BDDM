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
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import kotlinx.coroutines.delay
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
    @Volatile var trustedLoad = false

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
    var field by remember { mutableStateOf(TextFieldValue(tab.url)) }
    var addrFocused by remember { mutableStateOf(false) }
    var userTyped by remember { mutableStateOf(false) }
    var suggestions by remember { mutableStateOf(listOf<Suggestion>()) }
    var histSnap by remember { mutableStateOf(listOf<HistoryEntry>()) }
    var markSnap by remember { mutableStateOf(listOf<Bookmark>()) }
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val detectedAll by MediaDetector.all.collectAsState()
    val detected = detectedAll[tab.id].orEmpty()
    var probing by remember { mutableStateOf(false) }
    var pendingOpen by remember { mutableStateOf<OpenRequest?>(null) }
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

    // The field follows the page address unless the user is editing it.
    LaunchedEffect(address, addrFocused) {
        if (!addrFocused && field.text != address) field = TextFieldValue(address)
    }
    // Item 106: suggestions while typing (local at once, Google after a short pause).
    LaunchedEffect(field.text, addrFocused, userTyped) {
        val q = field.text.trim()
        if (!addrFocused || !userTyped || q.isEmpty()) { suggestions = emptyList(); return@LaunchedEffect }
        val loc = Suggestions.local(q, histSnap, markSnap)
        suggestions = Suggestions.merge(q, loc, emptyList())
        if (prefs.searchSuggestions) {
            delay(150)
            val remote = withContext(Dispatchers.IO) { Suggestions.fetchGoogle(q) }
            suggestions = Suggestions.merge(q, loc, remote)
        }
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
        field = TextFieldValue(url)
        userTyped = false
        suggestions = emptyList()
        focusManager.clearFocus()
        keyboard?.hide()
        tab.url = url
        store.touch(tab)
        act.trustedLoad = true
        act.webView?.loadUrl(url)
    }

    fun openInNewTab(url: String) {
        act.trustedLoad = true
        store.addTab(url = url)
        idx = store.currentIndex()
        address = url
        tabTick++
    }

    fun considerOpen(url: String) {
        val toHost = OpenPolicy.normalize(AdBlocker.hostOf(url)) ?: return
        val fromHost = OpenPolicy.normalize(AdBlocker.hostOf(tab.url)) ?: ""
        if (fromHost.isBlank() || tab.url.isBlank() || tab.url.startsWith("about:")) {
            openInNewTab(url)
            return
        }
        if (OpenPolicy.sameSite(fromHost, toHost)) {
            openInNewTab(url)
            return
        }
        when (prefs.openPolicy(fromHost)) {
            OpenPolicy.ALLOW -> openInNewTab(url)
            OpenPolicy.BLOCK -> { }
            else -> pendingOpen = OpenRequest(fromHost, url, toHost)
        }
    }

    BDDMTheme(prefs.theme) {
        Box(Modifier.fillMaxSize()) {
        Scaffold(snackbarHost = { SnackbarHost(snackHost) }) { pad ->
            Column(Modifier.fillMaxSize().padding(pad).background(MaterialTheme.colorScheme.background)) {
                // Address bar + shield + tab count (item 101: no "N blocked" bar)
                Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = { act.webView?.goBack() }) { Icon(Icons.Default.ArrowBack, "Back") }
                    OutlinedTextField(
                        value = field,
                        onValueChange = {
                            if (it.text != field.text) userTyped = true
                            field = it
                            address = it.text
                        },
                        modifier = Modifier.weight(1f).height(52.dp).onFocusChanged { st ->
                            val was = addrFocused
                            addrFocused = st.isFocused
                            if (!st.isFocused && was) {
                                // Left the bar without pressing Go: show the real address again.
                                userTyped = false
                                address = tab.url
                                field = TextFieldValue(tab.url)
                            }
                            if (st.isFocused && !was) {
                                userTyped = false
                                // Item 105: tapping the bar selects the whole URL (Chrome-style).
                                // The tap itself places the cursor after focus arrives, so select again shortly after.
                                scope.launch {
                                    withContext(Dispatchers.IO) {
                                        histSnap = store.history()
                                        markSnap = store.bookmarks()
                                    }
                                }
                                scope.launch {
                                    for (d in longArrayOf(30, 120, 300)) {
                                        delay(d)
                                        if (addrFocused && !userTyped) field = field.copy(selection = TextRange(0, field.text.length))
                                    }
                                }
                            }
                        },
                        singleLine = true,
                        textStyle = LocalTextStyle.current.copy(fontSize = 13.sp),
                        shape = RoundedCornerShape(24.dp),
                        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go),
                        keyboardActions = KeyboardActions(onGo = { load(field.text) }),
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
                                settings.setSupportMultipleWindows(true)
                                settings.javaScriptCanOpenWindowsAutomatically = true
                                settings.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
                                settings.userAgentString = FilterEngine.chromeLikeUa(settings.userAgentString)
                                setLayerType(View.LAYER_TYPE_HARDWARE, null)
                                cookie(tab.incognito)
                                val tabId = tab.id
                                addJavascriptInterface(
                                    DetectBridge(tabId, { tab.url }, { tab.title }, { u -> ad.isAdUrl(u, AdBlocker.hostOf(tab.url) ?: "") }),
                                    MediaHooks.BRIDGE_NAME,
                                )
                                val docStart = MediaHooks.installDocumentStart(this)
                                webViewClient = object : WebViewClient() {
                                    override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                                        val pageHost = AdBlocker.hostOf(tab.url) ?: request.url.host ?: ""
                                        val blockedResp = ad.intercept(request, pageHost)
                                        if (blockedResp != null) return blockedResp
                                        try { recordRequest(tabId, request, pageHost, ad, tab.url) } catch (_: Throwable) {}
                                        return null
                                    }
                                    override fun doUpdateVisitedHistory(view: WebView?, url: String?, isReload: Boolean) {
                                        // Single-page apps change the address without loading a new document.
                                        if (url == null || !url.startsWith("http") || isReload) return
                                        if (url.substringBefore('#') == tab.url.substringBefore('#')) return
                                        tab.url = url
                                        address = url
                                        MediaDetector.clear(tabId)
                                        view?.evaluateJavascript("window.__bddmReset && window.__bddmReset();", null)
                                        if (YtDlp.isSitePage(url) && MediaClassifier.isWatchPage(url)) {
                                            MediaDetector.add(tabId, DetectedMedia(url, MediaKind.PAGE, MediaClassifier.titleFor(url, view?.title ?: ""), url, "spa"))
                                        }
                                        view?.postDelayed({ MediaHooks.rescan(view) }, 1200)
                                    }
                                    override fun onPageStarted(view: WebView?, url: String?, favicon: Bitmap?) {
                                        ad.resetPageCount()
                                        blocked = 0
                                        if (url != null && url.startsWith("http")) {
                                            address = url
                                            tab.url = url
                                        }
                                        if (!docStart) MediaHooks.inject(view)
                                    }
                                    override fun onPageFinished(view: WebView?, url: String?) {
                                        act.trustedLoad = false
                                        blocked = ad.blockedOnPage.get()
                                        tab.blocked = blocked
                                        if (url != null && url.startsWith("http")) {
                                            tab.title = view?.title ?: url
                                            tab.url = url
                                            address = url
                                            store.touch(tab)
                                            if (!tab.incognito) store.addHistory(tab.title, url)
                                        }
                                        MediaHooks.inject(view)
                                        MediaHooks.rescan(view)
                                        MediaDetector.updateTitle(tabId, tab.title)
                                        view?.postDelayed({
                                            val bmp = view.let { TabThumbs.capture(it) }
                                            if (bmp != null) TabThumbs.saveThumb(act, tab.id, bmp)
                                        }, 400)
                                    }
                                    override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                                        val u = request?.url?.toString() ?: return false
                                        if (u.startsWith("blob:") || u.startsWith("data:") ||
                                            u.startsWith("about:") || u.startsWith("javascript:")) return false
                                        if (!u.startsWith("http")) return true
                                        if (request?.isForMainFrame != true) return false
                                        if (act.trustedLoad) return false
                                        val toHost = OpenPolicy.normalize(AdBlocker.hostOf(u))
                                        val fromHost = OpenPolicy.normalize(AdBlocker.hostOf(tab.url))
                                        if (toHost == null || fromHost == null || OpenPolicy.sameSite(fromHost, toHost)) return false
                                        view?.post { considerOpen(u) }
                                        return true
                                    }
                                }
                                webChromeClient = object : WebChromeClient() {
                                    override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: android.os.Message?): Boolean {
                                        if (prefs.blockPopups && !isUserGesture) return false
                                        if (resultMsg == null) return false
                                        val tmp = WebView(act).apply {
                                            settings.javaScriptEnabled = true
                                            webViewClient = object : WebViewClient() {
                                                private fun capture(url: String?) {
                                                    val u = url ?: return
                                                    if (!u.startsWith("http")) return
                                                    post {
                                                        considerOpen(u)
                                                        try { destroy() } catch (_: Exception) {}
                                                    }
                                                }
                                                override fun shouldOverrideUrlLoading(v: WebView?, req: WebResourceRequest?): Boolean {
                                                    capture(req?.url?.toString()); return true
                                                }
                                                override fun onPageStarted(v: WebView?, url: String?, favicon: Bitmap?) {
                                                    if (url != null && url.startsWith("http")) capture(url)
                                                }
                                            }
                                        }
                                        val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
                                        transport.webView = tmp
                                        resultMsg.sendToTarget()
                                        return true
                                    }
                                    override fun onReceivedTitle(view: WebView?, title: String?) {
                                        if (!title.isNullOrBlank()) { tab.title = title; MediaDetector.updateTitle(tabId, title) }
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
                                act.trustedLoad = true
                                loadUrl(tab.url.ifBlank { "about:blank" })
                            }
                        }, modifier = Modifier.fillMaxSize(), update = { wv -> act.webView = wv })
                    }
                    if (suggestions.isNotEmpty() && addrFocused) {
                        SuggestionList(
                            suggestions,
                            modifier = Modifier.align(Alignment.TopStart).fillMaxWidth().padding(horizontal = 8.dp),
                            onPick = { s -> load(s.url ?: s.text) },
                        )
                    }
                    FloatingActionButton(
                        onClick = {
                            if (probing) return@FloatingActionButton
                            MediaHooks.rescan(act.webView)
                            if (detected.isNotEmpty()) {
                                showDetected = true
                            } else {
                                probing = true
                                scope.launch {
                                    val found = findVideosOnPage(tab.url, tab.title)
                                    probing = false
                                    if (!found) snack = if (!tab.url.startsWith("http")) "Open a page with a video first."
                                        else "No video found yet. Press play on the video, then tap again."
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
                val p = item.toPending()
                PendingQueue.add(p)
                Downloads.enrichAsync(scope, p.url, p.referer)
            }, onDownloadAll = {
                showDetected = false
                detected.forEach {
                    val p = it.toPending()
                    PendingQueue.add(p)
                    Downloads.enrichAsync(scope, p.url, p.referer)
                }
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
        val openReq = pendingOpen
        if (openReq != null) {
            OpenSiteSheet(
                req = openReq,
                onOpen = { always ->
                    if (always) prefs.setOpenPolicy(openReq.fromHost, OpenPolicy.ALLOW)
                    pendingOpen = null
                    openInNewTab(openReq.url)
                },
                onBlock = { always ->
                    if (always) prefs.setOpenPolicy(openReq.fromHost, OpenPolicy.BLOCK)
                    pendingOpen = null
                },
                onDismiss = { pendingOpen = null },
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
private fun SuggestionList(items: List<Suggestion>, modifier: Modifier, onPick: (Suggestion) -> Unit) {
    Surface(
        modifier = modifier,
        shape = RoundedCornerShape(16.dp),
        tonalElevation = 6.dp,
        shadowElevation = 8.dp,
        color = MaterialTheme.colorScheme.surface,
    ) {
        LazyColumn(Modifier.heightIn(max = 360.dp)) {
            items(items.size) { i ->
                val s = items[i]
                Row(
                    Modifier.fillMaxWidth().clickable { onPick(s) }.padding(horizontal = 14.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(
                        when (s.kind) {
                            Suggestion.Kind.BOOKMARK -> Icons.Default.Star
                            Suggestion.Kind.HISTORY -> Icons.Default.History
                            Suggestion.Kind.SEARCH -> Icons.Default.Search
                        },
                        null, modifier = Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.width(12.dp))
                    Column(Modifier.weight(1f)) {
                        Text(s.text, maxLines = 1, overflow = TextOverflow.Ellipsis, fontSize = 14.sp)
                        if (s.subtitle.isNotBlank()) {
                            Text(s.subtitle, maxLines = 1, overflow = TextOverflow.Ellipsis, fontSize = 11.sp,
                                color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun DetectedSheet(items: List<DetectedMedia>, onDismiss: () -> Unit, onDownload: (DetectedMedia) -> Unit, onDownloadAll: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Found on this page") },
        text = {
            LazyColumn {
                items(items.size) { i ->
                    val m = items[i]
                    ListItem(
                        headlineContent = { Text(m.title.ifBlank { m.url }, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                        supportingContent = { Text(m.kind.label + if (m.host.isNotBlank()) " · " + m.host else "", maxLines = 1, fontSize = 12.sp) },
                        trailingContent = { TextButton(onClick = { onDownload(m) }) { Text("Download") } },
                    )
                }
            }
        },
        confirmButton = { if (items.size > 1) TextButton(onClick = onDownloadAll) { Text("Add all") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Close") } },
    )
}

private fun DetectedMedia.toPending() = PendingDownload(
    url = url,
    title = title.ifBlank { url },
    subtitle = kind.label + if (host.isNotBlank()) " · $host" else "",
    kind = PendingDownload.Kind.Video,
    referer = pageUrl,
)

/** Network-level capture: sees every request from every frame, including players that never touch the DOM. */
private fun recordRequest(tabId: String, request: WebResourceRequest, pageHost: String, ad: AdBlocker, pageUrl: String) {
    val u = request.url.toString()
    if (!u.startsWith("http")) return
    if (request.isForMainFrame) {
        MediaDetector.clear(tabId)
        if (YtDlp.isSitePage(u) && MediaClassifier.isWatchPage(u)) {
            MediaDetector.add(tabId, DetectedMedia(u, MediaKind.PAGE, MediaClassifier.titleFor(u, ""), u, "page"))
        }
        MediaDetector.offer(tabId, u, u, "", "network")
        return
    }
    if (!request.method.equals("GET", true)) return
    if (MediaClassifier.classify(u) == null) return
    if (ad.isAdUrl(u, pageHost)) return
    val page = request.requestHeaders?.get("Referer")?.takeIf { it.startsWith("http") } ?: pageUrl
    MediaDetector.offer(tabId, u, page, "", "network")
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

/** Last resort when nothing was captured: ask yt-dlp whether the page itself holds a video. */
private suspend fun findVideosOnPage(pageUrl: String, pageTitle: String): Boolean {
    if (!pageUrl.startsWith("http")) return false
    return try {
        val info = withContext(Dispatchers.IO) { YtDlp.probe(pageUrl, referer = pageUrl) }
        if (info.formats.isEmpty() && info.title.isBlank()) return false
        PendingQueue.add(
            PendingDownload(
                url = pageUrl,
                title = info.title.ifBlank { pageTitle }.ifBlank { pageUrl },
                subtitle = info.site + if (info.duration.isNotBlank()) " · ${info.duration}" else "",
                kind = PendingDownload.Kind.Video,
                formats = info.formats,
                referer = pageUrl,
            )
        )
        true
    } catch (_: Throwable) { false }
}
