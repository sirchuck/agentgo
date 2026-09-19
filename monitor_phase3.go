package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	monitorDefaultAIMemoryMaxKB = 20
	monitorAIInjectedMemoryMax  = 8 * 1024
	monitorAIConversationMax    = 220
	monitorAIConversationBytes  = 2 * 1024 * 1024
	monitorAISchedulerTick      = 15 * time.Second
	monitorAISystemPromptFile   = "monitor_ai.md"
)

var monitorAIMemoryMaxPresets = map[int]struct{}{20: {}, 50: {}, 100: {}}
var monitorAISchedulePresets = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

func normalizeMonitorAISchedule(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "15m", "30m", "1h", "6h", "24h", "custom":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "off"
	}
}

func monitorScheduleDescription(schedule, cronExpr string) string {
	schedule = normalizeMonitorAISchedule(schedule)
	switch schedule {
	case "15m":
		return "Every 15 minutes"
	case "30m":
		return "Every 30 minutes"
	case "1h":
		return "Every hour"
	case "6h":
		return "Every 6 hours"
	case "24h":
		return "Every 24 hours"
	case "custom":
		if desc, err := describeMonitorCron(cronExpr); err == nil {
			return desc
		}
		return "Custom cron"
	default:
		return "Off"
	}
}

func normalizeMonitorAIStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical":
		return "critical"
	case "warning", "warn":
		return "warning"
	case "notice":
		return "notice"
	default:
		return "normal"
	}
}

func monitorAIStatusRank(value string) int {
	switch normalizeMonitorAIStatus(value) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "notice":
		return 1
	default:
		return 0
	}
}

// monitorCronField implements the common five-field cron syntax AgentGO needs:
// *, comma-separated values, ranges, and /step suffixes. Names such as MON are
// intentionally not accepted so the UI can validate exactly what the backend runs.
type monitorCronField struct {
	min     int
	max     int
	allowed map[int]struct{}
}

func parseMonitorCronField(text string, min, max int) (monitorCronField, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return monitorCronField{}, errors.New("empty cron field")
	}
	field := monitorCronField{min: min, max: max, allowed: map[int]struct{}{}}
	addRange := func(start, end, step int) error {
		if start < min || start > max || end < min || end > max || start > end || step <= 0 {
			return errors.New("cron value out of range")
		}
		for value := start; value <= end; value += step {
			field.allowed[value] = struct{}{}
		}
		return nil
	}
	for _, rawPart := range strings.Split(text, ",") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return monitorCronField{}, errors.New("invalid empty cron list item")
		}
		base := part
		step := 1
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 {
				return monitorCronField{}, errors.New("invalid cron step")
			}
			base = strings.TrimSpace(pieces[0])
			parsedStep, err := strconv.Atoi(strings.TrimSpace(pieces[1]))
			if err != nil || parsedStep <= 0 {
				return monitorCronField{}, errors.New("invalid cron step")
			}
			step = parsedStep
		}
		switch {
		case base == "*":
			if err := addRange(min, max, step); err != nil {
				return monitorCronField{}, err
			}
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return monitorCronField{}, errors.New("invalid cron range")
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				return monitorCronField{}, errors.New("invalid cron range")
			}
			if err := addRange(start, end, step); err != nil {
				return monitorCronField{}, err
			}
		default:
			value, err := strconv.Atoi(base)
			if err != nil {
				return monitorCronField{}, errors.New("cron fields must use numbers, *, ranges, lists, or steps")
			}
			if err := addRange(value, value, step); err != nil {
				return monitorCronField{}, err
			}
		}
	}
	if len(field.allowed) == 0 {
		return monitorCronField{}, errors.New("cron field matches no values")
	}
	return field, nil
}

func (f monitorCronField) matches(value int) bool {
	_, ok := f.allowed[value]
	return ok
}

type monitorCronSpec struct {
	minute monitorCronField
	hour   monitorCronField
	day    monitorCronField
	month  monitorCronField
	week   monitorCronField
	raw    string
}

func parseMonitorCron(expr string) (monitorCronSpec, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return monitorCronSpec{}, errors.New("cron must contain exactly 5 fields: minute hour day month weekday")
	}
	minute, err := parseMonitorCronField(parts[0], 0, 59)
	if err != nil {
		return monitorCronSpec{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseMonitorCronField(parts[1], 0, 23)
	if err != nil {
		return monitorCronSpec{}, fmt.Errorf("hour: %w", err)
	}
	day, err := parseMonitorCronField(parts[2], 1, 31)
	if err != nil {
		return monitorCronSpec{}, fmt.Errorf("day: %w", err)
	}
	month, err := parseMonitorCronField(parts[3], 1, 12)
	if err != nil {
		return monitorCronSpec{}, fmt.Errorf("month: %w", err)
	}
	week, err := parseMonitorCronField(parts[4], 0, 6)
	if err != nil {
		return monitorCronSpec{}, fmt.Errorf("weekday: %w", err)
	}
	return monitorCronSpec{minute: minute, hour: hour, day: day, month: month, week: week, raw: strings.Join(parts, " ")}, nil
}

func (c monitorCronSpec) matches(t time.Time) bool {
	return c.minute.matches(t.Minute()) && c.hour.matches(t.Hour()) && c.day.matches(t.Day()) && c.month.matches(int(t.Month())) && c.week.matches(int(t.Weekday()))
}

func describeMonitorCron(expr string) (string, error) {
	spec, err := parseMonitorCron(expr)
	if err != nil {
		return "", err
	}
	parts := strings.Fields(spec.raw)
	minute, hour, day, month, week := parts[0], parts[1], parts[2], parts[3], parts[4]
	if day == "*" && month == "*" && week == "*" {
		switch {
		case minute == "*" && hour == "*":
			return "Every minute", nil
		case strings.HasPrefix(minute, "*/") && hour == "*":
			return "Every " + strings.TrimPrefix(minute, "*/") + " minutes", nil
		case hour == "*" && !strings.ContainsAny(minute, "*/,-"):
			return "At minute " + minute + " of every hour", nil
		case minute == "0" && strings.HasPrefix(hour, "*/"):
			return "Every " + strings.TrimPrefix(hour, "*/") + " hours", nil
		case !strings.ContainsAny(minute, "*/,-") && !strings.ContainsAny(hour, "*/,-"):
			h, _ := strconv.Atoi(hour)
			m, _ := strconv.Atoi(minute)
			stamp := time.Date(2000, 1, 1, h, m, 0, 0, time.Local)
			return "Daily at " + stamp.Format("3:04 PM"), nil
		}
	}
	return "Custom cron: " + spec.raw, nil
}

type MonitorAIMessage struct {
	ID         uint64 `json:"id"`
	Time       string `json:"time"`
	Role       string `json:"role"`
	Kind       string `json:"kind"`
	Status     string `json:"status,omitempty"`
	Content    string `json:"content"`
	ModelID    string `json:"model_id,omitempty"`
	ModelLabel string `json:"model_label,omitempty"`
	Scheduled  bool   `json:"scheduled,omitempty"`
}

type MonitorAINotification struct {
	ID      uint64 `json:"id"`
	Time    string `json:"time"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type monitorAIPersistedState struct {
	Sequence               uint64                 `json:"sequence"`
	MessageSequence        uint64                 `json:"message_sequence"`
	LastReportAt           string                 `json:"last_report_at,omitempty"`
	LastReportStatus       string                 `json:"last_report_status,omitempty"`
	ActiveFindingKeys      []string               `json:"active_finding_keys,omitempty"`
	LastWarningFingerprint string                 `json:"last_warning_fingerprint,omitempty"`
	ScheduleAnchor         string                 `json:"schedule_anchor,omitempty"`
	LastScheduledAt        string                 `json:"last_scheduled_at,omitempty"`
	LastCronMinute         string                 `json:"last_cron_minute,omitempty"`
	NotificationSequence   uint64                 `json:"notification_sequence"`
	LatestNotification     *MonitorAINotification `json:"latest_notification,omitempty"`
}

type MonitorAISummary struct {
	Sequence             uint64                 `json:"sequence"`
	Busy                 bool                   `json:"busy"`
	LastReportAt         string                 `json:"last_report_at,omitempty"`
	LastReportStatus     string                 `json:"last_report_status,omitempty"`
	ActiveFindingCount   int                    `json:"active_finding_count"`
	ScheduleDescription  string                 `json:"schedule_description"`
	ScheduledRuns        int                    `json:"scheduled_runs"`
	ScheduledRunLimit    int                    `json:"scheduled_run_limit"`
	ScheduleLimitReached bool                   `json:"schedule_limit_reached"`
	Notification         *MonitorAINotification `json:"notification,omitempty"`
}

type MonitorAIStateResponse struct {
	OK                  bool               `json:"ok"`
	Summary             MonitorAISummary   `json:"summary"`
	Conversation        []MonitorAIMessage `json:"conversation"`
	MemoryBytes         int                `json:"memory_bytes"`
	MemoryMaxKB         int                `json:"memory_max_kb"`
	MemoryPreview       string             `json:"memory_preview,omitempty"`
	ScheduleDescription string             `json:"schedule_description"`
}

type monitorAIResponseMeta struct {
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	FindingKeys  []string `json:"finding_keys"`
	MemoryAdd    []string `json:"memory_add"`
	MemoryRemove []string `json:"memory_remove"`
}

type monitorAIActionResponse struct {
	OK            bool             `json:"ok"`
	Message       MonitorAIMessage `json:"message"`
	Status        string           `json:"status"`
	MemoryUpdated bool             `json:"memory_updated,omitempty"`
	Warning       string           `json:"warning,omitempty"`
}

type monitorAIManager struct {
	app                      *App
	root                     string
	memoryPath               string
	conversationPath         string
	statePath                string
	mu                       sync.Mutex
	conversation             []MonitorAIMessage
	state                    monitorAIPersistedState
	inFlight                 bool
	scheduledRuns            int
	scheduledRunLimitStopped bool
	stop                     chan struct{}
	stopOnce                 sync.Once
}

func newMonitorAIManager(app *App, root string) *monitorAIManager {
	root = strings.TrimSpace(root)
	m := &monitorAIManager{
		app:              app,
		root:             root,
		memoryPath:       filepath.Join(root, "ai", "memory.md"),
		conversationPath: filepath.Join(root, "ai", "conversation.json"),
		statePath:        filepath.Join(root, "ai", "state.json"),
		stop:             make(chan struct{}),
	}
	m.load()
	return m
}

func (m *monitorAIManager) load() {
	if m == nil || m.root == "" {
		return
	}
	if data, err := os.ReadFile(m.conversationPath); err == nil {
		_ = json.Unmarshal(data, &m.conversation)
	}
	if data, err := os.ReadFile(m.statePath); err == nil {
		_ = json.Unmarshal(data, &m.state)
	}
	if len(m.conversation) > monitorAIConversationMax {
		m.conversation = append([]MonitorAIMessage(nil), m.conversation[len(m.conversation)-monitorAIConversationMax:]...)
	}
}

func (m *monitorAIManager) Start() {
	if m == nil {
		return
	}
	go m.loop()
}

func (m *monitorAIManager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { close(m.stop) })
}

func (m *monitorAIManager) loop() {
	ticker := time.NewTicker(monitorAISchedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.maybeRunScheduled(time.Now())
		case <-m.stop:
			return
		}
	}
}

func (m *monitorAIManager) persistStateLocked() {
	if m == nil || m.statePath == "" {
		return
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	_ = atomicWriteFile(m.statePath, data, 0o644)
}

func (m *monitorAIManager) persistConversationLocked() {
	if m == nil || m.conversationPath == "" {
		return
	}
	for len(m.conversation) > monitorAIConversationMax {
		m.conversation = m.conversation[1:]
	}
	for {
		data, err := json.MarshalIndent(m.conversation, "", "  ")
		if err != nil {
			return
		}
		if len(data) <= monitorAIConversationBytes || len(m.conversation) <= 20 {
			data = append(data, '\n')
			_ = atomicWriteFile(m.conversationPath, data, 0o644)
			return
		}
		remove := len(m.conversation) / 10
		if remove < 1 {
			remove = 1
		}
		m.conversation = append([]MonitorAIMessage(nil), m.conversation[remove:]...)
	}
}

func (m *monitorAIManager) appendMessageLocked(role, kind, status, content, modelID, modelLabel string, scheduled bool) MonitorAIMessage {
	m.state.MessageSequence++
	m.state.Sequence++
	message := MonitorAIMessage{
		ID:         m.state.MessageSequence,
		Time:       time.Now().Format(time.RFC3339),
		Role:       strings.TrimSpace(role),
		Kind:       strings.TrimSpace(kind),
		Status:     normalizeMonitorAIStatus(status),
		Content:    strings.TrimSpace(content),
		ModelID:    strings.TrimSpace(modelID),
		ModelLabel: strings.TrimSpace(modelLabel),
		Scheduled:  scheduled,
	}
	if kind == "chat" && role == "user" {
		message.Status = ""
	}
	m.conversation = append(m.conversation, message)
	m.persistConversationLocked()
	m.persistStateLocked()
	return message
}

func (m *monitorAIManager) Summary(cfg MonitorConfig) MonitorAISummary {
	if m == nil {
		return MonitorAISummary{ScheduleDescription: monitorScheduleDescription(cfg.AIReportSchedule, cfg.AIReportCron)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	notification := m.state.LatestNotification
	if notification != nil {
		copyNotification := *notification
		notification = &copyNotification
	}
	return MonitorAISummary{
		Sequence:             m.state.Sequence,
		Busy:                 m.inFlight,
		LastReportAt:         m.state.LastReportAt,
		LastReportStatus:     normalizeMonitorAIStatus(m.state.LastReportStatus),
		ActiveFindingCount:   len(m.state.ActiveFindingKeys),
		ScheduleDescription:  monitorScheduleDescription(cfg.AIReportSchedule, cfg.AIReportCron),
		ScheduledRuns:        m.scheduledRuns,
		ScheduledRunLimit:    cfg.AIScheduledRunLimit,
		ScheduleLimitReached: m.scheduledRunLimitStopped,
		Notification:         notification,
	}
}

func (m *monitorAIManager) StateResponse(cfg MonitorConfig) MonitorAIStateResponse {
	if m == nil {
		return MonitorAIStateResponse{OK: false}
	}
	m.mu.Lock()
	conversation := append([]MonitorAIMessage(nil), m.conversation...)
	summary := MonitorAISummary{
		Sequence:             m.state.Sequence,
		Busy:                 m.inFlight,
		LastReportAt:         m.state.LastReportAt,
		LastReportStatus:     normalizeMonitorAIStatus(m.state.LastReportStatus),
		ActiveFindingCount:   len(m.state.ActiveFindingKeys),
		ScheduleDescription:  monitorScheduleDescription(cfg.AIReportSchedule, cfg.AIReportCron),
		ScheduledRuns:        m.scheduledRuns,
		ScheduledRunLimit:    cfg.AIScheduledRunLimit,
		ScheduleLimitReached: m.scheduledRunLimitStopped,
	}
	if m.state.LatestNotification != nil {
		n := *m.state.LatestNotification
		summary.Notification = &n
	}
	m.mu.Unlock()
	memory, _ := os.ReadFile(m.memoryPath)
	preview := strings.TrimSpace(string(memory))
	if len(preview) > 4096 {
		preview = preview[:4096] + "\n…"
	}
	return MonitorAIStateResponse{
		OK:                  true,
		Summary:             summary,
		Conversation:        conversation,
		MemoryBytes:         len(memory),
		MemoryMaxKB:         cfg.AIMemoryMaxKB,
		MemoryPreview:       preview,
		ScheduleDescription: summary.ScheduleDescription,
	}
}

func (m *monitorAIManager) OnSettingsUpdated(previous, next MonitorConfig) {
	if m == nil {
		return
	}
	changedSchedule := normalizeMonitorAISchedule(previous.AIReportSchedule) != normalizeMonitorAISchedule(next.AIReportSchedule) || strings.TrimSpace(previous.AIReportCron) != strings.TrimSpace(next.AIReportCron) || strings.TrimSpace(previous.AIModelID) != strings.TrimSpace(next.AIModelID)
	if !changedSchedule {
		return
	}
	m.mu.Lock()
	m.state.ScheduleAnchor = time.Now().Format(time.RFC3339)
	m.state.LastCronMinute = ""
	m.state.Sequence++
	m.persistStateLocked()
	m.mu.Unlock()
}

func (m *monitorAIManager) beginRun() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight {
		return false
	}
	m.inFlight = true
	m.state.Sequence++
	m.persistStateLocked()
	return true
}

func (m *monitorAIManager) endRun() {
	m.mu.Lock()
	m.inFlight = false
	m.state.Sequence++
	m.persistStateLocked()
	m.mu.Unlock()
}

func (m *monitorAIManager) scheduledRunLimitReached(cfg MonitorConfig) bool {
	if m == nil {
		return true
	}
	cfg = normalizeMonitorConfig(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scheduledRunLimitStopped {
		return true
	}
	if m.scheduledRuns >= cfg.AIScheduledRunLimit {
		m.scheduledRunLimitStopped = true
		m.state.Sequence++
		m.persistStateLocked()
		return true
	}
	return false
}

func (m *monitorAIManager) reserveScheduledProviderCall(cfg MonitorConfig) bool {
	if m == nil {
		return false
	}
	cfg = normalizeMonitorConfig(cfg)
	limit := cfg.AIScheduledRunLimit
	m.mu.Lock()
	if m.scheduledRunLimitStopped || m.scheduledRuns >= limit {
		if !m.scheduledRunLimitStopped {
			m.scheduledRunLimitStopped = true
			m.state.Sequence++
			m.persistStateLocked()
		}
		m.mu.Unlock()
		return false
	}
	m.scheduledRuns++
	reachedNow := m.scheduledRuns >= limit
	if reachedNow {
		m.scheduledRunLimitStopped = true
	}
	runs := m.scheduledRuns
	m.state.Sequence++
	m.persistStateLocked()
	m.mu.Unlock()

	if reachedNow && m.app != nil {
		message := fmt.Sprintf("Scheduled AI report session limit reached (%d/%d). Automatic AI reports are paused until AgentGO restarts.", runs, limit)
		m.app.logf("monitor", "info", "%s", message)
		if m.app.monitor != nil {
			m.app.monitor.RecordEvent("info", "ai", message)
		}
	}
	return true
}

func (m *monitorAIManager) maybeRunScheduled(now time.Time) {
	if m == nil || m.app == nil || m.app.monitor == nil {
		return
	}
	cfg := m.app.monitorConfigSnapshot()
	if !cfg.Enabled || strings.TrimSpace(cfg.AIModelID) == "" || normalizeMonitorAISchedule(cfg.AIReportSchedule) == "off" {
		return
	}
	if m.scheduledRunLimitReached(cfg) {
		return
	}
	monitorState := m.app.monitor.State()
	if monitorState.Runtime.ConnectedClients <= 0 || monitorState.Runtime.Mode == "disconnected" || monitorState.Runtime.Mode == "off" {
		return
	}
	if !m.scheduleDue(cfg, now) {
		return
	}
	if !m.beginRun() {
		return
	}
	go func() {
		defer m.endRun()
		if _, err := m.runReport(context.Background(), cfg, true); err != nil {
			m.app.monitor.RecordEvent("warning", "ai", "Scheduled AI system report failed: "+err.Error())
			m.app.logf("monitor", "warn", "Scheduled AI system report failed: %v", err)
		}
	}()
}

func parseRFC3339OrZero(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func (m *monitorAIManager) scheduleDue(cfg MonitorConfig, now time.Time) bool {
	schedule := normalizeMonitorAISchedule(cfg.AIReportSchedule)
	m.mu.Lock()
	defer m.mu.Unlock()
	if schedule == "custom" {
		spec, err := parseMonitorCron(cfg.AIReportCron)
		if err != nil || !spec.matches(now) {
			return false
		}
		minuteKey := now.Format("2006-01-02T15:04")
		if m.state.LastCronMinute == minuteKey {
			return false
		}
		m.state.LastCronMinute = minuteKey
		m.state.LastScheduledAt = now.Format(time.RFC3339)
		m.persistStateLocked()
		return true
	}
	duration, ok := monitorAISchedulePresets[schedule]
	if !ok {
		return false
	}
	anchor := parseRFC3339OrZero(m.state.LastScheduledAt)
	if anchor.IsZero() {
		anchor = parseRFC3339OrZero(m.state.ScheduleAnchor)
	}
	if anchor.IsZero() {
		m.state.ScheduleAnchor = now.Format(time.RFC3339)
		m.persistStateLocked()
		return false
	}
	if now.Sub(anchor) < duration {
		return false
	}
	m.state.LastScheduledAt = now.Format(time.RFC3339)
	m.persistStateLocked()
	return true
}

func (m *monitorAIManager) loadMemory() string {
	if m == nil {
		return ""
	}
	data, err := os.ReadFile(m.memoryPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func monitorMemoryLineKey(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		if idx := strings.Index(line, "]"); idx > 0 && idx < 16 {
			line = strings.TrimSpace(line[idx+1:])
		}
	}
	return strings.ToLower(line)
}

func applyMonitorMemoryDelta(existing string, meta monitorAIResponseMeta, maxKB int) (string, bool) {
	if _, ok := monitorAIMemoryMaxPresets[maxKB]; !ok {
		maxKB = monitorDefaultAIMemoryMaxKB
	}
	lines := []string{}
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(existing))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key := monitorMemoryLineKey(line)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
	}
	removeKeys := make([]string, 0, len(meta.MemoryRemove))
	for _, item := range meta.MemoryRemove {
		key := monitorMemoryLineKey(item)
		if key != "" {
			removeKeys = append(removeKeys, key)
		}
	}
	if len(removeKeys) > 0 {
		filtered := lines[:0]
		for _, line := range lines {
			key := monitorMemoryLineKey(line)
			remove := false
			for _, target := range removeKeys {
				if key == target || strings.Contains(key, target) || strings.Contains(target, key) {
					remove = true
					break
				}
			}
			if !remove {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
		seen = map[string]struct{}{}
		for _, line := range lines {
			seen[monitorMemoryLineKey(line)] = struct{}{}
		}
	}
	datePrefix := "[" + time.Now().Format("2006-01-02") + "] "
	for _, raw := range meta.MemoryAdd {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if len(line) > 700 {
			line = line[:700]
		}
		key := monitorMemoryLineKey(line)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		if !strings.HasPrefix(line, "[") {
			line = datePrefix + line
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
	}
	maxBytes := maxKB * 1024
	isPriority := func(line string) bool {
		lower := strings.ToLower(line)
		for _, marker := range []string{"watch:", "issue:", "unresolved", "user:", "user-approved", "system:", "hardware:", "malware", "security"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		return false
	}
	join := func(items []string) string { return strings.TrimSpace(strings.Join(items, "\n")) }
	result := join(lines)
	for len(result) > maxBytes && len(lines) > 1 {
		removeIndex := -1
		for i, line := range lines {
			if !isPriority(line) {
				removeIndex = i
				break
			}
		}
		if removeIndex < 0 {
			removeIndex = 0
		}
		lines = append(lines[:removeIndex], lines[removeIndex+1:]...)
		result = join(lines)
	}
	if len(result) > maxBytes {
		result = result[len(result)-maxBytes:]
		if idx := strings.IndexByte(result, '\n'); idx >= 0 {
			result = strings.TrimSpace(result[idx+1:])
		}
	}
	return result, strings.TrimSpace(result) != strings.TrimSpace(existing)
}

func selectMonitorMemory(memory, prompt string, maxBytes int) string {
	memory = strings.TrimSpace(memory)
	if memory == "" {
		return "(empty)"
	}
	if maxBytes <= 0 {
		maxBytes = monitorAIInjectedMemoryMax
	}
	if len(memory) <= maxBytes {
		return memory
	}
	keywords := map[string]struct{}{}
	for _, word := range strings.Fields(strings.ToLower(prompt)) {
		word = strings.Trim(word, ".,:;!?()[]{}\"'")
		if len(word) >= 4 {
			keywords[word] = struct{}{}
		}
	}
	type scoredLine struct {
		line  string
		score int
		idx   int
	}
	var scored []scoredLine
	for i, line := range strings.Split(memory, "\n") {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		lower := strings.ToLower(clean)
		score := 0
		for _, marker := range []string{"watch:", "issue:", "unresolved", "user:", "system:", "hardware:", "security", "malware"} {
			if strings.Contains(lower, marker) {
				score += 6
			}
		}
		for keyword := range keywords {
			if strings.Contains(lower, keyword) {
				score += 3
			}
		}
		if i >= len(strings.Split(memory, "\n"))-12 {
			score++
		}
		scored = append(scored, scoredLine{line: clean, score: score, idx: i})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].idx > scored[j].idx
		}
		return scored[i].score > scored[j].score
	})
	selected := []scoredLine{}
	used := 0
	for _, item := range scored {
		cost := len(item.line) + 1
		if used+cost > maxBytes {
			continue
		}
		selected = append(selected, item)
		used += cost
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].idx < selected[j].idx })
	lines := make([]string, 0, len(selected))
	for _, item := range selected {
		lines = append(lines, item.line)
	}
	return strings.Join(lines, "\n")
}

func extractMonitorAIMeta(raw string) (string, monitorAIResponseMeta, bool) {
	const begin = "---BEGIN_AGENTGO_MONITOR_META---"
	const end = "---END_AGENTGO_MONITOR_META---"
	start := strings.Index(raw, begin)
	if start < 0 {
		return strings.TrimSpace(raw), monitorAIResponseMeta{}, false
	}
	endIndex := strings.Index(raw[start+len(begin):], end)
	if endIndex < 0 {
		return strings.TrimSpace(raw), monitorAIResponseMeta{}, false
	}
	metaStart := start + len(begin)
	metaEnd := metaStart + endIndex
	visible := strings.TrimSpace(raw[:start] + raw[metaEnd+len(end):])
	var meta monitorAIResponseMeta
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw[metaStart:metaEnd])), &meta); err != nil {
		return visible, monitorAIResponseMeta{}, false
	}
	meta.Status = normalizeMonitorAIStatus(meta.Status)
	meta.Summary = strings.TrimSpace(meta.Summary)
	meta.FindingKeys = normalizeMonitorFindingKeys(meta.FindingKeys)
	meta.MemoryAdd = normalizeMonitorMemoryDeltaItems(meta.MemoryAdd)
	meta.MemoryRemove = normalizeMonitorMemoryDeltaItems(meta.MemoryRemove)
	return visible, meta, true
}

func normalizeMonitorFindingKeys(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if len(clean) > 180 {
			clean = clean[:180]
		}
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
		if len(out) >= 48 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func normalizeMonitorMemoryDeltaItems(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if len(clean) > 700 {
			clean = clean[:700]
		}
		out = append(out, clean)
		if len(out) >= 80 {
			break
		}
	}
	return out
}

func monitorAIFingerprint(status string, findingKeys []string, summary string) string {
	payload := normalizeMonitorAIStatus(status) + "\n" + strings.Join(normalizeMonitorFindingKeys(findingKeys), "\n") + "\n" + strings.TrimSpace(summary)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func loadMonitorAISystemPrompt() string {
	path := filepath.Join(systemPromptsDir, monitorAISystemPromptFile)
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(`You are AgentGO Monitor AI, a system-health and security analyst. Use only evidence AgentGO provides. Distinguish unknown or suspicious software from confirmed malware. Correlate processes, resource use, thermals, networking, startup/services, operating-system logs, and security-tool evidence. Give clear remediation when warranted. Never invent unavailable data.`)
}

func monitorSnapshotContext(state monitorStateResponse, memorySelection, conversationContext string, lastReportAt, lastStatus string, activeFindings []string) string {
	s := state.Snapshot
	var b strings.Builder
	fmt.Fprintf(&b, "SYSTEM PROFILE\nOS=%s %s\nARCH=%s\nHOST=%s\nCPU=%s\nGPU=%s\nRAM=%s\nSTORAGE=%s total / %s free\nUPTIME=%s\n", s.System.OS, s.System.OSVersion, s.System.Architecture, s.System.Hostname, s.System.CPUModel, s.System.GPUModel, formatMonitorBytes(s.System.TotalMemoryBytes), formatMonitorBytes(s.System.StorageTotalBytes), formatMonitorBytes(s.System.StorageFreeBytes), time.Duration(s.System.UptimeSeconds)*time.Second)
	fmt.Fprintf(&b, "\nCURRENT SNAPSHOT\nCaptured=%s\nCPU=%s\nGPU=%s\nMemory=%s\nDisk=%s\nNetwork RX=%s/s TX=%s/s\nCPU Temp=%s\nGPU Temp=%s\nThermal State=%s\n", s.Timestamp, formatMonitorPercent(s.CPUPercent), formatMonitorPercent(s.GPUPercent), formatMonitorPercent(s.MemoryPercent), formatMonitorPercent(s.DiskPercent), formatMonitorFloatBytes(s.NetworkRxBytesPerSec), formatMonitorFloatBytes(s.NetworkTxBytesPerSec), formatMonitorTemp(s.CPUTemperatureC), formatMonitorTemp(s.GPUTemperatureC), s.ThermalState)

	processes := append([]MonitorProcess(nil), s.Processes...)
	sort.SliceStable(processes, func(i, j int) bool {
		left := 0.0
		right := 0.0
		if processes[i].CPUPercent != nil {
			left += *processes[i].CPUPercent * 3
		}
		if processes[j].CPUPercent != nil {
			right += *processes[j].CPUPercent * 3
		}
		left += float64(processes[i].MemoryBytes) / (1024 * 1024 * 512)
		right += float64(processes[j].MemoryBytes) / (1024 * 1024 * 512)
		return left > right
	})
	b.WriteString("\nTOP PROCESSES\n")
	for i, proc := range processes {
		if i >= 18 {
			break
		}
		fmt.Fprintf(&b, "- %s pid=%d cpu=%s gpu=%s ram=%s path=%s publisher=%s signature=%s\n", proc.Name, proc.PID, formatMonitorPercent(proc.CPUPercent), formatMonitorPercent(proc.GPUPercent), formatMonitorBytes(proc.MemoryBytes), emptyMonitorValue(proc.Path), emptyMonitorValue(proc.Publisher), emptyMonitorValue(proc.Signature))
	}

	b.WriteString("\nRECENT MONITOR EVENTS\n")
	lastReport := parseRFC3339OrZero(lastReportAt)
	events := state.Events
	start := 0
	if len(events) > 90 {
		start = len(events) - 90
	}
	for _, event := range events[start:] {
		if !lastReport.IsZero() {
			when := parseRFC3339OrZero(event.Time)
			if !when.IsZero() && when.Before(lastReport) {
				continue
			}
		}
		fmt.Fprintf(&b, "- [%s] %s/%s: %s\n", event.Time, event.Level, event.Kind, event.Message)
	}

	if len(s.Security) > 0 {
		b.WriteString("\nSECURITY TOOL EVIDENCE\n")
		start = 0
		if len(s.Security) > 30 {
			start = len(s.Security) - 30
		}
		for _, record := range s.Security[start:] {
			fmt.Fprintf(&b, "- [%s] %s severity=%s type=%s event=%s path=%s msg=%s\n", record.Time, record.Source, record.Severity, record.Type, record.EventID, record.Path, record.Message)
		}
	}
	if len(s.Logs) > 0 {
		b.WriteString("\nRELEVANT OS LOGS\n")
		count := 0
		for i := len(s.Logs) - 1; i >= 0 && count < 24; i-- {
			record := s.Logs[i]
			level := strings.ToLower(record.Level)
			if level != "warning" && level != "error" && level != "critical" {
				continue
			}
			fmt.Fprintf(&b, "- [%s] %s %s event=%s: %s\n", record.Time, record.Level, record.Source, record.EventID, record.Message)
			count++
		}
	}
	if len(s.Connections) > 0 {
		b.WriteString("\nNETWORK / LISTENERS (sample)\n")
		count := 0
		for _, conn := range s.Connections {
			if count >= 24 {
				break
			}
			if !isListeningConnection(conn) && conn.RemoteAddress == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s %s:%d -> %s:%d state=%s process=%s pid=%d\n", conn.Protocol, conn.LocalAddress, conn.LocalPort, conn.RemoteAddress, conn.RemotePort, conn.State, conn.Process, conn.PID)
			count++
		}
	}
	if len(state.History) > 0 {
		b.WriteString("\nRECENT TELEMETRY SUMMARIES\n")
		start = 0
		if len(state.History) > 12 {
			start = len(state.History) - 12
		}
		for _, item := range state.History[start:] {
			fmt.Fprintf(&b, "- %s..%s samples=%d cpu_avg=%s cpu_peak=%s gpu_avg=%s gpu_peak=%s mem_avg=%s cpu_temp_avg=%s cpu_temp_peak=%s\n", item.Start, item.End, item.Samples, formatMonitorPercent(item.CPUAverage), formatMonitorPercent(item.CPUPeak), formatMonitorPercent(item.GPUAverage), formatMonitorPercent(item.GPUPeak), formatMonitorPercent(item.MemoryAverage), formatMonitorTemp(item.CPUTemperatureAverage), formatMonitorTemp(item.CPUTemperaturePeak))
		}
	}
	fmt.Fprintf(&b, "\nPREVIOUS AI REVIEW\nLast=%s\nStatus=%s\nActive finding keys=%s\n", emptyMonitorValue(lastReportAt), emptyMonitorValue(lastStatus), strings.Join(activeFindings, ", "))
	fmt.Fprintf(&b, "\nRELEVANT MONITOR MEMORY (UGG)\n%s\n", memorySelection)
	if strings.TrimSpace(conversationContext) != "" {
		fmt.Fprintf(&b, "\nRECENT MONITOR CONVERSATION\n%s\n", conversationContext)
	}
	return b.String()
}

func formatMonitorBytes(value uint64) string {
	if value == 0 {
		return "unavailable"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(value)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func formatMonitorFloatBytes(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	if *value <= 0 {
		return "0 B"
	}
	return formatMonitorBytes(uint64(*value))
}

func formatMonitorPercent(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func formatMonitorTemp(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1fC", *value)
}

func emptyMonitorValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return strings.TrimSpace(value)
}

func (m *monitorAIManager) recentConversationContext(maxBytes int) string {
	m.mu.Lock()
	conversation := append([]MonitorAIMessage(nil), m.conversation...)
	m.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = 8 * 1024
	}
	var lines []string
	used := 0
	for i := len(conversation) - 1; i >= 0 && len(lines) < 12; i-- {
		item := conversation[i]
		if item.Kind == "failure" {
			continue
		}
		content := strings.TrimSpace(item.Content)
		if len(content) > 1800 {
			content = content[:1800] + "…"
		}
		line := strings.ToUpper(item.Role) + " [" + item.Kind + "]: " + content
		if used+len(line)+1 > maxBytes {
			continue
		}
		lines = append(lines, line)
		used += len(line) + 1
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m *monitorAIManager) runModel(ctx context.Context, cfg MonitorConfig, userPrompt string, isReport, scheduled bool) (monitorAIActionResponse, error) {
	return m.runModelWithContext(ctx, cfg, userPrompt, userPrompt, "", isReport, scheduled)
}

// runModelWithContext lets interactive item investigations supply a deliberately
// narrow evidence bundle instead of injecting the entire Monitor snapshot. The
// displayed user message can also remain short while the model receives the
// structured evidence it needs.
func (m *monitorAIManager) runModelWithContext(ctx context.Context, cfg MonitorConfig, userPrompt, displayPrompt, itemEvidence string, isReport, scheduled bool) (monitorAIActionResponse, error) {
	if m == nil || m.app == nil || m.app.monitor == nil {
		return monitorAIActionResponse{}, errors.New("Monitor AI is unavailable")
	}
	modelID := strings.TrimSpace(cfg.AIModelID)
	if modelID == "" {
		return monitorAIActionResponse{}, errors.New("select an AI model for AgentGO Monitor first")
	}
	model, ok := m.app.findModel(modelID)
	if !ok {
		return monitorAIActionResponse{}, errors.New("selected Monitor AI model is no longer configured")
	}
	if modelIsVideoGeneration(model) || modelIsMeshGeneration(model) || !model.Capabilities.SupportsTextIn {
		return monitorAIActionResponse{}, errors.New("selected Monitor AI model does not support normal text analysis")
	}

	memory := m.loadMemory()
	memorySelection := selectMonitorMemory(memory, userPrompt+"\n"+itemEvidence, monitorAIInjectedMemoryMax)
	m.mu.Lock()
	lastReportAt := m.state.LastReportAt
	lastStatus := m.state.LastReportStatus
	activeFindings := append([]string(nil), m.state.ActiveFindingKeys...)
	m.mu.Unlock()
	conversationContext := ""
	if !isReport {
		conversationContext = m.recentConversationContext(8 * 1024)
	}
	state := m.app.monitor.State()
	contextText := ""
	if strings.TrimSpace(itemEvidence) != "" {
		contextText = monitorItemContext(state, memorySelection, conversationContext, itemEvidence)
	} else {
		contextText = monitorSnapshotContext(state, memorySelection, conversationContext, lastReportAt, lastStatus, activeFindings)
	}
	instructions := loadMonitorAISystemPrompt() + "\n\n" + chatMemoryUggProtocolPrompt
	instructions = appendModelUggProtocol(instructions, model)
	mode := "DIRECT MONITOR QUESTION"
	if isReport {
		mode = "SYSTEM REVIEW REPORT"
	} else if strings.TrimSpace(itemEvidence) != "" {
		mode = "ITEM INVESTIGATION"
	}
	input := strings.TrimSpace(fmt.Sprintf("%s\n\n%s\n\nUSER/REPORT REQUEST\n%s", mode, contextText, strings.TrimSpace(userPrompt)))
	if !isReport {
		shown := strings.TrimSpace(displayPrompt)
		if shown == "" {
			shown = strings.TrimSpace(userPrompt)
		}
		m.mu.Lock()
		m.appendMessageLocked("user", "chat", "", shown, modelID, model.Label, false)
		m.mu.Unlock()
	}
	var raw string
	var err error
	if scheduled {
		var dispatchOnce sync.Once
		var dispatchErr error
		beforeDispatch := func() error {
			dispatchOnce.Do(func() {
				if !m.reserveScheduledProviderCall(cfg) {
					dispatchErr = errors.New("scheduled AI report session limit reached; restart AgentGO to allow scheduled reports again")
				}
			})
			return dispatchErr
		}
		raw, err = m.app.callStructuredTextModelWithDispatch(ctx, model, instructions, input, false, nil, beforeDispatch)
	} else {
		raw, err = m.app.callStructuredTextModel(ctx, model, instructions, input, false, nil)
	}
	if err != nil {
		m.mu.Lock()
		m.appendMessageLocked("system", "failure", "notice", "AI request failed: "+err.Error(), modelID, model.Label, scheduled)
		m.mu.Unlock()
		return monitorAIActionResponse{}, err
	}
	visible, meta, hasMeta := extractMonitorAIMeta(raw)
	if strings.TrimSpace(visible) == "" {
		visible = strings.TrimSpace(raw)
	}
	warning := ""
	if !hasMeta {
		// Treat missing machine metadata as a formatting problem, not evidence that
		// a previous finding has resolved. Keep report state/memory untouched.
		meta.Status = "notice"
		warning = "AI response did not include Monitor metadata; memory and finding state were preserved."
	}
	memoryUpdated := false
	if hasMeta && (len(meta.MemoryAdd) > 0 || len(meta.MemoryRemove) > 0) {
		updated, changed := applyMonitorMemoryDelta(memory, meta, cfg.AIMemoryMaxKB)
		if changed {
			if err := atomicWriteFile(m.memoryPath, []byte(strings.TrimSpace(updated)+"\n"), 0o644); err != nil {
				warning = "Could not save Monitor AI memory."
			} else {
				memoryUpdated = true
			}
		}
	}
	kind := "chat"
	if isReport {
		kind = "report"
	}
	m.mu.Lock()
	message := m.appendMessageLocked("assistant", kind, meta.Status, visible, modelID, model.Label, scheduled)
	if isReport && hasMeta {
		m.updateReportStateLocked(meta, visible, scheduled)
	}
	m.mu.Unlock()
	return monitorAIActionResponse{OK: true, Message: message, Status: normalizeMonitorAIStatus(meta.Status), MemoryUpdated: memoryUpdated, Warning: warning}, nil
}

func (m *monitorAIManager) updateReportStateLocked(meta monitorAIResponseMeta, visible string, scheduled bool) {
	previousStatus := normalizeMonitorAIStatus(m.state.LastReportStatus)
	previousFingerprint := m.state.LastWarningFingerprint
	status := normalizeMonitorAIStatus(meta.Status)
	findingKeys := normalizeMonitorFindingKeys(meta.FindingKeys)
	summary := strings.TrimSpace(meta.Summary)
	if summary == "" {
		summary = strings.TrimSpace(visible)
		if len(summary) > 260 {
			summary = summary[:260] + "…"
		}
	}
	fingerprint := monitorAIFingerprint(status, findingKeys, summary)
	m.state.LastReportAt = time.Now().Format(time.RFC3339)
	m.state.LastReportStatus = status
	m.state.ActiveFindingKeys = findingKeys
	newWarning := monitorAIStatusRank(status) >= monitorAIStatusRank("warning") && (fingerprint != previousFingerprint || monitorAIStatusRank(status) > monitorAIStatusRank(previousStatus))
	if newWarning {
		m.state.LastWarningFingerprint = fingerprint
		m.state.NotificationSequence++
		notification := MonitorAINotification{ID: m.state.NotificationSequence, Time: time.Now().Format(time.RFC3339), Status: status, Message: "AI System Monitor Warning Arrived"}
		if summary != "" {
			notification.Message += ": " + summary
		}
		m.state.LatestNotification = &notification
	}
	m.state.Sequence++
	m.persistStateLocked()
	if m.app != nil {
		if newWarning {
			m.app.logf("monitor", "warning", "AI System Monitor Warning Arrived: %s", summary)
			if m.app.monitor != nil {
				m.app.monitor.RecordEvent("warning", "ai", "AI System Monitor Warning Arrived: "+summary)
			}
		} else {
			m.app.logf("monitor", "info", "AI system report completed: %s.", strings.ToUpper(status))
			if m.app.monitor != nil {
				m.app.monitor.RecordEvent("info", "ai", "AI system report completed: "+strings.ToUpper(status)+".")
			}
		}
	}
	_ = scheduled
}

func (m *monitorAIManager) runReport(ctx context.Context, cfg MonitorConfig, scheduled bool) (monitorAIActionResponse, error) {
	request := "Review the current AgentGO Monitor evidence. Determine whether the system appears normal, whether anything deserves attention, what changed since the previous review, and what the user should do next. Follow the standardized report format."
	return m.runModel(ctx, cfg, request, true, scheduled)
}

func (m *monitorAIManager) RunReport(ctx context.Context, cfg MonitorConfig, scheduled bool) (monitorAIActionResponse, error) {
	if !m.beginRun() {
		return monitorAIActionResponse{}, errors.New("another Monitor AI request is already running")
	}
	defer m.endRun()
	return m.runReport(ctx, cfg, scheduled)
}

func (m *monitorAIManager) RunChat(ctx context.Context, cfg MonitorConfig, prompt string) (monitorAIActionResponse, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return monitorAIActionResponse{}, errors.New("question is required")
	}
	if !m.beginRun() {
		return monitorAIActionResponse{}, errors.New("another Monitor AI request is already running")
	}
	defer m.endRun()
	return m.runModel(ctx, cfg, prompt, false, false)
}

func (m *monitorAIManager) ClearMemory() error {
	if m == nil {
		return errors.New("Monitor AI is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := atomicWriteFile(m.memoryPath, []byte(""), 0o644); err != nil {
		return err
	}
	m.state.Sequence++
	m.persistStateLocked()
	return nil
}

func (m *monitorAIManager) Memory() string {
	return m.loadMemory()
}

func (a *App) monitorConfigSnapshot() MonitorConfig {
	if a != nil && a.monitor != nil {
		return a.monitor.SettingsSnapshot()
	}
	a.mu.RLock()
	cfg := normalizeMonitorConfig(a.cfg.Monitor)
	a.mu.RUnlock()
	return cfg
}

func (a *App) handleMonitorAIState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.monitorAI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "Monitor AI unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, a.monitorAI.StateResponse(a.monitorConfigSnapshot()))
}

func (a *App) handleMonitorAIReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.monitorConfigSnapshot()
	response, err := a.monitorAI.RunReport(r.Context(), cfg, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleMonitorAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	response, err := a.monitorAI.RunChat(r.Context(), a.monitorConfigSnapshot(), req.Prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleMonitorAIMemory(w http.ResponseWriter, r *http.Request) {
	if a.monitorAI == nil {
		http.Error(w, "Monitor AI unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		memory := a.monitorAI.Memory()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "memory": memory, "bytes": len([]byte(memory)), "max_kb": a.monitorConfigSnapshot().AIMemoryMaxKB})
	case http.MethodDelete, http.MethodPost:
		if err := a.monitorAI.ClearMemory(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a.monitor != nil {
			a.monitor.RecordEvent("info", "ai", "Monitor AI memory cleared by user.")
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
