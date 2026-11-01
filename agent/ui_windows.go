//go:build windows

package main

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mbInfo    = 0x40 // MB_ICONINFORMATION
	mbWarning = 0x30 // MB_ICONWARNING
	mbError   = 0x10 // MB_ICONERROR
	mbYesNo   = 0x04 // MB_YESNO

	idYes          = 6
	cfUnicodeText  = 13
	mbSetForegound = 0x10000
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procMessageBox       = user32.NewProc("MessageBoxW")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

// messageBox shows a standard Windows dialog. With mbYesNo it reports
// whether Yes was clicked; otherwise it returns true once dismissed.
func messageBox(title, text string, flags uintptr) bool {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	if flags&mbYesNo != 0 && flags&(mbWarning|mbError) == 0 {
		flags |= 0x20 // MB_ICONQUESTION
	}
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags|mbSetForegound)
	if flags&mbYesNo != 0 {
		return r == idYes
	}
	return true
}

// readClipboard returns the clipboard's text, or "" if there is none.
func readClipboard() string {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	// Converted through a pointer to p so go vet accepts the uintptr → pointer step.
	text := windows.UTF16PtrToString((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&p))))
	if len(text) > 4096 {
		return ""
	}
	return strings.TrimSpace(text)
}

func clearClipboard() {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return
	}
	procEmptyClipboard.Call()
	procCloseClipboard.Call()
}
