//go:build !windows
// +build !windows

package main

// initColors checks terminal capabilities and sets ANSI codes accordingly.
// On Unix-like systems (Linux, macOS, etc.), ANSI escape codes are natively
// supported in terminals, so we always set the color codes unless --no-color is used.
func initColors() {
	if noColor {
		return
	}
	setColorCodes()
}
