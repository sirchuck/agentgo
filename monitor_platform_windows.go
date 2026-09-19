//go:build windows

package main

import (
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
	"unsafe"
)

const (
	windowsTH32CS_SNAPPROCESS                = 0x00000002
	windowsPROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	windowsPROCESS_VM_READ                   = 0x0010
	windowsInvalidHandleValue                = ^uintptr(0)
)

var (
	monitorKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	monitorPsapi                      = syscall.NewLazyDLL("psapi.dll")
	monitorGetSystemTimes             = monitorKernel32.NewProc("GetSystemTimes")
	monitorGlobalMemoryStatusEx       = monitorKernel32.NewProc("GlobalMemoryStatusEx")
	monitorGetTickCount64             = monitorKernel32.NewProc("GetTickCount64")
	monitorCreateToolhelp32Snapshot   = monitorKernel32.NewProc("CreateToolhelp32Snapshot")
	monitorProcess32FirstW            = monitorKernel32.NewProc("Process32FirstW")
	monitorProcess32NextW             = monitorKernel32.NewProc("Process32NextW")
	monitorOpenProcess                = monitorKernel32.NewProc("OpenProcess")
	monitorCloseHandle                = monitorKernel32.NewProc("CloseHandle")
	monitorQueryFullProcessImageNameW = monitorKernel32.NewProc("QueryFullProcessImageNameW")
	monitorGetProcessTimes            = monitorKernel32.NewProc("GetProcessTimes")
	monitorGetProcessMemoryInfo       = monitorPsapi.NewProc("GetProcessMemoryInfo")
	monitorGetDiskFreeSpaceExW        = monitorKernel32.NewProc("GetDiskFreeSpaceExW")
)

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type windowsProcessEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

type windowsProcessMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type windowsMonitorCollector struct {
	staticOnce      sync.Once
	staticInfo      MonitorSystemInfo
	staticCaps      MonitorCapabilities
	staticErr       error
	lastSystemTotal uint64
	lastSystemIdle  uint64
	lastProcTicks   map[int]uint64
	lastNetRx       uint64
	lastNetTx       uint64
	lastAt          time.Time
	thermalAt       time.Time
	thermals        []MonitorThermalSensor
	cpuTemperatureC *float64
}

func newMonitorPlatformCollector() monitorPlatformCollector {
	return &windowsMonitorCollector{lastProcTicks: map[int]uint64{}}
}

func (c *windowsMonitorCollector) StaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	c.staticOnce.Do(func() {
		c.staticInfo, c.staticCaps, c.staticErr = collectWindowsStaticInfo()
	})
	return c.staticInfo, c.staticCaps, c.staticErr
}

func (c *windowsMonitorCollector) Collect(now time.Time) (MonitorSnapshot, error) {
	info, caps, err := c.StaticInfo()
	if err != nil {
		return MonitorSnapshot{}, err
	}
	snapshot := MonitorSnapshot{Timestamp: now.Format(time.RFC3339), System: info, Capabilities: caps}

	idle, total, cpuErr := readWindowsSystemTimes()
	if cpuErr == nil && c.lastSystemTotal > 0 && total > c.lastSystemTotal {
		totalDelta := total - c.lastSystemTotal
		idleDelta := idle - c.lastSystemIdle
		if totalDelta > 0 && idleDelta <= totalDelta {
			snapshot.CPUPercent = monitorFloat(clampPercent(float64(totalDelta-idleDelta) * 100 / float64(totalDelta)))
		}
	}
	processes, nextTicks := c.readWindowsProcesses(total)
	if len(processes) > 0 {
		if c.lastSystemTotal > 0 && total > c.lastSystemTotal {
			totalDelta := total - c.lastSystemTotal
			for i := range processes {
				if previous, ok := c.lastProcTicks[processes[i].PID]; ok {
					current := nextTicks[processes[i].PID]
					if current >= previous && totalDelta > 0 {
						processes[i].CPUPercent = monitorFloat(clampPercent(float64(current-previous) * 100 / float64(totalDelta)))
					}
				}
			}
		}
		snapshot.Processes = processes
	}
	c.lastProcTicks = nextTicks
	c.lastSystemIdle, c.lastSystemTotal = idle, total

	used, totalMem := readWindowsMemory()
	snapshot.MemoryUsedBytes = used
	snapshot.MemoryTotalBytes = totalMem
	snapshot.MemoryPercent = monitorPercent(used, totalMem)
	if totalMem > 0 {
		snapshot.System.TotalMemoryBytes = totalMem
	}
	diskTotal, diskFree := windowsDiskSpace()
	if diskTotal > 0 {
		snapshot.DiskTotalBytes = diskTotal
		snapshot.DiskUsedBytes = diskTotal - diskFree
		snapshot.DiskPercent = monitorPercent(snapshot.DiskUsedBytes, diskTotal)
		snapshot.System.StorageTotalBytes = diskTotal
		snapshot.System.StorageFreeBytes = diskFree
	}
	rx, tx := readWindowsNetworkBytes()
	elapsed := now.Sub(c.lastAt).Seconds()
	if !c.lastAt.IsZero() && elapsed > 0 && (rx > 0 || tx > 0) {
		if rx >= c.lastNetRx {
			snapshot.NetworkRxBytesPerSec = monitorFloat(float64(rx-c.lastNetRx) / elapsed)
		}
		if tx >= c.lastNetTx {
			snapshot.NetworkTxBytesPerSec = monitorFloat(float64(tx-c.lastNetTx) / elapsed)
		}
	}
	c.lastNetRx, c.lastNetTx, c.lastAt = rx, tx, now

	if c.thermalAt.IsZero() || now.Sub(c.thermalAt) >= 60*time.Second {
		c.thermals, c.cpuTemperatureC = readWindowsTemperatures()
		c.thermalAt = now
	}
	snapshot.Thermals = append([]MonitorThermalSensor{}, c.thermals...)
	snapshot.CPUTemperatureC = c.cpuTemperatureC
	if len(snapshot.Thermals) > 0 {
		snapshot.Capabilities.Temperatures = true
	}
	snapshot.System.UptimeSeconds = readWindowsUptime()
	return snapshot, nil
}

func collectWindowsStaticInfo() (MonitorSystemInfo, MonitorCapabilities, error) {
	hostname, _ := os.Hostname()
	info := MonitorSystemInfo{OS: "Windows", Architecture: runtime.GOARCH, Hostname: hostname, UptimeSeconds: readWindowsUptime()}
	type details struct {
		OS      string `json:"os"`
		Version string `json:"version"`
		Build   string `json:"build"`
		CPU     string `json:"cpu"`
		GPU     string `json:"gpu"`
	}
	ps := `$os=Get-CimInstance Win32_OperatingSystem; $cpu=(Get-CimInstance Win32_Processor | Select-Object -First 1).Name; $gpu=((Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name) -join ', '); [pscustomobject]@{os=$os.Caption;version=$os.Version;build=$os.BuildNumber;cpu=$cpu;gpu=$gpu} | ConvertTo-Json -Compress`
	if out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps).Output(); err == nil {
		var d details
		if json.Unmarshal(out, &d) == nil {
			info.OSVersion = strings.TrimSpace(d.OS + " " + d.Version)
			if strings.TrimSpace(d.Build) != "" {
				info.OSVersion += " (" + strings.TrimSpace(d.Build) + ")"
			}
			info.CPUModel = strings.TrimSpace(d.CPU)
			info.GPUModel = strings.TrimSpace(d.GPU)
		}
	}
	_, info.TotalMemoryBytes = readWindowsMemory()
	info.StorageTotalBytes, info.StorageFreeBytes = windowsDiskSpace()
	caps := MonitorCapabilities{
		CPUUsage:       true,
		Memory:         true,
		DiskUsage:      info.StorageTotalBytes > 0,
		Network:        true,
		Processes:      true,
		ProcessFile:    true,
		ProcessDetails: true,
	}
	return info, caps, nil
}

func windowsFiletimeValue(ft syscall.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func readWindowsSystemTimes() (uint64, uint64, error) {
	var idle, kernel, user syscall.Filetime
	r1, _, err := monitorGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r1 == 0 {
		return 0, 0, err
	}
	idleValue := windowsFiletimeValue(idle)
	return idleValue, windowsFiletimeValue(kernel) + windowsFiletimeValue(user), nil
}

func readWindowsMemory() (uint64, uint64) {
	status := windowsMemoryStatusEx{Length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	r1, _, _ := monitorGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if r1 == 0 || status.TotalPhys == 0 {
		return 0, 0
	}
	available := status.AvailPhys
	if available > status.TotalPhys {
		available = status.TotalPhys
	}
	return status.TotalPhys - available, status.TotalPhys
}

func readWindowsUptime() uint64 {
	r1, _, _ := monitorGetTickCount64.Call()
	return uint64(r1) / 1000
}

func windowsDiskSpace() (uint64, uint64) {
	cwd, err := os.Getwd()
	if err != nil {
		return 0, 0
	}
	volume := filepath.VolumeName(cwd)
	if volume == "" {
		volume = "C:"
	}
	root := volume + `\`
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0
	}
	var freeAvailable, total, totalFree uint64
	r1, _, _ := monitorGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		return 0, 0
	}
	return total, totalFree
}

func readWindowsNetworkBytes() (uint64, uint64) {
	cmd := exec.Command("netstat.exe", "-e")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.EqualFold(fields[0], "Bytes") {
			rx, _ := strconv.ParseUint(fields[1], 10, 64)
			tx, _ := strconv.ParseUint(fields[2], 10, 64)
			return rx, tx
		}
	}
	return 0, 0
}

func readWindowsTemperatures() ([]MonitorThermalSensor, *float64) {
	ps := `Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction SilentlyContinue | ForEach-Object { [math]::Round(($_.CurrentTemperature / 10) - 273.15, 1) }`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return nil, nil
	}
	var sensors []MonitorThermalSensor
	var cpu *float64
	index := 0
	for _, line := range strings.Split(string(out), "\n") {
		v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil || v < -20 || v > 160 {
			continue
		}
		index++
		sensors = append(sensors, MonitorThermalSensor{Name: "ACPI Thermal Zone " + strconv.Itoa(index), TemperatureC: monitorFloat(v), Kind: "system"})
		if cpu == nil || v > *cpu {
			cpu = monitorFloat(v)
		}
	}
	return sensors, cpu
}

func (c *windowsMonitorCollector) readWindowsProcesses(systemTotal uint64) ([]MonitorProcess, map[int]uint64) {
	handle, _, _ := monitorCreateToolhelp32Snapshot.Call(windowsTH32CS_SNAPPROCESS, 0)
	if handle == 0 || handle == windowsInvalidHandleValue {
		return nil, map[int]uint64{}
	}
	defer monitorCloseHandle.Call(handle)

	entry := windowsProcessEntry32{Size: uint32(unsafe.Sizeof(windowsProcessEntry32{}))}
	r1, _, _ := monitorProcess32FirstW.Call(handle, uintptr(unsafe.Pointer(&entry)))
	if r1 == 0 {
		return nil, map[int]uint64{}
	}
	processes := []MonitorProcess{}
	nextTicks := map[int]uint64{}
	for {
		pid := int(entry.ProcessID)
		if pid > 0 {
			name := syscall.UTF16ToString(entry.ExeFile[:])
			proc := MonitorProcess{PID: pid, ParentPID: int(entry.ParentProcessID), Name: name, Status: "Running"}
			processHandle, _, _ := monitorOpenProcess.Call(windowsPROCESS_QUERY_LIMITED_INFORMATION|windowsPROCESS_VM_READ, 0, uintptr(entry.ProcessID))
			if processHandle != 0 {
				path := windowsProcessPath(processHandle)
				proc.Path = path
				if path != "" {
					if info, err := os.Stat(path); err == nil {
						proc.FileSizeBytes = info.Size()
					}
				}
				proc.MemoryBytes = windowsProcessMemory(processHandle)
				if ticks, ok := windowsProcessCPUTicks(processHandle); ok {
					nextTicks[pid] = ticks
				}
				monitorCloseHandle.Call(processHandle)
			}
			processes = append(processes, proc)
		}
		entry = windowsProcessEntry32{Size: uint32(unsafe.Sizeof(windowsProcessEntry32{}))}
		r1, _, _ = monitorProcess32NextW.Call(handle, uintptr(unsafe.Pointer(&entry)))
		if r1 == 0 {
			break
		}
	}
	sortMonitorProcesses(processes)
	_ = systemTotal
	return processes, nextTicks
}

func windowsProcessPath(handle uintptr) string {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	r1, _, _ := monitorQueryFullProcessImageNameW.Call(handle, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if r1 == 0 || size == 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer[:size])
}

func windowsProcessMemory(handle uintptr) uint64 {
	counters := windowsProcessMemoryCounters{CB: uint32(unsafe.Sizeof(windowsProcessMemoryCounters{}))}
	r1, _, _ := monitorGetProcessMemoryInfo.Call(handle, uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB))
	if r1 == 0 {
		return 0
	}
	return uint64(counters.WorkingSetSize)
}

func windowsProcessCPUTicks(handle uintptr) (uint64, bool) {
	var creation, exit, kernel, user syscall.Filetime
	r1, _, _ := monitorGetProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		return 0, false
	}
	return windowsFiletimeValue(kernel) + windowsFiletimeValue(user), true
}
