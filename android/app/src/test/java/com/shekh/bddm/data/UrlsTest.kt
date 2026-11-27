package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Test

class UrlsTest {
    @Test
    fun extractsFirstHttpUrl() {
        assertEquals("https://youtu.be/abc", extractUrl("watch this https://youtu.be/abc please"))
        assertEquals("http://cdn.example/v.mp4", extractUrl("http://cdn.example/v.mp4."))
        assertNull(extractUrl("no link here"))
    }
}
