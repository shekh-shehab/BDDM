package com.shekh.bddm

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build
import com.shekh.bddm.data.AdBlocker
import com.shekh.bddm.data.EngineHolder
import com.shekh.bddm.data.Prefs
import com.shekh.bddm.data.LaterStore
import com.shekh.bddm.data.YtDlp
import com.yausername.ffmpeg.FFmpeg
import com.yausername.youtubedl_android.YoutubeDL
import java.io.File

class BDDMApp : Application() {
    lateinit var prefs: Prefs
        private set
    lateinit var adBlocker: AdBlocker
        private set

    override fun onCreate() {
        super.onCreate()
        instance = this
        prefs = Prefs(this)
        createNotificationChannel()
        val cfg = File(filesDir, "engine").apply { mkdirs() }
        val dl = prefs.downloadDirFile().apply { mkdirs() }
        EngineHolder.init(cfg.absolutePath, dl.absolutePath)
        LaterStore.init(this)
        adBlocker = AdBlocker(this, prefs)
        adBlocker.loadAsync()
        try {
            YoutubeDL.getInstance().init(this)
            FFmpeg.getInstance().init(this)
            YtDlp.ready = true
        } catch (t: Throwable) {
            YtDlp.ready = false
            YtDlp.initError = t.message
        }
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val ch = NotificationChannel(
                CHANNEL,
                getString(R.string.download_channel),
                NotificationManager.IMPORTANCE_LOW,
            )
            ch.description = getString(R.string.download_channel_desc)
            getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
        }
    }

    companion object {
        const val CHANNEL = "bddm_downloads"
        lateinit var instance: BDDMApp
            private set
    }
}
