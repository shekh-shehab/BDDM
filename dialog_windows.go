//go:build windows

package main

// dialog_windows.go: native folder and file pickers that are owned by the
// BDDM app window, so they appear in front of it (not behind it).

import (
	"syscall"
	"unsafe"
)

var (
	ole32   = syscall.NewLazyDLL("ole32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32.NewProc("CoUninitialize")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
)

const (
	coinitApartmentthreaded = 0x2
	clsctxInprocServer      = 0x1
	rpcEChangedMode         = 0x80010106

	fosPickFolders     = 0x20
	fosForceFilesystem = 0x40
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	fosNoChangeDir     = 0x8
	fosDontAddToRecent = 0x2000000

	sigdnFilesyspath = 0x80058000

	hresultCancelled = 0x800704C7
	sOK              = 0
)

// COM identifiers (IFileOpenDialog / IShellItem).
var (
	clsidFileOpenDialog = syscall.GUID{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = syscall.GUID{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xE3, 0xCB}}
	iidIShellItem       = syscall.GUID{0x43826D1E, 0xE718, 0x42EE, [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

type comDlgFilterSpec struct {
	name *uint16
	spec *uint16
}

func coInit() (uninit func()) {
	r, _, _ := procCoInitializeEx.Call(0, coinitApartmentthreaded)
	if r == sOK {
		return func() { procCoUninitialize.Call() }
	}
	return func() {}
}

func comVtbl(obj uintptr) *[32]uintptr {
	return *(**[32]uintptr)(unsafe.Pointer(obj))
}

func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	if obj == 0 {
		return 0x80004003 // E_POINTER
	}
	all := make([]uintptr, 0, 1+len(args))
	all = append(all, obj)
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(comVtbl(obj)[idx], all...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

func hresult(r uintptr) uint32 { return uint32(r) }

func failed(r uintptr) bool { return uint32(r)&0x80000000 != 0 }

func shCreateItem(path string) uintptr {
	if path == "" {
		return 0
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var item uintptr
	r, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&iidIShellItem)),
		uintptr(unsafe.Pointer(&item)),
	)
	if failed(r) {
		return 0
	}
	return item
}

func shellItemPath(item uintptr) string {
	if item == 0 {
		return ""
	}
	var name uintptr
	if failed(comCall(item, 5, sigdnFilesyspath, uintptr(unsafe.Pointer(&name)))) || name == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(name)
	return utf16PtrToString((*uint16)(unsafe.Pointer(name)))
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

func newFileOpenDialog() (uintptr, error) {
	var dlg uintptr
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if failed(r) || dlg == 0 {
		return 0, syscall.Errno(r)
	}
	return dlg, nil
}

func showFileOpenDialog(owner uintptr, title, start string, folders bool, filterName, filterSpec string) (string, error) {
	uninit := coInit()
	defer uninit()

	dlg, err := newFileOpenDialog()
	if err != nil {
		return "", err
	}
	defer comRelease(dlg)

	opts := uint32(fosForceFilesystem | fosPathMustExist | fosDontAddToRecent | fosNoChangeDir)
	if folders {
		opts |= fosPickFolders
	} else {
		opts |= fosFileMustExist
	}
	// IFileDialog::SetOptions is vtable index 9
	if failed(comCall(dlg, 9, uintptr(opts))) {
		// still try to show
	}
	if title != "" {
		if t, err := syscall.UTF16PtrFromString(title); err == nil {
			comCall(dlg, 17, uintptr(unsafe.Pointer(t))) // SetTitle
		}
	}
	if !folders && filterSpec != "" {
		name := filterName
		if name == "" {
			name = "Files"
		}
		n1, _ := syscall.UTF16PtrFromString(name)
		s1, _ := syscall.UTF16PtrFromString(filterSpec)
		n2, _ := syscall.UTF16PtrFromString("All files")
		s2, _ := syscall.UTF16PtrFromString("*.*")
		filters := []comDlgFilterSpec{{n1, s1}, {n2, s2}}
		comCall(dlg, 4, 2, uintptr(unsafe.Pointer(&filters[0]))) // SetFileTypes
		comCall(dlg, 5, 1)                                       // SetFileTypeIndex (1-based)
	}
	if item := shCreateItem(start); item != 0 {
		comCall(dlg, 12, item) // SetFolder
		comRelease(item)
	}

	var path string
	var showErr error
	withForeground(func() {
		if owner != 0 {
			activateWindow(owner)
		}
		r := comCall(dlg, 3, owner) // IModalWindow::Show
		if hresult(r) == hresultCancelled || hresult(r) == 0x800704C7 {
			return
		}
		if failed(r) {
			showErr = syscall.Errno(r)
			return
		}
		var item uintptr
		if failed(comCall(dlg, 20, uintptr(unsafe.Pointer(&item)))) || item == 0 { // GetResult
			return
		}
		path = shellItemPath(item)
		comRelease(item)
	})
	return path, showErr
}

// pickFolderNative shows the modern Windows folder dialog owned by the BDDM window.
func pickFolderNative(owner uintptr, start string) (string, error) {
	return showFileOpenDialog(owner, "Choose the folder for downloads", start, true, "", "")
}

// pickTorrentFileNative shows the Open dialog for a .torrent file, owned by the BDDM window.
func pickTorrentFileNative(owner uintptr, start string) (string, error) {
	return showFileOpenDialog(owner, "Add torrent", start, false, "Torrent files", "*.torrent")
}
