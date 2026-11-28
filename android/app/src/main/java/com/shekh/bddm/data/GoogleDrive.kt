package com.shekh.bddm.data

/** Item 52: turn Google Drive share links into direct download links. */
object GoogleDrive {
    private val idPatterns = listOf(
        Regex("""drive\.google\.com/file/d/([A-Za-z0-9_-]{10,})"""),
        Regex("""drive\.google\.com/(?:open|uc)\?(?:.*&)?id=([A-Za-z0-9_-]{10,})"""),
        Regex("""docs\.google\.com/uc\?(?:.*&)?id=([A-Za-z0-9_-]{10,})"""),
        Regex("""drive\.usercontent\.google\.com/(?:download|uc)\?(?:.*&)?id=([A-Za-z0-9_-]{10,})"""),
    )

    fun fileId(url: String): String? = idPatterns.firstNotNullOfOrNull { it.find(url)?.groupValues?.get(1) }

    fun isDriveFile(url: String) = fileId(url) != null

    /** Direct link that skips the "can't scan for viruses" page for large files. */
    fun directUrl(url: String): String {
        val id = fileId(url) ?: return url
        return "https://drive.usercontent.google.com/download?id=$id&export=download&confirm=t"
    }
}
