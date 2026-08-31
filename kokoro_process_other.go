//go:build !linux && !windows

package main

import "fmt"

func kokoroPathComparisonCaseInsensitive() bool { return false }

func kokoroProcessExecutablePath(pid int) (string, error) {
	return "", fmt.Errorf("verified stale-process cleanup is not available on this platform")
}
