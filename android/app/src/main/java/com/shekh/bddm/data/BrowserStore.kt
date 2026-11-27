package com.shekh.bddm.data

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

data class TabState(
    val id: String = UUID.randomUUID().toString(),
    var title: String = "New tab",
    var url: String = "",
    var incognito: Boolean = false,
    var lastUsed: Long = System.currentTimeMillis(),
    var blocked: Int = 0,
)

data class Bookmark(val id: String, val title: String, val url: String, val added: Long)
data class HistoryEntry(val id: String, val title: String, val url: String, val at: Long)

class BrowserStore(ctx: Context, private val prefs: Prefs) {
    private val sp = ctx.getSharedPreferences("bddm_browser", Context.MODE_PRIVATE)
    var tabs: MutableList<TabState> = loadTabs()
        private set

    fun currentIndex(): Int = sp.getInt("current", 0).coerceIn(0, (tabs.size - 1).coerceAtLeast(0))
    fun setCurrent(i: Int) { sp.edit().putInt("current", i).apply() }

    fun addTab(incognito: Boolean = false, url: String = ""): TabState {
        val t = TabState(incognito = incognito, url = url, title = if (incognito) "Incognito" else "New tab")
        tabs.add(t)
        setCurrent(tabs.lastIndex)
        saveTabs()
        return t
    }

    fun closeTab(id: String) {
        tabs.removeAll { it.id == id }
        if (tabs.isEmpty()) addTab()
        if (currentIndex() >= tabs.size) setCurrent(tabs.lastIndex)
        saveTabs()
    }

    fun closeAll(incognitoOnly: Boolean? = null) {
        tabs = when (incognitoOnly) {
            true -> tabs.filter { !it.incognito }.toMutableList()
            false -> tabs.filter { it.incognito }.toMutableList()
            null -> mutableListOf()
        }
        if (tabs.isEmpty()) addTab(incognito = incognitoOnly == true)
        setCurrent(tabs.lastIndex)
        saveTabs()
    }

    fun closeOthers(keepId: String) {
        tabs = tabs.filter { it.id == keepId }.toMutableList()
        if (tabs.isEmpty()) addTab()
        setCurrent(0)
        saveTabs()
    }

    fun touch(tab: TabState) {
        tab.lastUsed = System.currentTimeMillis()
        saveTabs()
    }

    /** Item 96: close unused (non-incognito) tabs older than N days. */
    fun autoCloseOldTabs() {
        val days = prefs.autoCloseTabsDays
        if (days <= 0) return
        val cut = System.currentTimeMillis() - days * 24L * 60 * 60 * 1000
        val kept = tabs.filter { it.incognito || it.lastUsed >= cut }.toMutableList()
        if (kept.isEmpty()) kept.add(TabState())
        if (kept.size != tabs.size) {
            tabs = kept
            setCurrent(tabs.lastIndex)
            saveTabs()
        }
    }

    fun bookmarks(): List<Bookmark> = readArray("bookmarks") { o ->
        Bookmark(o.getString("id"), o.optString("title"), o.getString("url"), o.optLong("added"))
    }

    fun addBookmark(title: String, url: String) {
        val list = bookmarks().toMutableList()
        if (list.any { it.url == url }) return
        list.add(0, Bookmark(UUID.randomUUID().toString(), title.ifBlank { url }, url, System.currentTimeMillis()))
        writeArray("bookmarks", list.map { o ->
            JSONObject().put("id", o.id).put("title", o.title).put("url", o.url).put("added", o.added)
        })
    }

    fun removeBookmark(id: String) {
        writeArray("bookmarks", bookmarks().filter { it.id != id }.map { o ->
            JSONObject().put("id", o.id).put("title", o.title).put("url", o.url).put("added", o.added)
        })
    }

    fun history(): List<HistoryEntry> = readArray("history") { o ->
        HistoryEntry(o.getString("id"), o.optString("title"), o.getString("url"), o.optLong("at"))
    }

    fun addHistory(title: String, url: String) {
        if (url.isBlank()) return
        val list = history().toMutableList()
        list.removeAll { it.url == url }
        list.add(0, HistoryEntry(UUID.randomUUID().toString(), title.ifBlank { url }, url, System.currentTimeMillis()))
        writeArray("history", list.take(500).map { o ->
            JSONObject().put("id", o.id).put("title", o.title).put("url", o.url).put("at", o.at)
        })
    }

    fun clearHistory() { sp.edit().putString("history", "[]").apply() }

    private fun loadTabs(): MutableList<TabState> {
        val list = readArray("tabs") { o ->
            TabState(
                id = o.getString("id"),
                title = o.optString("title"),
                url = o.optString("url"),
                incognito = o.optBoolean("incognito"),
                lastUsed = o.optLong("lastUsed", System.currentTimeMillis()),
            )
        }.toMutableList()
        if (list.isEmpty()) list.add(TabState())
        return list
    }

    fun saveTabs() {
        writeArray("tabs", tabs.filter { !it.incognito }.map { t ->
            JSONObject().put("id", t.id).put("title", t.title).put("url", t.url)
                .put("incognito", false).put("lastUsed", t.lastUsed)
        })
    }

    private fun <T> readArray(key: String, map: (JSONObject) -> T): List<T> {
        return try {
            val a = JSONArray(sp.getString(key, "[]"))
            (0 until a.length()).map { map(a.getJSONObject(it)) }
        } catch (_: Exception) { emptyList() }
    }

    private fun writeArray(key: String, items: List<JSONObject>) {
        val a = JSONArray()
        items.forEach { a.put(it) }
        sp.edit().putString(key, a.toString()).apply()
    }
}
