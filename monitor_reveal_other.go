//go:build !windows && !darwin && !linux

package main

import "fmt"

func revealMonitorFile(path string) error {
	return fmt.Errorf("Show in Folder is not supported on this platform")
}
