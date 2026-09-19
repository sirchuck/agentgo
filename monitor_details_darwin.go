//go:build darwin

package main

import (
	"encoding/json"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var darwinSwapRE = regexp.MustCompile(`(?i)(total|used|free)\s*=\s*([0-9.]+)([KMGTP])`)
var darwinDiskDeviceRE = regexp.MustCompile(`^(disk[0-9]+)`)

var (
	darwinGPUDetailOnce  sync.Once
	darwinGPUDetailCache []MonitorGPUDetail
)

func readDarwinCPUDetail() *MonitorCPUDetail {
	detail := &MonitorCPUDetail{
		Model:         strings.TrimSpace(commandOutput("sysctl", "-n", "machdep.cpu.brand_string")),
		Architecture:  runtime.GOARCH,
		PhysicalCores: parseDarwinInt(commandOutput("sysctl", "-n", "hw.physicalcpu")),
		LogicalCPUs:   parseDarwinInt(commandOutput("sysctl", "-n", "hw.logicalcpu")),
	}
	if detail.Model == "" {
		detail.Model = strings.TrimSpace(commandOutput("sysctl", "-n", "hw.model"))
	}
	if mhz := parseDarwinHzMHz(commandOutput("sysctl", "-n", "hw.cpufrequency")); mhz != nil {
		detail.CurrentFrequencyMHz = mhz
	}
	if mhz := parseDarwinHzMHz(commandOutput("sysctl", "-n", "hw.cpufrequency_max")); mhz != nil {
		detail.MaxFrequencyMHz = mhz
	}
	// Apple Silicon exposes performance-level topology on supported macOS builds.
	// We surface that topology when available without pretending macOS exposes
	// unprivileged per-core utilization counters that it does not provide here.
	index := 0
	for level := 0; level < 4; level++ {
		count := parseDarwinInt(commandOutput("sysctl", "-n", "hw.perflevel"+strconv.Itoa(level)+".physicalcpu"))
		if count <= 0 {
			continue
		}
		kind := strings.TrimSpace(commandOutput("sysctl", "-n", "hw.perflevel"+strconv.Itoa(level)+".name"))
		if kind == "" {
			kind = "Performance level " + strconv.Itoa(level)
		}
		for i := 0; i < count; i++ {
			detail.Cores = append(detail.Cores, MonitorCPUCoreDetail{Index: index, Label: "Core " + strconv.Itoa(index), Kind: kind})
			index++
		}
	}
	if detail.Model == "" && detail.PhysicalCores == 0 && detail.LogicalCPUs == 0 {
		return nil
	}
	return detail
}

func parseDarwinInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func parseDarwinHzMHz(value string) *float64 {
	hz, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || hz <= 0 {
		return nil
	}
	return monitorFloat(hz / 1_000_000)
}

func readDarwinMemoryDetail() *MonitorMemoryDetail {
	totalText := strings.TrimSpace(commandOutput("sysctl", "-n", "hw.memsize"))
	total, _ := strconv.ParseUint(totalText, 10, 64)
	if total == 0 {
		return nil
	}
	out := commandOutput("vm_stat")
	pageSize := uint64(4096)
	if first := strings.SplitN(out, "\n", 2)[0]; strings.Contains(first, "page size of") {
		fields := strings.Fields(first)
		for i, field := range fields {
			if field == "of" && i+1 < len(fields) {
				if value, err := strconv.ParseUint(strings.Trim(fields[i+1], " ."), 10, 64); err == nil {
					pageSize = value
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
		value, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(parts[1]), "."), 10, 64)
		if err == nil {
			values[strings.TrimSpace(parts[0])] = value * pageSize
		}
	}
	free := values["Pages free"] + values["Pages speculative"]
	if free > total {
		free = total
	}
	wired := values["Pages wired down"]
	compressed := values["Pages occupied by compressor"]
	active := values["Pages active"]
	inactive := values["Pages inactive"]
	cached := values["File-backed pages"]
	memory := &MonitorMemoryDetail{
		TotalBytes:      total,
		UsedBytes:       total - free,
		AvailableBytes:  free,
		ActiveBytes:     active,
		InactiveBytes:   inactive,
		CachedBytes:     cached,
		WiredBytes:      wired,
		CompressedBytes: compressed,
	}
	for _, match := range darwinSwapRE.FindAllStringSubmatch(commandOutput("sysctl", "vm.swapusage"), -1) {
		if len(match) != 4 {
			continue
		}
		value := parseDarwinUnitBytes(match[2], match[3])
		switch strings.ToLower(match[1]) {
		case "total":
			memory.SwapTotalBytes = value
		case "used":
			memory.SwapUsedBytes = value
		case "free":
			memory.SwapFreeBytes = value
		}
	}
	return memory
}

func parseDarwinUnitBytes(number, unit string) uint64 {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return 0
	}
	multiplier := float64(1)
	switch strings.ToUpper(unit) {
	case "K":
		multiplier = 1024
	case "M":
		multiplier = 1024 * 1024
	case "G":
		multiplier = 1024 * 1024 * 1024
	case "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "P":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	}
	return uint64(value * multiplier)
}

func readDarwinDiskDetails() []MonitorDiskDetail {
	fsTypes := readDarwinMountTypes()
	out := []MonitorDiskDetail{}
	for _, line := range strings.Split(commandOutput("df", "-kP"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || strings.EqualFold(fields[0], "Filesystem") {
			continue
		}
		totalKB, err1 := strconv.ParseUint(fields[1], 10, 64)
		usedKB, err2 := strconv.ParseUint(fields[2], 10, 64)
		freeKB, err3 := strconv.ParseUint(fields[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || totalKB == 0 {
			continue
		}
		device := fields[0]
		mountPoint := strings.Join(fields[5:], " ")
		fileSystem := fsTypes[device]
		group, groupKind := darwinDiskGroup(device, fileSystem)
		total := totalKB * 1024
		used := usedKB * 1024
		free := freeKB * 1024
		out = append(out, MonitorDiskDetail{
			Name:         strings.TrimPrefix(device, "/dev/"),
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

func darwinDiskGroup(device, fileSystem string) (string, string) {
	fs := strings.ToLower(strings.TrimSpace(fileSystem))
	if fs == "nfs" || fs == "smbfs" || fs == "afpfs" || fs == "webdav" || strings.Contains(fs, "sshfs") {
		return "Shared / Network", "shared"
	}
	base := strings.TrimPrefix(strings.TrimSpace(device), "/dev/")
	if match := darwinDiskDeviceRE.FindStringSubmatch(base); len(match) == 2 {
		return match[1], "physical"
	}
	if strings.HasPrefix(device, "/dev/") && base != "" {
		return base, "physical"
	}
	return "Virtual / Other", "virtual"
}

func readDarwinMountTypes() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(commandOutput("mount"), "\n") {
		parts := strings.SplitN(line, " on ", 2)
		if len(parts) != 2 {
			continue
		}
		device := strings.TrimSpace(parts[0])
		if open := strings.Index(parts[1], "("); open >= 0 {
			inside := parts[1][open+1:]
			if close := strings.Index(inside, ","); close >= 0 {
				inside = inside[:close]
			} else if close := strings.Index(inside, ")"); close >= 0 {
				inside = inside[:close]
			}
			out[device] = strings.TrimSpace(inside)
		}
	}
	return out
}

func readDarwinGPUDetails() []MonitorGPUDetail {
	darwinGPUDetailOnce.Do(func() {
		out := monitorCommandOutput(8*time.Second, "system_profiler", "SPDisplaysDataType", "-json")
		if strings.TrimSpace(out) == "" {
			return
		}
		var payload struct {
			Displays []map[string]any `json:"SPDisplaysDataType"`
		}
		if json.Unmarshal([]byte(out), &payload) != nil {
			return
		}
		gpus := make([]MonitorGPUDetail, 0, len(payload.Displays))
		for index, item := range payload.Displays {
			name := firstDarwinString(item, "sppci_model", "spdisplays_chipset_model", "_name")
			if name == "" {
				continue
			}
			vendor := firstDarwinString(item, "spdisplays_vendor", "sppci_vendor")
			driver := firstDarwinString(item, "spdisplays_gmux-version", "spdisplays_metal")
			vramText := firstDarwinString(item, "spdisplays_vram", "spdisplays_vram_shared")
			vram := parseDarwinHumanBytes(vramText)
			gpus = append(gpus, MonitorGPUDetail{Index: index, Name: name, Vendor: vendor, Driver: driver, VRAMTotalBytes: vram, VRAMFreeBytes: vram})
		}
		darwinGPUDetailCache = gpus
	})
	return append([]MonitorGPUDetail(nil), darwinGPUDetailCache...)
}

func firstDarwinString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseDarwinHumanBytes(value string) uint64 {
	fields := strings.Fields(strings.ReplaceAll(value, ",", ""))
	if len(fields) < 2 {
		return 0
	}
	number, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || number <= 0 {
		return 0
	}
	unit := strings.ToUpper(fields[1])
	multiplier := float64(1)
	switch {
	case strings.HasPrefix(unit, "KB"):
		multiplier = 1024
	case strings.HasPrefix(unit, "MB"):
		multiplier = 1024 * 1024
	case strings.HasPrefix(unit, "GB"):
		multiplier = 1024 * 1024 * 1024
	case strings.HasPrefix(unit, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
	}
	return uint64(number * multiplier)
}
