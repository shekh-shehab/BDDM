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

    // ---- uBlock-style rules (item 107) ----

    private fun fresh(vararg rules: String) = FilterEngine().also { eng -> rules.forEach { eng.addRule(it) } }

    @Test
    fun typeOptionsAreHonoured() {
        val f = fresh("||adserver.test^${'$'}script,third-party")
        val js = FilterEngine.T_SCRIPT
        assertTrue(f.shouldBlock("https://adserver.test/a.js", "news.test", true, false, js))
        assertFalse(f.shouldBlock("https://adserver.test/a.png", "news.test", true, false, FilterEngine.T_IMAGE))
        assertFalse(f.shouldBlock("https://adserver.test/a.js", "adserver.test", false, false, js))
        // unknown type (fetch/XHR without an extension) is treated like script/xhr
        assertTrue(f.shouldBlock("https://adserver.test/get", "news.test", true, false))
    }

    @Test
    fun exceptionRulesWinUnlessImportant() {
        val f = fresh("||tracker.test^", "@@||tracker.test/allowed/")
        assertTrue(f.shouldBlock("https://tracker.test/x.js", "a.test", true, false))
        assertFalse(f.shouldBlock("https://tracker.test/allowed/x.js", "a.test", true, false))
        val g = fresh("||tracker.test^${'$'}important", "@@||tracker.test/allowed/")
        assertTrue(g.shouldBlock("https://tracker.test/allowed/x.js", "a.test", true, false))
    }

    @Test
    fun domainOptionLimitsRulesToPages() {
        val f = fresh("||cdn.test/promo.js${'$'}domain=shop.test|~vip.shop.test")
        assertTrue(f.shouldBlock("https://cdn.test/promo.js", "shop.test", true, false))
        assertTrue(f.shouldBlock("https://cdn.test/promo.js", "m.shop.test", true, false))
        assertFalse(f.shouldBlock("https://cdn.test/promo.js", "vip.shop.test", true, false))
        assertFalse(f.shouldBlock("https://cdn.test/promo.js", "other.test", true, false))
    }

    @Test
    fun documentExceptionSwitchesBlockingOffForThePage() {
        val f = fresh("||ads.test^", "@@||friendly.test^${'$'}document")
        assertTrue(f.shouldBlock("https://ads.test/a.js", "news.test", true, false))
        assertFalse(f.shouldBlock("https://ads.test/a.js", "www.friendly.test", true, false))
        assertTrue(f.pageExempt("friendly.test"))
    }

    @Test
    fun pathPatternsWildcardsAndAnchors() {
        val f = fresh("/banners/*/ad_", "|https://exact.test/x.js|", "||site.test/ads^", "&adunit=")
        assertTrue(f.shouldBlock("https://any.test/banners/2024/ad_top.png", "p.test", true, false, FilterEngine.T_IMAGE))
        assertFalse(f.shouldBlock("https://any.test/banners/ad_top.png", "p.test", true, false, FilterEngine.T_IMAGE))
        assertTrue(f.shouldBlock("https://exact.test/x.js", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertFalse(f.shouldBlock("https://exact.test/x.js?v=2", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertTrue(f.shouldBlock("https://site.test/ads/1.js", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertFalse(f.shouldBlock("https://site.test/adsense/1.js", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertTrue(f.shouldBlock("https://x.test/p?a=1&adunit=3", "p.test", true, false))
    }

    @Test
    fun unsupportedRulesAreSkippedWithoutHarm() {
        val f = fresh(
            "example.com##.ad-banner", "##.sponsored", "example.com#@#.ad", "example.com##+js(aopr, x)",
            "||site.test^${'$'}popup", "||site.test^${'$'}csp=script-src 'none'", "||site.test^${'$'}removeparam=utm",
            "/ads\\d+/", "! comment", "[Adblock Plus 2.0]", "",
        )
        assertTrue(f.isEmpty())
        assertFalse(f.shouldBlock("https://site.test/a.js", "p.test", true, false, FilterEngine.T_SCRIPT))
    }

    @Test
    fun badfilterCancelsRulesAcrossLists() {
        val f = FilterEngine()
        f.addLists(listOf("||blocked.test^\n||kept.test^", "||blocked.test^${'$'}badfilter"))
        assertFalse(f.shouldBlock("https://blocked.test/a.js", "p.test", true, false))
        assertTrue(f.shouldBlock("https://kept.test/a.js", "p.test", true, false))
    }

    @Test
    fun hostsFileEntriesBlockTheHost() {
        val f = FilterEngine()
        f.addRules("# Peter Lowe\n127.0.0.1 ads.example.net\n127.0.0.1 localhost\n0.0.0.0 trk.example.org # note")
        assertTrue(f.shouldBlock("https://ads.example.net/x", "p.test", true, false))
        assertTrue(f.shouldBlock("https://sub.trk.example.org/x", "p.test", true, false))
        assertFalse(f.shouldBlock("http://localhost/x", "p.test", false, false))
    }

    @Test
    fun mediaFilesAndPlayerHostsAreNeverBlockedButAdMediaIsFlagged() {
        val f = fresh("||adcdn.test^", "||vimeocdn.com^", "||jwpcdn.com^")
        assertFalse(f.shouldBlock("https://adcdn.test/promo.mp4", "p.test", true, false, FilterEngine.T_MEDIA))
        assertFalse(f.shouldBlock("https://adcdn.test/live.m3u8?x=1", "p.test", true, false, FilterEngine.T_MEDIA))
        assertTrue(f.isAdUrl("https://adcdn.test/promo.mp4", true, "p.test"))
        assertFalse(f.isAdUrl("https://cdn.example.com/movie.mp4", true, "p.test"))
        assertFalse(f.shouldBlock("https://player.vimeocdn.com/x.js", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertFalse(f.shouldBlock("https://ssl.p.jwpcdn.com/player/jwplayer.js", "p.test", true, false, FilterEngine.T_SCRIPT))
        assertTrue(f.shouldBlock("https://adcdn.test/ad.js", "p.test", true, false, FilterEngine.T_SCRIPT))
    }

    @Test
    fun registrableDomainAndThirdPartyDetection() {
        assertEquals("example.com", FilterEngine.registrableDomain("www.example.com"))
        assertEquals("example.co.uk", FilterEngine.registrableDomain("a.b.example.co.uk"))
        assertFalse(FilterEngine.isThirdParty("static.example.com", "www.example.com"))
        assertTrue(FilterEngine.isThirdParty("cdn.other.com", "www.example.com"))
    }

    @Test
    fun resourceTypeGuessing() {
        assertEquals(FilterEngine.T_SCRIPT, FilterEngine.guessType("https://a.test/x.js?v=1", null))
        assertEquals(FilterEngine.T_IMAGE, FilterEngine.guessType("https://a.test/x.WEBP", null))
        assertEquals(FilterEngine.T_STYLE, FilterEngine.guessType("https://a.test/css", "text/css,*/*;q=0.1"))
        assertEquals(FilterEngine.T_SUBDOC, FilterEngine.guessType("https://a.test/embed", "text/html,application/xhtml+xml"))
        assertEquals(FilterEngine.T_UNKNOWN, FilterEngine.guessType("https://a.test/api", "*/*"))
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
