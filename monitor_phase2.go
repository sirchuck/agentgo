package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	monitorDeepPollActive  = 30 * time.Second
	monitorDeepPollPassive = 60 * time.Second
	monitorHistoryBucket   = 5 * time.Minute
	monitorRecentSummaries = 144
)

func monitorCommandOutput(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func parseNetworkEndpoint(value string) (string, int) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0
	}
	if strings.HasPrefix(value, "[") {
		if idx := strings.LastIndex(value, "]:"); idx >= 0 {
			address := strings.Trim(value[:idx+1], "[]")
			port, _ := strconv.Atoi(strings.TrimSpace(value[idx+2:]))
			return address, port
		}
	}
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return value, 0
	}
	address := strings.Trim(value[:idx], "[]")
	portText := strings.TrimSpace(value[idx+1:])
	if portText == "*" {
		return address, 0
	}
	port, _ := strconv.Atoi(portText)
	return address, port
}

func logLevelFromText(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "critical"), strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"):
		return "critical"
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"):
		return "error"
	case strings.Contains(lower, "warning"), strings.Contains(lower, "warn"):
		return "warning"
	default:
		return "info"
	}
}

type MonitorTelemetrySummary struct {
	Start                 string   `json:"start"`
	End                   string   `json:"end"`
	Samples               int      `json:"samples"`
	CPUAverage            *float64 `json:"cpu_average,omitempty"`
	CPUPeak               *float64 `json:"cpu_peak,omitempty"`
	GPUAverage            *float64 `json:"gpu_average,omitempty"`
	GPUPeak               *float64 `json:"gpu_peak,omitempty"`
	MemoryAverage         *float64 `json:"memory_average,omitempty"`
	MemoryPeak            *float64 `json:"memory_peak,omitempty"`
	DiskAverage           *float64 `json:"disk_average,omitempty"`
	DiskPeak              *float64 `json:"disk_peak,omitempty"`
	NetworkRxAverage      *float64 `json:"network_rx_average,omitempty"`
	NetworkRxPeak         *float64 `json:"network_rx_peak,omitempty"`
	NetworkTxAverage      *float64 `json:"network_tx_average,omitempty"`
	NetworkTxPeak         *float64 `json:"network_tx_peak,omitempty"`
	CPUTemperatureAverage *float64 `json:"cpu_temperature_average,omitempty"`
	CPUTemperaturePeak    *float64 `json:"cpu_temperature_peak,omitempty"`
	GPUTemperatureAverage *float64 `json:"gpu_temperature_average,omitempty"`
	GPUTemperaturePeak    *float64 `json:"gpu_temperature_peak,omitempty"`
}

type monitorMetricAccumulator struct {
	sum   float64
	count int
	peak  float64
}

func (a *monitorMetricAccumulator) add(value *float64) {
	if value == nil {
		return
	}
	v := *value
	a.sum += v
	a.count++
	if a.count == 1 || v > a.peak {
		a.peak = v
	}
}

func (a monitorMetricAccumulator) average() *float64 {
	if a.count == 0 {
		return nil
	}
	return monitorFloat(a.sum / float64(a.count))
}

func (a monitorMetricAccumulator) peakValue() *float64 {
	if a.count == 0 {
		return nil
	}
	return monitorFloat(a.peak)
}

type monitorTelemetryBucketState struct {
	start   time.Time
	end     time.Time
	samples int
	cpu     monitorMetricAccumulator
	gpu     monitorMetricAccumulator
	memory  monitorMetricAccumulator
	disk    monitorMetricAccumulator
	netRx   monitorMetricAccumulator
	netTx   monitorMetricAccumulator
	cpuTemp monitorMetricAccumulator
	gpuTemp monitorMetricAccumulator
}

func monitorBucketStart(t time.Time) time.Time {
	return t.Truncate(monitorHistoryBucket)
}

func (b *monitorTelemetryBucketState) add(now time.Time, snapshot MonitorSnapshot) {
	if b.start.IsZero() {
		b.start = monitorBucketStart(now)
	}
	b.end = now
	b.samples++
	b.cpu.add(snapshot.CPUPercent)
	b.gpu.add(snapshot.GPUPercent)
	b.memory.add(snapshot.MemoryPercent)
	b.disk.add(snapshot.DiskPercent)
	b.netRx.add(snapshot.NetworkRxBytesPerSec)
	b.netTx.add(snapshot.NetworkTxBytesPerSec)
	b.cpuTemp.add(snapshot.CPUTemperatureC)
	b.gpuTemp.add(snapshot.GPUTemperatureC)
}

func (b monitorTelemetryBucketState) summary(endOverride time.Time) (MonitorTelemetrySummary, bool) {
	if b.samples == 0 || b.start.IsZero() {
		return MonitorTelemetrySummary{}, false
	}
	end := b.end
	if !endOverride.IsZero() && endOverride.After(end) {
		end = endOverride
	}
	return MonitorTelemetrySummary{
		Start:                 b.start.Format(time.RFC3339),
		End:                   end.Format(time.RFC3339),
		Samples:               b.samples,
		CPUAverage:            b.cpu.average(),
		CPUPeak:               b.cpu.peakValue(),
		GPUAverage:            b.gpu.average(),
		GPUPeak:               b.gpu.peakValue(),
		MemoryAverage:         b.memory.average(),
		MemoryPeak:            b.memory.peakValue(),
		DiskAverage:           b.disk.average(),
		DiskPeak:              b.disk.peakValue(),
		NetworkRxAverage:      b.netRx.average(),
		NetworkRxPeak:         b.netRx.peakValue(),
		NetworkTxAverage:      b.netTx.average(),
		NetworkTxPeak:         b.netTx.peakValue(),
		CPUTemperatureAverage: b.cpuTemp.average(),
		CPUTemperaturePeak:    b.cpuTemp.peakValue(),
		GPUTemperatureAverage: b.gpuTemp.average(),
		GPUTemperaturePeak:    b.gpuTemp.peakValue(),
	}, true
}

type monitorHistoryRecord struct {
	Type      string                   `json:"type"`
	Time      string                   `json:"time"`
	Event     *MonitorEvent            `json:"event,omitempty"`
	Telemetry *MonitorTelemetrySummary `json:"telemetry,omitempty"`
}

type monitorHistoryStore struct {
	mu      sync.Mutex
	path    string
	maxByte int64
}

func newMonitorHistoryStore(root string, maxMB int) *monitorHistoryStore {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if _, ok := monitorHistoryMaxPresets[maxMB]; !ok {
		maxMB = monitorDefaultHistoryMaxMB
	}
	return &monitorHistoryStore{
		path:    filepath.Join(root, "history", "monitor_history.jsonl"),
		maxByte: int64(maxMB) * 1024 * 1024,
	}
}

func (s *monitorHistoryStore) setMaxMB(maxMB int) {
	if s == nil {
		return
	}
	if _, ok := monitorHistoryMaxPresets[maxMB]; !ok {
		maxMB = monitorDefaultHistoryMaxMB
	}
	s.mu.Lock()
	s.maxByte = int64(maxMB) * 1024 * 1024
	s.compactLocked()
	s.mu.Unlock()
}

func (s *monitorHistoryStore) append(record monitorHistoryRecord) {
	if s == nil {
		return
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_ = f.Close()
	if info, err := os.Stat(s.path); err == nil && info.Size() > s.maxByte {
		s.compactLocked()
	}
}

func (s *monitorHistoryStore) compactLocked() {
	if s == nil || s.maxByte <= 0 {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil || int64(len(data)) <= s.maxByte {
		return
	}
	// Keep a little headroom so a busy event period does not rewrite the file
	// on every append. JSONL boundaries are preserved by advancing to a newline.
	target := int64(float64(s.maxByte) * 0.88)
	start := int64(len(data)) - target
	if start < 0 {
		start = 0
	}
	for start < int64(len(data)) && data[start] != '\n' {
		start++
	}
	if start < int64(len(data)) {
		start++
	}
	trimmed := append([]byte(nil), data[start:]...)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, trimmed, 0o644); err == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *monitorHistoryStore) loadRecent() ([]MonitorEvent, []MonitorTelemetrySummary) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var events []MonitorEvent
	var summaries []MonitorTelemetrySummary
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		var record monitorHistoryRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.Event != nil {
			events = append(events, *record.Event)
			if len(events) > monitorMaxEvents {
				events = events[len(events)-monitorMaxEvents:]
			}
		}
		if record.Telemetry != nil {
			summaries = append(summaries, *record.Telemetry)
			if len(summaries) > monitorRecentSummaries {
				summaries = summaries[len(summaries)-monitorRecentSummaries:]
			}
		}
	}
	return append([]MonitorEvent(nil), events...), append([]MonitorTelemetrySummary(nil), summaries...)
}

func (s *monitorHistoryStore) clearEvents() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record monitorHistoryRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.Event != nil {
			continue
		}
		kept = append(kept, line)
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, []byte(content), 0o644)
}

type monitorPhase2State struct {
	mu          sync.Mutex
	store       *monitorHistoryStore
	history     []MonitorTelemetrySummary
	bucket      monitorTelemetryBucketState
	lastDeep    time.Time
	deepRunning bool
	cpuTempBand int
	gpuTempBand int
	cpuLoadBand int
	memoryBand  int
}

func newMonitorPhase2State(root string, maxMB int) *monitorPhase2State {
	p := &monitorPhase2State{store: newMonitorHistoryStore(root, maxMB)}
	if p.store != nil {
		_, p.history = p.store.loadRecent()
	}
	return p
}

func (p *monitorPhase2State) recentEvents() []MonitorEvent {
	if p == nil || p.store == nil {
		return nil
	}
	events, _ := p.store.loadRecent()
	return events
}

func (p *monitorPhase2State) recentHistory() []MonitorTelemetrySummary {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]MonitorTelemetrySummary(nil), p.history...)
}

func (p *monitorPhase2State) persistEvent(event MonitorEvent) {
	if p == nil || p.store == nil {
		return
	}
	p.store.append(monitorHistoryRecord{Type: "event", Time: event.Time, Event: &event})
}

func (p *monitorPhase2State) persistTelemetry(summary MonitorTelemetrySummary) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.history = append(p.history, summary)
	if len(p.history) > monitorRecentSummaries {
		p.history = append([]MonitorTelemetrySummary(nil), p.history[len(p.history)-monitorRecentSummaries:]...)
	}
	p.mu.Unlock()
	if p.store != nil {
		p.store.append(monitorHistoryRecord{Type: "telemetry", Time: summary.End, Telemetry: &summary})
	}
}

func (p *monitorPhase2State) setMaxMB(maxMB int) {
	if p == nil || p.store == nil {
		return
	}
	p.store.setMaxMB(maxMB)
}

func (p *monitorPhase2State) clearPersistedEvents() {
	if p == nil || p.store == nil {
		return
	}
	p.store.clearEvents()
}

func (p *monitorPhase2State) beginDeepCollection(now time.Time, mode string) bool {
	if p == nil {
		return false
	}
	interval := monitorDeepPollPassive
	if mode == "active" {
		interval = monitorDeepPollActive
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deepRunning {
		return false
	}
	if !p.lastDeep.IsZero() && now.Sub(p.lastDeep) < interval {
		return false
	}
	p.lastDeep = now
	p.deepRunning = true
	return true
}

func (p *monitorPhase2State) finishDeepCollection() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.deepRunning = false
	p.mu.Unlock()
}

func (m *monitorManager) phase2RecentHistoryLocked() []MonitorTelemetrySummary {
	if m == nil || m.phase2 == nil {
		return nil
	}
	return m.phase2.recentHistory()
}

func copyDeepFields(from MonitorSnapshot, into *MonitorSnapshot) {
	if into == nil {
		return
	}
	into.StartupItems = append([]MonitorStartupItem(nil), from.StartupItems...)
	into.Services = append([]MonitorService(nil), from.Services...)
	into.Connections = append([]MonitorNetworkConnection(nil), from.Connections...)
	into.Logs = append([]MonitorLogRecord(nil), from.Logs...)
	into.Security = append([]MonitorSecurityRecord(nil), from.Security...)
	into.SystemDetails = append([]MonitorInfoDetail(nil), from.SystemDetails...)
	into.CPUDetail = cloneMonitorCPUDetail(from.CPUDetail)
	into.GPUDetails = append([]MonitorGPUDetail(nil), from.GPUDetails...)
	into.MemoryDetail = cloneMonitorMemoryDetail(from.MemoryDetail)
	into.DiskDetails = append([]MonitorDiskDetail(nil), from.DiskDetails...)
	into.DeepCapturedAt = from.DeepCapturedAt
	into.Capabilities.StartupItems = from.Capabilities.StartupItems
	into.Capabilities.Services = from.Capabilities.Services
	into.Capabilities.Connections = from.Capabilities.Connections
	into.Capabilities.SystemLogs = from.Capabilities.SystemLogs
	into.Capabilities.SecurityEvents = from.Capabilities.SecurityEvents
	previousByPID := make(map[int]MonitorProcess, len(from.Processes))
	for _, proc := range from.Processes {
		previousByPID[proc.PID] = proc
	}
	for i := range into.Processes {
		previous, ok := previousByPID[into.Processes[i].PID]
		if !ok || (previous.Path != "" && into.Processes[i].Path != "" && previous.Path != into.Processes[i].Path) {
			continue
		}
		into.Processes[i].User = previous.User
		into.Processes[i].StartTime = previous.StartTime
		into.Processes[i].Version = previous.Version
		into.Processes[i].Publisher = previous.Publisher
		into.Processes[i].Signature = previous.Signature
	}
}

func applyDeepSnapshot(snapshot *MonitorSnapshot, deep MonitorDeepSnapshot) {
	if snapshot == nil {
		return
	}
	snapshot.StartupItems = deep.StartupItems
	snapshot.Services = deep.Services
	snapshot.Connections = deep.Connections
	snapshot.Logs = deep.Logs
	snapshot.Security = deep.Security
	snapshot.SystemDetails = deep.SystemDetails
	snapshot.CPUDetail = cloneMonitorCPUDetail(deep.CPUDetail)
	snapshot.GPUDetails = append([]MonitorGPUDetail(nil), deep.GPUDetails...)
	snapshot.MemoryDetail = cloneMonitorMemoryDetail(deep.MemoryDetail)
	snapshot.DiskDetails = append([]MonitorDiskDetail(nil), deep.DiskDetails...)
	snapshot.DeepCapturedAt = deep.CapturedAt
	snapshot.Capabilities.StartupItems = deep.StartupSupported
	snapshot.Capabilities.Services = deep.ServicesSupported
	snapshot.Capabilities.Connections = deep.ConnectionsSupported
	snapshot.Capabilities.SystemLogs = deep.LogsSupported
	snapshot.Capabilities.SecurityEvents = deep.SecuritySupported
	for i := range snapshot.Processes {
		if detail, ok := deep.ProcessDetails[snapshot.Processes[i].PID]; ok {
			if detail.User != "" {
				snapshot.Processes[i].User = detail.User
			}
			if detail.StartTime != "" {
				snapshot.Processes[i].StartTime = detail.StartTime
			}
			if detail.Version != "" {
				snapshot.Processes[i].Version = detail.Version
			}
			if detail.Publisher != "" {
				snapshot.Processes[i].Publisher = detail.Publisher
			}
			if detail.Signature != "" {
				snapshot.Processes[i].Signature = detail.Signature
			}
		}
	}
}

func cloneMonitorCPUDetail(detail *MonitorCPUDetail) *MonitorCPUDetail {
	if detail == nil {
		return nil
	}
	cloned := *detail
	cloned.Cores = append([]MonitorCPUCoreDetail(nil), detail.Cores...)
	return &cloned
}

func cloneMonitorMemoryDetail(detail *MonitorMemoryDetail) *MonitorMemoryDetail {
	if detail == nil {
		return nil
	}
	cloned := *detail
	return &cloned
}

func (m *monitorManager) enrichPhase2Snapshot(now time.Time, mode string, snapshot *MonitorSnapshot) {
	if m == nil || snapshot == nil || m.phase2 == nil {
		return
	}
	m.mu.RLock()
	previous := m.snapshot
	m.mu.RUnlock()
	copyDeepFields(previous, snapshot)
	if !m.phase2.beginDeepCollection(now, mode) {
		return
	}
	deepCollector, ok := m.collector.(monitorPlatformDeepCollector)
	if !ok {
		m.phase2.finishDeepCollection()
		return
	}
	processes := append([]MonitorProcess(nil), snapshot.Processes...)
	go m.collectPhase2Deep(now, deepCollector, processes)
}

// collectPhase2Deep deliberately runs outside the fast telemetry loop. Deep
// inventory commands (service lists, event logs, signatures, startup items)
// can take noticeably longer than a CPU/RAM sample, especially on Windows.
// Keeping them asynchronous prevents the user's 2-second Active polling
// choice from stalling while those slower OS queries complete.
func (m *monitorManager) collectPhase2Deep(now time.Time, collector monitorPlatformDeepCollector, processes []MonitorProcess) {
	defer m.phase2.finishDeepCollection()
	deep, err := collector.CollectDeep(now, processes)
	select {
	case <-m.stop:
		return
	default:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		message := strings.TrimSpace(err.Error())
		if message != "" {
			m.appendEventLocked("warning", "collector", "Deep collector warning: "+message)
		}
		return
	}
	previous := m.snapshot
	next := m.snapshot
	applyDeepSnapshot(&next, deep)
	m.detectDeepEventsLocked(previous, next)
	m.snapshot = next
}

func (m *monitorManager) addPhase2TelemetryLocked(now time.Time, snapshot MonitorSnapshot) {
	if m.phase2 == nil {
		return
	}
	p := m.phase2
	p.mu.Lock()
	bucketStart := monitorBucketStart(now)
	if !p.bucket.start.IsZero() && !p.bucket.start.Equal(bucketStart) {
		old := p.bucket
		p.bucket = monitorTelemetryBucketState{}
		p.mu.Unlock()
		if summary, ok := old.summary(old.start.Add(monitorHistoryBucket)); ok {
			p.persistTelemetry(summary)
		}
		p.mu.Lock()
	}
	p.bucket.add(now, snapshot)
	p.mu.Unlock()
}

func (m *monitorManager) flushPhase2TelemetryLocked(now time.Time) {
	if m == nil || m.phase2 == nil {
		return
	}
	p := m.phase2
	p.mu.Lock()
	old := p.bucket
	p.bucket = monitorTelemetryBucketState{}
	p.mu.Unlock()
	if summary, ok := old.summary(now); ok {
		p.persistTelemetry(summary)
	}
}

func processEventEligible(proc MonitorProcess) bool {
	if proc.PID <= 0 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(proc.Name))
	if name == "" {
		return false
	}
	if proc.Path == "" && (strings.HasPrefix(name, "kworker") || strings.HasPrefix(name, "migration/") || strings.HasPrefix(name, "cpuhp/") || strings.HasPrefix(name, "idle_inject/") || strings.HasPrefix(name, "irq/")) {
		return false
	}
	return true
}

func processMap(processes []MonitorProcess) map[int]MonitorProcess {
	out := make(map[int]MonitorProcess, len(processes))
	for _, proc := range processes {
		if processEventEligible(proc) {
			out[proc.PID] = proc
		}
	}
	return out
}

func startupKey(item MonitorStartupItem) string {
	return strings.ToLower(strings.TrimSpace(item.Source + "|" + item.Name + "|" + item.Command + "|" + item.Path))
}

func serviceKey(item MonitorService) string {
	return strings.ToLower(strings.TrimSpace(item.Name))
}

func connectionKey(item MonitorNetworkConnection) string {
	return strings.ToLower(fmt.Sprintf("%s|%s|%d|%s|%d|%d", item.Protocol, item.LocalAddress, item.LocalPort, item.RemoteAddress, item.RemotePort, item.PID))
}

func recordKey(timeValue, source, id, message string) string {
	return strings.ToLower(strings.TrimSpace(timeValue + "|" + source + "|" + id + "|" + message))
}

func isListeningConnection(item MonitorNetworkConnection) bool {
	state := strings.ToLower(strings.TrimSpace(item.State))
	return strings.Contains(state, "listen") || (strings.EqualFold(item.Protocol, "udp") && item.RemotePort == 0)
}

func (m *monitorManager) detectProcessEventsLocked(previous, next MonitorSnapshot) {
	if previous.Timestamp == "" {
		return
	}
	oldMap := processMap(previous.Processes)
	newMap := processMap(next.Processes)
	started := make([]MonitorProcess, 0)
	stopped := make([]MonitorProcess, 0)
	for pid, proc := range newMap {
		if _, ok := oldMap[pid]; !ok {
			started = append(started, proc)
		}
	}
	for pid, proc := range oldMap {
		if _, ok := newMap[pid]; !ok {
			stopped = append(stopped, proc)
		}
	}
	sort.Slice(started, func(i, j int) bool { return started[i].PID < started[j].PID })
	sort.Slice(stopped, func(i, j int) bool { return stopped[i].PID < stopped[j].PID })
	const limit = 8
	for i, proc := range started {
		if i >= limit {
			m.appendEventLocked("info", "process", fmt.Sprintf("%d additional processes started.", len(started)-limit))
			break
		}
		m.appendEventLocked("info", "process", fmt.Sprintf("Process started: %s (PID %d).", proc.Name, proc.PID))
	}
	for i, proc := range stopped {
		if i >= limit {
			m.appendEventLocked("info", "process", fmt.Sprintf("%d additional processes stopped.", len(stopped)-limit))
			break
		}
		m.appendEventLocked("info", "process", fmt.Sprintf("Process stopped: %s (PID %d).", proc.Name, proc.PID))
	}
}

func (m *monitorManager) detectDeepEventsLocked(previous, next MonitorSnapshot) {
	if previous.DeepCapturedAt == "" || next.DeepCapturedAt == "" || previous.DeepCapturedAt == next.DeepCapturedAt {
		return
	}
	oldStartup := map[string]MonitorStartupItem{}
	for _, item := range previous.StartupItems {
		oldStartup[startupKey(item)] = item
	}
	for _, item := range next.StartupItems {
		if _, ok := oldStartup[startupKey(item)]; !ok {
			m.appendEventLocked("notice", "startup", fmt.Sprintf("New startup item detected: %s.", item.Name))
		}
	}
	oldServices := map[string]MonitorService{}
	for _, service := range previous.Services {
		oldServices[serviceKey(service)] = service
	}
	for _, service := range next.Services {
		if old, ok := oldServices[serviceKey(service)]; ok && old.State != "" && service.State != "" && !strings.EqualFold(old.State, service.State) {
			m.appendEventLocked("notice", "service", fmt.Sprintf("Service %s changed state: %s → %s.", service.Name, old.State, service.State))
		}
	}
	oldListeners := map[string]struct{}{}
	for _, conn := range previous.Connections {
		if isListeningConnection(conn) {
			oldListeners[connectionKey(conn)] = struct{}{}
		}
	}
	newCount := 0
	for _, conn := range next.Connections {
		if !isListeningConnection(conn) {
			continue
		}
		if _, ok := oldListeners[connectionKey(conn)]; ok {
			continue
		}
		newCount++
		if newCount <= 6 {
			owner := conn.Process
			if owner == "" && conn.PID > 0 {
				owner = fmt.Sprintf("PID %d", conn.PID)
			}
			if owner == "" {
				owner = "unknown process"
			}
			m.appendEventLocked("notice", "network", fmt.Sprintf("New listening endpoint: %s %s:%d (%s).", strings.ToUpper(conn.Protocol), conn.LocalAddress, conn.LocalPort, owner))
		}
	}
	if newCount > 6 {
		m.appendEventLocked("info", "network", fmt.Sprintf("%d additional listening endpoints appeared.", newCount-6))
	}
	oldLogs := map[string]struct{}{}
	for _, record := range previous.Logs {
		oldLogs[recordKey(record.Time, record.Source, record.EventID, record.Message)] = struct{}{}
	}
	for _, record := range next.Logs {
		if _, ok := oldLogs[recordKey(record.Time, record.Source, record.EventID, record.Message)]; ok {
			continue
		}
		level := strings.ToLower(record.Level)
		if level == "error" || level == "critical" || level == "warning" {
			m.appendEventLocked("warning", "system", fmt.Sprintf("%s: %s", strings.TrimSpace(record.Source), strings.TrimSpace(record.Message)))
		}
	}
	oldSecurity := map[string]struct{}{}
	for _, record := range previous.Security {
		oldSecurity[recordKey(record.Time, record.Source, record.EventID, record.Message)] = struct{}{}
	}
	for _, record := range next.Security {
		if _, ok := oldSecurity[recordKey(record.Time, record.Source, record.EventID, record.Message)]; ok {
			continue
		}
		m.appendEventLocked("warning", "security", fmt.Sprintf("Security tool event from %s: %s", record.Source, record.Message))
	}
}

func monitorBand(value *float64, first, second float64) int {
	if value == nil {
		return 0
	}
	if *value >= second {
		return 2
	}
	if *value >= first {
		return 1
	}
	return 0
}

func (m *monitorManager) detectThresholdEventsLocked(next MonitorSnapshot) {
	if m.phase2 == nil {
		return
	}
	p := m.phase2
	p.mu.Lock()
	cpuTempBand := monitorBand(next.CPUTemperatureC, 80, 90)
	gpuTempBand := monitorBand(next.GPUTemperatureC, 80, 90)
	cpuLoadBand := monitorBand(next.CPUPercent, 90, 98)
	memoryBand := monitorBand(next.MemoryPercent, 90, 97)
	oldCPU, oldGPU, oldLoad, oldMem := p.cpuTempBand, p.gpuTempBand, p.cpuLoadBand, p.memoryBand
	p.cpuTempBand, p.gpuTempBand, p.cpuLoadBand, p.memoryBand = cpuTempBand, gpuTempBand, cpuLoadBand, memoryBand
	p.mu.Unlock()
	if cpuTempBand > oldCPU && next.CPUTemperatureC != nil {
		m.appendEventLocked("warning", "thermal", fmt.Sprintf("CPU temperature crossed %.0f°C.", *next.CPUTemperatureC))
	}
	if gpuTempBand > oldGPU && next.GPUTemperatureC != nil {
		m.appendEventLocked("warning", "thermal", fmt.Sprintf("GPU temperature crossed %.0f°C.", *next.GPUTemperatureC))
	}
	if cpuLoadBand > oldLoad && next.CPUPercent != nil {
		m.appendEventLocked("notice", "performance", fmt.Sprintf("CPU utilization reached %.0f%%.", *next.CPUPercent))
	}
	if memoryBand > oldMem && next.MemoryPercent != nil {
		m.appendEventLocked("notice", "performance", fmt.Sprintf("Memory utilization reached %.0f%%.", *next.MemoryPercent))
	}
}

func (m *monitorManager) applyPhase2SnapshotLocked(now time.Time, next MonitorSnapshot) {
	if m == nil || m.phase2 == nil {
		return
	}
	previous := m.snapshot
	m.addPhase2TelemetryLocked(now, next)
	m.detectProcessEventsLocked(previous, next)
	m.detectDeepEventsLocked(previous, next)
	m.detectThresholdEventsLocked(next)
}
