package com.shekh.bddm.data

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class OpenPolicyTest {
    @Test
    fun sameSiteTreatsSubdomainsAsOneSite() {
        assertTrue(OpenPolicy.sameSite("www.youtube.com", "youtube.com"))
        assertTrue(OpenPolicy.sameSite("m.youtube.com", "www.youtube.com"))
        assertFalse(OpenPolicy.sameSite("youtube.com", "accounts.google.com"))
        assertFalse(OpenPolicy.sameSite("news.example.com", "ads.other.com"))
    }

    @Test
    fun policyRoundTrip() {
        var json = "{}"
        json = OpenPolicy.set(json, "www.YouTube.com", OpenPolicy.BLOCK)
        assertEquals(OpenPolicy.BLOCK, OpenPolicy.get(json, "youtube.com"))
        json = OpenPolicy.set(json, "youtube.com", OpenPolicy.ASK)
        assertEquals(OpenPolicy.ASK, OpenPolicy.get(json, "youtube.com"))
        assertTrue(OpenPolicy.parse(json).isEmpty())
    }

    @Test
    fun pickApkPrefersArm64WhenDeviceIs64Bit() {
        val o = JSONObject()
            .put("apkArm64", "https://x/arm64.apk")
            .put("apkArmv7", "https://x/v7.apk")
            .put("apk", "https://x/v7.apk")
        assertEquals("https://x/arm64.apk", OpenPolicy.pickApkUrl(o, listOf("arm64-v8a", "armeabi-v7a")))
        assertEquals("https://x/v7.apk", OpenPolicy.pickApkUrl(o, listOf("armeabi-v7a")))
    }

    @Test
    fun pickApkFallsBackToLegacySingleAsset() {
        val o = JSONObject().put("apk", "https://x/BDDM-Android-1.6.0.apk")
        assertEquals("https://x/BDDM-Android-1.6.0.apk", OpenPolicy.pickApkUrl(o, listOf("arm64-v8a")))
    }
}
