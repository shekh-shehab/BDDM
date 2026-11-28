package com.shekh.bddm.service

import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import com.shekh.bddm.BDDMApp
import com.shekh.bddm.R
import com.shekh.bddm.data.EngineHolder
import com.shekh.bddm.data.VideoJob
import com.shekh.bddm.data.VideoJobs
import com.shekh.bddm.data.YtDlp
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.*
import kotlinx.coroutines.currentCoroutineContext
import org.json.JSONObject
import java.io.File

class DownloadService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var job: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForegroundInternal("BDDM is running")
        if (job == null) job = scope.launch { loop() }
        when (intent?.action) {
            ACTION_VIDEO -> {
                val url = intent.getStringExtra(EXTRA_URL) ?: return START_STICKY
                val format = intent.getStringExtra(EXTRA_FORMAT) ?: "best"
                val dir = intent.getStringExtra(EXTRA_DIR) ?: BDDMApp.instance.prefs.downloadDirFile().absolutePath
                val title = intent.getStringExtra(EXTRA_TITLE).orEmpty().ifBlank { url }
                val referer = intent.getStringExtra(EXTRA_REFERER).orEmpty()
                val cookie = intent.getStringExtra(EXTRA_COOKIE).orEmpty()
                val ua = intent.getStringExtra(EXTRA_UA).orEmpty()
                val jobId = "v" + System.nanoTime().toString(36)
                VideoJobs.add(VideoJob(id = jobId, url = url, title = title, referer = referer, format = format))
                scope.launch { videoSlots.withPermit { runVideo(jobId, url, format, File(dir), referer, cookie, ua) } }
            }
            ACTION_CANCEL -> {
                val id = intent.getStringExtra(EXTRA_JOB_ID) ?: return START_STICKY
                VideoJobs.update(id, persistNow = true) { if (it.active) it.copy(status = "Cancelled", message = "Cancelled") else it }
                YtDlp.cancel(id)
            }
        }
        return START_STICKY
    }

    private fun startForegroundInternal(text: String) {
        val n = NotificationCompat.Builder(this, BDDMApp.CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_download)
            .setContentTitle("BDDM")
            .setContentText(text)
            .setOngoing(true)
            .setSilent(true)
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            ServiceCompat.startForeground(this, 1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(1, n)
        }
    }

    private suspend fun loop() {
        while (currentCoroutineContext().isActive) {
            EngineHolder.refresh()
            val snap = EngineHolder.snap.value
            try {
                val o = JSONObject(snap)
                val speed = o.optDouble("speed", 0.0)
                val active = o.optInt("active", 0)
                val text = if (active > 0) {
                    val mb = speed / (1024 * 1024)
                    val sp = if (mb >= 0.1) "%.1f MB/s".format(mb) else "%.0f KB/s".format(speed / 1024)
                    "$active downloading · $sp"
                } else "No active downloads"
                startForegroundInternal(text)
                if (BDDMApp.instance.prefs.notifications) maybeNotifyFinished(o)
            } catch (_: Exception) {}
            delay(1000)
        }
    }

    private val notified = HashSet<String>()
    private fun maybeNotifyFinished(o: JSONObject) {
        val jobs = o.optJSONArray("jobs") ?: return
        for (i in 0 until jobs.length()) {
            val j = jobs.optJSONObject(i) ?: continue
            val id = j.optString("id")
            val st = j.optString("status")
            if (st == "Done" && notified.add("ok-$id")) {
                notify(2 + notified.size, j.optString("name"), "Download finished")
            }
            if (st == "Error" && notified.add("err-$id")) {
                notify(2 + notified.size, j.optString("name"), j.optString("error").ifBlank { "Download failed" })
            }
        }
    }

    private fun notify(id: Int, title: String, text: String) {
        val n = NotificationCompat.Builder(this, BDDMApp.CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_download)
            .setContentTitle(title.ifBlank { "BDDM" })
            .setContentText(text)
            .setAutoCancel(true)
            .build()
        try { NotificationManagerCompat.from(this).notify(id, n) } catch (_: SecurityException) {}
    }

    private val videoSlots = Semaphore(2)

    private fun runVideo(jobId: String, url: String, format: String, dir: File, referer: String, cookie: String, ua: String) {
        if (VideoJobs.get(jobId)?.status == "Cancelled") return
        VideoJobs.update(jobId) { it.copy(status = "Downloading", message = "Starting…") }
        startForegroundInternal("Preparing video…")
        var lastUi = 0L
        try {
            val file = YtDlp.download(url, format, dir, processId = jobId, referer = referer, userAgent = ua, cookie = cookie) { pct, eta, _ ->
                val now = System.currentTimeMillis()
                if (now - lastUi > 400) {
                    lastUi = now
                    VideoJobs.update(jobId) { if (it.status == "Cancelled") it else it.copy(percent = pct.coerceIn(0f, 100f), eta = eta, message = "") }
                }
            }
            VideoJobs.update(jobId, persistNow = true) { it.copy(status = "Done", percent = 100f, file = file.absolutePath, title = file.nameWithoutExtension.ifBlank { it.title }, message = "") }
            if (BDDMApp.instance.prefs.notifications) notify(2000 + jobId.hashCode(), file.name, "Video download finished")
        } catch (t: Throwable) {
            val cancelled = VideoJobs.get(jobId)?.status == "Cancelled"
            if (!cancelled) {
                val msg = (t.message ?: "Download failed").lineSequence().lastOrNull { it.isNotBlank() }?.take(300) ?: "Download failed"
                VideoJobs.update(jobId, persistNow = true) { it.copy(status = "Error", message = msg) }
                if (BDDMApp.instance.prefs.notifications) notify(2000 + jobId.hashCode(), "Video download failed", msg)
            }
        }
        EngineHolder.refresh()
    }

    override fun onDestroy() {
        job?.cancel()
        scope.cancel()
        super.onDestroy()
    }

    companion object {
        const val ACTION_VIDEO = "com.shekh.bddm.VIDEO"
        const val EXTRA_URL = "url"
        const val EXTRA_FORMAT = "format"
        const val EXTRA_DIR = "dir"
        const val EXTRA_TITLE = "title"
        const val EXTRA_REFERER = "referer"
        const val EXTRA_COOKIE = "cookie"
        const val EXTRA_UA = "ua"
        const val EXTRA_JOB_ID = "job_id"
        const val ACTION_CANCEL = "com.shekh.bddm.VIDEO_CANCEL"
    }
}
