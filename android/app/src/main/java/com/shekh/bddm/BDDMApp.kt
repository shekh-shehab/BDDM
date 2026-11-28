package com.shekh.bddm

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build
import com.shekh.bddm.data.AdBlocker
import com.shekh.bddm.data.EngineHolder
import com.shekh.bddm.data.Prefs
import com.shekh.bddm.data.LaterStore
import com.shekh.bddm.data.VideoJobs
import com.shekh.bddm.data.YtDlp
import java.io.File

class BDDMApp : Application() {
    lateinit var prefs: Prefs
        private set
    lateinit var adBlocker: AdBlocker
        private set

    /** The installed app's real version (the Go engine's built-in constant is not rebuilt for every release). */
    val versionName: String by lazy {
        try { packageManager.getPackageInfo(packageName, 0).versionName ?: "" } catch (_: Exception) { "" }
    }

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
        VideoJobs.init(this)
        YtDlp.initAsync(this)
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
