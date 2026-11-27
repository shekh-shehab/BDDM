package com.shekh.bddm.data

import android.content.Context
import android.os.Environment
import org.json.JSONObject
import java.io.File

class Prefs(private val ctx: Context) {
    private val sp = ctx.getSharedPreferences("bddm", Context.MODE_PRIVATE)

    var theme: String
        get() = sp.getString("theme", "dark") ?: "dark"
        set(v) { sp.edit().putString("theme", v).apply() }

    var wifiOnly: Boolean
        get() = sp.getBoolean("wifiOnly", false)
        set(v) { sp.edit().putBoolean("wifiOnly", v).apply() }

    var adBlocker: Boolean
        get() = sp.getBoolean("adBlocker", true)
        set(v) { sp.edit().putBoolean("adBlocker", v).apply() }

    var blockPopups: Boolean
        get() = sp.getBoolean("blockPopups", true)
        set(v) { sp.edit().putBoolean("blockPopups", v).apply() }

    var blockTrackers: Boolean
        get() = sp.getBoolean("blockTrackers", true)
        set(v) { sp.edit().putBoolean("blockTrackers", v).apply() }

    var filterListAutoUpdate: Boolean
        get() = sp.getBoolean("filterListAutoUpdate", true)
        set(v) { sp.edit().putBoolean("filterListAutoUpdate", v).apply() }

    var autoCloseTabsDays: Int
        get() = sp.getInt("autoCloseTabsDays", 7)
        set(v) { sp.edit().putInt("autoCloseTabsDays", v).apply() }

    var searchEngine: String
        get() = sp.getString("searchEngine", "https://www.google.com/search?q=") ?: "https://www.google.com/search?q="
        set(v) { sp.edit().putString("searchEngine", v).apply() }

    var notifications: Boolean
        get() = sp.getBoolean("notifications", true)
        set(v) { sp.edit().putBoolean("notifications", v).apply() }

    var checkUpdates: Boolean
        get() = sp.getBoolean("checkUpdates", true)
        set(v) { sp.edit().putBoolean("checkUpdates", v).apply() }

    var siteAdblockOverrides: String
        get() = sp.getString("siteAdblock", "{}") ?: "{}"
        set(v) { sp.edit().putString("siteAdblock", v).apply() }

    fun adblockOnFor(host: String): Boolean {
        if (!adBlocker) return false
        return try {
            val o = JSONObject(siteAdblockOverrides)
            if (o.has(host)) o.optBoolean(host, true) else true
        } catch (_: Exception) { true }
    }

    fun setAdblockFor(host: String, on: Boolean) {
        val o = try { JSONObject(siteAdblockOverrides) } catch (_: Exception) { JSONObject() }
        o.put(host, on)
        siteAdblockOverrides = o.toString()
    }

    fun downloadDirFile(): File {
        val custom = sp.getString("downloadDir", "") ?: ""
        if (custom.isNotBlank()) return File(custom)
        val pub = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        return File(pub, "BDDM")
    }

    fun setDownloadDir(path: String) {
        sp.edit().putString("downloadDir", path).apply()
    }

    fun syncFromEngine(json: String) {
        try {
            val o = JSONObject(json)
            theme = o.optString("theme", theme)
            wifiOnly = o.optBoolean("wifiOnly", wifiOnly)
            adBlocker = o.optBoolean("adBlocker", adBlocker)
            blockPopups = o.optBoolean("blockPopups", blockPopups)
            blockTrackers = o.optBoolean("blockTrackers", blockTrackers)
            filterListAutoUpdate = o.optBoolean("filterListAutoUpdate", filterListAutoUpdate)
            autoCloseTabsDays = o.optInt("autoCloseTabsDays", autoCloseTabsDays)
            notifications = o.optBoolean("notifications", notifications)
            checkUpdates = o.optBoolean("checkUpdates", checkUpdates)
            val dir = o.optString("downloadDir", "")
            if (dir.isNotBlank()) setDownloadDir(dir)
        } catch (_: Exception) {}
    }
}
