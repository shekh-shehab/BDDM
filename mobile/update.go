package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const githubReleasesURL = "https://api.github.com/repos/shekh-shehab/BDDM/releases?per_page=20"

// UpdateInfo is the JSON returned by CheckUpdate.
type UpdateInfo struct {
	OK      bool   `json:"ok"`
	Newer   bool   `json:"newer"`
	Version string `json:"version,omitempty"`
	Tag     string `json:"tag,omitempty"`
	URL     string `json:"url,omitempty"`
	APK     string `json:"apk,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CheckUpdate looks through GitHub releases for the newest Android APK
// (tag android-v*). It does not use /releases/latest, which is reserved
// for the PC app.
func (e *Engine) CheckUpdate(current string) string {
	info := e.checkUpdate(strings.TrimSpace(current))
	b, _ := json.Marshal(info)
	return string(b)
}

func (e *Engine) checkUpdate(current string) UpdateInfo {
	if current == "" {
		current = AppVersion
	}
	req, err := http.NewRequest(http.MethodGet, githubReleasesURL, nil)
	if err != nil {
		return UpdateInfo{Error: err.Error()}
	}
	req.Header.Set("User-Agent", "BDDM-Android/"+AppVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return UpdateInfo{Error: "could not check for updates"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{Error: fmt.Sprintf("GitHub returned HTTP %d", resp.StatusCode)}
	}
	var rels []ghRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return UpdateInfo{Error: "could not read the update list"}
	}
	var best *UpdateInfo
	for i := range rels {
		u := parseAndroidRelease(&rels[i])
		if u == nil {
			continue
		}
		if best == nil || compareVersions(u.Version, best.Version) > 0 {
			best = u
		}
	}
	if best == nil {
		return UpdateInfo{OK: true, Newer: false, Version: current}
	}
	best.OK = true
	best.Newer = compareVersions(best.Version, current) > 0
	return *best
}

type ghRelease struct {
	Tag    string    `json:"tag_name"`
	HTML   string    `json:"html_url"`
	Draft  bool      `json:"draft"`
	Assets []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func parseAndroidRelease(r *ghRelease) *UpdateInfo {
	if r == nil || r.Draft {
		return nil
	}
	tag := strings.TrimSpace(r.Tag)
	ver := ""
	switch {
	case strings.HasPrefix(strings.ToLower(tag), "android-v"):
		ver = strings.TrimPrefix(strings.TrimPrefix(tag, "android-"), "v")
		ver = strings.TrimPrefix(ver, "V")
	case strings.HasPrefix(strings.ToLower(tag), "android-"):
		ver = tag[len("android-"):]
		ver = strings.TrimPrefix(ver, "v")
	default:
		return nil
	}
	out := &UpdateInfo{Version: ver, Tag: tag, URL: r.HTML}
	for _, a := range r.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".apk") && strings.Contains(n, "android") {
			out.APK = a.URL
		}
		if strings.HasSuffix(n, ".sha256") && strings.Contains(n, "android") {
			out.SHA256 = a.URL
		}
	}
	if out.APK == "" {
		return nil
	}
	return out
}

// compareVersions returns 1 if a > b, -1 if a < b, 0 if equal.
// Only numeric dotted parts are compared ("1.6.0").
func compareVersions(a, b string) int {
	as, bs := verParts(a), verParts(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	return 0
}

func verParts(s string) []int {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(s), "v"))
	var out []int
	for _, p := range strings.Split(s, ".") {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}
