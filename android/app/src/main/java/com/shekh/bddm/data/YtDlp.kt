package com.shekh.bddm.data

import android.content.Context
import com.yausername.ffmpeg.FFmpeg
import com.yausername.youtubedl_android.YoutubeDL
import com.yausername.youtubedl_android.YoutubeDLRequest
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.locks.ReentrantReadWriteLock
import kotlin.concurrent.read

data class VideoFormat(
    val id: String,
    val label: String,
    val note: String,
    val size: Long,
    val ext: String,
    val isAudio: Boolean,
)

data class VideoInfo(
    val url: String,
    val title: String,
    val site: String,
    val duration: String,
    val thumb: String,
    val formats: List<VideoFormat>,
    val durationSec: Double = 0.0,
    val heights: List<Pair<Int, Long>> = emptyList(),  // (height, approx size) of video formats
)

object YtDlp {
    @Volatile var ready = false
    @Volatile var initError: String? = null
    @Volatile var version: String = ""
    private val initDone = CountDownLatch(1)
    /**
     * Bug 109: the self-update replaced yt-dlp's files while the first download/probe was
     * running, so the first attempt failed and Retry worked. Downloads and probes now hold
     * the read lock; the update only runs when it can take the write lock (nothing running).
     */
    private val useLock = ReentrantReadWriteLock()
    @Volatile var updating = false
        private set

    /** Extracts yt-dlp/ffmpeg off the main thread, then refreshes yt-dlp at most twice a day. */
    fun initAsync(app: Context) {
        Thread {
            try {
                YoutubeDL.getInstance().init(app)
                FFmpeg.getInstance().init(app)
                ready = true
            } catch (t: Throwable) {
                ready = false
                initError = t.message ?: t.javaClass.simpleName
            } finally {
                initDone.countDown()
            }
            if (ready) {
                try { version = YoutubeDL.version(app) ?: "" } catch (_: Throwable) {}
                try {
                    val sp = app.getSharedPreferences("bddm_ytdlp", Context.MODE_PRIVATE)
                    val last = sp.getLong("updated", 0L)
                    if (System.currentTimeMillis() - last > 12 * 3600 * 1000L) {
                        val w = useLock.writeLock()
                        if (w.tryLock(2, TimeUnit.SECONDS)) {
                            try {
                                updating = true
                                YoutubeDL.getInstance().updateYoutubeDL(app, YoutubeDL.UpdateChannel.STABLE)
                                sp.edit().putLong("updated", System.currentTimeMillis()).apply()
                                version = YoutubeDL.version(app) ?: version
                            } finally {
                                updating = false
                                w.unlock()
                            }
                        }
                    }
                } catch (_: Throwable) { /* offline or blocked: keep the bundled build */ }
            }
        }.start()
    }

    /** Blocks (off the main thread!) until init finished or the timeout passes. */
    fun await(timeoutMs: Long = 25000): Boolean {
        try { initDone.await(timeoutMs, TimeUnit.MILLISECONDS) } catch (_: InterruptedException) {}
        return ready
    }

    fun statusText(): String = when {
        ready -> "Ready" + if (version.isNotBlank()) " (yt-dlp $version)" else ""
        initError != null -> "Not available: $initError"
        else -> "Starting…"
    }

    private val siteHosts = listOf(
        "youtube.com", "youtu.be", "tiktok.com", "instagram.com", "facebook.com",
        "fb.watch", "fb.com", "x.com", "twitter.com", "vimeo.com", "reddit.com",
        "twitch.tv", "dailymotion.com", "bilibili.com", "vk.com", "rumble.com",
        "streamable.com", "pinterest.com", "linkedin.com", "threads.net",
    )

    fun isSitePage(url: String): Boolean {
        val h = AdBlocker.hostOf(url) ?: return false
        return siteHosts.any { h == it || h.endsWith(".$it") }
    }

    fun looksLikeMedia(url: String): Boolean {
        val u = url.lowercase().substringBefore('?')
        return listOf(".mp4", ".webm", ".mkv", ".m4v", ".mov", ".m3u8", ".mpd", ".m4a", ".mp3", ".aac").any { u.endsWith(it) }
    }

    fun isHlsOrDash(url: String): Boolean {
        val u = url.lowercase().substringBefore('?')
        return u.endsWith(".m3u8") || u.endsWith(".mpd") || u.contains(".m3u8") || u.contains(".mpd")
    }

    private fun applyHeaders(req: YoutubeDLRequest, url: String, referer: String, userAgent: String, cookie: String) {
        if (referer.isNotBlank()) req.addOption("--referer", referer)
        // Site extractors (YouTube, TikTok, ...) manage their own headers; only help generic/embedded URLs.
        if (!isSitePage(url)) {
            if (userAgent.isNotBlank()) req.addOption("--user-agent", userAgent)
            if (cookie.isNotBlank()) req.addOption("--add-header", "Cookie:$cookie")
        }
    }

    fun probe(url: String, referer: String = "", userAgent: String = "", cookie: String = ""): VideoInfo {
        if (!await()) throw IllegalStateException(initError ?: "yt-dlp is not ready")
        val req = YoutubeDLRequest(url)
        applyHeaders(req, url, referer, userAgent, cookie)
        req.addOption("-J")
        req.addOption("--no-playlist")
        req.addOption("--no-warnings")
        req.addOption("--skip-download")
        val out = useLock.read { YoutubeDL.getInstance().execute(req, null, null) }
        val json = out.out ?: out.err ?: "{}"
        val start = json.indexOf('{')
        val obj = JSONObject(if (start >= 0) json.substring(start) else json)
        val title = obj.optString("title").ifBlank { url }
        val site = obj.optString("extractor_key").ifBlank { obj.optString("extractor") }
        val dur = formatDuration(obj.optDouble("duration", 0.0))
        val thumb = obj.optString("thumbnail")
        val fa = obj.optJSONArray("formats") ?: JSONArray()
        val formats = parseFormats(fa)
        val heights = HashMap<Int, Long>()
        for (i in 0 until fa.length()) {
            val f = fa.optJSONObject(i) ?: continue
            val h = f.optInt("height", 0)
            if (h <= 0 || f.optString("vcodec") == "none") continue
            val sz = f.optLong("filesize", 0).let { if (it > 0) it else f.optLong("filesize_approx", 0) }
            heights[h] = maxOf(heights[h] ?: 0L, sz)
        }
        return VideoInfo(url, title, site, dur, thumb, formats, obj.optDouble("duration", 0.0).let { if (it.isNaN()) 0.0 else it },
            heights.entries.sortedByDescending { it.key }.map { it.key to it.value })
    }

    /** Every video on a profile / playlist / channel page (flat, no download). Max 300. */
    fun listEntries(url: String, referer: String = ""): List<Pair<String, String>> {
        if (!await()) throw IllegalStateException(initError ?: "yt-dlp is not ready")
        val req = YoutubeDLRequest(url)
        applyHeaders(req, url, referer, "", "")
        req.addOption("-J")
        req.addOption("--flat-playlist")
        req.addOption("--yes-playlist")
        req.addOption("--playlist-end", "300")
        req.addOption("--no-warnings")
        val out = useLock.read { YoutubeDL.getInstance().execute(req, null, null) }
        val json = out.out ?: "{}"
        val start = json.indexOf('{')
        val obj = JSONObject(if (start >= 0) json.substring(start) else json)
        return entriesFrom(obj, url)
    }

    fun entriesFrom(obj: JSONObject, pageUrl: String): List<Pair<String, String>> {
        val entries = obj.optJSONArray("entries")
        if (entries == null) {
            return if (obj.has("formats") || obj.has("url")) listOf(pageUrl to obj.optString("title")) else emptyList()
        }
        val out = ArrayList<Pair<String, String>>()
        for (i in 0 until entries.length()) {
            val e = entries.optJSONObject(i) ?: continue
            var u = e.optString("webpage_url").ifBlank { e.optString("url") }
            if (!u.startsWith("http")) {
                val id = e.optString("id").ifBlank { u }
                u = when (e.optString("ie_key").lowercase()) {
                    "youtube" -> "https://www.youtube.com/watch?v=$id"
                    else -> ""
                }
            }
            if (u.startsWith("http")) out.add(u to e.optString("title"))
        }
        return out.distinctBy { it.first }
    }

    private fun parseFormats(arr: JSONArray): List<VideoFormat> {
        val out = ArrayList<VideoFormat>()
        var bestAv = ""
        var bestH = -1
        for (i in 0 until arr.length()) {
            val f = arr.optJSONObject(i) ?: continue
            val id = f.optString("format_id")
            if (id.isBlank()) continue
            val vcodec = f.optString("vcodec")
            val acodec = f.optString("acodec")
            val ext = f.optString("ext").ifBlank { "mp4" }
            val h = f.optInt("height", 0)
            val size = f.optLong("filesize", 0).let { if (it > 0) it else f.optLong("filesize_approx", 0) }
            val note = f.optString("format_note").ifBlank { if (h > 0) "${h}p" else ext }
            val isAudio = (vcodec == "none" || vcodec.isBlank()) && acodec != "none"
            val hasBoth = vcodec != "none" && vcodec.isNotBlank() && acodec != "none" && acodec.isNotBlank()
            if (hasBoth && h >= bestH) { bestH = h; bestAv = id }
            val label = when {
                isAudio -> "Audio only (${ext.uppercase()})"
                h > 0 -> "${h}p"
                else -> note.ifBlank { id }
            }
            out.add(VideoFormat(if (isAudio) "audio:$id" else id, label, note, size, ext, isAudio))
        }
        // Prefer a merged "best" option at the top.
        val best = VideoFormat("best", "Best available", if (bestH > 0) "${bestH}p" else "best", 0, "mp4", false)
        val unique = out.distinctBy { it.label + it.ext + it.isAudio }
            .sortedWith(compareBy<VideoFormat> { it.isAudio }.thenByDescending { it.label.filter { ch -> ch.isDigit() }.toIntOrNull() ?: 0 })
        val mp3 = VideoFormat("audio:best", "Audio only (MP3)", "mp3", 0, "mp3", true)
        return listOf(best) + unique.filter { !it.isAudio }.take(10) + mp3 + unique.filter { it.isAudio }.take(3)
    }

    fun cancel(processId: String) {
        try { YoutubeDL.getInstance().destroyProcessById(processId) } catch (_: Throwable) {}
    }

    fun download(
        url: String,
        formatId: String,
        dir: File,
        processId: String? = null,
        referer: String = "",
        userAgent: String = "",
        cookie: String = "",
        title: String = "",
        limitBps: Long = 0,
        onProgress: (percent: Float, eta: Long, line: String) -> Unit,
    ): File {
        if (!await()) throw IllegalStateException(initError ?: "yt-dlp is not ready")
        dir.mkdirs()
        // Item 40: name generic streams after the page title instead of "master [master].mp4".
        val nice = safeFileName(title)
        val tmpl = if (!isSitePage(url) && nice.isNotBlank()) File(dir, "$nice.%(ext)s").absolutePath
            else File(dir, "%(title).80B [%(id)s].%(ext)s").absolutePath
        val req = YoutubeDLRequest(url)
        applyHeaders(req, url, referer, userAgent, cookie)
        req.addOption("-o", tmpl)
        req.addOption("--no-playlist")
        req.addOption("--no-mtime")
        req.addOption("--newline")
        req.addOption("--retries", "5")
        req.addOption("--fragment-retries", "10")
        if (limitBps > 0) req.addOption("--limit-rate", "${limitBps / 1024}K")
        if (formatId.startsWith("audio:") || formatId == "bestaudio") {
            // Item 51: audio only, converted to MP3.
            val aid = formatId.removePrefix("audio:")
            req.addOption("-f", if (aid == "best" || aid == "bestaudio" || aid.isBlank()) "ba/b" else "$aid/ba/b")
            req.addOption("-x")
            req.addOption("--audio-format", "mp3")
        } else {
            req.addOption("--merge-output-format", "mp4")
            if (formatId == "best" || formatId.isBlank()) {
                req.addOption("-f", "bv*+ba/b")
            } else if (formatId.startsWith("h:")) {
                // Quality picked in the detected-media sheet (item 112): best stream up to that height.
                val h = formatId.removePrefix("h:").toIntOrNull() ?: 0
                req.addOption("-f", "bv*[height<=$h]+ba/b[height<=$h]/bv*+ba/b")
            } else {
                // Exact id plus best audio (DASH), else the exact id alone (already has audio, e.g. HLS), else best.
                req.addOption("-f", "$formatId+ba/$formatId/bv*+ba/b")
            }
        }
        var lastFile: File? = null
        val started = System.currentTimeMillis()
        useLock.read {
        YoutubeDL.getInstance().execute(req, processId) { progress, etaInSeconds, line ->
            if (line != null) {
                val dest = Regex("Destination: (.+)").find(line)?.groupValues?.getOrNull(1)
                if (dest != null) lastFile = File(dest.trim())
            }
            if (line != null) {
                Regex("Merging formats into \"(.+)\"").find(line)?.groupValues?.getOrNull(1)?.let { lastFile = File(it.trim()) }
                Regex("\\[ExtractAudio\\] Destination: (.+)").find(line)?.groupValues?.getOrNull(1)?.let { lastFile = File(it.trim()) }
                Regex("\\[download\\] (.+) has already been downloaded").find(line)?.groupValues?.getOrNull(1)?.let { lastFile = File(it.trim()) }
            }
            onProgress(progress, etaInSeconds.toLong(), line ?: "")
        }
        }
        if (lastFile != null && lastFile!!.exists()) return lastFile!!
        // pick the newest file in dir as a fallback
        return dir.listFiles()?.filter { it.isFile && it.lastModified() >= started - 2000 }?.maxByOrNull { it.lastModified() }
            ?: throw IllegalStateException("the video was downloaded but the file could not be found")
    }

    fun safeFileName(s: String): String =
        s.replace(Regex("[\\\\/:*?\"<>|\\p{Cntrl}]"), " ").replace(Regex("\\s+"), " ").trim().take(90).trimEnd('.', ' ')

    private fun formatDuration(s: Double): String {
        if (s <= 0) return ""
        val t = s.toInt()
        val m = t / 60
        val sec = t % 60
        return "%d:%02d".format(m, sec)
    }
}
