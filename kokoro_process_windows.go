//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const kokoroProcessQueryLimitedInformation = 0x1000

var kokoroQueryFullProcessImageNameW = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

func kokoroPathComparisonCaseInsensitive() bool { return true }

func kokoroProcessExecutablePath(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid PID %d", pid)
	}
	handle, _, openErr := agenticOpenProcess.Call(kokoroProcessQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return "", fmt.Errorf("OpenProcess(%d) failed: %v", pid, openErr)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	ok, _, queryErr := kokoroQueryFullProcessImageNameW.Call(handle, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return "", fmt.Errorf("QueryFullProcessImageNameW(%d) failed: %v", pid, queryErr)
	}
	if size == 0 || int(size) > len(buffer) {
		return "", fmt.Errorf("QueryFullProcessImageNameW(%d) returned invalid length %d", pid, size)
	}
	return syscall.UTF16ToString(buffer[:size]), nil
}
