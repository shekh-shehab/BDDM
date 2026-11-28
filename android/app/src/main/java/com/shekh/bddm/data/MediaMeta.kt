package com.shekh.bddm.data

import java.net.URL

/** HLS playlist parsing (pure, unit-tested). */
object HlsParser {
    fun isMaster(text: String) = text.contains("#EXT-X-STREAM-INF")

    fun resolve(base: String, rel: String): String = try { URL(URL(base), rel.trim()).toString() } catch (_: Exception) { rel.trim() }

    /** Host + path, no query or fragment — tokens in the query must not split one stream into many. */
    fun urlIdentity(url: String): String = try {
        val u = URL(url)
        val path = u.path.trimEnd('/').lowercase()
        val auth = (u.authority ?: u.host ?: "").lowercase()
        "${u.protocol.lowercase()}://$auth$path"
    } catch (_: Exception) {
        url.substringBefore('#').substringBefore('?').trimEnd('/').lowercase()
    }

    private fun pathOf(url: String): String = try { URL(url).path } catch (_: Exception) {
        url.substringBefore('#').substringBefore('?')
    }

    private fun attr(attrs: String, name: String): String? {
        val m = Regex("""(?:^|,)\s*${name}=(?:"([^"]*)"|([^,]*))""", RegexOption.IGNORE_CASE).find(attrs) ?: return null
        return m.groupValues[1].ifBlank { m.groupValues[2] }.trim()
    }

    fun parseMaster(text: String, baseUrl: String): List<MediaVariant> {
        val out = ArrayList<MediaVariant>()
        val lines = text.lines()
        var i = 0
        while (i < lines.size) {
            val l = lines[i].trim()
            if (l.startsWith("#EXT-X-STREAM-INF")) {
                val attrs = l.substringAfter(':')
                val res = Regex("""RESOLUTION=(\d+)x(\d+)""").find(attrs)
                val bw = Regex("""(?:^|,)BANDWIDTH=(\d+)""").find(attrs)?.groupValues?.get(1)?.toLongOrNull()
                    ?: Regex("""(?:^|,)AVERAGE-BANDWIDTH=(\d+)""").find(attrs)?.groupValues?.get(1)?.toLongOrNull()
                    ?: 0
                var j = i + 1
                while (j < lines.size && (lines[j].isBlank() || lines[j].trim().startsWith("#"))) j++
                if (j < lines.size) {
                    out.add(MediaVariant(resolve(baseUrl, lines[j]), res?.groupValues?.get(1)?.toIntOrNull() ?: 0,
                        res?.groupValues?.get(2)?.toIntOrNull() ?: 0, bw, role = "video"))
                    i = j
                }
            }
            i++
        }
        return out.distinctBy { urlIdentity(it.url) }.sortedWith(compareByDescending<MediaVariant> { it.height }.thenByDescending { it.bandwidth })
    }

    /** Audio and subtitle renditions from a master playlist (#EXT-X-MEDIA). */
    fun parseAudioAndSubs(text: String, baseUrl: String): List<MediaVariant> {
        val out = ArrayList<MediaVariant>()
        for (raw in text.lines()) {
            val l = raw.trim()
            if (!l.startsWith("#EXT-X-MEDIA")) continue
            val attrs = l.substringAfter(':')
            val type = attr(attrs, "TYPE")?.uppercase() ?: continue
            val role = when (type) {
                "AUDIO" -> "audio"
                "SUBTITLES", "CLOSED-CAPTIONS" -> "subtitles"
                else -> continue
            }
            val uri = attr(attrs, "URI") ?: continue
            if (uri.isBlank()) continue
            out.add(MediaVariant(resolve(baseUrl, uri), 0, 0, 0, role = role))
        }
        return out.distinctBy { urlIdentity(it.url) }
    }

    fun parseAllVariants(text: String, baseUrl: String): List<MediaVariant> =
        parseMaster(text, baseUrl) + parseAudioAndSubs(text, baseUrl)

    /** Total duration of a media playlist in seconds. */
    fun mediaDuration(text: String): Double =
        Regex("""#EXTINF:\s*([\d.]+)""").findAll(text).sumOf { it.groupValues[1].toDoubleOrNull() ?: 0.0 }

    private val AUX_DIR_OR_FILE = Regex(
        """(?:^|[-_./])(audio(?:[-_]?only)?|aac(?:[-_.][a-z0-9]+)?|audiotrack|soundtrack|subs?(?:title)?s?|captions?|closed[-_]?captions?|text[-_]?stream|vtt)(?:[-_./]|$)""",
        RegexOption.IGNORE_CASE,
    )
    private val QUALITY_DIR = Regex("""^(\d{3,4})p?$""", RegexOption.IGNORE_CASE)
    private val GENERIC_PLAYLIST = setOf(
        "master", "manifest", "index", "playlist", "stream", "play", "live",
        "prog_index", "chunklist", "playlist_full", "main",
    )

    fun isRenditionDir(name: String): Boolean {
        val n = name.lowercase()
        if (n in setOf("audio", "audios", "aac", "subs", "sub", "subtitle", "subtitles",
                "captions", "caption", "text", "tracks", "renditions", "quality", "qualities", "video", "videos")) return true
        if (QUALITY_DIR.matches(n)) return true
        if (n.startsWith("audio") || n.startsWith("aac") || n.startsWith("sub")) return true
        return false
    }

    fun isAudioOrSubtitleUrl(url: String): Boolean = AUX_DIR_OR_FILE.containsMatchIn(pathOf(url).lowercase())

    fun isSubtitlePlaylist(url: String, text: String = ""): Boolean {
        if (Regex("""(?:^|[-_./])(subs?(?:title)?s?|captions?|closed[-_]?captions?|vtt|webvtt)(?:[-_./]|$)""", RegexOption.IGNORE_CASE)
                .containsMatchIn(pathOf(url).lowercase())) return true
        val segs = segmentUris(text)
        return segs.isNotEmpty() && segs.all { u ->
            val e = u.substringBefore('?').substringAfterLast('.').lowercase()
            e in setOf("vtt", "webvtt", "srt", "ttml")
        }
    }

    fun isAudioPlaylist(url: String, text: String = ""): Boolean {
        if (isSubtitlePlaylist(url, text)) return false
        if (isAudioOrSubtitleUrl(url)) return true
        val segs = segmentUris(text)
        return segs.isNotEmpty() && segs.all { u ->
            val e = u.substringBefore('?').substringAfterLast('.').lowercase()
            e in setOf("aac", "m4a", "mp3", "ac3", "eac3", "ogg", "opus")
        }
    }

    private fun segmentUris(text: String): List<String> =
        text.lines().map { it.trim() }.filter { it.isNotEmpty() && !it.startsWith("#") }

    fun looksLikeMasterUrl(url: String): Boolean {
        val path = pathOf(url).trimEnd('/')
        val file = path.substringAfterLast('/').substringBeforeLast('.').lowercase()
        val dir = path.substringBeforeLast('/').substringAfterLast('/')
        if (file in setOf("master", "manifest", "main")) return true
        return file in GENERIC_PLAYLIST && !isRenditionDir(dir)
    }

    /**
     * Key shared by a master playlist, its quality variants, and its audio/subtitle
     * renditions (same video on the page).
     */
    fun familyKey(url: String): String {
        val id = urlIdentity(url)
        val noScheme = id.substringAfter("://")
        val host = noScheme.substringBefore('/')
        val path = "/" + noScheme.substringAfter('/', "")
        val file = path.substringAfterLast('/')
        val dir = path.substringBeforeLast('/').ifBlank { "/" }
        val dirName = dir.substringAfterLast('/')
        if (isRenditionDir(dirName) || isAudioOrSubtitleUrl(url)) {
            val parent = if (isRenditionDir(dirName)) dir.substringBeforeLast('/').ifBlank { "/" } else dir
            return "$host|$parent"
        }
        val stem = file.substringBeforeLast('.').lowercase()
        if (stem.isEmpty() || stem in GENERIC_PLAYLIST) return "$host|$dir"
        val stripped = stem
            .replace(Regex("""(?:^|[-_.])(2160|1440|1080|720|540|480|360|240|144)p?$"""), "")
            .replace(Regex("""(?:^|[-_.])(hd|sd|fhd|uhd|high|low|medium)$"""), "")
            .trim('-', '_', '.')
        if (stripped.isEmpty() || stripped in GENERIC_PLAYLIST) return "$host|$dir"
        return "$host|$dir|$stripped"
    }
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
    /** Every URL that belongs to this video, so removing the card clears variants too. */
    val urls: List<String> = emptyList(),
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
        // Quality is often in a folder (…/1080p/index.m3u8), not only in the file name.
        val path = url.substringBefore('?').substringBefore('#').lowercase()
        val parts = path.split('/').filter { it.isNotEmpty() }.takeLast(3).joinToString("/")
        Regex("""(\d{3,4})x(\d{3,4})""").find(parts)?.let { return it.groupValues[2].toInt() }
        Regex("""(?:^|[^0-9])(2160|1440|1080|720|540|480|360|240|144)p?(?:[^0-9]|$)""").find(parts)?.let { return it.groupValues[1].toInt() }
        return 0
    }

    fun estimatedSize(bandwidth: Long, durationSec: Double, sizeBytes: Long = 0): Long {
        if (sizeBytes > 0) return sizeBytes
        if (bandwidth > 0 && durationSec > 0) return (bandwidth * durationSec / 8.0).toLong()
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
        // Variant / audio / subtitle playlists already listed on a master are hidden.
        val absorbed = HashSet<String>()
        for (m in items) {
            for (v in m.variants) {
                if (HlsParser.urlIdentity(v.url) != HlsParser.urlIdentity(m.url)) {
                    absorbed.add(HlsParser.urlIdentity(v.url))
                }
            }
        }
        val visible = items.filter { HlsParser.urlIdentity(it.url) !in absorbed }

        val out = ArrayList<MediaGroup>()

        // Item 118: all HLS URLs of the same video (master + renditions) become one card.
        val hls = visible.filter { it.kind == MediaKind.HLS }
        val nonHls = visible.filter { it.kind != MediaKind.HLS }
        val families = LinkedHashMap<String, MutableList<DetectedMedia>>()
        for (m in hls) {
            families.getOrPut(HlsParser.familyKey(m.url)) { ArrayList() }.add(m)
        }
        for ((k, list) in families) {
            groupHlsFamily("hls|$k", list)?.let { out.add(it) }
        }

        val groups = LinkedHashMap<String, MutableList<DetectedMedia>>()
        for (m in nonHls) {
            val k = when (m.kind) {
                MediaKind.DIRECT, MediaKind.AUDIO -> groupKey(m.url)
                else -> "u|" + MediaClassifier.key(m.url)
            }
            groups.getOrPut(k) { ArrayList() }.add(m)
        }
        for ((k, list) in groups) {
            toGroup(k, list, hideSmall)?.let { out.add(it) }
        }
        return out
    }

    private fun groupHlsFamily(key: String, list: List<DetectedMedia>): MediaGroup? {
        val aux = list.filter { m ->
            m.rendition == "audio" || m.rendition == "subtitles" ||
                HlsParser.isAudioOrSubtitleUrl(m.url) ||
                HlsParser.isAudioPlaylist(m.url) ||
                HlsParser.isSubtitlePlaylist(m.url)
        }
        val playable = list.filter { it !in aux }
        val members = ArrayList<String>()
        fun addUrl(u: String) { if (u.isNotBlank() && members.none { HlsParser.urlIdentity(it) == HlsParser.urlIdentity(u) }) members.add(u) }
        list.forEach { m ->
            addUrl(m.url)
            m.variants.forEach { addUrl(it.url) }
        }

        if (playable.isEmpty()) {
            val first = aux.firstOrNull() ?: return null
            return MediaGroup(
                key = key,
                title = list.maxByOrNull { it.title.length }?.title ?: first.title,
                kind = MediaKind.AUDIO,
                thumb = list.firstOrNull { it.thumb.isNotBlank() }?.thumb.orEmpty(),
                durationSec = list.maxOf { it.durationSec },
                options = listOf(QualityOption(first, 0, first.sizeBytes, "Audio")),
                at = list.minOf { it.at },
                urls = members,
            )
        }

        val master = playable.maxByOrNull { m -> m.variants.count { it.isVideo } }?.takeIf { it.variants.any { v -> v.isVideo } }
            ?: playable.firstOrNull { HlsParser.looksLikeMasterUrl(it.url) }

        val dur = (master?.durationSec ?: 0.0).let { d -> if (d > 0) d else playable.maxOf { it.durationSec } }
        val options = ArrayList<QualityOption>()
        if (master != null && master.variants.any { it.isVideo }) {
            master.variants.filter { it.isVideo }.forEach { v ->
                val size = estimatedSize(v.bandwidth, dur, v.sizeBytes)
                options.add(QualityOption(master, v.height, size, qualityLabel(v.height).ifBlank { "Auto" }))
            }
        } else {
            val withHeight = playable.map { m -> m to (if (m.height > 0) m.height else heightFromName(m.url)) }
            val heighted = withHeight.filter { it.second > 0 }
            if (heighted.isNotEmpty()) {
                val item = master ?: heighted.maxByOrNull { it.second }!!.first
                heighted.distinctBy { it.second }.sortedByDescending { it.second }.forEach { (m, h) ->
                    val size = if (m.sizeBytes > 0) m.sizeBytes else estimatedSize(0, dur, 0)
                    options.add(QualityOption(if (master != null) master else m, h, size, qualityLabel(h)))
                }
            } else {
                val item = master ?: playable.first()
                options.add(QualityOption(item, 0, item.sizeBytes, "Stream"))
            }
        }
        if (options.isEmpty()) return null
        options.sortWith(compareByDescending<QualityOption> { it.height }.thenByDescending { it.sizeBytes })
        val withThumb = list.firstOrNull { it.thumb.isNotBlank() }
        return MediaGroup(
            key = key,
            title = list.maxByOrNull { it.title.length }?.title ?: playable.first().title,
            kind = MediaKind.HLS,
            thumb = withThumb?.thumb.orEmpty(),
            durationSec = dur,
            options = options,
            at = list.minOf { it.at },
            urls = members,
        )
    }

    private fun toGroup(k: String, list: List<DetectedMedia>, hideSmall: Boolean): MediaGroup? {
        val first = list.first()
        val options = ArrayList<QualityOption>()
        if (first.kind != MediaKind.DIRECT && first.kind != MediaKind.AUDIO && first.variants.isNotEmpty()) {
            val dur = first.durationSec
            first.variants.filter { it.isVideo || it.role.isBlank() }.forEach { v ->
                val size = estimatedSize(v.bandwidth, dur, v.sizeBytes)
                options.add(QualityOption(first, v.height, size, qualityLabel(v.height).ifBlank { "Auto" }))
            }
        } else {
            list.forEach { m ->
                val h = if (m.height > 0) m.height else heightFromName(m.url)
                if (hideSmall && (m.kind == MediaKind.DIRECT || m.kind == MediaKind.AUDIO) && m.sizeBytes in 1 until SMALL_BYTES) return@forEach
                val label = when {
                    m.kind == MediaKind.AUDIO -> "Audio"
                    h > 0 -> qualityLabel(h)
                    m.kind == MediaKind.DASH -> "Stream"
                    m.kind == MediaKind.PAGE || m.kind == MediaKind.EMBED -> "Best"
                    else -> MediaClassifier.extension(m.url).uppercase().ifBlank { "File" }
                }
                options.add(QualityOption(m, h, m.sizeBytes, label))
            }
        }
        if (options.isEmpty()) return null
        options.sortWith(compareByDescending<QualityOption> { it.height }.thenByDescending { it.sizeBytes })
        val withThumb = list.firstOrNull { it.thumb.isNotBlank() }
        return MediaGroup(
            key = k,
            title = list.maxByOrNull { it.title.length }?.title ?: first.title,
            kind = first.kind,
            thumb = withThumb?.thumb.orEmpty(),
            durationSec = list.maxOf { it.durationSec },
            options = options,
            at = list.minOf { it.at },
            urls = list.flatMap { m -> listOf(m.url) + m.variants.map { it.url } }.distinct(),
        )
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
