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

data class FilterList(val id: String, val name: String, val url: String, val file: String)

/**
 * Network ad/tracker blocking with uBlock Origin's lists (item 107).
 * WebView cannot run the extension itself, so BDDM downloads the same filter lists and
 * applies their network rules. Cosmetic and scriptlet rules are skipped.
 */
class AdBlocker(private val ctx: Context, private val prefs: Prefs) {
    /** Replaced as a whole when lists change, so request threads never wait on parsing. */
    @Volatile private var engine = FilterEngine()
    @Volatile var loaded = false
        private set
    @Volatile var status: String = "Loading filter lists…"
        private set
    val blockedOnPage = AtomicInteger(0)
    private val worker = Executors.newSingleThreadExecutor()

    private val dir get() = File(ctx.filesDir, "filters").apply { mkdirs() }

    fun loadAsync() {
        worker.execute {
            rebuild()
            val missing = LISTS.any { prefs.filterListOn(it.id) && !File(dir, it.file).isFile }
            if (prefs.filterListAutoUpdate || missing) maybeUpdate(force = false)
        }
    }

    /** Parses all enabled lists from disk into a fresh engine and swaps it in. */
    private fun rebuild() {
        val texts = ArrayList<String>()
        for (l in LISTS) {
            if (!prefs.filterListOn(l.id)) continue
            val f = File(dir, l.file)
            if (f.isFile) try { texts.add(f.readText()) } catch (_: Exception) {}
        }
        val e = FilterEngine()
        if (texts.isEmpty()) e.addRules(BUILTIN) else { e.addLists(texts); e.addRules(BUILTIN) }
        engine = e
        loaded = true
        status = "${e.ruleCount} network rules from ${texts.size} list(s)" + if (texts.isEmpty()) " (built-in fallback; lists download on first connection)" else ""
    }

    /** Re-reads the lists after the user toggled one in Settings. */
    fun reloadAsync() { worker.execute { rebuild() } }

    private fun maybeUpdate(force: Boolean) {
        val stamp = File(dir, "updated")
        val missing = LISTS.any { prefs.filterListOn(it.id) && !File(dir, it.file).isFile }
        if (!force && !missing && stamp.isFile && System.currentTimeMillis() - stamp.lastModified() < 20 * 60 * 60 * 1000L) return
        var ok = 0
        for (l in LISTS) {
            if (!prefs.filterListOn(l.id)) continue
            try { download(l.url, File(dir, l.file)); ok++ } catch (_: Exception) { /* keep the previous copy */ }
        }
        if (ok > 0) {
            stamp.writeText(System.currentTimeMillis().toString())
            rebuild()
        }
    }

    fun forceUpdate() { worker.execute { maybeUpdate(force = true) } }

    private fun download(url: String, dest: File) {
        val c = URL(url).openConnection() as HttpURLConnection
        try {
            c.connectTimeout = 20000
            c.readTimeout = 60000
            c.setRequestProperty("User-Agent", "BDDM-Android")
            if (c.responseCode !in 200..299) throw IllegalStateException("HTTP ${c.responseCode}")
            val tmp = File(dest.parentFile, dest.name + ".tmp")
            c.inputStream.use { input -> tmp.outputStream().use { input.copyTo(it) } }
            if (tmp.length() < 200) { tmp.delete(); throw IllegalStateException("empty list") }
            if (!tmp.renameTo(dest)) { dest.delete(); tmp.renameTo(dest) }
        } finally { c.disconnect() }
    }

    fun shouldBlock(url: String, pageHost: String, isThirdParty: Boolean, isMainFrame: Boolean = false, type: Int = FilterEngine.T_UNKNOWN): Boolean {
        if (!prefs.adBlocker || !prefs.adblockOnFor(pageHost)) return false
        val host = FilterEngine.hostOf(url) ?: return false
        val e = engine
        if (e.isAllowlisted(host)) return false
        if (e.shouldBlock(url, pageHost, isThirdParty, isMainFrame, type)) return true
        if (prefs.blockTrackers && isThirdParty && !isMainFrame &&
            FilterEngine.inSet(host, TRACKERS) && !e.isAllowlisted(host)
        ) return true
        return false
    }

    /** Ad/tracker URL check that ignores the "never block media" exemption (for detection lists). */
    fun isAdUrl(url: String, pageHost: String): Boolean {
        if (!prefs.adBlocker) return false
        val host = FilterEngine.hostOf(url) ?: return false
        val third = FilterEngine.isThirdParty(host, pageHost)
        val e = engine
        if (e.isAdUrl(url, third, pageHost)) return true
        return prefs.blockTrackers && third && FilterEngine.inSet(host, TRACKERS) && !e.isAllowlisted(host)
    }

    fun intercept(request: WebResourceRequest, pageHost: String): WebResourceResponse? {
        if (request.isForMainFrame) return null
        val url = request.url?.toString() ?: return null
        val reqHost = request.url.host ?: ""
        val third = FilterEngine.isThirdParty(reqHost, pageHost)
        val type = FilterEngine.guessType(url, request.requestHeaders?.get("Accept"))
        if (shouldBlock(url, pageHost, third, isMainFrame = false, type = type)) {
            blockedOnPage.incrementAndGet()
            return emptyResponse()
        }
        return null
    }

    fun resetPageCount() { blockedOnPage.set(0) }

    companion object {
        const val EASYLIST = "https://easylist.to/easylist/easylist.txt"
        const val EASYPRIVACY = "https://easylist.to/easylist/easyprivacy.txt"
        private const val UASSETS = "https://ublockorigin.github.io/uAssets/filters/"
        val LISTS = listOf(
            FilterList("easylist", "EasyList", EASYLIST, "easylist.txt"),
            FilterList("easyprivacy", "EasyPrivacy", EASYPRIVACY, "easyprivacy.txt"),
            FilterList("ubo-filters", "uBlock filters", UASSETS + "filters.min.txt", "ubo-filters.txt"),
            FilterList("ubo-badware", "uBlock filters – Badware risks", UASSETS + "badware.min.txt", "ubo-badware.txt"),
            FilterList("ubo-privacy", "uBlock filters – Privacy", UASSETS + "privacy.min.txt", "ubo-privacy.txt"),
            FilterList("ubo-unbreak", "uBlock filters – Unbreak", UASSETS + "unbreak.min.txt", "ubo-unbreak.txt"),
            FilterList("peterlowe", "Peter Lowe's Ad and tracking server list", "https://pgl.yoyo.org/adservers/serverlist.php?hostformat=hosts&showintro=0&mimetype=plaintext", "peterlowe.txt"),
        )
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
