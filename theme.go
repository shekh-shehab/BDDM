package main

import (
	"strings"
	"sync/atomic"
)

// Theme choices for every BDDM window (item 78).
const (
	themeSystem = "system" // follow Windows (default)
	themeLight  = "light"
	themeDark   = "dark"
)

// currentTheme is the saved theme, so window code (which has no Manager)
// can read it.
var currentTheme atomic.Value

func normalizeTheme(t string) string {
	switch t = strings.ToLower(strings.TrimSpace(t)); t {
	case themeLight, themeDark:
		return t
	}
	return themeSystem
}

func uiTheme() string {
	if v, ok := currentTheme.Load().(string); ok {
		return normalizeTheme(v)
	}
	return themeSystem
}

// themeIsDark is whether a window should be dark for theme t, given
// whether Windows apps are dark.
func themeIsDark(t string, windowsDark bool) bool {
	switch normalizeTheme(t) {
	case themeDark:
		return true
	case themeLight:
		return false
	}
	return windowsDark
}
