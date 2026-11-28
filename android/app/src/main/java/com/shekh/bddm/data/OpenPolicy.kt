package com.shekh.bddm.data

import org.json.JSONObject

/** Remembered "always Open / always Block" choices for item 103. */
object OpenPolicy {
    const val ASK = "ask"
    const val ALLOW = "allow"
    const val BLOCK = "block"

    fun sameSite(a: String?, b: String?): Boolean {
        val x = normalize(a) ?: return false
        val y = normalize(b) ?: return false
        return x == y || x.endsWith(".$y") || y.endsWith(".$x")
    }

    fun normalize(host: String?): String? {
        val h = host?.lowercase()?.trim()?.trim('.') ?: return null
        if (h.isBlank()) return null
        return if (h.startsWith("www.")) h.removePrefix("www.") else h
    }

    fun parse(json: String): Map<String, String> {
        return try {
            val o = JSONObject(json)
            val out = LinkedHashMap<String, String>()
            o.keys().forEach { k ->
                val v = o.optString(k)
                if (v == ALLOW || v == BLOCK) out[k] = v
            }
            out
        } catch (_: Exception) { emptyMap() }
    }

    fun set(json: String, host: String, policy: String): String {
        val key = normalize(host) ?: return json
        val o = try { JSONObject(json) } catch (_: Exception) { JSONObject() }
        if (policy == ASK) o.remove(key) else o.put(key, policy)
        return o.toString()
    }

    fun get(json: String, host: String): String {
        val key = normalize(host) ?: return ASK
        return parse(json)[key] ?: ASK
    }

    /** Pick the APK URL that matches the device ABI from a CheckUpdate JSON object. */
    fun pickApkUrl(o: JSONObject, abis: List<String>): String? {
        val arm64 = o.optString("apkArm64")
        val armv7 = o.optString("apkArmv7")
        val any = o.optString("apk")
        val has64 = abis.any { it.contains("arm64", ignoreCase = true) }
        val has32 = abis.any { it == "armeabi-v7a" || it == "armeabi" }
        return when {
            has64 && arm64.isNotBlank() -> arm64
            has32 && armv7.isNotBlank() -> armv7
            // Releases are arm64-only now: a 32-bit-only phone must not be offered an APK it cannot install.
            !has64 && arm64.isNotBlank() && armv7.isBlank() && (any.isBlank() || any == arm64) -> null
            any.isNotBlank() -> any
            arm64.isNotBlank() -> arm64
            armv7.isNotBlank() -> armv7
            else -> o.optString("url").ifBlank { null }
        }
    }
}

data class OpenRequest(val fromHost: String, val url: String, val toHost: String)
