package com.shekh.bddm.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.shekh.bddm.BDDMApp
import com.shekh.bddm.core.mobile.Mobile
import com.shekh.bddm.data.EngineHolder
import com.shekh.bddm.data.OpenPolicy
import org.json.JSONObject

@Composable
fun SettingsScreen() {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val prefs = BDDMApp.instance.prefs
    var theme by remember { mutableStateOf(prefs.theme) }
    var wifiOnly by remember { mutableStateOf(prefs.wifiOnly) }
    var ad by remember { mutableStateOf(prefs.adBlocker) }
    var popups by remember { mutableStateOf(prefs.blockPopups) }
    var trackers by remember { mutableStateOf(prefs.blockTrackers) }
    var autoLists by remember { mutableStateOf(prefs.filterListAutoUpdate) }
    var closeDays by remember { mutableStateOf(prefs.autoCloseTabsDays.toString()) }
    var notify by remember { mutableStateOf(prefs.notifications) }
    var autoUpd by remember { mutableStateOf(prefs.checkUpdates) }
    var folder by remember { mutableStateOf(prefs.downloadDirFile().absolutePath) }
    var parts by remember { mutableStateOf("8") }
    var limitLabel by remember { mutableStateOf("Unlimited") }
    var updateMsg by remember { mutableStateOf<String?>(null) }
    var updateUrl by remember { mutableStateOf<String?>(null) }
    var checking by remember { mutableStateOf(false) }
    var policies by remember { mutableStateOf(prefs.openPolicyEntries()) }

    LaunchedEffect(Unit) {
        try {
            val o = JSONObject(EngineHolder.engine?.settingsJSON() ?: "{}")
            parts = o.optInt("parts", 8).toString()
            val lim = o.optLong("httpDownloadLimit", 0)
            limitLabel = if (lim <= 0) "Unlimited" else "${lim / 1024} KB/s"
            theme = o.optString("theme", theme)
        } catch (_: Exception) {}
    }

    fun pushEngine(vararg pairs: Pair<String, Any?>) {
        EngineHolder.applySettings(pairs.toMap())
    }

    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp)) {
        Text("Settings", fontSize = 22.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(12.dp))

        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Theme", fontWeight = FontWeight.SemiBold)
                Spacer(Modifier.height(6.dp))
                val options = listOf("dark" to "Dark", "light" to "Light", "system" to "System (follow Android)")
                options.forEach { (id, label) ->
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        RadioButton(selected = theme == id, onClick = {
                            theme = id; prefs.theme = id; pushEngine("theme" to id)
                        })
                        Text(label)
                    }
                }
            }
        }
        Spacer(Modifier.height(10.dp))
        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Folders", fontWeight = FontWeight.SemiBold)
                Spacer(Modifier.height(6.dp))
                Text(folder, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Text("New downloads are saved here, and videos go to the same folder unless you pick another one.",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        Spacer(Modifier.height(10.dp))
        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Speed limits", fontWeight = FontWeight.SemiBold)
                Text("A limit of 0 is unlimited. Applies to regular HTTP downloads.",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                val choices = listOf(0L to "Unlimited", 256L * 1024 to "256 KB/s", 512L * 1024 to "512 KB/s", 1024L * 1024 to "1 MB/s", 4L * 1024 * 1024 to "4 MB/s")
                choices.forEach { (bps, label) ->
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        RadioButton(selected = limitLabel == label, onClick = {
                            limitLabel = label; pushEngine("httpDownloadLimit" to bps)
                        })
                        Text(label)
                    }
                }
                OutlinedTextField(value = parts, onValueChange = {
                    parts = it.filter { ch -> ch.isDigit() }.take(2)
                    parts.toIntOrNull()?.let { n -> if (n in 1..32) pushEngine("parts" to n) }
                }, label = { Text("Parts per download") }, modifier = Modifier.fillMaxWidth())
            }
        }
        Spacer(Modifier.height(10.dp))
        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Ad blocker", fontWeight = FontWeight.SemiBold)
                Toggle("Ad blocker", "Blocks ads and scripts in the BDDM browser.", ad) {
                    ad = it; prefs.adBlocker = it; pushEngine("adBlocker" to it)
                }
                Toggle("Block popups", "Blocks windows that open without a tap. A tap that opens another site still asks first.", popups) { popups = it; prefs.blockPopups = it; pushEngine("blockPopups" to it) }
                Toggle("Block trackers", null, trackers) { trackers = it; prefs.blockTrackers = it; pushEngine("blockTrackers" to it) }
                Toggle("Update filter lists automatically", "Daily updates (EasyList, EasyPrivacy).", autoLists) {
                    autoLists = it; prefs.filterListAutoUpdate = it; pushEngine("filterListAutoUpdate" to it)
                    if (it) BDDMApp.instance.adBlocker.forceUpdate()
                }
                TextButton(onClick = { BDDMApp.instance.adBlocker.forceUpdate(); updateMsg = "Updating filter lists…" }) {
                    Text("Update lists now")
                }
            }
        }
        Spacer(Modifier.height(10.dp))
        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Browser", fontWeight = FontWeight.SemiBold)
                Text("Close unused tabs after this many days (0 = never). Vivaldi-style tab strip keeps recent tabs.",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                OutlinedTextField(value = closeDays, onValueChange = {
                    closeDays = it.filter { ch -> ch.isDigit() }.take(3)
                    closeDays.toIntOrNull()?.let { n -> prefs.autoCloseTabsDays = n; pushEngine("autoCloseTabsDays" to n) }
                }, label = { Text("Auto-close tabs after N days") }, modifier = Modifier.fillMaxWidth())
                Toggle("Wi-Fi only", "Pause new downloads on mobile data.", wifiOnly) {
                    wifiOnly = it; prefs.wifiOnly = it; pushEngine("wifiOnly" to it)
                }
                Toggle("Show notifications", null, notify) { notify = it; prefs.notifications = it; pushEngine("notifications" to it) }
                if (policies.isNotEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text("Always Open or Block from these sites", fontWeight = FontWeight.Medium)
                    Text("These choices come from the “Always for this site” box when a page tries to open another site.",
                        fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    policies.forEach { (host, policy) ->
                        Row(Modifier.fillMaxWidth().padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(host)
                                Text(if (policy == OpenPolicy.ALLOW) "Always open in a new tab" else "Always block",
                                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                            TextButton(onClick = {
                                prefs.setOpenPolicy(host, OpenPolicy.ASK)
                                policies = prefs.openPolicyEntries()
                            }) { Text("Ask again") }
                        }
                    }
                }
            }
        }
        Spacer(Modifier.height(10.dp))
        Card(shape = RoundedCornerShape(18.dp), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Updates", fontWeight = FontWeight.SemiBold)
                Text("This app is BDDM ${Mobile.AppVersion}. Android updates use the android-v tags, not the PC Latest release.",
                    fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Toggle("Check for updates automatically", null, autoUpd) {
                    autoUpd = it; prefs.checkUpdates = it; pushEngine("checkUpdates" to it)
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Button(onClick = {
                        scope.launch {
                            checking = true
                            val raw = withContext(Dispatchers.IO) {
                                EngineHolder.engine?.checkUpdate(Mobile.AppVersion) ?: "{}"
                            }
                            val o = try { JSONObject(raw) } catch (_: Exception) { JSONObject() }
                            val newer = o.optBoolean("newer")
                            val ver = o.optString("version")
                            val err = o.optString("error")
                            val apk = OpenPolicy.pickApkUrl(o, android.os.Build.SUPPORTED_ABIS.toList())
                            updateMsg = when {
                                err.isNotBlank() -> err
                                newer -> "BDDM $ver is available"
                                else -> "You're up to date"
                            }
                            updateUrl = apk
                            checking = false
                        }
                    }, enabled = !checking) { Text(if (checking) "Checking…" else "Check now") }
                    Spacer(Modifier.width(8.dp))
                    if (updateUrl != null) {
                        Button(onClick = {
                            ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(updateUrl)))
                        }) { Text("Install") }
                    }
                }
                updateMsg?.let { Text(it, modifier = Modifier.padding(top = 8.dp), fontSize = 13.sp) }
            }
        }
        Spacer(Modifier.height(24.dp))
        Text("BDDM ${Mobile.AppVersion}  ·  shekh-shehab/BDDM", fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.height(48.dp))
    }
}

@Composable
private fun Toggle(title: String, subtitle: String?, on: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(title, fontWeight = FontWeight.Medium)
            if (subtitle != null) Text(subtitle, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Switch(checked = on, onCheckedChange = onChange)
    }
}
