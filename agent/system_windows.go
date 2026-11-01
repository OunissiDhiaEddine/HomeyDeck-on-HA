//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue         = "HomeyDeckHA"
	uninstallKeyPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\HomeyDeckHA`
	exeName          = "homeydeck-ha-agent.exe"
)

var (
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole  = kernel32.NewProc("AttachConsole")
	procGetTickCount64 = kernel32.NewProc("GetTickCount64")
	powrprof           = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspend     = powrprof.NewProc("SetSuspendState")
)

// dataDir is %LOCALAPPDATA%\HomeyDeckHA (config + log).
func dataDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "HomeyDeckHA")
}

var consoleAttached bool

// The agent is built as a GUI program so no console window ever appears.
// For the install/status commands we borrow the console of the terminal that
// launched us, if there is one.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		os.Stdout, os.Stderr = nil, nil // nowhere to print; dialogs are used instead
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = out, out
		consoleAttached = true
	}
	fmt.Println()
}

func hasConsole() bool { return consoleAttached }

func singleInstance() (func(), error) {
	name, _ := windows.UTF16PtrFromString(`Local\HomeyDeckHAAgent`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return nil, errors.New("another copy is already running")
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

func uptime() time.Duration {
	ms, _, _ := procGetTickCount64.Call()
	return time.Duration(ms) * time.Millisecond
}

// --- Steam -------------------------------------------------------------------

func steamRunning() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "steam.exe") {
			return true
		}
	}
	return false
}

func launchSteam() error {
	// Ask for the normal library window. If Steam is set to start in Big
	// Picture, close Big Picture once Steam is up so the desktop client stays.
	if exe := steamExePath(); exe != "" {
		cmd := exec.Command(exe, "steam://open/main")
		cmd.Dir = filepath.Dir(exe)
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			go leaveBigPicture(exe)
			return nil
		}
	}
	// Fall back to the steam:// protocol handler.
	if err := openSteamURL("steam://open/main"); err != nil {
		return fmt.Errorf("could not start Steam: %w", err)
	}
	go leaveBigPicture("")
	return nil
}

// leaveBigPicture closes Big Picture once, if Steam opens in it, so the
// desktop client stays. It only touches Steam when the Big Picture window is
// actually showing: running steam.exe again would bring the library window
// back even after the user closed it.
func leaveBigPicture(exe string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if !bigPictureShowing() {
			continue
		}
		if exe != "" {
			cmd := exec.Command(exe, "steam://close/bigpicture")
			cmd.Dir = filepath.Dir(exe)
			if cmd.Start() == nil {
				_ = cmd.Process.Release()
				return
			}
		}
		_ = openSteamURL("steam://close/bigpicture")
		return
	}
}

var procFindWindow = user32.NewProc("FindWindowW")

func bigPictureShowing() bool {
	title, _ := windows.UTF16PtrFromString("Steam Big Picture Mode")
	hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(title)))
	return hwnd != 0
}

func openSteamURL(u string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func steamExePath() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	exe, _, err := k.GetStringValue("SteamExe")
	if err != nil {
		return ""
	}
	exe = filepath.FromSlash(exe)
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	return exe
}

// --- power -------------------------------------------------------------------

func canSleep() error {
	if err := procSetSuspend.Find(); err != nil {
		return errors.New("sleep is not available on this PC")
	}
	return nil
}

func sleepNow() error {
	// SetSuspendState(hibernate=FALSE, force=FALSE, disableWakeEvent=FALSE).
	// Wake events must stay enabled or Wake-on-LAN would not work.
	r, _, err := procSetSuspend.Call(0, 0, 0)
	if r == 0 {
		return err
	}
	return nil
}

func reboot() error {
	cmd := exec.Command("shutdown.exe", "/r", "/t", "5", "/c", "Restart requested from Home Assistant")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("shutdown.exe: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- install -----------------------------------------------------------------

// installBinary copies the running exe to %LOCALAPPDATA%\HomeyDeckHA so the
// download can be deleted. Re-running install from the installed copy is fine.
func installBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dataDir(), exeName)
	if strings.EqualFold(filepath.Clean(self), filepath.Clean(dest)) {
		return dest, nil
	}

	stopOtherInstances() // an older copy may be holding the file open
	time.Sleep(500 * time.Millisecond)

	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp := dest + ".new"
	dst, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(tmp, dest)
}

// runningInstalledCopy reports whether this is the copy in %LOCALAPPDATA%\HomeyDeckHA.
func runningInstalledCopy() bool {
	self, err := os.Executable()
	return err == nil && strings.EqualFold(filepath.Clean(self), filepath.Join(dataDir(), exeName))
}

func enableAutostart(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runValue, `"`+exe+`" run`)
}

func disableAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// registerUninstaller lists the agent in Windows Settings → Apps, whose
// Uninstall button runs `homeydeck-ha-agent.exe uninstall`.
func registerUninstaller(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	size := uint32(0)
	if info, err := os.Stat(exe); err == nil {
		size = uint32(info.Size() / 1024)
	}
	for name, value := range map[string]string{
		"DisplayName":     "HomeyDeck HA agent",
		"DisplayVersion":  version,
		"Publisher":       "HomeyDeck",
		"DisplayIcon":     exe,
		"InstallLocation": filepath.Dir(exe),
		"UninstallString": `"` + exe + `" uninstall`,
	} {
		if err := k.SetStringValue(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": size} {
		if err := k.SetDWordValue(name, value); err != nil {
			return err
		}
	}
	return nil
}

func unregisterUninstaller() error {
	err := registry.DeleteKey(registry.CURRENT_USER, uninstallKeyPath)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// removeDataDir deletes %LOCALAPPDATA%\HomeyDeckHA. When the running program
// is the installed copy, Windows won't delete it while it runs, so a hidden
// cmd.exe finishes the job a couple of seconds after we exit.
func removeDataDir() error {
	dir := dataDir()
	self, _ := os.Executable()
	if !strings.HasPrefix(strings.ToLower(filepath.Clean(self)), strings.ToLower(dir+string(filepath.Separator))) {
		var err error
		for range 5 { // the stopped agent may hold its files for a moment
			if err = os.RemoveAll(dir); err == nil {
				return nil
			}
			time.Sleep(400 * time.Millisecond)
		}
		return err
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS,
		CmdLine:       `cmd.exe /d /c ping -n 3 127.0.0.1 >nul & rmdir /s /q "` + dir + `"`,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func startDetached(exe string) error {
	cmd := exec.Command(exe, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func stopOtherInstances() {
	cmd := exec.Command("taskkill.exe", "/F", "/IM", exeName, "/FI", "PID ne "+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	_ = cmd.Run()
}
