package com.shekh.bddm.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import org.json.JSONObject

/**
 * Items 43 + 45: enforces "Wi-Fi only" and resumes downloads that failed because the
 * network dropped, once a usable connection is back.
 */
object NetworkWatcher {
    @Volatile var online = true
        private set
    @Volatile var unmetered = true
        private set
    private var prefs: Prefs? = null
    /** Engine jobs BDDM paused itself (Wi-Fi only / offline); only these are resumed automatically. */
    private val held = LinkedHashSet<String>()
    private val retries = HashMap<String, Int>()
    private val lastTry = HashMap<String, Long>()
    private var lastAllowed = true

    fun init(ctx: Context, p: Prefs) {
        prefs = p
        val cm = ctx.getSystemService(ConnectivityManager::class.java) ?: return
        try {
            cm.activeNetwork?.let { n -> cm.getNetworkCapabilities(n)?.let { update(it) } } ?: run { online = false }
            cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) { update(caps); apply() }
                override fun onLost(network: Network) { online = false; apply() }
            })
        } catch (_: Exception) { online = true; unmetered = true }
        lastAllowed = allowed()
    }

    private fun update(caps: NetworkCapabilities) {
        online = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
        unmetered = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) ||
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)
    }

    fun allowed(): Boolean = online && (prefs?.wifiOnly != true || unmetered)

    fun waitingReason(): String = when {
        !online -> "Waiting for network"
        prefs?.wifiOnly == true && !unmetered -> "Waiting for Wi-Fi"
        else -> ""
    }

    /** Pause a just-added job if downloads are not allowed right now. */
    @Synchronized
    fun holdIfNeeded(id: String) {
        if (allowed()) return
        EngineHolder.engine?.pause(id)
        held.add(id)
    }

    /** Called when the network or the Wi-Fi-only setting changes. */
    @Synchronized
    fun apply() {
        val e = EngineHolder.engine ?: return
        val ok = allowed()
        val snap = try { JSONObject(e.snapshotJSON()) } catch (_: Exception) { return }
        val jobs = snap.optJSONArray("jobs") ?: return
        if (!ok) {
            for (i in 0 until jobs.length()) {
                val j = jobs.optJSONObject(i) ?: continue
                val st = j.optString("status")
                if (st == "Downloading" || st == "Queued") {
                    try { e.pause(j.optString("id")); held.add(j.optString("id")) } catch (_: Exception) {}
                }
            }
        } else {
            held.toList().forEach { id -> try { e.resume(id) } catch (_: Exception) {} }
            held.clear()
            if (!lastAllowed) retryFailed(force = true)
        }
        lastAllowed = ok
        EngineHolder.refresh()
    }

    /** Called every second by the service: retries network failures with back-off (max 5 tries). */
    @Synchronized
    fun retryFailed(force: Boolean = false) {
        if (!allowed()) return
        val e = EngineHolder.engine ?: return
        val snap = try { JSONObject(e.snapshotJSON()) } catch (_: Exception) { return }
        val jobs = snap.optJSONArray("jobs") ?: return
        val now = System.currentTimeMillis()
        for (i in 0 until jobs.length()) {
            val j = jobs.optJSONObject(i) ?: continue
            if (j.optString("status") != "Error") continue
            val id = j.optString("id")
            if (!RetryPolicy.isNetworkError(j.optString("error"))) continue
            val n = retries[id] ?: 0
            if (n >= 5) continue
            val wait = RetryPolicy.backoffMs(n)
            if (!force && now - (lastTry[id] ?: 0L) < wait) continue
            retries[id] = n + 1
            lastTry[id] = now
            try { e.resume(id) } catch (_: Exception) {}
        }
    }
}

object RetryPolicy {
    private val NET = listOf(
        "could not connect", "timeout", "timed out", "connection reset", "connection refused", "no such host",
        "network is unreachable", "unexpected eof", "eof", "broken pipe", "connection abort", "i/o timeout",
        "tls handshake", "temporary failure", "no route to host", "unable to resolve", "failed to resolve",
        "software caused connection abort", "http 5", "returned http 50", "returned http 429", "network",
        "read error", "unable to download", "connection closed",
    )

    fun isNetworkError(msg: String): Boolean {
        val m = msg.lowercase()
        if (m.isBlank()) return false
        if (m.contains("http 403") || m.contains("http 404") || m.contains("http 410") || m.contains("http 401")) return false
        return NET.any { m.contains(it) }
    }

    /** 5 s, 15 s, 45 s, 2 min, 5 min */
    fun backoffMs(attempt: Int): Long = when (attempt) {
        0 -> 5_000L; 1 -> 15_000L; 2 -> 45_000L; 3 -> 120_000L; else -> 300_000L
    }
}
