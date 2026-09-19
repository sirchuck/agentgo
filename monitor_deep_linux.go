//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var linuxSSProcessRE = regexp.MustCompile(`users:\(\(\"([^\"]+)\",pid=(\d+)`)

func (c *linuxMonitorCollector) CollectDeep(now time.Time, processes []MonitorProcess) (MonitorDeepSnapshot, error) {
	deep := MonitorDeepSnapshot{
		CapturedAt:           now.Format(time.RFC3339),
		ProcessDetails:       readLinuxProcessDetails(processes),
		StartupItems:         readLinuxStartupItems(),
		Services:             readLinuxServices(),
		Connections:          readLinuxConnections(),
		Logs:                 readLinuxLogs(),
		Security:             readLinuxSecurityEvents(),
		SystemDetails:        readLinuxSystemDetails(),
		CPUDetail:            readLinuxCPUDetail(),
		GPUDetails:           readLinuxGPUDetails(),
		MemoryDetail:         readLinuxMemoryDetail(),
		DiskDetails:          readLinuxDiskDetails(),
		StartupSupported:     true,
		ServicesSupported:    true,
		ConnectionsSupported: true,
		LogsSupported:        true,
		SecuritySupported:    true,
	}
	return deep, nil
}

func readLinuxPasswd() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 3 {
			out[parts[2]] = parts[0]
		}
	}
	return out
}

func linuxBootTime() int64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			return v
		}
	}
	return 0
}

func linuxClockTicks() float64 {
	out := strings.TrimSpace(monitorCommandOutput(2*time.Second, "getconf", "CLK_TCK"))
	if v, err := strconv.ParseFloat(out, 64); err == nil && v > 0 {
		return v
	}
	return 100
}

func linuxProcessStartTime(pid int, boot int64, ticksPerSecond float64) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	text := string(data)
	closeIdx := strings.LastIndex(text, ")")
	if closeIdx < 0 {
		return ""
	}
	rest := strings.Fields(strings.TrimSpace(text[closeIdx+1:]))
	if len(rest) <= 19 || boot <= 0 || ticksPerSecond <= 0 {
		return ""
	}
	startTicks, err := strconv.ParseFloat(rest[19], 64)
	if err != nil {
		return ""
	}
	started := time.Unix(boot, 0).Add(time.Duration(startTicks / ticksPerSecond * float64(time.Second)))
	return started.Format(time.RFC3339)
}

func readLinuxProcessDetails(processes []MonitorProcess) map[int]MonitorProcessDetail {
	users := readLinuxPasswd()
	boot := linuxBootTime()
	ticks := linuxClockTicks()
	out := make(map[int]MonitorProcessDetail, len(processes))
	for _, proc := range processes {
		if proc.PID <= 0 {
			continue
		}
		detail := MonitorProcessDetail{StartTime: linuxProcessStartTime(proc.PID, boot, ticks)}
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", proc.PID)); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(line, "Uid:") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					detail.User = users[fields[1]]
					if detail.User == "" {
						detail.User = fields[1]
					}
				}
				break
			}
		}
		out[proc.PID] = detail
	}
	return out
}

func parseDesktopFile(path, source string) (MonitorStartupItem, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MonitorStartupItem{}, false
	}
	values := map[string]string{}
	inDesktop := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inDesktop = line == "[Desktop Entry]"
			continue
		}
		if !inDesktop || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	name := strings.TrimSpace(values["Name"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	enabled := !strings.EqualFold(values["Hidden"], "true") && !strings.EqualFold(values["X-GNOME-Autostart-enabled"], "false")
	return MonitorStartupItem{Name: name, Command: values["Exec"], Path: path, Source: source, Enabled: &enabled}, true
}

func readLinuxStartupItems() []MonitorStartupItem {
	dirs := []struct {
		path   string
		source string
	}{{"/etc/xdg/autostart", "System autostart"}}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, struct {
			path   string
			source string
		}{filepath.Join(home, ".config", "autostart"), "User autostart"})
	}
	items := []MonitorStartupItem{}
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir.path, "*.desktop"))
		for _, path := range matches {
			if item, ok := parseDesktopFile(path, dir.source); ok {
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items
}

func readLinuxServices() []MonitorService {
	startup := map[string]string{}
	files := monitorCommandOutput(5*time.Second, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--plain")
	for _, line := range strings.Split(files, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			startup[fields[0]] = fields[1]
		}
	}
	out := monitorCommandOutput(5*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain")
	services := []MonitorService{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		description := ""
		if len(fields) > 4 {
			description = strings.Join(fields[4:], " ")
		}
		services = append(services, MonitorService{Name: name, State: fields[2] + "/" + fields[3], StartupType: startup[name], Description: description})
		if len(services) >= 400 {
			break
		}
	}
	sort.Slice(services, func(i, j int) bool { return strings.ToLower(services[i].Name) < strings.ToLower(services[j].Name) })
	return services
}

func readLinuxConnections() []MonitorNetworkConnection {
	out := monitorCommandOutput(5*time.Second, "ss", "-H", "-tunap")
	connections := []MonitorNetworkConnection{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		protocol := strings.ToLower(fields[0])
		state := fields[1]
		localAddress, localPort := parseNetworkEndpoint(fields[4])
		remoteAddress, remotePort := parseNetworkEndpoint(fields[5])
		procName := ""
		pid := 0
		if match := linuxSSProcessRE.FindStringSubmatch(line); len(match) == 3 {
			procName = match[1]
			pid, _ = strconv.Atoi(match[2])
		}
		connections = append(connections, MonitorNetworkConnection{Protocol: protocol, LocalAddress: localAddress, LocalPort: localPort, RemoteAddress: remoteAddress, RemotePort: remotePort, State: state, PID: pid, Process: procName})
		if len(connections) >= 800 {
			break
		}
	}
	return connections
}

func readLinuxLogs() []MonitorLogRecord {
	out := monitorCommandOutput(6*time.Second, "journalctl", "-n", "100", "--no-pager", "--output=short-iso")
	if strings.TrimSpace(out) == "" {
		out = monitorCommandOutput(3*time.Second, "dmesg", "--ctime", "--level=err,warn")
	}
	logs := []MonitorLogRecord{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		logs = append(logs, MonitorLogRecord{Level: logLevelFromText(line), Source: "system", Message: line})
	}
	if len(logs) > 100 {
		logs = logs[len(logs)-100:]
	}
	return logs
}

func readLinuxSecurityEvents() []MonitorSecurityRecord {
	out := monitorCommandOutput(6*time.Second, "journalctl", "-n", "250", "--no-pager", "--output=short-iso")
	security := []MonitorSecurityRecord{}
	needles := []string{"failed password", "authentication failure", "apparmor=\"denied\"", "avc: denied", "selinux", "clamav", "malware", "xprotect", "sudo:"}
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		matched := false
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		security = append(security, MonitorSecurityRecord{Severity: logLevelFromText(line), Source: "Linux security logs", Type: "security-event", Message: strings.TrimSpace(line)})
		if len(security) >= 80 {
			break
		}
	}
	return security
}

func readLinuxSystemDetails() []MonitorInfoDetail {
	adapters := []string{}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, entry := range entries {
		if entry.Name() != "lo" {
			adapters = append(adapters, entry.Name())
		}
	}
	sort.Strings(adapters)
	kernel := strings.TrimSpace(monitorCommandOutput(2*time.Second, "uname", "-r"))
	return []MonitorInfoDetail{
		{Label: "Kernel", Value: kernel},
		{Label: "Logical CPUs", Value: strconv.Itoa(runtime.NumCPU())},
		{Label: "Network adapters", Value: strings.Join(adapters, ", ")},
	}
}
