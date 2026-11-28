package com.shekh.bddm.browser

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.media.MediaMetadataRetriever
import android.util.LruCache
import android.webkit.CookieManager
import com.shekh.bddm.data.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.ConcurrentHashMap

/** Small in-memory thumbnail cache for the detected-media sheet. */
object ThumbCache {
    private val cache = object : LruCache<String, Bitmap>(16 * 1024 * 1024) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }

    fun get(key: String): Bitmap? = cache.get(key)
    fun put(key: String, b: Bitmap) { cache.put(key, b) }

    suspend fun load(url: String, referer: String): Bitmap? {
        if (url.isBlank()) return null
        cache.get(url)?.let { return it }
        if (!url.startsWith("http")) return null
        return withContext(Dispatchers.IO) {
            try {
                val c = MediaEnricher.open(url, referer, 0)
                val bytes = c.inputStream.use { it.readBytes() }
                c.disconnect()
                if (bytes.size > 4_000_000) return@withContext null
                val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                BitmapFactory.decodeByteArray(bytes, 0, bytes.size, o)
                var s = 1
                while (o.outWidth / (s * 2) >= 320) s *= 2
                val b = BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = s })
                if (b != null) cache.put(url, b)
                b
            } catch (_: Exception) { null }
        }
    }
}

/** Fills in size / resolution / duration / thumbnail / stream qualities for detected media (item 112). */
object MediaEnricher {
    private val sem = Semaphore(3)
    private val inFlight = ConcurrentHashMap.newKeySet<String>()

    fun enrichAll(scope: CoroutineScope, tabId: String, items: List<DetectedMedia>) {
        for (m in items) {
            if (m.probed || !inFlight.add(tabId + m.url)) continue
            scope.launch(Dispatchers.IO) {
                try { sem.withPermit { enrich(tabId, m) } } catch (_: Throwable) {
                    MediaDetector.updateMeta(tabId, m.url) { it.copy(probed = true) }
                } finally { inFlight.remove(tabId + m.url) }
            }
        }
    }

    fun open(url: String, referer: String, rangeEnd: Long): HttpURLConnection {
        val c = URL(url).openConnection() as HttpURLConnection
        c.connectTimeout = 8000
        c.readTimeout = 10000
        c.instanceFollowRedirects = true
        c.setRequestProperty("User-Agent", UA)
        if (referer.startsWith("http")) c.setRequestProperty("Referer", referer)
        try { CookieManager.getInstance().getCookie(url)?.let { if (it.isNotBlank()) c.setRequestProperty("Cookie", it) } } catch (_: Throwable) {}
        if (rangeEnd > 0) c.setRequestProperty("Range", "bytes=0-$rangeEnd")
        return c
    }

    private const val UA = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36"

    private fun text(url: String, referer: String, max: Int = 400_000): String {
        val c = open(url, referer, 0)
        try {
            if (c.responseCode !in 200..299) return ""
            return c.inputStream.use { s ->
                val buf = s.readNBytesCompat(max)
                String(buf, Charsets.UTF_8)
            }
        } finally { c.disconnect() }
    }

    private fun java.io.InputStream.readNBytesCompat(max: Int): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        val b = ByteArray(16 * 1024)
        while (out.size() < max) {
            val n = read(b, 0, minOf(b.size, max - out.size()))
            if (n <= 0) break
            out.write(b, 0, n)
        }
        return out.toByteArray()
    }

    private fun sizeAndType(url: String, referer: String): Pair<Long, String> {
        val c = open(url, referer, 1)
        try {
            val code = c.responseCode
            val type = c.contentType.orEmpty()
            val size = when (code) {
                206 -> c.getHeaderField("Content-Range")?.substringAfterLast('/')?.toLongOrNull() ?: 0L
                200 -> c.contentLengthLong.coerceAtLeast(0L)
                else -> 0L
            }
            return size to type
        } finally { c.disconnect() }
    }

    private fun enrich(tabId: String, m: DetectedMedia) {
        when (m.kind) {
            MediaKind.DIRECT, MediaKind.AUDIO -> {
                val (size, type) = try { sizeAndType(m.url, m.pageUrl) } catch (_: Exception) { 0L to "" }
                MediaDetector.updateMeta(tabId, m.url) { it.copy(sizeBytes = if (size > 0) size else it.sizeBytes, mime = type, probed = true) }
                if (m.kind == MediaKind.DIRECT && (m.height == 0 || m.durationSec <= 0 || m.thumb.isBlank())) retrieve(tabId, m)
            }
            MediaKind.HLS -> {
                val body = text(m.url, m.pageUrl)
                if (HlsParser.isMaster(body)) {
                    val vars = HlsParser.parseMaster(body, m.url)
                    val aux = HlsParser.parseAudioAndSubs(body, m.url)
                    val dur = vars.lastOrNull()?.let { v -> try { HlsParser.mediaDuration(text(v.url, m.pageUrl)) } catch (_: Exception) { 0.0 } } ?: 0.0
                    MediaDetector.updateMeta(tabId, m.url) { it.copy(variants = vars + aux, durationSec = if (dur > 0) dur else it.durationSec,
                        height = vars.firstOrNull()?.height ?: it.height, probed = true) }
                } else {
                    val dur = HlsParser.mediaDuration(body)
                    val rendition = when {
                        HlsParser.isSubtitlePlaylist(m.url, body) -> "subtitles"
                        HlsParser.isAudioPlaylist(m.url, body) -> "audio"
                        else -> ""
                    }
                    MediaDetector.updateMeta(tabId, m.url) { it.copy(durationSec = if (dur > 0) dur else it.durationSec, rendition = rendition, probed = true) }
                }
            }
            MediaKind.DASH -> {
                val body = text(m.url, m.pageUrl)
                val vars = DashParser.variants(body, m.url)
                val dur = DashParser.duration(body)
                MediaDetector.updateMeta(tabId, m.url) { it.copy(variants = vars, durationSec = if (dur > 0) dur else it.durationSec,
                    height = vars.firstOrNull()?.height ?: it.height, probed = true) }
            }
            MediaKind.PAGE, MediaKind.EMBED -> {
                val info = YtDlp.probe(m.url, referer = m.pageUrl)
                val vars = info.heights.map { (h, sz) -> MediaVariant(m.url, 0, h, 0, sz) }
                MediaDetector.updateMeta(tabId, m.url) {
                    it.copy(title = info.title.ifBlank { it.title }, thumb = info.thumb.ifBlank { it.thumb },
                        durationSec = if (info.durationSec > 0) info.durationSec else it.durationSec,
                        variants = vars, height = vars.firstOrNull()?.height ?: it.height, probed = true)
                }
            }
        }
    }

    /** Duration, resolution and a frame for direct video files. Gives up after 8 s. */
    private fun retrieve(tabId: String, m: DetectedMedia) {
        var w = 0; var h = 0; var dur = 0.0; var frame: Bitmap? = null
        val t = Thread {
            val r = MediaMetadataRetriever()
            try {
                val headers = HashMap<String, String>()
                headers["User-Agent"] = UA
                if (m.pageUrl.startsWith("http")) headers["Referer"] = m.pageUrl
                try { CookieManager.getInstance().getCookie(m.url)?.let { if (it.isNotBlank()) headers["Cookie"] = it } } catch (_: Throwable) {}
                r.setDataSource(m.url, headers)
                w = r.extractMetadata(MediaMetadataRetriever.METADATA_KEY_VIDEO_WIDTH)?.toIntOrNull() ?: 0
                h = r.extractMetadata(MediaMetadataRetriever.METADATA_KEY_VIDEO_HEIGHT)?.toIntOrNull() ?: 0
                dur = (r.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION)?.toLongOrNull() ?: 0L) / 1000.0
                if (m.thumb.isBlank()) {
                    frame = r.getFrameAtTime(if (dur > 4) 2_000_000L else 0L, MediaMetadataRetriever.OPTION_CLOSEST_SYNC)
                        ?.let { b -> if (b.width > 320) Bitmap.createScaledBitmap(b, 320, 320 * b.height / b.width, true) else b }
                }
            } catch (_: Throwable) {
            } finally { try { r.release() } catch (_: Throwable) {} }
        }
        t.isDaemon = true
        t.start()
        t.join(8000)
        val f = frame
        if (f != null) ThumbCache.put("frame:" + m.url, f)
        MediaDetector.updateMeta(tabId, m.url) {
            it.copy(width = if (it.width > 0) it.width else w, height = if (it.height > 0) it.height else if (w > 0 && h > 0) minOf(w, h) else h,
                durationSec = if (it.durationSec > 0) it.durationSec else dur,
                thumb = if (it.thumb.isBlank() && f != null) "frame:" + m.url else it.thumb)
        }
    }
}
