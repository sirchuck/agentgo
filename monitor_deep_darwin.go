//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *darwinMonitorCollector) CollectDeep(now time.Time, processes []MonitorProcess) (MonitorDeepSnapshot, error) {
	return MonitorDeepSnapshot{
		CapturedAt:           now.Format(time.RFC3339),
		ProcessDetails:       readDarwinProcessDetails(),
		StartupItems:         readDarwinStartupItems(),
		Services:             readDarwinServices(),
		Connections:          readDarwinConnections(),
		Logs:                 readDarwinLogs(),
		Security:             readDarwinSecurityEvents(),
		SystemDetails:        readDarwinSystemDetails(),
		CPUDetail:            readDarwinCPUDetail(),
		GPUDetails:           readDarwinGPUDetails(),
		MemoryDetail:         readDarwinMemoryDetail(),
		DiskDetails:          readDarwinDiskDetails(),
		StartupSupported:     true,
		ServicesSupported:    true,
		ConnectionsSupported: true,
		LogsSupported:        true,
		SecuritySupported:    true,
	}, nil
}

func readDarwinProcessDetails() map[int]MonitorProcessDetail {
	out := monitorCommandOutput(5*time.Second, "ps", "-axo", "pid=,user=,lstart=")
	details := map[int]MonitorProcessDetail{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		started := strings.Join(fields[2:7], " ")
		if t, err := time.Parse("Mon Jan 2 15:04:05 2006", started); err == nil {
			started = t.Format(time.RFC3339)
		}
		details[pid] = MonitorProcessDetail{User: fields[1], StartTime: started}
	}
	return details
}

func readDarwinStartupItems() []MonitorStartupItem {
	dirs := []struct {
		path   string
		source string
	}{{"/Library/LaunchAgents", "System LaunchAgent"}, {"/Library/LaunchDaemons", "LaunchDaemon"}}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, struct {
			path   string
			source string
		}{filepath.Join(home, "Library", "LaunchAgents"), "User LaunchAgent"})
	}
	items := []MonitorStartupItem{}
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir.path, "*.plist"))
		for _, path := range matches {
			enabled := true
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			items = append(items, MonitorStartupItem{Name: name, Path: path, Source: dir.source, Enabled: &enabled})
		}
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items
}

func readDarwinServices() []MonitorService {
	out := monitorCommandOutput(5*time.Second, "launchctl", "list")
	services := []MonitorService{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.EqualFold(fields[0], "PID") {
			continue
		}
		pid := 0
		state := "Loaded"
		if fields[0] != "-" {
			pid, _ = strconv.Atoi(fields[0])
			if pid > 0 {
				state = "Running"
			}
		}
		services = append(services, MonitorService{Name: fields[2], State: state, StartupType: "launchd", PID: pid})
		if len(services) >= 500 {
			break
		}
	}
	sort.Slice(services, func(i, j int) bool { return strings.ToLower(services[i].Name) < strings.ToLower(services[j].Name) })
	return services
}

func readDarwinConnections() []MonitorNetworkConnection {
	out := monitorCommandOutput(6*time.Second, "lsof", "-nP", "-iTCP", "-iUDP")
	connections := []MonitorNetworkConnection{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || strings.EqualFold(fields[0], "COMMAND") {
			continue
		}
		pid, _ := strconv.Atoi(fields[1])
		protocol := strings.ToLower(fields[7])
		name := strings.Join(fields[8:], " ")
		state := ""
		if idx := strings.LastIndex(name, "("); idx >= 0 && strings.HasSuffix(name, ")") {
			state = strings.Trim(name[idx:], "()")
			name = strings.TrimSpace(name[:idx])
		}
		localPart, remotePart := name, ""
		if parts := strings.SplitN(name, "->", 2); len(parts) == 2 {
			localPart, remotePart = parts[0], parts[1]
		}
		localAddress, localPort := parseNetworkEndpoint(localPart)
		remoteAddress, remotePort := parseNetworkEndpoint(remotePart)
		connections = append(connections, MonitorNetworkConnection{Protocol: protocol, LocalAddress: localAddress, LocalPort: localPort, RemoteAddress: remoteAddress, RemotePort: remotePort, State: state, PID: pid, Process: fields[0]})
		if len(connections) >= 800 {
			break
		}
	}
	return connections
}

func readDarwinLogs() []MonitorLogRecord {
	out := monitorCommandOutput(8*time.Second, "/usr/bin/log", "show", "--last", "15m", "--style", "compact", "--predicate", "messageType == error OR messageType == fault")
	logs := []MonitorLogRecord{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Timestamp") {
			continue
		}
		logs = append(logs, MonitorLogRecord{Level: "error", Source: "macOS unified log", Message: line})
	}
	if len(logs) > 100 {
		logs = logs[len(logs)-100:]
	}
	return logs
}

func readDarwinSecurityEvents() []MonitorSecurityRecord {
	out := monitorCommandOutput(8*time.Second, "/usr/bin/log", "show", "--last", "2h", "--style", "compact", "--predicate", `process == "XProtectService" OR process == "syspolicyd" OR subsystem CONTAINS[c] "security"`)
	security := []MonitorSecurityRecord{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Timestamp") {
			continue
		}
		security = append(security, MonitorSecurityRecord{Severity: logLevelFromText(line), Source: "macOS security", Type: "security-event", Message: line})
		if len(security) >= 80 {
			break
		}
	}
	return security
}

func readDarwinSystemDetails() []MonitorInfoDetail {
	kernel := strings.TrimSpace(monitorCommandOutput(2*time.Second, "sysctl", "-n", "kern.osrelease"))
	logical := strings.TrimSpace(monitorCommandOutput(2*time.Second, "sysctl", "-n", "hw.logicalcpu"))
	physical := strings.TrimSpace(monitorCommandOutput(2*time.Second, "sysctl", "-n", "hw.physicalcpu"))
	ports := monitorCommandOutput(3*time.Second, "networksetup", "-listallhardwareports")
	adapters := []string{}
	for _, line := range strings.Split(ports, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Device:") {
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Device:"))
			if name != "" {
				adapters = append(adapters, name)
			}
		}
	}
	return []MonitorInfoDetail{{Label: "Darwin kernel", Value: kernel}, {Label: "Physical CPU cores", Value: physical}, {Label: "Logical CPUs", Value: logical}, {Label: "Network adapters", Value: strings.Join(adapters, ", ")}}
}
