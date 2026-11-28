package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Test

class SuggestionsTest {
    @Test
    fun parsesFirefoxStyleGoogleResponse() {
        val json = """["kotlin co",["kotlin coroutines","kotlin compose","kotlin collections"],[],{"google:suggestsubtypes":[]}]"""
        assertEquals(listOf("kotlin coroutines", "kotlin compose", "kotlin collections"), Suggestions.parseGoogle(json))
    }

    @Test
    fun badOrEmptyResponsesGiveNoSuggestions() {
        assertTrue(Suggestions.parseGoogle("").isEmpty())
        assertTrue(Suggestions.parseGoogle("<html>blocked</html>").isEmpty())
        assertTrue(Suggestions.parseGoogle("""["x"]""").isEmpty())
        assertTrue(Suggestions.parseGoogle("""["x",[]]""").isEmpty())
    }

    @Test
    fun requestUrlIsEncodedAndUsesFirefoxClient() {
        val u = Suggestions.googleUrl("a b&c")
        assertTrue(u.startsWith("https://suggestqueries.google.com/complete/search?client=firefox&q="))
        assertTrue(u.endsWith("a+b%26c"))
    }

    private val hist = listOf(
        HistoryEntry("1", "Example News", "https://www.example.com/news", 3),
        HistoryEntry("2", "Cooking tips", "https://food.test/tips", 2),
        HistoryEntry("3", "Example docs", "https://docs.example.com/", 1),
    )
    private val marks = listOf(Bookmark("b1", "My Example site", "https://example.com/", 5))

    @Test
    fun localMatchesBookmarksAndHistoryWithoutDuplicates() {
        val r = Suggestions.local("exam", hist, marks)
        assertEquals(3, r.size)
        assertEquals(Suggestion.Kind.BOOKMARK, r[0].kind)
        // example.com (bookmark) and www.example.com/news are different URLs; both are kept
        assertTrue(r.any { it.url == "https://www.example.com/news" })
        assertTrue(r.none { it.url == "https://food.test/tips" })
    }

    @Test
    fun sameUrlInBookmarksAndHistoryAppearsOnce() {
        val h = listOf(HistoryEntry("1", "Example", "http://www.example.com", 1))
        val r = Suggestions.local("example", h, marks)
        assertEquals(1, r.size)
        assertEquals(Suggestion.Kind.BOOKMARK, r[0].kind)
    }

    @Test
    fun multiWordQueryNeedsAllWords() {
        val r = Suggestions.local("cooking tips", hist, marks)
        assertEquals(1, r.size)
        assertEquals("https://food.test/tips", r[0].url)
        assertTrue(Suggestions.local("cooking zzz", hist, marks).isEmpty())
        assertTrue(Suggestions.local("   ", hist, marks).isEmpty())
    }

    @Test
    fun mergeKeepsLocalFirstDropsDuplicatesAndRespectsLimit() {
        val local = Suggestions.local("example", hist, marks)
        val m = Suggestions.merge("example", local, listOf("example", "example login", "Example Login", "example mail"), limit = 5)
        assertEquals(5, m.size)
        assertTrue(m.take(local.size).all { it.url != null })
        assertEquals(1, m.count { it.text.equals("example login", true) })
        assertFalse(m.any { it.kind == Suggestion.Kind.SEARCH && it.text == "example" })
    }

    @Test
    fun mergeWithNoLocalStillShowsTypedTextWhenGoogleReturnsIt() {
        val m = Suggestions.merge("kotlin", emptyList(), listOf("kotlin", "kotlin tutorial"))
        assertEquals(listOf("kotlin", "kotlin tutorial"), m.map { it.text })
    }
}
