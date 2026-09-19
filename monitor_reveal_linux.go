//go:build linux

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func revealMonitorFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("file path is empty")
	}
	dir := filepath.Dir(path)
	if bin, err := exec.LookPath("xdg-open"); err == nil {
		return exec.Command(bin, dir).Start()
	}
	if bin, err := exec.LookPath("gio"); err == nil {
		return exec.Command(bin, "open", dir).Start()
	}
	return fmt.Errorf("no supported file manager launcher found (xdg-open/gio)")
}
