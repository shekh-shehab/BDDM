package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func linuxNICCounters(routePath, devPath string) (in, out uint64, ok bool) {
	name, found := defaultRouteIface(routePath)
	dev, err := os.Open(devPath)
	if err != nil {
		return 0, 0, false
	}
	defer dev.Close()
	sc := bufio.NewScanner(dev)
	var totalIn, totalOut uint64
	var got bool
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		iface, rin, rout, rok := parseNetDevLine(line)
		if !rok || iface == "lo" {
			continue
		}
		if found && iface != name {
			continue
		}
		totalIn += rin
		totalOut += rout
		got = true
		if found {
			break
		}
	}
	return totalIn, totalOut, got
}

func defaultRouteIface(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		// skip header
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" && fields[0] != "lo" {
			return fields[0], true
		}
	}
	return "", false
}

func parseNetDevLine(line string) (name string, in, out uint64, ok bool) {
	i := strings.IndexByte(line, ':')
	if i <= 0 {
		return "", 0, 0, false
	}
	name = strings.TrimSpace(line[:i])
	fields := strings.Fields(line[i+1:])
	if len(fields) < 9 {
		return "", 0, 0, false
	}
	in, err1 := strconv.ParseUint(fields[0], 10, 64)
	out, err2 := strconv.ParseUint(fields[8], 10, 64)
	if err1 != nil || err2 != nil || name == "" {
		return "", 0, 0, false
	}
	return name, in, out, true
}
