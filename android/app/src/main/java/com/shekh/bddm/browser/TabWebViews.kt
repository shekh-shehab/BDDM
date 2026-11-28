package com.shekh.bddm.browser

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.PermissionRequest
import android.webkit.URLUtil
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import com.shekh.bddm.BDDMApp
import com.shekh.bddm.data.AdBlocker
import com.shekh.bddm.data.DetectedMedia
import com.shekh.bddm.data.DownloadTabPolicy
import com.shekh.bddm.data.FilterEngine
import com.shekh.bddm.data.MediaClassifier
import com.shekh.bddm.data.MediaDetector
import com.shekh.bddm.data.MediaKind
import com.shekh.bddm.data.OpenPolicy
import com.shekh.bddm.data.PendingDownload
import com.shekh.bddm.data.PendingQueue
import com.shekh.bddm.data.TabState
import com.shekh.bddm.data.YtDlp
import com.shekh.bddm.data.formatBytes
import com.shekh.bddm.data.TabThumbs

/**
 * Keeps one WebView per tab so switching back (for example after a download
 * popup closes) does not reload the page, wipe a form, or lose scroll position.
 */
class TabWebViews {
    private val views = LinkedHashMap<String, WebView>()

    fun get(id: String): WebView? = views[id]

    fun obtain(act: BrowserActivity, context: android.content.Context, tabId: String, loadUrl: Boolean, url: String = ""): WebView {
        views[tabId]?.let { return it }
        val wv = createBrowserWebView(act, context, tabId)
        views[tabId] = wv
        if (loadUrl) {
            act.trustedLoad = true
            wv.loadUrl(url.ifBlank { "about:blank" })
        }
        return wv
    }

    fun remove(id: String) {
        val wv = views.remove(id) ?: return
        (wv.parent as? ViewGroup)?.removeView(wv)
        try {
            wv.stopLoading()
            wv.webChromeClient = null
            wv.destroy()
        } catch (_: Exception) {}
    }

    fun clear() {
        views.keys.toList().forEach { remove(it) }
    }
}

/** Live UI hooks. BrowserRoot replaces these on every composition so WebViews stay current. */
class BrowserUi {
    var setAddress: (String) -> Unit = {}
    var setBlocked: (Int) -> Unit = {}
    var snack: (String) -> Unit = {}
    var considerOpen: (fromTabId: String, url: String) -> Unit = { _, _ -> }
    var onDownload: (tabId: String) -> Unit = {}
    var onPopupCreated: (childTabId: String) -> Unit = {}
    var onFullscreen: () -> Unit = {}
}

@SuppressLint("SetJavaScriptEnabled")
private fun createBrowserWebView(act: BrowserActivity, context: android.content.Context, tabId: String): WebView {
    val store = act.store
    val prefs = BDDMApp.instance.prefs
    val ad = BDDMApp.instance.adBlocker
    fun tab(): TabState? = store.tabs.find { it.id == tabId }
    fun current(): Boolean = store.tabs.getOrNull(store.currentIndex())?.id == tabId
    return WebView(context).apply {
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
        cookie(tab()?.incognito == true)
        addJavascriptInterface(
            DetectBridge(tabId, { tab()?.url ?: "" }, { tab()?.title ?: "" }, { u -> ad.isAdUrl(u, AdBlocker.hostOf(tab()?.url ?: "") ?: "") }),
            MediaHooks.BRIDGE_NAME,
        )
        val docStart = MediaHooks.installDocumentStart(this)
        setDownloadListener { dUrl, dUa, disposition, mime, length ->
            if (dUrl.startsWith("blob:") || dUrl.startsWith("data:")) {
                act.ui.snack("This file is created inside the page, so BDDM can't download it.")
                return@setDownloadListener
            }
            val t = tab()
            if (t != null) {
                t.mainFrameBecameDownload = true
                // The main document itself was the file, not a page the user was reading.
                val page = url ?: t.url
                if (t.openedAsPopup && !t.userNavigated &&
                    (DownloadTabPolicy.isBlankUrl(page) || page == dUrl || DownloadTabPolicy.isLikelyDownloadUrl(page))
                ) {
                    t.hasRealContent = false
                }
            }
            val name = URLUtil.guessFileName(dUrl, disposition, mime)
            val headers = HashMap<String, String>()
            if (!dUa.isNullOrBlank()) headers["User-Agent"] = FilterEngine.chromeLikeUa(dUa)
            try { CookieManager.getInstance().getCookie(dUrl)?.let { if (it.isNotBlank()) headers["Cookie"] = it } } catch (_: Throwable) {}
            val host = AdBlocker.hostOf(dUrl) ?: ""
            val referer = t?.url?.takeIf { it.startsWith("http") && !DownloadTabPolicy.isLikelyDownloadUrl(it) }
                ?: t?.openerId?.let { oid -> store.tabs.find { it.id == oid }?.url }?.takeIf { it.startsWith("http") }
                ?: ""
            PendingQueue.add(
                PendingDownload(
                    url = dUrl,
                    title = name,
                    subtitle = listOfNotNull(host.ifBlank { null }, if (length > 0) formatBytes(length) else null, mime?.takeIf { it.isNotBlank() }).joinToString(" · "),
                    kind = PendingDownload.Kind.File,
                    referer = referer,
                    fileName = name,
                    size = length,
                    headers = headers,
                    sourceTabId = tabId,
                )
            )
            act.ui.onDownload(tabId)
        }
        webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                val pageHost = AdBlocker.hostOf(tab()?.url ?: "") ?: request.url.host ?: ""
                val blockedResp = ad.intercept(request, pageHost)
                if (blockedResp != null) return blockedResp
                try { recordRequest(tabId, request, pageHost, ad, tab()?.url ?: "") } catch (_: Throwable) {}
                return null
            }
            override fun doUpdateVisitedHistory(view: WebView?, url: String?, isReload: Boolean) {
                val t = tab() ?: return
                if (url == null || !url.startsWith("http") || isReload) return
                if (url.substringBefore('#') == t.url.substringBefore('#')) return
                t.url = url
                if (current()) act.ui.setAddress(url)
                if (t.hasRealContent) t.userNavigated = true
                MediaDetector.clear(tabId)
                view?.evaluateJavascript("window.__bddmReset && window.__bddmReset();", null)
                if (YtDlp.isSitePage(url) && MediaClassifier.isWatchPage(url)) {
                    MediaDetector.add(tabId, DetectedMedia(url, MediaKind.PAGE, MediaClassifier.titleFor(url, view?.title ?: ""), url, "spa"))
                }
                view?.postDelayed({ MediaHooks.rescan(view) }, 1200)
            }
            override fun onPageStarted(view: WebView?, url: String?, favicon: Bitmap?) {
                ad.resetPageCount()
                if (current()) act.ui.setBlocked(0)
                val t = tab() ?: return
                if (url != null && url.startsWith("http")) {
                    t.url = url
                    if (current()) act.ui.setAddress(url)
                }
                if (!docStart) MediaHooks.inject(view)
            }
            override fun onPageFinished(view: WebView?, url: String?) {
                act.trustedLoad = false
                val t = tab() ?: return
                t.blocked = ad.blockedOnPage.get()
                if (current()) act.ui.setBlocked(t.blocked)
                if (url != null && url.startsWith("http")) {
                    t.title = view?.title ?: url
                    t.url = url
                    if (current()) act.ui.setAddress(url)
                    store.touch(t)
                    if (!t.incognito) store.addHistory(t.title, url)
                    if (!t.mainFrameBecameDownload &&
                        !DownloadTabPolicy.isBlankUrl(url) &&
                        !DownloadTabPolicy.isLikelyDownloadUrl(url)
                    ) {
                        t.hasRealContent = true
                    }
                }
                MediaHooks.inject(view)
                MediaHooks.rescan(view)
                MediaDetector.updateTitle(tabId, t.title)
                view?.postDelayed({
                    val bmp = view.let { TabThumbs.capture(it) }
                    if (bmp != null) TabThumbs.saveThumb(act, tabId, bmp)
                }, 400)
            }
            override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                val u = request?.url?.toString() ?: return false
                if (u.startsWith("blob:") || u.startsWith("data:") ||
                    u.startsWith("about:") || u.startsWith("javascript:")) return false
                if (!u.startsWith("http")) return true
                if (request?.isForMainFrame != true) return false
                if (act.trustedLoad) return false
                val t = tab() ?: return false
                val toHost = OpenPolicy.normalize(AdBlocker.hostOf(u))
                val fromHost = OpenPolicy.normalize(AdBlocker.hostOf(t.url))
                if (toHost == null || fromHost == null || OpenPolicy.sameSite(fromHost, toHost)) return false
                view?.post { act.ui.considerOpen(tabId, u) }
                return true
            }
        }
        webChromeClient = object : WebChromeClient() {
            override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: android.os.Message?): Boolean {
                if (prefs.blockPopups && !isUserGesture) return false
                if (resultMsg == null) return false
                val parent = tab()
                val child = store.addTab(
                    incognito = parent?.incognito == true,
                    url = "",
                    openerId = tabId,
                    openedAsPopup = true,
                )
                val childWv = act.tabViews.obtain(act, act, child.id, loadUrl = false)
                val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
                transport.webView = childWv
                resultMsg.sendToTarget()
                view?.post { act.ui.onPopupCreated(child.id) }
                return true
            }
            override fun onReceivedTitle(view: WebView?, title: String?) {
                val t = tab() ?: return
                if (!title.isNullOrBlank()) {
                    t.title = title
                    MediaDetector.updateTitle(tabId, title)
                }
            }
            override fun onReceivedIcon(view: WebView?, icon: Bitmap?) {
                if (icon != null) TabThumbs.saveFavicon(act, tabId, icon)
            }
            override fun onShowCustomView(view: View?, callback: CustomViewCallback?) {
                act.customView = view
                act.customCallback = callback
                act.ui.onFullscreen()
            }
            override fun onHideCustomView() {
                act.customView = null
                act.customCallback?.onCustomViewHidden()
                act.customCallback = null
                act.ui.onFullscreen()
            }
            override fun onPermissionRequest(request: PermissionRequest?) {
                val req = request ?: return
                val grant = req.resources.filter { it == PermissionRequest.RESOURCE_PROTECTED_MEDIA_ID }
                if (grant.isNotEmpty()) req.grant(grant.toTypedArray()) else req.deny()
            }
        }
    }
}
