package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Test

class MediaMetaTest {
    private val master = """
        #EXTM3U
        #EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=854x480,CODECS="avc1"
        480/index.m3u8
        #EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
        https://cdn.test/v/1080/index.m3u8
        #EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720
        720/index.m3u8
    """.trimIndent()

    @Test
    fun hlsMasterVariantsAreResolvedAndSortedBestFirst() {
        assertTrue(HlsParser.isMaster(master))
        val v = HlsParser.parseMaster(master, "https://cdn.test/v/master.m3u8")
        assertEquals(listOf(1080, 720, 480), v.map { it.height })
        assertEquals("https://cdn.test/v/720/index.m3u8", v[1].url)
        assertEquals(2_500_000L, v[1].bandwidth)
    }

    @Test
    fun hlsMediaPlaylistDuration() {
        val media = "#EXTM3U\n#EXTINF:10.0,\na.ts\n#EXTINF:9.5,\nb.ts\n#EXTINF:4,\nc.ts\n#EXT-X-ENDLIST"
        assertFalse(HlsParser.isMaster(media))
        assertEquals(23.5, HlsParser.mediaDuration(media), 0.001)
    }

    @Test
    fun dashDurationAndRepresentations() {
        val mpd = """<MPD mediaPresentationDuration="PT1H2M3.5S"><AdaptationSet>
            <Representation id="1" bandwidth="3000000" width="1280" height="720"/>
            <Representation id="2" bandwidth="6000000" width="1920" height="1080"/>
            <Representation id="a" bandwidth="128000" audioSamplingRate="48000"/></AdaptationSet></MPD>"""
        assertEquals(3723.5, DashParser.duration(mpd), 0.001)
        assertEquals(listOf(1080, 720), DashParser.variants(mpd, "https://x/m.mpd").map { it.height })
    }

    private fun dm(url: String, kind: MediaKind = MediaKind.DIRECT, size: Long = 0, title: String = "Clip", h: Int = 0, at: Long = 1) =
        DetectedMedia(url, kind, title, "https://page.test/", "network", at = at, sizeBytes = size, height = h)

    @Test
    fun sameVideoInSeveralResolutionsIsOneGroupWithBestFirst() {
        val g = MediaGrouping.group(listOf(
            dm("https://cdn.test/v/clip_720p.mp4", size = 50_000_000),
            dm("https://cdn.test/v/clip_1080p.mp4", size = 90_000_000),
            dm("https://cdn.test/v/clip_480p.mp4", size = 20_000_000),
            dm("https://cdn.test/v/other.mp4", size = 5_000_000),
        ), hideSmall = true)
        assertEquals(2, g.size)
        val clip = g.first { it.options.size == 3 }
        assertEquals(1080, clip.best.height)
        assertEquals("1080p", clip.best.label)
    }

    @Test
    fun masterAbsorbsItsVariantPlaylistsAndEstimatesSize() {
        val m = dm("https://cdn.test/v/master.m3u8", MediaKind.HLS).copy(
            durationSec = 100.0,
            variants = listOf(
                MediaVariant("https://cdn.test/v/1080/index.m3u8", 1920, 1080, 8_000_000),
                MediaVariant("https://cdn.test/v/720/index.m3u8", 1280, 720, 4_000_000),
            ),
        )
        val g = MediaGrouping.group(listOf(m, dm("https://cdn.test/v/720/index.m3u8", MediaKind.HLS)), hideSmall = true)
        assertEquals(1, g.size)
        assertEquals(listOf("1080p", "720p"), g[0].options.map { it.label })
        assertEquals(100_000_000L, g[0].best.sizeBytes)
    }

    @Test
    fun tinyFilesAreHiddenUnlessAskedFor() {
        val items = listOf(dm("https://cdn.test/a/thumbclip.mp4", size = 40_000), dm("https://cdn.test/a/movie.mp4", size = 80_000_000))
        assertEquals(1, MediaGrouping.group(items, hideSmall = true).size)
        assertEquals(2, MediaGrouping.group(items, hideSmall = false).size)
    }

    @Test
    fun sortAndFilter() {
        val g = MediaGrouping.group(listOf(
            dm("https://a.test/x/song.mp3", MediaKind.AUDIO, size = 4_000_000, title = "B song", at = 3),
            dm("https://a.test/x/movie_720.mp4", size = 70_000_000, title = "A movie", at = 2),
            dm("https://a.test/x/live.m3u8", MediaKind.HLS, title = "C live", at = 1),
        ), hideSmall = true)
        assertEquals(listOf("A movie", "B song", "C live"), MediaGrouping.sort(g, MediaSort.NAME).map { it.title })
        assertEquals("A movie", MediaGrouping.sort(g, MediaSort.SIZE).first().title)
        assertEquals("B song", MediaGrouping.sort(g, MediaSort.NEWEST).first().title)
        assertEquals(1, MediaGrouping.filter(g, MediaFilter.AUDIO).size)
        assertEquals(1, MediaGrouping.filter(g, MediaFilter.STREAMS).size)
        assertEquals(2, MediaGrouping.filter(g, MediaFilter.VIDEO).size)
    }

    @Test
    fun heightFromFileNames() {
        assertEquals(1080, MediaGrouping.heightFromName("https://x/v_1080p.mp4"))
        assertEquals(720, MediaGrouping.heightFromName("https://x/1280x720/clip.mp4".substringAfterLast("/").let { "https://x/clip-1280x720.mp4" }))
        assertEquals(0, MediaGrouping.heightFromName("https://x/movie2024.mp4"))
    }

    @Test
    fun fileNamesComeFromThePageTitle() {
        assertEquals("My Holiday Video 720p.mp4", MediaGrouping.fileNameFor("My Holiday: Video?", "https://cdn/x/index.mp4?sig=1", 720))
        assertEquals("abc123.mp4", MediaGrouping.fileNameFor("", "https://cdn/x/abc123.mp4", 0))
        assertEquals("Song.mp3", MediaGrouping.fileNameFor("Song", "https://cdn/x/a.mp3", 0))
        assertEquals("1:02:03", MediaGrouping.formatDuration(3723.0))
        assertEquals("0:45", MediaGrouping.formatDuration(45.2))
    }
}
