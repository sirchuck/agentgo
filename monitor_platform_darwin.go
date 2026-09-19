//go:build darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type darwinMonitorCollector struct {
	staticOnce   sync.Once
	staticInfo   MonitorSystemInfo
	staticCaps   MonitorCapabilities
	staticErr    error
	lastNetRx    uint64
	lastNetTx    uint64
	lastAt       time.Time
	thermalState string
	thermalAt    time.Time
}

func newMonitorPlatformCollector() monitorPlatformCollector {
	return &darwinMonitorCollector{}
}

func (c *darwinMonitorCollector) StaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	c.staticOnce.Do(func() {
		c.staticInfo, c.staticCaps, c.staticErr = collectDarwinStaticInfo()
	})
	return c.staticInfo, c.staticCaps, c.staticErr
}

func (c *darwinMonitorCollector) Collect(now time.Time) (MonitorSnapshot, error) {
	info, caps, err := c.StaticInfo()
	if err != nil {
		return MonitorSnapshot{}, err
	}
	processes, processCPU, procErr := readDarwinProcesses()
	snapshot := MonitorSnapshot{Timestamp: now.Format(time.RFC3339), System: info, Capabilities: caps}
	if procErr == nil {
		snapshot.Processes = processes
		snapshot.CPUPercent = monitorFloat(clampPercent(processCPU / float64(monitorDarwinMaxInt(runtime.NumCPU(), 1))))
	}
	usedMem, totalMem := readDarwinMemory()
	snapshot.MemoryUsedBytes = usedMem
	snapshot.MemoryTotalBytes = totalMem
	snapshot.MemoryPercent = monitorPercent(usedMem, totalMem)
	if totalMem > 0 {
		snapshot.System.TotalMemoryBytes = totalMem
	}
	rootTotal, rootFree := darwinDiskSpace("/")
	if rootTotal > 0 {
		snapshot.DiskTotalBytes = rootTotal
		snapshot.DiskUsedBytes = rootTotal - rootFree
		snapshot.DiskPercent = monitorPercent(snapshot.DiskUsedBytes, rootTotal)
		snapshot.System.StorageTotalBytes = rootTotal
		snapshot.System.StorageFreeBytes = rootFree
	}
	rx, tx := readDarwinNetworkBytes()
	elapsed := now.Sub(c.lastAt).Seconds()
	if !c.lastAt.IsZero() && elapsed > 0 {
		if rx >= c.lastNetRx {
			snapshot.NetworkRxBytesPerSec = monitorFloat(float64(rx-c.lastNetRx) / elapsed)
		}
		if tx >= c.lastNetTx {
			snapshot.NetworkTxBytesPerSec = monitorFloat(float64(tx-c.lastNetTx) / elapsed)
		}
	}
	c.lastNetRx, c.lastNetTx, c.lastAt = rx, tx, now
	if c.thermalAt.IsZero() || now.Sub(c.thermalAt) >= 30*time.Second {
		c.thermalState = readDarwinThermalState()
		c.thermalAt = now
	}
	snapshot.ThermalState = c.thermalState
	if c.thermalState != "" && c.thermalState != "Unavailable" {
		snapshot.Capabilities.ThermalState = true
	}
	snapshot.System.UptimeSeconds = readDarwinUptime()
	return snapshot, nil
}

func collectDarwinStaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	hostname, _ := os.Hostname()
	product := strings.TrimSpace(commandOutput("sw_vers", "-productName"))
	version := strings.TrimSpace(commandOutput("sw_vers", "-productVersion"))
	build := strings.TrimSpace(commandOutput("sw_vers", "-buildVersion"))
	osVersion := strings.TrimSpace(product + " " + version)
	if build != "" {
		osVersion += " (" + build + ")"
	}
	cpuModel := strings.TrimSpace(commandOutput("sysctl", "-n", "machdep.cpu.brand_string"))
	if cpuModel == "" {
		cpuModel = strings.TrimSpace(commandOutput("sysctl", "-n", "hw.model"))
	}
	info := MonitorSystemInfo{
		OS:            "macOS",
		OSVersion:     osVersion,
		Architecture:  runtime.GOARCH,
		Hostname:      hostname,
		CPUModel:      cpuModel,
		GPUModel:      readDarwinGPUModel(),
		UptimeSeconds: readDarwinUptime(),
	}
	_, info.TotalMemoryBytes = readDarwinMemory()
	info.StorageTotalBytes, info.StorageFreeBytes = darwinDiskSpace("/")
	caps := MonitorCapabilities{
		CPUUsage:       true,
		Memory:         true,
		DiskUsage:      info.StorageTotalBytes > 0,
		Network:        true,
		Processes:      true,
		ProcessFile:    true,
		ProcessDetails: true,
		ThermalState:   true,
	}
	return info, caps, nil
}

func commandOutput(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func readDarwinGPUModel() string {
	out := commandOutput("system_profiler", "SPDisplaysDataType", "-json")
	if strings.TrimSpace(out) == "" {
		return ""
	}
	var payload struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return ""
	}
	for _, item := range payload.Displays {
		for _, key := range []string{"sppci_model", "_name", "spdisplays_chipset_model"} {
			if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func readDarwinMemory() (uint64, uint64) {
	totalText := strings.TrimSpace(commandOutput("sysctl", "-n", "hw.memsize"))
	total, _ := strconv.ParseUint(totalText, 10, 64)
	if total == 0 {
		return 0, 0
	}
	out := commandOutput("vm_stat")
	if out == "" {
		return 0, total
	}
	pageSize := uint64(4096)
	if first := strings.SplitN(out, "\n", 2)[0]; strings.Contains(first, "page size of") {
		fields := strings.Fields(first)
		for i, field := range fields {
			if field == "of" && i+1 < len(fields) {
				if v, err := strconv.ParseUint(strings.Trim(fields[i+1], " ."), 10, 64); err == nil {
					pageSize = v
				}
			}
		}
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		v, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(parts[1]), "."), 10, 64)
		if err == nil {
			values[strings.TrimSpace(parts[0])] = v
		}
	}
	freePages := values["Pages free"] + values["Pages speculative"]
	available := freePages * pageSize
	if available > total {
		available = total
	}
	return total - available, total
}

func darwinDiskSpace(root string) (uint64, uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return 0, 0
	}
	blockSize := uint64(stat.Bsize)
	return stat.Blocks * blockSize, stat.Bavail * blockSize
}

func readDarwinUptime() uint64 {
	out := strings.TrimSpace(commandOutput("sysctl", "-n", "kern.boottime"))
	// Example: { sec = 1720000000, usec = 0 } ...
	idx := strings.Index(out, "sec =")
	if idx < 0 {
		return 0
	}
	tail := strings.TrimSpace(out[idx+len("sec ="):])
	end := strings.IndexAny(tail, ",}")
	if end >= 0 {
		tail = tail[:end]
	}
	boot, err := strconv.ParseInt(strings.TrimSpace(tail), 10, 64)
	if err != nil || boot <= 0 {
		return 0
	}
	now := time.Now().Unix()
	if now <= boot {
		return 0
	}
	return uint64(now - boot)
}

func readDarwinNetworkBytes() (uint64, uint64) {
	out := commandOutput("netstat", "-ibn")
	if out == "" {
		return 0, 0
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	headers := strings.Fields(lines[0])
	iIn, iOut, iName := -1, -1, -1
	for i, header := range headers {
		switch strings.ToLower(header) {
		case "name":
			iName = i
		case "ibytes":
			iIn = i
		case "obytes":
			iOut = i
		}
	}
	if iIn < 0 || iOut < 0 || iName < 0 {
		return 0, 0
	}
	seen := map[string]bool{}
	var rx, tx uint64
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		maxIndex := iOut
		if iIn > maxIndex {
			maxIndex = iIn
		}
		if iName > maxIndex {
			maxIndex = iName
		}
		if len(fields) <= maxIndex {
			continue
		}
		name := fields[iName]
		if name == "lo0" || seen[name] {
			continue
		}
		seen[name] = true
		in, _ := strconv.ParseUint(fields[iIn], 10, 64)
		out, _ := strconv.ParseUint(fields[iOut], 10, 64)
		rx += in
		tx += out
	}
	return rx, tx
}

func readDarwinThermalState() string {
	out := strings.TrimSpace(commandOutput("pmset", "-g", "therm"))
	if out == "" {
		return "Unavailable"
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "no thermal warning") {
		return "Nominal"
	}
	for _, key := range []string{"cpu_speed_limit", "gpu_speed_limit", "scheduler_limit"} {
		idx := strings.Index(lower, key)
		if idx < 0 {
			continue
		}
		line := strings.SplitN(lower[idx:], "\n", 2)[0]
		fields := strings.Fields(line)
		for _, field := range fields {
			clean := strings.Trim(field, "%=:, ")
			if v, err := strconv.Atoi(clean); err == nil && v < 100 {
				return "Elevated"
			}
		}
	}
	return "Nominal"
}

func readDarwinProcesses() ([]MonitorProcess, float64, error) {
	cmd := exec.Command("ps", "-axo", "pid=,ppid=,%cpu=,rss=,stat=,comm=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, err
	}
	processes := []MonitorProcess{}
	var totalCPU float64
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 6 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		cpu, errCPU := strconv.ParseFloat(fields[2], 64)
		rssKB, _ := strconv.ParseUint(fields[3], 10, 64)
		if errPID != nil || errCPU != nil || pid <= 0 {
			continue
		}
		status := fields[4]
		path := strings.Join(fields[5:], " ")
		name := filepath.Base(path)
		if name == "." || name == string(filepath.Separator) || name == "" {
			name = path
		}
		var size int64
		if info, statErr := os.Stat(path); statErr == nil {
			size = info.Size()
		}
		cpuValue := clampPercent(cpu / float64(monitorDarwinMaxInt(runtime.NumCPU(), 1)))
		processes = append(processes, MonitorProcess{
			PID:           pid,
			ParentPID:     ppid,
			Name:          name,
			Path:          path,
			FileSizeBytes: size,
			CPUPercent:    monitorFloat(cpuValue),
			MemoryBytes:   rssKB * 1024,
			Status:        darwinProcessStatus(status),
		})
		totalCPU += cpu
	}
	sortMonitorProcesses(processes)
	return processes, totalCPU, nil
}

func darwinProcessStatus(status string) string {
	if status == "" {
		return ""
	}
	switch status[0] {
	case 'R':
		return "Running"
	case 'S', 'I':
		return "Sleeping"
	case 'T':
		return "Stopped"
	case 'Z':
		return "Zombie"
	default:
		return status
	}
}

func monitorDarwinMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
