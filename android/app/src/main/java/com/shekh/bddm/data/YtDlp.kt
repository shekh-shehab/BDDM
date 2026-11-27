package com.shekh.bddm.data

import com.yausername.youtubedl_android.YoutubeDL
import com.yausername.youtubedl_android.YoutubeDLRequest
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

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
)

object YtDlp {
    @Volatile var ready = false
    @Volatile var initError: String? = null

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

    fun probe(url: String): VideoInfo {
        if (!ready) throw IllegalStateException(initError ?: "yt-dlp is not ready")
        val req = YoutubeDLRequest(url)
        req.addOption("-J")
        req.addOption("--no-playlist")
        req.addOption("--no-warnings")
        req.addOption("--skip-download")
        val out = YoutubeDL.getInstance().execute(req, null, null)
        val json = out.out ?: out.err ?: "{}"
        val start = json.indexOf('{')
        val obj = JSONObject(if (start >= 0) json.substring(start) else json)
        val title = obj.optString("title").ifBlank { url }
        val site = obj.optString("extractor_key").ifBlank { obj.optString("extractor") }
        val dur = formatDuration(obj.optDouble("duration", 0.0))
        val thumb = obj.optString("thumbnail")
        val formats = parseFormats(obj.optJSONArray("formats") ?: JSONArray())
        return VideoInfo(url, title, site, dur, thumb, formats)
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
            out.add(VideoFormat(id, label, note, size, ext, isAudio))
        }
        // Prefer a merged "best" option at the top.
        val best = VideoFormat("best", "Best available", if (bestH > 0) "${bestH}p" else "best", 0, "mp4", false)
        val unique = out.distinctBy { it.label + it.ext + it.isAudio }
            .sortedWith(compareBy<VideoFormat> { it.isAudio }.thenByDescending { it.label.filter { ch -> ch.isDigit() }.toIntOrNull() ?: 0 })
        return listOf(best) + unique.take(12)
    }

    fun download(
        url: String,
        formatId: String,
        dir: File,
        onProgress: (percent: Float, eta: Long, line: String) -> Unit,
    ): File {
        if (!ready) throw IllegalStateException(initError ?: "yt-dlp is not ready")
        dir.mkdirs()
        val tmpl = File(dir, "%(title).80B [%(id)s].%(ext)s").absolutePath
        val req = YoutubeDLRequest(url)
        req.addOption("-o", tmpl)
        req.addOption("--no-playlist")
        req.addOption("--no-mtime")
        req.addOption("--merge-output-format", "mp4")
        if (formatId == "best" || formatId.isBlank()) {
            req.addOption("-f", "bv*+ba/b")
        } else if (formatId.contains("audio", true) || formatId == "bestaudio") {
            req.addOption("-f", "ba/bestaudio")
            req.addOption("-x")
            req.addOption("--audio-format", "mp3")
        } else {
            // Try exact id, fall back to that video + best audio (DASH/HLS merge).
            req.addOption("-f", "$formatId+bestaudio/ba/$formatId/b")
        }
        var lastFile: File? = null
        YoutubeDL.getInstance().execute(req, null) { progress, etaInSeconds, line ->
            if (line != null) {
                val dest = Regex("Destination: (.+)").find(line)?.groupValues?.getOrNull(1)
                if (dest != null) lastFile = File(dest.trim())
            }
            onProgress(progress, etaInSeconds.toLong(), line ?: "")
        }
        if (lastFile != null && lastFile!!.exists()) return lastFile!!
        // pick the newest file in dir as a fallback
        return dir.listFiles()?.maxByOrNull { it.lastModified() }
            ?: throw IllegalStateException("the video was downloaded but the file could not be found")
    }

    private fun formatDuration(s: Double): String {
        if (s <= 0) return ""
        val t = s.toInt()
        val m = t / 60
        val sec = t % 60
        return "%d:%02d".format(m, sec)
    }
}
