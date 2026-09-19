//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"os"
	"runtime"
	"time"
)

type genericMonitorCollector struct{}

func newMonitorPlatformCollector() monitorPlatformCollector { return &genericMonitorCollector{} }

func (c *genericMonitorCollector) StaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	hostname, _ := os.Hostname()
	return MonitorSystemInfo{OS: runtime.GOOS, Architecture: runtime.GOARCH, Hostname: hostname}, MonitorCapabilities{}, nil
}

func (c *genericMonitorCollector) Collect(now time.Time) (MonitorSnapshot, error) {
	info, caps, _ := c.StaticInfo()
	return MonitorSnapshot{Timestamp: now.Format(time.RFC3339), System: info, Capabilities: caps, CollectorNote: "System telemetry is not implemented on this platform."}, errors.New("system telemetry is not implemented on this platform")
}
