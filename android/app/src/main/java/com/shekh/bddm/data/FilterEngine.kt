package com.shekh.bddm.data

/**
 * Network-rule matcher for Adblock-Plus / uBlock Origin filter lists (EasyList, EasyPrivacy,
 * uBlock filters, Peter Lowe's list, ...). Cosmetic and scriptlet rules are skipped.
 *
 * The first Android release treated `||youtube.com^$third-party` as a block of
 * the entire youtube.com host, which stopped the player, scripts, and often the
 * page itself from loading. Options after `$` are honoured, the main frame is
 * never intercepted, and media CDNs used by YouTube (and similar sites) are
 * allowlisted.
 */
class FilterEngine {
    /** One parsed network rule that needs more than a plain host match. */
    private class Rule(
        val pat: CharArray,            // lowercase pattern; '*' and '^' are wildcards. Empty = match anything for the host.
        val hostAnchored: Boolean,     // pattern starts right after the (suffix-matched) hostname
        val startAnchor: Boolean,
        val endAnchor: Boolean,
        val typeMask: Int,
        val party: Int,                // 0 any, 1 third-party only, 2 first-party only
        val important: Boolean,
        val domInc: Array<String>?,
        val domExc: Array<String>?,
    )

    private class Index {
        val hostPlain = HashSet<String>()
        val byHost = HashMap<String, ArrayList<Rule>>()
        val byToken = HashMap<String, ArrayList<Rule>>()
        val generic = ArrayList<Rule>()
        fun clear() { hostPlain.clear(); byHost.clear(); byToken.clear(); generic.clear() }
        fun isEmpty() = hostPlain.isEmpty() && byHost.isEmpty() && byToken.isEmpty() && generic.isEmpty()
    }

    private val block = Index()
    private val allow = Index()
    /** Hosts where a `@@||host^$document` (or $all) exception switches blocking off for the whole page. */
    private val pageAllow = HashSet<String>()
    var ruleCount = 0; private set
    var skippedCount = 0; private set

    fun clear() {
        block.clear(); allow.clear(); pageAllow.clear(); ruleCount = 0; skippedCount = 0
    }

    fun isEmpty(): Boolean = block.isEmpty()

    /** Is blocking switched off for this page by a document-level exception rule? */
    fun pageExempt(pageHost: String): Boolean = pageHost.isNotEmpty() && inSet(pageHost, pageAllow)

    fun addRules(text: String) { text.lineSequence().forEach { addRule(it) } }

    /**
     * Adds several lists at once so `$badfilter` rules in any list can cancel rules in another.
     */
    fun addLists(texts: List<String>) {
        val bad = HashSet<String>()
        for (tx in texts) tx.lineSequence().forEach { l ->
            if (l.contains("badfilter")) {
                val s = l.trim()
                bad.add(s.replace(",badfilter", "").replace("badfilter,", "").replace("\$badfilter", ""))
            }
        }
        for (tx in texts) tx.lineSequence().forEach { l -> if (bad.isEmpty() || l.trim() !in bad) addRule(l) }
    }

    fun addRule(raw: String) {
        var line = raw.trim()
        if (line.isEmpty()) return
        val c0 = line[0]
        if (c0 == '!' || c0 == '[' || c0 == '#') return
        if (isCosmetic(line)) { return }

        // hosts-file entries ("0.0.0.0 ads.example.com")
        if (c0.isDigit() && (line.startsWith("127.0.0.1") || line.startsWith("0.0.0.0"))) {
            val h = line.substringBefore('#').trim().split(Regex("\\s+")).getOrNull(1)?.lowercase()
            if (h != null && h.contains('.') && h != "localhost") { block.hostPlain.add(h); ruleCount++ }
            return
        }

        val exception = line.startsWith("@@")
        if (exception) line = line.substring(2)

        var pattern = line
        var optText = ""
        val dollar = line.lastIndexOf('$')
        if (dollar >= 0 && !line.startsWith("/")) {
            val cand = line.substring(dollar + 1)
            if (cand.all { it.isLetterOrDigit() || it in "_-,=~|.*:" }) {
                pattern = line.substring(0, dollar)
                optText = cand
            } else if (Regex("^~?[a-z-]+=.*").matches(cand)) {
                skippedCount++; return // csp=, replace=, header=... with free-text values
            }
        }
        if (pattern.startsWith("/") && pattern.endsWith("/") && pattern.length > 2) { skippedCount++; return } // regex rule

        var typeInc = 0
        var typeExc = 0
        var party = 0
        var important = false
        var docRule = false
        var domInc: ArrayList<String>? = null
        var domExc: ArrayList<String>? = null
        if (optText.isNotEmpty()) {
            for (o0 in optText.split(',')) {
                val o = o0.trim().lowercase()
                if (o.isEmpty()) continue
                val neg = o.startsWith("~")
                val name = o.removePrefix("~")
                when {
                    name == "third-party" || name == "3p" -> party = if (neg) 2 else 1
                    name == "first-party" || name == "1p" -> party = if (neg) 1 else 2
                    name == "important" -> important = true
                    name == "match-case" -> {}
                    name == "script" -> if (neg) typeExc = typeExc or T_SCRIPT else typeInc = typeInc or T_SCRIPT
                    name == "image" -> if (neg) typeExc = typeExc or T_IMAGE else typeInc = typeInc or T_IMAGE
                    name == "stylesheet" || name == "css" -> if (neg) typeExc = typeExc or T_STYLE else typeInc = typeInc or T_STYLE
                    name == "font" -> if (neg) typeExc = typeExc or T_FONT else typeInc = typeInc or T_FONT
                    name == "media" -> if (neg) typeExc = typeExc or T_MEDIA else typeInc = typeInc or T_MEDIA
                    name == "xmlhttprequest" || name == "xhr" -> if (neg) typeExc = typeExc or T_XHR else typeInc = typeInc or T_XHR
                    name == "subdocument" || name == "frame" -> if (neg) typeExc = typeExc or T_SUBDOC else typeInc = typeInc or T_SUBDOC
                    name == "other" || name == "ping" || name == "beacon" || name == "websocket" ||
                        name == "object" || name == "object-subrequest" || name == "webrtc" || name == "empty" ->
                        if (neg) typeExc = typeExc or T_OTHER else typeInc = typeInc or T_OTHER
                    name == "document" || name == "doc" -> { if (!neg) docRule = true }
                    name == "all" -> { /* every type */ }
                    name.startsWith("domain=") -> {
                        for (d0 in name.substring(7).split('|')) {
                            val dn = d0.startsWith("~")
                            val d = d0.removePrefix("~").trim('.')
                            if (d.isEmpty() || d.contains('*') || d.contains('/')) { if (!dn) { skippedCount++; return } else continue }
                            if (dn) { if (domExc == null) domExc = ArrayList(); domExc.add(d) }
                            else { if (domInc == null) domInc = ArrayList(); domInc.add(d) }
                        }
                    }
                    name.startsWith("redirect=") -> { /* treated as a plain block; WebView gets an empty response */ }
                    else -> { skippedCount++; return } // popup, csp, removeparam, replace, header, elemhide, badfilter, ...
                }
            }
        }
        var mask = if (typeInc != 0) typeInc else T_ALL_NON_DOC
        mask = mask and typeExc.inv()
        if (mask == 0) return

        // ---- pattern ----
        var hostAnchored = false
        var startAnchor = false
        var endAnchor = false
        var pat = pattern
        if (pat.startsWith("||")) { hostAnchored = true; pat = pat.substring(2) }
        else if (pat.startsWith("|")) { startAnchor = true; pat = pat.substring(1) }
        if (pat.endsWith("|")) { endAnchor = true; pat = pat.dropLast(1) }
        pat = pat.lowercase()
        if (pat.isEmpty() && !hostAnchored) { skippedCount++; return }

        if (docRule) {
            // Only whole-site exceptions with a plain host are supported.
            if (exception && hostAnchored) {
                val h = pat.substringBefore('^').substringBefore('/')
                if (h.isNotEmpty() && !h.contains('*')) { pageAllow.add(h); ruleCount++ } else skippedCount++
            } else skippedCount++
            return
        }

        val idx = if (exception) allow else block
        val domIncA = domInc?.toTypedArray()
        val domExcA = domExc?.toTypedArray()

        if (hostAnchored) {
            var cut = 0
            while (cut < pat.length && pat[cut] != '^' && pat[cut] != '/' && pat[cut] != '*' && pat[cut] != '?' && pat[cut] != ':') cut++
            val host = pat.substring(0, cut).trim('.')
            if (host.isEmpty() || !host.contains('.') && cut < pat.length) { skippedCount++; return }
            if (host.any { !(it.isLetterOrDigit() || it == '.' || it == '-' || it == '_') }) { skippedCount++; return }
            var rest = pat.substring(cut)
            // "^" alone after the host means "host, then separator or end".
            val plain = (rest.isEmpty() || rest == "^") && !endAnchor
            if (plain && mask == T_ALL_NON_DOC && party == 0 && domIncA == null && domExcA == null && !important) {
                idx.hostPlain.add(host); ruleCount++; return
            }
            if (rest == "^" && !endAnchor) rest = ""
            val r = Rule(collapse(rest).toCharArray(), true, false, endAnchor, mask, party, important, domIncA, domExcA)
            idx.byHost.getOrPut(host) { ArrayList(2) }.add(r)
            ruleCount++
            return
        }

        val body = collapse(pat).let { if (!startAnchor) it.trimStart('*') else it }.let { if (!endAnchor) it.trimEnd('*') else it }
        if (body.isEmpty()) { skippedCount++; return }
        val r = Rule(body.toCharArray(), false, startAnchor, endAnchor, mask, party, important, domIncA, domExcA)
        val tok = pickToken(body, startAnchor, endAnchor)
        if (tok != null) idx.byToken.getOrPut(tok) { ArrayList(2) }.add(r) else idx.generic.add(r)
        ruleCount++
    }

    private fun collapse(s: String): String {
        if (!s.contains("**")) return s
        val sb = StringBuilder()
        var prevStar = false
        for (c in s) { if (c == '*') { if (!prevStar) sb.append(c); prevStar = true } else { sb.append(c); prevStar = false } }
        return sb.toString()
    }

    private fun isCosmetic(l: String): Boolean =
        l.contains("##") || l.contains("#@#") || l.contains("#?#") || l.contains("#$#") ||
            l.contains("#%#") || l.contains("#@$#") || l.contains("#@%#") || l.contains("+js(")

    private fun isTok(c: Char) = (c in 'a'..'z') || (c in '0'..'9')

    /** Longest alphanumeric run that is delimited on both sides, so it must appear as a whole token in a matching URL. */
    private fun pickToken(p: String, startAnchor: Boolean, endAnchor: Boolean): String? {
        var best: String? = null
        var i = 0
        while (i < p.length) {
            if (!isTok(p[i])) { i++; continue }
            var j = i
            while (j < p.length && isTok(p[j])) j++
            val leftOk = if (i == 0) startAnchor else p[i - 1] != '*'
            val rightOk = if (j == p.length) endAnchor else p[j] != '*'
            if (leftOk && rightOk && j - i >= 3) {
                val s = p.substring(i, j)
                if (s != "http" && s != "https" && s != "www" && (best == null || s.length > best.length)) best = s
            }
            i = j
        }
        return best
    }

    // ---------------------------------------------------------------- matching

    private class Ctx(
        val url: String,       // lowercase
        val host: String,
        val authEnd: Int,
        val third: Boolean,
        val type: Int,
        val pageHost: String,
    )

    private fun makeCtx(url: String, pageHost: String, third: Boolean, type: Int): Ctx? {
        val u = url.lowercase()
        val s = u.indexOf("://")
        if (s < 0) return null
        var e = u.length
        for (k in s + 3 until u.length) { val c = u[k]; if (c == '/' || c == '?' || c == '#') { e = k; break } }
        var auth = u.substring(s + 3, e)
        val at = auth.lastIndexOf('@')
        if (at >= 0) auth = auth.substring(at + 1)
        val host = auth.substringBefore(':').trim('.')
        if (host.isEmpty()) return null
        return Ctx(u, host, e, third, type, pageHost.lowercase())
    }

    private fun typeOk(r: Rule, type: Int): Boolean =
        if (type == T_UNKNOWN) (r.typeMask and (T_SCRIPT or T_XHR or T_OTHER)) != 0 else (r.typeMask and type) != 0

    private fun applies(r: Rule, c: Ctx): Boolean {
        if (r.party == 1 && !c.third) return false
        if (r.party == 2 && c.third) return false
        if (!typeOk(r, c.type)) return false
        val inc = r.domInc
        if (inc != null) { if (c.pageHost.isEmpty() || inc.none { domainMatch(c.pageHost, it) }) return false }
        val exc = r.domExc
        if (exc != null && c.pageHost.isNotEmpty() && exc.any { domainMatch(c.pageHost, it) }) return false
        return true
    }

    private fun domainMatch(host: String, d: String) = host == d || host.endsWith(".$d")

    private fun patMatches(r: Rule, c: Ctx): Boolean {
        val u = c.url
        if (r.hostAnchored) {
            if (r.pat.isEmpty()) return true
            return matchHere(r.pat, 0, u, c.authEnd, r.endAnchor)
        }
        if (r.startAnchor) return matchHere(r.pat, 0, u, 0, r.endAnchor)
        val first = r.pat[0]
        val lit = first != '*' && first != '^'
        var k = 0
        while (k <= u.length) {
            if (lit) {
                k = u.indexOf(first, k)
                if (k < 0) return false
            }
            if (matchHere(r.pat, 0, u, k, r.endAnchor)) return true
            k++
        }
        return false
    }

    private fun matchHere(p: CharArray, pStart: Int, s: String, sStart: Int, endAnchor: Boolean): Boolean {
        var pi = pStart
        var si = sStart
        while (pi < p.size) {
            val c = p[pi]
            if (c == '*') {
                while (pi < p.size && p[pi] == '*') pi++
                if (pi == p.size) return true
                var k = si
                while (k <= s.length) { if (matchHere(p, pi, s, k, endAnchor)) return true; k++ }
                return false
            }
            if (c == '^') {
                if (si == s.length) { pi++; continue }
                if (!isSep(s[si])) return false
                pi++; si++; continue
            }
            if (si >= s.length || s[si] != c) return false
            pi++; si++
        }
        return !endAnchor || si == s.length
    }

    private fun isSep(ch: Char) = !(ch.isLetterOrDigit() || ch == '_' || ch == '-' || ch == '.' || ch == '%')

    /** Returns 0 = no match, 1 = match, 2 = match of an $important rule. */
    private fun scan(idx: Index, c: Ctx): Int {
        var hit = 0
        // hostname suffixes
        var h = c.host
        while (true) {
            if (idx.hostPlain.contains(h)) hit = 1
            val l = idx.byHost[h]
            if (l != null) for (r in l) if (applies(r, c) && patMatches(r, c)) { if (r.important) return 2; hit = 1 }
            val i = h.indexOf('.')
            if (i <= 0) break
            h = h.substring(i + 1)
        }
        // URL tokens
        if (idx.byToken.isNotEmpty()) {
            val u = c.url
            var i = 0
            var n = 0
            while (i < u.length && n < 80) {
                if (!isTok(u[i])) { i++; continue }
                var j = i
                while (j < u.length && isTok(u[j])) j++
                if (j - i >= 3) {
                    n++
                    val l = idx.byToken[u.substring(i, j)]
                    if (l != null) for (r in l) if (applies(r, c) && patMatches(r, c)) { if (r.important) return 2; hit = 1 }
                }
                i = j
            }
        }
        for (r in idx.generic) if (applies(r, c) && patMatches(r, c)) { if (r.important) return 2; hit = 1 }
        return hit
    }

    private fun decide(url: String, pageHost: String, third: Boolean, type: Int, ignoreMediaExemption: Boolean): Boolean {
        val c = makeCtx(url, pageHost, third, type) ?: return false
        if (isAllowlisted(c.host)) return false
        if (!ignoreMediaExemption && looksLikeMedia(url)) return false
        if (pageHost.isNotEmpty() && inSet(pageHost, pageAllow)) return false
        val b = scan(block, c)
        if (b == 0) return false
        if (b == 2) return true
        return scan(allow, c) == 0
    }

    /** True if the URL matches an ad/tracker rule, even if it is media (used to keep ad videos out of the download list). */
    fun isAdUrl(url: String, isThirdParty: Boolean, pageHost: String = "", type: Int = T_UNKNOWN): Boolean =
        decide(url, pageHost, isThirdParty, type, ignoreMediaExemption = true)

    fun shouldBlock(url: String, pageHost: String, isThirdParty: Boolean, isMainFrame: Boolean, type: Int = T_UNKNOWN): Boolean {
        if (isMainFrame) return false
        return decide(url, pageHost, isThirdParty, type, ignoreMediaExemption = false)
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
        const val T_SCRIPT = 1
        const val T_STYLE = 2
        const val T_IMAGE = 4
        const val T_FONT = 8
        const val T_MEDIA = 16
        const val T_XHR = 32
        const val T_SUBDOC = 64
        const val T_OTHER = 128
        const val T_ALL_NON_DOC = 255
        const val T_UNKNOWN = 0

        /** WebView does not tell us the resource type, so infer it from the URL and the Accept header. */
        fun guessType(url: String, accept: String?): Int {
            val path = url.substringBefore('?').substringBefore('#').lowercase()
            val ext = path.substringAfterLast('/', "").substringAfterLast('.', "")
            when (ext) {
                "js", "mjs" -> return T_SCRIPT
                "css" -> return T_STYLE
                "png", "jpg", "jpeg", "gif", "webp", "svg", "ico", "bmp", "avif" -> return T_IMAGE
                "woff", "woff2", "ttf", "otf", "eot" -> return T_FONT
                "mp4", "webm", "m3u8", "mpd", "mp3", "m4a", "aac", "ogg", "m4s", "ts", "mkv", "mov" -> return T_MEDIA
            }
            val a = accept?.lowercase() ?: return T_UNKNOWN
            return when {
                a.startsWith("image/") || a.contains("image/avif") || a.contains("image/webp") -> T_IMAGE
                a.startsWith("text/css") -> T_STYLE
                a.startsWith("text/html") -> T_SUBDOC
                a.startsWith("video/") || a.startsWith("audio/") -> T_MEDIA
                else -> T_UNKNOWN
            }
        }

        private val SECOND_LEVEL = setOf("co", "com", "org", "net", "gov", "edu", "ac", "or", "ne", "go")

        /** Approximation of eTLD+1 without a public-suffix list: good enough to tell first-party from third-party. */
        fun registrableDomain(host: String): String {
            val h = host.lowercase().trim('.')
            val parts = h.split('.')
            if (parts.size <= 2) return h
            if (h.matches(Regex("[0-9.]+"))) return h
            val tld = parts.last()
            val sld = parts[parts.size - 2]
            val n = if (tld.length == 2 && sld in SECOND_LEVEL) 3 else 2
            return parts.takeLast(n).joinToString(".")
        }

        fun isThirdParty(requestHost: String, pageHost: String): Boolean {
            if (requestHost.isEmpty() || pageHost.isEmpty()) return false
            return registrableDomain(requestHost) != registrableDomain(pageHost)
        }

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
            if (listOf(".mp4", ".webm", ".mkv", ".m4v", ".mov", ".m3u8", ".mpd", ".m4a", ".mp3", ".aac", ".m4s").any { path.endsWith(it) }) return true
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
