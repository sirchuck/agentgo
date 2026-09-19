//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	windowsDeepCacheMu   sync.Mutex
	windowsDeepPathCache = map[string]MonitorProcessDetail{}
)

func psJSON(script string, target any) bool {
	out := monitorCommandOutput(12*time.Second, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script+" | ConvertTo-Json -Compress -Depth 5")
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return false
	}
	return json.Unmarshal([]byte(out), target) == nil
}

func decodePSList[T any](script string) []T {
	out := monitorCommandOutput(12*time.Second, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script+" | ConvertTo-Json -Compress -Depth 5")
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil
	}
	var list []T
	if strings.HasPrefix(out, "[") {
		_ = json.Unmarshal([]byte(out), &list)
		return list
	}
	var one T
	if json.Unmarshal([]byte(out), &one) == nil {
		return []T{one}
	}
	return nil
}

func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (c *windowsMonitorCollector) CollectDeep(now time.Time, processes []MonitorProcess) (MonitorDeepSnapshot, error) {
	deep := MonitorDeepSnapshot{
		CapturedAt:           now.Format(time.RFC3339),
		ProcessDetails:       readWindowsProcessDetails(processes),
		StartupItems:         readWindowsStartupItems(),
		Services:             readWindowsServices(),
		Connections:          readWindowsConnections(),
		Logs:                 readWindowsLogs(),
		Security:             readWindowsSecurityEvents(),
		SystemDetails:        readWindowsSystemDetails(),
		CPUDetail:            readWindowsCPUDetail(),
		GPUDetails:           readWindowsGPUDetails(),
		MemoryDetail:         readWindowsMemoryDetail(),
		DiskDetails:          readWindowsDiskDetails(),
		StartupSupported:     true,
		ServicesSupported:    true,
		ConnectionsSupported: true,
		LogsSupported:        true,
		SecuritySupported:    true,
	}
	return deep, nil
}

func readWindowsProcessDetails(processes []MonitorProcess) map[int]MonitorProcessDetail {
	type startRow struct {
		ID        int    `json:"Id"`
		StartTime string `json:"StartTime"`
	}
	starts := decodePSList[startRow](`Get-Process -ErrorAction SilentlyContinue | ForEach-Object { $s=''; try {$s=$_.StartTime.ToString('o')} catch {}; [pscustomobject]@{Id=$_.Id;StartTime=$s} }`)
	startMap := map[int]string{}
	for _, row := range starts {
		startMap[row.ID] = row.StartTime
	}

	windowsDeepCacheMu.Lock()
	uncached := []string{}
	seen := map[string]bool{}
	for _, proc := range processes {
		path := strings.TrimSpace(proc.Path)
		if path == "" || seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		if _, ok := windowsDeepPathCache[strings.ToLower(path)]; !ok {
			uncached = append(uncached, path)
			if len(uncached) >= 48 {
				break
			}
		}
	}
	windowsDeepCacheMu.Unlock()

	if len(uncached) > 0 {
		type fileRow struct {
			Path      string `json:"Path"`
			Version   string `json:"Version"`
			Publisher string `json:"Publisher"`
			Signature string `json:"Signature"`
		}
		quoted := make([]string, 0, len(uncached))
		for _, path := range uncached {
			quoted = append(quoted, psQuote(path))
		}
		script := fmt.Sprintf(`$paths=@(%s); foreach($p in $paths){ try { $i=Get-Item -LiteralPath $p -ErrorAction Stop; $sig=Get-AuthenticodeSignature -FilePath $p -ErrorAction SilentlyContinue; [pscustomobject]@{Path=$p;Version=$i.VersionInfo.FileVersion;Publisher=$i.VersionInfo.CompanyName;Signature=if($sig){$sig.Status.ToString()}else{''}} } catch {} }`, strings.Join(quoted, ","))
		rows := decodePSList[fileRow](script)
		windowsDeepCacheMu.Lock()
		for _, row := range rows {
			windowsDeepPathCache[strings.ToLower(row.Path)] = MonitorProcessDetail{Version: strings.TrimSpace(row.Version), Publisher: strings.TrimSpace(row.Publisher), Signature: strings.TrimSpace(row.Signature)}
		}
		windowsDeepCacheMu.Unlock()
	}

	out := make(map[int]MonitorProcessDetail, len(processes))
	windowsDeepCacheMu.Lock()
	defer windowsDeepCacheMu.Unlock()
	for _, proc := range processes {
		detail := windowsDeepPathCache[strings.ToLower(strings.TrimSpace(proc.Path))]
		detail.StartTime = startMap[proc.PID]
		out[proc.PID] = detail
	}
	return out
}

func readWindowsStartupItems() []MonitorStartupItem {
	type row struct {
		Name     string `json:"Name"`
		Command  string `json:"Command"`
		Location string `json:"Location"`
		User     string `json:"User"`
	}
	rows := decodePSList[row](`Get-CimInstance Win32_StartupCommand -ErrorAction SilentlyContinue | Select-Object Name,Command,Location,User`)
	items := make([]MonitorStartupItem, 0, len(rows))
	for _, row := range rows {
		enabled := true
		items = append(items, MonitorStartupItem{Name: strings.TrimSpace(row.Name), Command: strings.TrimSpace(row.Command), Source: strings.TrimSpace(row.Location), Enabled: &enabled})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items
}

func readWindowsServices() []MonitorService {
	type row struct {
		Name        string `json:"Name"`
		State       string `json:"State"`
		StartMode   string `json:"StartMode"`
		ProcessID   int    `json:"ProcessId"`
		PathName    string `json:"PathName"`
		StartName   string `json:"StartName"`
		Description string `json:"Description"`
	}
	rows := decodePSList[row](`Get-CimInstance Win32_Service -ErrorAction SilentlyContinue | Select-Object Name,State,StartMode,ProcessId,PathName,StartName,Description`)
	services := make([]MonitorService, 0, len(rows))
	for _, row := range rows {
		services = append(services, MonitorService{Name: row.Name, State: row.State, StartupType: row.StartMode, PID: row.ProcessID, Command: row.PathName, User: row.StartName, Description: row.Description})
	}
	sort.Slice(services, func(i, j int) bool { return strings.ToLower(services[i].Name) < strings.ToLower(services[j].Name) })
	return services
}

func readWindowsConnections() []MonitorNetworkConnection {
	type row struct {
		Protocol      string `json:"Protocol"`
		LocalAddress  string `json:"LocalAddress"`
		LocalPort     int    `json:"LocalPort"`
		RemoteAddress string `json:"RemoteAddress"`
		RemotePort    int    `json:"RemotePort"`
		State         string `json:"State"`
		PID           int    `json:"PID"`
		Process       string `json:"Process"`
	}
	script := `$names=@{}; Get-Process -ErrorAction SilentlyContinue | ForEach-Object {$names[$_.Id]=$_.ProcessName}; $tcp=Get-NetTCPConnection -ErrorAction SilentlyContinue | ForEach-Object {[pscustomobject]@{Protocol='tcp';LocalAddress=$_.LocalAddress;LocalPort=$_.LocalPort;RemoteAddress=$_.RemoteAddress;RemotePort=$_.RemotePort;State=$_.State.ToString();PID=$_.OwningProcess;Process=$names[$_.OwningProcess]}}; $udp=Get-NetUDPEndpoint -ErrorAction SilentlyContinue | ForEach-Object {[pscustomobject]@{Protocol='udp';LocalAddress=$_.LocalAddress;LocalPort=$_.LocalPort;RemoteAddress='';RemotePort=0;State='Bound';PID=$_.OwningProcess;Process=$names[$_.OwningProcess]}}; @($tcp)+@($udp)`
	rows := decodePSList[row](script)
	connections := make([]MonitorNetworkConnection, 0, len(rows))
	for _, row := range rows {
		connections = append(connections, MonitorNetworkConnection{Protocol: row.Protocol, LocalAddress: row.LocalAddress, LocalPort: row.LocalPort, RemoteAddress: row.RemoteAddress, RemotePort: row.RemotePort, State: row.State, PID: row.PID, Process: row.Process})
		if len(connections) >= 1000 {
			break
		}
	}
	return connections
}

func readWindowsLogs() []MonitorLogRecord {
	type row struct {
		TimeCreated      string `json:"TimeCreated"`
		LevelDisplayName string `json:"LevelDisplayName"`
		ProviderName     string `json:"ProviderName"`
		ID               int    `json:"Id"`
		Message          string `json:"Message"`
	}
	rows := decodePSList[row](`Get-WinEvent -FilterHashtable @{LogName='System','Application';StartTime=(Get-Date).AddMinutes(-20)} -MaxEvents 100 -ErrorAction SilentlyContinue | Select-Object @{N='TimeCreated';E={$_.TimeCreated.ToString('o')}},LevelDisplayName,ProviderName,Id,Message`)
	logs := make([]MonitorLogRecord, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, MonitorLogRecord{Time: row.TimeCreated, Level: strings.ToLower(row.LevelDisplayName), Source: row.ProviderName, EventID: strconv.Itoa(row.ID), Message: strings.TrimSpace(row.Message)})
	}
	return logs
}

func readWindowsSecurityEvents() []MonitorSecurityRecord {
	type row struct {
		TimeCreated  string `json:"TimeCreated"`
		ProviderName string `json:"ProviderName"`
		ID           int    `json:"Id"`
		Message      string `json:"Message"`
	}
	script := `Get-WinEvent -FilterHashtable @{LogName='Microsoft-Windows-Windows Defender/Operational';StartTime=(Get-Date).AddHours(-2)} -MaxEvents 80 -ErrorAction SilentlyContinue | Where-Object {$_.Id -in 1006,1007,1116,1117,1118,1119,5007} | Select-Object @{N='TimeCreated';E={$_.TimeCreated.ToString('o')}},ProviderName,Id,Message`
	rows := decodePSList[row](script)
	security := make([]MonitorSecurityRecord, 0, len(rows))
	for _, row := range rows {
		severity := "notice"
		if row.ID == 1116 || row.ID == 1006 || row.ID == 1007 {
			severity = "warning"
		}
		security = append(security, MonitorSecurityRecord{Time: row.TimeCreated, Severity: severity, Source: row.ProviderName, Type: "defender", EventID: strconv.Itoa(row.ID), Message: strings.TrimSpace(row.Message)})
	}
	return security
}

func readWindowsSystemDetails() []MonitorInfoDetail {
	type row struct {
		Label string `json:"Label"`
		Value string `json:"Value"`
	}
	script := `$os=Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue; $cs=Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue; $bios=Get-CimInstance Win32_BIOS -ErrorAction SilentlyContinue; @([pscustomobject]@{Label='Windows build';Value=$os.BuildNumber},[pscustomobject]@{Label='Manufacturer / model';Value=(($cs.Manufacturer+' '+$cs.Model).Trim())},[pscustomobject]@{Label='Logical processors';Value=[string]$cs.NumberOfLogicalProcessors},[pscustomobject]@{Label='BIOS';Value=(($bios.Manufacturer+' '+$bios.SMBIOSBIOSVersion).Trim())})`
	rows := decodePSList[row](script)
	details := make([]MonitorInfoDetail, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Value) != "" {
			details = append(details, MonitorInfoDetail{Label: row.Label, Value: row.Value})
		}
	}
	return details
}
