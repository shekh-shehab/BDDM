package com.shekh.bddm.data

import android.content.Context
import android.content.SharedPreferences
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONArray
import org.json.JSONObject

/** Videos the user chose to "Download later" — shown on the main list until started. */
object LaterStore {
    private val _items = MutableStateFlow<List<PendingDownload>>(emptyList())
    val items: StateFlow<List<PendingDownload>> = _items
    private var sp: SharedPreferences? = null

    fun init(ctx: Context) {
        sp = ctx.getSharedPreferences("bddm_later", Context.MODE_PRIVATE)
        _items.value = load()
    }

    fun add(item: PendingDownload) {
        val cur = _items.value.toMutableList()
        cur.removeAll { it.url == item.url }
        cur.add(0, item)
        _items.value = cur
        persist()
    }

    fun remove(url: String) {
        _items.value = _items.value.filter { it.url != url }
        persist()
    }

    private fun persist() {
        val a = JSONArray()
        _items.value.forEach { p ->
            a.put(
                JSONObject()
                    .put("url", p.url)
                    .put("title", p.title)
                    .put("subtitle", p.subtitle)
                    .put("qualityId", p.qualityId)
            )
        }
        sp?.edit()?.putString("items", a.toString())?.apply()
    }

    private fun load(): List<PendingDownload> {
        return try {
            val a = JSONArray(sp?.getString("items", "[]") ?: "[]")
            (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                PendingDownload(
                    url = o.optString("url"),
                    title = o.optString("title"),
                    subtitle = o.optString("subtitle"),
                    kind = PendingDownload.Kind.Video,
                    qualityId = o.optString("qualityId", "best"),
                ).takeIf { it.url.isNotBlank() }
            }
        } catch (_: Exception) { emptyList() }
    }
}
