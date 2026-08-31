//go:build linux

package main

import (
	"fmt"
	"os"
)

func kokoroPathComparisonCaseInsensitive() bool { return false }

func kokoroProcessExecutablePath(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid PID %d", pid)
	}
	return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
}
