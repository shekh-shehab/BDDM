package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Test

class DownloadTabPolicyTest {

    @Test
    fun closesDownloadOnlyPopup() {
        assertTrue(
            DownloadTabPolicy.shouldCloseAfterDownload(
                openerId = "parent",
                openedAsPopup = true,
                hasRealContent = false,
                userNavigated = false,
                url = "https://example.com/ubuntu.iso",
                mainFrameBecameDownload = true,
            ),
        )
    }

    @Test
    fun closesStillBlankPopupAfterDownload() {
        assertTrue(
            DownloadTabPolicy.shouldCloseAfterDownload(
                openerId = "parent",
                openedAsPopup = true,
                hasRealContent = false,
                userNavigated = false,
                url = "about:blank",
                mainFrameBecameDownload = true,
            ),
        )
    }

    @Test
    fun keepsTabTheUserOpened() {
        assertFalse(
            DownloadTabPolicy.shouldCloseAfterDownload(
                openerId = null,
                openedAsPopup = false,
                hasRealContent = false,
                userNavigated = false,
                url = "https://example.com/file.zip",
                mainFrameBecameDownload = true,
            ),
        )
    }

    @Test
    fun keepsPopupThatLoadedARealPage() {
        assertFalse(
            DownloadTabPolicy.shouldCloseAfterDownload(
                openerId = "parent",
                openedAsPopup = true,
                hasRealContent = true,
                userNavigated = false,
                url = "https://example.com/docs",
                mainFrameBecameDownload = true,
            ),
        )
    }

    @Test
    fun keepsPopupAfterUserNavigated() {
        assertFalse(
            DownloadTabPolicy.shouldCloseAfterDownload(
                openerId = "parent",
                openedAsPopup = true,
                hasRealContent = false,
                userNavigated = true,
                url = "https://example.com/",
                mainFrameBecameDownload = true,
            ),
        )
    }

    @Test
    fun blankUrlHelpers() {
        assertTrue(DownloadTabPolicy.isBlankUrl(""))
        assertTrue(DownloadTabPolicy.isBlankUrl("about:blank"))
        assertTrue(DownloadTabPolicy.isBlankUrl(" about:srcdoc "))
        assertFalse(DownloadTabPolicy.isBlankUrl("https://example.com"))
        assertTrue(DownloadTabPolicy.isLikelyDownloadUrl("https://cdimage.ubuntu.com/ubuntu.iso?token=1"))
        assertFalse(DownloadTabPolicy.isLikelyDownloadUrl("https://example.com/index.html"))
    }

    @Test
    fun returnsToOpenerAfterClosingPopup() {
        val ids = listOf("a", "b", "c")
        assertEquals("a", DownloadTabPolicy.tabToShowAfterClose(ids, currentId = "c", closedId = "c", selectId = "a"))
        assertEquals("b", DownloadTabPolicy.tabToShowAfterClose(ids, currentId = "b", closedId = "c", selectId = "a"))
        assertEquals("b", DownloadTabPolicy.tabToShowAfterClose(ids, currentId = "c", closedId = "c", selectId = null))
        assertNull(DownloadTabPolicy.tabToShowAfterClose(listOf("only"), currentId = "only", closedId = "only", selectId = "a"))
    }
}
