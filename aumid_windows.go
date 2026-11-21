//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procSHGetPropertyStoreForWindow             = shell32.NewProc("SHGetPropertyStoreForWindow")
	procSHChangeNotify                          = shell32.NewProc("SHChangeNotify")
	procPropVariantClear                        = ole32.NewProc("PropVariantClear")
)

const (
	stgmReadWrite   = 0x00000002
	shcneUpdateItem = 0x00002000
	shcnfPathW      = 0x0005
	vtLPWSTR        = 31
	maxShortcutPath = 32768
)

var (
	clsidShellLink    = syscall.GUID{0x00021401, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIShellLinkW    = syscall.GUID{0x000214F9, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIPersistFile   = syscall.GUID{0x0000010B, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIPropertyStore = syscall.GUID{0x886D8EEB, 0x8CF2, 0x4446, [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}

	pkeyFmtAppUserModel      = syscall.GUID{0x9F4C2855, 0x9F79, 0x4B39, [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
	pkeyAppUserModelID       = propertykey{pkeyFmtAppUserModel, 5}
	pkeyRelaunchCommand      = propertykey{pkeyFmtAppUserModel, 2}
	pkeyRelaunchIconResource = propertykey{pkeyFmtAppUserModel, 3}
	pkeyRelaunchDisplayName  = propertykey{pkeyFmtAppUserModel, 4}
)

type propertykey struct {
	fmtid syscall.GUID
	pid   uint32
}

// propvariant is the 24-byte x64 / 16-byte x86 PROPVARIANT layout we
// need for VT_LPWSTR values.
type propvariant struct {
	vt         uint16
	r1, r2, r3 uint16
	val        uintptr
	pad        uint64
}

func setCurrentProcessAppUserModelID() {
	p, err := syscall.UTF16PtrFromString(appUserModelID)
	if err != nil {
		return
	}
	r, _, _ := procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(p)))
	if failed(r) {
		log.Printf("SetCurrentProcessExplicitAppUserModelID: 0x%X", uint32(r))
	}
}

func registerAppIdentity() error {
	setCurrentProcessAppUserModelID()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("app identity: %w", err)
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	exeDir := filepath.Dir(exe)

	configDir := filepath.Join(".", "BDDM-data")
	if d, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(d, "BDDM")
	}
	_ = os.MkdirAll(configDir, 0755)

	png := ensureToastPNG(exeDir, configDir)
	_, ico := installIconPaths(exeDir)
	iconURI := fileURI(png)
	if iconURI == "" {
		iconURI = fileURI(ico)
	}
	if err := writeAppUserModelRegistry(appUserModelID, appDisplayName, iconURI); err != nil {
		log.Printf("app identity registry: %v", err)
	}

	home, _ := os.UserHomeDir()
	var first error
	for _, lnk := range shortcutCandidatePaths(home, os.Getenv("APPDATA")) {
		if !fileExists(lnk) {
			continue
		}
		if err := stampShortcutAppUserModelID(lnk, exe); err != nil {
			log.Printf("app identity shortcut %s: %v", lnk, err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func ensureToastPNG(exeDir, configDir string) string {
	png, _ := installIconPaths(exeDir)
	if png != "" {
		return png
	}
	if configDir == "" {
		return ""
	}
	dest := filepath.Join(configDir, "toast-icon.png")
	if st, err := os.Stat(dest); err == nil && !st.IsDir() && st.Size() > 0 {
		return dest
	}
	data, err := uiFiles.ReadFile("ui/icon.png")
	if err != nil {
		return ""
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return ""
	}
	return dest
}

func writeAppUserModelRegistry(id, name, iconURI string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+id, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("DisplayName", name); err != nil {
		return err
	}
	if iconURI != "" {
		if err := key.SetStringValue("IconUri", iconURI); err != nil {
			return err
		}
	}
	settings, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Notifications\Settings\`+id, registry.SET_VALUE)
	if err != nil {
		return nil // display name is enough; this key is optional
	}
	defer settings.Close()
	_ = settings.SetDWordValue("Enabled", 1)
	_ = settings.SetDWordValue("ShowInActionCenter", 1)
	return nil
}

func stampShortcutAppUserModelID(lnk, ourExe string) error {
	uninit := coInit()
	defer uninit()

	var sln uintptr
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)),
		uintptr(unsafe.Pointer(&sln)),
	)
	if failed(r) || sln == 0 {
		return fmt.Errorf("ShellLink: 0x%X", uint32(r))
	}
	defer comRelease(sln)

	var persist uintptr
	if failed(comCall(sln, 0, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&persist)))) || persist == 0 {
		return fmt.Errorf("IPersistFile")
	}
	defer comRelease(persist)

	wp, err := syscall.UTF16PtrFromString(lnk)
	if err != nil {
		return err
	}
	if failed(comCall(persist, 5, uintptr(unsafe.Pointer(wp)), stgmReadWrite)) { // Load
		return fmt.Errorf("load shortcut")
	}

	target := shortcutTarget(sln)
	if !shouldStampShortcut(target, ourExe) {
		return nil
	}

	var store uintptr
	if failed(comCall(sln, 0, uintptr(unsafe.Pointer(&iidIPropertyStore)), uintptr(unsafe.Pointer(&store)))) || store == 0 {
		return fmt.Errorf("IPropertyStore")
	}
	defer comRelease(store)

	if cur, ok := propertyStoreGetString(store, pkeyAppUserModelID); ok && cur == appUserModelID {
		return nil
	}
	if err := propertyStoreSetString(store, pkeyAppUserModelID, appUserModelID); err != nil {
		return err
	}
	if failed(comCall(store, 7)) { // Commit
		return fmt.Errorf("commit shortcut properties")
	}
	// IPersistFile::Save(NULL, TRUE) writes back to the loaded path.
	if failed(comCall(persist, 6, 0, 1)) {
		return fmt.Errorf("save shortcut")
	}
	procSHChangeNotify.Call(shcneUpdateItem, shcnfPathW, uintptr(unsafe.Pointer(wp)), 0)
	return nil
}

func shortcutTarget(sln uintptr) string {
	buf := make([]uint16, maxShortcutPath)
	// IShellLinkW::GetPath, SLGP_RAWPATH = 4
	if failed(comCall(sln, 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 4)) {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func propertyStoreSetString(store uintptr, key propertykey, s string) error {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	pv := propvariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(p))}
	r := comCall(store, 6, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))
	runtime.KeepAlive(p)
	if failed(r) {
		return fmt.Errorf("IPropertyStore.SetValue: 0x%X", uint32(r))
	}
	return nil
}

func propertyStoreGetString(store uintptr, key propertykey) (string, bool) {
	var pv propvariant
	r := comCall(store, 5, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))
	if failed(r) || pv.vt != vtLPWSTR || pv.val == 0 {
		propVariantClear(&pv)
		return "", false
	}
	s := utf16PtrToString((*uint16)(unsafe.Pointer(pv.val)))
	propVariantClear(&pv)
	return s, true
}

func propVariantClear(pv *propvariant) {
	if pv != nil {
		procPropVariantClear.Call(uintptr(unsafe.Pointer(pv)))
	}
}

func setWindowAppIdentity(hwnd uintptr) error {
	if hwnd == 0 {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	_, ico := installIconPaths(filepath.Dir(exe))
	cmd := relaunchCommand(exe)
	icon := relaunchIconResource(exe, ico)

	uninit := coInit()
	defer uninit()

	var store uintptr
	r, _, _ := procSHGetPropertyStoreForWindow.Call(
		hwnd,
		uintptr(unsafe.Pointer(&iidIPropertyStore)),
		uintptr(unsafe.Pointer(&store)),
	)
	if failed(r) || store == 0 {
		return fmt.Errorf("SHGetPropertyStoreForWindow: 0x%X", uint32(r))
	}
	defer comRelease(store)

	if err := propertyStoreSetString(store, pkeyAppUserModelID, appUserModelID); err != nil {
		return err
	}
	if cmd != "" {
		_ = propertyStoreSetString(store, pkeyRelaunchCommand, cmd)
	}
	if icon != "" {
		_ = propertyStoreSetString(store, pkeyRelaunchIconResource, icon)
	}
	_ = propertyStoreSetString(store, pkeyRelaunchDisplayName, appDisplayName)
	if failed(comCall(store, 7)) {
		return fmt.Errorf("commit window properties")
	}
	return nil
}

func tagBDDMAppWindows() {
	for _, w := range listBDDMAppWindows() {
		if err := setWindowAppIdentity(w.hwnd); err != nil {
			log.Printf("app identity window: %v", err)
		}
	}
}

var (
	tagWinMu    sync.Mutex
	tagWinSched bool
)

func scheduleTagBDDMAppWindows() {
	tagBDDMAppWindows()
	tagWinMu.Lock()
	if tagWinSched {
		tagWinMu.Unlock()
		return
	}
	tagWinSched = true
	tagWinMu.Unlock()
	go func() {
		defer func() {
			tagWinMu.Lock()
			tagWinSched = false
			tagWinMu.Unlock()
		}()
		for _, d := range []time.Duration{400 * time.Millisecond, 2 * time.Second} {
			time.Sleep(d)
			tagBDDMAppWindows()
		}
	}()
}

func toastIconPaths() (logoURI, icoPath string) {
	exe, err := os.Executable()
	dir := ""
	if err == nil {
		if abs, err := filepath.Abs(exe); err == nil {
			exe = abs
		}
		dir = filepath.Dir(exe)
	}
	configDir := filepath.Join(".", "BDDM-data")
	if d, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(d, "BDDM")
	}
	png := ensureToastPNG(dir, configDir)
	_, ico := installIconPaths(dir)
	return fileURI(png), ico
}
