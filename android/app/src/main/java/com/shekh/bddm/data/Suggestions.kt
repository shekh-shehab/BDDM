package com.shekh.bddm.data

import org.json.JSONArray
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

data class Suggestion(
    val text: String,          // what to show / search for
    val url: String?,          // non-null: navigate here (history or bookmark)
    val kind: Kind,
    val subtitle: String = "",
) {
    enum class Kind { BOOKMARK, HISTORY, SEARCH }
}

/** Address-bar suggestions (item 106): local history + bookmarks first, then Google's. */
object Suggestions {
    /** Parses suggestqueries.google.com `client=firefox` output: ["query",["a","b",...],...] */
    fun parseGoogle(json: String): List<String> {
        return try {
            val root = JSONArray(json.trim())
            val arr = root.optJSONArray(1) ?: return emptyList()
            (0 until arr.length()).mapNotNull { arr.optString(it, "").trim().takeIf { s -> s.isNotEmpty() } }
        } catch (_: Exception) { emptyList() }
    }

    fun normalizeUrl(u: String): String =
        u.trim().lowercase().removePrefix("https://").removePrefix("http://").removePrefix("www.").trimEnd('/')

    /** History/bookmark matches for [query], best first. Bookmarks beat history on equal score. */
    fun local(query: String, history: List<HistoryEntry>, bookmarks: List<Bookmark>, limit: Int = 5): List<Suggestion> {
        val q = query.trim().lowercase()
        if (q.isEmpty()) return emptyList()
        val words = q.split(Regex("\\s+")).filter { it.isNotEmpty() }
        class Scored(val s: Suggestion, val score: Int, val order: Int)
        val out = ArrayList<Scored>()
        val seen = HashSet<String>()
        fun consider(title: String, url: String, kind: Suggestion.Kind, order: Int) {
            val key = normalizeUrl(url)
            if (key.isEmpty() || key in seen) return
            val t = title.lowercase()
            val u = key
            if (!words.all { t.contains(it) || u.contains(it) }) return
            var score = 0
            if (u.startsWith(q)) score += 6
            else if (u.substringBefore('/').contains(q)) score += 4
            if (t.startsWith(q)) score += 3
            if (t.contains(q)) score += 1
            if (u.contains(q)) score += 1
            if (kind == Suggestion.Kind.BOOKMARK) score += 4
            seen.add(key)
            out.add(Scored(Suggestion(title.ifBlank { url }, url, kind, url), score, order))
        }
        bookmarks.forEachIndexed { i, b -> consider(b.title, b.url, Suggestion.Kind.BOOKMARK, i) }
        history.forEachIndexed { i, h -> consider(h.title, h.url, Suggestion.Kind.HISTORY, 1000 + i) }
        return out.sortedWith(compareByDescending<Scored> { it.score }.thenBy { it.order }).take(limit).map { it.s }
    }

    /** Local matches first, then remote search suggestions that do not repeat the typed text. */
    fun merge(query: String, local: List<Suggestion>, remote: List<String>, limit: Int = 8): List<Suggestion> {
        val q = query.trim()
        val out = ArrayList<Suggestion>(local)
        val have = HashSet<String>(local.map { it.text.lowercase() })
        for (r in remote) {
            if (out.size >= limit) break
            if (r.equals(q, ignoreCase = true) && local.isNotEmpty()) continue
            if (have.add(r.lowercase())) out.add(Suggestion(r, null, Suggestion.Kind.SEARCH))
        }
        return out.take(limit)
    }

    fun googleUrl(query: String): String =
        "https://suggestqueries.google.com/complete/search?client=firefox&q=" + URLEncoder.encode(query.trim(), "UTF-8")

    /** Blocking network call: run on an IO dispatcher. Returns an empty list on any failure. */
    fun fetchGoogle(query: String): List<String> {
        if (query.isBlank()) return emptyList()
        var c: HttpURLConnection? = null
        return try {
            c = URL(googleUrl(query)).openConnection() as HttpURLConnection
            c.connectTimeout = 2500
            c.readTimeout = 2500
            c.setRequestProperty("User-Agent", "Mozilla/5.0")
            if (c.responseCode != 200) emptyList()
            else parseGoogle(c.inputStream.bufferedReader(Charsets.UTF_8).use { it.readText().take(20000) })
        } catch (_: Exception) { emptyList() } finally { c?.disconnect() }
    }
}
