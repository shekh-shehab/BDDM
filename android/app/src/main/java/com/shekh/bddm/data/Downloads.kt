package com.shekh.bddm.data

import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import com.shekh.bddm.service.DownloadService

object Downloads {
    fun startHttp(url: String, later: Boolean) {
        val id = EngineHolder.engine?.add(url, com.shekh.bddm.BDDMApp.instance.prefs.downloadDirFile().absolutePath, "")
        if (later && id != null) EngineHolder.engine?.pause(id)
        EngineHolder.refresh()
    }

    fun startVideo(ctx: Context, item: PendingDownload, later: Boolean) {
        if (later) {
            LaterStore.add(item)
            return
        }
        LaterStore.remove(item.url)
        val i = Intent(ctx, DownloadService::class.java).apply {
            action = DownloadService.ACTION_VIDEO
            putExtra(DownloadService.EXTRA_URL, item.url)
            putExtra(DownloadService.EXTRA_FORMAT, item.qualityId)
            putExtra(DownloadService.EXTRA_DIR, com.shekh.bddm.BDDMApp.instance.prefs.downloadDirFile().absolutePath)
        }
        ContextCompat.startForegroundService(ctx, i)
    }
}
