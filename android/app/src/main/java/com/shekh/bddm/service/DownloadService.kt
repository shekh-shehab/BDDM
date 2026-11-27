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
import com.shekh.bddm.data.YtDlp
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
                scope.launch { runVideo(url, format, File(dir)) }
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

    private fun runVideo(url: String, format: String, dir: File) {
        startForegroundInternal("Preparing video…")
        try {
            val file = YtDlp.download(url, format, dir) { pct, eta, _ ->
                val etaTxt = if (eta > 0) " · ${eta}s left" else ""
                startForegroundInternal("Video ${pct.toInt()}%$etaTxt")
            }
            notify(2000 + url.hashCode(), file.name, "Video download finished")
        } catch (t: Throwable) {
            notify(2000 + url.hashCode(), "Video download", t.message ?: "failed")
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
    }
}
