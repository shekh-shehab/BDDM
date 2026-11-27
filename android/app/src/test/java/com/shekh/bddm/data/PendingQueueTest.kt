package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Before
import org.junit.Test

class PendingQueueTest {
    @Before
    fun reset() {
        PendingQueue.close()
    }

    @Test
    fun accumulatesSuccessiveAddsInOneSheet() {
        PendingQueue.add(PendingDownload("https://a.example/1", "one"))
        PendingQueue.add(PendingDownload("https://b.example/2", "two"))
        assertEquals(2, PendingQueue.size())
        assertTrue(PendingQueue.isOpen())
        assertEquals("https://a.example/1", PendingQueue.items.value[0].url)
        assertEquals("https://b.example/2", PendingQueue.items.value[1].url)
    }

    @Test
    fun ignoresDuplicateUrl() {
        PendingQueue.add(PendingDownload("https://a.example/1", "one"))
        PendingQueue.add(PendingDownload("https://a.example/1", "one again"))
        assertEquals(1, PendingQueue.size())
    }

    @Test
    fun closeClearsItemsAndSheet() {
        PendingQueue.add(PendingDownload("https://a.example/1", "one"))
        PendingQueue.close()
        assertEquals(0, PendingQueue.size())
        assertFalse(PendingQueue.isOpen())
    }
}
