package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	monitorVirusTotalWebsiteURL   = "https://www.virustotal.com/gui/home/upload"
	monitorVirusTotalReportBase   = "https://www.virustotal.com/gui/file/"
	monitorMaxHashResponseBytes   = 2 * 1024 * 1024
	monitorMaxInvestigationEvents = 40
)

// Overridable only by tests. Production always uses VirusTotal's v3 API.
var monitorVirusTotalAPIBaseURL = "https://www.virustotal.com/api/v3"

// monitorItemRef is deliberately a selector, not a trusted local path. Every
// handler resolves it against the current Monitor snapshot before touching a
// file so the browser API cannot be turned into an arbitrary file reader.
type monitorItemRef struct {
	Kind          string `json:"kind"`
	PID           int    `json:"pid,omitempty"`
	Name          string `json:"name,omitempty"`
	Path          string `json:"path,omitempty"`
	Command       string `json:"command,omitempty"`
	Source        string `json:"source,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	LocalAddress  string `json:"local_address,omitempty"`
	LocalPort     int    `json:"local_port,omitempty"`
	RemoteAddress string `json:"remote_address,omitempty"`
	RemotePort    int    `json:"remote_port,omitempty"`
}

type monitorResolvedItem struct {
	Ref         monitorItemRef
	Name        string
	Path        string
	Command     string
	Source      string
	Description string
	PID         int
	ParentPID   int
	User        string
	Version     string
	Publisher   string
	Signature   string
	StartTime   string
	FileSize    int64
	CPUPercent  *float64
	GPUPercent  *float64
	MemoryBytes uint64
	Connections []MonitorNetworkConnection
	Security    []MonitorSecurityRecord
}

type monitorFileIdentity struct {
	Size        int64 `json:"size"`
	ModUnixNano int64 `json:"mod_unix_nano"`
}

type monitorReviewRecord struct {
	Path             string              `json:"path"`
	Name             string              `json:"name,omitempty"`
	SHA256           string              `json:"sha256,omitempty"`
	Identity         monitorFileIdentity `json:"identity"`
	Status           string              `json:"status"`
	UserKnown        bool                `json:"user_known,omitempty"`
	AIStatus         string              `json:"ai_status,omitempty"`
	FirstSeen        string              `json:"first_seen,omitempty"`
	LastSeen         string              `json:"last_seen,omitempty"`
	LastReviewed     string              `json:"last_reviewed,omitempty"`
	LastReputation   string              `json:"last_reputation,omitempty"`
	LastReputationAt string              `json:"last_reputation_at,omitempty"`
}

type monitorReviewFile struct {
	Items map[string]monitorReviewRecord `json:"items"`
}

type monitorItemDetailResponse struct {
	OK               bool                       `json:"ok"`
	Kind             string                     `json:"kind"`
	Name             string                     `json:"name"`
	PID              int                        `json:"pid,omitempty"`
	ParentPID        int                        `json:"parent_pid,omitempty"`
	Path             string                     `json:"path,omitempty"`
	Command          string                     `json:"command,omitempty"`
	Source           string                     `json:"source,omitempty"`
	Description      string                     `json:"description,omitempty"`
	FileExists       bool                       `json:"file_exists"`
	FileSizeBytes    int64                      `json:"file_size_bytes,omitempty"`
	SHA256           string                     `json:"sha256,omitempty"`
	PreviousSHA256   string                     `json:"previous_sha256,omitempty"`
	Version          string                     `json:"version,omitempty"`
	Publisher        string                     `json:"publisher,omitempty"`
	Signature        string                     `json:"signature,omitempty"`
	User             string                     `json:"user,omitempty"`
	StartTime        string                     `json:"start_time,omitempty"`
	CPUPercent       *float64                   `json:"cpu_percent,omitempty"`
	GPUPercent       *float64                   `json:"gpu_percent,omitempty"`
	MemoryBytes      uint64                     `json:"memory_bytes,omitempty"`
	Connections      []MonitorNetworkConnection `json:"connections,omitempty"`
	SecurityEvidence []MonitorSecurityRecord    `json:"security_evidence,omitempty"`
	ReviewState      string                     `json:"review_state"`
	FirstSeen        string                     `json:"first_seen,omitempty"`
	LastSeen         string                     `json:"last_seen,omitempty"`
	LastReviewed     string                     `json:"last_reviewed,omitempty"`
	UserKnown        bool                       `json:"user_known,omitempty"`
	VirusTotalURL    string                     `json:"virustotal_url"`
	VirusTotalReport string                     `json:"virustotal_report,omitempty"`
}

type monitorHashReputation struct {
	Provider         string `json:"provider"`
	Configured       bool   `json:"configured"`
	Known            bool   `json:"known"`
	Malicious        int    `json:"malicious,omitempty"`
	Suspicious       int    `json:"suspicious,omitempty"`
	Harmless         int    `json:"harmless,omitempty"`
	Undetected       int    `json:"undetected,omitempty"`
	Timeout          int    `json:"timeout,omitempty"`
	Reputation       int    `json:"reputation,omitempty"`
	MeaningfulName   string `json:"meaningful_name,omitempty"`
	TypeDescription  string `json:"type_description,omitempty"`
	LastAnalysisDate string `json:"last_analysis_date,omitempty"`
	Message          string `json:"message,omitempty"`
	ReportURL        string `json:"report_url,omitempty"`
}

type monitorItemHashResponse struct {
	OK         bool                      `json:"ok"`
	Item       monitorItemDetailResponse `json:"item"`
	SHA256     string                    `json:"sha256"`
	Reputation monitorHashReputation     `json:"reputation"`
}

type monitorHashCacheEntry struct {
	Identity monitorFileIdentity
	SHA256   string
}

var monitorHashCache = struct {
	sync.Mutex
	Items map[string]monitorHashCacheEntry
}{Items: map[string]monitorHashCacheEntry{}}

var monitorReviewMu sync.Mutex

func monitorReviewKey(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func monitorReviewPath(appRoot string) string {
	return filepath.Join(appRoot, "monitor", "investigation", "reviewed.json")
}

func loadMonitorReviewFile(appRoot string) monitorReviewFile {
	out := monitorReviewFile{Items: map[string]monitorReviewRecord{}}
	data, err := os.ReadFile(monitorReviewPath(appRoot))
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	if out.Items == nil {
		out.Items = map[string]monitorReviewRecord{}
	}
	return out
}

func saveMonitorReviewFile(appRoot string, file monitorReviewFile) error {
	if file.Items == nil {
		file.Items = map[string]monitorReviewRecord{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(monitorReviewPath(appRoot), append(data, '\n'), 0o644)
}

func monitorFileStatIdentity(path string) (monitorFileIdentity, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return monitorFileIdentity{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return monitorFileIdentity{}, nil, errors.New("selected path is not a regular file")
	}
	return monitorFileIdentity{Size: info.Size(), ModUnixNano: info.ModTime().UnixNano()}, info, nil
}

func computeMonitorSHA256(path string) (string, monitorFileIdentity, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", monitorFileIdentity{}, errors.New("selected item has no executable file path")
	}
	identity, _, err := monitorFileStatIdentity(path)
	if err != nil {
		return "", monitorFileIdentity{}, err
	}
	key := monitorReviewKey(path)
	monitorHashCache.Lock()
	if cached, ok := monitorHashCache.Items[key]; ok && cached.Identity == identity && cached.SHA256 != "" {
		monitorHashCache.Unlock()
		return cached.SHA256, identity, nil
	}
	monitorHashCache.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return "", monitorFileIdentity{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", monitorFileIdentity{}, err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	monitorHashCache.Lock()
	if len(monitorHashCache.Items) > 512 {
		for k := range monitorHashCache.Items {
			delete(monitorHashCache.Items, k)
			if len(monitorHashCache.Items) <= 384 {
				break
			}
		}
	}
	monitorHashCache.Items[key] = monitorHashCacheEntry{Identity: identity, SHA256: hash}
	monitorHashCache.Unlock()
	return hash, identity, nil
}

func monitorExecutableFromCommand(command string) string {
	value := strings.TrimSpace(os.ExpandEnv(command))
	if value == "" {
		return ""
	}
	for _, quote := range []string{"\"", "'"} {
		if strings.HasPrefix(value, quote) {
			if end := strings.Index(value[1:], quote); end >= 0 {
				return filepath.Clean(value[1 : end+1])
			}
		}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Clean(strings.Trim(fields[0], "\"'"))
}

func firstExistingMonitorPath(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		candidate = filepath.Clean(os.ExpandEnv(candidate))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func monitorRefMatchesText(refValue, actual string) bool {
	refValue = strings.TrimSpace(refValue)
	return refValue == "" || strings.EqualFold(refValue, strings.TrimSpace(actual))
}

func resolveMonitorItem(snapshot MonitorSnapshot, ref monitorItemRef) (monitorResolvedItem, error) {
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	ref.Kind = kind
	result := monitorResolvedItem{Ref: ref}
	switch kind {
	case "process":
		if ref.PID <= 0 {
			return result, errors.New("process PID is required")
		}
		var found *MonitorProcess
		for i := range snapshot.Processes {
			if snapshot.Processes[i].PID == ref.PID {
				found = &snapshot.Processes[i]
				break
			}
		}
		if found == nil {
			return result, errors.New("selected process is no longer running")
		}
		p := *found
		result.Name, result.Path, result.PID, result.ParentPID = p.Name, p.Path, p.PID, p.ParentPID
		result.User, result.Version, result.Publisher, result.Signature, result.StartTime = p.User, p.Version, p.Publisher, p.Signature, p.StartTime
		result.FileSize, result.CPUPercent, result.GPUPercent, result.MemoryBytes = p.FileSizeBytes, p.CPUPercent, p.GPUPercent, p.MemoryBytes
	case "startup":
		var found *MonitorStartupItem
		for i := range snapshot.StartupItems {
			item := &snapshot.StartupItems[i]
			if monitorRefMatchesText(ref.Name, item.Name) && monitorRefMatchesText(ref.Source, item.Source) && monitorRefMatchesText(ref.Command, item.Command) && monitorRefMatchesText(ref.Path, item.Path) {
				found = item
				break
			}
		}
		if found == nil {
			return result, errors.New("selected startup item is no longer available")
		}
		result.Name, result.Command, result.Source, result.Publisher = found.Name, found.Command, found.Source, found.Publisher
		result.Path = firstExistingMonitorPath(found.Path, monitorExecutableFromCommand(found.Command))
	case "service":
		var found *MonitorService
		for i := range snapshot.Services {
			item := &snapshot.Services[i]
			if monitorRefMatchesText(ref.Name, item.Name) && (ref.PID <= 0 || ref.PID == item.PID) {
				found = item
				break
			}
		}
		if found == nil {
			return result, errors.New("selected service/daemon is no longer available")
		}
		result.Name, result.Command, result.User, result.Description, result.PID = found.Name, found.Command, found.User, found.Description, found.PID
		result.Path = firstExistingMonitorPath(monitorExecutableFromCommand(found.Command))
		if result.PID > 0 {
			for _, proc := range snapshot.Processes {
				if proc.PID == result.PID {
					if result.Path == "" {
						result.Path = firstExistingMonitorPath(proc.Path)
					}
					result.ParentPID, result.Version, result.Publisher, result.Signature, result.StartTime = proc.ParentPID, proc.Version, proc.Publisher, proc.Signature, proc.StartTime
					result.CPUPercent, result.GPUPercent, result.MemoryBytes = proc.CPUPercent, proc.GPUPercent, proc.MemoryBytes
					break
				}
			}
		}
	case "network":
		var found *MonitorNetworkConnection
		for i := range snapshot.Connections {
			item := &snapshot.Connections[i]
			if (ref.PID <= 0 || item.PID == ref.PID) && item.LocalPort == ref.LocalPort && item.RemotePort == ref.RemotePort && monitorRefMatchesText(ref.LocalAddress, item.LocalAddress) && monitorRefMatchesText(ref.RemoteAddress, item.RemoteAddress) {
				found = item
				break
			}
		}
		if found == nil {
			return result, errors.New("selected network connection is no longer available")
		}
		result.PID, result.Name = found.PID, found.Process
		result.Description = fmt.Sprintf("%s %s:%d -> %s:%d state=%s", found.Protocol, found.LocalAddress, found.LocalPort, found.RemoteAddress, found.RemotePort, found.State)
		if found.PID > 0 {
			for _, proc := range snapshot.Processes {
				if proc.PID == found.PID {
					result.Path, result.ParentPID, result.User, result.Version, result.Publisher, result.Signature, result.StartTime = proc.Path, proc.ParentPID, proc.User, proc.Version, proc.Publisher, proc.Signature, proc.StartTime
					result.FileSize, result.CPUPercent, result.GPUPercent, result.MemoryBytes = proc.FileSizeBytes, proc.CPUPercent, proc.GPUPercent, proc.MemoryBytes
					if result.Name == "" {
						result.Name = proc.Name
					}
					break
				}
			}
		}
	case "security":
		var found *MonitorSecurityRecord
		for i := range snapshot.Security {
			item := &snapshot.Security[i]
			if monitorRefMatchesText(ref.EventID, item.EventID) && monitorRefMatchesText(ref.Source, item.Source) && monitorRefMatchesText(ref.Path, item.Path) {
				found = item
				break
			}
		}
		if found == nil {
			return result, errors.New("selected security evidence is no longer available")
		}
		result.Name, result.Source, result.Description = filepath.Base(found.Path), found.Source, found.Message
		result.Path = firstExistingMonitorPath(found.Path)
	default:
		return result, errors.New("unsupported Monitor item type")
	}

	if result.Path != "" {
		result.Path = filepath.Clean(result.Path)
		if _, info, err := monitorFileStatIdentity(result.Path); err == nil {
			result.FileSize = info.Size()
		}
	}
	if result.PID > 0 {
		for _, conn := range snapshot.Connections {
			if conn.PID == result.PID {
				result.Connections = append(result.Connections, conn)
				if len(result.Connections) >= monitorMaxInvestigationEvents {
					break
				}
			}
		}
	}
	if result.Path != "" {
		for _, record := range snapshot.Security {
			if strings.TrimSpace(record.Path) != "" && monitorReviewKey(record.Path) == monitorReviewKey(result.Path) {
				result.Security = append(result.Security, record)
				if len(result.Security) >= monitorMaxInvestigationEvents {
					break
				}
			}
		}
	}
	return result, nil
}

func monitorItemDetail(appRoot string, item monitorResolvedItem) monitorItemDetailResponse {
	response := monitorItemDetailResponse{
		OK: true, Kind: item.Ref.Kind, Name: item.Name, PID: item.PID, ParentPID: item.ParentPID,
		Path: item.Path, Command: item.Command, Source: item.Source, Description: item.Description,
		FileSizeBytes: item.FileSize, Version: item.Version, Publisher: item.Publisher,
		Signature: item.Signature, User: item.User, StartTime: item.StartTime, CPUPercent: item.CPUPercent,
		GPUPercent: item.GPUPercent, MemoryBytes: item.MemoryBytes, Connections: item.Connections,
		SecurityEvidence: item.Security, ReviewState: "needs_review", VirusTotalURL: monitorVirusTotalWebsiteURL,
	}
	if item.Path == "" {
		response.ReviewState = "no_file"
		return response
	}
	identity, _, err := monitorFileStatIdentity(item.Path)
	if err != nil {
		response.ReviewState = "file_unavailable"
		return response
	}
	response.FileExists = true
	key := monitorReviewKey(item.Path)
	monitorReviewMu.Lock()
	file := loadMonitorReviewFile(appRoot)
	record, ok := file.Items[key]
	if ok {
		record.LastSeen = time.Now().Format(time.RFC3339)
		file.Items[key] = record
		_ = saveMonitorReviewFile(appRoot, file)
	}
	monitorReviewMu.Unlock()
	if ok {
		response.FirstSeen, response.LastSeen, response.LastReviewed, response.UserKnown = record.FirstSeen, record.LastSeen, record.LastReviewed, record.UserKnown
		if record.Identity != (monitorFileIdentity{}) && record.Identity != identity {
			response.ReviewState = "changed_since_review"
			response.PreviousSHA256 = record.SHA256
		} else {
			response.SHA256 = record.SHA256
			if record.UserKnown {
				response.ReviewState = "user_known"
			} else if strings.TrimSpace(record.Status) != "" {
				response.ReviewState = record.Status
			}
		}
	}
	monitorHashCache.Lock()
	if cached, exists := monitorHashCache.Items[key]; exists && cached.Identity == identity {
		response.SHA256 = cached.SHA256
	}
	monitorHashCache.Unlock()
	if response.SHA256 != "" {
		response.VirusTotalReport = monitorVirusTotalReportBase + response.SHA256
	}
	return response
}

func upsertMonitorReview(appRoot string, item monitorResolvedItem, hash string, identity monitorFileIdentity, status, aiStatus, reputation string, userKnown bool) (monitorReviewRecord, error) {
	if item.Path == "" {
		return monitorReviewRecord{}, errors.New("selected item has no executable file path")
	}
	now := time.Now().Format(time.RFC3339)
	key := monitorReviewKey(item.Path)
	monitorReviewMu.Lock()
	defer monitorReviewMu.Unlock()
	file := loadMonitorReviewFile(appRoot)
	record := file.Items[key]
	if record.FirstSeen == "" {
		record.FirstSeen = now
	}
	record.Path, record.Name, record.LastSeen = item.Path, item.Name, now
	if hash != "" {
		record.SHA256 = hash
	}
	if identity != (monitorFileIdentity{}) {
		record.Identity = identity
	}
	if strings.TrimSpace(status) != "" {
		record.Status = strings.TrimSpace(status)
	}
	if strings.TrimSpace(aiStatus) != "" {
		record.AIStatus = strings.TrimSpace(aiStatus)
	}
	if userKnown {
		record.UserKnown, record.Status = true, "user_known"
	}
	if status == "previously_reviewed" || userKnown {
		record.LastReviewed = now
	}
	if strings.TrimSpace(reputation) != "" {
		record.LastReputation, record.LastReputationAt = strings.TrimSpace(reputation), now
	}
	file.Items[key] = record
	return record, saveMonitorReviewFile(appRoot, file)
}

// recordMonitorHashCheck does not erase a prior reviewed identity when a binary
// changed. That old identity is exactly what lets Monitor say "changed since
// review" until the user or AI explicitly reviews the new binary.
func recordMonitorHashCheck(appRoot string, item monitorResolvedItem, hash string, identity monitorFileIdentity, reputation string) error {
	key := monitorReviewKey(item.Path)
	monitorReviewMu.Lock()
	defer monitorReviewMu.Unlock()
	file := loadMonitorReviewFile(appRoot)
	record, ok := file.Items[key]
	now := time.Now().Format(time.RFC3339)
	if ok && (record.UserKnown || record.Status == "previously_reviewed") && record.Identity != (monitorFileIdentity{}) && record.Identity != identity {
		record.LastSeen = now
		if record.SHA256 != "" && strings.EqualFold(record.SHA256, hash) {
			// Metadata/mtime changed but content did not. Keep the reviewed state and
			// advance the cached identity so we do not create a false binary-change alert.
			record.Identity = identity
			if reputation != "" {
				record.LastReputation, record.LastReputationAt = reputation, now
			}
		}
		file.Items[key] = record
		return saveMonitorReviewFile(appRoot, file)
	}
	if record.FirstSeen == "" {
		record.FirstSeen = now
	}
	record.Path, record.Name, record.LastSeen = item.Path, item.Name, now
	record.SHA256, record.Identity = hash, identity
	if record.Status == "" {
		record.Status = "needs_review"
	}
	if reputation != "" {
		record.LastReputation, record.LastReputationAt = reputation, now
	}
	file.Items[key] = record
	return saveMonitorReviewFile(appRoot, file)
}

func monitorVirusTotalAPIKey(cfg MonitorConfig) string {
	envName := strings.TrimSpace(cfg.VirusTotalAPIKeyEnv)
	if envName == "" {
		envName = monitorDefaultVirusTotalAPIKeyEnv
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func lookupMonitorVirusTotal(ctx context.Context, apiKey, hash string) (monitorHashReputation, error) {
	result := monitorHashReputation{Provider: "VirusTotal", Configured: strings.TrimSpace(apiKey) != "", ReportURL: monitorVirusTotalReportBase + hash}
	if strings.TrimSpace(apiKey) == "" {
		result.Message = "VirusTotal API key is not configured. SHA-256 was calculated locally; Open VirusTotal for manual research."
		return result, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(monitorVirusTotalAPIBaseURL, "/")+"/files/"+hash, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("x-apikey", apiKey)
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		result.Message = "VirusTotal does not currently have a report for this hash."
		return result, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, monitorMaxHashResponseBytes))
	if err != nil {
		return result, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("VirusTotal lookup failed: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			Attributes struct {
				MeaningfulName   string         `json:"meaningful_name"`
				TypeDescription  string         `json:"type_description"`
				Reputation       int            `json:"reputation"`
				LastAnalysisDate int64          `json:"last_analysis_date"`
				Stats            map[string]int `json:"last_analysis_stats"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return result, errors.New("VirusTotal returned an unreadable response")
	}
	stats := payload.Data.Attributes.Stats
	result.Known = true
	result.Malicious, result.Suspicious, result.Harmless, result.Undetected = stats["malicious"], stats["suspicious"], stats["harmless"], stats["undetected"]
	result.Timeout = stats["timeout"] + stats["confirmed-timeout"]
	result.Reputation, result.MeaningfulName, result.TypeDescription = payload.Data.Attributes.Reputation, payload.Data.Attributes.MeaningfulName, payload.Data.Attributes.TypeDescription
	if payload.Data.Attributes.LastAnalysisDate > 0 {
		result.LastAnalysisDate = time.Unix(payload.Data.Attributes.LastAnalysisDate, 0).UTC().Format(time.RFC3339)
	}
	result.Message = fmt.Sprintf("VirusTotal report: %d malicious, %d suspicious, %d undetected.", result.Malicious, result.Suspicious, result.Undetected)
	return result, nil
}

func monitorReputationSummary(rep monitorHashReputation) string {
	if !rep.Configured {
		return "hash-only; VirusTotal API not configured"
	}
	if !rep.Known {
		return "VirusTotal: hash unknown"
	}
	return fmt.Sprintf("VirusTotal: malicious=%d suspicious=%d harmless=%d undetected=%d", rep.Malicious, rep.Suspicious, rep.Harmless, rep.Undetected)
}

func monitorItemEvidence(detail monitorItemDetailResponse, reputation string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SELECTED ITEM\nkind=%s\nname=%s\npid=%d\nparent_pid=%d\npath=%s\ncommand=%s\nsource=%s\ndescription=%s\nfile_size=%d\nsha256=%s\nprevious_sha256=%s\nreview_state=%s\nuser_known=%t\nversion=%s\npublisher=%s\nsignature=%s\nuser=%s\nstart_time=%s\ncpu=%s\ngpu=%s\nram=%s\n", detail.Kind, emptyMonitorValue(detail.Name), detail.PID, detail.ParentPID, emptyMonitorValue(detail.Path), emptyMonitorValue(detail.Command), emptyMonitorValue(detail.Source), emptyMonitorValue(detail.Description), detail.FileSizeBytes, emptyMonitorValue(detail.SHA256), emptyMonitorValue(detail.PreviousSHA256), detail.ReviewState, detail.UserKnown, emptyMonitorValue(detail.Version), emptyMonitorValue(detail.Publisher), emptyMonitorValue(detail.Signature), emptyMonitorValue(detail.User), emptyMonitorValue(detail.StartTime), formatMonitorPercent(detail.CPUPercent), formatMonitorPercent(detail.GPUPercent), formatMonitorBytes(detail.MemoryBytes))
	if strings.TrimSpace(reputation) != "" {
		fmt.Fprintf(&b, "reputation=%s\n", reputation)
	}
	if len(detail.Connections) > 0 {
		b.WriteString("\nSELECTED ITEM NETWORK\n")
		for _, conn := range detail.Connections {
			fmt.Fprintf(&b, "- %s %s:%d -> %s:%d state=%s\n", conn.Protocol, conn.LocalAddress, conn.LocalPort, conn.RemoteAddress, conn.RemotePort, conn.State)
		}
	}
	if len(detail.SecurityEvidence) > 0 {
		b.WriteString("\nSELECTED ITEM SECURITY EVIDENCE\n")
		for _, record := range detail.SecurityEvidence {
			fmt.Fprintf(&b, "- [%s] source=%s severity=%s type=%s event=%s msg=%s\n", record.Time, record.Source, record.Severity, record.Type, record.EventID, record.Message)
		}
	}
	return b.String()
}

func monitorItemContext(state monitorStateResponse, memorySelection, conversationContext, evidence string) string {
	s := state.Snapshot
	var b strings.Builder
	fmt.Fprintf(&b, "SYSTEM PROFILE\nOS=%s %s\nARCH=%s\nHOST=%s\nCPU=%s\nGPU=%s\nRAM=%s\n", s.System.OS, s.System.OSVersion, s.System.Architecture, s.System.Hostname, s.System.CPUModel, s.System.GPUModel, formatMonitorBytes(s.System.TotalMemoryBytes))
	fmt.Fprintf(&b, "\nCURRENT SYSTEM LOAD\nCPU=%s\nGPU=%s\nMemory=%s\nCPU Temp=%s\nGPU Temp=%s\nThermal State=%s\n", formatMonitorPercent(s.CPUPercent), formatMonitorPercent(s.GPUPercent), formatMonitorPercent(s.MemoryPercent), formatMonitorTemp(s.CPUTemperatureC), formatMonitorTemp(s.GPUTemperatureC), emptyMonitorValue(s.ThermalState))
	fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(evidence))
	fmt.Fprintf(&b, "\nRELEVANT MONITOR MEMORY (UGG)\n%s\n", memorySelection)
	if strings.TrimSpace(conversationContext) != "" {
		fmt.Fprintf(&b, "\nRECENT MONITOR CONVERSATION\n%s\n", conversationContext)
	}
	return b.String()
}

func (m *monitorAIManager) RunItemReview(ctx context.Context, cfg MonitorConfig, detail monitorItemDetailResponse, reputation string) (monitorAIActionResponse, error) {
	if !m.beginRun() {
		return monitorAIActionResponse{}, errors.New("another Monitor AI request is already running")
	}
	defer m.endRun()
	evidence := monitorItemEvidence(detail, reputation)
	request := "ITEM INVESTIGATION\nAnalyze only the selected item and its directly relevant evidence. Explain what it is likely to be, whether it belongs, what evidence is reassuring or concerning, and what the user should verify next. Do not call it malware merely because it is unfamiliar.\n\n" + evidence
	display := "Investigate selected item: " + detail.Name
	if detail.Path != "" {
		display += " (" + detail.Path + ")"
	}
	return m.runModelWithContext(ctx, cfg, request, display, evidence, false, false)
}

// rememberUserKnown is a compact durable UGG memory update. It is intentionally
// separate from the review database so users can clear AI memory without losing
// explicit investigation state, and vice versa.
func (m *monitorAIManager) rememberUserKnown(cfg MonitorConfig, detail monitorItemDetailResponse) error {
	if m == nil {
		return nil
	}
	line := fmt.Sprintf("user-known:%s|sha256=%s|path=%s", strings.TrimSpace(detail.Name), strings.TrimSpace(detail.SHA256), strings.TrimSpace(detail.Path))
	meta := monitorAIResponseMeta{MemoryAdd: []string{line}}
	m.mu.Lock()
	defer m.mu.Unlock()
	memory := m.loadMemory()
	updated, changed := applyMonitorMemoryDelta(memory, meta, cfg.AIMemoryMaxKB)
	if !changed {
		return nil
	}
	if err := atomicWriteFile(m.memoryPath, []byte(strings.TrimSpace(updated)+"\n"), 0o644); err != nil {
		return err
	}
	m.state.Sequence++
	m.persistStateLocked()
	return nil
}

func decodeMonitorItemRef(w http.ResponseWriter, r *http.Request) (monitorItemRef, bool) {
	var ref monitorItemRef
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&ref); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return ref, false
	}
	return ref, true
}

func (a *App) resolveMonitorItemRef(ref monitorItemRef) (monitorResolvedItem, error) {
	if a == nil || a.monitor == nil {
		return monitorResolvedItem{}, errors.New("Monitor is unavailable")
	}
	return resolveMonitorItem(a.monitor.State().Snapshot, ref)
}

func (a *App) handleMonitorItemDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, monitorItemDetail(a.appRoot, item))
}

func (a *App) handleMonitorItemHash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if item.Path == "" {
		http.Error(w, "selected item does not resolve to a local executable file", http.StatusBadRequest)
		return
	}
	hash, identity, err := computeMonitorSHA256(item.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := a.monitorConfigSnapshot()
	rep, repErr := lookupMonitorVirusTotal(r.Context(), monitorVirusTotalAPIKey(cfg), hash)
	if repErr != nil {
		rep = monitorHashReputation{Provider: "VirusTotal", Configured: monitorVirusTotalAPIKey(cfg) != "", ReportURL: monitorVirusTotalReportBase + hash, Message: repErr.Error()}
	}
	_ = recordMonitorHashCheck(a.appRoot, item, hash, identity, monitorReputationSummary(rep))
	detail := monitorItemDetail(a.appRoot, item)
	detail.SHA256, detail.VirusTotalReport = hash, monitorVirusTotalReportBase+hash
	if a.monitor != nil {
		a.monitor.RecordEvent("info", "security", "SHA-256 checked for "+item.Name+"; "+monitorReputationSummary(rep)+".")
	}
	writeJSON(w, http.StatusOK, monitorItemHashResponse{OK: true, Item: detail, SHA256: hash, Reputation: rep})
}

func (a *App) handleMonitorItemReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if item.Path == "" {
		http.Error(w, "selected item does not resolve to a local executable file", http.StatusBadRequest)
		return
	}
	if _, _, err := monitorFileStatIdentity(item.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := revealMonitorFile(item.Path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": item.Path})
}

func (a *App) handleMonitorItemAI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.monitorAI == nil {
		http.Error(w, "Monitor AI unavailable", http.StatusServiceUnavailable)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	detail := monitorItemDetail(a.appRoot, item)
	var identity monitorFileIdentity
	if item.Path != "" {
		if hash, id, hashErr := computeMonitorSHA256(item.Path); hashErr == nil {
			detail.SHA256, detail.VirusTotalReport, identity = hash, monitorVirusTotalReportBase+hash, id
		}
	}
	reputation := ""
	if item.Path != "" {
		monitorReviewMu.Lock()
		record := loadMonitorReviewFile(a.appRoot).Items[monitorReviewKey(item.Path)]
		monitorReviewMu.Unlock()
		reputation = record.LastReputation
	}
	response, err := a.monitorAI.RunItemReview(r.Context(), a.monitorConfigSnapshot(), detail, reputation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if item.Path != "" {
		_, _ = upsertMonitorReview(a.appRoot, item, detail.SHA256, identity, "previously_reviewed", response.Status, reputation, false)
	}
	if a.monitor != nil {
		a.monitor.RecordEvent("info", "ai", "AI item review completed for "+item.Name+" ("+strings.ToUpper(response.Status)+").")
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleMonitorItemKnown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if item.Path == "" {
		http.Error(w, "selected item does not resolve to a local executable file", http.StatusBadRequest)
		return
	}
	hash, identity, err := computeMonitorSHA256(item.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := upsertMonitorReview(a.appRoot, item, hash, identity, "user_known", "", "", true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail := monitorItemDetail(a.appRoot, item)
	detail.SHA256 = hash
	if a.monitorAI != nil {
		_ = a.monitorAI.rememberUserKnown(a.monitorConfigSnapshot(), detail)
	}
	if a.monitor != nil {
		a.monitor.RecordEvent("info", "security", "User marked "+item.Name+" as known for this binary hash.")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": record, "item": detail})
}

func (a *App) handleMonitorItemVirusTotal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, ok := decodeMonitorItemRef(w, r)
	if !ok {
		return
	}
	item, err := a.resolveMonitorItemRef(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if item.Path == "" {
		http.Error(w, "selected item does not resolve to a local executable file", http.StatusBadRequest)
		return
	}
	detail := monitorItemDetail(a.appRoot, item)
	if detail.SHA256 == "" {
		if hash, _, hashErr := computeMonitorSHA256(item.Path); hashErr == nil {
			detail.SHA256, detail.VirusTotalReport = hash, monitorVirusTotalReportBase+hash
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "path": item.Path, "name": item.Name, "sha256": detail.SHA256,
		"website_url": monitorVirusTotalWebsiteURL, "report_url": detail.VirusTotalReport,
		"notice": "AgentGO does not upload this file. File selection/upload on VirusTotal is entirely user controlled.",
	})
}
