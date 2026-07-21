//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os/exec"
	"syscall"
)

type agenticProcessController struct{}

func newAgenticProcessController(cmd *exec.Cmd) (*agenticProcessController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &agenticProcessController{}, nil
}
func (c *agenticProcessController) AfterStart(cmd *exec.Cmd) error { return nil }
func (c *agenticProcessController) Graceful(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
}
func (c *agenticProcessController) Force(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
func (c *agenticProcessController) Close() error { return nil }
