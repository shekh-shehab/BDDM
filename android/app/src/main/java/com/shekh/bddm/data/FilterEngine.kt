package com.shekh.bddm.data

/**
 * EasyList / EasyPrivacy network-rule matcher.
 *
 * The first Android release treated `||youtube.com^$third-party` as a block of
 * the entire youtube.com host, which stopped the player, scripts, and often the
 * page itself from loading. Options after `$` are honoured, the main frame is
 * never intercepted, and media CDNs used by YouTube (and similar sites) are
 * allowlisted.
 */
class FilterEngine {
    private val hostsAll = HashSet<String>()
    private val hostsThird = HashSet<String>()
    private val exceptions = HashSet<String>()
    private val pathRules = ArrayList<PathRule>()

    data class PathRule(
        val host: String,
        val path: String,
        val thirdPartyOnly: Boolean,
        val firstPartyOnly: Boolean,
    )

    fun clear() {
        hostsAll.clear()
        hostsThird.clear()
        exceptions.clear()
        pathRules.clear()
    }

    fun isEmpty(): Boolean = hostsAll.isEmpty() && hostsThird.isEmpty() && pathRules.isEmpty()

    fun addRules(text: String) {
        text.lineSequence().forEach { addRule(it) }
    }

    fun addRule(raw: String) {
        var line = raw.trim()
        if (line.isEmpty() || line.startsWith("!") || line.startsWith("[")) return
        if (line.contains("##") || line.contains("#@#") || line.contains("#?#")) return

        val exception = line.startsWith("@@")
        if (exception) line = line.removePrefix("@@")

        val dollar = line.lastIndexOf('$')
        val pattern: String
        val opts: List<String>
        if (dollar >= 0 && dollar >= line.indexOf("||").coerceAtLeast(0)) {
            pattern = line.substring(0, dollar)
            opts = line.substring(dollar + 1).split(',').map { it.trim().lowercase() }.filter { it.isNotEmpty() }
        } else {
            pattern = line
            opts = emptyList()
        }

        if (shouldSkip(opts)) return

        val thirdPartyOnly = opts.contains("third-party")
        val firstPartyOnly = opts.contains("~third-party")

        if (!pattern.startsWith("||")) return

        val rest = pattern.removePrefix("||")
        val cut = rest.indexOfAny(charArrayOf('^', '/'))
        val host = (if (cut >= 0) rest.substring(0, cut) else rest).lowercase().trim('.')
        if (host.isEmpty() || host.contains("*") || host.contains("/")) return

        if (exception) {
            exceptions.add(host)
            return
        }

        val hasPath = cut >= 0 && rest[cut] == '/'
        if (hasPath) {
            val path = rest.substring(cut).trimEnd('^').substringBefore('$')
            if (path.isNotEmpty()) {
                pathRules.add(PathRule(host, path.lowercase(), thirdPartyOnly, firstPartyOnly))
            }
        } else if (thirdPartyOnly) {
            hostsThird.add(host)
        } else {
            hostsAll.add(host)
        }
    }

    fun shouldBlock(url: String, pageHost: String, isThirdParty: Boolean, isMainFrame: Boolean): Boolean {
        if (isMainFrame) return false
        val host = hostOf(url) ?: return false
        if (isAllowlisted(host)) return false
        if (looksLikeMedia(url)) return false
        if (inSet(host, exceptions)) return false

        if (inSet(host, hostsAll)) return true
        if (isThirdParty && inSet(host, hostsThird)) return true

        val u = url.lowercase()
        for (r in pathRules) {
            if (host == r.host || host.endsWith(".${r.host}")) {
                if (r.thirdPartyOnly && !isThirdParty) continue
                if (r.firstPartyOnly && isThirdParty) continue
                if (u.contains(r.path)) return true
            }
        }
        return false
    }

    fun isAllowlisted(host: String): Boolean {
        var h = host.lowercase()
        while (true) {
            if (h in MEDIA_ALLOW) return true
            val i = h.indexOf('.')
            if (i <= 0) return false
            h = h.substring(i + 1)
        }
    }

    companion object {
        /** Hosts the YouTube (and similar) players need. Never network-block these. */
        val MEDIA_ALLOW = setOf(
            // YouTube
            "googlevideo.com",
            "youtube.com",
            "youtu.be",
            "youtube-nocookie.com",
            "youtubekids.com",
            "ytimg.com",
            "ggpht.com",
            "jnn-pa.googleapis.com",
            "youtubei.googleapis.com",
            "wide-youtube.l.google.com",
            "yt3.ggpht.com",
            "gstatic.com",
            // JW Player and JW Platform (error 101102 if the player JS/CDN is blocked)
            "jwplayer.com",
            "jwpcdn.com",
            "jwpsrv.com",
            "jwplatform.com",
            "cdn-jwplayer.com",
            // Other common embedded players / CDNs
            "vimeo.com",
            "vimeocdn.com",
            "vimeo.tv",
            "zencdn.net",
            "videojs.com",
            "brightcove.com",
            "brightcove.net",
            "flowplayer.com",
            "flowplayer.org",
            "plyr.io",
            "bitmovin.com",
            "wistia.com",
            "wistia.net",
            "kaltura.com",
            "dailymotion.com",
            "dmcdn.net",
            "twimg.com",
            "fbcdn.net",
            "cdninstagram.com",
            "tiktokcdn.com",
            "tiktokcdn-us.com",
            "musical.ly",
            "twitch.tv",
            "twitchcdn.net",
            "jtvnw.net",
            "rumble.com",
            "streamable.com",
            "redditmedia.com",
            "redditstatic.com",
            "hlsjs.org",
        )

        private val SKIP_OPTS = setOf(
            "popup", "elemhide", "generichide", "genericblock", "csp",
            "rewrite", "header", "removeparam", "remove", "document",
        )

        private fun shouldSkip(opts: List<String>): Boolean {
            if (opts.isEmpty()) return false
            // A rule that is only a popup/cosmetic/CSP action is not a network block.
            val meaningful = opts.filter {
                it != "third-party" && it != "~third-party" &&
                    !it.startsWith("domain=") && !it.startsWith("~domain") &&
                    it != "important" && it != "all" &&
                    it != "script" && it != "image" && it != "stylesheet" &&
                    it != "xmlhttprequest" && it != "media" && it != "font" &&
                    it != "subdocument" && it != "websocket" && it != "ping" &&
                    it != "other" && it != "object" && it != "xhr"
            }
            return meaningful.any { opt ->
                opt in SKIP_OPTS || opt.startsWith("removeparam") || opt.startsWith("rewrite=")
            } && !opts.contains("script") && !opts.contains("xmlhttprequest") && !opts.contains("image")
        }

        fun inSet(host: String, set: Set<String>): Boolean {
            var h = host
            while (true) {
                if (set.contains(h)) return true
                val i = h.indexOf('.')
                if (i <= 0) return false
                h = h.substring(i + 1)
            }
        }

        fun hostOf(url: String): String? {
            return try {
                val s = if (url.contains("://")) url else "https://$url"
                java.net.URL(s).host?.lowercase()
            } catch (_: Exception) { null }
        }

        fun looksLikeMedia(url: String): Boolean {
            val u = url.lowercase()
            val path = u.substringBefore('?')
            if (listOf(".mp4", ".webm", ".mkv", ".m4v", ".mov", ".m3u8", ".mpd", ".m4a", ".mp3", ".aac").any { path.endsWith(it) }) return true
            if (u.contains("googlevideo.com/videoplayback") || u.contains("mime=video") || u.contains("mime=audio")) return true
            return false
        }

        /** Strip WebView markers so YouTube does not refuse HTML5 playback. */
        fun chromeLikeUa(ua: String): String {
            return ua
                .replace("; wv", "")
                .replace(Regex("(?i)Version/\\d[\\d.]*\\s*"), "")
                .replace(Regex("\\s+"), " ")
                .trim()
        }
    }
}
