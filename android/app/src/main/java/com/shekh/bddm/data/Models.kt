package com.shekh.bddm.data

import org.json.JSONObject

data class Seg(val w: Long, val done: Long, val state: String)
data class Segs(val kind: String, val segs: List<Seg>, val total: Int, val finished: Int, val active: Int, val retrying: Int)
data class Job(
    val id: String,
    val url: String,
    val name: String,
    val dir: String,
    val size: Long,
    val done: Long,
    val speed: Double,
    val eta: Long,
    val status: String,
    val error: String,
    val message: String,
    val started: String,
    val finished: String,
    val kind: String,
    val segs: Segs?,
)
data class Snap(
    val version: String,
    val jobs: List<Job>,
    val speed: Double,
    val eta: Long,
    val upload: Double,
    val active: Int,
    val queued: Int,
    val done: Int,
    val error: Int,
)

fun parseSnap(raw: String): Snap {
    val o = try { JSONObject(raw) } catch (_: Exception) { return Snap("1.6.1", emptyList(), 0.0, -1, 0.0, 0, 0, 0, 0) }
    val arr = o.optJSONArray("jobs")
    val jobs = mutableListOf<Job>()
    if (arr != null) for (i in 0 until arr.length()) {
        val j = arr.optJSONObject(i) ?: continue
        jobs.add(Job(
            id = j.optString("id"),
            url = j.optString("url"),
            name = j.optString("name"),
            dir = j.optString("dir"),
            size = j.optLong("size"),
            done = j.optLong("done"),
            speed = j.optDouble("speed"),
            eta = j.optLong("eta", -1),
            status = j.optString("status"),
            error = j.optString("error"),
            message = j.optString("message"),
            started = j.optString("started"),
            finished = j.optString("finished"),
            kind = j.optString("kind"),
            segs = parseSegs(j.optJSONObject("segs")),
        ))
    }
    return Snap(
        version = o.optString("version", "1.6.1"),
        jobs = jobs,
        speed = o.optDouble("speed"),
        eta = o.optLong("eta", -1),
        upload = o.optDouble("upload"),
        active = o.optInt("active"),
        queued = o.optInt("queued"),
        done = o.optInt("done"),
        error = o.optInt("error"),
    )
}

private fun parseSegs(o: JSONObject?): Segs? {
    if (o == null) return null
    val arr = o.optJSONArray("segs") ?: return null
    val segs = (0 until arr.length()).mapNotNull { i ->
        val s = arr.optJSONObject(i) ?: return@mapNotNull null
        Seg(s.optLong("w"), s.optLong("d"), s.optString("s"))
    }
    return Segs(o.optString("kind"), segs, o.optInt("total"), o.optInt("finished"), o.optInt("active"), o.optInt("retrying"))
}

fun formatBytes(n: Long): String {
    if (n < 0) return "?"
    if (n < 1024) return "$n B"
    val kb = n / 1024.0
    if (kb < 1024) return "%.1f KB".format(kb)
    val mb = kb / 1024.0
    if (mb < 1024) return "%.1f MB".format(mb)
    return "%.2f GB".format(mb / 1024.0)
}

fun formatSpeed(bps: Double): String {
    if (bps <= 0) return "0 B/s"
    return formatBytes(bps.toLong()) + "/s"
}

fun formatEta(sec: Long): String {
    if (sec < 0) return "—"
    if (sec < 60) return "${sec}s"
    val m = sec / 60
    val s = sec % 60
    if (m < 60) return if (s == 0L) "${m}m" else "${m}m ${s}s"
    val h = m / 60
    return "${h}h ${m % 60}m"
}
