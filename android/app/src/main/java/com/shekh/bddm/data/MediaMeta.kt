package com.shekh.bddm.data

import java.net.URL

/** HLS playlist parsing (pure, unit-tested). */
object HlsParser {
    fun isMaster(text: String) = text.contains("#EXT-X-STREAM-INF")

    fun resolve(base: String, rel: String): String = try { URL(URL(base), rel.trim()).toString() } catch (_: Exception) { rel.trim() }

    fun parseMaster(text: String, baseUrl: String): List<MediaVariant> {
        val out = ArrayList<MediaVariant>()
        val lines = text.lines()
        var i = 0
        while (i < lines.size) {
            val l = lines[i].trim()
            if (l.startsWith("#EXT-X-STREAM-INF")) {
                val attrs = l.substringAfter(':')
                val res = Regex("""RESOLUTION=(\d+)x(\d+)""").find(attrs)
                val bw = Regex("""(?:^|,)BANDWIDTH=(\d+)""").find(attrs)?.groupValues?.get(1)?.toLongOrNull() ?: 0
                var j = i + 1
                while (j < lines.size && (lines[j].isBlank() || lines[j].trim().startsWith("#"))) j++
                if (j < lines.size) {
                    out.add(MediaVariant(resolve(baseUrl, lines[j]), res?.groupValues?.get(1)?.toIntOrNull() ?: 0,
                        res?.groupValues?.get(2)?.toIntOrNull() ?: 0, bw))
                    i = j
                }
            }
            i++
        }
        return out.distinctBy { it.url }.sortedWith(compareByDescending<MediaVariant> { it.height }.thenByDescending { it.bandwidth })
    }

    /** Total duration of a media playlist in seconds. */
    fun mediaDuration(text: String): Double =
        Regex("""#EXTINF:\s*([\d.]+)""").findAll(text).sumOf { it.groupValues[1].toDoubleOrNull() ?: 0.0 }
}

/** Minimal DASH manifest reading: duration and video representations. */
object DashParser {
    fun duration(text: String): Double {
        val m = Regex("""mediaPresentationDuration="P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?"""").find(text) ?: return 0.0
        val (d, h, mi, s) = m.destructured
        return (d.toDoubleOrNull() ?: 0.0) * 86400 + (h.toDoubleOrNull() ?: 0.0) * 3600 +
            (mi.toDoubleOrNull() ?: 0.0) * 60 + (s.toDoubleOrNull() ?: 0.0)
    }

    fun variants(text: String, mpdUrl: String): List<MediaVariant> =
        Regex("""<Representation\b[^>]*>""").findAll(text).mapNotNull { r ->
            val tag = r.value
            val h = Regex("""\bheight="(\d+)"""").find(tag)?.groupValues?.get(1)?.toIntOrNull() ?: return@mapNotNull null
            val w = Regex("""\bwidth="(\d+)"""").find(tag)?.groupValues?.get(1)?.toIntOrNull() ?: 0
            val bw = Regex("""\bbandwidth="(\d+)"""").find(tag)?.groupValues?.get(1)?.toLongOrNull() ?: 0
            MediaVariant(mpdUrl, w, h, bw)
        }.toList().distinctBy { it.height }.sortedByDescending { it.height }
}

/** One choice inside a group: a file, or one height of a stream. */
data class QualityOption(val item: DetectedMedia, val height: Int, val sizeBytes: Long, val label: String)

/** One video as the user sees it: variants of the same media grouped together. */
data class MediaGroup(
    val key: String,
    val title: String,
    val kind: MediaKind,
    val thumb: String,
    val durationSec: Double,
    val options: List<QualityOption>,
    val at: Long,
) {
    val best: QualityOption get() = options.first()
}

enum class MediaSort { QUALITY, SIZE, NAME, NEWEST }
enum class MediaFilter { ALL, VIDEO, AUDIO, STREAMS }

object MediaGrouping {
    const val SMALL_BYTES = 512L * 1024

    fun qualityLabel(h: Int): String = when {
        h >= 2160 -> "4K"
        h >= 1440 -> "1440p"
        h > 0 -> "${h}p"
        else -> ""
    }

    fun heightFromName(url: String): Int {
        val name = url.substringBefore('?').substringAfterLast('/').lowercase()
        Regex("""(\d{3,4})x(\d{3,4})""").find(name)?.let { return it.groupValues[2].toInt() }
        Regex("""(?:^|[^0-9])(2160|1440|1080|720|540|480|360|240|144)p?(?:[^0-9]|$)""").find(name)?.let { return it.groupValues[1].toInt() }
        return 0
    }

    /** Same video in other resolutions maps to the same key (e.g. clip_720p.mp4 / clip_1080p.mp4). */
    fun groupKey(url: String): String {
        val host = MediaClassifier.hostOf(url) ?: ""
        val path = url.substringBefore('?').substringBefore('#')
        val dir = path.substringBeforeLast('/')
        val name = path.substringAfterLast('/').lowercase()
        val stem = name.substringBeforeLast('.')
            .replace(Regex("""\d{3,4}x\d{3,4}"""), "")
            .replace(Regex("""(?:^|[-_. ])(2160|1440|1080|720|540|480|360|240|144)p?(?=$|[-_. ])"""), "")
            .replace(Regex("""(?:^|[-_. ])(hd|sd|fhd|uhd|high|low|medium)(?=$|[-_. ])"""), "")
            .trim('-', '_', '.', ' ')
        return host + "|" + dir.substringAfter("://").substringAfter('/') + "|" + stem + "|" + MediaClassifier.extension(url)
    }

    fun group(items: List<DetectedMedia>, hideSmall: Boolean): List<MediaGroup> {
        // Variant playlists already listed inside a master playlist are not shown again.
        val absorbed = items.flatMap { m -> m.variants.filter { it.url != m.url }.map { MediaClassifier.key(it.url) } }.toSet()
        val visible = items.filter { MediaClassifier.key(it.url) !in absorbed }
        val groups = LinkedHashMap<String, MutableList<DetectedMedia>>()
        for (m in visible) {
            val k = when (m.kind) {
                MediaKind.DIRECT, MediaKind.AUDIO -> groupKey(m.url)
                else -> "u|" + MediaClassifier.key(m.url)
            }
            groups.getOrPut(k) { ArrayList() }.add(m)
        }
        val out = ArrayList<MediaGroup>()
        for ((k, list) in groups) {
            val first = list.first()
            val options = ArrayList<QualityOption>()
            if (first.kind != MediaKind.DIRECT && first.kind != MediaKind.AUDIO && first.variants.isNotEmpty()) {
                val dur = first.durationSec
                first.variants.forEach { v ->
                    val size = if (v.sizeBytes > 0) v.sizeBytes else if (v.bandwidth > 0 && dur > 0) (v.bandwidth * dur / 8).toLong() else 0
                    options.add(QualityOption(first, v.height, size, qualityLabel(v.height).ifBlank { "Auto" }))
                }
            } else {
                list.forEach { m ->
                    val h = if (m.height > 0) m.height else heightFromName(m.url)
                    if (hideSmall && (m.kind == MediaKind.DIRECT || m.kind == MediaKind.AUDIO) && m.sizeBytes in 1 until SMALL_BYTES) return@forEach
                    val label = when {
                        m.kind == MediaKind.AUDIO -> "Audio"
                        h > 0 -> qualityLabel(h)
                        m.kind == MediaKind.HLS || m.kind == MediaKind.DASH -> "Stream"
                        m.kind == MediaKind.PAGE || m.kind == MediaKind.EMBED -> "Best"
                        else -> MediaClassifier.extension(m.url).uppercase().ifBlank { "File" }
                    }
                    options.add(QualityOption(m, h, m.sizeBytes, label))
                }
            }
            if (options.isEmpty()) continue
            options.sortWith(compareByDescending<QualityOption> { it.height }.thenByDescending { it.sizeBytes })
            val withThumb = list.firstOrNull { it.thumb.isNotBlank() }
            out.add(MediaGroup(
                key = k,
                title = list.maxByOrNull { it.title.length }?.title ?: first.title,
                kind = first.kind,
                thumb = withThumb?.thumb.orEmpty(),
                durationSec = list.maxOf { it.durationSec },
                options = options,
                at = list.minOf { it.at },
            ))
        }
        return out
    }

    fun filter(groups: List<MediaGroup>, f: MediaFilter): List<MediaGroup> = when (f) {
        MediaFilter.ALL -> groups
        MediaFilter.AUDIO -> groups.filter { it.kind == MediaKind.AUDIO }
        MediaFilter.STREAMS -> groups.filter { it.kind == MediaKind.HLS || it.kind == MediaKind.DASH }
        MediaFilter.VIDEO -> groups.filter { it.kind != MediaKind.AUDIO }
    }

    fun sort(groups: List<MediaGroup>, s: MediaSort): List<MediaGroup> = when (s) {
        MediaSort.QUALITY -> groups.sortedWith(compareByDescending<MediaGroup> { it.best.height }.thenByDescending { it.best.sizeBytes })
        MediaSort.SIZE -> groups.sortedByDescending { it.best.sizeBytes }
        MediaSort.NAME -> groups.sortedBy { it.title.lowercase() }
        MediaSort.NEWEST -> groups.sortedByDescending { it.at }
    }

    /** Item 40: "Page title 720p.mp4" instead of "index.mp4" or a random hash. */
    fun fileNameFor(title: String, url: String, height: Int): String {
        val ext = MediaClassifier.extension(url).ifBlank { "mp4" }
        val urlName = url.substringBefore('?').substringAfterLast('/')
        val base = YtDlpNames.safe(title).let { t ->
            if (t.isBlank() || t.equals(urlName, true) || t.startsWith("http")) YtDlpNames.safe(urlName.substringBeforeLast('.')) else t
        }.ifBlank { "video" }
        val q = if (height > 0 && !base.contains("${height}p")) " ${qualityLabel(height)}" else ""
        return "$base$q.$ext"
    }

    fun formatDuration(sec: Double): String {
        if (sec <= 0) return ""
        val t = sec.toLong()
        val h = t / 3600
        val m = (t % 3600) / 60
        val s = t % 60
        return if (h > 0) "%d:%02d:%02d".format(h, m, s) else "%d:%02d".format(m, s)
    }
}

/** File-name cleaning shared by the engine and yt-dlp paths. */
object YtDlpNames {
    fun safe(s: String): String =
        s.replace(Regex("[\\\\/:*?\"<>|\\p{Cntrl}]"), " ").replace(Regex("\\s+"), " ").trim().take(90).trimEnd('.', ' ')
}
