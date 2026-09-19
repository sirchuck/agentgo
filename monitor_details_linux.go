//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var linuxDRMCardRE = regexp.MustCompile(`^card([0-9]+)$`)

func readLinuxCPUDetail() *MonitorCPUDetail {
	detail := &MonitorCPUDetail{
		Model:         readLinuxCPUModel(),
		Architecture:  runtime.GOARCH,
		PhysicalCores: readLinuxPhysicalCoreCount(),
		LogicalCPUs:   runtime.NumCPU(),
	}
	first := readLinuxCPUCoreTotals()
	if len(first) > 0 {
		time.Sleep(120 * time.Millisecond)
		second := readLinuxCPUCoreTotals()
		indices := make([]int, 0, len(second))
		for index := range second {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		var currentFreqTotal, maxFreqTotal float64
		var currentFreqCount, maxFreqCount int
		for _, index := range indices {
			current := second[index]
			core := MonitorCPUCoreDetail{Index: index, Label: "CPU " + strconv.Itoa(index)}
			if previous, ok := first[index]; ok && current.total > previous.total {
				totalDelta := current.total - previous.total
				idleDelta := current.idle - previous.idle
				if totalDelta > 0 && idleDelta <= totalDelta {
					core.UsagePercent = monitorFloat(clampPercent(float64(totalDelta-idleDelta) * 100 / float64(totalDelta)))
				}
			}
			if freq := readLinuxCPUFrequencyMHz(index, false); freq != nil {
				core.FrequencyMHz = freq
				currentFreqTotal += *freq
				currentFreqCount++
			}
			if freq := readLinuxCPUFrequencyMHz(index, true); freq != nil {
				core.MaxFrequencyMHz = freq
				maxFreqTotal += *freq
				maxFreqCount++
			}
			detail.Cores = append(detail.Cores, core)
		}
		if currentFreqCount > 0 {
			detail.CurrentFrequencyMHz = monitorFloat(currentFreqTotal / float64(currentFreqCount))
		}
		if maxFreqCount > 0 {
			detail.MaxFrequencyMHz = monitorFloat(maxFreqTotal / float64(maxFreqCount))
		}
	}
	if detail.PhysicalCores <= 0 && detail.LogicalCPUs > 0 {
		detail.PhysicalCores = detail.LogicalCPUs
	}
	if detail.Model == "" && detail.LogicalCPUs == 0 && len(detail.Cores) == 0 {
		return nil
	}
	return detail
}

func readLinuxCPUCoreTotals() map[int]linuxCPUTotals {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil
	}
	out := map[int]linuxCPUTotals{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") || fields[0] == "cpu" {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu"))
		if err != nil {
			continue
		}
		values := make([]uint64, 0, len(fields)-1)
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err == nil {
				values = append(values, value)
			}
		}
		if len(values) < 4 {
			continue
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		out[index] = linuxCPUTotals{idle: idle, total: total}
	}
	return out
}

func readLinuxPhysicalCoreCount() int {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	seen := map[string]struct{}{}
	physicalID, coreID := "0", ""
	flush := func() {
		if coreID != "" {
			seen[physicalID+":"+coreID] = struct{}{}
		}
		physicalID, coreID = "0", ""
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.TrimSpace(parts[0]) {
		case "physical id":
			physicalID = strings.TrimSpace(parts[1])
		case "core id":
			coreID = strings.TrimSpace(parts[1])
		}
	}
	flush()
	return len(seen)
}

func readLinuxCPUFrequencyMHz(index int, max bool) *float64 {
	name := "scaling_cur_freq"
	if max {
		name = "cpuinfo_max_freq"
	}
	path := filepath.Join("/sys/devices/system/cpu", "cpu"+strconv.Itoa(index), "cpufreq", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	khz, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil || khz <= 0 {
		return nil
	}
	return monitorFloat(khz / 1000)
}

func readLinuxMemoryDetail() *MonitorMemoryDetail {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return nil
	}
	if available > total {
		available = total
	}
	swapTotal := values["SwapTotal"]
	swapFree := values["SwapFree"]
	if swapFree > swapTotal {
		swapFree = swapTotal
	}
	cached := values["Cached"] + values["SReclaimable"]
	return &MonitorMemoryDetail{
		TotalBytes:     total,
		UsedBytes:      total - available,
		AvailableBytes: available,
		ActiveBytes:    values["Active"],
		InactiveBytes:  values["Inactive"],
		CachedBytes:    cached,
		BuffersBytes:   values["Buffers"],
		SwapTotalBytes: swapTotal,
		SwapUsedBytes:  swapTotal - swapFree,
		SwapFreeBytes:  swapFree,
	}
}

func readLinuxDiskDetails() []MonitorDiskDetail {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	skipFS := map[string]bool{
		"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
		"cgroup": true, "cgroup2": true, "securityfs": true, "pstore": true, "debugfs": true,
		"tracefs": true, "configfs": true, "fusectl": true, "mqueue": true, "hugetlbfs": true,
		"autofs": true, "rpc_pipefs": true, "binfmt_misc": true, "efivarfs": true,
	}
	seen := map[string]bool{}
	out := []MonitorDiskDetail{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		device := decodeLinuxMountField(fields[0])
		mountPoint := decodeLinuxMountField(fields[1])
		fileSystem := fields[2]
		if mountPoint == "" || seen[mountPoint] || skipFS[fileSystem] {
			continue
		}
		seen[mountPoint] = true
		total, free := linuxDiskSpace(mountPoint)
		if total == 0 {
			continue
		}
		used := total - free
		name := filepath.Base(device)
		if name == "." || name == "/" || name == "" {
			name = device
		}
		group, groupKind := linuxDiskGroup(device, fileSystem)
		out = append(out, MonitorDiskDetail{
			Name:         name,
			Device:       device,
			MountPoint:   mountPoint,
			FileSystem:   fileSystem,
			Group:        group,
			GroupKind:    groupKind,
			TotalBytes:   total,
			UsedBytes:    used,
			FreeBytes:    free,
			UsagePercent: monitorPercent(used, total),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MountPoint == "/" {
			return true
		}
		if out[j].MountPoint == "/" {
			return false
		}
		return strings.ToLower(out[i].MountPoint) < strings.ToLower(out[j].MountPoint)
	})
	return out
}

func linuxDiskGroup(device, fileSystem string) (string, string) {
	fs := strings.ToLower(strings.TrimSpace(fileSystem))
	if linuxSharedFileSystem(fs) {
		return "Shared / Network", "shared"
	}
	base := filepath.Base(strings.TrimSpace(device))
	if strings.HasPrefix(base, "loop") || fs == "squashfs" {
		return "Virtual / Loop", "loop"
	}
	if strings.HasPrefix(device, "/dev/") {
		if resolved, err := filepath.EvalSymlinks(device); err == nil {
			base = filepath.Base(resolved)
		}
		parents := linuxUnderlyingBlockDevices(base, map[string]bool{})
		if len(parents) > 0 {
			sort.Strings(parents)
			return strings.Join(parents, " + "), "physical"
		}
	}
	return "Virtual / Other", "virtual"
}

func linuxSharedFileSystem(fileSystem string) bool {
	fs := strings.ToLower(strings.TrimSpace(fileSystem))
	return fs == "vboxsf" || fs == "9p" || fs == "cifs" || fs == "smbfs" || fs == "nfs" || fs == "nfs4" ||
		strings.Contains(fs, "sshfs") || strings.Contains(fs, "fuse.ssh")
}

func linuxUnderlyingBlockDevices(name string, seen map[string]bool) []string {
	name = strings.TrimSpace(name)
	if name == "" || seen[name] {
		return nil
	}
	seen[name] = true
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") ||
		strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") {
		return nil
	}
	sysPath := filepath.Join("/sys/class/block", name)
	if slaves, err := os.ReadDir(filepath.Join(sysPath, "slaves")); err == nil && len(slaves) > 0 {
		set := map[string]bool{}
		for _, slave := range slaves {
			for _, parent := range linuxUnderlyingBlockDevices(slave.Name(), seen) {
				set[parent] = true
			}
		}
		out := make([]string, 0, len(set))
		for parent := range set {
			out = append(out, parent)
		}
		return out
	}
	if _, err := os.Stat(filepath.Join(sysPath, "partition")); err == nil {
		if resolved, err := filepath.EvalSymlinks(sysPath); err == nil {
			parent := filepath.Base(filepath.Dir(resolved))
			if parent != "" && parent != name {
				return linuxUnderlyingBlockDevices(parent, seen)
			}
		}
	}
	return []string{name}
}

func decodeLinuxMountField(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

func readLinuxGPUDetails() []MonitorGPUDetail {
	if nvidia := readLinuxNvidiaSMIGPUs(); len(nvidia) > 0 {
		return nvidia
	}
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return nil
	}
	out := []MonitorGPUDetail{}
	for _, entry := range entries {
		match := linuxDRMCardRE.FindStringSubmatch(entry.Name())
		if len(match) != 2 {
			continue
		}
		index, _ := strconv.Atoi(match[1])
		deviceDir := filepath.Join("/sys/class/drm", entry.Name(), "device")
		if _, err := os.Stat(deviceDir); err != nil {
			continue
		}
		vendorID := strings.TrimPrefix(strings.TrimSpace(readSmallText(filepath.Join(deviceDir, "vendor"))), "0x")
		deviceID := strings.TrimPrefix(strings.TrimSpace(readSmallText(filepath.Join(deviceDir, "device"))), "0x")
		name := strings.TrimSpace("PCI " + vendorID + ":" + deviceID)
		if vendorID == "" && deviceID == "" {
			name = "GPU " + strconv.Itoa(index)
		}
		detail := MonitorGPUDetail{Index: index, Name: name, Vendor: linuxPCIVendorName(vendorID)}
		if driverPath, err := filepath.EvalSymlinks(filepath.Join(deviceDir, "driver")); err == nil {
			detail.Driver = filepath.Base(driverPath)
		}
		if value := readLinuxGPUPercent(filepath.Join(deviceDir, "gpu_busy_percent")); value != nil {
			detail.UsagePercent = value
		}
		detail.VRAMTotalBytes = readLinuxUint(filepath.Join(deviceDir, "mem_info_vram_total"))
		detail.VRAMUsedBytes = readLinuxUint(filepath.Join(deviceDir, "mem_info_vram_used"))
		if detail.VRAMTotalBytes >= detail.VRAMUsedBytes {
			detail.VRAMFreeBytes = detail.VRAMTotalBytes - detail.VRAMUsedBytes
		}
		if temp, fan := readLinuxGPUHwmon(deviceDir); temp != nil || fan != nil {
			detail.TemperatureC = temp
			detail.FanRPM = fan
		}
		out = append(out, detail)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func readSmallText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readLinuxUint(path string) uint64 {
	value, _ := strconv.ParseUint(strings.TrimSpace(readSmallText(path)), 10, 64)
	return value
}

func readLinuxGPUPercent(path string) *float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(readSmallText(path)), 64)
	if err != nil {
		return nil
	}
	return monitorFloat(clampPercent(value))
}

func readLinuxGPUHwmon(deviceDir string) (*float64, *float64) {
	matches, _ := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*"))
	for _, dir := range matches {
		var temp, fan *float64
		if raw, err := strconv.ParseFloat(strings.TrimSpace(readSmallText(filepath.Join(dir, "temp1_input"))), 64); err == nil && raw > 0 {
			temp = monitorFloat(raw / 1000)
		}
		if raw, err := strconv.ParseFloat(strings.TrimSpace(readSmallText(filepath.Join(dir, "fan1_input"))), 64); err == nil && raw > 0 {
			fan = monitorFloat(raw)
		}
		if temp != nil || fan != nil {
			return temp, fan
		}
	}
	return nil, nil
}

func linuxPCIVendorName(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "10de":
		return "NVIDIA"
	case "1002", "1022":
		return "AMD"
	case "8086":
		return "Intel"
	case "106b":
		return "Apple"
	default:
		return ""
	}
}

func readLinuxNvidiaSMIGPUs() []MonitorGPUDetail {
	if _, err := os.Stat("/usr/bin/nvidia-smi"); err != nil {
		if _, err := os.Stat("/usr/local/bin/nvidia-smi"); err != nil {
			return nil
		}
	}
	query := "index,name,driver_version,utilization.gpu,memory.total,memory.used,memory.free,temperature.gpu,fan.speed,clocks.current.graphics,clocks.current.memory"
	out := monitorCommandOutput(5*time.Second, "nvidia-smi", "--query-gpu="+query, "--format=csv,noheader,nounits")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	rows := []MonitorGPUDetail{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 11 {
			continue
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		index, _ := strconv.Atoi(parts[0])
		detail := MonitorGPUDetail{Index: index, Name: parts[1], Vendor: "NVIDIA", Driver: parts[2]}
		detail.UsagePercent = parseLinuxOptionalFloat(parts[3])
		detail.VRAMTotalBytes = parseLinuxMiB(parts[4])
		detail.VRAMUsedBytes = parseLinuxMiB(parts[5])
		detail.VRAMFreeBytes = parseLinuxMiB(parts[6])
		detail.TemperatureC = parseLinuxOptionalFloat(parts[7])
		detail.FanPercent = parseLinuxOptionalFloat(parts[8])
		detail.CoreClockMHz = parseLinuxOptionalFloat(parts[9])
		detail.MemoryClockMHz = parseLinuxOptionalFloat(parts[10])
		rows = append(rows, detail)
	}
	return rows
}

func parseLinuxOptionalFloat(value string) *float64 {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") || strings.EqualFold(value, "[Not Supported]") {
		return nil
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	return monitorFloat(n)
}

func parseLinuxMiB(value string) uint64 {
	if n := parseLinuxOptionalFloat(value); n != nil && *n > 0 {
		return uint64(*n * 1024 * 1024)
	}
	return 0
}
