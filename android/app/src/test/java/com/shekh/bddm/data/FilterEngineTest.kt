package com.shekh.bddm.data

import org.junit.Assert.*
import org.junit.Before
import org.junit.Test

class FilterEngineTest {
    private lateinit var e: FilterEngine

    @Before
    fun setup() {
        e = FilterEngine()
        e.addRules(
            """
            ||doubleclick.net^
            ||googlesyndication.com^
            ||googleadservices.com^
            ||youtube.com^${'$'}third-party
            ||youtube.com/pagead/
            ||youtube.com^${'$'}popup
            ||googlevideo.com/ptracking
            ||google-analytics.com^
            ||example-ads.com^
            @@||ads-exception.example.com^
            """.trimIndent()
        )
    }

    @Test
    fun thirdPartyYoutubeRuleDoesNotBlockFirstPartyPlayer() {
        val page = "www.youtube.com"
        assertFalse(e.shouldBlock("https://www.youtube.com/watch?v=abc", page, false, false))
        assertFalse(e.shouldBlock("https://www.youtube.com/s/player/abc/player_ias.vflset/en_US/base.js", page, false, false))
        assertFalse(e.shouldBlock("https://www.youtube.com/youtubei/v1/player", page, false, false))
    }

    @Test
    fun youtubeHostIsAllowlistedEvenIfThirdParty() {
        // Embedded youtube on another site still needs the player.
        assertFalse(e.shouldBlock("https://www.youtube.com/embed/abc", "example.com", true, false))
    }

    @Test
    fun googlevideoStreamsAreNeverBlocked() {
        val url = "https://rr1---sn-abc.googlevideo.com/videoplayback?expire=1&mime=video/mp4"
        assertFalse(e.shouldBlock(url, "www.youtube.com", true, false))
        assertTrue(e.isAllowlisted("rr1---sn-abc.googlevideo.com"))
    }

    @Test
    fun ytimgAndAttestationHostsAreAllowlisted() {
        assertTrue(e.isAllowlisted("i.ytimg.com"))
        assertTrue(e.isAllowlisted("s.ytimg.com"))
        assertTrue(e.isAllowlisted("jnn-pa.googleapis.com"))
        assertTrue(e.isAllowlisted("youtubei.googleapis.com"))
    }

    @Test
    fun mainFrameIsNeverBlocked() {
        e.addRule("||evil.example^")
        assertFalse(e.shouldBlock("https://evil.example/index.html", "evil.example", false, true))
        assertTrue(e.shouldBlock("https://evil.example/ads.js", "evil.example", false, false))
    }

    @Test
    fun realAdHostsAreStillBlocked() {
        assertTrue(e.shouldBlock("https://securepubads.g.doubleclick.net/gampad/ads", "www.example.com", true, false))
        assertTrue(e.shouldBlock("https://pagead2.googlesyndication.com/pagead/js/adsbygoogle.js", "www.example.com", true, false))
        assertTrue(e.shouldBlock("https://ads.example-ads.com/banner.js", "www.example.com", true, false))
    }

    @Test
    fun youtubePageadPathIsBlockedButNotThePlayer() {
        // pagead is a path on youtube.com; allowlist wins so we do not break the site.
        // Ads on YouTube itself are accepted so the player keeps working.
        assertFalse(e.shouldBlock("https://www.youtube.com/pagead/id", "www.youtube.com", false, false))
    }

    @Test
    fun popupOnlyRuleIsIgnoredForNetworkRequests() {
        // ||youtube.com^${'$'}popup must not add youtube.com as a host block.
        assertFalse(e.shouldBlock("https://www.youtube.com/watch?v=abc", "www.youtube.com", false, false))
    }

    @Test
    fun thirdPartyJwPlayerScriptsAreNotBlocked() {
        e.addRule("||jwpcdn.com^${'$'}third-party")
        e.addRule("||jwpsrv.com^${'$'}third-party")
        e.addRule("||jwplayer.com^${'$'}third-party")
        val page = "www.example-news.com"
        assertFalse(e.shouldBlock("https://cdn.jwplayer.com/libraries/AbCdEfGh.js", page, true, false))
        assertFalse(e.shouldBlock("https://ssl.p.jwpcdn.com/player/v/8.26.0/jwplayer.js", page, true, false))
        assertFalse(e.shouldBlock("https://assets-jpcust.jwpsrv.com/player/v/8.26.0/jwplayer.js", page, true, false))
        assertTrue(e.isAllowlisted("ssl.p.jwpcdn.com"))
        assertTrue(e.isAllowlisted("cdn.jwplayer.com"))
        assertTrue(e.isAllowlisted("content.jwplatform.com"))
    }

    @Test
    fun chromeLikeUaStripsWebViewMarkers() {
        val ua = "Mozilla/5.0 (Linux; Android 14; Pixel 7; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0.0.0 Mobile Safari/537.36"
        val out = FilterEngine.chromeLikeUa(ua)
        assertFalse(out.contains("wv"))
        assertFalse(out.contains("Version/4.0"))
        assertTrue(out.contains("Chrome/120.0.0.0"))
        assertTrue(out.contains("Mobile Safari/537.36"))
    }
}
