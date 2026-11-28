package com.shekh.bddm.browser

import android.annotation.SuppressLint
import android.content.Intent
import android.graphics.Bitmap
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
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
    val tabViews = TabWebViews()
    val ui = BrowserUi()
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

    override fun onDestroy() {
        tabViews.clear()
        super.onDestroy()
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
        tab.userNavigated = true
        store.touch(tab)
        act.trustedLoad = true
        act.webView?.loadUrl(url)
    }

    fun showTab(id: String) {
        val i = store.tabs.indexOfFirst { it.id == id }
        if (i < 0) return
        idx = i
        store.setCurrent(i)
        address = store.tabs[i].url
    }

    fun destroyTabView(id: String) {
        act.tabViews.remove(id)
        TabThumbs.delete(ctx, id)
        MediaDetector.clear(id)
    }

    /** Close a download-only popup and return to its opener without reloading that page. */
    fun closeDownloadPopup(id: String) {
        val t = store.tabs.find { it.id == id } ?: return
        if (!DownloadTabPolicy.shouldCloseAfterDownload(
                t.openerId, t.openedAsPopup, t.hasRealContent, t.userNavigated, t.url, t.mainFrameBecameDownload,
            )) return
        val opener = t.openerId
        destroyTabView(id)
        store.closeTab(id, selectId = opener)
        val i = store.currentIndex()
        idx = i
        address = store.tabs.getOrNull(i)?.url ?: ""
    }

    fun openInNewTab(url: String, asPopup: Boolean = false, fromTabId: String = tab.id) {
        act.trustedLoad = true
        val from = store.tabs.find { it.id == fromTabId }
        store.addTab(
            incognito = from?.incognito == true,
            url = url,
            openerId = if (asPopup) fromTabId else null,
            openedAsPopup = asPopup,
        )
        idx = store.currentIndex()
        address = url
    }

    fun considerOpen(url: String, fromTabId: String = tab.id) {
        val from = store.tabs.find { it.id == fromTabId } ?: tab
        val toHost = OpenPolicy.normalize(AdBlocker.hostOf(url)) ?: return
        val fromHost = OpenPolicy.normalize(AdBlocker.hostOf(from.url)) ?: ""
        if (fromHost.isBlank() || from.url.isBlank() || from.url.startsWith("about:")) {
            openInNewTab(url, asPopup = true, fromTabId = fromTabId)
            return
        }
        if (OpenPolicy.sameSite(fromHost, toHost)) {
            openInNewTab(url, asPopup = true, fromTabId = fromTabId)
            return
        }
        when (prefs.openPolicy(fromHost)) {
            OpenPolicy.ALLOW -> openInNewTab(url, asPopup = true, fromTabId = fromTabId)
            OpenPolicy.BLOCK -> { }
            else -> pendingOpen = OpenRequest(fromHost, url, toHost)
        }
    }

    act.ui.setAddress = { address = it }
    act.ui.setBlocked = { blocked = it }
    act.ui.snack = { snack = it }
    act.ui.considerOpen = { fromId, u -> considerOpen(u, fromId) }
    act.ui.onDownload = { id -> closeDownloadPopup(id) }
    act.ui.onPopupCreated = { id -> showTab(id) }
    act.ui.onFullscreen = { fullscreenTick++ }

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
                        // Item 111: a clear way back to the download list.
                        DropdownMenuItem(
                            text = { Text("BDDM downloads") },
                            leadingIcon = { Icon(Icons.Default.Download, null) },
                            onClick = { showMenu = false; openDownloads(act) },
                        )
                        HorizontalDivider()
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
                        DropdownMenuItem(text = { Text("Download all videos on this page") }, onClick = {
                            showMenu = false
                            if (!tab.url.startsWith("http")) { snack = "Open a page first."; return@DropdownMenuItem }
                            snack = "Looking for videos on this page…"
                            scope.launch {
                                val n = findAllOnPage(tab.url, tab.title)
                                snack = if (n > 0) "Found $n video(s)." else "No video list found on this page."
                            }
                        })
                        DropdownMenuItem(text = { Text("Save as PDF") }, onClick = {
                            showMenu = false
                            saveAsPdf(act, tab.title.ifBlank { "page" })
                        })
                        DropdownMenuItem(text = { Text("Share") }, onClick = {
                            showMenu = false
                            val send = Intent(Intent.ACTION_SEND).apply { type = "text/plain"; putExtra(Intent.EXTRA_TEXT, tab.url) }
                            ctx.startActivity(Intent.createChooser(send, "Share"))
                        })
                    }
                }
                Box(Modifier.weight(1f)) {
                    AndroidView(factory = { c ->
                        FrameLayout(c).apply {
                            layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                        }
                    }, modifier = Modifier.fillMaxSize(), update = { container ->
                        val wv = act.tabViews.obtain(act, container.context, tab.id, loadUrl = true, url = tab.url)
                        if (wv.parent !== container) {
                            (wv.parent as? ViewGroup)?.removeView(wv)
                            container.removeAllViews()
                            container.addView(wv, FrameLayout.LayoutParams(
                                ViewGroup.LayoutParams.MATCH_PARENT,
                                ViewGroup.LayoutParams.MATCH_PARENT,
                            ))
                        }
                        act.webView = wv
                    })
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
            LaunchedEffect(detected) { MediaEnricher.enrichAll(scope, tab.id, detected) }
            DetectedMediaSheet(
                items = detected,
                pageThumb = MediaDetector.pageThumb(tab.id),
                onDismiss = { showDetected = false },
                onDownload = { picks, later ->
                    showDetected = false
                    picks.forEach { Downloads.startPending(ctx, it.toPending(), later) }
                    snack = when {
                        picks.isEmpty() -> null
                        later -> "Saved ${picks.size} for later."
                        else -> "Started ${picks.size} download${if (picks.size == 1) "" else "s"}."
                    }
                },
                onPreview = { pick -> preview(act, pick) { snack = it } },
                onCopy = { u ->
                    val cm = ctx.getSystemService(android.content.ClipboardManager::class.java)
                    cm?.setPrimaryClip(android.content.ClipData.newPlainText("link", u))
                    snack = "Link copied."
                },
                onRemove = { urls -> MediaDetector.remove(tab.id, urls) },
                onOpenDownloads = { showDetected = false; openDownloads(act) },
            )
        }
        if (sheetOpen && pending.isNotEmpty()) {
            ConfirmQueueSheet(
                items = pending,
                onChange = { PendingQueue.update(it) },
                onDismiss = {
                    val ids = pending.map { it.sourceTabId }.filter { it.isNotBlank() }.distinct()
                    PendingQueue.close()
                    ids.forEach { closeDownloadPopup(it) }
                },
                onConfirm = { later ->
                    val selected = pending.filter { it.selected }
                    val ids = pending.map { it.sourceTabId }.filter { it.isNotBlank() }.distinct()
                    PendingQueue.close()
                    selected.forEach { item ->
                        Downloads.startPending(ctx, item, later)
                    }
                    ids.forEach { closeDownloadPopup(it) }
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
                    openInNewTab(openReq.url, asPopup = true)
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
                    destroyTabView(tSel.id)
                    store.closeTab(tSel.id)
                    idx = store.currentIndex()
                    address = store.tabs.getOrNull(idx)?.url ?: ""
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
                    gone.forEach { destroyTabView(it.id) }
                    store.closeAll(incognitoOnly = incognitoSpace)
                    idx = store.currentIndex()
                    address = store.tabs.getOrNull(idx)?.url ?: ""
                    tabTick++
                    showTabs = false
                },
                onCloseOthers = {
                    store.tabs.filter { it.id != tab.id }.forEach { destroyTabView(it.id) }
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

/** Turns a pick from the sheet into a download: files go to the multi-part engine, streams to yt-dlp. */
private fun MediaPick.toPending(): PendingDownload {
    val m = option.item
    return when {
        audioOnly -> PendingDownload(url = m.url, title = group.title, kind = PendingDownload.Kind.Video, qualityId = "audio:best", referer = m.pageUrl)
        m.kind == MediaKind.DIRECT || m.kind == MediaKind.AUDIO -> PendingDownload(
            url = m.url, title = group.title, kind = PendingDownload.Kind.File,
            fileName = MediaGrouping.fileNameFor(group.title, m.url, option.height),
            referer = m.pageUrl, size = option.sizeBytes,
        )
        else -> PendingDownload(
            url = m.url, title = group.title, kind = PendingDownload.Kind.Video,
            qualityId = if (option.height > 0) "h:${option.height}" else "best", referer = m.pageUrl,
        )
    }
}

/** Opens the stream in a video player app (VLC, MX Player, ...). */
private fun preview(act: BrowserActivity, pick: MediaPick, onMessage: (String) -> Unit) {
    val m = pick.option.item
    val url = if (m.kind == MediaKind.HLS) m.variants.firstOrNull { it.height == pick.option.height }?.url ?: m.url else m.url
    val type = when (m.kind) {
        MediaKind.HLS -> "application/x-mpegURL"
        MediaKind.DASH -> "application/dash+xml"
        MediaKind.AUDIO -> "audio/*"
        else -> "video/*"
    }
    try {
        act.startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(android.net.Uri.parse(url), type).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    } catch (_: Exception) {
        onMessage("No video player app found to preview this.")
    }
}

/** Network-level capture: sees every request from every frame, including players that never touch the DOM. */
internal fun recordRequest(tabId: String, request: WebResourceRequest, pageHost: String, ad: AdBlocker, pageUrl: String) {
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

private fun openDownloads(act: BrowserActivity) {
    act.startActivity(Intent(act, com.shekh.bddm.MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT or Intent.FLAG_ACTIVITY_SINGLE_TOP))
}

/** Item 53: Android's print dialog offers "Save as PDF" and lets the user pick the folder. */
private fun saveAsPdf(act: BrowserActivity, title: String) {
    val wv = act.webView ?: return
    val pm = act.getSystemService(android.print.PrintManager::class.java) ?: return
    val name = YtDlp.safeFileName(title).ifBlank { "page" }
    pm.print(name, wv.createPrintDocumentAdapter(name), android.print.PrintAttributes.Builder().build())
}

/** Profile / playlist / channel pages (PC items 90/92): list every video yt-dlp can see. */
private suspend fun findAllOnPage(pageUrl: String, pageTitle: String): Int {
    val list = try { withContext(Dispatchers.IO) { YtDlp.listEntries(pageUrl, referer = pageUrl) } } catch (_: Throwable) { emptyList() }
    list.forEach { (u, title) ->
        PendingQueue.add(PendingDownload(url = u, title = title.ifBlank { u }, subtitle = pageTitle, kind = PendingDownload.Kind.Video, referer = pageUrl))
    }
    return list.size
}

internal fun WebView.cookie(incognito: Boolean) {
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
