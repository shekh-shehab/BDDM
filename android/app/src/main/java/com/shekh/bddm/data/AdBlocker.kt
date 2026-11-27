package com.shekh.bddm.data

import android.content.Context
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import java.io.ByteArrayInputStream
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/**
 * EasyList / EasyPrivacy network-filter matcher (item 95).
 * Cosmetic rules are skipped. Lists auto-update daily when enabled.
 */
class AdBlocker(private val ctx: Context, private val prefs: Prefs) {
    private val engine = FilterEngine()
    @Volatile private var loaded = false
    val blockedOnPage = AtomicInteger(0)

    private val dir get() = File(ctx.filesDir, "filters").apply { mkdirs() }

    fun loadAsync() {
        Executors.newSingleThreadExecutor().execute {
            loadFromDisk()
            if (engine.isEmpty()) loadBuiltIn()
            loaded = true
            if (prefs.filterListAutoUpdate) maybeUpdate()
        }
    }

    @Synchronized
    private fun loadFromDisk() {
        engine.clear()
        listOf("easylist.txt", "easyprivacy.txt").forEach { name ->
            val f = File(dir, name)
            if (f.isFile) engine.addRules(f.readText())
        }
    }

    private fun loadBuiltIn() {
        engine.addRules(BUILTIN)
    }

    private fun maybeUpdate() {
        val stamp = File(dir, "updated")
        if (stamp.isFile && System.currentTimeMillis() - stamp.lastModified() < 20 * 60 * 60 * 1000L) return
        try {
            download(EASYLIST, File(dir, "easylist.txt"))
            download(EASYPRIVACY, File(dir, "easyprivacy.txt"))
            stamp.writeText(System.currentTimeMillis().toString())
            loadFromDisk()
        } catch (_: Exception) { /* keep existing lists */ }
    }

    fun forceUpdate() {
        Executors.newSingleThreadExecutor().execute { File(dir, "updated").delete(); maybeUpdate() }
    }

    private fun download(url: String, dest: File) {
        val c = URL(url).openConnection() as HttpURLConnection
        c.connectTimeout = 20000
        c.readTimeout = 60000
        c.setRequestProperty("User-Agent", "BDDM-Android/1.6.0")
        c.inputStream.use { input -> dest.outputStream().use { input.copyTo(it) } }
        c.disconnect()
    }

    @Synchronized
    fun shouldBlock(url: String, pageHost: String, isThirdParty: Boolean, isMainFrame: Boolean = false): Boolean {
        if (!prefs.adBlocker || !prefs.adblockOnFor(pageHost)) return false
        val host = FilterEngine.hostOf(url) ?: return false
        if (engine.isAllowlisted(host)) return false
        if (engine.shouldBlock(url, pageHost, isThirdParty, isMainFrame)) return true
        if (prefs.blockTrackers && isThirdParty && !isMainFrame &&
            FilterEngine.inSet(host, TRACKERS) && !engine.isAllowlisted(host)
        ) return true
        return false
    }

    fun intercept(request: WebResourceRequest, pageHost: String): WebResourceResponse? {
        if (request.isForMainFrame) return null
        val url = request.url?.toString() ?: return null
        val reqHost = request.url.host ?: ""
        val third = reqHost.isNotEmpty() && pageHost.isNotEmpty() &&
            !reqHost.equals(pageHost, true) && !reqHost.endsWith(".$pageHost") &&
            !pageHost.endsWith(".$reqHost")
        if (shouldBlock(url, pageHost, third, isMainFrame = false)) {
            blockedOnPage.incrementAndGet()
            return emptyResponse()
        }
        return null
    }

    fun resetPageCount() { blockedOnPage.set(0) }

    companion object {
        const val EASYLIST = "https://easylist.to/easylist/easylist.txt"
        const val EASYPRIVACY = "https://easylist.to/easylist/easyprivacy.txt"
        private val TRACKERS = setOf(
            "doubleclick.net", "google-analytics.com", "googletagmanager.com",
            "googlesyndication.com", "facebook.net", "scorecardresearch.com",
            "adservice.google.com", "hotjar.com", "taboola.com", "outbrain.com",
            "criteo.com", "ads-twitter.com", "quantserve.com", "adnxs.com",
        )
        private const val BUILTIN = """
||doubleclick.net^
||googlesyndication.com^
||googleadservices.com^
||adservice.google.com^
||pagead2.googlesyndication.com^
||facebook.net^
||ads-twitter.com^
||taboola.com^
||outbrain.com^
||scorecardresearch.com^
||hotjar.com^
||adnxs.com^
||criteo.com^
||quantserve.com^
||google-analytics.com^
||googletagmanager.com^
"""
        fun hostOf(url: String): String? = FilterEngine.hostOf(url)
        fun emptyResponse(): WebResourceResponse {
            return WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
        }
    }
}
