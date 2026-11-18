package main

// referer.go: which Referer a video stream wants (1.4.2).
//
// Streaming sites often play their video in a player frame from another
// site (anikoto.tv -> megaplay.buzz), and their video server checks the
// Referer: the browser sends only "https://megaplay.buzz/" (its default
// rule for another site), and the server refuses anything else with 403.
// BDDM sends the Referer the browser really sent (headers.go). When the
// site still answers 403, BDDM tries, in this order, the player's site only,
// the player's full address and the tab's page, and remembers for that
// video server which one worked (referers.json in BDDM's data folder).

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Kinds of Referer (what is remembered per video server).
const (
	refOrigin = "origin" // "https://player-site/"
	refFrame  = "frame"  // the player frame's full address
	refPage   = "page"   // the tab's page
)

type refChoice struct {
	Kind, Value string
}

// refererChoices: the Referers worth trying for a stream, in order: the
// player's site only, the player's full address, the tab's page.
func refererChoices(frame, page string) []refChoice {
	var out []refChoice
	seen := map[string]bool{}
	add := func(kind, v string) {
		v = strings.TrimSpace(v)
		if !isWebLink(v) || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, refChoice{kind, v})
	}
	if o := originOf(frame); o != "" {
		add(refOrigin, o+"/")
	}
	add(refFrame, withoutFragment(frame))
	add(refPage, withoutFragment(page))
	return out
}

func withoutFragment(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// refererAlternatives: the choices other than the current Referer.
func refererAlternatives(current, target, frame, page string) []string {
	var out []string
	for _, c := range refererChoices(frame, page) {
		if c.Value != current {
			out = append(out, c.Value)
		}
	}
	return out
}

// refererKind tells which kind of choice ref is for this frame and page.
func refererKind(ref, frame, page string) string {
	for _, c := range refererChoices(frame, page) {
		if c.Value == ref {
			return c.Kind
		}
	}
	return ""
}

// ---------------------------------------------------------------- memory

type refMemory struct {
	mu     sync.Mutex
	path   string
	loaded bool
	picks  map[string]string // video server host -> kind
}

var refMem = &refMemory{}

const maxRefPicks = 500

func (r *refMemory) loadLocked() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.picks = map[string]string{}
	if r.path == "" {
		return
	}
	if data, err := os.ReadFile(r.path); err == nil {
		_ = json.Unmarshal(data, &r.picks)
	}
}

// setDir: where referers.json lives (BDDM's data folder).
func (r *refMemory) setDir(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.path = filepath.Join(dir, "referers.json")
	r.loaded = false
}

func (r *refMemory) get(host string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	return r.picks[strings.ToLower(host)]
}

func (r *refMemory) remember(host, kind string) {
	host = strings.ToLower(host)
	if host == "" || kind == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if r.picks[host] == kind {
		return
	}
	if len(r.picks) >= maxRefPicks {
		r.picks = map[string]string{} // rarely reached; start over
	}
	r.picks[host] = kind
	if r.path == "" {
		return
	}
	if data, err := json.MarshalIndent(r.picks, "", "  "); err == nil {
		tmp := r.path + ".tmp"
		if os.WriteFile(tmp, data, 0644) == nil {
			_ = os.Rename(tmp, r.path)
		}
	}
}

func hostOfLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// useRememberedReferer: when the browser's own Referer is not known, start
// with the kind that worked for this video server before.
func (j *Job) useRememberedReferer(frame, page string) {
	kind := refMem.get(hostOfLink(j.URL))
	if kind == "" {
		return
	}
	for _, c := range refererChoices(frame, page) {
		if c.Kind == kind && c.Value != j.Referer {
			old := j.Referer
			j.Referer = c.Value
			alts := []string{}
			if old != "" {
				alts = append(alts, old)
			}
			for _, a := range j.RefAlts {
				if a != c.Value && a != old {
					alts = append(alts, a)
				}
			}
			j.RefAlts = alts
			return
		}
	}
}

var refusedRe = regexp.MustCompile(`(?i)http error (401|403)|\b403 forbidden|\(http 40[13]`)

// isRefused: the site answered 401/403.
func isRefused(msg string) bool {
	return refusedRe.MatchString(msg) || msg == msgStreamRefused
}
