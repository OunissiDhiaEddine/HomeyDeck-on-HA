//go:build windows

package main

// A small dialog with one text box, for typing the pairing code when it
// can't be copied (for example when Home Assistant is open on a phone). It's
// built from an in-memory dialog template, so it needs no resource files.

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wsPopup         = 0x80000000
	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsCaption       = 0x00C00000
	wsBorder        = 0x00800000
	wsSysMenu       = 0x00080000
	wsTabStop       = 0x00010000
	dsModalFrame    = 0x80
	dsSetFont       = 0x40
	dsSetForeground = 0x200
	dsCenter        = 0x800
	esAutoHScroll   = 0x80
	bsDefPushButton = 0x1

	wmInitDialog = 0x0110
	wmCommand    = 0x0111
	emSetSel     = 0x00B1
	idOK         = 1
	idCancel     = 2
	idEdit       = 100

	classButton = 0x0080
	classEdit   = 0x0081
	classStatic = 0x0082
)

var (
	procDialogBoxIndirectParam = user32.NewProc("DialogBoxIndirectParamW")
	procEndDialog              = user32.NewProc("EndDialog")
	procSetDlgItemText         = user32.NewProc("SetDlgItemTextW")
	procGetDlgItemText         = user32.NewProc("GetDlgItemTextW")
	procSendDlgItemMessage     = user32.NewProc("SendDlgItemMessageW")
	procGetModuleHandle        = kernel32.NewProc("GetModuleHandleW")

	// One dialog at a time, so its state can live here; Windows callbacks
	// can't capture Go variables.
	dialogInitial string
	dialogResult  string
	dialogProc    = windows.NewCallback(inputDialogProc)
)

func inputDialogProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmInitDialog:
		text, _ := windows.UTF16PtrFromString(dialogInitial)
		procSetDlgItemText.Call(hwnd, idEdit, uintptr(unsafe.Pointer(text)))
		procSendDlgItemMessage.Call(hwnd, idEdit, emSetSel, 0, ^uintptr(0))
		return 1 // focus goes to the text box, the first control that takes it
	case wmCommand:
		switch wparam & 0xffff {
		case idOK:
			buf := make([]uint16, 512)
			procGetDlgItemText.Call(hwnd, idEdit, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			dialogResult = windows.UTF16ToString(buf)
			procEndDialog.Call(hwnd, 1)
			return 1
		case idCancel:
			procEndDialog.Call(hwnd, 0)
			return 1
		}
	}
	return 0
}

// inputBox shows prompt and a text box holding initial. It returns what was
// typed, and false if the dialog was cancelled.
func inputBox(title, prompt, initial string) (string, bool) {
	tpl := newDialogTemplate(dsSetFont|dsModalFrame|dsCenter|dsSetForeground|wsPopup|wsCaption|wsSysMenu, 0, 0, 280, 92, title)
	tpl.item(wsChild|wsVisible, 8, 8, 264, 40, 0xffff, classStatic, prompt)
	tpl.item(wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll, 8, 50, 264, 14, idEdit, classEdit, "")
	tpl.item(wsChild|wsVisible|wsTabStop|bsDefPushButton, 166, 72, 50, 14, idOK, classButton, "Pair")
	tpl.item(wsChild|wsVisible|wsTabStop, 222, 72, 50, 14, idCancel, classButton, "Cancel")

	dialogInitial, dialogResult = initial, ""
	mem := tpl.aligned()
	instance, _, _ := procGetModuleHandle.Call(0)
	r, _, _ := procDialogBoxIndirectParam.Call(instance, uintptr(unsafe.Pointer(&mem[0])), 0, dialogProc, 0)
	runtime.KeepAlive(mem)
	return dialogResult, r == 1
}

// dialogTemplate writes a DLGTEMPLATE followed by DLGITEMTEMPLATEs.
type dialogTemplate struct {
	buf   []byte
	count int
}

func newDialogTemplate(style uint32, x, y, cx, cy int16, title string) *dialogTemplate {
	t := &dialogTemplate{}
	t.u32(style)
	t.u32(0) // extended style
	t.u16(0) // item count, filled in by item()
	t.i16(x, y, cx, cy)
	t.u16(0) // no menu
	t.u16(0) // default dialog class
	t.str(title)
	t.u16(9) // font size, for DS_SETFONT
	t.str("Segoe UI")
	return t
}

func (t *dialogTemplate) item(style uint32, x, y, cx, cy int16, id uint16, class uint16, text string) {
	for len(t.buf)%4 != 0 {
		t.buf = append(t.buf, 0)
	}
	t.u32(style)
	t.u32(0) // extended style
	t.i16(x, y, cx, cy)
	t.u16(id)
	t.u16(0xffff) // a predefined class, by atom
	t.u16(class)
	t.str(text)
	t.u16(0) // no creation data
	t.count++
	binary.LittleEndian.PutUint16(t.buf[8:], uint16(t.count))
}

// aligned copies the template into DWORD-aligned memory, as Windows requires.
func (t *dialogTemplate) aligned() []uint32 {
	mem := make([]uint32, (len(t.buf)+3)/4)
	for i, b := range t.buf {
		mem[i/4] |= uint32(b) << (8 * (i % 4))
	}
	return mem
}

func (t *dialogTemplate) u16(v uint16) { t.buf = binary.LittleEndian.AppendUint16(t.buf, v) }
func (t *dialogTemplate) u32(v uint32) { t.buf = binary.LittleEndian.AppendUint32(t.buf, v) }

func (t *dialogTemplate) i16(vs ...int16) {
	for _, v := range vs {
		t.u16(uint16(v))
	}
}

func (t *dialogTemplate) str(s string) {
	u, _ := windows.UTF16FromString(s)
	for _, c := range u {
		t.u16(c)
	}
}
