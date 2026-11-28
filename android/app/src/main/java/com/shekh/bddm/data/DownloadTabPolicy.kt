package com.shekh.bddm.data

/**
 * Bug 120: a tab that another page opened only to start a download should close
 * by itself, so the user lands back on the original page with the form and
 * scroll position still there.
 */
object DownloadTabPolicy {

    fun isBlankUrl(url: String?): Boolean {
        val u = url?.trim().orEmpty()
        return u.isEmpty() ||
            u.equals("about:blank", true) ||
            u.equals("about:srcdoc", true)
    }

    private val DOWNLOAD_EXT = setOf(
        "7z", "apk", "avi", "bin", "bz2", "csv", "deb", "dmg", "docx", "exe",
        "gz", "img", "iso", "jar", "mkv", "mov", "mp3", "mp4", "msi", "pdf",
        "pptx", "rar", "rpm", "tar", "tgz", "webm", "webp", "xlsx", "xz", "zip",
    )

    /** True when the URL itself looks like a file, not a web page. */
    fun isLikelyDownloadUrl(url: String?): Boolean {
        val path = url?.substringBefore('#')?.substringBefore('?')?.substringAfterLast('/') ?: return false
        val ext = path.substringAfterLast('.', "").lowercase()
        return ext.isNotEmpty() && ext in DOWNLOAD_EXT
    }

    /**
     * Close only popups opened by another tab that never became a real page
     * (the main document turned into a download, or the tab is still blank).
     * Tabs the user opened or navigated around in are left alone.
     */
    fun shouldCloseAfterDownload(
        openerId: String?,
        openedAsPopup: Boolean,
        hasRealContent: Boolean,
        userNavigated: Boolean,
        url: String = "",
        mainFrameBecameDownload: Boolean = false,
    ): Boolean {
        if (openerId.isNullOrBlank()) return false
        if (!openedAsPopup) return false
        if (userNavigated) return false
        if (hasRealContent) return false
        // Invoked only after a download was captured from this tab.
        // url / mainFrameBecameDownload are kept so call sites can document the reason.
        return true
    }

    /** Which tab to show after closing [closedId]. Prefers [selectId] (the opener) when leaving the closed tab. */
    fun tabToShowAfterClose(ids: List<String>, currentId: String?, closedId: String, selectId: String?): String? {
        val left = ids.filter { it != closedId }
        if (left.isEmpty()) return null
        val closingCurrent = currentId == closedId
        return when {
            closingCurrent && selectId != null && left.contains(selectId) -> selectId
            closingCurrent -> left.last()
            currentId != null && left.contains(currentId) -> currentId
            else -> left.last()
        }
    }
}
