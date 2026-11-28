package com.shekh.bddm

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.lifecycle.lifecycleScope
import com.shekh.bddm.data.*
import com.shekh.bddm.ui.BDDMTheme
import com.shekh.bddm.ui.ConfirmQueueSheet
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class ShareActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val text = intent?.getStringExtra(Intent.EXTRA_TEXT)
            ?: intent?.dataString
            ?: ""
        val url = extractUrl(text) ?: text.trim()
        if (url.startsWith("magnet:", true) || url.isBlank()) {
            finish(); return
        }
        val kind = if (YtDlp.isSitePage(url) || YtDlp.looksLikeMedia(url) || YtDlp.isHlsOrDash(url))
            PendingDownload.Kind.Video else PendingDownload.Kind.File
        PendingQueue.add(PendingDownload(url = url, title = url, subtitle = url, kind = kind))
        if (kind == PendingDownload.Kind.Video) Downloads.enrichAsync(lifecycleScope, url)
        setContent {
            val prefs = BDDMApp.instance.prefs
            val items by PendingQueue.items.collectAsState()
            val open by PendingQueue.open.collectAsState()
            BDDMTheme(prefs.theme) {
                if (open && items.isNotEmpty()) {
                    ConfirmQueueSheet(
                        items = items,
                        onChange = { PendingQueue.update(it) },
                        onDismiss = { PendingQueue.close(); finish() },
                        onConfirm = { later ->
                            val selected = items.filter { it.selected }
                            PendingQueue.close()
                            selected.forEach { item ->
                                when (item.kind) {
                                    PendingDownload.Kind.Video -> Downloads.startVideo(this, item, later)
                                    else -> Downloads.startHttp(item.url, later)
                                }
                            }
                            finish()
                        },
                    )
                } else {
                    finish()
                }
            }
        }
    }
}
