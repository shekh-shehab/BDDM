package com.shekh.bddm.service

import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.MediaScannerConnection
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import com.shekh.bddm.BDDMApp
import com.shekh.bddm.MainActivity
import com.shekh.bddm.R
import com.shekh.bddm.data.*
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import org.json.JSONObject
import java.io.File

/**
 * Shows one live notification for engine (multi-part) downloads and yt-dlp video downloads,
 * and stops itself as soon as nothing is running (bug 113).
 */
class DownloadService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var loopJob: kotlinx.coroutines.Job? = null
    private val videoSlots = Semaphore(2)
    @Volatile private var pendingVideos = 0
    @Volatile private var inForeground = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        running = true
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Must call startForeground quickly after startForegroundService.
        showStatus(Status("Starting…", "", -1, 0, 0))
        if (loopJob?.isActive != true) loopJob = scope.launch { loop() }
        when (intent?.action) {
            ACTION_VIDEO -> {
                val url = intent.getStringExtra(EXTRA_URL)
                if (url != null) {
                    val format = intent.getStringExtra(EXTRA_FORMAT) ?: "best"
                    val dir = intent.getStringExtra(EXTRA_DIR) ?: BDDMApp.instance.prefs.downloadDirFile().absolutePath
                    val title = intent.getStringExtra(EXTRA_TITLE).orEmpty().ifBlank { url }
                    val referer = intent.getStringExtra(EXTRA_REFERER).orEmpty()
                    val cookie = intent.getStringExtra(EXTRA_COOKIE).orEmpty()
                    val ua = intent.getStringExtra(EXTRA_UA).orEmpty()
                    val jobId = "v" + System.nanoTime().toString(36)
                    VideoJobs.add(VideoJob(id = jobId, url = url, title = title, referer = referer, format = format))
                    pendingVideos++
                    scope.launch {
                        try { videoSlots.withPermit { runVideo(jobId, url, format, File(dir), referer, cookie, ua, title) } }
                        finally { pendingVideos-- }
                    }
                }
            }
            ACTION_CANCEL -> {
                val id = intent.getStringExtra(EXTRA_JOB_ID)
                if (id != null) {
                    VideoJobs.update(id, persistNow = true) { if (it.active) it.copy(status = "Cancelled", message = "Cancelled", speed = 0.0) else it }
                    YtDlp.cancel(id)
                }
            }
        }
        return START_NOT_STICKY
    }

    data class Status(val title: String, val text: String, val percent: Int, val active: Int, val queued: Int)

    private fun showStatus(s: Status) {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val b = NotificationCompat.Builder(this, BDDMApp.CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_download)
            .setContentTitle(s.title)
            .setOngoing(true)
            .setSilent(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .setCategory(NotificationCompat.CATEGORY_PROGRESS)
        if (s.text.isNotBlank()) b.setContentText(s.text)
        if (s.active > 0 || s.queued > 0) {
            if (s.percent in 0..100) b.setProgress(100, s.percent, false) else b.setProgress(0, 0, true)
        }
        val n = b.build()
        try {
            if (Build.VERSION.SDK_INT >= 34) {
                ServiceCompat.startForeground(this, NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
            } else {
                startForeground(NOTIF_ID, n)
            }
            inForeground = true
        } catch (_: Exception) { }
    }

    private suspend fun loop() {
        var idleTicks = 0
        while (currentCoroutineContext().isActive) {
            EngineHolder.refresh()
            NetworkWatcher.retryFailed()
            val snap = parseSnap(EngineHolder.snap.value)
            notifyFinished(snap)
            val videos = VideoJobs.jobs.value.filter { it.active }
            val goActive = snap.jobs.filter { it.status == "Downloading" }
            val goQueued = snap.jobs.count { it.status == "Queued" }
            val busy = goActive.isNotEmpty() || goQueued > 0 || videos.isNotEmpty() || pendingVideos > 0
            if (!busy) {
                idleTicks++
                if (idleTicks >= 3) {
                    stopSelfNow()
                    return
                }
            } else {
                idleTicks = 0
                showStatus(summarize(goActive, goQueued, videos))
            }
            delay(1000)
        }
    }

    /** Pure aggregation: one line for everything that is running. */
    private fun summarize(go: List<com.shekh.bddm.data.Job>, goQueued: Int, videos: List<VideoJob>): Status {
        val count = go.size + videos.size
        var speed = go.sumOf { it.speed } + videos.sumOf { it.speed }
        var total = go.filter { it.size > 0 }.sumOf { it.size } + videos.filter { it.total > 0 }.sumOf { it.total }
        var done = go.filter { it.size > 0 }.sumOf { it.done } + videos.filter { it.total > 0 }.sumOf { it.doneBytes }
        val pct = when {
            total > 0 -> ((done * 100) / total).toInt()
            count == 1 && videos.size == 1 && videos[0].percent > 0f -> videos[0].percent.toInt()
            else -> -1
        }
        val title = when {
            count == 1 && go.size == 1 -> go[0].name.ifBlank { "Downloading" }
            count == 1 && videos.size == 1 -> videos[0].title
            count > 1 -> "Downloading $count files"
            else -> if (goQueued > 0) "$goQueued queued" else "Preparing…"
        }
        val wait = NetworkWatcher.waitingReason()
        val text = buildString {
            if (pct >= 0) append("$pct%")
            if (speed > 0) { if (isNotEmpty()) append(" · "); append(formatSpeed(speed)) }
            if (total > 0 && speed > 0) {
                val eta = ((total - done) / speed).toLong()
                if (eta > 0) { append(" · "); append(formatEta(eta)).append(" left") }
            }
            if (goQueued > 0 && count > 0) { append(" · $goQueued queued") }
            if (isEmpty()) append(videos.firstOrNull()?.message?.ifBlank { null } ?: wait.ifBlank { "Connecting…" })
        }
        return Status(title, text, pct, count, goQueued)
    }

    private fun stopSelfNow() {
        try { ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE) } catch (_: Exception) {}
        inForeground = false
        stopSelf()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        // App swiped away: if nothing runs, take the notification down right away.
        val snap = parseSnap(EngineHolder.snap.value)
        val busy = snap.jobs.any { it.status == "Downloading" || it.status == "Queued" } ||
            VideoJobs.jobs.value.any { it.active } || pendingVideos > 0
        if (!busy) stopSelfNow()
        super.onTaskRemoved(rootIntent)
    }

    private val notified = HashSet<String>()
    private var primed = false
    private fun notifyFinished(s: Snap) {
        if (!primed) {
            // Do not re-announce downloads that finished before this service started.
            s.jobs.filter { it.status == "Done" || it.status == "Error" }.forEach { notified.add(it.status + it.id) }
            primed = true
            return
        }
        for (j in s.jobs) {
            if (j.status == "Done" && notified.add("Done" + j.id)) {
                scanFile(File(j.dir, j.name).absolutePath)
                if (BDDMApp.instance.prefs.notifications) notify(j.id.hashCode(), j.name, "Download finished")
            }
            if (j.status == "Error" && notified.add("Error" + j.id)) {
                if (!RetryPolicy.isNetworkError(j.error) && BDDMApp.instance.prefs.notifications) {
                    notify(j.id.hashCode(), j.name.ifBlank { "Download" }, j.error.ifBlank { "Download failed" })
                }
            }
            if (j.status != "Error") notified.remove("Error" + j.id)
        }
    }

    /** Item 37: make finished files show up in Gallery / Files right away. */
    private fun scanFile(path: String) {
        try { MediaScannerConnection.scanFile(applicationContext, arrayOf(path), null, null) } catch (_: Exception) {}
    }

    private fun notify(id: Int, title: String, text: String) {
        val open = PendingIntent.getActivity(
            this, 1, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val n = NotificationCompat.Builder(this, BDDMApp.CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_download)
            .setContentTitle(title.ifBlank { "BDDM" })
            .setContentText(text)
            .setContentIntent(open)
            .setAutoCancel(true)
            .build()
        try { NotificationManagerCompat.from(this).notify(id, n) } catch (_: SecurityException) {}
    }

    private suspend fun runVideo(jobId: String, url: String, format: String, dir: File, referer: String, cookie: String, ua: String, title: String) {
        if (VideoJobs.get(jobId)?.status == "Cancelled") return
        val maxAttempts = 3
        var attempt = 1
        while (true) {
            // Item 45: honour Wi-Fi only / offline.
            while (!NetworkWatcher.allowed()) {
                if (VideoJobs.get(jobId)?.status == "Cancelled") return
                VideoJobs.update(jobId) { it.copy(status = "Queued", message = NetworkWatcher.waitingReason(), speed = 0.0) }
                delay(2000)
            }
            VideoJobs.update(jobId) {
                it.copy(status = "Downloading", attempt = attempt,
                    message = if (YtDlp.updating) "Updating the video engine…" else if (attempt > 1) "Retrying ($attempt/$maxAttempts)…" else "Starting…")
            }
            var lastUi = 0L
            try {
                val limit = try { JSONObject(EngineHolder.settings.value).optLong("httpDownloadLimit", 0) } catch (_: Exception) { 0L }
                val file = YtDlp.download(url, format, dir, processId = jobId, referer = referer, userAgent = ua, cookie = cookie,
                    title = title, limitBps = limit) { pct, eta, line ->
                    val p = YtProgressParser.parse(line)
                    val now = System.currentTimeMillis()
                    if (now - lastUi > 400) {
                        lastUi = now
                        VideoJobs.update(jobId) {
                            if (it.status == "Cancelled") it else it.copy(
                                percent = (p?.percent ?: pct).coerceIn(0f, 100f),
                                eta = if (p != null && p.etaSec >= 0) p.etaSec else eta,
                                speed = p?.speedBps ?: it.speed,
                                total = if (p != null && p.totalBytes > 0) p.totalBytes else it.total,
                                message = if (line.contains("Merging") || line.contains("ExtractAudio")) "Finishing…" else "",
                            )
                        }
                    }
                }
                VideoJobs.update(jobId, persistNow = true) {
                    it.copy(status = "Done", percent = 100f, speed = 0.0, eta = 0, file = file.absolutePath,
                        total = if (file.length() > 0) file.length() else it.total,
                        title = file.nameWithoutExtension.ifBlank { it.title }, message = "")
                }
                scanFile(file.absolutePath)
                if (BDDMApp.instance.prefs.notifications) notify(jobId.hashCode(), file.name, "Video download finished")
                return
            } catch (t: Throwable) {
                if (VideoJobs.get(jobId)?.status == "Cancelled") return
                val msg = (t.message ?: "Download failed").lineSequence().lastOrNull { it.isNotBlank() }?.take(300) ?: "Download failed"
                if (attempt < maxAttempts) {
                    // Bug 109: a transient first-attempt failure is retried automatically.
                    attempt++
                    VideoJobs.update(jobId) { it.copy(message = "Retrying in a moment… ($msg)", speed = 0.0) }
                    delay(2500L * attempt)
                    continue
                }
                VideoJobs.update(jobId, persistNow = true) { it.copy(status = "Error", message = msg, speed = 0.0) }
                if (BDDMApp.instance.prefs.notifications) notify(jobId.hashCode(), "Video download failed", msg)
                return
            }
        }
    }

    override fun onDestroy() {
        running = false
        loopJob?.cancel()
        scope.cancel()
        super.onDestroy()
    }

    companion object {
        @Volatile var running = false
        const val NOTIF_ID = 1
        const val ACTION_VIDEO = "com.shekh.bddm.VIDEO"
        const val ACTION_KICK = "com.shekh.bddm.KICK"
        const val ACTION_CANCEL = "com.shekh.bddm.VIDEO_CANCEL"
        const val EXTRA_URL = "url"
        const val EXTRA_FORMAT = "format"
        const val EXTRA_DIR = "dir"
        const val EXTRA_TITLE = "title"
        const val EXTRA_REFERER = "referer"
        const val EXTRA_COOKIE = "cookie"
        const val EXTRA_UA = "ua"
        const val EXTRA_JOB_ID = "job_id"
    }
}
