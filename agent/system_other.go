//go:build !windows

// Stand-ins so the agent can be run and tested on macOS/Linux during
// development. Power actions are only logged, never performed.
package main

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

var started = time.Now()

func dataDir() string {
	if d := os.Getenv("HOMEYDECK_DATA_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "HomeyDeckHA")
}

func attachConsole()                   {}
func hasConsole() bool                 { return true }
func singleInstance() (func(), error)  { return func() {}, nil }
func uptime() time.Duration            { return time.Since(started) }
func steamRunning() bool               { return os.Getenv("HOMEYDECK_FAKE_STEAM") == "1" }
func launchSteam() error               { log.Printf("[dev] would launch Steam"); return nil }
func sunshineRunning() bool            { return os.Getenv("HOMEYDECK_FAKE_SUNSHINE") == "1" }
func launchSunshine() error            { log.Printf("[dev] would launch Sunshine"); return nil }
func canSleep() error                  { return nil }
func sleepNow() error                  { log.Printf("[dev] would sleep"); return nil }
func reboot() error                    { log.Printf("[dev] would reboot"); return nil }
func installBinary() (string, error)   { return os.Executable() }
func runningInstalledCopy() bool       { return false }
func enableAutostart(string) error     { return nil }
func disableAutostart() error          { return nil }
func autostartEnabled() bool           { return false }
func startDetached(string) error       { return nil }
func stopOtherInstances()              {}
func registerUninstaller(string) error { return nil }
func unregisterUninstaller() error     { return nil }
func removeDataDir() error             { return os.RemoveAll(dataDir()) }

const (
	mbInfo    = 0x40
	mbWarning = 0x30
	mbError   = 0x10
	mbYesNo   = 0x04
)

// On other systems dialogs go to the terminal and Yes is assumed, and the
// clipboard is faked with HOMEYDECK_FAKE_CLIPBOARD, for development.
func messageBox(title, text string, flags uintptr) bool {
	log.Printf("[dialog] %s: %s", title, text)
	return true
}
func readClipboard() string { return os.Getenv("HOMEYDECK_FAKE_CLIPBOARD") }
func clearClipboard()       {}

// Stand-ins for DPAPI and the input dialog during development.
func protect(data []byte) ([]byte, error)   { return data, nil }
func unprotect(data []byte) ([]byte, error) { return data, nil }
func inputBox(title, prompt, initial string) (string, bool) {
	log.Printf("[input] %s: %s", title, prompt)
	v := os.Getenv("HOMEYDECK_FAKE_INPUT")
	return v, v != ""
}
