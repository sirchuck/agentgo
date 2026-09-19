//go:build windows

package main

import (
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func readWindowsCPUDetail() *MonitorCPUDetail {
	type cpuRow struct {
		Name                      string  `json:"Name"`
		NumberOfCores             int     `json:"NumberOfCores"`
		NumberOfLogicalProcessors int     `json:"NumberOfLogicalProcessors"`
		CurrentClockSpeed         float64 `json:"CurrentClockSpeed"`
		MaxClockSpeed             float64 `json:"MaxClockSpeed"`
	}
	cpus := decodePSList[cpuRow](`Get-CimInstance Win32_Processor -ErrorAction SilentlyContinue | Select-Object Name,NumberOfCores,NumberOfLogicalProcessors,CurrentClockSpeed,MaxClockSpeed`)
	detail := &MonitorCPUDetail{Architecture: runtime.GOARCH}
	var currentTotal, maxTotal float64
	for i, cpu := range cpus {
		if i == 0 {
			detail.Model = strings.TrimSpace(cpu.Name)
		} else if name := strings.TrimSpace(cpu.Name); name != "" && !strings.Contains(detail.Model, name) {
			detail.Model = strings.TrimSpace(detail.Model + ", " + name)
		}
		detail.PhysicalCores += cpu.NumberOfCores
		detail.LogicalCPUs += cpu.NumberOfLogicalProcessors
		if cpu.CurrentClockSpeed > 0 {
			currentTotal += cpu.CurrentClockSpeed
		}
		if cpu.MaxClockSpeed > 0 {
			maxTotal += cpu.MaxClockSpeed
		}
	}
	if len(cpus) > 0 {
		if currentTotal > 0 {
			detail.CurrentFrequencyMHz = monitorFloat(currentTotal / float64(len(cpus)))
		}
		if maxTotal > 0 {
			detail.MaxFrequencyMHz = monitorFloat(maxTotal / float64(len(cpus)))
		}
	}
	type perfRow struct {
		Name                 string  `json:"Name"`
		PercentProcessorTime float64 `json:"PercentProcessorTime"`
	}
	rows := decodePSList[perfRow](`Get-CimInstance Win32_PerfFormattedData_PerfOS_Processor -ErrorAction SilentlyContinue | Where-Object {$_.Name -ne '_Total'} | Select-Object Name,PercentProcessorTime`)
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		index := len(detail.Cores)
		if parsed, err := strconv.Atoi(name); err == nil {
			index = parsed
		}
		usage := clampPercent(row.PercentProcessorTime)
		detail.Cores = append(detail.Cores, MonitorCPUCoreDetail{Index: index, Label: "CPU " + name, UsagePercent: monitorFloat(usage)})
	}
	sort.Slice(detail.Cores, func(i, j int) bool { return detail.Cores[i].Index < detail.Cores[j].Index })
	if detail.LogicalCPUs == 0 {
		detail.LogicalCPUs = len(detail.Cores)
	}
	if detail.Model == "" && detail.LogicalCPUs == 0 && len(detail.Cores) == 0 {
		return nil
	}
	return detail
}

func readWindowsMemoryDetail() *MonitorMemoryDetail {
	type row struct {
		Total     uint64 `json:"Total"`
		Free      uint64 `json:"Free"`
		Cache     uint64 `json:"Cache"`
		PageTotal uint64 `json:"PageTotal"`
		PageUsed  uint64 `json:"PageUsed"`
	}
	script := `$os=Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue; $perf=Get-CimInstance Win32_PerfFormattedData_PerfOS_Memory -ErrorAction SilentlyContinue; $pages=Get-CimInstance Win32_PageFileUsage -ErrorAction SilentlyContinue; $pt=0; $pu=0; foreach($p in @($pages)){if($p){$pt += [uint64]$p.AllocatedBaseSize*1MB; $pu += [uint64]$p.CurrentUsage*1MB}}; [pscustomobject]@{Total=[uint64]$os.TotalVisibleMemorySize*1KB;Free=[uint64]$os.FreePhysicalMemory*1KB;Cache=[uint64]$perf.CacheBytes;PageTotal=$pt;PageUsed=$pu}`
	rows := decodePSList[row](script)
	if len(rows) == 0 || rows[0].Total == 0 {
		return nil
	}
	r := rows[0]
	free := r.Free
	if free > r.Total {
		free = r.Total
	}
	pageUsed := r.PageUsed
	if pageUsed > r.PageTotal {
		pageUsed = r.PageTotal
	}
	return &MonitorMemoryDetail{
		TotalBytes:     r.Total,
		UsedBytes:      r.Total - free,
		AvailableBytes: free,
		CachedBytes:    r.Cache,
		SwapTotalBytes: r.PageTotal,
		SwapUsedBytes:  pageUsed,
		SwapFreeBytes:  r.PageTotal - pageUsed,
	}
}

func readWindowsDiskDetails() []MonitorDiskDetail {
	type row struct {
		DeviceID   string `json:"DeviceID"`
		VolumeName string `json:"VolumeName"`
		FileSystem string `json:"FileSystem"`
		Size       uint64 `json:"Size"`
		FreeSpace  uint64 `json:"FreeSpace"`
		DriveType  int    `json:"DriveType"`
	}
	rows := decodePSList[row](`Get-CimInstance Win32_LogicalDisk -ErrorAction SilentlyContinue | Where-Object {$_.DriveType -in 2,3,4} | Select-Object DeviceID,VolumeName,FileSystem,Size,FreeSpace,DriveType`)
	out := make([]MonitorDiskDetail, 0, len(rows))
	for _, row := range rows {
		if row.Size == 0 {
			continue
		}
		free := row.FreeSpace
		if free > row.Size {
			free = row.Size
		}
		used := row.Size - free
		kind := "Local"
		groupKind := "physical"
		switch row.DriveType {
		case 2:
			kind = "Removable"
		case 4:
			kind = "Network"
			groupKind = "shared"
		}
		name := strings.TrimSpace(row.VolumeName)
		if name == "" {
			name = strings.TrimSpace(row.DeviceID)
		}
		out = append(out, MonitorDiskDetail{
			Name:       name,
			Device:     strings.TrimSpace(row.DeviceID),
			MountPoint: strings.TrimSpace(row.DeviceID) + `\`,
			FileSystem: strings.TrimSpace(row.FileSystem),
			Kind:       kind,
			Group: func() string {
				if groupKind == "shared" {
					return "Shared / Network"
				}
				return strings.TrimSpace(row.DeviceID)
			}(),
			GroupKind:    groupKind,
			TotalBytes:   row.Size,
			UsedBytes:    used,
			FreeBytes:    free,
			UsagePercent: monitorPercent(used, row.Size),
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Device) < strings.ToLower(out[j].Device) })
	return out
}

func readWindowsGPUDetails() []MonitorGPUDetail {
	if rows := readWindowsNvidiaSMIGPUs(); len(rows) > 0 {
		return rows
	}
	type row struct {
		Name           string `json:"Name"`
		AdapterRAM     uint64 `json:"AdapterRAM"`
		DriverVersion  string `json:"DriverVersion"`
		VideoProcessor string `json:"VideoProcessor"`
	}
	rows := decodePSList[row](`Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | Select-Object Name,AdapterRAM,DriverVersion,VideoProcessor`)
	out := make([]MonitorGPUDetail, 0, len(rows))
	for index, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = strings.TrimSpace(row.VideoProcessor)
		}
		if name == "" {
			continue
		}
		vendor := ""
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "nvidia"):
			vendor = "NVIDIA"
		case strings.Contains(lower, "amd") || strings.Contains(lower, "radeon"):
			vendor = "AMD"
		case strings.Contains(lower, "intel"):
			vendor = "Intel"
		}
		out = append(out, MonitorGPUDetail{Index: index, Name: name, Vendor: vendor, Driver: strings.TrimSpace(row.DriverVersion), VRAMTotalBytes: row.AdapterRAM, VRAMFreeBytes: row.AdapterRAM})
	}
	return out
}

func readWindowsNvidiaSMIGPUs() []MonitorGPUDetail {
	query := "index,name,driver_version,utilization.gpu,memory.total,memory.used,memory.free,temperature.gpu,fan.speed,clocks.current.graphics,clocks.current.memory"
	out := monitorCommandOutput(5*time.Second, "nvidia-smi.exe", "--query-gpu="+query, "--format=csv,noheader,nounits")
	if strings.TrimSpace(out) == "" {
		out = monitorCommandOutput(5*time.Second, "nvidia-smi", "--query-gpu="+query, "--format=csv,noheader,nounits")
	}
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
		detail.UsagePercent = parseWindowsOptionalFloat(parts[3])
		detail.VRAMTotalBytes = parseWindowsMiB(parts[4])
		detail.VRAMUsedBytes = parseWindowsMiB(parts[5])
		detail.VRAMFreeBytes = parseWindowsMiB(parts[6])
		detail.TemperatureC = parseWindowsOptionalFloat(parts[7])
		detail.FanPercent = parseWindowsOptionalFloat(parts[8])
		detail.CoreClockMHz = parseWindowsOptionalFloat(parts[9])
		detail.MemoryClockMHz = parseWindowsOptionalFloat(parts[10])
		rows = append(rows, detail)
	}
	return rows
}

func parseWindowsOptionalFloat(value string) *float64 {
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

func parseWindowsMiB(value string) uint64 {
	if n := parseWindowsOptionalFloat(value); n != nil && *n > 0 {
		return uint64(*n * 1024 * 1024)
	}
	return 0
}
