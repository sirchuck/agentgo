package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	monitorDefaultActivePollSeconds   = 2
	monitorDefaultPassivePollSeconds  = 10
	monitorDefaultHistoryMaxMB        = 10
	monitorDefaultScheduledRunLimit   = 100
	monitorDefaultVirusTotalAPIKeyEnv = "VIRUSTOTAL_API_KEY"
	monitorDefaultThermalCardView     = "overview"
	monitorDefaultQuickCard2          = "top-load"
	monitorDefaultQuickCard3          = "network-ports"
	monitorDefaultQuickCard4          = "security"
	monitorDisconnectGrace            = 120 * time.Second
	monitorMaxEvents                  = 240
)

var monitorActivePollPresets = map[int]struct{}{2: {}, 5: {}, 10: {}}
var monitorPassivePollPresets = map[int]struct{}{5: {}, 10: {}, 20: {}}
var monitorHistoryMaxPresets = map[int]struct{}{5: {}, 10: {}, 20: {}, 50: {}}
var monitorThermalCardViews = map[string]struct{}{
	"overview": {}, "cpu": {}, "gpu": {}, "storage": {}, "fans": {}, "thermal-state": {},
}
var monitorQuickCardTypes = map[string]struct{}{
	"top-load": {}, "security": {}, "network-ports": {}, "storage-health": {}, "gpu-vram": {}, "system-uptime": {},
}
var monitorEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type MonitorConfig struct {
	Enabled             bool   `json:"enabled"`
	ActivePollSeconds   int    `json:"active_poll_seconds"`
	PassivePollSeconds  int    `json:"passive_poll_seconds"`
	HistoryMaxMB        int    `json:"history_max_mb"`
	AIModelID           string `json:"ai_model_id,omitempty"`
	AIReportSchedule    string `json:"ai_report_schedule,omitempty"`
	AIReportCron        string `json:"ai_report_cron,omitempty"`
	AIScheduledRunLimit int    `json:"ai_scheduled_run_limit,omitempty"`
	AIMemoryMaxKB       int    `json:"ai_memory_max_kb,omitempty"`
	VirusTotalAPIKeyEnv string `json:"virustotal_api_key_env,omitempty"`
	ThermalCardView     string `json:"thermal_card_view,omitempty"`
	QuickCard2          string `json:"quick_card_2,omitempty"`
	QuickCard3          string `json:"quick_card_3,omitempty"`
	QuickCard4          string `json:"quick_card_4,omitempty"`
}

func normalizeMonitorConfig(cfg MonitorConfig) MonitorConfig {
	if _, ok := monitorActivePollPresets[cfg.ActivePollSeconds]; !ok {
		cfg.ActivePollSeconds = monitorDefaultActivePollSeconds
	}
	if _, ok := monitorPassivePollPresets[cfg.PassivePollSeconds]; !ok {
		cfg.PassivePollSeconds = monitorDefaultPassivePollSeconds
	}
	if _, ok := monitorHistoryMaxPresets[cfg.HistoryMaxMB]; !ok {
		cfg.HistoryMaxMB = monitorDefaultHistoryMaxMB
	}
	cfg.AIModelID = strings.TrimSpace(cfg.AIModelID)
	cfg.AIReportSchedule = normalizeMonitorAISchedule(cfg.AIReportSchedule)
	cfg.AIReportCron = strings.TrimSpace(cfg.AIReportCron)
	if cfg.AIScheduledRunLimit < 100 || cfg.AIScheduledRunLimit > 1000 {
		cfg.AIScheduledRunLimit = monitorDefaultScheduledRunLimit
	}
	if _, ok := monitorAIMemoryMaxPresets[cfg.AIMemoryMaxKB]; !ok {
		cfg.AIMemoryMaxKB = monitorDefaultAIMemoryMaxKB
	}
	cfg.VirusTotalAPIKeyEnv = strings.TrimSpace(cfg.VirusTotalAPIKeyEnv)
	if cfg.VirusTotalAPIKeyEnv == "" {
		cfg.VirusTotalAPIKeyEnv = monitorDefaultVirusTotalAPIKeyEnv
	}
	cfg.ThermalCardView = normalizeMonitorThermalCardView(cfg.ThermalCardView)
	cfg.QuickCard2, cfg.QuickCard3, cfg.QuickCard4 = normalizeMonitorQuickCards(cfg.QuickCard2, cfg.QuickCard3, cfg.QuickCard4)
	return cfg
}

func normalizeMonitorThermalCardView(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := monitorThermalCardViews[value]; ok {
		return value
	}
	return monitorDefaultThermalCardView
}

func normalizeMonitorQuickCardType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := monitorQuickCardTypes[value]; ok {
		return value
	}
	return ""
}

func normalizeMonitorQuickCards(card2, card3, card4 string) (string, string, string) {
	requested := []string{normalizeMonitorQuickCardType(card2), normalizeMonitorQuickCardType(card3), normalizeMonitorQuickCardType(card4)}
	defaults := []string{monitorDefaultQuickCard2, monitorDefaultQuickCard3, monitorDefaultQuickCard4, "storage-health", "gpu-vram", "system-uptime"}
	used := map[string]struct{}{}
	out := make([]string, 3)
	for i := range out {
		if requested[i] != "" {
			if _, exists := used[requested[i]]; !exists {
				out[i] = requested[i]
				used[out[i]] = struct{}{}
			}
		}
		if out[i] != "" {
			continue
		}
		for _, candidate := range defaults {
			if _, exists := used[candidate]; exists {
				continue
			}
			out[i] = candidate
			used[candidate] = struct{}{}
			break
		}
	}
	return out[0], out[1], out[2]
}

func validateMonitorConfig(cfg MonitorConfig) error {
	if _, ok := monitorActivePollPresets[cfg.ActivePollSeconds]; !ok {
		return errors.New("active polling must be 2, 5, or 10 seconds")
	}
	if _, ok := monitorPassivePollPresets[cfg.PassivePollSeconds]; !ok {
		return errors.New("passive polling must be 5, 10, or 20 seconds")
	}
	if cfg.HistoryMaxMB != 0 {
		if _, ok := monitorHistoryMaxPresets[cfg.HistoryMaxMB]; !ok {
			return errors.New("history limit must be 5, 10, 20, or 50 MB")
		}
	}
	if cfg.AIScheduledRunLimit != 0 && (cfg.AIScheduledRunLimit < 100 || cfg.AIScheduledRunLimit > 1000) {
		return errors.New("scheduled AI run limit must be between 100 and 1000 per session")
	}
	if cfg.AIMemoryMaxKB != 0 {
		if _, ok := monitorAIMemoryMaxPresets[cfg.AIMemoryMaxKB]; !ok {
			return errors.New("AI memory limit must be 20, 50, or 100 KB")
		}
	}
	if envName := strings.TrimSpace(cfg.VirusTotalAPIKeyEnv); envName != "" && !monitorEnvNamePattern.MatchString(envName) {
		return errors.New("VirusTotal API key environment variable name is invalid")
	}
	if value := strings.ToLower(strings.TrimSpace(cfg.ThermalCardView)); value != "" {
		if _, ok := monitorThermalCardViews[value]; !ok {
			return errors.New("thermal card view is invalid")
		}
	}
	seenQuickCards := map[string]struct{}{}
	for _, value := range []string{cfg.QuickCard2, cfg.QuickCard3, cfg.QuickCard4} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := monitorQuickCardTypes[value]; !ok {
			return errors.New("quick card selection is invalid")
		}
		if _, exists := seenQuickCards[value]; exists {
			return errors.New("quick card selections must be unique")
		}
		seenQuickCards[value] = struct{}{}
	}
	schedule := normalizeMonitorAISchedule(cfg.AIReportSchedule)
	if strings.TrimSpace(cfg.AIReportSchedule) != "" && schedule == "off" && !strings.EqualFold(strings.TrimSpace(cfg.AIReportSchedule), "off") {
		return errors.New("AI report schedule must be off, 15m, 30m, 1h, 6h, 24h, or custom")
	}
	if schedule == "custom" {
		if _, err := parseMonitorCron(cfg.AIReportCron); err != nil {
			return fmt.Errorf("invalid custom AI report cron: %w", err)
		}
	}
	return nil
}

type MonitorCapabilities struct {
	CPUUsage       bool `json:"cpu_usage"`
	GPUUsage       bool `json:"gpu_usage"`
	Memory         bool `json:"memory"`
	DiskUsage      bool `json:"disk_usage"`
	DiskActivity   bool `json:"disk_activity"`
	Network        bool `json:"network"`
	Temperatures   bool `json:"temperatures"`
	Fans           bool `json:"fans"`
	Processes      bool `json:"processes"`
	ThermalState   bool `json:"thermal_state"`
	ProcessGPU     bool `json:"process_gpu"`
	ProcessFile    bool `json:"process_file"`
	ProcessDetails bool `json:"process_details"`
	StartupItems   bool `json:"startup_items"`
	Services       bool `json:"services"`
	Connections    bool `json:"connections"`
	SystemLogs     bool `json:"system_logs"`
	SecurityEvents bool `json:"security_events"`
}

type MonitorSystemInfo struct {
	OS                  string   `json:"os"`
	OSVersion           string   `json:"os_version,omitempty"`
	Architecture        string   `json:"architecture"`
	Hostname            string   `json:"hostname,omitempty"`
	CPUModel            string   `json:"cpu_model,omitempty"`
	GPUModel            string   `json:"gpu_model,omitempty"`
	TotalMemoryBytes    uint64   `json:"total_memory_bytes,omitempty"`
	StorageTotalBytes   uint64   `json:"storage_total_bytes,omitempty"`
	StorageFreeBytes    uint64   `json:"storage_free_bytes,omitempty"`
	UptimeSeconds       uint64   `json:"uptime_seconds,omitempty"`
	PlatformDescription string   `json:"platform_description,omitempty"`
	KernelVersion       string   `json:"kernel_version,omitempty"`
	PhysicalCPUCores    int      `json:"physical_cpu_cores,omitempty"`
	LogicalCPUs         int      `json:"logical_cpus,omitempty"`
	NetworkAdapters     []string `json:"network_adapters,omitempty"`
}

type MonitorThermalSensor struct {
	Name         string   `json:"name"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	Kind         string   `json:"kind,omitempty"`
}

type MonitorFanSensor struct {
	Name string  `json:"name"`
	RPM  float64 `json:"rpm"`
}

type MonitorProcess struct {
	PID           int      `json:"pid"`
	ParentPID     int      `json:"parent_pid,omitempty"`
	Name          string   `json:"name"`
	Path          string   `json:"path,omitempty"`
	FileSizeBytes int64    `json:"file_size_bytes,omitempty"`
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	GPUPercent    *float64 `json:"gpu_percent,omitempty"`
	MemoryBytes   uint64   `json:"memory_bytes,omitempty"`
	Status        string   `json:"status,omitempty"`
	StartTime     string   `json:"start_time,omitempty"`
	User          string   `json:"user,omitempty"`
	Version       string   `json:"version,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	Signature     string   `json:"signature,omitempty"`
}

type MonitorStartupItem struct {
	Name      string `json:"name"`
	Command   string `json:"command,omitempty"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"`
	Publisher string `json:"publisher,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

type MonitorService struct {
	Name        string `json:"name"`
	State       string `json:"state,omitempty"`
	StartupType string `json:"startup_type,omitempty"`
	PID         int    `json:"pid,omitempty"`
	Command     string `json:"command,omitempty"`
	User        string `json:"user,omitempty"`
	Description string `json:"description,omitempty"`
}

type MonitorNetworkConnection struct {
	Protocol      string `json:"protocol"`
	LocalAddress  string `json:"local_address,omitempty"`
	LocalPort     int    `json:"local_port,omitempty"`
	RemoteAddress string `json:"remote_address,omitempty"`
	RemotePort    int    `json:"remote_port,omitempty"`
	State         string `json:"state,omitempty"`
	PID           int    `json:"pid,omitempty"`
	Process       string `json:"process,omitempty"`
}

type MonitorLogRecord struct {
	Time    string `json:"time,omitempty"`
	Level   string `json:"level,omitempty"`
	Source  string `json:"source,omitempty"`
	EventID string `json:"event_id,omitempty"`
	Message string `json:"message"`
}

type MonitorSecurityRecord struct {
	Time     string `json:"time,omitempty"`
	Severity string `json:"severity,omitempty"`
	Source   string `json:"source,omitempty"`
	Type     string `json:"type,omitempty"`
	EventID  string `json:"event_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type MonitorInfoDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type MonitorCPUCoreDetail struct {
	Index           int      `json:"index"`
	Label           string   `json:"label,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	UsagePercent    *float64 `json:"usage_percent,omitempty"`
	FrequencyMHz    *float64 `json:"frequency_mhz,omitempty"`
	MaxFrequencyMHz *float64 `json:"max_frequency_mhz,omitempty"`
}

type MonitorCPUDetail struct {
	Model               string                 `json:"model,omitempty"`
	Architecture        string                 `json:"architecture,omitempty"`
	PhysicalCores       int                    `json:"physical_cores,omitempty"`
	LogicalCPUs         int                    `json:"logical_cpus,omitempty"`
	CurrentFrequencyMHz *float64               `json:"current_frequency_mhz,omitempty"`
	MaxFrequencyMHz     *float64               `json:"max_frequency_mhz,omitempty"`
	Cores               []MonitorCPUCoreDetail `json:"cores,omitempty"`
}

type MonitorGPUDetail struct {
	Index          int      `json:"index"`
	Name           string   `json:"name,omitempty"`
	Vendor         string   `json:"vendor,omitempty"`
	Driver         string   `json:"driver,omitempty"`
	UsagePercent   *float64 `json:"usage_percent,omitempty"`
	VRAMTotalBytes uint64   `json:"vram_total_bytes,omitempty"`
	VRAMUsedBytes  uint64   `json:"vram_used_bytes,omitempty"`
	VRAMFreeBytes  uint64   `json:"vram_free_bytes,omitempty"`
	TemperatureC   *float64 `json:"temperature_c,omitempty"`
	FanRPM         *float64 `json:"fan_rpm,omitempty"`
	FanPercent     *float64 `json:"fan_percent,omitempty"`
	CoreClockMHz   *float64 `json:"core_clock_mhz,omitempty"`
	MemoryClockMHz *float64 `json:"memory_clock_mhz,omitempty"`
}

type MonitorMemoryDetail struct {
	TotalBytes      uint64 `json:"total_bytes,omitempty"`
	UsedBytes       uint64 `json:"used_bytes,omitempty"`
	AvailableBytes  uint64 `json:"available_bytes,omitempty"`
	ActiveBytes     uint64 `json:"active_bytes,omitempty"`
	InactiveBytes   uint64 `json:"inactive_bytes,omitempty"`
	CachedBytes     uint64 `json:"cached_bytes,omitempty"`
	BuffersBytes    uint64 `json:"buffers_bytes,omitempty"`
	WiredBytes      uint64 `json:"wired_bytes,omitempty"`
	CompressedBytes uint64 `json:"compressed_bytes,omitempty"`
	SwapTotalBytes  uint64 `json:"swap_total_bytes,omitempty"`
	SwapUsedBytes   uint64 `json:"swap_used_bytes,omitempty"`
	SwapFreeBytes   uint64 `json:"swap_free_bytes,omitempty"`
}

type MonitorDiskDetail struct {
	Name         string   `json:"name,omitempty"`
	Device       string   `json:"device,omitempty"`
	MountPoint   string   `json:"mount_point,omitempty"`
	FileSystem   string   `json:"file_system,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Group        string   `json:"group,omitempty"`
	GroupKind    string   `json:"group_kind,omitempty"`
	Model        string   `json:"model,omitempty"`
	TotalBytes   uint64   `json:"total_bytes,omitempty"`
	UsedBytes    uint64   `json:"used_bytes,omitempty"`
	FreeBytes    uint64   `json:"free_bytes,omitempty"`
	UsagePercent *float64 `json:"usage_percent,omitempty"`
}

type MonitorDiskIO struct {
	Device           string   `json:"device,omitempty"`
	ReadBytesPerSec  *float64 `json:"read_bytes_per_sec,omitempty"`
	WriteBytesPerSec *float64 `json:"write_bytes_per_sec,omitempty"`
}

type MonitorProcessDetail struct {
	User      string `json:"user,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	Version   string `json:"version,omitempty"`
	Publisher string `json:"publisher,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type MonitorDeepSnapshot struct {
	CapturedAt           string                       `json:"captured_at,omitempty"`
	ProcessDetails       map[int]MonitorProcessDetail `json:"-"`
	StartupItems         []MonitorStartupItem         `json:"startup_items,omitempty"`
	Services             []MonitorService             `json:"services,omitempty"`
	Connections          []MonitorNetworkConnection   `json:"connections,omitempty"`
	Logs                 []MonitorLogRecord           `json:"logs,omitempty"`
	Security             []MonitorSecurityRecord      `json:"security,omitempty"`
	SystemDetails        []MonitorInfoDetail          `json:"system_details,omitempty"`
	CPUDetail            *MonitorCPUDetail            `json:"cpu_detail,omitempty"`
	GPUDetails           []MonitorGPUDetail           `json:"gpu_details,omitempty"`
	MemoryDetail         *MonitorMemoryDetail         `json:"memory_detail,omitempty"`
	DiskDetails          []MonitorDiskDetail          `json:"disk_details,omitempty"`
	StartupSupported     bool                         `json:"-"`
	ServicesSupported    bool                         `json:"-"`
	ConnectionsSupported bool                         `json:"-"`
	LogsSupported        bool                         `json:"-"`
	SecuritySupported    bool                         `json:"-"`
}

type MonitorSnapshot struct {
	Timestamp            string                     `json:"timestamp,omitempty"`
	System               MonitorSystemInfo          `json:"system"`
	Capabilities         MonitorCapabilities        `json:"capabilities"`
	CPUPercent           *float64                   `json:"cpu_percent,omitempty"`
	GPUPercent           *float64                   `json:"gpu_percent,omitempty"`
	MemoryUsedBytes      uint64                     `json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes     uint64                     `json:"memory_total_bytes,omitempty"`
	MemoryPercent        *float64                   `json:"memory_percent,omitempty"`
	DiskReadBytesPerSec  *float64                   `json:"disk_read_bytes_per_sec,omitempty"`
	DiskWriteBytesPerSec *float64                   `json:"disk_write_bytes_per_sec,omitempty"`
	DiskIO               []MonitorDiskIO            `json:"disk_io,omitempty"`
	DiskUsedBytes        uint64                     `json:"disk_used_bytes,omitempty"`
	DiskTotalBytes       uint64                     `json:"disk_total_bytes,omitempty"`
	DiskPercent          *float64                   `json:"disk_percent,omitempty"`
	NetworkRxBytesPerSec *float64                   `json:"network_rx_bytes_per_sec,omitempty"`
	NetworkTxBytesPerSec *float64                   `json:"network_tx_bytes_per_sec,omitempty"`
	CPUTemperatureC      *float64                   `json:"cpu_temperature_c,omitempty"`
	GPUTemperatureC      *float64                   `json:"gpu_temperature_c,omitempty"`
	ThermalState         string                     `json:"thermal_state,omitempty"`
	Thermals             []MonitorThermalSensor     `json:"thermals,omitempty"`
	Fans                 []MonitorFanSensor         `json:"fans,omitempty"`
	Processes            []MonitorProcess           `json:"processes,omitempty"`
	StartupItems         []MonitorStartupItem       `json:"startup_items,omitempty"`
	Services             []MonitorService           `json:"services,omitempty"`
	Connections          []MonitorNetworkConnection `json:"connections,omitempty"`
	Logs                 []MonitorLogRecord         `json:"logs,omitempty"`
	Security             []MonitorSecurityRecord    `json:"security,omitempty"`
	SystemDetails        []MonitorInfoDetail        `json:"system_details,omitempty"`
	CPUDetail            *MonitorCPUDetail          `json:"cpu_detail,omitempty"`
	GPUDetails           []MonitorGPUDetail         `json:"gpu_details,omitempty"`
	MemoryDetail         *MonitorMemoryDetail       `json:"memory_detail,omitempty"`
	DiskDetails          []MonitorDiskDetail        `json:"disk_details,omitempty"`
	DeepCapturedAt       string                     `json:"deep_captured_at,omitempty"`
	CollectorNote        string                     `json:"collector_note,omitempty"`
}

type MonitorEvent struct {
	ID      uint64 `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type monitorPlatformCollector interface {
	Collect(time.Time) (MonitorSnapshot, error)
	StaticInfo() (MonitorSystemInfo, MonitorCapabilities, error)
}

type monitorPlatformDeepCollector interface {
	CollectDeep(time.Time, []MonitorProcess) (MonitorDeepSnapshot, error)
}

type monitorClientPresence struct {
	LastSeen time.Time
	Active   bool
}

type monitorManager struct {
	mu          sync.RWMutex
	collectMu   sync.Mutex
	collector   monitorPlatformCollector
	settings    MonitorConfig
	clients     map[string]monitorClientPresence
	snapshot    MonitorSnapshot
	events      []MonitorEvent
	eventSeq    uint64
	lastPoll    time.Time
	lastMode    string
	lastError   string
	lastErrorAt time.Time
	wake        chan struct{}
	stop        chan struct{}
	stopOnce    sync.Once
	phase2      *monitorPhase2State
}

type monitorRuntimeState struct {
	Mode             string `json:"mode"`
	ConnectedClients int    `json:"connected_clients"`
	ActiveClients    int    `json:"active_clients"`
	LastPoll         string `json:"last_poll,omitempty"`
	GraceSeconds     int    `json:"disconnect_grace_seconds"`
}

type monitorStateResponse struct {
	OK       bool                      `json:"ok"`
	Settings MonitorConfig             `json:"settings"`
	Runtime  monitorRuntimeState       `json:"runtime"`
	Snapshot MonitorSnapshot           `json:"snapshot"`
	Events   []MonitorEvent            `json:"events"`
	History  []MonitorTelemetrySummary `json:"history,omitempty"`
	AI       *MonitorAISummary         `json:"ai,omitempty"`
}

func newMonitorManager(settings MonitorConfig) *monitorManager {
	return newMonitorManagerWithRoot(settings, "")
}

func newMonitorManagerWithRoot(settings MonitorConfig, root string) *monitorManager {
	settings = normalizeMonitorConfig(settings)
	// Monitor ON/OFF is session-only. Never resume monitoring just because the
	// previous AgentGO process exited while Monitor was enabled. Persisted
	// schedule/configuration values remain available, but every fresh process
	// requires an explicit user action before collection or scheduled AI runs.
	settings.Enabled = false
	m := &monitorManager{
		collector: newMonitorPlatformCollector(),
		settings:  settings,
		clients:   map[string]monitorClientPresence{},
		events:    []MonitorEvent{},
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		lastMode:  "off",
		phase2:    newMonitorPhase2State(root, settings.HistoryMaxMB),
	}
	if m.phase2 != nil {
		m.events = m.phase2.recentEvents()
		for _, event := range m.events {
			if event.ID > m.eventSeq {
				m.eventSeq = event.ID
			}
		}
	}
	return m
}

func (m *monitorManager) Start() {
	if m == nil {
		return
	}
	go m.loop()
}

func (m *monitorManager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.flushPhase2TelemetryLocked(time.Now())
		m.mu.Unlock()
		close(m.stop)
	})
}

func (m *monitorManager) signal() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *monitorManager) appendEventLocked(level, kind, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	m.eventSeq++
	event := MonitorEvent{
		ID:      m.eventSeq,
		Time:    time.Now().Format(time.RFC3339),
		Level:   strings.TrimSpace(level),
		Kind:    strings.TrimSpace(kind),
		Message: message,
	}
	m.events = append(m.events, event)
	if m.phase2 != nil {
		m.phase2.persistEvent(event)
	}
	if len(m.events) > monitorMaxEvents {
		m.events = append([]MonitorEvent{}, m.events[len(m.events)-monitorMaxEvents:]...)
	}
}

func (m *monitorManager) modeLocked(now time.Time) (string, int, int) {
	for id, client := range m.clients {
		if now.Sub(client.LastSeen) > monitorDisconnectGrace {
			delete(m.clients, id)
		}
	}
	connected := len(m.clients)
	active := 0
	for _, client := range m.clients {
		if client.Active {
			active++
		}
	}
	if !m.settings.Enabled {
		return "off", connected, active
	}
	if active > 0 {
		return "active", connected, active
	}
	if connected > 0 {
		return "passive", connected, active
	}
	return "disconnected", connected, active
}

func (m *monitorManager) intervalLocked(mode string) time.Duration {
	switch mode {
	case "active":
		return time.Duration(m.settings.ActivePollSeconds) * time.Second
	case "passive":
		return time.Duration(m.settings.PassivePollSeconds) * time.Second
	default:
		return 0
	}
}

func (m *monitorManager) noteModeTransitionLocked(mode string) {
	previous := m.lastMode
	if previous == mode {
		return
	}
	m.lastMode = mode
	if !m.settings.Enabled || mode == "off" {
		return
	}
	if mode == "disconnected" && previous != "off" && previous != "disconnected" {
		m.flushPhase2TelemetryLocked(time.Now())
		m.appendEventLocked("info", "monitor", "Browser disconnected; AgentGO Monitor paused.")
		return
	}
	if previous == "disconnected" && (mode == "active" || mode == "passive") {
		m.appendEventLocked("info", "monitor", "Browser reconnected; AgentGO Monitor resumed.")
	}
}

func (m *monitorManager) loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-m.wake:
		case <-m.stop:
			return
		}

		now := time.Now()
		m.mu.Lock()
		mode, _, _ := m.modeLocked(now)
		m.noteModeTransitionLocked(mode)
		interval := m.intervalLocked(mode)
		due := interval > 0 && (m.lastPoll.IsZero() || now.Sub(m.lastPoll) >= interval)
		if due {
			// Reserve the poll slot before collecting so a burst of wake signals cannot
			// start duplicate collectors while a platform query is still in flight.
			m.lastPoll = now
		}
		m.mu.Unlock()
		if !due {
			continue
		}

		snapshot, err := m.collect(now)
		if err == nil {
			m.enrichPhase2Snapshot(now, mode, &snapshot)
		}
		m.mu.Lock()
		if err != nil {
			message := err.Error()
			if message != m.lastError || now.Sub(m.lastErrorAt) >= 5*time.Minute {
				m.lastError = message
				m.lastErrorAt = now
				m.appendEventLocked("warning", "collector", "Collector warning: "+message)
			}
		} else {
			m.lastError = ""
			m.applyPhase2SnapshotLocked(now, snapshot)
			m.snapshot = snapshot
		}
		m.mu.Unlock()
	}
}

func (m *monitorManager) collect(now time.Time) (MonitorSnapshot, error) {
	m.collectMu.Lock()
	defer m.collectMu.Unlock()
	if m.collector == nil {
		return MonitorSnapshot{}, errors.New("system collector unavailable")
	}
	return m.collector.Collect(now)
}

func (m *monitorManager) ensureStatic() {
	if m == nil {
		return
	}
	m.mu.RLock()
	hasStatic := strings.TrimSpace(m.snapshot.System.OS) != ""
	m.mu.RUnlock()
	if hasStatic {
		return
	}
	m.collectMu.Lock()
	info, caps, err := m.collector.StaticInfo()
	m.collectMu.Unlock()
	m.mu.Lock()
	if err != nil {
		if strings.TrimSpace(m.snapshot.CollectorNote) == "" {
			m.snapshot.CollectorNote = err.Error()
		}
	} else {
		m.snapshot.System = info
		m.snapshot.Capabilities = caps
	}
	m.mu.Unlock()
}

func (m *monitorManager) SettingsSnapshot() MonitorConfig {
	if m == nil {
		return normalizeMonitorConfig(MonitorConfig{})
	}
	m.mu.RLock()
	cfg := m.settings
	m.mu.RUnlock()
	return normalizeMonitorConfig(cfg)
}

func (m *monitorManager) UpdateSettings(next MonitorConfig) {
	if m == nil {
		return
	}
	next = normalizeMonitorConfig(next)
	m.mu.Lock()
	previous := m.settings
	m.settings = next
	if m.phase2 != nil {
		m.phase2.setMaxMB(next.HistoryMaxMB)
	}
	if previous.Enabled != next.Enabled {
		if next.Enabled {
			m.appendEventLocked("system", "monitor", "AgentGO Monitor started.")
		} else {
			m.flushPhase2TelemetryLocked(time.Now())
			m.appendEventLocked("system", "monitor", "AgentGO Monitor stopped.")
		}
	} else if previous.ActivePollSeconds != next.ActivePollSeconds || previous.PassivePollSeconds != next.PassivePollSeconds || previous.HistoryMaxMB != next.HistoryMaxMB {
		m.appendEventLocked("info", "settings", fmt.Sprintf("Monitor settings updated: active %ds, passive %ds, history %d MB.", next.ActivePollSeconds, next.PassivePollSeconds, next.HistoryMaxMB))
	}
	// Poll immediately after turning on or changing a cadence.
	m.lastPoll = time.Time{}
	m.mu.Unlock()
	m.signal()
}

func (m *monitorManager) UpdatePresence(clientID string, active bool) {
	if m == nil {
		return
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return
	}
	m.mu.Lock()
	m.clients[clientID] = monitorClientPresence{LastSeen: time.Now(), Active: active}
	m.mu.Unlock()
	m.signal()
}

func (m *monitorManager) Disconnect(clientID string) {
	if m == nil {
		return
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return
	}
	// Keep the last-seen record for the disconnect grace window instead of
	// deleting it immediately. This prevents normal refresh/navigation from
	// briefly stopping Monitor and lets the backend pause only after the user
	// has genuinely left AgentGO.
	m.mu.Lock()
	m.clients[clientID] = monitorClientPresence{LastSeen: time.Now(), Active: false}
	m.mu.Unlock()
	m.signal()
}

func (m *monitorManager) ClearEvents() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.events = []MonitorEvent{}
	if m.phase2 != nil {
		m.phase2.clearPersistedEvents()
	}
	m.mu.Unlock()
}

func (m *monitorManager) RecordEvent(level, kind, message string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.appendEventLocked(level, kind, message)
	m.mu.Unlock()
}

func (m *monitorManager) State() monitorStateResponse {
	if m == nil {
		return monitorStateResponse{OK: false}
	}
	m.ensureStatic()
	now := time.Now()
	m.mu.Lock()
	mode, connected, active := m.modeLocked(now)
	m.noteModeTransitionLocked(mode)
	settings := m.settings
	snapshot := m.snapshot
	events := append([]MonitorEvent{}, m.events...)
	history := m.phase2RecentHistoryLocked()
	lastPoll := ""
	if !m.lastPoll.IsZero() {
		lastPoll = m.lastPoll.Format(time.RFC3339)
	}
	m.mu.Unlock()
	return monitorStateResponse{
		OK:       true,
		Settings: settings,
		Runtime: monitorRuntimeState{
			Mode:             mode,
			ConnectedClients: connected,
			ActiveClients:    active,
			LastPoll:         lastPoll,
			GraceSeconds:     int(monitorDisconnectGrace / time.Second),
		},
		Snapshot: snapshot,
		Events:   events,
		History:  history,
	}
}

func sortMonitorProcesses(processes []MonitorProcess) {
	sort.SliceStable(processes, func(i, j int) bool {
		left := strings.ToLower(processes[i].Name)
		right := strings.ToLower(processes[j].Name)
		if left == right {
			return processes[i].PID < processes[j].PID
		}
		return left < right
	})
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func monitorFloat(v float64) *float64 {
	value := v
	return &value
}

func monitorPercent(used, total uint64) *float64 {
	if total == 0 {
		return nil
	}
	return monitorFloat(clampPercent(float64(used) * 100 / float64(total)))
}

func (a *App) saveMonitorConfig(next MonitorConfig) error {
	original := next
	if err := validateMonitorConfig(original); err != nil {
		return err
	}
	next = normalizeMonitorConfig(next)
	persisted := next
	// Enabled is deliberately runtime-only. Keep the on-disk value false so
	// unrelated config saves can never turn Monitor back on after a restart.
	persisted.Enabled = false
	if original.HistoryMaxMB == 0 {
		persisted.HistoryMaxMB = 0
	}
	// Preserve zero-value Phase 1/2 config fields on disk when older callers do
	// not send the new Phase 3 AI settings. Runtime snapshots still normalize
	// these fields to their safe defaults.
	if strings.TrimSpace(original.AIReportSchedule) == "" {
		persisted.AIReportSchedule = ""
		persisted.AIReportCron = ""
	}
	if original.AIScheduledRunLimit == 0 {
		persisted.AIScheduledRunLimit = 0
	}
	if original.AIMemoryMaxKB == 0 {
		persisted.AIMemoryMaxKB = 0
	}
	if strings.TrimSpace(original.VirusTotalAPIKeyEnv) == "" {
		persisted.VirusTotalAPIKeyEnv = ""
	}
	if strings.TrimSpace(original.ThermalCardView) == "" {
		persisted.ThermalCardView = ""
	}
	if strings.TrimSpace(original.QuickCard2) == "" {
		persisted.QuickCard2 = ""
	}
	if strings.TrimSpace(original.QuickCard3) == "" {
		persisted.QuickCard3 = ""
	}
	if strings.TrimSpace(original.QuickCard4) == "" {
		persisted.QuickCard4 = ""
	}
	previousMonitor := a.monitorConfigSnapshot()
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	cfg.Monitor = persisted
	applyConfigDefaults(&cfg)
	if original.HistoryMaxMB == 0 {
		cfg.Monitor.HistoryMaxMB = 0
	}
	if strings.TrimSpace(original.AIReportSchedule) == "" {
		cfg.Monitor.AIReportSchedule = ""
		cfg.Monitor.AIReportCron = ""
	}
	if original.AIScheduledRunLimit == 0 {
		cfg.Monitor.AIScheduledRunLimit = 0
	}
	if original.AIMemoryMaxKB == 0 {
		cfg.Monitor.AIMemoryMaxKB = 0
	}
	if strings.TrimSpace(original.VirusTotalAPIKeyEnv) == "" {
		cfg.Monitor.VirusTotalAPIKeyEnv = ""
	}
	if strings.TrimSpace(original.ThermalCardView) == "" {
		cfg.Monitor.ThermalCardView = ""
	}
	if strings.TrimSpace(original.QuickCard2) == "" {
		cfg.Monitor.QuickCard2 = ""
	}
	if strings.TrimSpace(original.QuickCard3) == "" {
		cfg.Monitor.QuickCard3 = ""
	}
	if strings.TrimSpace(original.QuickCard4) == "" {
		cfg.Monitor.QuickCard4 = ""
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWriteFile(a.configPath, data, 0o644); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Monitor = persisted
	a.mu.Unlock()
	if a.monitor != nil {
		a.monitor.UpdateSettings(next)
	}
	if a.monitorAI != nil {
		a.monitorAI.OnSettingsUpdated(previousMonitor, next)
	}
	return nil
}

func (a *App) handleMonitorState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.monitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "monitor unavailable"})
		return
	}
	state := a.monitor.State()
	if a.monitorAI != nil {
		cfg := a.monitorConfigSnapshot()
		summary := a.monitorAI.Summary(cfg)
		state.AI = &summary
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleMonitorSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req MonitorConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := validateMonitorConfig(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.saveMonitorConfig(req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a.monitor.State())
}

type monitorPresenceRequest struct {
	ClientID string `json:"client_id"`
	Active   bool   `json:"active"`
}

func (a *App) handleMonitorPresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req monitorPresenceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ClientID) == "" {
		http.Error(w, "client_id is required", http.StatusBadRequest)
		return
	}
	if a.monitor != nil {
		a.monitor.UpdatePresence(req.ClientID, req.Active)
	}
	response := map[string]any{"ok": true}
	if a.monitorAI != nil {
		response["ai"] = a.monitorAI.Summary(a.monitorConfigSnapshot())
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleMonitorDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req monitorPresenceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if a.monitor != nil {
		a.monitor.Disconnect(req.ClientID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleMonitorClearEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.monitor != nil {
		a.monitor.ClearEvents()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
