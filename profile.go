package main

// profile.go: "Download profile media" (item 90). A profile or page link
// (a TikTok, Instagram, RedGIFs, Fansly or X profile, a YouTube channel, or
// any page the browser extension sends) is listed with all of its photos,
// carousel images, videos and reels/clips, with titles and thumbnails where
// the site gives them. The user picks all or some in ui/profile.html and
// every picked item becomes a normal download in one folder (a group).
//
// Listing uses gallery-dl (photos, carousels and videos on many sites) and
// yt-dlp (videos), both installed with the other video tools, and the media
// the browser extension collected from the open page. Logged-in content
// comes only from the user's own browser session: the cookies the extension
// sends, or Settings > Video tools > "Use cookies from". Nothing is
// unlocked: paid, locked, private and DRM-protected media that the user's
// account cannot play is not listed, and protected players are only counted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	profileMaxItems    = 1500
	profileMaxPosts    = 500
	profileListTimeout = 5 * time.Minute
)

// MediaItem is one photo or video of a profile or page.
type MediaItem struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"` // "video", "photo" or "post" (a post link whose media is unknown)
	Title    string  `json:"title,omitempty"`
	Thumb    string  `json:"thumb,omitempty"`
	Page     string  `json:"page,omitempty"`   // the post/video page (downloaded with yt-dlp)
	Media    string  `json:"media,omitempty"`  // the photo or video file itself
	Stream   string  `json:"stream,omitempty"` // "hls"/"dash" when Media is a stream playlist
	Ext      string  `json:"ext,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Post     string  `json:"post,omitempty"`  // post key ("ig:Cx1", "tt:123"); carousel images share it
	Index    int     `json:"index,omitempty"` // place in a carousel (1, 2, ...)
	Skip     string  `json:"skip,omitempty"`  // why it can't be downloaded
	// Cookie: cookies the site's CDN needs for this one file (Fansly's
	// signed video playlists). Only from the extension; never shown to
	// the window (Profile clears it).
	Cookie string `json:"cookie,omitempty"`
}

// ProfileRequest is what the browser extension sends for "Download all
// media on this page with BDDM": the page, the browser's session details
// (AddRequest) and the media it found on the page.
type ProfileRequest struct {
	AddRequest
	Items     []MediaItem `json:"items"`
	Protected int         `json:"protected,omitempty"` // DRM, locked or stream-only media on the page
	// Name: the profile's user name as the page shows it (the extension
	// reads it from the site, e.g. Fansly's account), for the folder name.
	Name string `json:"name,omitempty"`
	// Complete: the extension listed the whole profile through the site's
	// own API with the user's session (not only what is scrolled into view).
	Complete bool `json:"complete,omitempty"`
	// Limited: the extension stopped at its limit (the newest posts only).
	Limited bool `json:"limited,omitempty"`
}

type profileList struct {
	id        string
	req       AddRequest // the page and the browser's session details
	site      string
	name      string
	state     string // "loading", "ready", "error"
	err       string
	note      string
	items     []MediaItem
	fromPage  int
	complete  bool // the page's list is the whole profile (site API)
	protected int
	dir       string
	folder    string
	limited   bool
	cancel    context.CancelFunc
}

// ProfileView is what the profile window shows.
type ProfileView struct {
	ID        string      `json:"id"`
	URL       string      `json:"url"`
	Site      string      `json:"site"`
	Name      string      `json:"name"`
	State     string      `json:"state"`
	Error     string      `json:"error,omitempty"`
	Note      string      `json:"note,omitempty"`
	Items     []MediaItem `json:"items"`
	Videos    int         `json:"videos"`
	Photos    int         `json:"photos"`
	Protected int         `json:"protected,omitempty"`
	Dir       string      `json:"dir"`
	Folder    string      `json:"folder"`
	Limited   bool        `json:"limited,omitempty"`
}

// ProfileResult tells the window what happened after Download.
type ProfileResult struct {
	Added   int      `json:"added"`
	Skipped []string `json:"skipped,omitempty"` // downloaded before
	Folder  string   `json:"folder"`
}

// ---------------------------------------------------------------- which links

var (
	tiktokUserRe    = regexp.MustCompile(`^/@([\w.\-]+)/?$`)
	instagramUserRe = regexp.MustCompile(`^/([A-Za-z0-9._]{1,30})(?:/(?:posts|reels|tagged|photos))?/?$`)
	redgifsUserRe   = regexp.MustCompile(`^/users/([^/?#]+)(?:/(?:collections(?:/[^/?#]+)?|gifs|images))?/?$`)
	fanslyUserRe    = regexp.MustCompile(`^/([A-Za-z0-9_.\-]{2,40})(?:/(?:posts|media)(?:/wall/\d+)?)?/?$`)
	xUserRe         = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})(?:/(?:media|photos|videos))?/?$`)
	youtubeUserRe   = regexp.MustCompile(`^/(?:@[\w.\-]+|channel/[\w-]+|c/[^/]+|user/[^/]+)(?:/(?:videos|shorts|streams|featured))?/?$`)
)

var instagramReserved = setOf("p", "reel", "reels", "tv", "explore", "accounts", "stories", "direct", "about",
	"legal", "developer", "web", "api", "emails", "challenge", "privacy", "terms", "session", "oauth", "s")
var fanslyReserved = setOf("post", "home", "lists", "explore", "messages", "notifications", "settings", "search",
	"collection", "subscriptions", "wallet", "login", "signup", "tos", "privacy", "faq", "application", "live")
var xReserved = setOf("home", "explore", "notifications", "messages", "i", "settings", "search", "compose",
	"login", "signup", "tos", "privacy", "hashtag", "intent", "share", "jobs")

func setOf(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x] = true
	}
	return m
}

// profileSite reports whether a link is a profile/creator page whose media
// BDDM can list, with the site's name and the profile's name.
func profileSite(raw string) (site, name string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", false
	}
	h := siteHost(u.Hostname())
	p := u.Path
	switch {
	case h == "tiktok.com":
		if m := tiktokUserRe.FindStringSubmatch(p); m != nil {
			return "TikTok", "@" + m[1], true
		}
	case h == "instagram.com":
		if m := instagramUserRe.FindStringSubmatch(p); m != nil && !instagramReserved[strings.ToLower(m[1])] {
			return "Instagram", m[1], true
		}
	case h == "redgifs.com" || strings.HasSuffix(h, ".redgifs.com"):
		if m := redgifsUserRe.FindStringSubmatch(p); m != nil {
			n, _ := url.PathUnescape(m[1])
			return "RedGIFs", n, true
		}
	case h == "fansly.com":
		if m := fanslyUserRe.FindStringSubmatch(p); m != nil && !fanslyReserved[strings.ToLower(m[1])] {
			return "Fansly", m[1], true
		}
	case h == "x.com" || h == "twitter.com":
		if m := xUserRe.FindStringSubmatch(p); m != nil && !xReserved[strings.ToLower(m[1])] {
			return "X", "@" + m[1], true
		}
	case h == "youtube.com":
		if youtubeUserRe.MatchString(p) {
			seg := strings.Split(strings.Trim(p, "/"), "/")
			n := seg[0]
			if len(seg) > 1 && !strings.HasPrefix(n, "@") {
				n = seg[1]
			}
			return "YouTube", n, true
		}
	}
	return "", "", false
}

// genericPageSite names a page that is not a known profile ("example.com").
func genericPageSite(raw string) (site, name string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "Web page", "Page"
	}
	h := siteHost(u.Hostname())
	// The last part of the path that is a name: not a number (a wall,
	// page or account id) and not a tab such as "posts" or "media".
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	n := ""
	for i := len(parts) - 1; i >= 0; i-- {
		s, _ := url.PathUnescape(parts[i])
		if s == "" || numericName(s) || genericPathWords[strings.ToLower(s)] {
			continue
		}
		n = s
		break
	}
	if n == "" {
		n = h
	}
	return h, n
}

var genericPathWords = setOf("posts", "post", "media", "wall", "walls", "photos", "videos", "gallery", "page",
	"p", "user", "users", "u", "profile", "creator", "index.html", "home", "feed", "tab", "all")

// numericName: an id rather than a name ("785496081252233221").
func numericName(s string) bool {
	s = strings.TrimPrefix(strings.TrimSpace(s), "@")
	if len(s) < 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cleanProfileName checks a user name sent by the page.
func cleanProfileName(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	if s == "" || len(s) > 60 || numericName(s) || strings.ContainsAny(s, "/\\<>:\"|?*\r\n\t") {
		return ""
	}
	return s
}

// postKey turns a post/video page into a key that the same post gets from
// every source (the page, gallery-dl, yt-dlp), so it is listed only once.
var postKeyRules = []struct {
	host   string
	re     *regexp.Regexp
	prefix string
}{
	{"instagram.com", regexp.MustCompile(`^/(?:[^/]+/)?(?:p|reel|reels|tv)/([\w-]+)`), "ig:"},
	{"tiktok.com", regexp.MustCompile(`^/@[^/]*/(?:video|photo)/(\d+)`), "tt:"},
	{"redgifs.com", regexp.MustCompile(`^/(?:watch|ifr|i)/([\w-]+)`), "rg:"},
	{"fansly.com", regexp.MustCompile(`^/post/(\d+)`), "fs:"},
	{"x.com", regexp.MustCompile(`^/[^/]+/status/(\d+)`), "x:"},
	{"twitter.com", regexp.MustCompile(`^/[^/]+/status/(\d+)`), "x:"},
	{"youtube.com", regexp.MustCompile(`^/shorts/([\w-]+)`), "yt:"},
}

func postKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	h := siteHost(u.Hostname())
	if h == "youtube.com" && u.Path == "/watch" && u.Query().Get("v") != "" {
		return "yt:" + u.Query().Get("v")
	}
	if h == "youtu.be" && len(u.Path) > 1 {
		return "yt:" + strings.Trim(u.Path, "/")
	}
	for _, r := range postKeyRules {
		if h == r.host || strings.HasSuffix(h, "."+r.host) {
			if m := r.re.FindStringSubmatch(u.Path); m != nil {
				id := m[1]
				if r.prefix == "rg:" {
					id = strings.ToLower(id)
				}
				return r.prefix + id
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------- window

// AddProfile starts listing a profile or page for the profile window.
// pageItems are the media the extension found on the open page (or nil).
func (m *Manager) AddProfile(pr ProfileRequest) (string, error) {
	r, pageItems, protected := pr.AddRequest, pr.Items, pr.Protected
	if protected < 0 {
		protected = 0
	}
	r.URL = strings.TrimSpace(r.URL)
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("Please enter a valid http:// or https:// link.")
	}
	site, name, known := profileSite(r.URL)
	if !known {
		site, name = genericPageSite(r.URL)
	}
	if pn := cleanProfileName(pr.Name); pn != "" && (!known || site == "Fansly" || numericName(name)) {
		// The page knows the real user name (e.g. a Fansly page whose
		// address has the account's number).
		if strings.HasPrefix(name, "@") && !strings.HasPrefix(pn, "@") {
			pn = "@" + pn
		}
		name = pn
	}
	items := cleanPageItems(pageItems, r.URL)
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return "", errors.New("BDDM is shutting down")
	}
	if m.profiles == nil {
		m.profiles = map[string]*profileList{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), profileListTimeout+time.Minute)
	p := &profileList{id: newID(), req: r, site: site, name: name, state: "loading", items: items,
		fromPage: len(items), complete: pr.Complete && len(items) > 0, limited: pr.Limited, protected: protected, dir: m.settings.dirForCategory("Video"),
		folder: profileFolderName(site, name), cancel: cancel}
	m.profiles[p.id] = p
	m.mu.Unlock()

	go m.listProfile(ctx, p, known)
	go func() {
		time.Sleep(pendingTimeout)
		m.CancelProfile(p.id)
	}()
	return p.id, nil
}

// profileFolderName is the folder the media goes into ("soloshinee (TikTok)").
func profileFolderName(site, name string) string {
	n := strings.TrimPrefix(strings.TrimSpace(name), "@")
	n = firstNonEmpty(sanitizeName(n), "Profile")
	if site != "" && !strings.Contains(site, ".") && site != "Web page" {
		return n + " (" + site + ")"
	}
	return n
}

func (m *Manager) Profile(id string) (ProfileView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.profiles[id]
	if p == nil {
		return ProfileView{}, errors.New("This window has expired. Paste the profile link again.")
	}
	v := ProfileView{ID: p.id, URL: p.req.URL, Site: p.site, Name: p.name, State: p.state, Error: p.err,
		Note: p.note, Items: append([]MediaItem{}, p.items...), Protected: p.protected, Dir: p.dir,
		Folder: p.folder, Limited: p.limited}
	for i := range v.Items {
		v.Items[i].Cookie = ""
	}
	for _, it := range p.items {
		switch it.Kind {
		case "photo":
			v.Photos++
		default:
			v.Videos++
		}
	}
	return v, nil
}

func (m *Manager) CancelProfile(id string) {
	m.mu.Lock()
	p := m.profiles[id]
	delete(m.profiles, id)
	m.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

// listProfile lists the profile with the tools and merges in what the
// extension found on the page.
func (m *Manager) listProfile(ctx context.Context, p *profileList, known bool) {
	m.mu.Lock()
	req := p.req
	site := p.site
	pageCount := p.fromPage
	m.mu.Unlock()

	var toolItems []MediaItem
	var toolErr error
	var limited bool
	// Fansly needs a sign-in token the tools can't get from cookies; its
	// creator pages are listed from the open page only. Unknown pages use
	// the page's media first and the tools only when nothing was found.
	useTools := known && site != "Fansly"
	if !known && pageCount == 0 {
		useTools = true
	}
	if useTools {
		toolItems, limited, toolErr = m.listWithTools(ctx, req, site)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.profiles[p.id] != p {
		return
	}
	p.items = mergeMedia(p.items, toolItems)
	p.limited = p.limited || limited
	if len(p.items) > profileMaxItems {
		p.items = p.items[:profileMaxItems]
		p.limited = true
	}
	switch {
	case len(p.items) == 0 && toolErr != nil:
		p.state, p.err = "error", friendlyProfileError(site, toolErr.Error(), pageCount > 0 || p.protected > 0)
	case len(p.items) == 0:
		p.state = "error"
		p.err = "No photos or videos that your account can open were found on this page."
		if site == "Fansly" {
			p.err = fanslyHelp
		}
	default:
		p.state = "ready"
		if toolErr != nil && pageCount > 0 {
			p.note = fmt.Sprintf("Showing the %d item(s) that were loaded on the page. Scroll further down on the page and send it to BDDM again to add more.", len(p.items))
		} else if p.complete {
			// The extension listed the whole profile with the site's API.
		} else if site == "Fansly" || (!useTools && pageCount > 0) {
			p.note = "Showing what is loaded on the page. Scroll further down on the page and send it to BDDM again to add more."
		}
	}
	if p.protected > 0 {
		extra := fmt.Sprintf("%d item(s) are locked, paid or protected (DRM) for your account, so BDDM can't save them.", p.protected)
		p.note = strings.TrimSpace(p.note + " " + extra)
	}
	if p.limited {
		p.note = strings.TrimSpace(p.note + fmt.Sprintf(" Only the newest %d posts are listed.", profileMaxPosts))
	}
}

const fanslyHelp = "Fansly shows media only to signed-in accounts. Sign in to Fansly in your browser, open the creator's page, scroll to load the posts you want, then right-click the page and choose \"Download all media on this page with BDDM\". Only media your account can already open is listed; locked or paid posts are not."

const extensionHelp = "Open the profile in your browser (signed in if the site needs it), then right-click the page and choose \"Download all media on this page with BDDM\". BDDM then uses your browser's session and lists what your account can see."

// friendlyProfileError turns a tool's error into plain English.
func friendlyProfileError(site, msg string, fromPage bool) string {
	low := strings.ToLower(msg)
	if site == "Fansly" {
		return fanslyHelp
	}
	tail := " " + extensionHelp
	if fromPage {
		tail = " Make sure you are signed in on the page and the media is visible there, then try again."
	}
	switch {
	case strings.Contains(low, "403") || strings.Contains(low, "forbidden") || strings.Contains(low, "401"):
		return "The site refused BDDM (HTTP 403 Forbidden) when it asked for this profile." + tail
	case strings.Contains(low, "login") || strings.Contains(low, "log in") || strings.Contains(low, "sign in") ||
		strings.Contains(low, "authrequired") || strings.Contains(low, "authorization") || strings.Contains(low, "cookies") ||
		strings.Contains(low, "private") || strings.Contains(low, "token"):
		return "This profile needs you to be signed in, or your account can't see it." + tail
	case strings.Contains(low, "not found") || strings.Contains(low, "404") || strings.Contains(low, "notfound"):
		return "This profile was not found. Check the link."
	case strings.Contains(low, "unsupported url") || strings.Contains(low, "noextractor") || strings.Contains(low, "no suitable extractor"):
		return "BDDM can't list this page by itself." + tail
	case strings.Contains(low, "rate") && strings.Contains(low, "limit"):
		return "The site is limiting requests right now. Wait a few minutes and try again."
	case strings.Contains(low, "took too long"):
		return "Listing this profile took too long." + tail
	case strings.Contains(low, "not installed") || strings.Contains(low, "could not download"):
		return msg
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "ERROR:"))
	if msg == "" {
		return "BDDM could not list this profile." + tail
	}
	return "BDDM could not list this profile: " + strings.TrimRight(msg, ". ") + "." + tail
}

// ---------------------------------------------------------------- tools

// listWithTools runs gallery-dl and yt-dlp (whichever can handle the site)
// at the same time and merges what they find.
func (m *Manager) listWithTools(ctx context.Context, req AddRequest, site string) ([]MediaItem, bool, error) {
	useGDL := site != "YouTube"
	useYT := site != "Instagram" && site != "Fansly" && site != "X"
	gdl := ""
	if useGDL {
		gdl = m.tools.Ensure(ctx, "gallery-dl", 3*time.Minute)
	}
	yt := ""
	if useYT {
		yt = m.tools.Find("yt-dlp")
	}
	if gdl == "" && yt == "" {
		return nil, false, errors.New("The tools that list profiles (gallery-dl and yt-dlp) are not installed yet. Open BDDM Settings > Video tools and press \"Download missing tools\", then try again.")
	}
	lctx, cancel := context.WithTimeout(ctx, profileListTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var gItems, yItems []MediaItem
	var gErr, yErr error
	var gLimited bool
	if gdl != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gItems, gLimited, gErr = m.listGalleryDL(lctx, gdl, req, site)
		}()
	}
	if yt != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			yItems, yErr = m.listYtdlp(lctx, req)
		}()
	}
	wg.Wait()
	if lctx.Err() == context.DeadlineExceeded && len(gItems) == 0 && len(yItems) == 0 {
		return nil, false, errors.New("Listing took too long.")
	}
	items := mergeMedia(gItems, yItems)
	if len(items) > 0 {
		return items, gLimited, nil
	}
	if gErr != nil {
		return nil, false, gErr
	}
	if yErr != nil {
		return nil, false, yErr
	}
	return nil, false, nil
}

// toolJob is a job-like holder so the yt-dlp helpers get the page's
// cookies, browser details and the proxy.
func toolJob(req AddRequest) *Job {
	j := &Job{URL: req.URL, Referer: strings.TrimSpace(req.Referer), Cookie: strings.TrimSpace(req.Cookie), Kind: "video", Name: "profile"}
	j.applyBrowser(req)
	return j
}

func (m *Manager) listYtdlp(ctx context.Context, req AddRequest) ([]MediaItem, error) {
	j := toolJob(req)
	args, done := m.baseArgs(j)
	defer done()
	args = append(args, "-J", "--flat-playlist", "--yes-playlist", "--playlist-end", strconv.Itoa(profileMaxPosts), "--", req.URL)
	cmd := m.ytdlpCommand(ctx, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := lastErrorLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, errors.New("yt-dlp gave an answer BDDM could not read")
	}
	return parseYtdlpProfile(data), nil
}

// parseYtdlpProfile reads the entries of a flat yt-dlp playlist.
func parseYtdlpProfile(data map[string]any) []MediaItem {
	var out []MediaItem
	entries, _ := data["entries"].([]any)
	if len(entries) == 0 {
		if t, _ := data["_type"].(string); t != "playlist" {
			if u := firstNonEmpty(str(data, "webpage_url"), str(data, "original_url")); u != "" {
				entries = []any{data}
			}
		}
	}
	for _, e := range entries {
		d, ok := e.(map[string]any)
		if !ok {
			continue
		}
		page := firstNonEmpty(str(d, "webpage_url"), str(d, "url"), str(d, "original_url"))
		if page == "" || !strings.HasPrefix(page, "http") {
			continue
		}
		it := MediaItem{Kind: "video", Title: strings.TrimSpace(firstNonEmpty(str(d, "title"), str(d, "description"))),
			Page: page, Duration: num(d, "duration"), Post: postKey(page)}
		it.Thumb = safeVideoThumb(ytdlpThumb(d))
		out = append(out, it)
	}
	return out
}

func ytdlpThumb(d map[string]any) string {
	if t := str(d, "thumbnail"); t != "" {
		return t
	}
	list, _ := d["thumbnails"].([]any)
	best, bestW := "", -1.0
	for _, x := range list {
		t, ok := x.(map[string]any)
		if !ok || str(t, "url") == "" {
			continue
		}
		w := num(t, "width")
		if best == "" || (w > bestW && w <= 720) {
			best, bestW = str(t, "url"), w
		}
	}
	return best
}

// galleryDLArgs are the options of a gallery-dl listing run.
func (m *Manager) galleryDLArgs(req AddRequest, cookieFile string) []string {
	args := []string{"--config-ignore", "-J", "--post-range", "1-" + strconv.Itoa(profileMaxPosts),
		"-o", "output.mode=null", "-o", "output.progress=false"}
	if req.UserAgent != "" {
		args = append(args, "--user-agent", req.UserAgent)
	}
	if addr := m.yieldProxyAddr(); addr != "" {
		args = append(args, "--proxy", "http://"+addr)
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	} else if b, _ := cookiesBrowser.Load().(string); b != "" {
		args = append(args, "--cookies-from-browser", b)
	}
	return append(args, "--", req.URL)
}

func (m *Manager) listGalleryDL(ctx context.Context, exe string, req AddRequest, site string) ([]MediaItem, bool, error) {
	j := toolJob(req)
	cookieFile, remove := "", func() {}
	if j.Cookie != "" || len(j.jar) > 0 {
		if p, rm, err := writeCookieFile("", j.URL, j.Cookie, j.jar); err == nil && p != "" {
			cookieFile, remove = p, rm
		}
	}
	defer remove()
	cmd := exec.CommandContext(ctx, exe, m.galleryDLArgs(req, cookieFile)...)
	prepareProc(cmd)
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 10 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	items, limited, gerr := parseGalleryDL(out, site)
	if len(items) > 0 {
		return items, limited, nil
	}
	if gerr != nil {
		return nil, false, gerr
	}
	if err != nil {
		msg := lastErrorLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, false, errors.New(msg)
	}
	return nil, false, nil
}

var videoExts = setOf("mp4", "m4v", "webm", "mov", "mkv", "avi", "flv", "m3u8", "mpd", "ts", "gifv")
var photoExts = setOf("jpg", "jpeg", "png", "webp", "gif", "avif", "heic", "bmp")

// parseGalleryDL reads gallery-dl's -J output: a JSON list of messages
// ([2, info] a post, [3, url, info] a file, [6, url, info] a link for
// another extractor, [-1, {error, message}] an error).
func parseGalleryDL(out []byte, site string) ([]MediaItem, bool, error) {
	var msgs []json.RawMessage
	if err := json.Unmarshal(out, &msgs); err != nil {
		if len(strings.TrimSpace(string(out))) == 0 {
			return nil, false, nil
		}
		return nil, false, errors.New("gallery-dl gave an answer BDDM could not read")
	}
	var items []MediaItem
	var firstErr error
	posts := map[string]bool{}
	for _, raw := range msgs {
		var msg []json.RawMessage
		if json.Unmarshal(raw, &msg) != nil || len(msg) < 2 {
			continue
		}
		var typ int
		if json.Unmarshal(msg[0], &typ) != nil {
			continue
		}
		switch typ {
		case -1:
			var e map[string]any
			_ = json.Unmarshal(msg[1], &e)
			if firstErr == nil {
				firstErr = errors.New(strings.TrimSpace(str(e, "error") + ": " + str(e, "message")))
			}
		case 3, 6:
			if len(msg) < 3 {
				continue
			}
			var u string
			var kw map[string]any
			if json.Unmarshal(msg[1], &u) != nil || json.Unmarshal(msg[2], &kw) != nil {
				continue
			}
			it, ok := galleryDLItem(u, kw, site, typ == 6)
			if !ok {
				continue
			}
			if it.Post != "" {
				posts[it.Post] = true
			}
			items = append(items, it)
		}
	}
	return items, len(posts) >= profileMaxPosts, firstErr
}

func kwString(kw map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := kw[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

// galleryDLItem turns one gallery-dl file into a MediaItem.
func galleryDLItem(u string, kw map[string]any, site string, queued bool) (MediaItem, bool) {
	var it MediaItem
	page := ""
	if strings.HasPrefix(u, "ytdl:") {
		page = strings.TrimPrefix(u, "ytdl:")
		u = ""
	}
	if queued {
		// A link another extractor would read: keep it only if it is a
		// post/video page (downloaded with yt-dlp).
		if postKey(u) == "" && !isVideoPage(u) {
			return it, false
		}
		page, u = u, ""
	}
	ext := strings.ToLower(strings.TrimPrefix(kwString(kw, "extension"), "."))
	if ext == "" && u != "" {
		if pu, err := url.Parse(u); err == nil {
			ext = strings.ToLower(strings.TrimPrefix(path.Ext(pu.Path), "."))
		}
	}
	typ := strings.ToLower(kwString(kw, "type", "post_type", "media_type"))
	switch {
	case typ == "audio" || ext == "mp3" || ext == "m4a" || typ == "cover" || typ == "avatar":
		return it, false
	case typ == "video" || typ == "gif" && videoExts[ext] || videoExts[ext]:
		it.Kind = "video"
	case typ == "image" || typ == "photo" || photoExts[ext]:
		it.Kind = "photo"
	case page != "":
		it.Kind = "video"
	default:
		return it, false
	}
	it.Media = u
	it.Ext = ext
	it.Title = strings.TrimSpace(firstNonEmpty(kwString(kw, "title"), kwString(kw, "description"), kwString(kw, "content"), kwString(kw, "caption")))
	if len([]rune(it.Title)) > 200 {
		it.Title = string([]rune(it.Title)[:200])
	}
	if n, ok := kw["num"].(float64); ok && n > 0 {
		it.Index = int(n)
	}
	if d, ok := kw["duration"].(float64); ok {
		it.Duration = d
	}
	// The post this file belongs to.
	postURL := firstNonEmpty(kwString(kw, "post_url"), page)
	switch site {
	case "Instagram":
		if sc := kwString(kw, "post_shortcode", "shortcode"); sc != "" {
			it.Post = "ig:" + sc
			if postURL == "" {
				postURL = "https://www.instagram.com/p/" + sc + "/"
			}
		}
	case "TikTok":
		if id := kwString(kw, "id"); id != "" {
			it.Post = "tt:" + id
			if postURL == "" {
				user := ""
				if a, ok := kw["author"].(map[string]any); ok {
					user = kwString(a, "uniqueId")
				}
				if user == "" {
					user = kwString(kw, "user")
				}
				what := "video"
				if it.Kind == "photo" {
					what = "photo"
				}
				postURL = "https://www.tiktok.com/@" + user + "/" + what + "/" + id
			}
		}
	case "RedGIFs":
		if id := kwString(kw, "id"); id != "" {
			it.Post = "rg:" + strings.ToLower(id)
			if postURL == "" {
				postURL = "https://www.redgifs.com/watch/" + strings.ToLower(id)
			}
		}
	case "Fansly":
		if id := kwString(kw, "post_id"); id != "" {
			it.Post = "fs:" + id
		}
	case "X":
		if id := kwString(kw, "tweet_id"); id != "" {
			it.Post = "x:" + id
		}
	}
	if it.Post == "" && postURL != "" {
		it.Post = postKey(postURL)
	}
	if it.Kind == "video" && (site == "TikTok" || it.Media == "") && postURL != "" {
		// TikTok's video files need the app's own tokens: let yt-dlp
		// download the video from its page instead.
		it.Page = postURL
	}
	if it.Media == "" && it.Page == "" {
		return it, false
	}
	thumb := kwString(kw, "thumbnail", "display_url", "poster", "cover", "preview")
	if thumb == "" && it.Kind == "photo" {
		thumb = it.Media
	}
	if t, ok := kw["urls"].(map[string]any); ok && thumb == "" {
		thumb = kwString(t, "thumbnail", "poster", "sd")
	}
	it.Thumb = safeVideoThumb(thumb)
	if it.Media != "" && (ext == "m3u8" || ext == "mpd") {
		it.Stream = map[string]string{"m3u8": "hls", "mpd": "dash"}[ext]
	}
	return it, true
}

// ---------------------------------------------------------------- page items

// cleanPageItems checks what the extension found on the page.
func cleanPageItems(in []MediaItem, pageURL string) []MediaItem {
	var out []MediaItem
	for _, it := range in {
		it.Title = strings.TrimSpace(it.Title)
		if len([]rune(it.Title)) > 200 {
			it.Title = string([]rune(it.Title)[:200])
		}
		it.Thumb = safeVideoThumb(it.Thumb)
		it.Media = webLink(it.Media)
		it.Page = webLink(it.Page)
		it.Stream = streamKind(it.Stream)
		it.Skip, it.ID = "", ""
		if len(it.Cookie) > 8<<10 || strings.ContainsAny(it.Cookie, "\r\n") {
			it.Cookie = ""
		}
		if it.Kind == "photo" && siteChromeImage(it.Media, pageURL) {
			continue // a logo, icon or other part of the site, not media
		}
		switch it.Kind {
		case "photo", "video":
		case "post":
			if it.Page == "" {
				continue
			}
		default:
			continue
		}
		if it.Media == "" && it.Page == "" {
			continue
		}
		if it.Kind == "photo" && it.Media == "" {
			continue
		}
		if it.Media != "" && it.Stream == "" && isStreamURL(it.Media) {
			if strings.HasSuffix(strings.ToLower(strings.SplitN(it.Media, "?", 2)[0]), ".mpd") {
				it.Stream = "dash"
			} else {
				it.Stream = "hls"
			}
		}
		if it.Post == "" {
			it.Post = postKey(it.Page)
		} else if !postKeyRe.MatchString(it.Post) {
			it.Post = postKey(it.Page)
		}
		if it.Ext == "" && it.Media != "" {
			if pu, err := url.Parse(it.Media); err == nil {
				it.Ext = strings.ToLower(strings.TrimPrefix(path.Ext(pu.Path), "."))
			}
		}
		if len(it.Ext) > 5 {
			it.Ext = ""
		}
		out = append(out, it)
		if len(out) >= profileMaxItems {
			break
		}
	}
	return out
}

var chromeImageRe = regexp.MustCompile(`(?i)(^|[/_.\-])(logo|logos|favicon|sprite|sprites|icon|icons|emoji|emojis|badge|badges|placeholder|spinner|loader)([/_.\-]|$)`)

// siteChromeImage: an image that is part of the site itself (a logo, an
// icon, an SVG, a Fansly site asset), not a photo of the profile.
func siteChromeImage(media, pageURL string) bool {
	u, err := url.Parse(media)
	if err != nil {
		return true
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if ext == ".svg" || ext == ".ico" {
		return true
	}
	if chromeImageRe.MatchString(u.Path) {
		return true
	}
	// Fansly's own pictures (the wordmark, the heart-lock logo) come from
	// fansly.com itself; post media comes from its CDN (cdn3.fansly.com).
	if pu, err := url.Parse(pageURL); err == nil && siteHost(pu.Hostname()) == "fansly.com" {
		if h := strings.ToLower(u.Hostname()); h == "fansly.com" || h == "www.fansly.com" {
			return true
		}
	}
	return false
}

func webLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8<<10 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return raw
}

// mediaKey identifies one item: the same file or the same post page.
func mediaKey(it MediaItem) string {
	if it.Post != "" {
		k := it.Post + "#" + it.Kind
		if it.Kind == "photo" || (it.Kind == "video" && it.Index > 0) {
			k += "#" + strconv.Itoa(it.Index)
		}
		return k
	}
	link := firstNonEmpty(it.Media, it.Page)
	if u, err := url.Parse(link); err == nil {
		return strings.ToLower(siteHost(u.Hostname())) + strings.TrimRight(u.Path, "/")
	}
	return link
}

// mergeMedia adds b to a, without the items a already has. A post that a
// tool listed in full replaces the page's bare link to that post.
func mergeMedia(a, b []MediaItem) []MediaItem {
	full := map[string]bool{}
	for _, it := range append(append([]MediaItem{}, a...), b...) {
		if it.Post != "" && it.Kind != "post" {
			full[it.Post] = true
		}
	}
	seen := map[string]int{}
	var out []MediaItem
	add := func(it MediaItem) {
		if it.Kind == "post" && full[it.Post] {
			return
		}
		k := mediaKey(it)
		if i, ok := seen[k]; ok {
			o := &out[i]
			if o.Thumb == "" {
				o.Thumb = it.Thumb
			}
			if o.Title == "" {
				o.Title = it.Title
			}
			if o.Page == "" {
				o.Page = it.Page
			}
			if o.Media == "" {
				o.Media, o.Stream, o.Ext, o.Cookie = it.Media, it.Stream, it.Ext, it.Cookie
			}
			if o.Duration == 0 {
				o.Duration = it.Duration
			}
			return
		}
		seen[k] = len(out)
		out = append(out, it)
	}
	for _, it := range a {
		add(it)
	}
	for _, it := range b {
		add(it)
	}
	for i := range out {
		out[i].ID = "m" + strconv.Itoa(i+1)
		if out[i].Kind == "photo" && out[i].Media == "" {
			out[i].Skip = "BDDM could not find this photo's file."
		}
	}
	return out
}

// ---------------------------------------------------------------- download

// StartProfile queues the picked items. quality is the video quality
// ("best", "1080", "720", "480"); again=true also downloads items that
// were downloaded before.
func (m *Manager) StartProfile(id string, ids []string, dir, folder, quality string, later, again bool) (ProfileResult, error) {
	var res ProfileResult
	dir = strings.TrimSpace(dir)
	m.mu.Lock()
	p := m.profiles[id]
	if p == nil || p.state != "ready" {
		m.mu.Unlock()
		return res, errors.New("This window has expired. Paste the profile link again.")
	}
	if dir == "" {
		dir = p.dir
	}
	m.mu.Unlock()
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return res, errors.New("Please choose a full folder path, for example D:\\Videos.")
	}
	switch quality {
	case "best", "1080", "720", "480":
	default:
		quality = "best"
	}
	want := map[string]bool{}
	for _, x := range ids {
		want[x] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.profiles[id] != p {
		return res, errors.New("This window has expired. Paste the profile link again.")
	}
	folder = sanitizeName(strings.TrimSpace(folder))
	if folder == "" {
		folder = p.folder
	}
	base := filepath.Join(dir, folder)
	res.Folder = base
	var picks []MediaItem
	for _, it := range p.items {
		if want[it.ID] && it.Skip == "" {
			picks = append(picks, it)
		}
	}
	if len(picks) == 0 {
		return res, errors.New("Nothing is selected.")
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return res, errors.New("Cannot create the folder " + base + ": " + err.Error())
	}
	g := &Group{ID: newID(), Name: folder, Dir: base, URL: p.req.URL, Created: time.Now()}
	names := map[string]bool{}
	for i, it := range picks {
		j := profileJob(it, p, base, quality, i+1, names, m.settings.Parts)
		if j == nil {
			continue
		}
		if !again && m.findDoneLocked(j.URL) != nil {
			res.Skipped = append(res.Skipped, firstNonEmpty(it.Title, j.Name))
			continue
		}
		j.Group = g.ID
		if later {
			j.Status = StatusPaused
		} else {
			j.Status = StatusQueued
		}
		m.jobs = append(m.jobs, j)
		res.Added++
	}
	if res.Added > 0 {
		m.groups = append(m.groups, g)
		m.saveGroupsLocked()
		m.rememberDirLocked("Video", dir)
	}
	m.scheduleLocked()
	m.saveHistoryLocked()
	if res.Added > 0 || len(res.Skipped) > 0 {
		delete(m.profiles, id)
		p.cancel()
	}
	return res, nil
}

// profileJob makes the download of one item: a photo or video file goes to
// BDDM's own engine; a post/video page goes to yt-dlp.
func profileJob(it MediaItem, p *profileList, dir, quality string, n int, names map[string]bool, parts int) *Job {
	title := strings.TrimSpace(it.Title)
	label := map[string]string{"photo": "Photo", "video": "Video", "post": "Post"}[it.Kind]
	j := &Job{ID: newID(), Dir: dir, Parts: parts, Started: time.Now(), Referer: p.req.URL, Cookie: strings.TrimSpace(p.req.Cookie)}
	j.applyBrowser(p.req)
	j.Referer = p.req.URL
	if c := strings.TrimSpace(it.Cookie); c != "" && it.Media != "" {
		// The CDN's own cookies for this file (signed video playlists).
		j.Cookie = c
	}
	// Post links and videos known only by their page go to yt-dlp, and so
	// do TikTok videos (their files need the app's own tokens).
	viaPage := it.Page != "" && (it.Kind == "post" ||
		(it.Kind == "video" && (it.Media == "" || strings.HasPrefix(postKey(it.Page), "tt:"))))
	switch {
	case viaPage:
		j.URL = it.Page
		j.Kind = "video"
		vt := firstNonEmpty(title, label+" "+strconv.Itoa(n))
		j.Video = &VideoOptions{Quality: quality, Title: vt}
		j.Name = firstNonEmpty(cleanTitle(vt), sanitizeName(vt), "Video") + ".mp4"
		// The page itself is the referer of a post page.
		j.Referer = p.req.URL
		j.extra = nil
	case it.Media != "" && it.Stream != "":
		j.URL = it.Media
		j.Kind = "video"
		j.Stream = it.Stream
		vt := firstNonEmpty(title, label+" "+strconv.Itoa(n))
		j.Video = &VideoOptions{Quality: quality, Title: uniqueTitle(vt, it, names)}
		j.Name = firstNonEmpty(cleanTitle(vt), sanitizeName(vt), "Video") + ".mp4"
		j.refFrame, j.refPage = p.req.URL, p.req.URL
	case it.Media != "":
		j.URL = it.Media
		ext := it.Ext
		if ext == "" || len(ext) > 5 {
			if it.Kind == "photo" {
				ext = "jpg"
			} else {
				ext = "mp4"
			}
		}
		if ext == "gifv" {
			ext = "mp4"
		}
		j.Want = uniqueTitle(firstNonEmpty(title, label+" "+strconv.Itoa(n)), it, names) + "." + ext
		j.Name = j.Want
	default:
		return nil
	}
	return j
}

var postIDRe = regexp.MustCompile(`^[a-z]{1,3}:`)
var postKeyRe = regexp.MustCompile(`^[a-z]{1,3}:[\w-]{1,64}$`)

// uniqueTitle is a short, safe file name for an item: its title (or
// "Photo 3"), the post's id and the carousel place, never twice the same.
func uniqueTitle(title string, it MediaItem, names map[string]bool) string {
	t := firstNonEmpty(cleanTitle(title), sanitizeName(title), "Media")
	if r := []rune(t); len(r) > 80 {
		t = strings.TrimSpace(string(r[:80]))
	}
	if it.Post != "" {
		id := postIDRe.ReplaceAllString(it.Post, "")
		if it.Index > 0 {
			id += "_" + strconv.Itoa(it.Index)
		}
		t += " [" + sanitizeName(id) + "]"
	}
	name := t
	for i := 2; names[strings.ToLower(name)]; i++ {
		name = t + " (" + strconv.Itoa(i) + ")"
	}
	names[strings.ToLower(name)] = true
	return name
}
