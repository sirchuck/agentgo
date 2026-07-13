package main

import (
	"context"
	"os/exec"
	"time"
)

func runBrowserCommand(ctx context.Context, cmd *exec.Cmd) error {
	configureBrowserCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		terminateBrowserCommand(cmd)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		return ctx.Err()
	}
}
