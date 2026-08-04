//go:build windows
// +build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// initColors checks terminal capabilities and sets ANSI codes accordingly.
// On Windows, it attempts to enable Virtual Terminal Processing so that
// cmd.exe and PowerShell can interpret ANSI escape sequences natively.
// If the terminal doesn't support colors, all color constants are set to empty strings.
func initColors() {
	if noColor {
		// User explicitly disabled colors
		return
	}

	// Try to enable Virtual Terminal Processing on Windows
	// This makes cmd.exe and PowerShell interpret ANSI codes natively
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err == nil {
		// ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004
		if err := windows.SetConsoleMode(handle, mode|0x0004); err == nil {
			// Success: terminal now supports ANSI codes
			setColorCodes()
			return
		}
	}
	// Failed to enable VT processing — output is likely piped/redirected
	// or running in an old console that doesn't support it
}
