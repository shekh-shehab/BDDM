package com.shekh.bddm.data

import android.content.Context
import android.content.Intent
import android.webkit.CookieManager
import android.webkit.WebSettings
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import com.shekh.bddm.service.DownloadService

object Downloads {
    fun startHttp(url: String, later: Boolean) {
        val id = EngineHolder.engine?.add(url, com.shekh.bddm.BDDMApp.instance.prefs.downloadDirFile().absolutePath, "")
        if (later && id != null) EngineHolder.engine?.pause(id)
        EngineHolder.refresh()
    }

    fun startVideo(ctx: Context, item: PendingDownload, later: Boolean) {
        if (later) {
            LaterStore.add(item)
            return
        }
        LaterStore.remove(item.url)
        val i = Intent(ctx, DownloadService::class.java).apply {
            action = DownloadService.ACTION_VIDEO
            putExtra(DownloadService.EXTRA_URL, item.url)
            putExtra(DownloadService.EXTRA_FORMAT, item.qualityId)
            putExtra(DownloadService.EXTRA_DIR, com.shekh.bddm.BDDMApp.instance.prefs.downloadDirFile().absolutePath)
            putExtra(DownloadService.EXTRA_TITLE, item.title)
            putExtra(DownloadService.EXTRA_REFERER, item.referer)
            putExtra(DownloadService.EXTRA_COOKIE, try { CookieManager.getInstance().getCookie(item.url) ?: "" } catch (_: Throwable) { "" })
            putExtra(DownloadService.EXTRA_UA, try { WebSettings.getDefaultUserAgent(ctx) } catch (_: Throwable) { "" })
        }
        ContextCompat.startForegroundService(ctx, i)
    }

    /** Reads the available qualities in the background and updates the queued item in place. */
    fun enrichAsync(scope: CoroutineScope, url: String, referer: String = "") {
        markLoading(url, true)
        scope.launch {
            try {
                val info = withContext(Dispatchers.IO) { YtDlp.probe(url, referer) }
                val cur = PendingQueue.items.value.toMutableList()
                val i = cur.indexOfFirst { it.url == url }
                if (i >= 0) {
                    val old = cur[i]
                    cur[i] = old.copy(
                        title = info.title.ifBlank { old.title },
                        formats = info.formats,
                        subtitle = info.site.ifBlank { old.subtitle },
                        thumb = info.thumb.ifBlank { old.thumb },
                        loading = false,
                    )
                    PendingQueue.update(cur)
                }
            } catch (_: Throwable) {
                markLoading(url, false)
            }
        }
    }

    private fun markLoading(url: String, v: Boolean) {
        val cur = PendingQueue.items.value.toMutableList()
        val i = cur.indexOfFirst { it.url == url }
        if (i >= 0) { cur[i] = cur[i].copy(loading = v); PendingQueue.update(cur) }
    }
}
