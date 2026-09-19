//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type linuxMonitorCollector struct {
	staticOnce     sync.Once
	staticInfo     MonitorSystemInfo
	staticCaps     MonitorCapabilities
	staticErr      error
	lastCPU        linuxCPUTotals
	lastDiskRead   uint64
	lastDiskWrite  uint64
	lastDiskIO     map[string]linuxDiskCounters
	lastNetRx      uint64
	lastNetTx      uint64
	lastAt         time.Time
	lastProcTicks  map[int]uint64
	lastTotalTicks uint64
	pageSize       uint64
}

type linuxCPUTotals struct {
	idle  uint64
	total uint64
}

type linuxDiskCounters struct {
	readBytes  uint64
	writeBytes uint64
}

func newMonitorPlatformCollector() monitorPlatformCollector {
	return &linuxMonitorCollector{lastProcTicks: map[int]uint64{}, lastDiskIO: map[string]linuxDiskCounters{}, pageSize: uint64(os.Getpagesize())}
}

func (c *linuxMonitorCollector) StaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	c.staticOnce.Do(func() {
		c.staticInfo, c.staticCaps, c.staticErr = collectLinuxStaticInfo()
	})
	return c.staticInfo, c.staticCaps, c.staticErr
}

func (c *linuxMonitorCollector) Collect(now time.Time) (MonitorSnapshot, error) {
	info, caps, staticErr := c.StaticInfo()
	if staticErr != nil {
		return MonitorSnapshot{}, staticErr
	}
	snapshot := MonitorSnapshot{Timestamp: now.Format(time.RFC3339), System: info, Capabilities: caps}

	cpu, err := readLinuxCPU()
	if err == nil && c.lastCPU.total > 0 && cpu.total > c.lastCPU.total {
		totalDelta := cpu.total - c.lastCPU.total
		idleDelta := cpu.idle - c.lastCPU.idle
		if totalDelta > 0 && idleDelta <= totalDelta {
			snapshot.CPUPercent = monitorFloat(clampPercent(float64(totalDelta-idleDelta) * 100 / float64(totalDelta)))
		}
	}
	c.lastCPU = cpu

	usedMem, totalMem := readLinuxMemory()
	snapshot.MemoryUsedBytes = usedMem
	snapshot.MemoryTotalBytes = totalMem
	snapshot.MemoryPercent = monitorPercent(usedMem, totalMem)

	rootTotal, rootFree := linuxDiskSpace("/")
	if rootTotal > 0 {
		snapshot.DiskTotalBytes = rootTotal
		snapshot.DiskUsedBytes = rootTotal - rootFree
		snapshot.DiskPercent = monitorPercent(snapshot.DiskUsedBytes, rootTotal)
		snapshot.System.StorageTotalBytes = rootTotal
		snapshot.System.StorageFreeBytes = rootFree
	}

	diskIO := readLinuxDiskCounters()
	diskRead, diskWrite := sumLinuxDiskCounters(diskIO)
	netRx, netTx := readLinuxNetworkBytes()
	elapsed := now.Sub(c.lastAt).Seconds()
	if elapsed > 0 && !c.lastAt.IsZero() {
		if diskRead >= c.lastDiskRead {
			snapshot.DiskReadBytesPerSec = monitorFloat(float64(diskRead-c.lastDiskRead) / elapsed)
		}
		if diskWrite >= c.lastDiskWrite {
			snapshot.DiskWriteBytesPerSec = monitorFloat(float64(diskWrite-c.lastDiskWrite) / elapsed)
		}
		devices := make([]string, 0, len(diskIO))
		for device := range diskIO {
			devices = append(devices, device)
		}
		sort.Strings(devices)
		for _, device := range devices {
			current := diskIO[device]
			previous, ok := c.lastDiskIO[device]
			if !ok || current.readBytes < previous.readBytes || current.writeBytes < previous.writeBytes {
				continue
			}
			snapshot.DiskIO = append(snapshot.DiskIO, MonitorDiskIO{
				Device:           device,
				ReadBytesPerSec:  monitorFloat(float64(current.readBytes-previous.readBytes) / elapsed),
				WriteBytesPerSec: monitorFloat(float64(current.writeBytes-previous.writeBytes) / elapsed),
			})
		}
		if netRx >= c.lastNetRx {
			snapshot.NetworkRxBytesPerSec = monitorFloat(float64(netRx-c.lastNetRx) / elapsed)
		}
		if netTx >= c.lastNetTx {
			snapshot.NetworkTxBytesPerSec = monitorFloat(float64(netTx-c.lastNetTx) / elapsed)
		}
	}
	c.lastDiskRead, c.lastDiskWrite = diskRead, diskWrite
	c.lastDiskIO = diskIO
	c.lastNetRx, c.lastNetTx = netRx, netTx
	c.lastAt = now

	thermals, fans, cpuTemp, gpuTemp := readLinuxSensors()
	snapshot.Thermals = thermals
	snapshot.Fans = fans
	snapshot.CPUTemperatureC = cpuTemp
	snapshot.GPUTemperatureC = gpuTemp
	if len(thermals) > 0 {
		snapshot.Capabilities.Temperatures = true
	}
	if len(fans) > 0 {
		snapshot.Capabilities.Fans = true
	}
	if gpu := readLinuxGPUUsage(); gpu != nil {
		snapshot.GPUPercent = gpu
		snapshot.Capabilities.GPUUsage = true
	}

	processes, procErr := c.readProcesses(cpu.total)
	if procErr == nil {
		snapshot.Processes = processes
	}
	if snapshot.System.UptimeSeconds == 0 {
		snapshot.System.UptimeSeconds = readLinuxUptime()
	}
	return snapshot, nil
}

func collectLinuxStaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	hostname, _ := os.Hostname()
	info := MonitorSystemInfo{
		OS:            "Linux",
		OSVersion:     readLinuxOSVersion(),
		Architecture:  runtime.GOARCH,
		Hostname:      hostname,
		CPUModel:      readLinuxCPUModel(),
		GPUModel:      readLinuxGPUModel(),
		UptimeSeconds: readLinuxUptime(),
	}
	_, totalMem := readLinuxMemory()
	info.TotalMemoryBytes = totalMem
	info.StorageTotalBytes, info.StorageFreeBytes = linuxDiskSpace("/")
	caps := MonitorCapabilities{
		CPUUsage:       true,
		Memory:         true,
		DiskUsage:      info.StorageTotalBytes > 0,
		DiskActivity:   true,
		Network:        true,
		Processes:      true,
		ProcessFile:    true,
		ProcessDetails: true,
	}
	return info, caps, nil
}

func readLinuxOSVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[parts[0]] = strings.Trim(parts[1], "\"'")
	}
	if pretty := strings.TrimSpace(values["PRETTY_NAME"]); pretty != "" {
		return pretty
	}
	return strings.TrimSpace(values["NAME"] + " " + values["VERSION_ID"])
}

func readLinuxCPUModel() string {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		for _, key := range []string{"model name", "Hardware", "Processor"} {
			prefix := key + ":"
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return ""
}

func readLinuxGPUModel() string {
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/uevent")
	for _, filename := range matches {
		data, err := os.ReadFile(filename)
		if err != nil {
			continue
		}
		vendor, device := "", ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PCI_ID=") {
				parts := strings.Split(strings.TrimPrefix(line, "PCI_ID="), ":")
				if len(parts) == 2 {
					vendor, device = parts[0], parts[1]
				}
			}
		}
		if vendor != "" || device != "" {
			return strings.TrimSpace("PCI " + vendor + ":" + device)
		}
	}
	return ""
}

func readLinuxUptime() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func readLinuxCPU() (linuxCPUTotals, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return linuxCPUTotals{}, err
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return linuxCPUTotals{}, errors.New("unexpected /proc/stat format")
	}
	var values []uint64
	for _, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err == nil {
			values = append(values, v)
		}
	}
	if len(values) < 4 {
		return linuxCPUTotals{}, errors.New("not enough CPU counters")
	}
	var total uint64
	for _, v := range values {
		total += v
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return linuxCPUTotals{idle: idle, total: total}, nil
}

func readLinuxMemory() (uint64, uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = value * 1024
		case "MemAvailable":
			available = value * 1024
		}
	}
	if total == 0 {
		return 0, 0
	}
	if available > total {
		available = total
	}
	return total - available, total
}

func linuxDiskSpace(root string) (uint64, uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return 0, 0
	}
	blockSize := uint64(stat.Bsize)
	return stat.Blocks * blockSize, stat.Bavail * blockSize
}

func readLinuxDiskBytes() (uint64, uint64) {
	return sumLinuxDiskCounters(readLinuxDiskCounters())
}

func sumLinuxDiskCounters(counters map[string]linuxDiskCounters) (uint64, uint64) {
	var readBytes, writeBytes uint64
	for _, counter := range counters {
		readBytes += counter.readBytes
		writeBytes += counter.writeBytes
	}
	return readBytes, writeBytes
}

func readLinuxDiskCounters() map[string]linuxDiskCounters {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return nil
	}
	allowed := map[string]bool{}
	if devices, err := os.ReadDir("/sys/block"); err == nil {
		for _, device := range devices {
			name := device.Name()
			if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") ||
				strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") {
				continue
			}
			if slaves, err := os.ReadDir(filepath.Join("/sys/class/block", name, "slaves")); err == nil && len(slaves) > 0 {
				// Layered devices (LVM/device-mapper/RAID) account for I/O also seen by
				// their backing devices. Skip them here so aggregate rates are not doubled.
				continue
			}
			allowed[name] = true
		}
	}
	out := map[string]linuxDiskCounters{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") {
			continue
		}
		reads, errR := strconv.ParseUint(fields[5], 10, 64)
		writes, errW := strconv.ParseUint(fields[9], 10, 64)
		if errR != nil || errW != nil {
			continue
		}
		out[name] = linuxDiskCounters{readBytes: reads * 512, writeBytes: writes * 512}
	}
	return out
}

func readLinuxNetworkBytes() (uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, errR := strconv.ParseUint(fields[0], 10, 64)
		t, errT := strconv.ParseUint(fields[8], 10, 64)
		if errR == nil {
			rx += r
		}
		if errT == nil {
			tx += t
		}
	}
	return rx, tx
}

func readLinuxSensors() ([]MonitorThermalSensor, []MonitorFanSensor, *float64, *float64) {
	thermals := []MonitorThermalSensor{}
	fans := []MonitorFanSensor{}
	seen := map[string]bool{}
	var cpuTemp, gpuTemp *float64

	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, dir := range hwmons {
		nameBytes, _ := os.ReadFile(filepath.Join(dir, "name"))
		chip := strings.TrimSpace(string(nameBytes))
		temps, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		for _, tempFile := range temps {
			base := strings.TrimSuffix(filepath.Base(tempFile), "_input")
			labelBytes, _ := os.ReadFile(filepath.Join(dir, base+"_label"))
			label := strings.TrimSpace(string(labelBytes))
			if label == "" {
				label = base
			}
			valueBytes, err := os.ReadFile(tempFile)
			if err != nil {
				continue
			}
			raw, err := strconv.ParseFloat(strings.TrimSpace(string(valueBytes)), 64)
			if err != nil {
				continue
			}
			value := raw / 1000
			if value < -20 || value > 160 {
				continue
			}
			sensorName := strings.TrimSpace(chip + " " + label)
			if seen[sensorName] {
				continue
			}
			seen[sensorName] = true
			kind := "system"
			lower := strings.ToLower(chip + " " + label)
			if strings.Contains(lower, "cpu") || strings.Contains(lower, "core") || strings.Contains(lower, "package") || strings.Contains(lower, "k10temp") || strings.Contains(lower, "coretemp") {
				kind = "cpu"
				if cpuTemp == nil || value > *cpuTemp {
					cpuTemp = monitorFloat(value)
				}
			}
			if strings.Contains(lower, "gpu") || strings.Contains(lower, "amdgpu") || strings.Contains(lower, "nouveau") {
				kind = "gpu"
				if gpuTemp == nil || value > *gpuTemp {
					gpuTemp = monitorFloat(value)
				}
			}
			thermals = append(thermals, MonitorThermalSensor{Name: sensorName, TemperatureC: monitorFloat(value), Kind: kind})
		}
		fanFiles, _ := filepath.Glob(filepath.Join(dir, "fan*_input"))
		for _, fanFile := range fanFiles {
			valueBytes, err := os.ReadFile(fanFile)
			if err != nil {
				continue
			}
			rpm, err := strconv.ParseFloat(strings.TrimSpace(string(valueBytes)), 64)
			if err != nil || rpm <= 0 {
				continue
			}
			base := strings.TrimSuffix(filepath.Base(fanFile), "_input")
			labelBytes, _ := os.ReadFile(filepath.Join(dir, base+"_label"))
			label := strings.TrimSpace(string(labelBytes))
			if label == "" {
				label = base
			}
			fans = append(fans, MonitorFanSensor{Name: strings.TrimSpace(chip + " " + label), RPM: rpm})
		}
	}

	if len(thermals) == 0 {
		zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
		for _, zone := range zones {
			typeBytes, _ := os.ReadFile(filepath.Join(zone, "type"))
			valueBytes, err := os.ReadFile(filepath.Join(zone, "temp"))
			if err != nil {
				continue
			}
			raw, err := strconv.ParseFloat(strings.TrimSpace(string(valueBytes)), 64)
			if err != nil {
				continue
			}
			value := raw / 1000
			if value < -20 || value > 160 {
				continue
			}
			label := strings.TrimSpace(string(typeBytes))
			if label == "" {
				label = filepath.Base(zone)
			}
			thermals = append(thermals, MonitorThermalSensor{Name: label, TemperatureC: monitorFloat(value), Kind: "system"})
		}
	}
	return thermals, fans, cpuTemp, gpuTemp
}

func readLinuxGPUUsage() *float64 {
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent")
	for _, filename := range matches {
		data, err := os.ReadFile(filename)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err == nil {
			return monitorFloat(clampPercent(v))
		}
	}
	return nil
}

func (c *linuxMonitorCollector) readProcesses(totalTicks uint64) ([]MonitorProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	processes := make([]MonitorProcess, 0, len(entries)/2)
	nextTicks := make(map[int]uint64, len(c.lastProcTicks))
	totalDelta := uint64(0)
	if c.lastTotalTicks > 0 && totalTicks > c.lastTotalTicks {
		totalDelta = totalTicks - c.lastTotalTicks
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		proc, ticks, ok := c.readLinuxProcess(pid, totalDelta)
		if !ok {
			continue
		}
		nextTicks[pid] = ticks
		if previous, found := c.lastProcTicks[pid]; found && totalDelta > 0 && ticks >= previous {
			proc.CPUPercent = monitorFloat(clampPercent(float64(ticks-previous) * 100 / float64(totalDelta)))
		}
		processes = append(processes, proc)
	}
	c.lastProcTicks = nextTicks
	c.lastTotalTicks = totalTicks
	sortMonitorProcesses(processes)
	return processes, nil
}

func (c *linuxMonitorCollector) readLinuxProcess(pid int, totalDelta uint64) (MonitorProcess, uint64, bool) {
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return MonitorProcess{}, 0, false
	}
	text := string(statData)
	closeIdx := strings.LastIndex(text, ")")
	openIdx := strings.Index(text, "(")
	if openIdx < 0 || closeIdx <= openIdx {
		return MonitorProcess{}, 0, false
	}
	name := text[openIdx+1 : closeIdx]
	rest := strings.Fields(strings.TrimSpace(text[closeIdx+1:]))
	if len(rest) < 22 {
		return MonitorProcess{}, 0, false
	}
	ppid64, _ := strconv.ParseInt(rest[1], 10, 64)
	utime, _ := strconv.ParseUint(rest[11], 10, 64)
	stime, _ := strconv.ParseUint(rest[12], 10, 64)
	ticks := utime + stime
	status := rest[0]
	path, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	var size int64
	if path != "" {
		if info, err := os.Stat(path); err == nil {
			size = info.Size()
		}
	}
	var memory uint64
	if statm, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
		fields := strings.Fields(string(statm))
		if len(fields) > 1 {
			pages, _ := strconv.ParseUint(fields[1], 10, 64)
			memory = pages * c.pageSize
		}
	}
	return MonitorProcess{PID: pid, ParentPID: int(ppid64), Name: name, Path: path, FileSizeBytes: size, MemoryBytes: memory, Status: linuxProcessStatus(status)}, ticks, true
}

func linuxProcessStatus(code string) string {
	switch code {
	case "R":
		return "Running"
	case "S":
		return "Sleeping"
	case "D":
		return "Waiting"
	case "T", "t":
		return "Stopped"
	case "Z":
		return "Zombie"
	default:
		return code
	}
}
