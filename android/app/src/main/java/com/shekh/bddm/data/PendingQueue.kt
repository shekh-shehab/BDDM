package com.shekh.bddm.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Item 97: downloads requested in quick succession share one confirmation sheet
 * instead of replacing each other.
 */
data class PendingDownload(
    val url: String,
    val title: String,
    val subtitle: String = "",
    val thumb: String = "",
    val kind: Kind = Kind.File,
    val formats: List<VideoFormat> = emptyList(),
    var qualityId: String = "best",
    var selected: Boolean = true,
) {
    enum class Kind { File, Video }
}

object PendingQueue {
    private val _items = MutableStateFlow<List<PendingDownload>>(emptyList())
    val items: StateFlow<List<PendingDownload>> = _items
    private val _open = MutableStateFlow(false)
    val open: StateFlow<Boolean> = _open

    @Synchronized
    fun add(item: PendingDownload) {
        val cur = _items.value.toMutableList()
        if (cur.none { it.url == item.url }) cur.add(item)
        _items.value = cur
        _open.value = true
    }

    fun addAll(list: List<PendingDownload>) {
        list.forEach { add(it) }
    }

    fun update(list: List<PendingDownload>) { _items.value = list }
    fun close() { _open.value = false; _items.value = emptyList() }
    fun dismissKeep() { _open.value = false }
    fun isOpen() = _open.value
    fun size() = _items.value.size
}
