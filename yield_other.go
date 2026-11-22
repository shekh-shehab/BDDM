//go:build !windows

package main

func systemNICCounters() (in, out uint64, ok bool) {
	return linuxNICCounters("/proc/net/route", "/proc/net/dev")
}
