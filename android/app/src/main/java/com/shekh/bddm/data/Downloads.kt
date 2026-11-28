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
    /**
     * Regular file download on the multi-part engine (item 108). Headers carry the browser's
     * cookies/referer/user agent so links that need the browser session still work.
     */
    fun startHttp(url: String, later: Boolean, fileName: String = "", headers: Map<String, String> = emptyMap(), ctx: Context = com.shekh.bddm.BDDMApp.instance): String? {
        val direct = GoogleDrive.directUrl(url.trim())
        val h = HashMap(headers.filterValues { it.isNotBlank() })
        if (!h.containsKey("Cookie")) {
            try { CookieManager.getInstance().getCookie(direct)?.let { if (it.isNotBlank()) h["Cookie"] = it } } catch (_: Throwable) {}
        }
        if (!h.containsKey("User-Agent")) {
            try { h["User-Agent"] = FilterEngine.chromeLikeUa(WebSettings.getDefaultUserAgent(ctx)) } catch (_: Throwable) {}
        }
        val id = try {
            EngineHolder.engine?.addWithHeaders(direct, com.shekh.bddm.BDDMApp.instance.prefs.downloadDirFile().absolutePath,
                fileName, org.json.JSONObject(h as Map<*, *>).toString())
        } catch (_: Exception) { null }
        if (id != null) {
            if (later) EngineHolder.engine?.pause(id) else NetworkWatcher.holdIfNeeded(id)
        }
        EngineHolder.refresh()
        if (!later) kick(ctx)
        return id
    }

    /** Starts whatever the confirmation sheet holds. */
    fun startPending(ctx: Context, item: PendingDownload, later: Boolean) {
        when (item.kind) {
            PendingDownload.Kind.Video -> startVideo(ctx, item, later)
            else -> startHttp(item.url, later, item.fileName, item.headers + (if (item.referer.isNotBlank()) mapOf("Referer" to item.referer) else emptyMap()), ctx)
        }
    }

    /** Makes sure the foreground service is running to show progress; it stops itself when idle (bug 113). */
    fun kick(ctx: Context) {
        try {
            ContextCompat.startForegroundService(ctx, Intent(ctx, DownloadService::class.java).setAction(DownloadService.ACTION_KICK))
        } catch (_: Exception) { }
    }

    /** Item 47: hand a link to Android's own download manager when BDDM's engine cannot fetch it. */
    fun systemFallback(ctx: Context, url: String, fileName: String = ""): Boolean {
        return try {
            val dm = ctx.getSystemService(android.app.DownloadManager::class.java) ?: return false
            val name = fileName.ifBlank { android.webkit.URLUtil.guessFileName(url, null, null) }
            val r = android.app.DownloadManager.Request(android.net.Uri.parse(url))
                .setTitle(name)
                .setNotificationVisibility(android.app.DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                .setDestinationInExternalPublicDir(android.os.Environment.DIRECTORY_DOWNLOADS, "BDDM/$name")
            try { CookieManager.getInstance().getCookie(url)?.let { r.addRequestHeader("Cookie", it) } } catch (_: Throwable) {}
            try { r.addRequestHeader("User-Agent", FilterEngine.chromeLikeUa(WebSettings.getDefaultUserAgent(ctx))) } catch (_: Throwable) {}
            dm.enqueue(r)
            true
        } catch (_: Exception) { false }
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
