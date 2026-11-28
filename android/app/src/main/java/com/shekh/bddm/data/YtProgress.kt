package com.shekh.bddm.data

/** Progress parsed from one yt-dlp output line (bug 110: speed and size were never shown). */
data class YtProgress(val percent: Float, val totalBytes: Long, val speedBps: Double, val etaSec: Long)

object YtProgressParser {
    private val re = Regex(
        """\[download\]\s+([\d.]+)%(?:\s+of\s+~?\s*([\d.]+)\s*([KMGT]?i?B))?(?:\s+at\s+([\d.]+)\s*([KMGT]?i?B)/s)?(?:\s+ETA\s+([\d:]+))?"""
    )

    fun parse(line: String): YtProgress? {
        val m = re.find(line) ?: return null
        val pct = m.groupValues[1].toFloatOrNull() ?: return null
        val total = bytes(m.groupValues[2], m.groupValues[3])
        val speed = bytes(m.groupValues[4], m.groupValues[5]).toDouble()
        val eta = etaSeconds(m.groupValues[6])
        return YtProgress(pct.coerceIn(0f, 100f), total, speed, eta)
    }

    fun bytes(num: String, unit: String): Long {
        val n = num.toDoubleOrNull() ?: return 0
        val mul = when (unit.uppercase().replace("I", "")) {
            "B" -> 1.0
            "KB" -> 1024.0
            "MB" -> 1024.0 * 1024
            "GB" -> 1024.0 * 1024 * 1024
            "TB" -> 1024.0 * 1024 * 1024 * 1024
            else -> return 0
        }
        return (n * mul).toLong()
    }

    fun etaSeconds(s: String): Long {
        if (s.isBlank()) return -1
        var total = 0L
        for (p in s.split(':')) total = total * 60 + (p.toLongOrNull() ?: return -1)
        return total
    }
}
