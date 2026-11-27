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
    private val hosts = HashSet<String>()
    private val exceptions = HashSet<String>()
    private val pathRules = ArrayList<Pair<String, String>>() // host-or-* to path contains
    @Volatile private var loaded = false
    val blockedOnPage = AtomicInteger(0)

    private val dir get() = File(ctx.filesDir, "filters").apply { mkdirs() }

    fun loadAsync() {
        Executors.newSingleThreadExecutor().execute {
            loadFromDisk()
            if (hosts.isEmpty()) loadBuiltIn()
            loaded = true
            if (prefs.filterListAutoUpdate) maybeUpdate()
        }
    }

    @Synchronized
    private fun loadFromDisk() {
        hosts.clear(); exceptions.clear(); pathRules.clear()
        listOf("easylist.txt", "easyprivacy.txt").forEach { name ->
            val f = File(dir, name)
            if (f.isFile) parse(f.readText())
        }
    }

    private fun loadBuiltIn() {
        // A compact starter list so blocking works before the full lists arrive.
        BUILTIN.split('\n').forEach { addRule(it) }
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
    private fun parse(text: String) {
        text.lineSequence().forEach { addRule(it) }
    }

    private fun addRule(raw: String) {
        var line = raw.trim()
        if (line.isEmpty() || line.startsWith("!") || line.startsWith("[") || line.contains("##") || line.contains("#@#")) return
        if (line.startsWith("@@")) {
            val h = hostFrom(line.removePrefix("@@")) ?: return
            exceptions.add(h)
            return
        }
        if (line.startsWith("||")) {
            val rest = line.removePrefix("||")
            val cut = rest.indexOfAny(charArrayOf('^', '/', '$'))
            val host = (if (cut >= 0) rest.substring(0, cut) else rest).lowercase().trim('.')
            if (host.isEmpty() || host.contains("*")) return
            if (cut >= 0 && rest.getOrNull(cut) == '/') {
                val path = rest.substring(cut).substringBefore('$')
                pathRules.add(host to path)
            } else {
                hosts.add(host)
            }
        }
    }

    private fun hostFrom(rule: String): String? {
        val s = rule.removePrefix("||")
        val cut = s.indexOfAny(charArrayOf('^', '/', '$'))
        val h = (if (cut >= 0) s.substring(0, cut) else s).lowercase().trim('.')
        return h.ifEmpty { null }
    }

    fun shouldBlock(url: String, pageHost: String, isThirdParty: Boolean): Boolean {
        if (!prefs.adBlocker || !prefs.adblockOnFor(pageHost)) return false
        val u = url.lowercase()
        val host = hostOf(u) ?: return false
        if (exceptions.contains(host) || exceptions.any { host.endsWith(".$it") }) return false
        if (matchHost(host)) return true
        if (prefs.blockTrackers && isThirdParty && TRACKERS.any { host == it || host.endsWith(".$it") }) return true
        synchronized(this) {
            for ((h, path) in pathRules) {
                if (host == h || host.endsWith(".$h")) {
                    if (u.contains(path.lowercase())) return true
                }
            }
        }
        return false
    }

    private fun matchHost(host: String): Boolean {
        var h = host
        while (true) {
            if (hosts.contains(h)) return true
            val i = h.indexOf('.')
            if (i <= 0) return false
            h = h.substring(i + 1)
        }
    }

    fun intercept(request: WebResourceRequest, pageHost: String): WebResourceResponse? {
        val url = request.url?.toString() ?: return null
        val reqHost = request.url.host ?: ""
        val third = reqHost.isNotEmpty() && pageHost.isNotEmpty() &&
            !reqHost.equals(pageHost, true) && !reqHost.endsWith(".$pageHost")
        if (shouldBlock(url, pageHost, third)) {
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
        fun hostOf(url: String): String? {
            return try {
                val s = if (url.contains("://")) url else "https://$url"
                URL(s).host?.lowercase()
            } catch (_: Exception) { null }
        }
        fun emptyResponse(): WebResourceResponse {
            return WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
        }
    }
}
