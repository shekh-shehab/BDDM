package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Before
import org.junit.Test

class MediaDetectorTest {
    @Before
    fun reset() { MediaDetector.clear("t1"); MediaDetector.clear("t2") }

    @Test
    fun classifiesDirectFilesAndStreams() {
        assertEquals(MediaKind.DIRECT, MediaClassifier.classify("https://cdn.example.com/v/movie.mp4?token=abc"))
        assertEquals(MediaKind.DIRECT, MediaClassifier.classify("https://cdn.example.com/v/clip.webm"))
        assertEquals(MediaKind.HLS, MediaClassifier.classify("https://cdn.example.com/hls/master.m3u8?x=1"))
        assertEquals(MediaKind.DASH, MediaClassifier.classify("https://cdn.example.com/dash/manifest.mpd"))
        assertEquals(MediaKind.AUDIO, MediaClassifier.classify("https://cdn.example.com/a/song.m4a"))
    }

    @Test
    fun usesContentTypeWhenExtensionIsMissing() {
        assertEquals(MediaKind.HLS, MediaClassifier.classify("https://x.example/stream/123", "application/vnd.apple.mpegurl"))
        assertEquals(MediaKind.DASH, MediaClassifier.classify("https://x.example/stream/123", "application/dash+xml"))
        assertEquals(MediaKind.DIRECT, MediaClassifier.classify("https://x.example/get/123", "video/mp4"))
    }

    @Test
    fun ignoresSegmentsAndNonMedia() {
        assertNull(MediaClassifier.classify("https://cdn.example.com/hls/seg-12.ts"))
        assertNull(MediaClassifier.classify("https://cdn.example.com/dash/chunk-0001.m4s"))
        assertNull(MediaClassifier.classify("https://cdn.example.com/v/seg-3-v1-a1.mp4"))
        assertNull(MediaClassifier.classify("https://cdn.example.com/v/init.mp4"))
        assertNull(MediaClassifier.classify("https://rr1---sn-x.googlevideo.com/videoplayback?expire=1&mime=video%2Fmp4&range=0-999"))
        assertNull(MediaClassifier.classify("https://example.com/index.html"))
        assertNull(MediaClassifier.classify("blob:https://example.com/1234"))
        assertNull(MediaClassifier.classify("javascript:void(0)"))
    }

    @Test
    fun embedPlayerDetection() {
        assertTrue(MediaClassifier.isEmbedPlayer("https://www.youtube.com/embed/abc"))
        assertTrue(MediaClassifier.isEmbedPlayer("https://player.vimeo.com/video/123"))
        assertFalse(MediaClassifier.isEmbedPlayer("https://ads.example.com/embed/ad"))
    }

    @Test
    fun keyIgnoresCacheBustersAndFragments() {
        assertEquals(
            MediaClassifier.key("https://a.example/v.mp4?id=5&_=111#t=3"),
            MediaClassifier.key("https://a.example/v.mp4?id=5&_=222"),
        )
        assertNotEquals(MediaClassifier.key("https://a.example/v.mp4?id=5"), MediaClassifier.key("https://a.example/v.mp4?id=6"))
    }

    @Test
    fun accumulatesPerTabAndDedupes() {
        assertTrue(MediaDetector.offer("t1", "https://a.example/hls/master.m3u8", "https://a.example/p", "My video", "network"))
        assertTrue(MediaDetector.offer("t1", "https://a.example/v.mp4", "https://a.example/p", "My video", "xhr"))
        assertFalse(MediaDetector.offer("t1", "https://a.example/v.mp4?_=9", "https://a.example/p", "My video", "js"))
        assertFalse(MediaDetector.offer("t1", "https://a.example/hls/seg-1.ts", "https://a.example/p", "My video", "network"))
        MediaDetector.offer("t2", "https://b.example/clip.webm", "https://b.example/p", "", "network")
        val t1 = MediaDetector.forTab("t1")
        assertEquals(2, t1.size)
        // direct files rank before streams
        assertEquals(MediaKind.DIRECT, t1[0].kind)
        assertEquals(MediaKind.HLS, t1[1].kind)
        assertEquals(1, MediaDetector.forTab("t2").size)
    }

    @Test
    fun pageFallbackSortsAfterRealMedia() {
        MediaDetector.add("t1", DetectedMedia("https://site.example/watch?v=1", MediaKind.PAGE, "Page", "https://site.example/watch?v=1", "page"))
        MediaDetector.offer("t1", "https://cdn.example/x.mp4", "https://site.example/watch?v=1", "Page", "network")
        val l = MediaDetector.forTab("t1")
        assertEquals(MediaKind.DIRECT, l.first().kind)
        assertEquals(MediaKind.PAGE, l.last().kind)
    }

    @Test
    fun clearOnlyAffectsThatTab() {
        MediaDetector.offer("t1", "https://a.example/v.mp4", "", "", "network")
        MediaDetector.offer("t2", "https://b.example/v.mp4", "", "", "network")
        MediaDetector.clear("t1")
        assertTrue(MediaDetector.forTab("t1").isEmpty())
        assertEquals(1, MediaDetector.forTab("t2").size)
    }

    @Test
    fun titleUpdatesFromFileNameToPageTitle() {
        MediaDetector.offer("t1", "https://a.example/files/abc123.mp4", "", "", "network")
        MediaDetector.updateTitle("t1", "Funny cat video")
        assertEquals("Funny cat video", MediaDetector.forTab("t1")[0].title)
    }
}
