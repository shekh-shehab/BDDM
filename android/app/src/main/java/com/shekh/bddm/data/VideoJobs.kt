package com.shekh.bddm.data

import android.content.Context
import android.content.SharedPreferences
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONArray
import org.json.JSONObject

/** A video download run by yt-dlp, shown in the Downloads list (the Go engine only knows plain files). */
data class VideoJob(
    val id: String,
    val url: String,
    val title: String,
    val status: String = "Queued", // Queued | Downloading | Done | Error | Cancelled
    val percent: Float = 0f,
    val eta: Long = 0,
    val message: String = "",
    val file: String = "",
    val referer: String = "",
    val format: String = "best",
    val started: Long = System.currentTimeMillis(),
    val speed: Double = 0.0,      // bytes/s
    val total: Long = 0,          // bytes, 0 = unknown
    val attempt: Int = 1,
) {
    val doneBytes: Long get() = if (total > 0) (total * percent / 100f).toLong() else 0
    val active get() = status == "Queued" || status == "Downloading"
}

object VideoJobs {
    private val _jobs = MutableStateFlow<List<VideoJob>>(emptyList())
    val jobs: StateFlow<List<VideoJob>> = _jobs
    private var sp: SharedPreferences? = null
    private const val MAX = 60

    fun init(ctx: Context) {
        sp = ctx.getSharedPreferences("bddm_video_jobs", Context.MODE_PRIVATE)
        // Anything that was running when the app died is no longer running.
        _jobs.value = load().map { if (it.active) it.copy(status = "Error", message = "Interrupted") else it }
        persist()
    }

    @Synchronized
    fun add(job: VideoJob) {
        _jobs.value = (listOf(job) + _jobs.value.filter { it.id != job.id }).take(MAX)
        persist()
    }

    @Synchronized
    fun update(id: String, persistNow: Boolean = false, f: (VideoJob) -> VideoJob) {
        _jobs.value = _jobs.value.map { if (it.id == id) f(it) else it }
        if (persistNow) persist()
    }

    @Synchronized
    fun remove(id: String) {
        _jobs.value = _jobs.value.filter { it.id != id }
        persist()
    }

    fun get(id: String): VideoJob? = _jobs.value.firstOrNull { it.id == id }

    private fun persist() {
        val a = JSONArray()
        _jobs.value.forEach { j ->
            a.put(JSONObject().put("id", j.id).put("url", j.url).put("title", j.title).put("status", j.status)
                .put("message", j.message).put("file", j.file).put("referer", j.referer).put("format", j.format)
                .put("started", j.started).put("percent", j.percent.toDouble()))
        }
        sp?.edit()?.putString("jobs", a.toString())?.apply()
    }

    private fun load(): List<VideoJob> = try {
        val a = JSONArray(sp?.getString("jobs", "[]") ?: "[]")
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            VideoJob(
                id = o.optString("id"), url = o.optString("url"), title = o.optString("title"),
                status = o.optString("status", "Error"), percent = o.optDouble("percent", 0.0).toFloat(),
                message = o.optString("message"), file = o.optString("file"), referer = o.optString("referer"),
                format = o.optString("format", "best"), started = o.optLong("started"),
            ).takeIf { it.id.isNotBlank() }
        }
    } catch (_: Exception) { emptyList() }
}
