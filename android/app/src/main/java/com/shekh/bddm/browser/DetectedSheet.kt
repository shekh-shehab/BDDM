package com.shekh.bddm.browser

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.shekh.bddm.data.*

/** What the user picked: one quality of one video. `audioOnly` converts to MP3. */
data class MediaPick(val group: MediaGroup, val option: QualityOption, val audioOnly: Boolean = false)

/**
 * Item 112: modern "Found on this page" sheet. Variants of one video are grouped, the best
 * quality is preselected, tiny files and ads are hidden, with multi-select, sort and filter.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DetectedMediaSheet(
    items: List<DetectedMedia>,
    pageThumb: String,
    onDismiss: () -> Unit,
    onDownload: (List<MediaPick>, later: Boolean) -> Unit,
    onPreview: (MediaPick) -> Unit,
    onCopy: (String) -> Unit,
    onRemove: (List<String>) -> Unit,
    onOpenDownloads: () -> Unit,
) {
    var sort by remember { mutableStateOf(MediaSort.QUALITY) }
    var filter by remember { mutableStateOf(MediaFilter.ALL) }
    var showSmall by remember { mutableStateOf(false) }
    var menu by remember { mutableStateOf(false) }
    val allGroups = remember(items, showSmall) { MediaGrouping.group(items, hideSmall = !showSmall) }
    val shown = remember(allGroups, sort, filter) { MediaGrouping.sort(MediaGrouping.filter(allGroups, filter), sort) }
    val selected = remember { mutableStateMapOf<String, Boolean>() }
    val choice = remember { mutableStateMapOf<String, Int>() }
    // Preselect the top result once.
    LaunchedEffect(allGroups.isNotEmpty()) {
        if (selected.isEmpty()) MediaGrouping.sort(allGroups, MediaSort.QUALITY).firstOrNull()?.let { selected[it.key] = true }
    }
    val picks = shown.filter { selected[it.key] == true }.map { g ->
        MediaPick(g, g.options.getOrElse(choice[g.key] ?: 0) { g.best })
    }
    val sheet = rememberModalBottomSheetState(skipPartiallyExpanded = true)

    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = sheet) {
        Column(Modifier.fillMaxWidth().fillMaxHeight(0.92f).navigationBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(start = 20.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("Found on this page", fontWeight = FontWeight.Bold, fontSize = 20.sp)
                    Text(
                        "${shown.size} item${if (shown.size == 1) "" else "s"}" + if (picks.isNotEmpty()) " · ${picks.size} selected" else "",
                        fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                Box {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Default.Sort, "Sort and options") }
                    DropdownMenu(menu, { menu = false }) {
                        listOf(MediaSort.QUALITY to "Sort by quality", MediaSort.SIZE to "Sort by size",
                            MediaSort.NAME to "Sort by name", MediaSort.NEWEST to "Newest first").forEach { (s, label) ->
                            DropdownMenuItem(
                                text = { Text(label) },
                                leadingIcon = { if (sort == s) Icon(Icons.Default.Check, null) else Spacer(Modifier.size(24.dp)) },
                                onClick = { sort = s; menu = false },
                            )
                        }
                        HorizontalDivider()
                        DropdownMenuItem(
                            text = { Text(if (showSmall) "Hide small files" else "Show small files") },
                            onClick = { showSmall = !showSmall; menu = false },
                        )
                        DropdownMenuItem(
                            text = { Text("Reset selection") },
                            onClick = { selected.clear(); choice.clear(); menu = false },
                        )
                        DropdownMenuItem(
                            text = { Text("Clear list") },
                            onClick = { onRemove(items.map { it.url }); selected.clear(); menu = false },
                        )
                    }
                }
                IconButton(onClick = onDismiss) { Icon(Icons.Default.Close, "Close") }
            }
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 6.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically,
            ) {
                listOf(MediaFilter.ALL to "All", MediaFilter.VIDEO to "Video", MediaFilter.AUDIO to "Audio", MediaFilter.STREAMS to "Streams").forEach { (f, label) ->
                    val n = MediaGrouping.filter(allGroups, f).size
                    FilterChip(selected = filter == f, onClick = { filter = f }, label = { Text("$label $n") })
                }
                val allOn = shown.isNotEmpty() && shown.all { selected[it.key] == true }
                TextButton(onClick = { shown.forEach { selected[it.key] = !allOn } }) { Text(if (allOn) "Select none" else "Select all") }
            }
            if (shown.isEmpty()) {
                Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    Text(
                        if (items.isEmpty()) "Nothing found yet.\nPress play on the video, then open this again."
                        else "Only small files or ads were found.\nUse the sort menu to show small files.",
                        color = MaterialTheme.colorScheme.onSurfaceVariant, fontSize = 14.sp,
                        modifier = Modifier.padding(24.dp),
                    )
                }
            } else {
                LazyColumn(
                    Modifier.weight(1f),
                    contentPadding = PaddingValues(horizontal = 14.dp, vertical = 6.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    items(shown, key = { it.key }) { g ->
                        MediaCard(
                            g = g,
                            pageThumb = pageThumb,
                            isSelected = selected[g.key] == true,
                            chosen = choice[g.key] ?: 0,
                            onToggle = { selected[g.key] = !(selected[g.key] ?: false) },
                            onChoose = { i -> choice[g.key] = i; selected[g.key] = true },
                            onPreview = { onPreview(MediaPick(g, g.options.getOrElse(choice[g.key] ?: 0) { g.best })) },
                            onAudio = { onDownload(listOf(MediaPick(g, g.best, audioOnly = true)), false) },
                            onCopy = { onCopy(g.options.getOrElse(choice[g.key] ?: 0) { g.best }.item.url) },
                            onRemove = { onRemove(g.options.map { it.item.url }.distinct()); selected.remove(g.key) },
                        )
                    }
                }
            }
            HorizontalDivider()
            Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                OutlinedButton(onClick = { onDownload(picks, true) }, enabled = picks.isNotEmpty(), modifier = Modifier.height(52.dp)) {
                    Text("Later")
                }
                Spacer(Modifier.width(10.dp))
                Button(
                    onClick = { onDownload(picks, false) },
                    enabled = picks.isNotEmpty(),
                    modifier = Modifier.weight(1f).height(52.dp),
                    shape = RoundedCornerShape(16.dp),
                ) {
                    Icon(Icons.Default.Download, null)
                    Spacer(Modifier.width(8.dp))
                    val total = picks.sumOf { it.option.sizeBytes }
                    Text(
                        if (picks.isEmpty()) "Select to download"
                        else "Download ${picks.size}" + if (total > 0) " · ${formatBytes(total)}" else "",
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            }
            TextButton(onClick = onOpenDownloads, modifier = Modifier.align(Alignment.CenterHorizontally)) { Text("Open BDDM downloads") }
        }
    }
}

@Composable
private fun MediaCard(
    g: MediaGroup,
    pageThumb: String,
    isSelected: Boolean,
    chosen: Int,
    onToggle: () -> Unit,
    onChoose: (Int) -> Unit,
    onPreview: () -> Unit,
    onAudio: () -> Unit,
    onCopy: () -> Unit,
    onRemove: () -> Unit,
) {
    val opt = g.options.getOrElse(chosen) { g.best }
    val border = if (isSelected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline.copy(alpha = 0.18f)
    var more by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(20.dp))
            .background(if (isSelected) MaterialTheme.colorScheme.primary.copy(alpha = 0.08f) else MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.45f))
            .border(if (isSelected) 2.dp else 1.dp, border, RoundedCornerShape(20.dp))
            .clickable(onClick = onToggle)
            .padding(10.dp)
    ) {
        Row(verticalAlignment = Alignment.Top) {
            Thumb(g, pageThumb, opt.item.pageUrl)
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Text(g.title, fontWeight = FontWeight.SemiBold, fontSize = 15.sp, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Spacer(Modifier.height(6.dp))
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    QualityChip(opt.label, opt.height, g.kind)
                    val meta = listOfNotNull(
                        if (opt.sizeBytes > 0) (if (g.kind == MediaKind.HLS || g.kind == MediaKind.DASH) "≈ " else "") + formatBytes(opt.sizeBytes) else null,
                        opt.item.host.removePrefix("www.").ifBlank { null },
                    ).joinToString(" · ")
                    Text(meta, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                Text(g.kind.label, fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 2.dp))
            }
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Checkbox(checked = isSelected, onCheckedChange = { onToggle() })
                Box {
                    IconButton(onClick = { more = true }, modifier = Modifier.size(32.dp)) { Icon(Icons.Default.MoreVert, "More") }
                    DropdownMenu(more, { more = false }) {
                        if (g.kind != MediaKind.PAGE && g.kind != MediaKind.EMBED) {
                            DropdownMenuItem(text = { Text("Preview / stream") }, leadingIcon = { Icon(Icons.Default.PlayArrow, null) },
                                onClick = { more = false; onPreview() })
                        }
                        if (g.kind != MediaKind.AUDIO) {
                            DropdownMenuItem(text = { Text("Audio only (MP3)") }, leadingIcon = { Icon(Icons.Default.MusicNote, null) },
                                onClick = { more = false; onAudio() })
                        }
                        DropdownMenuItem(text = { Text("Copy link") }, leadingIcon = { Icon(Icons.Default.ContentCopy, null) },
                            onClick = { more = false; onCopy() })
                        DropdownMenuItem(text = { Text("Remove from list") }, leadingIcon = { Icon(Icons.Default.Delete, null) },
                            onClick = { more = false; onRemove() })
                    }
                }
            }
        }
        if (g.options.size > 1) {
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(top = 8.dp),
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                g.options.forEachIndexed { i, o ->
                    FilterChip(
                        selected = i == chosen,
                        onClick = { onChoose(i) },
                        label = { Text(o.label + if (o.sizeBytes > 0) " · ${formatBytes(o.sizeBytes)}" else "", fontSize = 12.sp) },
                    )
                }
            }
        }
    }
}

@Composable
private fun Thumb(g: MediaGroup, pageThumb: String, referer: String) {
    val key = g.thumb.ifBlank { pageThumb }
    val bmp by produceState(initialValue = ThumbCache.get(key), key) {
        if (value == null && key.isNotBlank()) value = ThumbCache.load(key, referer)
    }
    Box(
        Modifier.size(width = 120.dp, height = 68.dp).clip(RoundedCornerShape(14.dp))
            .background(Brush.linearGradient(listOf(Color(0xFF1E3A8A), Color(0xFF6D28D9)))),
        contentAlignment = Alignment.Center,
    ) {
        val b = bmp
        if (b != null) {
            Image(b.asImageBitmap(), null, contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize())
        } else {
            Icon(if (g.kind == MediaKind.AUDIO) Icons.Default.MusicNote else Icons.Default.PlayCircle, null, tint = Color.White.copy(alpha = 0.85f), modifier = Modifier.size(30.dp))
        }
        val d = MediaGrouping.formatDuration(g.durationSec)
        if (d.isNotBlank()) {
            Text(
                d, color = Color.White, fontSize = 10.sp, fontWeight = FontWeight.SemiBold,
                modifier = Modifier.align(Alignment.BottomEnd).padding(4.dp).clip(RoundedCornerShape(6.dp))
                    .background(Color.Black.copy(alpha = 0.7f)).padding(horizontal = 5.dp, vertical = 1.dp),
            )
        }
    }
}

@Composable
private fun QualityChip(label: String, height: Int, kind: MediaKind) {
    val c = when {
        kind == MediaKind.AUDIO || label == "Audio" -> Color(0xFFDB2777)
        height >= 1440 -> Color(0xFF7C3AED)
        height >= 1080 -> Color(0xFF2563EB)
        height >= 720 -> Color(0xFF16A34A)
        height >= 480 -> Color(0xFFEA580C)
        height > 0 -> Color(0xFF6B7280)
        else -> Color(0xFF0D9488)
    }
    Text(
        label.ifBlank { "Video" }, color = Color.White, fontSize = 11.sp, fontWeight = FontWeight.Bold,
        modifier = Modifier.clip(RoundedCornerShape(8.dp)).background(c).padding(horizontal = 7.dp, vertical = 2.dp),
    )
}
