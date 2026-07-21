//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package main

import "os/exec"

type agenticProcessController struct{}

func newAgenticProcessController(cmd *exec.Cmd) (*agenticProcessController, error) {
	return &agenticProcessController{}, nil
}
func (c *agenticProcessController) AfterStart(cmd *exec.Cmd) error { return nil }
func (c *agenticProcessController) Graceful(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func (c *agenticProcessController) Force(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func (c *agenticProcessController) Close() error { return nil }
