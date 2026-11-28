package com.shekh.bddm.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.URL

enum class MediaKind(val label: String) {
    DIRECT("Video file"),
    AUDIO("Audio file"),
    HLS("HLS stream"),
    DASH("DASH stream"),
    PAGE("Page video"),
    EMBED("Embedded player"),
}

data class DetectedMedia(
    val url: String,
    val kind: MediaKind,
    val title: String,
    val pageUrl: String,
    val via: String,
    val at: Long = System.currentTimeMillis(),
    // Filled in later by the page hooks or the network probe (item 112).
    val width: Int = 0,
    val height: Int = 0,
    val durationSec: Double = 0.0,
    val sizeBytes: Long = 0,
    val thumb: String = "",
    val mime: String = "",
    val variants: List<MediaVariant> = emptyList(),
    val probed: Boolean = false,
) {
    val host: String get() = MediaClassifier.hostOf(url) ?: ""
}

/** One quality of an HLS/DASH stream (or a same-named file in another resolution). */
data class MediaVariant(val url: String, val width: Int, val height: Int, val bandwidth: Long, val sizeBytes: Long = 0)

/** Pure URL classification so it can be unit-tested without Android. */
object MediaClassifier {
    private val VIDEO_EXT = setOf("mp4", "webm", "mkv", "m4v", "mov", "flv", "3gp", "avi", "ogv")
    private val AUDIO_EXT = setOf("mp3", "m4a", "aac", "ogg", "opus", "wav", "flac")
    private val SEGMENT_NAME = Regex(
        "(^|[-_./])(seg(ment)?|chunk|frag(ment)?|part|init|slice)[-_]?\\d*([-_.]|$)|[-_]\\d+[-_]v\\d+[-_]a\\d+|\\.(ts|m4s|cmfv|cmfa|aac_seg)$",
        RegexOption.IGNORE_CASE,
    )
    private val EMBED_HOSTS = listOf(
        "youtube.com", "youtube-nocookie.com", "youtu.be", "vimeo.com", "dailymotion.com",
        "facebook.com", "fb.watch", "tiktok.com", "twitch.tv", "streamable.com", "rumble.com",
        "bilibili.com", "ok.ru", "vk.com", "jwplatform.com", "jwplayer.com", "wistia.com",
        "brightcove.net", "players.brightcove.net", "kaltura.com", "player.vimeo.com",
    )

    fun hostOf(url: String): String? = try {
        URL(if (url.contains("://")) url else "https://$url").host?.lowercase()
    } catch (_: Exception) { null }

    private fun pathOf(url: String): String =
        try { URL(url).path.lowercase() } catch (_: Exception) { url.substringBefore('?').substringBefore('#').lowercase() }

    fun extension(url: String): String {
        val last = pathOf(url).substringAfterLast('/')
        return if (last.contains('.')) last.substringAfterLast('.') else ""
    }

    fun isHls(url: String, mime: String? = null): Boolean {
        val m = mime?.lowercase().orEmpty()
        if (m.contains("mpegurl")) return true
        val p = pathOf(url)
        return p.endsWith(".m3u8") || p.contains(".m3u8/")
    }

    fun isDash(url: String, mime: String? = null): Boolean {
        val m = mime?.lowercase().orEmpty()
        if (m.contains("dash+xml")) return true
        val p = pathOf(url)
        return p.endsWith(".mpd") || p.contains(".mpd/")
    }

    /** True for HLS/DASH/CMAF fragments and numbered chunks that are not a whole video. */
    fun isSegment(url: String): Boolean {
        val u = url.lowercase()
        // YouTube serves a video as many ranged videoplayback requests: use the page instead.
        if (u.contains("googlevideo.com/videoplayback")) return true
        if (u.contains("range=") && (u.contains("mime=video") || u.contains("mime=audio"))) return true
        val name = pathOf(url).substringAfterLast('/')
        return SEGMENT_NAME.containsMatchIn(name)
    }

    /** For sites yt-dlp knows: is this a page that can hold a single video (not a feed or home page)? */
    fun isWatchPage(url: String): Boolean {
        val h = hostOf(url) ?: return false
        val path = pathOf(url)
        if (h == "youtu.be") return path.length > 1
        if (h == "youtube.com" || h.endsWith(".youtube.com")) {
            return path.startsWith("/watch") || path.startsWith("/shorts/") || path.startsWith("/live/") || path.startsWith("/embed/")
        }
        return path.length > 1
    }

    fun isEmbedPlayer(url: String): Boolean {
        val h = hostOf(url) ?: return false
        val p = pathOf(url)
        val onKnownHost = EMBED_HOSTS.any { h == it || h.endsWith(".$it") }
        return onKnownHost && (p.contains("/embed") || p.contains("/player") || h.startsWith("player.") || p.contains("/v/") || p.contains("/video"))
    }

    /** Returns the kind of media a URL points at, or null if it is not (a whole) media URL. */
    fun classify(url: String, mime: String? = null): MediaKind? {
        if (!(url.startsWith("http://") || url.startsWith("https://"))) return null
        if (isHls(url, mime)) return MediaKind.HLS
        if (isDash(url, mime)) return MediaKind.DASH
        if (isSegment(url)) return null
        val ext = extension(url)
        val m = mime?.lowercase().orEmpty()
        if (ext in VIDEO_EXT || m.startsWith("video/")) return MediaKind.DIRECT
        if (ext in AUDIO_EXT || m.startsWith("audio/")) return MediaKind.AUDIO
        return null
    }

    /** Key used to avoid listing the same media twice (ignores fragment and cache-busting params). */
    fun key(url: String): String {
        val base = url.substringBefore('#')
        val q = base.substringAfter('?', "")
        if (q.isEmpty()) return base
        val keep = q.split('&').filter { kv ->
            val k = kv.substringBefore('=').lowercase()
            k !in setOf("_", "t", "ts", "time", "timestamp", "rand", "random", "nocache", "cb", "r")
        }
        val path = base.substringBefore('?')
        return if (keep.isEmpty()) path else path + "?" + keep.joinToString("&")
    }

    fun titleFor(url: String, pageTitle: String): String {
        val file = pathOf(url).substringAfterLast('/')
        val t = pageTitle.trim()
        return when {
            t.isNotBlank() -> t
            file.isNotBlank() -> file
            else -> url
        }
    }
}

/**
 * Per-tab store of everything the browser noticed. Network hits come from
 * shouldInterceptRequest, DOM/XHR/fetch/MSE hints come from the in-page hooks.
 * Entries accumulate; they are only cleared when the tab starts a new page load.
 */
object MediaDetector {
    private const val MAX_PER_TAB = 30
    private val lock = Any()
    private val map = HashMap<String, MutableList<DetectedMedia>>()
    private val _all = MutableStateFlow<Map<String, List<DetectedMedia>>>(emptyMap())
    val all: StateFlow<Map<String, List<DetectedMedia>>> = _all

    fun forTab(tabId: String): List<DetectedMedia> = _all.value[tabId].orEmpty()

    /** Called from any thread. Returns true if a new entry was added. */
    fun offer(tabId: String, url: String, pageUrl: String, pageTitle: String, via: String, mime: String? = null, forceKind: MediaKind? = null): Boolean {
        val kind = forceKind ?: MediaClassifier.classify(url, mime) ?: return false
        return add(tabId, DetectedMedia(url, kind, MediaClassifier.titleFor(url, pageTitle), pageUrl, via))
    }

    fun add(tabId: String, item: DetectedMedia): Boolean {
        synchronized(lock) {
            val list = map.getOrPut(tabId) { ArrayList() }
            val k = MediaClassifier.key(item.url)
            val existing = list.indexOfFirst { MediaClassifier.key(it.url) == k }
            if (existing >= 0) {
                // Keep the better title and any metadata the newer hint carries.
                val old = list[existing]
                val merged = old.copy(
                    title = if (item.title.length > old.title.length && item.kind == old.kind) item.title else old.title,
                    width = if (old.width > 0) old.width else item.width,
                    height = if (old.height > 0) old.height else item.height,
                    durationSec = if (old.durationSec > 0) old.durationSec else item.durationSec,
                    thumb = old.thumb.ifBlank { item.thumb },
                )
                if (merged != old) { list[existing] = merged; publish() }
                return false
            }
            if (list.size >= MAX_PER_TAB) list.removeAt(list.lastIndex)
            // Streams and files first, page/embedded fall-backs after.
            val rank = rankOf(item.kind)
            val at = list.indexOfFirst { rankOf(it.kind) > rank }.let { if (it < 0) list.size else it }
            list.add(at, item)
            publish()
            return true
        }
    }

    private fun rankOf(k: MediaKind) = when (k) {
        MediaKind.DIRECT -> 0
        MediaKind.HLS, MediaKind.DASH -> 1
        MediaKind.AUDIO -> 2
        MediaKind.PAGE -> 3
        MediaKind.EMBED -> 4
    }

    fun updateTitle(tabId: String, pageTitle: String) {
        if (pageTitle.isBlank()) return
        synchronized(lock) {
            val list = map[tabId] ?: return
            var changed = false
            for (i in list.indices) {
                val it = list[i]
                val file = MediaClassifier.titleFor(it.url, "")
                if (it.title == file && it.title != pageTitle && it.kind != MediaKind.PAGE) {
                    list[i] = it.copy(title = pageTitle); changed = true
                }
            }
            if (changed) publish()
        }
    }

    /** Updates one entry in place (metadata from probes). */
    fun updateMeta(tabId: String, url: String, f: (DetectedMedia) -> DetectedMedia) {
        synchronized(lock) {
            val list = map[tabId] ?: return
            val k = MediaClassifier.key(url)
            val i = list.indexOfFirst { MediaClassifier.key(it.url) == k }
            if (i < 0) return
            val n = f(list[i])
            if (n != list[i]) { list[i] = n; publish() }
        }
    }

    /** Removes entries the user cleared from the sheet. */
    fun remove(tabId: String, urls: Collection<String>) {
        val keys = urls.map { MediaClassifier.key(it) }.toSet()
        synchronized(lock) {
            val list = map[tabId] ?: return
            if (list.removeAll { MediaClassifier.key(it.url) in keys }) publish()
        }
    }

    private val pageThumbs = HashMap<String, String>()
    fun setPageThumb(tabId: String, url: String) { synchronized(lock) { pageThumbs[tabId] = url } }
    fun pageThumb(tabId: String): String = synchronized(lock) { pageThumbs[tabId].orEmpty() }

    fun clear(tabId: String) {
        synchronized(lock) {
            pageThumbs.remove(tabId)
            if (map.remove(tabId) != null) publish()
        }
    }

    private fun publish() {
        _all.value = map.mapValues { it.value.toList() }
    }
}
