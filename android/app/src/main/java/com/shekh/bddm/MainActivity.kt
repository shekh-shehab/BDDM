package com.shekh.bddm

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Download
import androidx.compose.material.icons.outlined.Language
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import com.shekh.bddm.browser.BrowserActivity
import com.shekh.bddm.data.*
import com.shekh.bddm.service.DownloadService
import com.shekh.bddm.ui.*
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        startService(Intent(this, DownloadService::class.java))
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        handleShare(intent)
        setContent { AppRoot() }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleShare(intent)
    }

    private fun handleShare(intent: Intent?) {
        val text = intent?.getStringExtra(Intent.EXTRA_TEXT)
            ?: intent?.dataString
            ?: return
        val url = extractUrl(text) ?: return
        PendingQueue.add(PendingDownload(url = url, title = url, kind = if (YtDlp.isSitePage(url) || YtDlp.looksLikeMedia(url)) PendingDownload.Kind.Video else PendingDownload.Kind.File))
    }
}

@Composable
fun AppRoot() {
    val prefs = BDDMApp.instance.prefs
    var tab by remember { mutableStateOf(0) }
    val snapJson by EngineHolder.snap.collectAsState()
    val snap = remember(snapJson) { parseSnap(snapJson) }
    var filter by remember { mutableStateOf("All") }
    var query by remember { mutableStateOf("") }
    var detail by remember { mutableStateOf<Job?>(null) }
    var showAdd by remember { mutableStateOf(false) }
    var addInitial by remember { mutableStateOf("") }
    val pending by PendingQueue.items.collectAsState()
    val sheetOpen by PendingQueue.open.collectAsState()
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()

    BDDMTheme(prefs.theme) {
        if (detail != null) {
            DetailScreen(
                job = snap.jobs.firstOrNull { it.id == detail!!.id } ?: detail!!,
                onBack = { detail = null },
                onPause = { EngineHolder.engine?.pause(detail!!.id); EngineHolder.refresh() },
                onResume = { EngineHolder.engine?.resume(detail!!.id); EngineHolder.refresh() },
                onDelete = { also ->
                    EngineHolder.engine?.delete(detail!!.id, also)
                    EngineHolder.refresh()
                    detail = null
                },
            )
        } else {
            Scaffold(
                bottomBar = {
                    NavigationBar {
                        NavigationBarItem(selected = tab == 0, onClick = { tab = 0 },
                            icon = { Icon(Icons.Outlined.Download, null) }, label = { Text("Downloads") })
                        NavigationBarItem(selected = false, onClick = {
                            ctx.startActivity(Intent(ctx, BrowserActivity::class.java))
                        }, icon = { Icon(Icons.Outlined.Language, null) }, label = { Text("Browser") })
                        NavigationBarItem(selected = tab == 2, onClick = { tab = 2 },
                            icon = { Icon(Icons.Outlined.Settings, null) }, label = { Text("Settings") })
                    }
                }
            ) { pad ->
                androidx.compose.foundation.layout.Box(Modifier.padding(pad)) {
                    when (tab) {
                        2 -> SettingsScreen()
                        else -> DownloadListScreen(
                            snap = snap,
                            query = query,
                            onQuery = { query = it },
                            filter = filter,
                            onFilter = { filter = it },
                            onOpen = { detail = it },
                            onAdd = { addInitial = query; showAdd = true },
                            onPaste = { url ->
                                PendingQueue.add(PendingDownload(url = url, title = url))
                            },
                        )
                    }
                }
            }
        }
        if (showAdd && !sheetOpen) {
            AddDownloadSheet(
                initial = addInitial,
                onDismiss = { showAdd = false },
                onStart = { url, later ->
                    showAdd = false
                    Downloads.startHttp(url, later)
                },
            )
        }
        if (sheetOpen && pending.isNotEmpty()) {
            ConfirmQueueSheet(
                items = pending,
                onChange = { PendingQueue.update(it) },
                onDismiss = { PendingQueue.close() },
                onConfirm = { later ->
                    val selected = pending.filter { it.selected }
                    PendingQueue.close()
                    selected.forEach { item ->
                        when (item.kind) {
                            PendingDownload.Kind.Video -> Downloads.startVideo(ctx, item, later)
                            else -> Downloads.startHttp(item.url, later)
                        }
                    }
                },
            )
        }
    }
}
