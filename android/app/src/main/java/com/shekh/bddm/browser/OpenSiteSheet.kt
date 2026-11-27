package com.shekh.bddm.browser

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.shekh.bddm.data.OpenRequest

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OpenSiteSheet(
    req: OpenRequest,
    onOpen: (always: Boolean) -> Unit,
    onBlock: (always: Boolean) -> Unit,
    onDismiss: () -> Unit,
) {
    var always by remember { mutableStateOf(false) }
    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(Modifier.padding(horizontal = 20.dp, vertical = 8.dp).navigationBarsPadding()) {
            Text("Open this site?", fontWeight = FontWeight.SemiBold, fontSize = 20.sp)
            Spacer(Modifier.height(8.dp))
            Text(
                req.toHost,
                fontSize = 22.sp,
                fontWeight = FontWeight.Bold,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
            Text(
                req.url,
                fontSize = 12.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(top = 4.dp),
            )
            Text(
                "From ${req.fromHost}",
                fontSize = 12.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 4.dp),
            )
            Spacer(Modifier.height(12.dp))
            Row(
                Modifier.fillMaxWidth().clickable { always = !always }.padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Checkbox(checked = always, onCheckedChange = { always = it })
                Text("Always for this site (${req.fromHost})", modifier = Modifier.padding(start = 4.dp))
            }
            Spacer(Modifier.height(8.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                OutlinedButton(onClick = { onBlock(always) }, modifier = Modifier.weight(1f)) { Text("Block") }
                Button(onClick = { onOpen(always) }, modifier = Modifier.weight(1f)) { Text("Open") }
            }
            Spacer(Modifier.height(16.dp))
        }
    }
}
