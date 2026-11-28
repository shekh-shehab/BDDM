package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Test

class YtProgressTest {
    @Test
    fun parsesFullLine() {
        val p = YtProgressParser.parse("[download]  12.5% of   40.00MiB at    2.00MiB/s ETA 00:17")!!
        assertEquals(12.5f, p.percent, 0.01f)
        assertEquals(40L * 1024 * 1024, p.totalBytes)
        assertEquals(2.0 * 1024 * 1024, p.speedBps, 1.0)
        assertEquals(17L, p.etaSec)
    }

    @Test
    fun parsesApproxSizeAndFragments() {
        val p = YtProgressParser.parse("[download]  50.0% of ~ 1.20GiB at  512.00KiB/s ETA 1:02:03 (frag 10/20)")!!
        assertEquals(50f, p.percent, 0.01f)
        assertTrue(p.totalBytes > 1_200_000_000L)
        assertEquals(512.0 * 1024, p.speedBps, 1.0)
        assertEquals(3723L, p.etaSec)
    }

    @Test
    fun parsesPartialLinesAndIgnoresOthers() {
        val p = YtProgressParser.parse("[download] 100% of 3.50MiB in 00:02")!!
        assertEquals(100f, p.percent, 0.01f)
        assertEquals(0.0, p.speedBps, 0.0)
        assertNull(YtProgressParser.parse("[youtube] abc: Downloading webpage"))
        assertNull(YtProgressParser.parse("[download] Destination: x.mp4"))
    }
}

class NetworkAndDriveTest {
    @Test
    fun networkErrorsAreRetriedButNotPermissionErrors() {
        assertTrue(RetryPolicy.isNetworkError("could not connect: dial tcp: lookup x: no such host"))
        assertTrue(RetryPolicy.isNetworkError("read tcp 1.2.3.4: connection reset by peer"))
        assertTrue(RetryPolicy.isNetworkError("unexpected EOF"))
        assertTrue(RetryPolicy.isNetworkError("the server returned HTTP 503"))
        assertFalse(RetryPolicy.isNetworkError("the server returned HTTP 403"))
        assertFalse(RetryPolicy.isNetworkError("the server returned HTTP 404"))
        assertFalse(RetryPolicy.isNetworkError(""))
        assertTrue(RetryPolicy.backoffMs(0) < RetryPolicy.backoffMs(3))
    }

    @Test
    fun googleDriveLinksBecomeDirect() {
        val d = "https://drive.usercontent.google.com/download?id=1AbCdEfGhIjKlMn&export=download&confirm=t"
        assertEquals(d, GoogleDrive.directUrl("https://drive.google.com/file/d/1AbCdEfGhIjKlMn/view?usp=sharing"))
        assertEquals(d, GoogleDrive.directUrl("https://drive.google.com/open?id=1AbCdEfGhIjKlMn"))
        assertEquals(d, GoogleDrive.directUrl("https://drive.google.com/uc?export=download&id=1AbCdEfGhIjKlMn"))
        assertEquals("https://example.com/a.zip", GoogleDrive.directUrl("https://example.com/a.zip"))
        assertFalse(GoogleDrive.isDriveFile("https://docs.google.com/document/d/abc/edit"))
    }
}
