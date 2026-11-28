package com.shekh.bddm.data

import com.shekh.bddm.core.mobile.Engine
import com.shekh.bddm.core.mobile.Listener
import com.shekh.bddm.core.mobile.Mobile
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONObject

object EngineHolder {
    @Volatile var engine: Engine? = null
        private set
    private val _snap = MutableStateFlow("{}")
    val snap: StateFlow<String> = _snap
    private val _settings = MutableStateFlow("{}")
    val settings: StateFlow<String> = _settings

    fun init(configDir: String, downloadDir: String) {
        if (engine != null) return
        val e = Mobile.new_(configDir, downloadDir)
        e.setListener(Listener {
            refresh()
            // Bug 113/110: the service runs only while something downloads; start it when work appears.
            val s = _snap.value
            if (!com.shekh.bddm.service.DownloadService.running &&
                (s.contains("\"status\":\"Downloading\"") || s.contains("\"status\":\"Queued\""))
            ) {
                Downloads.kick(com.shekh.bddm.BDDMApp.instance)
            }
        })
        engine = e
        refresh()
    }

    fun refresh() {
        val e = engine ?: return
        _snap.value = e.snapshotJSON()
        _settings.value = e.settingsJSON()
    }

    fun applySettings(patch: Map<String, Any?>) {
        val e = engine ?: return
        val o = try { JSONObject(e.settingsJSON()) } catch (_: Exception) { JSONObject() }
        patch.forEach { (k, v) ->
            when (v) {
                null -> o.remove(k)
                is Boolean -> o.put(k, v)
                is Int -> o.put(k, v)
                is Long -> o.put(k, v)
                is Double -> o.put(k, v)
                else -> o.put(k, v.toString())
            }
        }
        e.setSettingsJSON(o.toString())
        refresh()
    }
}
