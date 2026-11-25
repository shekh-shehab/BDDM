//go:build windows

package main

import (
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ITaskbarList3 progress overlay on the Edge/Chrome app HWND (item 30).

const (
	tbpNoProgress    = 0
	tbpIndeterminate = 0x1
	tbpNormal        = 0x2
	tbpError         = 0x4
	tbpPaused        = 0x8

	// ITaskbarList3 vtable: IUnknown (0-2), ITaskbarList (3-7),
	// ITaskbarList2 (8), SetProgressValue (9), SetProgressState (10).
	idxHrInit           = 3
	idxSetProgressValue = 9
	idxSetProgressState = 10
)

var (
	clsidTaskbarList = syscall.GUID{0x56FDF344, 0xFD6D, 0x11D0, [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	iidITaskbarList3 = syscall.GUID{0xEA1AFB91, 0x9E28, 0x4B86, [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}

	taskbarMu     sync.Mutex
	taskbarStop   chan struct{}
	taskbarObjPtr uintptr
)

func startTaskbar(m *Manager) {
	taskbarMu.Lock()
	if taskbarStop != nil {
		taskbarMu.Unlock()
		return
	}
	taskbarStop = make(chan struct{})
	stop := taskbarStop
	taskbarMu.Unlock()
	go taskbarLoop(m, stop)
}

func stopTaskbar() {
	taskbarMu.Lock()
	stop := taskbarStop
	taskbarStop = nil
	taskbarMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func mainWindowHWND() uintptr {
	for _, w := range listBDDMAppWindows() {
		if w.title == "BDDM tray" || strings.HasPrefix(w.title, "BDDM tray") {
			continue
		}
		if w.title == "BDDM - Download Manager" || w.title == "BDDM" || strings.HasPrefix(w.title, "BDDM - ") {
			return w.hwnd
		}
	}
	return 0
}

func taskbarLoop(m *Manager, stop <-chan struct{}) {
	uninit := coInit()
	defer uninit()
	obj := createTaskbarList()
	if obj == 0 {
		log.Print("taskbar progress: could not create ITaskbarList3")
		return
	}
	taskbarMu.Lock()
	taskbarObjPtr = obj
	taskbarMu.Unlock()
	defer func() {
		applyTaskbar(obj, 0, taskbarProgress{State: taskbarNone})
		comRelease(obj)
		taskbarMu.Lock()
		taskbarObjPtr = 0
		taskbarMu.Unlock()
	}()

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var lastHWND uintptr
	for {
		select {
		case <-stop:
			if lastHWND != 0 {
				applyTaskbar(obj, lastHWND, taskbarProgress{State: taskbarNone})
			}
			return
		case <-tick.C:
			hwnd := mainWindowHWND()
			if lastHWND != 0 && lastHWND != hwnd {
				applyTaskbar(obj, lastHWND, taskbarProgress{State: taskbarNone})
			}
			lastHWND = hwnd
			if hwnd == 0 {
				continue
			}
			applyTaskbar(obj, hwnd, taskbarFromJobs(m.List()))
		}
	}
}

func createTaskbarList() uintptr {
	var obj uintptr
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTaskbarList)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidITaskbarList3)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if failed(r) || obj == 0 {
		return 0
	}
	if failed(comCall(obj, idxHrInit)) {
		comRelease(obj)
		return 0
	}
	return obj
}

func packUint64(v uint64) []uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return []uintptr{uintptr(v)}
	}
	return []uintptr{uintptr(uint32(v)), uintptr(v >> 32)}
}

func applyTaskbar(obj, hwnd uintptr, p taskbarProgress) {
	if obj == 0 || hwnd == 0 {
		return
	}
	var flag uintptr
	switch p.State {
	case taskbarNormal:
		flag = tbpNormal
	case taskbarPaused:
		flag = tbpPaused
	case taskbarError:
		flag = tbpError
	case taskbarIndeterminate:
		flag = tbpIndeterminate
	default:
		flag = tbpNoProgress
	}
	comCall(obj, idxSetProgressState, hwnd, flag)
	if flag == tbpNormal || flag == tbpPaused || flag == tbpError {
		total := p.Total
		if total == 0 {
			total = 1
		}
		args := append([]uintptr{hwnd}, packUint64(p.Done)...)
		args = append(args, packUint64(total)...)
		comCall(obj, idxSetProgressValue, args...)
	}
}
