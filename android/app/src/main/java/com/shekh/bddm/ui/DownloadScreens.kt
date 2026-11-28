package com.shekh.bddm.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.shekh.bddm.data.*

@Composable
fun DownloadListScreen(
    snap: Snap,
    query: String,
    onQuery: (String) -> Unit,
    filter: String,
    onFilter: (String) -> Unit,
    onOpen: (Job) -> Unit,
    onAdd: () -> Unit,
    onPaste: (String) -> Unit,
) {
    val filtered = remember(snap, filter, query) {
        snap.jobs.filter { j ->
            val q = query.isBlank() || j.name.contains(query, true) || j.url.contains(query, true)
            val f = when (filter) {
                "Active" -> j.status == "Downloading" || j.status == "Queued"
                "Completed" -> j.status == "Done"
                "Error" -> j.status == "Error"
                else -> true
            }
            q && f
        }
    }
    val later by LaterStore.items.collectAsState()
    val allVideo by VideoJobs.jobs.collectAsState()
    val videos = remember(allVideo, filter, query) {
        allVideo.filter { v ->
            val q = query.isBlank() || v.title.contains(query, true) || v.url.contains(query, true)
            val f = when (filter) {
                "Active" -> v.active
                "Completed" -> v.status == "Done"
                "Error" -> v.status == "Error" || v.status == "Cancelled"
                else -> true
            }
            q && f
        }
    }
    val ctx = androidx.compose.ui.platform.LocalContext.current
    Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) {
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("BDDM", fontSize = 20.sp, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onBackground)
        }
        OutlinedTextField(
            value = query,
            onValueChange = {
                onQuery(it)
                if (it.trim().startsWith("http://") || it.trim().startsWith("https://")) {
                    // leave as search; user taps Add to confirm
                }
            },
            placeholder = { Text("Search or paste a link, video, or magnet", fontSize = 13.sp) },
            leadingIcon = { Icon(Icons.Default.Search, null) },
            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
            singleLine = true,
            shape = RoundedCornerShape(24.dp),
        )
        Spacer(Modifier.height(8.dp))
        Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            val counts = mapOf(
                "All" to snap.jobs.size + allVideo.size,
                "Active" to snap.jobs.count { it.status == "Downloading" || it.status == "Queued" } + allVideo.count { it.active },
                "Completed" to snap.done + allVideo.count { it.status == "Done" },
                "Error" to snap.error + allVideo.count { it.status == "Error" || it.status == "Cancelled" },
            )
            counts.forEach { (name, n) ->
                FilterChip(
                    selected = filter == name,
                    onClick = { onFilter(name) },
                    label = { Text("$name $n", fontSize = 12.sp) },
                )
            }
        }
        Spacer(Modifier.height(8.dp))
        Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            val vActive = allVideo.filter { it.status == "Downloading" }
            val totalSpeed = snap.speed + vActive.sumOf { it.speed }
            val vEta = vActive.maxOfOrNull { it.eta } ?: -1L
            StatCard("Speed", formatSpeed(totalSpeed), Modifier.weight(1f))
            StatCard("Time left", formatEta(maxOf(snap.eta, vEta)), Modifier.weight(1f))
            StatCard("Upload", formatSpeed(snap.upload), Modifier.weight(1f))
        }
        Spacer(Modifier.height(8.dp))
        Box(Modifier.weight(1f)) {
            if (filtered.isEmpty() && later.isEmpty() && videos.isEmpty()) {
                Text(
                    "No downloads yet.\nPaste a link or share one to BDDM.",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.align(Alignment.Center).padding(24.dp),
                )
            } else {
                LazyColumn(contentPadding = PaddingValues(12.dp, 0.dp, 12.dp, 88.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (later.isNotEmpty() && (filter == "All" || filter == "Active")) {
                        items(later, key = { "later-"+it.url }) { item ->
                            LaterCard(item, onStart = { Downloads.startVideo(ctx, item, later = false) }, onRemove = { LaterStore.remove(item.url) })
                        }
                    }
                    items(videos, key = { "video-" + it.id }) { v ->
                        VideoJobCard(
                            v,
                            onCancel = {
                                val i = android.content.Intent(ctx, com.shekh.bddm.service.DownloadService::class.java).apply {
                                    action = com.shekh.bddm.service.DownloadService.ACTION_CANCEL
                                    putExtra(com.shekh.bddm.service.DownloadService.EXTRA_JOB_ID, v.id)
                                }
                                androidx.core.content.ContextCompat.startForegroundService(ctx, i)
                            },
                            onRetry = {
                                VideoJobs.remove(v.id)
                                Downloads.startVideo(ctx, PendingDownload(v.url, v.title, kind = PendingDownload.Kind.Video, qualityId = v.format, referer = v.referer), later = false)
                            },
                            onRemove = { VideoJobs.remove(v.id) },
                        )
                    }
                    items(filtered, key = { it.id }) { job ->
                        JobCard(job, onClick = { onOpen(job) })
                    }
                }
            }
            FloatingActionButton(
                onClick = {
                    val t = query.trim()
                    if (t.startsWith("http://") || t.startsWith("https://")) onPaste(t) else onAdd()
                },
                modifier = Modifier.align(Alignment.BottomEnd).padding(18.dp),
                containerColor = PrimaryBtn,
                contentColor = OnPrimaryBtn,
            ) { Icon(Icons.Default.Add, "Add download") }
        }
    }
}


@Composable
private fun LaterCard(item: PendingDownload, onStart: () -> Unit, onRemove: () -> Unit) {
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp))
            .background(MaterialTheme.colorScheme.surface)
            .border(1.dp, MaterialTheme.colorScheme.outline.copy(alpha = 0.2f), RoundedCornerShape(18.dp))
            .padding(12.dp)
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(item.title.ifBlank { item.url }, fontWeight = FontWeight.SemiBold, fontSize = 14.sp,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            StatusBadge("Later")
        }
        Spacer(Modifier.height(6.dp))
        Text(item.subtitle.ifBlank { item.url }, fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            TextButton(onClick = onRemove) { Text("Remove") }
            Button(onClick = onStart) { Text("Start") }
        }
    }
}

@Composable
private fun VideoJobCard(v: VideoJob, onCancel: () -> Unit, onRetry: () -> Unit, onRemove: () -> Unit) {
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp))
            .background(MaterialTheme.colorScheme.surface)
            .border(1.dp, MaterialTheme.colorScheme.outline.copy(alpha = 0.2f), RoundedCornerShape(18.dp))
            .padding(12.dp)
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(v.title.ifBlank { v.url }, fontWeight = FontWeight.SemiBold, fontSize = 14.sp,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            StatusBadge(if (v.status == "Cancelled") "Error" else v.status)
        }
        Spacer(Modifier.height(8.dp))
        if (v.active && v.percent <= 0f) {
            LinearProgressIndicator(
                modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(99.dp)),
                color = AccentDark, trackColor = MaterialTheme.colorScheme.surfaceVariant, strokeCap = StrokeCap.Round,
            )
        } else {
            LinearProgressIndicator(
                progress = { (v.percent / 100f).coerceIn(0f, 1f) },
                modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(99.dp)),
                color = if (v.status == "Done") SuccessDark else AccentDark,
                trackColor = MaterialTheme.colorScheme.surfaceVariant,
                strokeCap = StrokeCap.Round,
            )
        }
        Spacer(Modifier.height(6.dp))
        val meta = buildString {
            append(if (v.format.startsWith("audio:")) "Audio (MP3)" else "Video")
            if (v.status == "Downloading" && v.percent > 0f) append("  ·  ").append(v.percent.toInt()).append("%")
            if (v.total > 0) {
                append("  ·  ")
                if (v.status == "Downloading") append(formatBytes(v.doneBytes)).append(" / ")
                append(formatBytes(v.total))
            }
            if (v.status == "Downloading" && v.speed > 0) append("  ·  ").append(formatSpeed(v.speed))
            if (v.status == "Downloading" && v.eta > 0) append("  ·  ").append(formatEta(v.eta)).append(" left")
            if (v.message.isNotBlank()) append("  ·  ").append(v.message)
            if (v.status == "Done" && v.file.isNotBlank()) append("  ·  ").append(v.file.substringAfterLast('/'))
        }
        Text(meta, fontSize = 11.sp, color = if (v.status == "Error") DangerDark else MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            if (v.active) TextButton(onClick = onCancel) { Text("Cancel") }
            else {
                if (v.status == "Error" || v.status == "Cancelled") TextButton(onClick = onRetry) { Text("Retry") }
                TextButton(onClick = onRemove) { Text("Remove") }
            }
        }
    }
}

@Composable
private fun StatCard(label: String, value: String, modifier: Modifier = Modifier) {
    Column(
        modifier.clip(RoundedCornerShape(14.dp)).background(MaterialTheme.colorScheme.surfaceVariant).padding(10.dp)
    ) {
        Text(label, fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(value, fontSize = 15.sp, fontWeight = FontWeight.SemiBold, maxLines = 1)
    }
}

@Composable
fun JobCard(job: Job, onClick: () -> Unit) {
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp))
            .background(MaterialTheme.colorScheme.surface)
            .border(1.dp, MaterialTheme.colorScheme.outline.copy(alpha = 0.2f), RoundedCornerShape(18.dp))
            .clickable(onClick = onClick)
            .padding(12.dp)
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(job.name.ifBlank { job.url }, fontWeight = FontWeight.SemiBold, fontSize = 14.sp,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            StatusBadge(job.status)
        }
        Spacer(Modifier.height(8.dp))
        if (job.segs != null && job.segs.segs.size >= 2) {
            PartBar(job.segs)
        } else {
            val p = if (job.size > 0) (job.done.toFloat() / job.size).coerceIn(0f, 1f) else 0f
            LinearProgressIndicator(
                progress = { p },
                modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(99.dp)),
                color = if (job.status == "Done") SuccessDark else if (job.status == "Paused") OrangeDark else AccentDark,
                trackColor = MaterialTheme.colorScheme.surfaceVariant,
                strokeCap = StrokeCap.Round,
            )
        }
        Spacer(Modifier.height(6.dp))
        val meta = buildString {
            append(formatBytes(job.done))
            if (job.size > 0) append(" / ").append(formatBytes(job.size))
            if (job.status == "Downloading") append("  ·  ").append(formatSpeed(job.speed)).append("  ·  ").append(formatEta(job.eta))
            if (job.status == "Error" && job.error.isNotBlank()) append("  ·  ").append(job.error)
        }
        Text(meta, fontSize = 11.sp, color = if (job.status == "Error") DangerDark else MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2)
    }
}

@Composable
fun PartBar(segs: Segs) {
    Row(Modifier.fillMaxWidth().height(8.dp), horizontalArrangement = Arrangement.spacedBy(3.dp)) {
        segs.segs.forEach { s ->
            val w = s.w.coerceAtLeast(1)
            val frac = (s.done.toFloat() / w).coerceIn(0f, 1f)
            val color = when (s.state) {
                "done" -> SuccessDark
                "active" -> AccentDark
                "retry" -> OrangeDark
                else -> MaterialTheme.colorScheme.surfaceVariant
            }
            Box(Modifier.weight(w.toFloat()).fillMaxHeight().clip(RoundedCornerShape(99.dp)).background(MaterialTheme.colorScheme.surfaceVariant)) {
                Box(Modifier.fillMaxHeight().fillMaxWidth(frac).background(color))
            }
        }
    }
}

@Composable
fun StatusBadge(status: String) {
    val (fg, bg) = when (status) {
        "Downloading", "Queued" -> TextAccentDark to BgAccentDark
        "Done" -> Color(0xFF86EFAC) to Color(0xFF052E16)
        "Error" -> Color(0xFFFCA5A5) to Color(0xFF450A0A)
        "Paused" -> Color(0xFFFCD34D) to Color(0xFF2B1A05)
        else -> Text2Dark to NeutralDark
    }
    Text(
        status,
        color = fg,
        fontSize = 10.sp,
        fontWeight = FontWeight.SemiBold,
        modifier = Modifier.clip(RoundedCornerShape(99.dp)).background(bg).padding(horizontal = 8.dp, vertical = 2.dp),
    )
}

@Composable
fun DetailScreen(job: Job, onBack: () -> Unit, onPause: () -> Unit, onResume: () -> Unit, onDelete: (Boolean) -> Unit) {
    var confirm by remember { mutableStateOf(false) }
    var linkDialog by remember { mutableStateOf(false) }
    var newLink by remember { mutableStateOf("") }
    var linkMsg by remember { mutableStateOf("") }
    Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) {
        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onBack) { Icon(Icons.Default.ArrowBack, "Back") }
            Text("Download details", fontWeight = FontWeight.SemiBold, fontSize = 18.sp)
        }
        Column(Modifier.padding(16.dp).clip(RoundedCornerShape(18.dp)).background(MaterialTheme.colorScheme.surface).padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(job.name, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f), maxLines = 2)
                StatusBadge(job.status)
            }
            Text(job.url, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(12.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                StatCard("Speed", formatSpeed(job.speed), Modifier.weight(1f))
                StatCard("Time left", formatEta(job.eta), Modifier.weight(1f))
                val pct = if (job.size > 0) "${(job.done * 100 / job.size)}%" else "—"
                StatCard("Progress", pct, Modifier.weight(1f))
            }
            Spacer(Modifier.height(10.dp))
            if (job.segs != null) PartBar(job.segs) else {
                val p = if (job.size > 0) job.done.toFloat() / job.size else 0f
                LinearProgressIndicator(progress = { p }, modifier = Modifier.fillMaxWidth().height(8.dp).clip(RoundedCornerShape(99.dp)))
            }
            Text("${formatBytes(job.done)} / ${formatBytes(job.size)}" + if (job.segs != null) "  ·  ${job.segs.active} active, ${job.segs.finished} done, ${job.segs.retrying} retrying" else "",
                fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp))
            if (job.error.isNotBlank()) Text(job.error, color = DangerDark, fontSize = 13.sp, modifier = Modifier.padding(top = 8.dp))
            Spacer(Modifier.height(12.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (job.status == "Downloading" || job.status == "Queued") {
                    OutlinedButton(onClick = onPause, modifier = Modifier.weight(1f)) { Text("Pause") }
                } else if (job.status != "Done") {
                    Button(onClick = onResume, modifier = Modifier.weight(1f)) { Text("Resume") }
                }
                OutlinedButton(onClick = { confirm = true }, modifier = Modifier.weight(1f)) { Text("Delete") }
            }
            if (job.status == "Paused" || job.status == "Error") {
                val ctx = androidx.compose.ui.platform.LocalContext.current
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 8.dp)) {
                    // Item 44: replace an expired link and continue from where it stopped.
                    TextButton(onClick = { newLink = job.url; linkDialog = true }) { Text("Change link") }
                    // Item 47: let Android's own downloader try with the browser cookies.
                    TextButton(onClick = {
                        linkMsg = if (Downloads.systemFallback(ctx, job.url, job.name)) "Sent to Android's downloader. Check the notification." else "Android's downloader could not take this link."
                    }) { Text("Try system downloader") }
                }
                if (linkMsg.isNotBlank()) Text(linkMsg, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
    if (linkDialog) {
        AlertDialog(
            onDismissRequest = { linkDialog = false },
            title = { Text("Change link") },
            text = {
                Column {
                    Text("Paste a fresh link for the same file. Downloaded parts are kept when the size matches.", fontSize = 13.sp)
                    OutlinedTextField(value = newLink, onValueChange = { newLink = it }, singleLine = true, modifier = Modifier.fillMaxWidth().padding(top = 8.dp))
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    linkDialog = false
                    linkMsg = try { EngineHolder.engine?.updateURL(job.id, newLink.trim()); EngineHolder.engine?.resume(job.id); EngineHolder.refresh(); "Link updated." }
                        catch (e: Exception) { e.message ?: "Could not change the link." }
                }) { Text("Save and resume") }
            },
            dismissButton = { TextButton(onClick = { linkDialog = false }) { Text("Cancel") } },
        )
    }
    if (confirm) {
        AlertDialog(
            onDismissRequest = { confirm = false },
            title = { Text("Delete download?") },
            text = { Text("Also delete the file from the device?") },
            confirmButton = { TextButton(onClick = { confirm = false; onDelete(true) }) { Text("Delete file") } },
            dismissButton = {
                Row {
                    TextButton(onClick = { confirm = false; onDelete(false) }) { Text("Remove from list") }
                    TextButton(onClick = { confirm = false }) { Text("Cancel") }
                }
            },
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddDownloadSheet(initial: String, onDismiss: () -> Unit, onStart: (url: String, later: Boolean) -> Unit) {
    var url by remember { mutableStateOf(initial) }
    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(Modifier.padding(horizontal = 20.dp, vertical = 8.dp).navigationBarsPadding()) {
            Text("Add download", fontWeight = FontWeight.SemiBold, fontSize = 20.sp)
            Spacer(Modifier.height(12.dp))
            OutlinedTextField(value = url, onValueChange = { url = it }, label = { Text("Link") }, modifier = Modifier.fillMaxWidth(), singleLine = true)
            Text("Paste an http or https file link. Videos from YouTube, TikTok, Instagram and other sites can also be added from the BDDM browser.",
                fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 8.dp))
            Spacer(Modifier.height(16.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                TextButton(onClick = { onStart(url, true) }) { Text("Download later") }
                Spacer(Modifier.weight(1f))
                TextButton(onClick = onDismiss) { Text("Cancel") }
                Button(onClick = { onStart(url, false) }, enabled = url.startsWith("http")) { Text("Start download") }
            }
            Spacer(Modifier.height(12.dp))
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConfirmQueueSheet(
    items: List<PendingDownload>,
    onChange: (List<PendingDownload>) -> Unit,
    onDismiss: () -> Unit,
    onConfirm: (later: Boolean) -> Unit,
) {
    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(Modifier.padding(horizontal = 20.dp, vertical = 8.dp).navigationBarsPadding()) {
            Text(if (items.size == 1) "Download with BDDM" else "${items.size} downloads", fontWeight = FontWeight.SemiBold, fontSize = 20.sp)
            Spacer(Modifier.height(8.dp))
            items.forEachIndexed { i, p ->
                Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = p.selected, onCheckedChange = { checked ->
                        onChange(items.toMutableList().also { it[i] = p.copy(selected = checked) })
                    })
                    Column(Modifier.weight(1f)) {
                        Text(p.title, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(p.subtitle.ifBlank { p.url }, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (p.loading && p.formats.isEmpty()) {
                            Text("Reading available qualities…", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        if (p.formats.isNotEmpty()) {
                            var expanded by remember { mutableStateOf(false) }
                            val selected = p.formats.firstOrNull { it.id == p.qualityId } ?: p.formats.first()
                            Box {
                                TextButton(onClick = { expanded = true }) { Text(selected.label) }
                                DropdownMenu(expanded, { expanded = false }) {
                                    p.formats.forEach { f ->
                                        DropdownMenuItem(text = { Text(buildString {
                                            append(f.label)
                                            if (f.size > 0) append("  ·  ").append(formatBytes(f.size))
                                        }) }, onClick = {
                                            onChange(items.toMutableList().also { it[i] = p.copy(qualityId = f.id) })
                                            expanded = false
                                        })
                                    }
                                }
                            }
                        }
                    }
                }
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                TextButton(onClick = { onConfirm(true) }) { Text("Download later") }
                Spacer(Modifier.weight(1f))
                TextButton(onClick = onDismiss) { Text("Cancel") }
                Button(onClick = { onConfirm(false) }) { Text(if (items.size == 1) "Start download" else "Download selected") }
            }
            Spacer(Modifier.height(12.dp))
        }
    }
}
