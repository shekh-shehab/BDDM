package com.shekh.bddm.browser

import android.graphics.Bitmap
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.outlined.VisibilityOff
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.shekh.bddm.data.TabState
import com.shekh.bddm.ui.PageDark
import com.shekh.bddm.ui.SurfaceDark
import com.shekh.bddm.ui.TextAccentDark
import com.shekh.bddm.ui.TextDark

private val SwitcherBg = Color(0xFF0E1013)
private val CardBg = Color(0xFF1B1E24)
private val PillBg = Color(0xFF1A1D22)
private val ActiveBorder = Color(0xFFB7C9F0)
private val FabBlue = Color(0xFF3D7EFF)

@Composable
fun TabSwitcher(
    tabs: List<TabState>,
    currentId: String,
    incognitoSpace: Boolean,
    thumbs: Map<String, Bitmap?>,
    favicons: Map<String, Bitmap?>,
    onSelect: (TabState) -> Unit,
    onClose: (TabState) -> Unit,
    onNewTab: (incognito: Boolean) -> Unit,
    onCloseAll: () -> Unit,
    onCloseOthers: () -> Unit,
    onDismiss: () -> Unit,
    onToggleIncognitoSpace: () -> Unit,
    onMessage: (String) -> Unit,
) {
    var searchOpen by remember { mutableStateOf(false) }
    var query by remember { mutableStateOf("") }
    var menu by remember { mutableStateOf(false) }
    var confirmCloseAll by remember { mutableStateOf(false) }

    val visible = tabs.filter { it.incognito == incognitoSpace }
        .filter {
            query.isBlank() || it.title.contains(query, true) || it.url.contains(query, true)
        }
    val count = tabs.count { it.incognito == incognitoSpace }

    Box(Modifier.fillMaxSize().background(SwitcherBg).statusBarsPadding().navigationBarsPadding()) {
        Column(Modifier.fillMaxSize()) {
            Row(
                Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    if (incognitoSpace) "Private ($count)" else "Tabs ($count)",
                    fontSize = 28.sp,
                    fontWeight = FontWeight.SemiBold,
                    color = TextDark,
                    modifier = Modifier.weight(1f),
                )
                IconButton(onClick = { searchOpen = !searchOpen }) { Icon(Icons.Default.Search, "Search tabs", tint = TextDark) }
                Box {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, "Menu", tint = TextDark) }
                    DropdownMenu(menu, { menu = false }) {
                        DropdownMenuItem(text = { Text("Close all") }, onClick = { menu = false; confirmCloseAll = true })
                        DropdownMenuItem(text = { Text("Close other tabs") }, onClick = { menu = false; onCloseOthers() })
                    }
                }
                IconButton(onClick = onDismiss) { Icon(Icons.Default.Close, "Close", tint = TextDark) }
            }
            if (searchOpen) {
                OutlinedTextField(
                    value = query,
                    onValueChange = { query = it },
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp),
                    singleLine = true,
                    placeholder = { Text("Search tabs") },
                    shape = RoundedCornerShape(24.dp),
                )
            }
            if (visible.isEmpty()) {
                Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    Text(
                        if (incognitoSpace) "No private tabs" else "No tabs",
                        color = Color(0xFF8B93A0),
                    )
                }
            } else {
                LazyVerticalGrid(
                    columns = GridCells.Fixed(2),
                    modifier = Modifier.weight(1f),
                    contentPadding = PaddingValues(12.dp, 4.dp, 12.dp, 140.dp),
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    items(visible, key = { it.id }) { t ->
                        TabCard(
                            tab = t,
                            active = t.id == currentId,
                            thumb = thumbs[t.id],
                            favicon = favicons[t.id],
                            onSelect = { onSelect(t) },
                            onClose = { onClose(t) },
                        )
                    }
                }
            }
        }

        FloatingActionButton(
            onClick = { onNewTab(incognitoSpace) },
            modifier = Modifier.align(Alignment.BottomEnd).padding(end = 20.dp, bottom = 96.dp),
            containerColor = FabBlue,
            contentColor = Color.White,
            shape = CircleShape,
        ) { Icon(Icons.Default.Add, "New tab") }

        Row(
            Modifier
                .align(Alignment.BottomCenter)
                .padding(bottom = 20.dp)
                .clip(RoundedCornerShape(28.dp))
                .background(PillBg)
                .padding(horizontal = 10.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            PillIcon(selected = !incognitoSpace, onClick = { if (incognitoSpace) onToggleIncognitoSpace() }) {
                Box(
                    Modifier.size(28.dp).clip(RoundedCornerShape(8.dp))
                        .then(if (!incognitoSpace) Modifier.background(Color(0xFF2A2E36)) else Modifier)
                        .border(1.dp, Color(0xFF8B93A0), RoundedCornerShape(8.dp)),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        "${tabs.count { !it.incognito }}",
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold,
                        color = TextDark,
                    )
                }
            }
            PillIcon(selected = incognitoSpace, onClick = { if (!incognitoSpace) onToggleIncognitoSpace() }) {
                Icon(Icons.Outlined.VisibilityOff, "Private tabs", tint = if (incognitoSpace) TextAccentDark else Color(0xFFB0B6C0), modifier = Modifier.size(26.dp))
            }
            PillIcon(selected = false, onClick = { onMessage("Tab groups are not in this version yet.") }) {
                Icon(Icons.Default.GridView, "Tab groups", tint = Color(0xFFB0B6C0), modifier = Modifier.size(26.dp))
            }
            PillIcon(selected = false, onClick = { onMessage("Tab sync is not available yet.") }) {
                Icon(Icons.Default.Cloud, "Synced tabs", tint = Color(0xFFB0B6C0), modifier = Modifier.size(26.dp))
            }
            PillIcon(selected = false, onClick = { confirmCloseAll = true }) {
                Icon(Icons.Default.Delete, "Close all", tint = Color(0xFFB0B6C0), modifier = Modifier.size(26.dp))
            }
        }
    }

    if (confirmCloseAll) {
        AlertDialog(
            onDismissRequest = { confirmCloseAll = false },
            title = { Text(if (incognitoSpace) "Close all private tabs?" else "Close all tabs?") },
            confirmButton = {
                TextButton(onClick = { confirmCloseAll = false; onCloseAll() }) { Text("Close all") }
            },
            dismissButton = { TextButton(onClick = { confirmCloseAll = false }) { Text("Cancel") } },
        )
    }
}

@Composable
private fun PillIcon(selected: Boolean, onClick: () -> Unit, content: @Composable () -> Unit) {
    Box(
        Modifier.size(48.dp).clip(CircleShape).clickable(onClick = onClick),
        contentAlignment = Alignment.Center,
    ) { content() }
}

@Composable
private fun TabCard(
    tab: TabState,
    active: Boolean,
    thumb: Bitmap?,
    favicon: Bitmap?,
    onSelect: () -> Unit,
    onClose: () -> Unit,
) {
    val shape = RoundedCornerShape(18.dp)
    Column(
        Modifier
            .fillMaxWidth()
            .aspectRatio(0.78f)
            .clip(shape)
            .background(CardBg)
            .then(if (active) Modifier.border(3.dp, ActiveBorder, shape) else Modifier)
            .clickable(onClick = onSelect)
    ) {
        Row(
            Modifier.fillMaxWidth().padding(start = 10.dp, end = 2.dp, top = 8.dp, bottom = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (favicon != null) {
                Image(favicon.asImageBitmap(), null, modifier = Modifier.size(16.dp).clip(RoundedCornerShape(3.dp)))
            } else {
                Box(Modifier.size(16.dp).clip(RoundedCornerShape(3.dp)).background(Color(0xFF3A414C)))
            }
            Spacer(Modifier.width(6.dp))
            Text(
                tab.title.ifBlank { if (tab.incognito) "Private tab" else "New tab" },
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium,
                color = TextDark,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            Icon(
                Icons.Default.Close,
                "Close tab",
                tint = Color(0xFFD0D4DC),
                modifier = Modifier.size(28.dp).clickable(onClick = onClose).padding(4.dp),
            )
        }
        Box(
            Modifier.fillMaxSize().padding(start = 8.dp, end = 8.dp, bottom = 8.dp)
                .clip(RoundedCornerShape(12.dp))
                .background(SurfaceDark)
        ) {
            if (thumb != null) {
                Image(
                    thumb.asImageBitmap(),
                    contentDescription = tab.title,
                    modifier = Modifier.fillMaxSize(),
                    contentScale = ContentScale.Crop,
                )
            } else {
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Text(
                        tab.url.ifBlank { "Blank tab" }.removePrefix("https://").removePrefix("http://"),
                        color = Color(0xFF8B93A0),
                        fontSize = 11.sp,
                        maxLines = 3,
                    )
                }
            }
        }
    }
}
