//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func revealMonitorFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("file path is empty")
	}
	return exec.Command("open", "-R", path).Start()
}
