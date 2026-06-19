package main

import (
	"agentgo/adapters"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type ReleaseInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

func normalizeRevisionLabel(value string) string {
	clean := strings.TrimSpace(value)
	clean = strings.TrimLeft(clean, "rR")
	if clean == "" {
		return "000"
	}
	if n, err := strconv.Atoi(clean); err == nil {
		if n < 0 {
			n = 0
		}
		return fmt.Sprintf("%03d", n)
	}
	return clean
}

// Label formats the current release as the short UI build tag.
func (r ReleaseInfo) Label() string {
	version := strings.TrimSpace(r.Version)
	if version == "" {
		version = "0.0.0"
	}
	revision := normalizeRevisionLabel(r.Revision)
	return fmt.Sprintf("AgentGO v%s · r%s", version, revision)
}

type AppConfig struct {
	AgentGOFile                 string        `json:"agentgo_file,omitempty"`
	FileVersion                 int           `json:"file_version,omitempty"`
	HTTPPort                    int           `json:"http_port"`
	HTTPSPort                   int           `json:"https_port"`
	BindHost                    string        `json:"bind_host"`
	TLSCertFile                 string        `json:"tls_cert_file"`
	TLSKeyFile                  string        `json:"tls_key_file"`
	WorkRoot                    string        `json:"work_root"`
	MaxResponseHistory          int           `json:"max_response_history"`
	RiskModeMaxIterations       int           `json:"risk_mode_max_iterations"`
	OutfitRunRetention          int           `json:"outfit_run_retention"`
	PromptVersion               int           `json:"prompt_version"`
	WireTap                     WireTapLimits `json:"wiretap"`
	AutoMergeSingleBuilderWaves *bool         `json:"auto_merge_single_builder_waves,omitempty"`
	Models                      []ModelConfig `json:"-"`
}

type RequestDefaults struct {
	Temperature float64 `json:"temperature,omitempty"`
}

type ModelCapabilities struct {
	SupportsTextIn    bool `json:"supports_text_in"`
	SupportsImageIn   bool `json:"supports_image_in"`
	SupportsAudioIn   bool `json:"supports_audio_in"`
	SupportsVideoIn   bool `json:"supports_video_in"`
	SupportsFileIn    bool `json:"supports_file_in"`
	SupportsBinaryOut bool `json:"supports_binary_out"`
}

type ModelConfig struct {
	ID                     int               `json:"id"`
	Label                  string            `json:"label"`
	StrictStructuredOutput *bool             `json:"strict_structured_output,omitempty"`
	PromptMode             string            `json:"prompt_mode,omitempty"`
	UseLowWeightPrompts    bool              `json:"use_low_weight_prompts,omitempty"`
	UseUggPrompt           bool              `json:"use_ugg_prompt"`
	VideoGeneration        bool              `json:"video_generation,omitempty"`
	VideoPromptOnly        bool              `json:"video_prompt_only,omitempty"`
	VideoStartFrame        bool              `json:"video_start_frame,omitempty"`
	VideoEndFrame          bool              `json:"video_end_frame,omitempty"`
	VideoIngredients       bool              `json:"video_ingredients,omitempty"`
	VideoDuration          string            `json:"video_duration,omitempty"`
	VideoAspectRatio       string            `json:"video_aspect_ratio,omitempty"`
	VideoResolution        string            `json:"video_resolution,omitempty"`
	VideoOutputFormat      string            `json:"video_output_format,omitempty"`
	VideoFPS               string            `json:"video_fps,omitempty"`
	VideoQuality           string            `json:"video_quality,omitempty"`
	MeshGeneration         bool              `json:"mesh_generation,omitempty"`
	MeshPromptOnly         bool              `json:"mesh_prompt_only,omitempty"`
	MeshImageInput         bool              `json:"mesh_image_input,omitempty"`
	MeshMultiImage         bool              `json:"mesh_multi_image,omitempty"`
	MeshQuality            string            `json:"mesh_quality,omitempty"`
	MeshOutputFormat       string            `json:"mesh_output_format,omitempty"`
	Provider               string            `json:"provider"`
	Adapter                string            `json:"adapter"`
	WorkDir                string            `json:"work_dir"`
	APIUser                string            `json:"api_user"`
	APIPass                string            `json:"api_pass"`
	APIKey                 string            `json:"api_key"`
	APIKeyEnv              string            `json:"api_key_env,omitempty"`
	AuthType               string            `json:"auth_type"`
	AuthHeader             string            `json:"auth_header"`
	BaseURL                string            `json:"base_url"`
	APIPath                string            `json:"api_path"`
	ModelName              string            `json:"model_name"`
	Headers                map[string]string `json:"headers"`
	MaxOutputTokens        int               `json:"max_output_tokens,omitempty"`
	TimeoutSeconds         int               `json:"timeout_seconds,omitempty"`
	RequestDefaults        RequestDefaults   `json:"request_defaults"`
	ProviderOptions        map[string]any    `json:"provider_options"`
	CapabilityMode         string            `json:"capability_mode,omitempty"`
	Capabilities           ModelCapabilities `json:"capabilities"`
	RunOrder               int               `json:"run_order,omitempty"`
	MasterMindMemory       string            `json:"mastermind_memory,omitempty"`
	MasterMindIdentity     string            `json:"mastermind_identity,omitempty"`
	Notes                  string            `json:"notes"`
	CreatedAt              string            `json:"created_at"`
	UpdatedAt              string            `json:"updated_at"`
}

type ModelRegistry struct {
	AgentGOFile   string        `json:"agentgo_file,omitempty"`
	FileVersion   int           `json:"file_version,omitempty"`
	SchemaVersion int           `json:"schema_version,omitempty"`
	TopID         int           `json:"top_id"`
	Models        []ModelConfig `json:"models"`
}

func normalizeCapabilityMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual":
		return "manual"
	default:
		return "auto"
	}
}

func normalizePromptMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case promptModeNone:
		return promptModeNone
	case promptModeLow:
		return promptModeLow
	default:
		return promptModeBalanced
	}
}

func normalizeModelCapabilities(caps ModelCapabilities) ModelCapabilities {
	caps.SupportsTextIn = true
	return caps
}

func autoMergeSingleBuilderWavesEnabled(cfg AppConfig) bool {
	if cfg.AutoMergeSingleBuilderWaves == nil {
		return true
	}
	return *cfg.AutoMergeSingleBuilderWaves
}

func normalizeWireTapLimits(limits WireTapLimits) WireTapLimits {
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = wireTapDefaultMaxEntries
	}
	if limits.MaxEntries < 1 {
		limits.MaxEntries = wireTapDefaultMaxEntries
	}
	if limits.MaxEntries > wireTapHardMaxEntries {
		log.Printf("[WARN] system: wiretap.max_wiretap_entries=%d exceeds hard cap %d; using cap", limits.MaxEntries, wireTapHardMaxEntries)
		limits.MaxEntries = wireTapHardMaxEntries
	}
	if limits.DefaultRuntimeSliceEntries <= 0 {
		limits.DefaultRuntimeSliceEntries = wireTapDefaultRuntimeSliceEntries
	}
	if limits.DefaultRuntimeSliceEntries > limits.MaxEntries {
		limits.DefaultRuntimeSliceEntries = limits.MaxEntries
	}
	if limits.MaxRuntimeSliceEntries <= 0 {
		limits.MaxRuntimeSliceEntries = wireTapMaxRuntimeSliceEntries
	}
	if limits.MaxRuntimeSliceEntries < limits.DefaultRuntimeSliceEntries {
		limits.MaxRuntimeSliceEntries = limits.DefaultRuntimeSliceEntries
	}
	if limits.MaxRuntimeSliceEntries > wireTapHardMaxRuntimeSliceEntries {
		log.Printf("[WARN] system: wiretap.max_runtime_slice_entries=%d exceeds hard cap %d; using cap", limits.MaxRuntimeSliceEntries, wireTapHardMaxRuntimeSliceEntries)
		limits.MaxRuntimeSliceEntries = wireTapHardMaxRuntimeSliceEntries
	}
	if limits.MaxRuntimeSliceEntries > limits.MaxEntries {
		limits.MaxRuntimeSliceEntries = limits.MaxEntries
	}
	return limits
}

func normalizeRunOrder(value int) int {
	if value < 0 {
		return 0
	}
	if value > 99 {
		return 99
	}
	return value
}

func normalizeLoopCount(value int) int {
	return normalizeRunOrder(value)
}

func buildExecutionWaves(builders []ModelConfig) []executionWave {
	if len(builders) == 0 {
		return nil
	}
	grouped := map[int][]ModelConfig{}
	waveNumbers := make([]int, 0)
	seen := map[int]bool{}
	for _, model := range builders {
		wave := normalizeRunOrder(model.RunOrder)
		grouped[wave] = append(grouped[wave], model)
		if !seen[wave] {
			seen[wave] = true
			waveNumbers = append(waveNumbers, wave)
		}
	}
	sort.Ints(waveNumbers)
	waves := make([]executionWave, 0, len(waveNumbers))
	for _, waveNumber := range waveNumbers {
		members := grouped[waveNumber]
		sort.Slice(members, func(i, j int) bool {
			return strings.ToLower(strings.TrimSpace(members[i].Label)) < strings.ToLower(strings.TrimSpace(members[j].Label))
		})
		wave := executionWave{Number: waveNumber}
		for _, model := range members {
			wave.BuilderIDs = append(wave.BuilderIDs, modelIDString(model.ID))
			wave.BuilderLabels = append(wave.BuilderLabels, model.Label)
		}
		waves = append(waves, wave)
	}
	return waves
}

func remainingWaveNumbers(waves []executionWave, startIndex int) []int {
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex >= len(waves) {
		return nil
	}
	out := make([]int, 0, len(waves)-startIndex)
	for _, wave := range waves[startIndex:] {
		out = append(out, wave.Number)
	}
	return out
}

type App struct {
	cfg                         AppConfig
	configPath                  string
	modelsPath                  string
	modelSchemaVersion          int
	modelTopID                  int
	tmpl                        *template.Template
	release                     ReleaseInfo
	startedAt                   time.Time
	mu                          sync.RWMutex
	httpMu                      sync.Mutex
	httpActiveRequests          int
	httpActiveByRoute           map[string]int
	httpTotalRequests           uint64
	httpSlowRequests            uint64
	httpRejectedPolls           uint64
	httpAbortedRequests         uint64
	httpConns                   map[net.Conn]http.ConnState
	activeDiagnosticsStreams    int
	deadDropStatusMu            sync.Mutex
	deadDropStatusCache         map[string]deadDropStatusCacheEntry
	activeProjectName           string
	logs                        []LogEntry
	logSeq                      uint64
	toggles                     map[string]bool
	activeCancels               map[string]activeCancelEntry
	reviewerID                  string
	riskModeEnabled             bool
	riskIterationsTotal         int
	riskIterationsRemain        int
	riskOriginalPrompt          string
	riskContextFiles            []string
	riskBuilderIDs              []string
	riskCurrentIteration        int
	riskStatusTitle             string
	riskStatusLines             []string
	riskStopReason              string
	riskLastUpdated             string
	lastMergedFilesByProject    map[string][]string
	lastMergedDeletesByProject  map[string][]string
	lastMergeSummaryByProject   map[string]mergeSummary
	pendingMergeCountsByProject map[string]map[string]int
	waveExecutionsByProject     map[string]waveExecutionState
	waveStatusByProject         map[string]waveStatusState
	diagSubscribers             map[int]chan diagnosticsEntry
	diagSubscriberTopID         int
	activeOutfitRunsByProject   map[string]activeOutfitRun
	workModeSessionsByProject   map[string]workModeSessionState
	sessionTokenEstimate        tokenUsageEstimate
	currentLoopTokenEstimate    *tokenUsageBreakdown
	currentWaveTokenEstimate    *tokenUsageBreakdown
	tokenUsageRunProject        string
	tokenUsageExecutionID       string
	tokenUsageLoopLabel         string
	tokenUsageWaveLabel         string
}

type LogEntry struct {
	Seq     uint64 `json:"seq,omitempty"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Risk    bool   `json:"risk,omitempty"`
}

type statusRecordingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecordingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecordingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *statusRecordingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

const (
	defaultLogResponseLimit  = 150
	maxLogResponseLimit      = 500
	maxDeadDropStatusMatches = 10
)

const (
	pollRequestBusyRetrySeconds  = 3
	slowHTTPRequestWarningAfter  = 2 * time.Second
	deadDropStatusCacheTTL       = 15 * time.Second
	defaultHTTPReadHeaderTimeout = 10 * time.Second
	defaultHTTPIdleTimeout       = 90 * time.Second
	defaultHTTPMaxHeaderBytes    = 1 << 20
)

func pollRouteLimit(path string, method string) (int, bool) {
	if method != http.MethodGet {
		return 0, false
	}
	switch path {
	case "/api/logs", "/api/risk", "/api/wave-state", "/api/deaddrop/status":
		return 1, true
	default:
		return 0, false
	}
}

func (a *App) wrapHTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if limit, limited := pollRouteLimit(path, r.Method); limited {
			a.httpMu.Lock()
			active := a.httpActiveByRoute[path]
			if active >= limit {
				a.httpTotalRequests++
				a.httpRejectedPolls++
				a.httpMu.Unlock()
				w.Header().Set("Retry-After", strconv.Itoa(pollRequestBusyRetrySeconds))
				w.Header().Set("X-AgentGO-Poll-Skip", "busy")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			a.httpMu.Unlock()
		}

		start := time.Now()
		a.httpMu.Lock()
		a.httpTotalRequests++
		a.httpActiveRequests++
		a.httpActiveByRoute[path]++
		a.httpMu.Unlock()

		rec := &statusRecordingResponseWriter{ResponseWriter: w}
		defer func() {
			duration := time.Since(start)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			aborted := r.Context().Err() != nil
			a.httpMu.Lock()
			a.httpActiveRequests--
			if a.httpActiveRequests < 0 {
				a.httpActiveRequests = 0
			}
			if a.httpActiveByRoute[path] > 1 {
				a.httpActiveByRoute[path]--
			} else {
				delete(a.httpActiveByRoute, path)
			}
			if duration >= slowHTTPRequestWarningAfter {
				a.httpSlowRequests++
			}
			if aborted {
				a.httpAbortedRequests++
			}
			a.httpMu.Unlock()
			if duration >= slowHTTPRequestWarningAfter {
				a.logf("http", "warn", "Slow request %s %s took %s status=%d bytes=%d remote=%s aborted=%t", r.Method, path, duration.Round(time.Millisecond), status, rec.bytes, r.RemoteAddr, aborted)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func (a *App) httpConnStateHook(conn net.Conn, state http.ConnState) {
	a.httpMu.Lock()
	defer a.httpMu.Unlock()
	if state == http.StateClosed || state == http.StateHijacked {
		delete(a.httpConns, conn)
		return
	}
	a.httpConns[conn] = state
}

func (a *App) httpStateSnapshot() map[string]any {
	a.httpMu.Lock()
	defer a.httpMu.Unlock()
	activeByRoute := map[string]int{}
	for route, count := range a.httpActiveByRoute {
		activeByRoute[route] = count
	}
	connStates := map[string]int{}
	for _, state := range a.httpConns {
		connStates[state.String()]++
	}
	return map[string]any{
		"ok":                       true,
		"uptimeSeconds":            int64(time.Since(a.startedAt).Seconds()),
		"pid":                      os.Getpid(),
		"goroutines":               runtime.NumGoroutine(),
		"activeRequests":           a.httpActiveRequests,
		"activeRequestsByRoute":    activeByRoute,
		"totalRequests":            a.httpTotalRequests,
		"slowRequests":             a.httpSlowRequests,
		"abortedRequests":          a.httpAbortedRequests,
		"pollBusyRejections":       a.httpRejectedPolls,
		"activeDiagnosticsStreams": a.activeDiagnosticsStreams,
		"connectionStates":         connStates,
	}
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"uptimeSeconds": int64(time.Since(a.startedAt).Seconds()),
		"pid":           os.Getpid(),
		"goroutines":    runtime.NumGoroutine(),
		"version":       a.release.Version,
		"revision":      a.release.Revision,
	})
}

func (a *App) handleDebugHTTPState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.httpStateSnapshot())
}

func (a *App) handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if profile := pprof.Lookup("goroutine"); profile != nil {
		_ = profile.WriteTo(w, 2)
		return
	}
	_, _ = fmt.Fprintln(w, "goroutine profile unavailable")
}

func (a *App) invalidateDeadDropStatusCache(projectName string) {
	a.deadDropStatusMu.Lock()
	defer a.deadDropStatusMu.Unlock()
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		a.deadDropStatusCache = map[string]deadDropStatusCacheEntry{}
		return
	}
	delete(a.deadDropStatusCache, projectName)
}

type homeData struct {
	Models                []ModelConfig
	RiskModeMaxIterations int
	ReleaseLabel          string
}

type listDirResponse struct {
	CurrentPath string      `json:"currentPath"`
	Entries     []fileEntry `json:"entries"`
}

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

type fileResponse struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	ContentType  string `json:"contentType"`
	IsText       bool   `json:"isText"`
	ImageDataURL string `json:"imageDataUrl,omitempty"`
	PreviewKind  string `json:"previewKind,omitempty"`
	BlobURL      string `json:"blobUrl,omitempty"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
}

type fileSaveRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fileDeleteRequest struct {
	Path string `json:"path"`
}

type fileRenameRequest struct {
	Path    string `json:"path"`
	NewPath string `json:"newPath"`
}

type fileCreateRequest struct {
	ParentPath string `json:"parentPath"`
	Name       string `json:"name"`
	ItemType   string `json:"itemType"`
}

type workModeTmpWorkMergeRequest struct {
	Path string `json:"path"`
}

type workModeTmpWorkMergeResponse struct {
	OK          bool     `json:"ok"`
	MergedFiles []string `json:"mergedFiles,omitempty"`
	SourcePath  string   `json:"sourcePath,omitempty"`
	TargetPath  string   `json:"targetPath,omitempty"`
	Message     string   `json:"message,omitempty"`
}

type workModeSearchResult struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type workModeSearchResponse struct {
	Query        string                 `json:"query"`
	Results      []workModeSearchResult `json:"results"`
	TotalMatches int                    `json:"totalMatches"`
	FileCount    int                    `json:"fileCount"`
}

type deadDropRevisionInfo struct {
	Path     string `json:"path"`
	FileName string `json:"fileName"`
	Revision int    `json:"revision"`
}

type deadDropStatusResponse struct {
	Project            string                 `json:"project,omitempty"`
	HasSource          bool                   `json:"hasSource"`
	SourcePath         string                 `json:"sourcePath,omitempty"`
	SourceKind         string                 `json:"sourceKind,omitempty"`
	ProjectworkMatches []string               `json:"projectworkMatches,omitempty"`
	RevisionCount      int                    `json:"revisionCount"`
	NextRevision       int                    `json:"nextRevision"`
	Revisions          []deadDropRevisionInfo `json:"revisions,omitempty"`
}

type deadDropStatusCacheEntry struct {
	Response deadDropStatusResponse
	CachedAt time.Time
}

type deadDropSetRequest struct {
	Path string `json:"path"`
}

type deadDropExecuteRequest struct {
	Prompt        string `json:"prompt"`
	LoopCount     int    `json:"loopCount,omitempty"`
	StopScore     int    `json:"stopScore,omitempty"`
	RevisionLevel string `json:"revisionLevel,omitempty"`
}

type deadDropAIContextPayload struct {
	ImprovementsMade []string `json:"improvements_made"`
	HandoffNotes     string   `json:"handoff_notes"`
}

type deadDropResponsePayload struct {
	AgentGOTool      string   `json:"agentgo_tool,omitempty"`
	ToolVersion      int      `json:"tool_version,omitempty"`
	SchemaVersion    string   `json:"schema_version,omitempty"`
	Score            float64  `json:"score"`
	ReturnedFile     bool     `json:"returned_file"`
	ImprovementsMade []string `json:"improvements_made"`
	HandoffNotes     string   `json:"handoff_notes"`
	FileContent      *string  `json:"file_content,omitempty"`
}

type deadDropStepResult struct {
	ModelID            string
	ModelLabel         string
	Score              float64
	ReturnedFile       bool
	Dropout            bool
	Improvements       []string
	HandoffJSON        string
	NewSourcePath      string
	SnapshotPath       string
	RevisionNumber     int
	ResponseRawFile    string
	ReturnedBinaryData []byte
	ReturnedBinaryName string
	ReturnedBinaryMIME string
	Err                error
}

type knowledgeResponse struct {
	Readme     string `json:"readme"`
	Notes      string `json:"notes"`
	DefaultTab string `json:"defaultTab"`
}

type notesSaveRequest struct {
	Content string `json:"content"`
}

type projectInfo struct {
	Name         string        `json:"name"`
	LastAccessed string        `json:"lastAccessed"`
	Active       bool          `json:"active"`
	Limits       ProjectLimits `json:"limits"`
}

type projectListResponse struct {
	ActiveProject string        `json:"activeProject"`
	Projects      []projectInfo `json:"projects"`
}

type projectCreateRequest struct {
	Name   string        `json:"name"`
	Limits ProjectLimits `json:"limits"`
}

type projectSelectRequest struct {
	Name string `json:"name"`
}

type projectDeleteRequest struct {
	Name string `json:"name"`
}

type projectUpdateRequest struct {
	Name   string        `json:"name"`
	Limits ProjectLimits `json:"limits"`
}

type tokenUsageBreakdown struct {
	Label        string `json:"label,omitempty"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	HasUsage     bool   `json:"hasUsage"`
}

type tokenUsageEstimate struct {
	InputTokens  int                  `json:"inputTokens"`
	OutputTokens int                  `json:"outputTokens"`
	HasUsage     bool                 `json:"hasUsage"`
	UpdatedAt    string               `json:"updatedAt,omitempty"`
	Loop         *tokenUsageBreakdown `json:"loop,omitempty"`
	Wave         *tokenUsageBreakdown `json:"wave,omitempty"`
}

type projectImportGitRequest struct {
	RepoURL string `json:"repoUrl"`
	Branch  string `json:"branch"`
}

type projectImportURLRequest struct {
	ResourceURL string `json:"resourceUrl"`
}

type ProjectLimits struct {
	MaxFiles      int `json:"max_files"`
	MaxFileSizeKB int `json:"max_file_size_kb"`
	MaxPayloadKB  int `json:"max_payload_kb"`
}

type executeRequest struct {
	Prompt               string                       `json:"prompt,omitempty"`
	ContextFiles         []string                     `json:"contextFiles,omitempty"`
	TemporaryAttachments []temporaryAttachmentInput   `json:"temporaryAttachments,omitempty"`
	WavePrompts          map[string]string            `json:"wavePrompts,omitempty"`
	WaveContextFiles     map[string][]string          `json:"waveContextFiles,omitempty"`
	WaveMediaInputRoles  map[string]map[string]string `json:"waveMediaInputRoles,omitempty"`
	LoopCount            int                          `json:"loopCount,omitempty"`
	CypherEnabled        bool                         `json:"cypherEnabled,omitempty"`
	WireTapEnabled       bool                         `json:"wireTapEnabled,omitempty"`
	DoubleTapEnabled     bool                         `json:"doubleTapEnabled,omitempty"`
	DoubleTapCount       int                          `json:"doubleTapCount,omitempty"`
}

type temporaryAttachmentInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Data      string `json:"data,omitempty"`
	Text      string `json:"text,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type chatRequest struct {
	ModelID            string `json:"modelId"`
	Prompt             string `json:"prompt"`
	IncludeRoleContext *bool  `json:"includeRoleContext,omitempty"`
	ResponseMode       string `json:"responseMode,omitempty"`
	ProjectFile        string `json:"projectFile,omitempty"`
	UseMemory          bool   `json:"useMemory,omitempty"`
}

type promptHelperRequest struct {
	ModelID string `json:"modelId"`
	Prompt  string `json:"prompt"`
}

type roleIdeasRequest struct {
	ModelID            string `json:"modelId"`
	ProjectDescription string `json:"projectDescription"`
	HelpNeeded         string `json:"helpNeeded"`
	ThinkingStyle      string `json:"thinkingStyle"`
	ExtraNote          string `json:"extraNote"`
}

type chatResponse struct {
	Reply         string `json:"reply"`
	MemoryUpdated bool   `json:"memoryUpdated,omitempty"`
	MemoryWarning string `json:"memoryWarning,omitempty"`
}

type workModeRequest struct {
	ModelID              string                     `json:"modelId"`
	Prompt               string                     `json:"prompt"`
	SelectedFiles        []string                   `json:"selectedFiles,omitempty"`
	TemporaryAttachments []temporaryAttachmentInput `json:"temporaryAttachments,omitempty"`
	IncludeRoleContext   *bool                      `json:"includeRoleContext,omitempty"`
	ResponseMode         string                     `json:"responseMode,omitempty"`
	UseMemory            bool                       `json:"useMemory,omitempty"`
	AllowCreate          bool                       `json:"allowCreate,omitempty"`
	AllowUpdate          bool                       `json:"allowUpdate,omitempty"`
	ObserverReview       bool                       `json:"observerReview,omitempty"`
	ObserverModelID      string                     `json:"observerModelId,omitempty"`
	MaxPasses            int                        `json:"maxPasses,omitempty"`
}

type workModeMemoryRequest struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name,omitempty"`
}

type workModeMemoryFile struct {
	Name       string `json:"name"`
	FileName   string `json:"fileName"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
}

type workModeMemoryResponse struct {
	ActiveExists bool                 `json:"activeExists"`
	ActiveBytes  int64                `json:"activeBytes,omitempty"`
	Saved        []workModeMemoryFile `json:"saved"`
	Message      string               `json:"message,omitempty"`
}

type workModeResponse struct {
	Reply          string                      `json:"reply"`
	ChangedFiles   []string                    `json:"changedFiles,omitempty"`
	SkippedFiles   []string                    `json:"skippedFiles,omitempty"`
	BlockedFiles   []workModeBlockedFileOutput `json:"blockedFiles,omitempty"`
	InlineFiles    []workModeInlineFileOutput  `json:"inlineFiles,omitempty"`
	DiffFiles      []workModeDiffFile          `json:"diffFiles,omitempty"`
	AgentMessage   string                      `json:"agentMessage,omitempty"`
	MemoryUpdated  bool                        `json:"memoryUpdated,omitempty"`
	MemoryWarning  string                      `json:"memoryWarning,omitempty"`
	State          *workModeSessionState       `json:"state,omitempty"`
	ReviewMessages []workModeReviewMessage     `json:"reviewMessages,omitempty"`
}

type workModeDiffFile struct {
	Path            string `json:"path"`
	Previous        string `json:"previous"`
	Current         string `json:"current"`
	PreviousOmitted bool   `json:"previousOmitted,omitempty"`
	CurrentOmitted  bool   `json:"currentOmitted,omitempty"`
}

type workModeInlineFileOutput struct {
	Path        string `json:"path"`
	RelPath     string `json:"relPath"`
	PreviewKind string `json:"previewKind"`
	ContentType string `json:"contentType"`
	BlobURL     string `json:"blobUrl"`
}

type workModeBlockedFileOutput struct {
	Path           string `json:"path"`
	Action         string `json:"action"`
	Reason         string `json:"reason"`
	Content        string `json:"content,omitempty"`
	ContentOmitted bool   `json:"contentOmitted,omitempty"`
}

type workModeAIResponse struct {
	Reply          string            `json:"reply"`
	Files          []builderFileOp   `json:"files"`
	Artifacts      []builderArtifact `json:"artifacts,omitempty"`
	Memory         string            `json:"memory,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
	ReviewComplete bool              `json:"review_complete,omitempty"`
}

type workModeObserverResponse struct {
	Reply           string   `json:"reply"`
	HasInput        bool     `json:"has_input"`
	Recommendations []string `json:"recommendations,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

type workModeReviewMessage struct {
	Owner           string                      `json:"owner"`
	Pass            int                         `json:"pass,omitempty"`
	Reply           string                      `json:"reply"`
	HasInput        *bool                       `json:"hasInput,omitempty"`
	Recommendations []string                    `json:"recommendations,omitempty"`
	ChangedFiles    []string                    `json:"changedFiles,omitempty"`
	SkippedFiles    []string                    `json:"skippedFiles,omitempty"`
	BlockedFiles    []workModeBlockedFileOutput `json:"blockedFiles,omitempty"`
	InlineFiles     []workModeInlineFileOutput  `json:"inlineFiles,omitempty"`
}

const (
	workModeTmpWorkDirName = "tmp-work"

	workModeModeNormal         = "normal_work_mode"
	workModeModeObserverReview = "observer_review_mode"

	workModeStatusRunning            = "running"
	workModeStatusPausedAfterCurrent = "paused_after_current_call"
	workModeStatusFinalizing         = "finalizing"
	workModeStatusFinalized          = "finalized"
	workModeStatusEmergencyStopped   = "emergency_stopped"

	workModeCallOwnerNone     = "none"
	workModeCallOwnerWorker   = "worker"
	workModeCallOwnerObserver = "observer"
)

type workModeSessionState struct {
	ProjectName           string                  `json:"projectName,omitempty"`
	Mode                  string                  `json:"mode"`
	Status                string                  `json:"status"`
	WorkerID              string                  `json:"workerId,omitempty"`
	WorkerLabel           string                  `json:"workerLabel,omitempty"`
	ObserverID            string                  `json:"observerId,omitempty"`
	ObserverLabel         string                  `json:"observerLabel,omitempty"`
	ObserverReview        bool                    `json:"observerReview"`
	CurrentPass           int                     `json:"currentPass"`
	MaxPasses             int                     `json:"maxPasses"`
	LatestWorkerMessage   string                  `json:"latestWorkerMessage,omitempty"`
	LatestWorkerFileState []string                `json:"latestWorkerFileState,omitempty"`
	LatestObserverMessage string                  `json:"latestObserverMessage,omitempty"`
	ObserverHasInput      *bool                   `json:"observerHasInput,omitempty"`
	ReviewMessages        []workModeReviewMessage `json:"reviewMessages,omitempty"`
	ActiveCallOwner       string                  `json:"activeCallOwner"`
	ExecutionID           string                  `json:"executionId,omitempty"`
	InitialPrompt         string                  `json:"initialPrompt,omitempty"`
	UpdatedAt             string                  `json:"updatedAt,omitempty"`
}

type workModeSessionRequest struct {
	Action          string `json:"action"`
	WorkerModelID   string `json:"workerModelId,omitempty"`
	ObserverModelID string `json:"observerModelId,omitempty"`
	ObserverReview  bool   `json:"observerReview,omitempty"`
	MaxPasses       int    `json:"maxPasses,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
}

type workModeSessionResponse struct {
	OK      bool                 `json:"ok"`
	State   workModeSessionState `json:"state"`
	Message string               `json:"message,omitempty"`
}

type workModeRoleSelection struct {
	Worker      ModelConfig
	Observer    ModelConfig
	HasObserver bool
}

type workModeJSONErrorResponse struct {
	Error       string `json:"error"`
	Message     string `json:"message"`
	ParseError  string `json:"parseError"`
	RawResponse string `json:"rawResponse"`
}

type promptHelperResponse struct {
	RecommendedPrompt string `json:"recommended_prompt"`
	WhySafer          string `json:"why_safer"`
	Tip               string `json:"tip,omitempty"`
}

type roleIdeaBehavior struct {
	Tone       string `json:"tone"`
	Verbosity  string `json:"verbosity"`
	Creativity string `json:"creativity"`
}

type roleIdeaOption struct {
	Title               string           `json:"title"`
	Purpose             string           `json:"purpose"`
	WhyUseful           string           `json:"why_useful"`
	WhenToChoose        string           `json:"when_to_choose"`
	ThinkingType        string           `json:"thinking_type"`
	BehaviorSuggestions roleIdeaBehavior `json:"behavior_suggestions"`
}

type roleIdeasResult struct {
	AgentGoResponse struct {
		Feature string `json:"feature"`
		Status  string `json:"status"`
	} `json:"agentgo_response"`
	AIRoles []roleIdeaOption `json:"ai_roles"`
}

var allowedRoleIdeaThinkingTypes = map[string]string{
	"critical":    "critical",
	"analytical":  "analytical",
	"practical":   "practical",
	"creative":    "creative",
	"exploratory": "exploratory",
	"balanced":    "balanced",
	"strategic":   "practical",
}

var allowedRoleIdeaTones = map[string]string{
	"professional": "professional",
	"direct":       "direct",
	"concise":      "concise",
	"friendly":     "friendly",
	"teaching":     "teaching",
	"analytical":   "professional",
	"skeptical":    "professional",
}

var allowedRoleIdeaLevels = map[string]string{
	"low":    "low",
	"medium": "medium",
	"high":   "high",
}

func normalizeRoleIdeaThinkingType(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := allowedRoleIdeaThinkingTypes[clean]; ok {
		return normalized
	}
	return "balanced"
}

func normalizeRoleIdeaTone(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := allowedRoleIdeaTones[clean]; ok {
		return normalized
	}
	return "professional"
}

func normalizeRoleIdeaLevel(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := allowedRoleIdeaLevels[clean]; ok {
		return normalized
	}
	return "low"
}

func normalizeRoleIdeasResult(result *roleIdeasResult) {
	for i := range result.AIRoles {
		result.AIRoles[i].ThinkingType = normalizeRoleIdeaThinkingType(result.AIRoles[i].ThinkingType)
		result.AIRoles[i].BehaviorSuggestions.Tone = normalizeRoleIdeaTone(result.AIRoles[i].BehaviorSuggestions.Tone)
		result.AIRoles[i].BehaviorSuggestions.Verbosity = normalizeRoleIdeaLevel(result.AIRoles[i].BehaviorSuggestions.Verbosity)
		result.AIRoles[i].BehaviorSuggestions.Creativity = normalizeRoleIdeaLevel(result.AIRoles[i].BehaviorSuggestions.Creativity)
	}
}

type contextFileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	IsText bool   `json:"isText"`
}

type contextFilesResponse struct {
	Root            string             `json:"root"`
	Files           []contextFileEntry `json:"files"`
	LastMergedFiles []string           `json:"lastMergedFiles,omitempty"`
}

type WireTapLimits struct {
	MaxEntries                 int `json:"max_wiretap_entries"`
	DefaultRuntimeSliceEntries int `json:"default_runtime_slice_entries"`
	MaxRuntimeSliceEntries     int `json:"max_runtime_slice_entries"`
}

type WireTapDocument struct {
	AgentGOFile                string         `json:"agentgo_file"`
	FileVersion                int            `json:"file_version"`
	WireTapVersion             int            `json:"wiretap_version"`
	Project                    string         `json:"project"`
	ResearchTags               []string       `json:"research_tags"`
	MaxEntries                 int            `json:"max_entries"`
	DefaultRuntimeSliceEntries int            `json:"default_runtime_slice_entries"`
	MaxRuntimeSliceEntries     int            `json:"max_runtime_slice_entries"`
	CreatedAt                  string         `json:"created_at"`
	UpdatedAt                  string         `json:"updated_at"`
	CompletionStatus           string         `json:"completion_status"`
	ScopeSummary               string         `json:"scope_summary,omitempty"`
	KnownGaps                  []string       `json:"known_gaps,omitempty"`
	Entries                    []WireTapEntry `json:"entries"`
}

type WireTapEntry struct {
	ID               string          `json:"id"`
	BatchIndex       int             `json:"batch_index,omitempty"`
	BatchTarget      int             `json:"batch_target,omitempty"`
	Tags             []string        `json:"tags"`
	Kind             string          `json:"kind,omitempty"`
	Claim            string          `json:"claim"`
	Category         string          `json:"category,omitempty"`
	EvidenceSummary  string          `json:"evidence_summary,omitempty"`
	ReasoningValue   string          `json:"reasoning_value,omitempty"`
	Basis            string          `json:"basis,omitempty"`
	Certainty        string          `json:"certainty,omitempty"`
	Sources          []WireTapSource `json:"sources,omitempty"`
	SourceHints      []string        `json:"source_hints,omitempty"`
	Confidence       string          `json:"confidence"`
	Relevance        string          `json:"relevance"`
	RelatedEntries   []string        `json:"related_entries,omitempty"`
	Status           string          `json:"status,omitempty"`
	QualityTier      string          `json:"quality_tier,omitempty"`
	SourceStatus     string          `json:"source_status,omitempty"`
	EvidenceType     string          `json:"evidence_type,omitempty"`
	PrimarySourceKey string          `json:"primary_source_key,omitempty"`
	NoveltyReason    string          `json:"novelty_reason,omitempty"`
	DuplicateCheck   string          `json:"duplicate_check,omitempty"`
	LastVerified     string          `json:"last_verified,omitempty"`
	Notes            string          `json:"notes,omitempty"`
	Extra            map[string]any  `json:"extra,omitempty"`
}

type WireTapSource struct {
	Title       string `json:"title"`
	AuthorOrOrg string `json:"author_or_org,omitempty"`
	Year        string `json:"year,omitempty"`
	URLOrDOI    string `json:"url_or_doi,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

func (s *WireTapSource) UnmarshalJSON(data []byte) error {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	s.Title = rawFlexibleStringFieldCI(fields, "title")
	s.AuthorOrOrg = firstNonEmpty(
		rawFlexibleStringFieldCI(fields, "author_or_org"),
		rawFlexibleStringFieldCI(fields, "author"),
		rawFlexibleStringFieldCI(fields, "authors"),
		rawFlexibleStringFieldCI(fields, "org"),
		rawFlexibleStringFieldCI(fields, "organization"),
		rawFlexibleStringFieldCI(fields, "publisher"),
	)
	s.Year = rawFlexibleStringFieldCI(fields, "year")
	s.URLOrDOI = firstNonEmpty(
		rawFlexibleStringFieldCI(fields, "url_or_doi"),
		rawFlexibleStringFieldCI(fields, "url"),
		rawFlexibleStringFieldCI(fields, "doi"),
		rawFlexibleStringFieldCI(fields, "link"),
	)
	s.Notes = firstNonEmpty(rawFlexibleStringFieldCI(fields, "notes"), rawFlexibleStringFieldCI(fields, "note"))
	return nil
}

func (e *WireTapEntry) UnmarshalJSON(data []byte) error {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	e.ID = firstNonEmpty(rawFlexibleStringFieldCI(fields, "id"), rawFlexibleStringFieldCI(fields, "entry_id"))
	e.Tags = rawStringSliceFieldCI(fields, "tags")
	e.Kind = firstNonEmpty(rawFlexibleStringFieldCI(fields, "kind"), rawFlexibleStringFieldCI(fields, "type"))
	e.Claim = firstNonEmpty(
		rawFlexibleStringFieldCI(fields, "claim"),
		rawFlexibleStringFieldCI(fields, "claim_text"),
		rawFlexibleStringFieldCI(fields, "fact"),
		rawFlexibleStringFieldCI(fields, "finding"),
	)
	e.Category = rawFlexibleStringFieldCI(fields, "category")
	e.EvidenceSummary = firstNonEmpty(
		rawFlexibleStringFieldCI(fields, "evidence_summary"),
		rawFlexibleStringFieldCI(fields, "evidenceSummary"),
		rawFlexibleStringFieldCI(fields, "evidence"),
		rawFlexibleStringFieldCI(fields, "summary"),
	)
	e.ReasoningValue = firstNonEmpty(rawFlexibleStringFieldCI(fields, "reasoning_value"), rawFlexibleStringFieldCI(fields, "reasoningValue"), rawFlexibleStringFieldCI(fields, "reasoning"))
	e.Basis = rawFlexibleStringFieldCI(fields, "basis")
	e.Certainty = rawFlexibleStringFieldCI(fields, "certainty")
	if rawSources, ok := rawFieldCI(fields, "sources"); ok {
		e.Sources = parseWireTapSourcesFlexible(rawSources)
	} else if rawSource, ok := rawFieldCI(fields, "source"); ok {
		e.Sources = parseWireTapSourcesFlexible(rawSource)
	}
	e.SourceHints = rawStringSliceFieldCI(fields, "source_hints")
	if len(e.SourceHints) == 0 {
		e.SourceHints = rawStringSliceFieldCI(fields, "sourceHints")
	}
	e.Confidence = rawFlexibleStringFieldCI(fields, "confidence")
	e.Relevance = rawFlexibleStringFieldCI(fields, "relevance")
	e.RelatedEntries = rawStringSliceFieldCI(fields, "related_entries")
	if len(e.RelatedEntries) == 0 {
		e.RelatedEntries = rawStringSliceFieldCI(fields, "relatedEntries")
	}
	e.Status = rawFlexibleStringFieldCI(fields, "status")
	e.QualityTier = firstNonEmpty(rawFlexibleStringFieldCI(fields, "quality_tier"), rawFlexibleStringFieldCI(fields, "qualityTier"))
	e.SourceStatus = firstNonEmpty(rawFlexibleStringFieldCI(fields, "source_status"), rawFlexibleStringFieldCI(fields, "sourceStatus"))
	e.EvidenceType = firstNonEmpty(rawFlexibleStringFieldCI(fields, "evidence_type"), rawFlexibleStringFieldCI(fields, "evidenceType"))
	e.PrimarySourceKey = firstNonEmpty(rawFlexibleStringFieldCI(fields, "primary_source_key"), rawFlexibleStringFieldCI(fields, "primarySourceKey"))
	e.NoveltyReason = firstNonEmpty(rawFlexibleStringFieldCI(fields, "novelty_reason"), rawFlexibleStringFieldCI(fields, "noveltyReason"))
	e.DuplicateCheck = firstNonEmpty(rawFlexibleStringFieldCI(fields, "duplicate_check"), rawFlexibleStringFieldCI(fields, "duplicateCheck"))
	e.LastVerified = firstNonEmpty(rawFlexibleStringFieldCI(fields, "last_verified"), rawFlexibleStringFieldCI(fields, "lastVerified"))
	e.Notes = firstNonEmpty(rawFlexibleStringFieldCI(fields, "notes"), rawFlexibleStringFieldCI(fields, "note"))
	if rawExtra, ok := rawFieldCI(fields, "extra"); ok {
		var extra map[string]any
		if err := json.Unmarshal(rawExtra, &extra); err == nil {
			e.Extra = extra
		}
	}
	return nil
}

type WireTapBuildRequest struct {
	ResearchTags   string   `json:"researchTags,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	MaxEntries     int      `json:"maxEntries,omitempty"`
	RuntimeEntries int      `json:"runtimeEntries,omitempty"`
}

type WireTapBuildResponse struct {
	OK                     bool            `json:"ok"`
	Project                string          `json:"project"`
	Path                   string          `json:"path"`
	Ready                  bool            `json:"ready"`
	Enabled                bool            `json:"enabled,omitempty"`
	Created                bool            `json:"created"`
	EntryCount             int             `json:"entryCount"`
	MaxEntries             int             `json:"maxEntries"`
	RuntimeEntries         int             `json:"runtimeEntries"`
	MaxRuntimeSliceEntries int             `json:"maxRuntimeSliceEntries"`
	Status                 string          `json:"status"`
	Message                string          `json:"message"`
	WireTap                WireTapDocument `json:"wiretap"`
}

type wireTapAIResearchResponse struct {
	WireTap                WireTapDocument `json:"wiretap"`
	Status                 string          `json:"status"`
	AddedEntries           int             `json:"added_entries,omitempty"`
	TargetEntriesRequested int             `json:"target_entries_requested,omitempty"`
	EntriesReturned        int             `json:"entries_returned,omitempty"`
	ExhaustionReason       string          `json:"exhaustion_reason,omitempty"`
	Notes                  string          `json:"notes,omitempty"`
}

type wireTapSelectionResponse struct {
	RequiredEntries         []string `json:"required_entries"`
	PossiblyRelevantEntries []string `json:"possibly_relevant_entries"`
	BackgroundEntries       []string `json:"background_entries"`
	ExcludedEntries         []string `json:"excluded_entries,omitempty"`
	MissingNeededEvidence   []string `json:"missing_needed_evidence,omitempty"`
	Notes                   string   `json:"notes,omitempty"`
}

type CypherManifest struct {
	AgentGOTool           string                 `json:"agentgo_tool"`
	ToolVersion           int                    `json:"tool_version"`
	CypherVersion         int                    `json:"cypher_version"`
	Project               string                 `json:"project"`
	ProjectRoot           string                 `json:"project_root"`
	ContentDomain         string                 `json:"content_domain"`
	CreatedBy             string                 `json:"created_by"`
	GeneratorVersion      string                 `json:"generator_version"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
	TextEncoding          string                 `json:"text_encoding"`
	PositionEncoding      string                 `json:"position_encoding"`
	FileCount             int                    `json:"file_count"`
	TransferableFileCount int                    `json:"transferable_file_count"`
	TokenEstimate         int                    `json:"token_estimate"`
	Summary               string                 `json:"summary"`
	Instructions          string                 `json:"instructions"`
	LastBuilderSelection  CypherBuilderSelection `json:"last_builder_selection,omitempty"`
	DirectoryStructure    []string               `json:"directory_structure"`
	Files                 []CypherFileEntry      `json:"files"`
	ExternalSymbols       []CypherAnchor         `json:"external_symbols"`
	Git                   CypherGitInfo          `json:"git"`
}

type CypherBuilderSelection struct {
	SummaryBuilderID    string `json:"summary_builder_id,omitempty"`
	SummaryBuilderLabel string `json:"summary_builder_label,omitempty"`
	WorkBuilderID       string `json:"work_builder_id,omitempty"`
	WorkBuilderLabel    string `json:"work_builder_label,omitempty"`
}

type CypherImportance struct {
	Inference int `json:"inference"`
	Search    int `json:"search"`
}

func (manifest *CypherManifest) UnmarshalJSON(data []byte) error {
	type cypherManifestAlias CypherManifest
	var parsed cypherManifestAlias
	aux := struct {
		CypherVersion json.RawMessage `json:"cypher_version"`
		*cypherManifestAlias
	}{
		cypherManifestAlias: (*cypherManifestAlias)(&parsed),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	version, err := parseCypherVersionValue(aux.CypherVersion)
	if err != nil {
		return err
	}
	parsed.CypherVersion = version
	*manifest = CypherManifest(parsed)
	enforceCypherManifestIdentity(manifest)
	return nil
}

func parseCypherVersionValue(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	var n int
	if err := json.Unmarshal(trimmed, &n); err == nil {
		return n, nil
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err == nil {
		if f == float64(int(f)) {
			return int(f), nil
		}
		return 0, fmt.Errorf("cypher_version must be a whole number, got %v", f)
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		if version, ok := normalizeCypherVersionString(text); ok {
			return version, nil
		}
		return 0, fmt.Errorf("cypher_version has unsupported string value %q", strings.TrimSpace(text))
	}
	return 0, errors.New("cypher_version must be a number or supported version string")
}

func normalizeCypherVersionString(value string) (int, bool) {
	if strings.TrimSpace(value) == strconv.Itoa(agentGOToolVersion) {
		return agentGOToolVersion, true
	}
	return 0, false
}

func parseCypherImportanceValue(raw json.RawMessage) CypherImportance {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return CypherImportance{}
	}
	var parsed CypherImportance
	if err := json.Unmarshal(trimmed, &parsed); err == nil {
		return normalizeCypherImportance(parsed)
	}
	// Legacy r269 and earlier stored importance as a single integer. Treat it as
	// obsolete runtime state and reset into the new grouped importance block.
	var legacy int
	if err := json.Unmarshal(trimmed, &legacy); err == nil {
		return CypherImportance{}
	}
	return CypherImportance{}
}

func normalizeCypherImportance(value CypherImportance) CypherImportance {
	value.Inference = clampCypherImportanceScore(value.Inference)
	value.Search = clampCypherImportanceScore(value.Search)
	return value
}

func clampCypherImportanceScore(value int) int {
	if value < 0 {
		return 0
	}
	if value > 5 {
		return 5
	}
	return value
}

type CypherFileEntry struct {
	Path            string           `json:"path"`
	Language        string           `json:"language"`
	ContentKind     string           `json:"content_kind"`
	Kind            string           `json:"kind"`
	TransferAllowed bool             `json:"transfer_allowed"`
	Excluded        bool             `json:"excluded"`
	ExclusionSource string           `json:"exclusion_source,omitempty"`
	ExcludeReason   string           `json:"exclude_reason"`
	NeverSend       bool             `json:"never_send"`
	SizeBytes       int64            `json:"size_bytes"`
	Hash            string           `json:"hash"`
	LastModified    string           `json:"last_modified"`
	TokenEstimate   int              `json:"token_estimate"`
	Summary         string           `json:"summary"`
	SummaryStatus   string           `json:"summary_status"`
	AIReviewed      bool             `json:"ai_reviewed"`
	Importance      CypherImportance `json:"importance"`
	Anchors         []string         `json:"anchors"`
	Symbols         []string         `json:"symbols,omitempty"`
	Continuity      CypherContinuity `json:"continuity"`
	Dependencies    []string         `json:"dependencies"`
	ReferencedBy    []string         `json:"referenced_by"`
}

func (file *CypherFileEntry) UnmarshalJSON(data []byte) error {
	var parsed struct {
		Path            string           `json:"path"`
		Language        string           `json:"language"`
		ContentKind     string           `json:"content_kind"`
		Kind            string           `json:"kind"`
		TransferAllowed bool             `json:"transfer_allowed"`
		Excluded        bool             `json:"excluded"`
		ExclusionSource string           `json:"exclusion_source,omitempty"`
		ExcludeReason   string           `json:"exclude_reason"`
		NeverSend       bool             `json:"never_send"`
		SizeBytes       int64            `json:"size_bytes"`
		Hash            string           `json:"hash"`
		LastModified    string           `json:"last_modified"`
		TokenEstimate   int              `json:"token_estimate"`
		Summary         json.RawMessage  `json:"summary"`
		SummaryStatus   string           `json:"summary_status"`
		AIReviewed      bool             `json:"ai_reviewed"`
		Importance      json.RawMessage  `json:"importance"`
		Anchors         json.RawMessage  `json:"anchors"`
		Symbols         []string         `json:"symbols,omitempty"`
		Continuity      CypherContinuity `json:"continuity"`
		Dependencies    []string         `json:"dependencies"`
		ReferencedBy    []string         `json:"referenced_by"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	summary, err := parseCypherTextField(parsed.Summary)
	if err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	parsedAnchors, err := parseCypherStringListField(parsed.Anchors)
	if err != nil {
		return fmt.Errorf("anchors: %w", err)
	}
	*file = CypherFileEntry{
		Path:            parsed.Path,
		Language:        parsed.Language,
		ContentKind:     parsed.ContentKind,
		Kind:            parsed.Kind,
		TransferAllowed: parsed.TransferAllowed,
		Excluded:        parsed.Excluded,
		ExclusionSource: parsed.ExclusionSource,
		ExcludeReason:   parsed.ExcludeReason,
		NeverSend:       parsed.NeverSend,
		SizeBytes:       parsed.SizeBytes,
		Hash:            parsed.Hash,
		LastModified:    parsed.LastModified,
		TokenEstimate:   parsed.TokenEstimate,
		Summary:         summary,
		SummaryStatus:   normalizeCypherSummaryStatus(parsed.SummaryStatus),
		AIReviewed:      parsed.AIReviewed,
		Importance:      parseCypherImportanceValue(parsed.Importance),
		Anchors:         normalizeCypherStringList(parsedAnchors),
		Symbols:         normalizeCypherStringList(parsed.Symbols),
		Continuity:      nonNilCypherContinuity(parsed.Continuity),
		Dependencies:    parsed.Dependencies,
		ReferencedBy:    parsed.ReferencedBy,
	}
	return nil
}

func parseCypherTextField(value json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err == nil {
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			partText, err := parseCypherTextPart(part)
			if err != nil {
				return "", err
			}
			partText = strings.TrimSpace(partText)
			if partText != "" {
				out = append(out, partText)
			}
		}
		return strings.Join(out, "; "), nil
	}
	return "", errors.New("expected string or array")
}

func parseCypherTextPart(value json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func parseCypherStringListField(value json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []string{}, nil
	}
	var single string
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return normalizeCypherStringList([]string{single}), nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawItems); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rawItems))
	for _, rawItem := range rawItems {
		text, err := parseCypherStringListItem(rawItem)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return normalizeCypherStringList(out), nil
}

func parseCypherStringListItem(value json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var shaped struct {
		Name        string   `json:"name"`
		Kind        string   `json:"kind"`
		Summary     string   `json:"summary"`
		Description string   `json:"description"`
		Signature   string   `json:"signature"`
		Path        string   `json:"path"`
		Facts       []string `json:"facts"`
	}
	if err := json.Unmarshal(trimmed, &shaped); err == nil {
		name := strings.TrimSpace(firstNonEmpty(shaped.Name, shaped.Path, shaped.Signature))
		summary := strings.TrimSpace(firstNonEmpty(shaped.Summary, shaped.Description))
		kind := strings.TrimSpace(shaped.Kind)
		parts := []string{}
		if kind != "" && name != "" && !strings.EqualFold(kind, "concept") {
			parts = append(parts, kind+": "+name)
		} else if name != "" {
			parts = append(parts, name)
		}
		if summary != "" {
			if len(parts) == 0 {
				parts = append(parts, summary)
			} else {
				parts[0] = parts[0] + ": " + summary
			}
		}
		for _, fact := range shaped.Facts {
			if clean := strings.TrimSpace(fact); clean != "" {
				parts = append(parts, clean)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; "), nil
		}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func normalizeCypherStringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if clean == "" {
			continue
		}
		if len([]rune(clean)) > 240 {
			runes := []rune(clean)
			clean = strings.TrimSpace(string(runes[:240])) + "…"
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
		if len(out) >= 80 {
			break
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

type CypherAnchor struct {
	Name       string           `json:"name"`
	Kind       string           `json:"kind"`
	Signature  string           `json:"signature,omitempty"`
	LineStart  int              `json:"line_start,omitempty"`
	LineEnd    int              `json:"line_end,omitempty"`
	Summary    string           `json:"summary"`
	Locations  []CypherLocation `json:"locations,omitempty"`
	Importance int              `json:"importance,omitempty"`
}

func (anchor *CypherAnchor) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*anchor = CypherAnchor{}
		return nil
	}

	var name string
	if err := json.Unmarshal(trimmed, &name); err == nil {
		*anchor = CypherAnchor{
			Name:    strings.TrimSpace(name),
			Kind:    "concept",
			Summary: "",
		}
		return nil
	}

	type cypherAnchorAlias CypherAnchor
	var parsed cypherAnchorAlias
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return err
	}
	parsed.Name = strings.TrimSpace(parsed.Name)
	parsed.Kind = strings.TrimSpace(parsed.Kind)
	if parsed.Kind == "" {
		parsed.Kind = "concept"
	}
	parsed.Signature = strings.TrimSpace(parsed.Signature)
	parsed.Summary = strings.TrimSpace(parsed.Summary)
	for idx := range parsed.Locations {
		parsed.Locations[idx].Path = filepath.ToSlash(strings.TrimSpace(parsed.Locations[idx].Path))
	}
	*anchor = CypherAnchor(parsed)
	return nil
}

type CypherLocation struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

type CypherContinuity struct {
	Characters     []string `json:"characters"`
	Relationships  []string `json:"relationships"`
	TimelineEvents []string `json:"timeline_events"`
	Locations      []string `json:"locations"`
	Rules          []string `json:"rules"`
	Contradictions []string `json:"contradictions"`
}

func (continuity *CypherContinuity) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*continuity = emptyCypherContinuity()
		return nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		clean := strings.TrimSpace(text)
		parsed := emptyCypherContinuity()
		if clean != "" {
			parsed.Rules = append(parsed.Rules, clean)
		}
		*continuity = parsed
		return nil
	}
	if items, err := parseCypherStringListField(trimmed); err == nil && len(items) > 0 && bytes.HasPrefix(trimmed, []byte("[")) {
		parsed := emptyCypherContinuity()
		parsed.Rules = items
		*continuity = parsed
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	parsed := emptyCypherContinuity()
	if value, ok := object["characters"]; ok {
		items, err := parseCypherStringListField(value)
		if err != nil {
			return fmt.Errorf("characters: %w", err)
		}
		parsed.Characters = items
	}
	if value, ok := object["relationships"]; ok {
		relationships, err := parseCypherRelationshipStringListField(value)
		if err != nil {
			return fmt.Errorf("relationships: %w", err)
		}
		parsed.Relationships = relationships
	}
	if value, ok := object["timeline_events"]; ok {
		events, err := parseCypherStringListField(value)
		if err != nil {
			return fmt.Errorf("timeline_events: %w", err)
		}
		parsed.TimelineEvents = events
	}
	if value, ok := object["locations"]; ok {
		items, err := parseCypherStringListField(value)
		if err != nil {
			return fmt.Errorf("locations: %w", err)
		}
		parsed.Locations = items
	}
	if value, ok := object["rules"]; ok {
		items, err := parseCypherStringListField(value)
		if err != nil {
			return fmt.Errorf("rules: %w", err)
		}
		parsed.Rules = items
	}
	if value, ok := object["contradictions"]; ok {
		items, err := parseCypherStringListField(value)
		if err != nil {
			return fmt.Errorf("contradictions: %w", err)
		}
		parsed.Contradictions = items
	}
	*continuity = nonNilCypherContinuity(parsed)
	return nil
}

func parseCypherRelationshipStringListField(value json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []string{}, nil
	}
	var single string
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return normalizeCypherStringList([]string{single}), nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawItems); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rawItems))
	for _, rawItem := range rawItems {
		text, err := parseCypherRelationshipStringItem(rawItem)
		if err != nil {
			return nil, err
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return normalizeCypherStringList(out), nil
}

func parseCypherRelationshipStringItem(value json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var shaped struct {
		Source       string `json:"source"`
		Target       string `json:"target"`
		Summary      string `json:"summary"`
		Relationship string `json:"relationship"`
		Description  string `json:"description"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(trimmed, &shaped); err == nil {
		source := strings.TrimSpace(shaped.Source)
		target := strings.TrimSpace(shaped.Target)
		summary := strings.TrimSpace(firstNonEmpty(shaped.Summary, shaped.Relationship, shaped.Description, shaped.Name))
		if source != "" || target != "" || summary != "" {
			return formatCypherRelationshipString(source, target, summary), nil
		}
	}
	var values map[string]string
	if err := json.Unmarshal(trimmed, &values); err == nil {
		parts := make([]string, 0, len(values))
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cleanKey := strings.TrimSpace(key)
			cleanValue := strings.TrimSpace(values[key])
			if cleanKey == "" && cleanValue == "" {
				continue
			}
			if cleanKey == "" {
				parts = append(parts, cleanValue)
			} else if cleanValue == "" {
				parts = append(parts, cleanKey)
			} else {
				parts = append(parts, cleanKey+": "+cleanValue)
			}
		}
		return strings.Join(parts, "; "), nil
	}
	return "", errors.New("expected relationship string or object")
}

func formatCypherRelationshipString(source, target, summary string) string {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	summary = strings.TrimSpace(summary)
	if source != "" && target != "" && summary != "" {
		return source + " -> " + target + ": " + summary
	}
	if source != "" && target != "" {
		return source + " -> " + target
	}
	if source != "" && summary != "" && strings.EqualFold(source, summary) {
		return source
	}
	if source != "" && summary != "" {
		return source + ": " + summary
	}
	if target != "" && summary != "" {
		return target + ": " + summary
	}
	if summary != "" {
		return summary
	}
	if source != "" {
		return source
	}
	return target
}

type CypherContinuityItem struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Facts   []string `json:"facts,omitempty"`
}

func (item *CypherContinuityItem) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*item = CypherContinuityItem{}
		return nil
	}
	var name string
	if err := json.Unmarshal(trimmed, &name); err == nil {
		*item = CypherContinuityItem{Name: strings.TrimSpace(name)}
		return nil
	}
	type cypherContinuityItemAlias CypherContinuityItem
	var parsed cypherContinuityItemAlias
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return err
	}
	parsed.Name = strings.TrimSpace(parsed.Name)
	parsed.Summary = strings.TrimSpace(parsed.Summary)
	for idx := range parsed.Facts {
		parsed.Facts[idx] = strings.TrimSpace(parsed.Facts[idx])
	}
	*item = CypherContinuityItem(parsed)
	return nil
}

type CypherTimelineEvent struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Order   int    `json:"order,omitempty"`
}

func (event *CypherTimelineEvent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*event = CypherTimelineEvent{}
		return nil
	}
	var name string
	if err := json.Unmarshal(trimmed, &name); err == nil {
		*event = CypherTimelineEvent{Name: strings.TrimSpace(name)}
		return nil
	}
	type cypherTimelineEventAlias CypherTimelineEvent
	var parsed cypherTimelineEventAlias
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return err
	}
	parsed.Name = strings.TrimSpace(parsed.Name)
	parsed.Summary = strings.TrimSpace(parsed.Summary)
	*event = CypherTimelineEvent(parsed)
	return nil
}

type CypherGitInfo struct {
	Branch     string              `json:"branch"`
	LastCommit string              `json:"last_commit"`
	RecentLogs []CypherGitLogEntry `json:"recent_logs"`
}

type CypherGitLogEntry struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
	Date    string `json:"date"`
}

type CypherBuildRequest struct {
	SummaryBuilderID string `json:"summaryBuilderId"`
	WorkBuilderID    string `json:"workBuilderId"`
	SummaryOnly      bool   `json:"summaryOnly"`
}

type CypherBuildResponse struct {
	OK                   bool                   `json:"ok"`
	Project              string                 `json:"project"`
	Path                 string                 `json:"path"`
	Ready                bool                   `json:"ready"`
	Enabled              bool                   `json:"enabled,omitempty"`
	Created              bool                   `json:"created"`
	FileNamesChanged     bool                   `json:"fileNamesChanged"`
	FileCount            int                    `json:"fileCount"`
	TransferableCount    int                    `json:"transferableCount"`
	Message              string                 `json:"message"`
	LastBuilderSelection CypherBuilderSelection `json:"lastBuilderSelection,omitempty"`
	Manifest             CypherManifest         `json:"manifest"`
}

type CypherStatusResponse struct {
	OK                   bool                   `json:"ok"`
	Ready                bool                   `json:"ready"`
	Path                 string                 `json:"path,omitempty"`
	FileCount            int                    `json:"fileCount,omitempty"`
	TransferableCount    int                    `json:"transferableCount,omitempty"`
	LastBuilderSelection CypherBuilderSelection `json:"lastBuilderSelection,omitempty"`
	Error                string                 `json:"error,omitempty"`
}

type executeResponse struct {
	Started          []string `json:"started"`
	Skipped          []string `json:"skipped"`
	WaveStarted      int      `json:"waveStarted,omitempty"`
	TotalWaves       int      `json:"totalWaves,omitempty"`
	RemainingWaves   []int    `json:"remainingWaves,omitempty"`
	QueuedBuilders   []string `json:"queuedBuilders,omitempty"`
	ContextFilesUsed int      `json:"contextFilesUsed,omitempty"`
}

type executionWave struct {
	Number        int      `json:"number"`
	BuilderIDs    []string `json:"builderIds,omitempty"`
	BuilderLabels []string `json:"builderLabels,omitempty"`
}

type waveExecutionState struct {
	ProjectName             string                     `json:"projectName"`
	ExecutionID             string                     `json:"executionId,omitempty"`
	RootPrompt              string                     `json:"rootPrompt"`
	ContextFiles            []string                   `json:"contextFiles"`
	TemporaryAttachments    []temporaryAttachmentInput `json:"temporaryAttachments,omitempty"`
	WireTapEnabled          bool                       `json:"wireTapEnabled,omitempty"`
	WavePrompts             map[int]string             `json:"wavePrompts,omitempty"`
	WaveContextFiles        map[int][]string           `json:"waveContextFiles,omitempty"`
	WaveMediaInputRoles     map[int]map[string]string  `json:"waveMediaInputRoles,omitempty"`
	Waves                   []executionWave            `json:"waves"`
	CurrentIndex            int                        `json:"currentIndex"`
	CurrentWave             int                        `json:"currentWave"`
	CurrentPromptSource     string                     `json:"currentPromptSource,omitempty"`
	CurrentContextFilesUsed int                        `json:"currentContextFilesUsed"`
	LoopCount               int                        `json:"loopCount,omitempty"`
	LoopsRemaining          int                        `json:"loopsRemaining,omitempty"`
	CycleNumber             int                        `json:"cycleNumber,omitempty"`
	AwaitingMerge           bool                       `json:"awaitingMerge"`
	AIContextBaselines      map[string]string          `json:"-"`
	StartedAt               string                     `json:"startedAt,omitempty"`
}

type waveStatusState struct {
	ProjectName         string `json:"projectName,omitempty"`
	Visible             bool   `json:"visible"`
	CurrentWave         int    `json:"currentWave,omitempty"`
	CurrentWavePosition int    `json:"currentWavePosition,omitempty"`
	TotalWaves          int    `json:"totalWaves,omitempty"`
	CurrentLoop         int    `json:"currentLoop,omitempty"`
	TotalLoops          int    `json:"totalLoops,omitempty"`
	State               string `json:"state,omitempty"`
	Detail              string `json:"detail,omitempty"`
	PromptSource        string `json:"promptSource,omitempty"`
	ContextFilesUsed    int    `json:"contextFilesUsed"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
}

type activeCancelEntry struct {
	ExecutionID string
	ProjectName string
	Cancel      context.CancelFunc
}

type diagnosticsFileRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type diagnosticsEntry struct {
	ID               string               `json:"id"`
	Mode             string               `json:"mode"`
	Target           string               `json:"target"`
	ModelID          string               `json:"modelId,omitempty"`
	ModelLabel       string               `json:"modelLabel"`
	Stage            string               `json:"stage"`
	Project          string               `json:"project,omitempty"`
	WaveNumber       int                  `json:"waveNumber"`
	PromptSource     string               `json:"promptSource,omitempty"`
	ContextFilesUsed int                  `json:"contextFilesUsed"`
	Files            []diagnosticsFileRef `json:"files,omitempty"`
	Prompt           string               `json:"prompt,omitempty"`
	SystemPrompt     string               `json:"systemPrompt,omitempty"`
	ReviewInputs     []string             `json:"reviewInputs,omitempty"`
	Candidates       []string             `json:"candidates,omitempty"`
	Response         string               `json:"response,omitempty"`
	ResponseLabel    string               `json:"responseLabel,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	StatusMessage    string               `json:"statusMessage,omitempty"`
}

type reviewerDiagnosticsMeta struct {
	Files         []diagnosticsFileRef
	ReviewInputs  []string
	Candidates    []string
	StatusMessage string
}

type requestManifestEntry struct {
	Path      string `json:"path"`
	MIMEType  string `json:"mime_type,omitempty"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Editable  bool   `json:"editable"`
	Transport string `json:"transport"`
	Reason    string `json:"reason,omitempty"`
}

type multimodalAssemblyReport struct {
	UsedPaths         []string
	TextFiles         int
	ImageFiles        int
	AudioFiles        int
	VideoFiles        int
	NativeFileFiles   int
	ManifestOnlyFiles int
	SkippedFiles      int
	Profile           adapters.TransportProfile
}

const (
	builderContextMaxTextBytes            = 1_800_000
	reviewerBaselineMaxTextBytes          = 450_000
	reviewerCandidateMaxTextBytes         = 300_000
	multimodalMaxImageBytes               = 10_000_000
	multimodalMaxImageCount               = 6
	multimodalMaxFileBytes                = 12_000_000
	multimodalMaxNativeBinaryBytes        = 20_000_000
	multimodalMaxNativeBinaryCount        = 6
	temporaryAttachmentMaxCount           = 8
	temporaryAttachmentMaxTextBytes       = 250_000
	chatProjectFileMaxBytes               = 450_000
	temporaryAttachmentMaxImageBytes      = 200_000
	temporaryAttachmentMaxImageTotalBytes = 800_000
	chatMemoryFileName                    = "memory.md"
	chatMemoryBeginMarker                 = "---BEGIN_AGENTGO_MEMORY_MD---"
	chatMemoryEndMarker                   = "---END_AGENTGO_MEMORY_MD---"
)

type modelMetaResponse struct {
	ModelID  string            `json:"modelId"`
	Project  string            `json:"project"`
	Files    map[string]string `json:"files"`
	Reviewer bool              `json:"reviewer"`
}

type riskStateResponse struct {
	Enabled             bool     `json:"enabled"`
	IterationsRemaining int      `json:"iterationsRemaining"`
	IterationsTotal     int      `json:"iterationsTotal"`
	OriginalPrompt      string   `json:"originalPrompt"`
	CurrentIteration    int      `json:"currentIteration"`
	StatusTitle         string   `json:"statusTitle"`
	StatusLines         []string `json:"statusLines"`
	StopReason          string   `json:"stopReason"`
	ShowBubble          bool     `json:"showBubble"`
}

type mergeRequest struct {
	ModelID string   `json:"modelId"`
	Files   []string `json:"files"`
}

type mergeSummary struct {
	Type        string            `json:"type"`
	SourceModel string            `json:"source_model"`
	MergeMode   string            `json:"merge_mode"`
	Files       mergeSummaryFiles `json:"files"`
	PostMerge   mergeSummaryPost  `json:"post_merge"`
	Instruction string            `json:"instruction"`
}

type mergeSummaryFiles struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
	Skipped  []string `json:"skipped"`
}

type mergeSummaryPost struct {
	ProjectworkUpdated            bool `json:"projectwork_updated"`
	ActiveBuilderProjectsResynced bool `json:"active_builder_projects_resynced"`
	AllModelProjectsResynced      bool `json:"all_model_projects_resynced,omitempty"`
}

type aiRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type aiResponse struct {
	OutputText string `json:"output_text"`
}

type diffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	DiffText  string `json:"diffText"`
	Selected  bool   `json:"selected"`
	ByteDelta int    `json:"byteDelta"`
}

type diffResponse struct {
	ModelID string     `json:"modelId"`
	Files   []diffFile `json:"files"`
}

type diffPreviewResponse struct {
	ModelID               string `json:"modelId"`
	Path                  string `json:"path"`
	Status                string `json:"status"`
	CurrentPath           string `json:"currentPath"`
	CurrentExists         bool   `json:"currentExists"`
	CurrentIsText         bool   `json:"currentIsText"`
	CurrentContentType    string `json:"currentContentType"`
	CurrentContent        string `json:"currentContent"`
	CurrentImageDataURL   string `json:"currentImageDataUrl"`
	CurrentPreviewKind    string `json:"currentPreviewKind,omitempty"`
	CurrentBlobPath       string `json:"currentBlobPath,omitempty"`
	CandidatePath         string `json:"candidatePath"`
	CandidateExists       bool   `json:"candidateExists"`
	CandidateIsText       bool   `json:"candidateIsText"`
	CandidateContentType  string `json:"candidateContentType"`
	CandidateContent      string `json:"candidateContent"`
	CandidateImageDataURL string `json:"candidateImageDataUrl"`
	CandidatePreviewKind  string `json:"candidatePreviewKind,omitempty"`
	CandidateBlobPath     string `json:"candidateBlobPath,omitempty"`
}

type builderComparePreviewResponse struct {
	LeftModelID       string `json:"leftModelId"`
	LeftModelLabel    string `json:"leftModelLabel"`
	LeftPath          string `json:"leftPath"`
	LeftExists        bool   `json:"leftExists"`
	LeftIsText        bool   `json:"leftIsText"`
	LeftContentType   string `json:"leftContentType"`
	LeftContent       string `json:"leftContent"`
	LeftImageDataURL  string `json:"leftImageDataUrl"`
	LeftPreviewKind   string `json:"leftPreviewKind,omitempty"`
	LeftBlobPath      string `json:"leftBlobPath,omitempty"`
	LeftBaseExists    bool   `json:"leftBaseExists"`
	LeftBaseIsText    bool   `json:"leftBaseIsText"`
	LeftBaseContent   string `json:"leftBaseContent"`
	RightModelID      string `json:"rightModelId"`
	RightModelLabel   string `json:"rightModelLabel"`
	RightPath         string `json:"rightPath"`
	RightExists       bool   `json:"rightExists"`
	RightIsText       bool   `json:"rightIsText"`
	RightContentType  string `json:"rightContentType"`
	RightContent      string `json:"rightContent"`
	RightImageDataURL string `json:"rightImageDataUrl"`
	RightPreviewKind  string `json:"rightPreviewKind,omitempty"`
	RightBlobPath     string `json:"rightBlobPath,omitempty"`
	RightBaseExists   bool   `json:"rightBaseExists"`
	RightBaseIsText   bool   `json:"rightBaseIsText"`
	RightBaseContent  string `json:"rightBaseContent"`
}

type builderFileOp struct {
	Path        string `json:"path"`
	Action      string `json:"action"`
	Content     string `json:"content,omitempty"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
}

func (op *builderFileOp) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*op = builderFileOp{}
		return nil
	}
	var pathOnly string
	if err := json.Unmarshal(trimmed, &pathOnly); err == nil {
		*op = builderFileOp{Path: strings.TrimSpace(pathOnly)}
		return nil
	}
	type builderFileOpAlias builderFileOp
	var parsed builderFileOpAlias
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return err
	}
	parsed.Path = strings.TrimSpace(parsed.Path)
	parsed.Action = strings.ToLower(strings.TrimSpace(parsed.Action))
	parsed.ArtifactRef = strings.TrimSpace(parsed.ArtifactRef)
	*op = builderFileOp(parsed)
	return nil
}

type builderArtifact struct {
	ID       string `json:"id"`
	Encoding string `json:"encoding"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data"`
}

type builderReturnedFile struct {
	Path        string `json:"path"`
	WorkPath    string `json:"workPath"`
	Action      string `json:"action"`
	ContentType string `json:"contentType,omitempty"`
	PreviewKind string `json:"previewKind,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	IsBinary    bool   `json:"isBinary"`
}

type builderAIContext struct {
	AgentGOFile      string   `json:"agentgo_file"`
	FileVersion      int      `json:"file_version"`
	Terminology      []string `json:"terminology"`
	Architecture     []string `json:"architecture"`
	PriorChanges     []string `json:"prior_changes"`
	KnownIssues      []string `json:"known_issues"`
	RisksConstraints []string `json:"risks_constraints"`
}

type builderReport struct {
	Status          string   `json:"status,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	ChangedFiles    []string `json:"changed_files,omitempty"`
	IssuesFound     []string `json:"issues_found,omitempty"`
	Recommendations []string `json:"recommendations,omitempty"`
	NextSteps       []string `json:"next_steps,omitempty"`
}

type builderResponse struct {
	AgentGOTool   string            `json:"agentgo_tool,omitempty"`
	ToolVersion   int               `json:"tool_version,omitempty"`
	SchemaVersion string            `json:"schema_version,omitempty"`
	Summary       string            `json:"summary"`
	Files         []builderFileOp   `json:"files"`
	Artifacts     []builderArtifact `json:"artifacts,omitempty"`
	AIContext     builderAIContext  `json:"ai_context"`
	BuilderReport builderReport     `json:"builder_report,omitempty"`
	Notes         string            `json:"notes,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	Confidence    *float64          `json:"confidence,omitempty"`
}

type builderOutputState struct {
	ModelID            string                    `json:"modelId"`
	ModelLabel         string                    `json:"modelLabel"`
	Project            string                    `json:"project"`
	HasResponse        bool                      `json:"hasResponse"`
	Unread             bool                      `json:"unread"`
	Kind               string                    `json:"kind"`
	StatusLabel        string                    `json:"statusLabel"`
	StatusMessage      string                    `json:"statusMessage"`
	Timestamp          string                    `json:"timestamp"`
	RawFile            string                    `json:"rawFile"`
	RawResponse        string                    `json:"rawResponse"`
	UserFacingResponse string                    `json:"userFacingResponse"`
	Summary            string                    `json:"summary"`
	Notes              string                    `json:"notes"`
	Warnings           []string                  `json:"warnings"`
	AIContextSummary   string                    `json:"aiContextSummary"`
	AIContextRisks     []string                  `json:"aiContextRisks"`
	AIContextNext      []string                  `json:"aiContextNext"`
	BuilderReport      builderReport             `json:"builderReport,omitempty"`
	FileCount          int                       `json:"fileCount"`
	ArtifactCount      int                       `json:"artifactCount"`
	AppliedOps         int                       `json:"appliedOps"`
	PendingCount       int                       `json:"pendingCount"`
	ReturnedFiles      []builderReturnedFile     `json:"returnedFiles,omitempty"`
	CypherRunLog       []cypherActionRunLogEntry `json:"cypherRunLog,omitempty"`
	Error              string                    `json:"error"`
}

type reviewerModelAssessment struct {
	Model      string   `json:"model"`
	Grade      int      `json:"grade"`
	Summary    string   `json:"summary"`
	Upgrades   []string `json:"upgrades"`
	Misses     []string `json:"misses"`
	MergeReady bool     `json:"merge_ready"`
}

type reviewerResponse struct {
	AgentGOTool          string                    `json:"agentgo_tool,omitempty"`
	ToolVersion          int                       `json:"tool_version,omitempty"`
	SchemaVersion        string                    `json:"schema_version,omitempty"`
	Overview             string                    `json:"overview"`
	Models               []reviewerModelAssessment `json:"models"`
	RecommendedCandidate string                    `json:"recommended_candidate"`
	Reasoning            string                    `json:"reasoning"`
	NextPrompt           string                    `json:"next_prompt"`
	AlternateNextPrompts []string                  `json:"alternate_next_prompts"`
	Confidence           *float64                  `json:"confidence,omitempty"`
}

type reviewerCandidateState struct {
	ModelID         string                `json:"modelId"`
	ModelLabel      string                `json:"modelLabel"`
	Grade           int                   `json:"grade"`
	Summary         string                `json:"summary"`
	Upgrades        []string              `json:"upgrades"`
	Misses          []string              `json:"misses"`
	MergeReady      bool                  `json:"mergeReady"`
	Recommended     bool                  `json:"recommended"`
	ObserverOmitted bool                  `json:"observerOmitted,omitempty"`
	ReturnedFiles   []builderReturnedFile `json:"returnedFiles,omitempty"`
}

type reviewerOutputState struct {
	ModelID              string                   `json:"modelId"`
	ModelLabel           string                   `json:"modelLabel"`
	Project              string                   `json:"project"`
	HasReport            bool                     `json:"hasReport"`
	Unread               bool                     `json:"unread"`
	Timestamp            string                   `json:"timestamp"`
	Overview             string                   `json:"overview"`
	Reasoning            string                   `json:"reasoning"`
	RecommendedCandidate string                   `json:"recommendedCandidate"`
	RecommendedModelID   string                   `json:"recommendedModelId"`
	FallbackNote         string                   `json:"fallbackNote,omitempty"`
	EndState             string                   `json:"endState,omitempty"`
	Confidence           *float64                 `json:"confidence,omitempty"`
	Candidates           []reviewerCandidateState `json:"candidates"`
	PromptOptions        []string                 `json:"promptOptions"`
	RawResponse          string                   `json:"rawResponse"`
}

type modelRunResult struct {
	ModelID           string
	ModelLabel        string
	Valid             bool
	AppliedOperations int
	PendingCount      int
	Err               error
}

type openAIMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIInputMessage struct {
	Role    string                 `json:"role"`
	Content []openAIMessageContent `json:"content"`
}

type openAIResponsesRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions,omitempty"`
	Input        []openAIInputMessage `json:"input,omitempty"`
	Store        bool                 `json:"store"`
	Text         map[string]any       `json:"text,omitempty"`
}

type openAIResponsesResponse struct {
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt,omitempty"`
	System string `json:"system,omitempty"`
	Format string `json:"format,omitempty"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateResponse struct {
	Model              string `json:"model"`
	CreatedAt          string `json:"created_at"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason,omitempty"`
	Context            []int  `json:"context,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int    `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
	Error              string `json:"error,omitempty"`
}

func main() {
	configPath := "config.json"
	modelsPath := "models.json"
	modelNamesPath := "model_names.json"
	releasePath := "version.json"
	cfg := loadConfig(configPath)
	applyConfigDefaults(&cfg)
	registry := loadOrInitModelRegistry(modelsPath)
	release := loadReleaseInfo(releasePath)
	cfg.Models = registry.Models
	ensureWorkDirs(cfg)
	if err := validateSystemPromptFiles(cfg); err != nil {
		log.Fatalf("system prompt setup error: %v", err)
	}

	app := &App{
		cfg:                         cfg,
		configPath:                  configPath,
		modelsPath:                  modelsPath,
		modelSchemaVersion:          registry.SchemaVersion,
		modelTopID:                  registry.TopID,
		tmpl:                        template.Must(template.ParseFiles("templates/index.html")),
		release:                     release,
		startedAt:                   time.Now(),
		toggles:                     map[string]bool{},
		httpActiveByRoute:           map[string]int{},
		httpConns:                   map[net.Conn]http.ConnState{},
		deadDropStatusCache:         map[string]deadDropStatusCacheEntry{},
		activeCancels:               map[string]activeCancelEntry{},
		lastMergedFilesByProject:    map[string][]string{},
		lastMergedDeletesByProject:  map[string][]string{},
		lastMergeSummaryByProject:   map[string]mergeSummary{},
		pendingMergeCountsByProject: map[string]map[string]int{},
		waveExecutionsByProject:     map[string]waveExecutionState{},
		waveStatusByProject:         map[string]waveStatusState{},
		diagSubscribers:             map[int]chan diagnosticsEntry{},
		activeOutfitRunsByProject:   map[string]activeOutfitRun{},
		workModeSessionsByProject:   map[string]workModeSessionState{},
	}
	for _, m := range cfg.Models {
		app.toggles[modelIDString(m.ID)] = false
	}
	if err := app.ensureOutfitsDir(); err != nil {
		log.Fatalf("could not initialize Outfits folder: %v", err)
	}
	app.startOutfitTimerLoop()
	app.logf("system", "info", "Loaded %s", app.release.Label())

	mux := http.NewServeMux()
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	mux.HandleFunc("/favicon.ico", handleFavicon)
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/api/healthz", app.handleHealthz)
	mux.HandleFunc("/api/debug/goroutines", app.handleDebugGoroutines)
	mux.HandleFunc("/api/debug/http-state", app.handleDebugHTTPState)
	mux.HandleFunc("/api/models", app.handleModels)
	mux.HandleFunc("/api/models/definitions", app.handleModelDefinitions)
	mux.HandleFunc("/api/model-names", func(w http.ResponseWriter, r *http.Request) { handleKnownModelNames(w, r, modelNamesPath) })
	mux.HandleFunc("/api/models/create", app.handleCreateModel)
	mux.HandleFunc("/api/models/update", app.handleUpdateModel)
	mux.HandleFunc("/api/models/delete", app.handleDeleteModel)
	mux.HandleFunc("/api/models/toggle", app.handleModelToggle)
	mux.HandleFunc("/api/models/run-order", app.handleModelRunOrder)
	mux.HandleFunc("/api/reviewer", app.handleReviewerToggle)
	mux.HandleFunc("/api/model-meta", app.handleModelMeta)
	mux.HandleFunc("/api/mastermind", app.handleMasterMindState)
	mux.HandleFunc("/api/mastermind/folder", app.handleMasterMindFolder)
	mux.HandleFunc("/api/mastermind/delete", app.handleMasterMindDelete)
	mux.HandleFunc("/api/mastermind/selection", app.handleMasterMindSelection)
	mux.HandleFunc("/api/video/jobs", app.handleVideoJobs)
	mux.HandleFunc("/api/video/jobs/promote", app.handleVideoJobPromote)
	mux.HandleFunc("/api/mesh/jobs", app.handleMeshJobs)
	mux.HandleFunc("/api/mesh/jobs/download", app.handleMeshJobDownload)
	mux.HandleFunc("/api/mesh/jobs/refine", app.handleMeshJobRefine)
	mux.HandleFunc("/api/mesh/jobs/promote", app.handleMeshJobPromote)
	mux.HandleFunc("/api/chat", app.handleChat)
	mux.HandleFunc("/api/work-mode/send", app.handleWorkModeSend)
	mux.HandleFunc("/api/work-mode/session", app.handleWorkModeSession)
	mux.HandleFunc("/api/work-mode/memory", app.handleWorkModeMemory)
	mux.HandleFunc("/api/work-mode/memory/new", app.handleWorkModeMemoryNew)
	mux.HandleFunc("/api/work-mode/memory/load", app.handleWorkModeMemoryLoad)
	mux.HandleFunc("/api/work-mode/memory/save", app.handleWorkModeMemorySave)
	mux.HandleFunc("/api/work-mode/memory/delete", app.handleWorkModeMemoryDelete)
	mux.HandleFunc("/api/work-mode/search", app.handleWorkModeSearch)
	mux.HandleFunc("/api/work-mode/tmp-work/merge", app.handleWorkModeTmpWorkMerge)
	mux.HandleFunc("/api/work-mode/tmp-work/merge-all", app.handleWorkModeTmpWorkMergeAll)
	mux.HandleFunc("/api/chat/prompt-helper", app.handlePromptHelper)
	mux.HandleFunc("/api/chat/role-ideas", app.handleRoleIdeas)
	mux.HandleFunc("/api/builder-output", app.handleBuilderOutput)
	mux.HandleFunc("/api/reviewer-output", app.handleReviewerOutput)
	mux.HandleFunc("/api/risk", app.handleRiskMode)
	mux.HandleFunc("/api/execute", app.handleExecute)
	mux.HandleFunc("/api/stop", app.handleStop)
	mux.HandleFunc("/api/logs", app.handleLogs)
	mux.HandleFunc("/api/logs/toast", app.handleToastLog)
	mux.HandleFunc("/api/logs/clear", app.handleClearLogs)
	mux.HandleFunc("/api/diagnostics/stream", app.handleDiagnosticsStream)
	mux.HandleFunc("/api/diagnostics/file", app.handleDiagnosticsFile)
	mux.HandleFunc("/api/files", app.handleListFiles)
	mux.HandleFunc("/api/file", app.handleReadFile)
	mux.HandleFunc("/api/file/blob", app.handleFileBlob)
	mux.HandleFunc("/api/file/save", app.handleSaveFile)
	mux.HandleFunc("/api/file/create", app.handleCreateFileItem)
	mux.HandleFunc("/api/file/rename", app.handleRenameFile)
	mux.HandleFunc("/api/file/delete", app.handleDeleteFile)
	mux.HandleFunc("/api/deaddrop/status", app.handleDeadDropStatus)
	mux.HandleFunc("/api/deaddrop/set", app.handleSetDeadDrop)
	mux.HandleFunc("/api/deaddrop/execute", app.handleDeadDropExecute)
	mux.HandleFunc("/api/context-files", app.handleContextFiles)
	mux.HandleFunc("/api/context/clear", app.handleClearRunContext)
	mux.HandleFunc("/api/cypher/status", app.handleCypherStatus)
	mux.HandleFunc("/api/cypher/build", app.handleCypherBuild)
	mux.HandleFunc("/api/wiretap/status", app.handleWireTapStatus)
	mux.HandleFunc("/api/wiretap/build", app.handleWireTapBuild)
	mux.HandleFunc("/api/project/import/upload", app.handleProjectImportUpload)
	mux.HandleFunc("/api/project/import/git", app.handleProjectImportGit)
	mux.HandleFunc("/api/project/import/url", app.handleProjectImportURL)
	mux.HandleFunc("/api/project/download", app.handleProjectDownload)
	mux.HandleFunc("/api/diff", app.handleDiff)
	mux.HandleFunc("/api/diff/preview", app.handleDiffPreview)
	mux.HandleFunc("/api/observer/compare-preview", app.handleObserverComparePreview)
	mux.HandleFunc("/api/diff/candidate/save", app.handleDiffCandidateSave)
	mux.HandleFunc("/api/diff/candidate/delete", app.handleDiffCandidateDelete)
	mux.HandleFunc("/api/merge", app.handleMerge)
	mux.HandleFunc("/api/merge/bypass", app.handleBypassMerge)
	mux.HandleFunc("/api/workflow/end", app.handleEndWorkflowEnd)
	mux.HandleFunc("/api/wave-state", app.handleWaveState)
	mux.HandleFunc("/api/outfits", app.handleOutfits)
	mux.HandleFunc("/api/outfits/create", app.handleCreateOutfit)
	mux.HandleFunc("/api/outfits/update", app.handleUpdateOutfit)
	mux.HandleFunc("/api/outfits/rename", app.handleRenameOutfit)
	mux.HandleFunc("/api/outfits/duplicate", app.handleDuplicateOutfit)
	mux.HandleFunc("/api/outfits/delete", app.handleDeleteOutfit)
	mux.HandleFunc("/api/outfits/export", app.handleExportOutfit)
	mux.HandleFunc("/api/outfits/import", app.handleImportOutfit)
	mux.HandleFunc("/api/outfits/apply", app.handleApplyOutfit)
	mux.HandleFunc("/api/outfits/triggers", app.handleUpdateOutfitTriggers)
	mux.HandleFunc("/api/outfits/runs", app.handleAPIOutfitRuns)
	mux.HandleFunc("/api/outfits/runs/meta", app.handleAPIOutfitRunMeta)
	mux.HandleFunc("/api/outfits/runs/project", app.handleAPIOutfitRunProject)
	mux.HandleFunc("/api/outfits/runs/projectwork-zip", app.handleAPIOutfitRunProjectworkZip)
	mux.HandleFunc("/api/outfits/runs/changed-files-manifest", app.handleAPIOutfitRunChangedFilesManifest)
	mux.HandleFunc("/api/outfits/runs/changed-files-zip", app.handleAPIOutfitRunChangedFilesZip)
	mux.HandleFunc("/api/outfits/runs/callback-retry", app.handleAPIOutfitRunCallbackRetry)
	mux.HandleFunc("/api/outfits/runs/deaddrop-final", app.handleAPIOutfitRunDeadDropFinal)
	mux.HandleFunc("/api/outfits/runs/deaddrop-zip", app.handleAPIOutfitRunDeadDropZip)
	mux.HandleFunc("/api/outfits/runs/delete", app.handleAPIOutfitRunDelete)
	mux.HandleFunc("/api/outfits/cron-preview", app.handleOutfitCronPreview)
	mux.HandleFunc("/api/outfits/webhook/regenerate", app.handleRegenerateOutfitWebhookKey)
	mux.HandleFunc("/outfits/", app.handleOutfitPublicAPI)
	mux.HandleFunc("/api/projects", app.handleProjects)
	mux.HandleFunc("/api/projects/create", app.handleCreateProject)
	mux.HandleFunc("/api/projects/select", app.handleSelectProject)
	mux.HandleFunc("/api/projects/delete", app.handleDeleteProject)
	mux.HandleFunc("/api/projects/update", app.handleUpdateProject)
	mux.HandleFunc("/api/session/reset", app.handleSessionReset)
	mux.HandleFunc("/api/session/usage", app.handleSessionUsage)
	mux.HandleFunc("/api/knowledge", app.handleKnowledge)
	mux.HandleFunc("/api/knowledge/notes", app.handleKnowledgeNotesSave)

	httpEnabled := cfg.HTTPPort > 0
	httpsRequested := cfg.HTTPSPort > 0
	httpsEnabled := false
	tlsReason := "HTTPS disabled by https_port=0"
	if httpsRequested {
		httpsEnabled, tlsReason = tlsListenerReady(cfg)
	}
	if !httpEnabled && !httpsEnabled {
		reasons := []string{}
		if cfg.HTTPPort <= 0 {
			reasons = append(reasons, "HTTP disabled by http_port=0")
		}
		if !httpsRequested {
			reasons = append(reasons, "HTTPS disabled by https_port=0")
		} else if !httpsEnabled {
			reasons = append(reasons, "HTTPS disabled: "+tlsReason)
		}
		log.Fatalf("AgentGO cannot start because no HTTP or HTTPS server is available. %s", strings.Join(reasons, "; "))
	}

	httpAddr := listenAddress(cfg.BindHost, cfg.HTTPPort)
	httpsAddr := listenAddress(cfg.BindHost, cfg.HTTPSPort)
	printStartupBanner(app.release, cfg, httpEnabled, httpsEnabled, tlsReason)
	handler := app.wrapHTTPHandler(mux)

	serverErrs := make(chan error, 2)
	if httpEnabled {
		go func() {
			app.logf("system", "info", "Starting HTTP server on %s", httpAddr)
			server := &http.Server{
				Addr:              httpAddr,
				Handler:           handler,
				ReadHeaderTimeout: defaultHTTPReadHeaderTimeout,
				IdleTimeout:       defaultHTTPIdleTimeout,
				MaxHeaderBytes:    defaultHTTPMaxHeaderBytes,
				ConnState:         app.httpConnStateHook,
			}
			serverErrs <- fmt.Errorf("HTTP server stopped: %w", server.ListenAndServe())
		}()
	} else {
		app.logf("system", "info", "HTTP listener disabled by http_port=0")
	}
	if httpsEnabled {
		go func() {
			app.logf("system", "info", "Starting HTTPS server on %s", httpsAddr)
			server := &http.Server{
				Addr:              httpsAddr,
				Handler:           handler,
				ReadHeaderTimeout: defaultHTTPReadHeaderTimeout,
				IdleTimeout:       defaultHTTPIdleTimeout,
				MaxHeaderBytes:    defaultHTTPMaxHeaderBytes,
				ConnState:         app.httpConnStateHook,
			}
			serverErrs <- fmt.Errorf("HTTPS server stopped: %w", server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile))
		}()
	} else if httpsRequested {
		app.logf("system", "info", "HTTPS listener disabled: %s", tlsReason)
	} else {
		app.logf("system", "info", "HTTPS listener disabled by https_port=0")
	}

	log.Fatal(<-serverErrs)
}

func listenAddress(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}

func displayServerURL(scheme, host string, port int) string {
	return scheme + "://" + listenAddress(host, port)
}

func localBrowserURL(scheme string, port int) string {
	return scheme + "://localhost:" + strconv.Itoa(port)
}

func printStartupBanner(release ReleaseInfo, cfg AppConfig, httpEnabled, httpsEnabled bool, tlsReason string) {
	lines := []string{
		"***************************************",
		"          WELCOME TO AGENTGO!         ",
		"***************************************",
		"",
		"AgentGO has started successfully.",
		"",
		"Version: " + release.Label(),
		"",
		"Server status:",
	}
	if httpEnabled {
		lines = append(lines, "  HTTP:  running at "+displayServerURL("http", cfg.BindHost, cfg.HTTPPort))
	} else {
		lines = append(lines, "  HTTP:  not running")
	}
	if httpsEnabled {
		lines = append(lines, "  HTTPS: running at "+displayServerURL("https", cfg.BindHost, cfg.HTTPSPort))
	} else {
		line := "  HTTPS: not running"
		if cfg.HTTPSPort > 0 && strings.TrimSpace(tlsReason) != "" {
			line += " (" + tlsReason + ")"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "Open AgentGO in your browser:")
	if httpEnabled {
		lines = append(lines, "  Local HTTP:  "+localBrowserURL("http", cfg.HTTPPort))
	}
	if httpsEnabled {
		lines = append(lines, "  Local HTTPS: "+localBrowserURL("https", cfg.HTTPSPort))
	}
	lines = append(lines,
		"",
		"Network access:",
		"  bind_host is set to "+cfg.BindHost+" in config.json.",
		"  0.0.0.0 is convenient for VM, LAN, Docker, and multi-device access.",
		"  For local-only access, set bind_host to 127.0.0.1 or localhost.",
		"  To bind one network device, set bind_host to that device's IP address.",
		"",
		"More information:",
		"  https://agentgo.FrostCandy.com",
		"",
	)
	fmt.Print("\n" + strings.Join(lines, "\n") + "\n")
}

func applyConfigDefaults(cfg *AppConfig) {
	cfg.AgentGOFile = agentGOFileConfig
	cfg.FileVersion = agentGOFileVersion
	cfg.BindHost = strings.TrimSpace(cfg.BindHost)
	if cfg.BindHost == "" {
		cfg.BindHost = "0.0.0.0"
	}
	cfg.TLSCertFile = strings.TrimSpace(cfg.TLSCertFile)
	cfg.TLSKeyFile = strings.TrimSpace(cfg.TLSKeyFile)
	if cfg.WorkRoot == "" {
		cfg.WorkRoot = "work"
	}
	if cfg.MaxResponseHistory <= 0 {
		cfg.MaxResponseHistory = 50
	}
	if cfg.OutfitRunRetention <= 0 {
		cfg.OutfitRunRetention = 50
	}
	if cfg.PromptVersion <= 0 {
		cfg.PromptVersion = 1
	}
	cfg.WireTap = normalizeWireTapLimits(cfg.WireTap)
	if cfg.RiskModeMaxIterations <= 0 {
		cfg.RiskModeMaxIterations = 10
	}
}

func tlsListenerReady(cfg AppConfig) (bool, string) {
	certFile := strings.TrimSpace(cfg.TLSCertFile)
	keyFile := strings.TrimSpace(cfg.TLSKeyFile)
	if certFile == "" || keyFile == "" {
		return false, "TLS cert/key not configured; starting HTTP only"
	}
	if _, err := os.Stat(certFile); err != nil {
		return false, fmt.Sprintf("TLS cert file not found: %s", certFile)
	}
	if _, err := os.Stat(keyFile); err != nil {
		return false, fmt.Sprintf("TLS key file not found: %s", keyFile)
	}
	return true, "TLS configured"
}

func loadOrInitModelRegistry(filename string) ModelRegistry {
	registry, err := readModelRegistry(filename)
	if err == nil {
		normalizeModelRegistryIdentity(&registry)
		if registry.Models == nil {
			registry.Models = []ModelConfig{}
		}
		return registry
	}
	if !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("error reading models file: %v", err)
	}
	registry = ModelRegistry{AgentGOFile: agentGOFileModels, FileVersion: agentGOFileVersion, SchemaVersion: agentGOFileVersion, TopID: 0, Models: []ModelConfig{}}
	if err := writeModelRegistry(filename, registry); err != nil {
		log.Fatalf("error initializing models file: %v", err)
	}
	return registry
}

func readModelRegistry(filename string) (ModelRegistry, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ModelRegistry{}, err
	}
	var registry ModelRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return ModelRegistry{}, err
	}
	normalizeModelRegistryIdentity(&registry)
	if registry.Models == nil {
		registry.Models = []ModelConfig{}
	}
	for i := range registry.Models {
		if registry.Models[i].Headers == nil {
			registry.Models[i].Headers = map[string]string{}
		}
		if registry.Models[i].ProviderOptions == nil {
			registry.Models[i].ProviderOptions = map[string]any{}
		}
		normalizeStoredModelDefaults(&registry.Models[i])
	}
	return registry, nil
}

func normalizeModelRegistryIdentity(registry *ModelRegistry) {
	if registry == nil {
		return
	}
	registry.AgentGOFile = agentGOFileModels
	registry.FileVersion = agentGOFileVersion
	registry.SchemaVersion = agentGOFileVersion
}

func writeModelRegistry(filename string, registry ModelRegistry) error {
	normalizeModelRegistryIdentity(&registry)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(filename, data, 0o644)
}

func adapterSupportsStrictStructuredOutput(adapter string) bool {
	switch strings.ToLower(strings.TrimSpace(adapter)) {
	case "ollama_generate":
		return true
	default:
		return false
	}
}

func defaultStrictStructuredOutput(adapter string) *bool {
	if !adapterSupportsStrictStructuredOutput(adapter) {
		return nil
	}
	v := true
	return &v
}

func normalizeStoredModelDefaults(model *ModelConfig) {
	if model == nil {
		return
	}
	if strings.TrimSpace(model.PromptMode) == "" {
		if model.UseLowWeightPrompts {
			model.PromptMode = promptModeLow
		} else {
			model.PromptMode = promptModeBalanced
		}
	} else {
		model.PromptMode = normalizePromptMode(model.PromptMode)
	}
	model.UseLowWeightPrompts = model.PromptMode == promptModeLow
	if model.StrictStructuredOutput == nil && adapterSupportsStrictStructuredOutput(model.Adapter) {
		model.StrictStructuredOutput = defaultStrictStructuredOutput(model.Adapter)
	}
}

func normalizeProtectedFolderPath(rel string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "." {
		return ""
	}
	return strings.Trim(clean, "/")
}

func isProtectedFileManagerFolder(rel string) bool {
	clean := normalizeProtectedFolderPath(rel)
	switch clean {
	case "projects", "outfits", "mastermind", "mastermind/memories", "mastermind/identities":
		return true
	}
	parts := strings.Split(clean, "/")
	if len(parts) == 2 && parts[0] == "projects" && isValidProjectName(parts[1]) {
		return true
	}
	if len(parts) == 3 && parts[0] == "projects" && isValidProjectName(parts[1]) && parts[2] == "projectwork" {
		return true
	}
	return false
}

func isProtectedFileManagerFile(rel string) bool {
	clean := normalizeProtectedFolderPath(rel)
	parts := strings.Split(clean, "/")
	return len(parts) == 3 && parts[0] == "projects" && isValidProjectName(parts[1]) && parts[2] == "project.json"
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/assets/frostcandy_logo_font.png", http.StatusTemporaryRedirect)
}

// loadReleaseInfo loads the visible app release tag from version.json.
func loadReleaseInfo(filename string) ReleaseInfo {
	release := ReleaseInfo{Version: "0.1.0", Revision: "005"}
	data, err := os.ReadFile(filename)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[WARN] version: unable to read %s: %v", filename, err)
		}
		return release
	}
	var raw struct {
		Version  string          `json:"version"`
		Revision json.RawMessage `json:"revision"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("[WARN] version: unable to parse %s: %v", filename, err)
		return ReleaseInfo{Version: "0.1.0", Revision: "005"}
	}
	if version := strings.TrimSpace(raw.Version); version != "" {
		release.Version = version
	}
	if len(raw.Revision) != 0 {
		var revisionString string
		if err := json.Unmarshal(raw.Revision, &revisionString); err == nil {
			release.Revision = normalizeRevisionLabel(revisionString)
		} else {
			var revisionNumber int
			if err := json.Unmarshal(raw.Revision, &revisionNumber); err == nil {
				release.Revision = normalizeRevisionLabel(strconv.Itoa(revisionNumber))
			}
		}
	}
	return release
}

func loadConfig(filename string) AppConfig {
	f, err := os.Open(filename)
	if err != nil {
		log.Fatalf("error opening config file: %v", err)
	}
	defer f.Close()

	var cfg AppConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		log.Fatalf("error parsing config file: %v", err)
	}
	applyConfigDefaults(&cfg)
	return cfg
}

func ensureWorkDirs(cfg AppConfig) {
	mustMkdir(cfg.WorkRoot)
	mustMkdir(filepath.Join(cfg.WorkRoot, "projects"))
	mustMkdir(filepath.Join(cfg.WorkRoot, "mastermind"))
	mustMkdir(filepath.Join(cfg.WorkRoot, "mastermind", "memories"))
	mustMkdir(filepath.Join(cfg.WorkRoot, "mastermind", "identities"))
	for _, model := range cfg.Models {
		if model.WorkDir != "" {
			mustMkdir(filepath.Join(cfg.WorkRoot, model.WorkDir))
		}
	}
}

const (
	defaultProjectMaxFiles      = 10
	defaultProjectMaxFileSizeKB = 256
	defaultProjectMaxPayloadKB  = 512
	knowledgeReadmeFilename     = "README.md"
	knowledgeNotesFilename      = "agentgo_notes.md"
)

func defaultProjectLimits() ProjectLimits {
	return ProjectLimits{
		MaxFiles:      defaultProjectMaxFiles,
		MaxFileSizeKB: defaultProjectMaxFileSizeKB,
		MaxPayloadKB:  defaultProjectMaxPayloadKB,
	}
}

func normalizeProjectLimits(limits ProjectLimits) ProjectLimits {
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaultProjectMaxFiles
	}
	if limits.MaxFileSizeKB <= 0 {
		limits.MaxFileSizeKB = defaultProjectMaxFileSizeKB
	}
	if limits.MaxPayloadKB <= 0 {
		limits.MaxPayloadKB = defaultProjectMaxPayloadKB
	}
	return limits
}

func isValidProjectName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) validateProjectName(name string) error {
	if !isValidProjectName(name) {
		return errors.New("project name must use only letters, numbers, and underscore")
	}
	return nil
}

func (a *App) activeProject() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.TrimSpace(a.activeProjectName)
}

func (a *App) requireActiveProject() (string, error) {
	name := a.activeProject()
	if name == "" {
		return "", errors.New("no active project selected")
	}
	return name, nil
}

func (a *App) requireActiveProjectForSession() error {
	if _, err := a.requireActiveProject(); err != nil {
		return errors.New("select an active project first")
	}
	return nil
}

func (a *App) listProjects() ([]projectInfo, error) {
	projectsRoot, err := safeJoin(a.cfg.WorkRoot, "projects")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []projectInfo{}, nil
		}
		return nil, err
	}

	active := a.activeProject()
	out := make([]projectInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isValidProjectName(name) {
			continue
		}

		projectDir, err := safeJoin(projectsRoot, name)
		if err != nil {
			continue
		}
		mod := time.Time{}
		if info, err := os.Stat(projectDir); err == nil {
			mod = info.ModTime()
		}
		settingsPath := filepath.Join(projectDir, "project.json")
		if info, err := os.Stat(settingsPath); err == nil && info.ModTime().After(mod) {
			mod = info.ModTime()
		}

		limits, err := a.loadProjectLimits(name)
		if err != nil {
			limits = defaultProjectLimits()
		}
		out = append(out, projectInfo{Name: name, LastAccessed: mod.Format(time.RFC3339), Active: name == active, Limits: limits})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].LastAccessed != out[j].LastAccessed {
			return out[i].LastAccessed > out[j].LastAccessed
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (a *App) createProject(name string) error {
	if err := a.validateProjectName(name); err != nil {
		return err
	}
	return a.ensureProjectScaffold(name)
}

func (a *App) ensureProjectScaffold(name string) error {
	if err := a.validateProjectName(name); err != nil {
		return err
	}
	for _, model := range a.cfg.Models {
		base, err := safeJoin(a.cfg.WorkRoot, model.WorkDir, name)
		if err != nil {
			return err
		}
		metaDir := filepath.Join(base, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(reviewerReviewsDir(metaDir), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(base, "project"), 0o755); err != nil {
			return err
		}
		if err := ensureFile(filepath.Join(metaDir, "user_context.json"), "{}\n"); err != nil {
			return err
		}
		if err := ensureFile(filepath.Join(metaDir, "ai_context.json"), string(defaultAIContextJSON())); err != nil {
			return err
		}
		if err := ensureFile(filepath.Join(metaDir, "reviewer_context.json"), "{}\n"); err != nil {
			return err
		}
		if err := ensureFile(filepath.Join(metaDir, chatMemoryFileName), ""); err != nil {
			return err
		}
	}
	projectworkRoot, err := a.projectWorkRoot(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(projectworkRoot, 0o755); err != nil {
		return err
	}
	if err := a.ensureProjectSettings(name); err != nil {
		return err
	}
	return nil
}

func (a *App) projectSettingsDir(projectName string) (string, error) {
	if !isValidProjectName(projectName) {
		return "", errors.New("invalid project name")
	}
	return safeJoin(a.cfg.WorkRoot, "projects", projectName)
}

func (a *App) projectSettingsPath(projectName string) (string, error) {
	dir, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "project.json"), nil
}

func (a *App) ensureProjectSettings(projectName string) error {
	settingsDir, err := a.projectSettingsDir(projectName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return err
	}
	settingsPath := filepath.Join(settingsDir, "project.json")
	if _, err := os.Stat(settingsPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return a.saveProjectLimits(projectName, defaultProjectLimits())
}

func (a *App) loadProjectLimits(projectName string) (ProjectLimits, error) {
	if err := a.ensureProjectSettings(projectName); err != nil {
		return ProjectLimits{}, err
	}
	settingsPath, err := a.projectSettingsPath(projectName)
	if err != nil {
		return ProjectLimits{}, err
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return ProjectLimits{}, err
	}
	var limits ProjectLimits
	if err := json.Unmarshal(data, &limits); err != nil {
		return ProjectLimits{}, fmt.Errorf("invalid project settings: %w", err)
	}
	return normalizeProjectLimits(limits), nil
}

func (a *App) saveProjectLimits(projectName string, limits ProjectLimits) error {
	settingsDir, err := a.projectSettingsDir(projectName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return err
	}
	settingsPath := filepath.Join(settingsDir, "project.json")
	limits = normalizeProjectLimits(limits)
	data, err := json.MarshalIndent(limits, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(settingsPath, data, 0o644)
}

func (a *App) setActiveProject(name string) error {
	name = strings.TrimSpace(name)
	if name != "" && !isValidProjectName(name) {
		return errors.New("invalid project name")
	}
	a.mu.Lock()
	a.activeProjectName = name
	a.mu.Unlock()
	return nil
}

func (a *App) resetProjectSessionState() {
	a.mu.Lock()
	for id, entry := range a.activeCancels {
		if entry.Cancel != nil {
			entry.Cancel()
		}
		delete(a.activeCancels, id)
	}
	for _, model := range a.cfg.Models {
		a.toggles[modelIDString(model.ID)] = false
	}
	a.reviewerID = ""
	a.activeProjectName = ""
	a.clearRiskModeLocked()
	a.pendingMergeCountsByProject = map[string]map[string]int{}
	a.clearAllWaveExecutionsLocked()
	a.waveStatusByProject = map[string]waveStatusState{}
	a.mu.Unlock()
}

func (a *App) projectPaths(model ModelConfig, projectName string) (projectRoot string, metaRoot string, err error) {
	projectRoot, err = safeJoin(a.cfg.WorkRoot, model.WorkDir, projectName, "project")
	if err != nil {
		return "", "", err
	}
	metaRoot, err = safeJoin(a.cfg.WorkRoot, model.WorkDir, projectName, "meta")
	if err != nil {
		return "", "", err
	}
	return projectRoot, metaRoot, nil
}

func (a *App) projectWorkRoot(projectName string) (string, error) {
	if !isValidProjectName(projectName) {
		return "", errors.New("invalid project name")
	}
	return safeJoin(a.cfg.WorkRoot, "projects", projectName, "projectwork")
}

func mustMkdir(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		log.Fatalf("failed to create %s: %v", path, err)
	}
}

func ensureFile(path string, defaultContent string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte(defaultContent), 0o644)
}

func reviewerReviewsDir(metaRoot string) string {
	return filepath.Join(metaRoot, "reviews")
}

func builderResponsesDir(metaRoot string) string {
	return filepath.Join(metaRoot, "builder_responses")
}

func promptHelperResponsesDir(metaRoot string) string {
	return filepath.Join(metaRoot, "prompt_helper_responses")
}

func deadDropResponsesDir(metaRoot string) string {
	return filepath.Join(metaRoot, "deaddrop_responses")
}

func cypherResponsesDir(metaRoot string) string {
	return filepath.Join(metaRoot, "cypher_responses")
}

func reviewerLatestPath(metaRoot string) string {
	return filepath.Join(reviewerReviewsDir(metaRoot), "reviewer_latest.json")
}

func reviewerArchivePath(metaRoot, timestamp string) string {
	return filepath.Join(reviewerReviewsDir(metaRoot), "reviewer_"+timestamp+".json")
}

func reviewerRawResponsePath(metaRoot, timestamp string) string {
	return filepath.Join(reviewerReviewsDir(metaRoot), "response_"+timestamp+".md")
}

func readOptionalTextFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func appRootFilePath(name string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(name))
	if clean == "" || clean == "." || clean == string(os.PathSeparator) || strings.Contains(clean, "..") {
		return "", errors.New("invalid app file path")
	}
	return filepath.Abs(clean)
}

func knowledgeDefaultTab(notes string) string {
	if strings.TrimSpace(notes) != "" {
		return "notes"
	}
	return "readme"
}

func (a *App) readKnowledgeFiles() (knowledgeResponse, error) {
	readmePath, err := appRootFilePath(knowledgeReadmeFilename)
	if err != nil {
		return knowledgeResponse{}, err
	}
	readme, err := readOptionalTextFile(readmePath)
	if err != nil {
		return knowledgeResponse{}, err
	}
	if strings.TrimSpace(readme) == "" {
		readme = "# README unavailable\n\nREADME.md was not found in the AgentGO root folder.\n"
	}
	notesPath, err := appRootFilePath(knowledgeNotesFilename)
	if err != nil {
		return knowledgeResponse{}, err
	}
	notes, err := readOptionalTextFile(notesPath)
	if err != nil {
		return knowledgeResponse{}, err
	}
	return knowledgeResponse{Readme: readme, Notes: notes, DefaultTab: knowledgeDefaultTab(notes)}, nil
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if err := a.tmpl.Execute(w, homeData{Models: a.cfg.Models, RiskModeMaxIterations: a.cfg.RiskModeMaxIterations, ReleaseLabel: a.release.Label()}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelView struct {
		ID                       string `json:"id"`
		Label                    string `json:"label"`
		Enabled                  bool   `json:"enabled"`
		Reviewer                 bool   `json:"reviewer"`
		Running                  bool   `json:"running"`
		HasOutputReady           bool   `json:"hasOutputReady"`
		PendingCount             int    `json:"pendingCount"`
		HasBuilderResponse       bool   `json:"hasBuilderResponse"`
		HasUnreadBuilderResponse bool   `json:"hasUnreadBuilderResponse"`
		BuilderReportStatus      string `json:"builderReportStatus,omitempty"`
		HasReviewerReport        bool   `json:"hasReviewerReport"`
		HasUnreadReviewerReport  bool   `json:"hasUnreadReviewerReport"`
		HasAIContext             bool   `json:"hasAIContext,omitempty"`
		HasReviewerContext       bool   `json:"hasReviewerContext,omitempty"`
		IsVideoGeneration        bool   `json:"isVideoGeneration,omitempty"`
		IsMeshGeneration         bool   `json:"isMeshGeneration,omitempty"`
	}
	projectName := a.activeProject()
	a.mu.RLock()
	reviewerID := a.reviewerID
	pendingSnapshot := map[string]int{}
	for modelID, count := range a.pendingMergeCountsByProject[projectName] {
		pendingSnapshot[modelID] = count
	}
	togglesSnapshot := map[string]bool{}
	for modelID, enabled := range a.toggles {
		togglesSnapshot[modelID] = enabled
	}
	runningSnapshot := map[string]bool{}
	for modelID, entry := range a.activeCancels {
		if entry.Cancel != nil {
			runningSnapshot[modelID] = true
		}
	}
	a.mu.RUnlock()

	views := make([]modelView, 0, len(a.cfg.Models))
	for _, m := range a.cfg.Models {
		modelID := modelIDString(m.ID)
		count := pendingSnapshot[modelID]
		view := modelView{ID: modelID, Label: m.Label, Enabled: togglesSnapshot[modelID], Reviewer: reviewerID == modelID, Running: runningSnapshot[modelID], HasOutputReady: count > 0, PendingCount: count, IsVideoGeneration: modelIsVideoGeneration(m), IsMeshGeneration: modelIsMeshGeneration(m)}
		if projectName != "" {
			if _, metaRoot, err := a.projectPaths(m, projectName); err == nil {
				if state, err := readBuilderOutputState(metaRoot); err == nil && state.HasResponse {
					view.HasBuilderResponse = true
					view.HasUnreadBuilderResponse = state.Unread
					if state.Unread && builderReportHasContent(state.BuilderReport) {
						view.BuilderReportStatus = normalizeBuilderReportStatus(state.BuilderReport.Status)
					}
				}
				if state, err := readReviewerOutputState(metaRoot); err == nil && state.HasReport {
					view.HasReviewerReport = true
					view.HasUnreadReviewerReport = state.Unread
				}
				view.HasAIContext = fileHasMeaningfulJSON(filepath.Join(metaRoot, "ai_context.json"))
				view.HasReviewerContext = fileHasMeaningfulJSON(filepath.Join(metaRoot, "reviewer_context.json"))
			}
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func handleKnownModelNames(w http.ResponseWriter, r *http.Request, filename string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "models": []any{}})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		http.Error(w, "model_names.json is not valid JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) handleWaveState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	a.mu.RLock()
	state, ok := a.waveStatusByProject[strings.TrimSpace(projectName)]
	execState, hasExec := a.waveExecutionsByProject[strings.TrimSpace(projectName)]
	a.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusOK, waveStatusState{ProjectName: strings.TrimSpace(projectName), Visible: false})
		return
	}
	if hasExec {
		state = populateWaveStatusProgress(state, execState)
	}
	writeJSON(w, http.StatusOK, state)
}

func clearChatMemoryFile(metaRoot string) error {
	if strings.TrimSpace(metaRoot) == "" {
		return nil
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(metaRoot, chatMemoryFileName), []byte(""), 0o644)
}

func normalizeWorkModeMemoryFileName(raw string) (string, string, error) {
	name := strings.TrimSpace(raw)
	if strings.HasSuffix(strings.ToLower(name), ".md") {
		name = strings.TrimSpace(name[:len(name)-3])
	}
	if name == "" {
		return "", "", errors.New("memory name is required")
	}
	if utf8.RuneCountInString(name) > 80 {
		return "", "", errors.New("memory name must be 80 characters or fewer")
	}
	if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", "", errors.New("memory name must not include path separators or traversal")
	}
	if strings.HasPrefix(name, ".") {
		return "", "", errors.New("hidden memory files are not supported")
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return "", "", errors.New("memory name contains an unsupported character")
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '_' {
			continue
		}
		return "", "", errors.New("memory name may use letters, numbers, spaces, dash, and underscore")
	}
	return name, name + ".md", nil
}

func workModeMemoryActiveInfo(metaRoot string) (bool, int64) {
	data, err := os.ReadFile(filepath.Join(metaRoot, chatMemoryFileName))
	if err != nil {
		return false, 0
	}
	return strings.TrimSpace(string(data)) != "", int64(len(data))
}

func workModeMemoriesRoot(metaRoot string) (string, error) {
	return safeJoin(metaRoot, "memories")
}

func listWorkModeMemoryFiles(metaRoot string) ([]workModeMemoryFile, error) {
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(memoriesRoot)
	if err != nil {
		return nil, err
	}
	files := []workModeMemoryFile{}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		fileName := strings.TrimSpace(entry.Name())
		if !strings.HasSuffix(strings.ToLower(fileName), ".md") {
			continue
		}
		displayName, normalizedFileName, err := normalizeWorkModeMemoryFileName(fileName)
		if err != nil || normalizedFileName != fileName {
			continue
		}
		info, _ := entry.Info()
		item := workModeMemoryFile{Name: displayName, FileName: fileName}
		if info != nil {
			item.SizeBytes = info.Size()
			item.ModifiedAt = info.ModTime().Format(time.RFC3339)
		}
		files = append(files, item)
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

func (a *App) workModeMemoryMetaRoot(modelID, projectName string) (ModelConfig, string, error) {
	roles, _, err := a.resolveWorkModeRoles(strings.TrimSpace(modelID), "", false)
	if err != nil {
		return ModelConfig{}, "", err
	}
	_, metaRoot, err := a.projectPaths(roles.Worker, projectName)
	if err != nil {
		return ModelConfig{}, "", err
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		return ModelConfig{}, "", err
	}
	if err := ensureFile(filepath.Join(metaRoot, chatMemoryFileName), ""); err != nil {
		return ModelConfig{}, "", err
	}
	return roles.Worker, metaRoot, nil
}

func (a *App) workModeMemoryResponse(metaRoot, message string) (workModeMemoryResponse, error) {
	activeExists, activeBytes := workModeMemoryActiveInfo(metaRoot)
	saved, err := listWorkModeMemoryFiles(metaRoot)
	if err != nil {
		return workModeMemoryResponse{}, err
	}
	return workModeMemoryResponse{ActiveExists: activeExists, ActiveBytes: activeBytes, Saved: saved, Message: message}, nil
}

func (a *App) handleWorkModeMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
	_, metaRoot, err := a.workModeMemoryMetaRoot(modelID, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := a.workModeMemoryResponse(metaRoot, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) decodeWorkModeMemoryRequest(w http.ResponseWriter, r *http.Request) (workModeMemoryRequest, string, string, error) {
	var req workModeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return req, "", "", err
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return req, "", "", err
	}
	_, metaRoot, err := a.workModeMemoryMetaRoot(req.ModelID, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return req, "", "", err
	}
	return req, projectName, metaRoot, nil
}

func (a *App) handleWorkModeMemoryNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _, metaRoot, err := a.decodeWorkModeMemoryRequest(w, r)
	if err != nil {
		return
	}
	if err := clearChatMemoryFile(metaRoot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.workModeMemoryResponse(metaRoot, "New Work Mode memory started.")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleWorkModeMemoryLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, _, metaRoot, err := a.decodeWorkModeMemoryRequest(w, r)
	if err != nil {
		return
	}
	displayName, fileName, err := normalizeWorkModeMemoryFileName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	full, err := safeJoin(memoriesRoot, fileName)
	if err != nil {
		http.Error(w, "invalid memory file", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "saved memory file was not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(filepath.Join(metaRoot, chatMemoryFileName), data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.workModeMemoryResponse(metaRoot, "Loaded memory: "+displayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleWorkModeMemorySave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, _, metaRoot, err := a.decodeWorkModeMemoryRequest(w, r)
	if err != nil {
		return
	}
	displayName, fileName, err := normalizeWorkModeMemoryFileName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join(metaRoot, chatMemoryFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(string(data)) == "" {
		http.Error(w, "current memory is empty", http.StatusBadRequest)
		return
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	full, err := safeJoin(memoriesRoot, fileName)
	if err != nil {
		http.Error(w, "invalid memory file", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.workModeMemoryResponse(metaRoot, "Saved memory: "+displayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleWorkModeMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, _, metaRoot, err := a.decodeWorkModeMemoryRequest(w, r)
	if err != nil {
		return
	}
	displayName, fileName, err := normalizeWorkModeMemoryFileName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	full, err := safeJoin(memoriesRoot, fileName)
	if err != nil {
		http.Error(w, "invalid memory file", http.StatusBadRequest)
		return
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.workModeMemoryResponse(metaRoot, "Deleted memory: "+displayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func sanitizeChatMemoryVisibleReply(reply string) string {
	text := string(reply)
	for _, marker := range []string{chatMemoryBeginMarker, chatMemoryEndMarker} {
		text = strings.ReplaceAll(text, marker, "")
	}
	return strings.TrimSpace(text)
}

func extractChatMemoryBlock(reply string) (string, string, bool) {
	text := string(reply)
	begin := strings.Index(text, chatMemoryBeginMarker)
	if begin < 0 {
		return sanitizeChatMemoryVisibleReply(text), "", false
	}
	afterBegin := begin + len(chatMemoryBeginMarker)
	endRel := strings.Index(text[afterBegin:], chatMemoryEndMarker)
	if endRel < 0 {
		return sanitizeChatMemoryVisibleReply(text), "", false
	}
	end := afterBegin + endRel
	memory := strings.TrimSpace(text[afterBegin:end])
	visible := text[:begin] + text[end+len(chatMemoryEndMarker):]
	return sanitizeChatMemoryVisibleReply(visible), memory, true
}

func (a *App) handleModelToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	a.mu.Lock()
	wasEnabled := a.toggles[req.ModelID]
	a.toggles[req.ModelID] = req.Enabled
	projectName := a.activeProjectName
	a.mu.Unlock()
	if req.Enabled && !wasEnabled && strings.TrimSpace(projectName) != "" {
		if _, err := a.syncBuilderProjectsFromProjectwork(projectName, []ModelConfig{model}); err != nil {
			a.mu.Lock()
			a.toggles[req.ModelID] = wasEnabled
			a.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.resetModelAIContextToEmpty(projectName, model); err != nil {
			a.mu.Lock()
			a.toggles[req.ModelID] = wasEnabled
			a.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.logf(req.ModelID, "info", "Reset ai_context.json to strict empty memory and reviewer_context.json to {} for activated model in project %s", projectName)
	}
	if !req.Enabled && projectName != "" {
		a.clearPendingMergeCount(projectName, req.ModelID)
	}
	if strings.TrimSpace(projectName) != "" {
		if _, metaRoot, err := a.projectPaths(model, projectName); err != nil {
			a.logf(req.ModelID, "warn", "Could not resolve chat memory path while toggling model: %v", err)
		} else if err := clearChatMemoryFile(metaRoot); err != nil {
			a.logf(req.ModelID, "warn", "Could not clear chat memory.md while toggling model: %v", err)
		}
	}
	a.logf(req.ModelID, "info", "Model toggled %v", req.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleReviewerToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID  string `json:"modelId"`
		Reviewer bool   `json:"reviewer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if _, ok := a.findModel(req.ModelID); !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	if req.Reviewer {
		if err := a.requireActiveProjectForSession(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	a.mu.Lock()
	if req.Reviewer {
		a.reviewerID = req.ModelID
		a.toggles[req.ModelID] = false
	} else if a.reviewerID == req.ModelID {
		a.reviewerID = ""
	}
	a.mu.Unlock()
	a.logf(req.ModelID, "info", "Reviewer mode set to %v", req.Reviewer)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleModelMeta(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
	if modelID == "" {
		http.Error(w, "modelId is required", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		files := map[string]string{}
		for _, name := range []string{"user_context.json", "ai_context.json", "reviewer_context.json", chatMemoryFileName} {
			data, _ := os.ReadFile(filepath.Join(metaRoot, name))
			files[name] = string(data)
		}
		writeJSON(w, http.StatusOK, modelMetaResponse{ModelID: modelID, Project: projectName, Files: files, Reviewer: a.reviewerID == modelID})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	for name, content := range req.Files {
		if name != "user_context.json" && name != "ai_context.json" && name != "reviewer_context.json" && name != chatMemoryFileName {
			http.Error(w, "invalid meta file", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(metaRoot, name), []byte(content), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	a.logf(modelID, "info", "Updated meta files for project %s", projectName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleBuilderOutput(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	readState := func(modelID string) (builderOutputState, string, error) {
		model, ok := a.findModel(modelID)
		if !ok {
			return builderOutputState{}, "", errors.New("unknown model")
		}
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return builderOutputState{}, "", err
		}
		state, err := readBuilderOutputState(metaRoot)
		if err != nil {
			return builderOutputState{}, metaRoot, err
		}
		if !state.HasResponse {
			state.ModelID = modelID
			state.ModelLabel = model.Label
			state.Project = projectName
		}
		return state, metaRoot, nil
	}
	switch r.Method {
	case http.MethodGet:
		modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
		if modelID == "" {
			http.Error(w, "modelId is required", http.StatusBadRequest)
			return
		}
		state, _, err := readState(modelID)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "unknown model") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, state)
	case http.MethodPost:
		var req struct {
			ModelID  string `json:"modelId"`
			MarkRead bool   `json:"markRead"`
			Clear    bool   `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.ModelID = strings.TrimSpace(req.ModelID)
		if req.ModelID == "" {
			http.Error(w, "modelId is required", http.StatusBadRequest)
			return
		}
		state, metaRoot, err := readState(req.ModelID)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "unknown model") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		if req.Clear {
			if err := clearBuilderOutputState(metaRoot); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			state = builderOutputState{ModelID: req.ModelID, ModelLabel: state.ModelLabel, Project: projectName}
			writeJSON(w, http.StatusOK, state)
			return
		}
		if req.MarkRead && state.HasResponse && state.Unread {
			state.Unread = false
			if err := writeBuilderOutputState(metaRoot, state); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, state)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleReviewerOutput(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	readState := func(modelID string) (reviewerOutputState, string, error) {
		model, ok := a.findModel(modelID)
		if !ok {
			return reviewerOutputState{}, "", errors.New("unknown model")
		}
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return reviewerOutputState{}, "", err
		}
		state, err := readReviewerOutputState(metaRoot)
		if err != nil {
			return reviewerOutputState{}, metaRoot, err
		}
		if !state.HasReport {
			state.ModelID = modelID
			state.ModelLabel = model.Label
			state.Project = projectName
		} else {
			a.attachReviewerCandidateReturnedFiles(projectName, &state)
		}
		return state, metaRoot, nil
	}
	switch r.Method {
	case http.MethodGet:
		modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
		if modelID == "" {
			http.Error(w, "modelId is required", http.StatusBadRequest)
			return
		}
		state, _, err := readState(modelID)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "unknown model") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, state)
	case http.MethodPost:
		var req struct {
			ModelID  string `json:"modelId"`
			MarkRead bool   `json:"markRead"`
			Clear    bool   `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.ModelID = strings.TrimSpace(req.ModelID)
		if req.ModelID == "" {
			http.Error(w, "modelId is required", http.StatusBadRequest)
			return
		}
		state, metaRoot, err := readState(req.ModelID)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "unknown model") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		if req.Clear {
			if err := clearReviewerOutputState(metaRoot); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			state = reviewerOutputState{ModelID: req.ModelID, ModelLabel: state.ModelLabel, Project: projectName}
			writeJSON(w, http.StatusOK, state)
			return
		}
		if req.MarkRead && state.HasReport && state.Unread {
			state.Unread = false
			if err := writeReviewerOutputState(metaRoot, state); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, state)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func normalizeChatResponseMode(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.ReplaceAll(clean, "-", "_")
	clean = strings.ReplaceAll(clean, " ", "_")
	switch clean {
	case "quick", "deep", "ugg", "step_by_step", "ask_first", "skeptic", "builder", "teacher", "editor":
		return clean
	default:
		return "auto"
	}
}

func chatResponseModeInstruction(mode string) string {
	switch normalizeChatResponseMode(mode) {
	case "quick":
		return "Prioritize a short, direct answer. Avoid extra explanation unless it is needed."
	case "deep":
		return "Answer carefully. Check assumptions, edge cases, risks, and likely failure points before giving the final answer. Do not reveal private reasoning."
	case "ugg":
		return "Use compressed, low-filler wording. Preserve exact names, paths, APIs, and code."
	case "step_by_step":
		return "Explain the answer in clear visible steps."
	case "ask_first":
		return "If the request is ambiguous or missing key information, ask a clarifying question before giving a full answer."
	case "skeptic":
		return "Challenge weak assumptions, point out risks, and identify what might be wrong."
	case "builder":
		return "Focus on practical implementation, concrete actions, and usable output."
	case "teacher":
		return "Explain concepts, tradeoffs, and reasoning in a helpful teaching style."
	case "editor":
		return "Improve clarity, wording, structure, tone, and consistency."
	default:
		return ""
	}
}

func readChatProjectFile(projectworkRoot, rel string) (string, string, int64, error) {
	cleanRel := filepath.ToSlash(strings.TrimSpace(rel))
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	if cleanRel == "" {
		return "", "", 0, nil
	}
	full, err := safeJoin(projectworkRoot, cleanRel)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid Project Mode file path")
	}
	if err := rejectSymlinkPath(projectworkRoot, full); err != nil {
		return "", "", 0, fmt.Errorf("Project Mode file is an unsupported symlink path")
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", 0, fmt.Errorf("Project Mode file not found")
		}
		return "", "", 0, err
	}
	if info.IsDir() {
		return "", "", 0, fmt.Errorf("Project Mode can only send one text file, not a folder")
	}
	if info.Size() > chatProjectFileMaxBytes {
		return "", "", 0, fmt.Errorf("Project Mode file is too large (%d bytes > %d bytes)", info.Size(), chatProjectFileMaxBytes)
	}
	data, err := readFileUnderRoot(projectworkRoot, full)
	if err != nil {
		return "", "", 0, err
	}
	contentType := detectContentType(cleanRel, data)
	if !isLikelyText(cleanRel, data, contentType) {
		return "", "", 0, fmt.Errorf("Project Mode can only send text files")
	}
	return cleanRel, string(data), int64(len(data)), nil
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ModelID = strings.TrimSpace(req.ModelID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ResponseMode = normalizeChatResponseMode(req.ResponseMode)
	req.ProjectFile = filepath.ToSlash(strings.TrimSpace(req.ProjectFile))
	if req.ModelID == "" || req.Prompt == "" {
		http.Error(w, "modelId and prompt are required", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	a.mu.RLock()
	enabled := a.toggles[req.ModelID]
	reviewer := a.reviewerID == req.ModelID
	a.mu.RUnlock()
	if !enabled && !reviewer {
		http.Error(w, "Activate this model or enable reviewer mode first.", http.StatusBadRequest)
		return
	}
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	includeRoleContext := true
	if req.IncludeRoleContext != nil {
		includeRoleContext = *req.IncludeRoleContext
	}
	userContext := []byte("{}")
	if includeRoleContext {
		userContext, _ = os.ReadFile(filepath.Join(metaRoot, "user_context.json"))
	}
	memoryPath := filepath.Join(metaRoot, chatMemoryFileName)
	chatMemory := []byte("")
	if req.UseMemory {
		chatMemory, _ = os.ReadFile(memoryPath)
	} else {
		if err := clearChatMemoryFile(metaRoot); err != nil {
			a.logf(modelIDString(model.ID), "warn", "Could not clear chat memory.md while memory mode was off: %v", err)
		}
	}
	instructions := strings.TrimSpace(`You are the selected AI model for this AgentGO project's Chat-To-AI conversation.

ROLE
- Treat this as direct conversation with the user.

RESPONSE STYLE
- Respond in clear natural language unless the user explicitly asks for another format.

TRUTHFULNESS
- If something is uncertain, unavailable, or not actually present in the provided context, say so plainly.`)
	if includeRoleContext {
		instructions = strings.TrimSpace(instructions + `

CONTEXT
- Use the provided project and role context, including user_context.json, when it is included.`)
	}
	if req.UseMemory {
		instructions = strings.TrimSpace(instructions + `

CHAT MEMORY
- AgentGO included meta/memory.md for this Chat-To-AI model/project. Read it before answering.
- Use memory.md to store any information about this chat that you think you will require for the user to follow up on.
- Clean up garbage, stale noise, duplicates, or low-value text before saving memory.
- Use Ugg Protocol only when writing memory.md, to save the user tokens. Do not force the visible answer into Ugg style unless the response mode or user asks for it.
- Internal memory markers are machine-only control tokens. They are not separators, headings, or part of the visible answer.
- Never repeat internal memory markers inside the visible answer.
- Put the complete visible answer first. Then append exactly one complete memory block using this exact shape:
---BEGIN_AGENTGO_MEMORY_MD---
<complete updated compact memory.md content>
---END_AGENTGO_MEMORY_MD---
- AgentGO hides this memory block from the user and saves only the content between the markers.` + "\n\n" + chatMemoryUggProtocolPrompt)
	}
	if modeInstruction := chatResponseModeInstruction(req.ResponseMode); modeInstruction != "" {
		instructions = strings.TrimSpace(instructions + "\n\nRESPONSE MODE\n- " + modeInstruction)
	}
	instructions = appendModelUggProtocol(instructions, model)
	var projectModeFilePath, projectModeFileText string
	if req.ProjectFile != "" {
		projectworkRoot, err := a.projectWorkRoot(projectName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		projectModeFilePath, projectModeFileText, _, err = readChatProjectFile(projectworkRoot, req.ProjectFile)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	inputParts := []string{
		"AGENTGO CHAT SESSION",
		"PROJECT: " + projectName,
		"MODEL: " + model.Label,
	}
	if includeRoleContext {
		inputParts = append(inputParts, "", "MODEL USER CONTEXT (meta/user_context.json):", strings.TrimSpace(string(userContext)))
	} else {
		inputParts = append(inputParts, "", "MODEL USER CONTEXT:", "(disabled for this message)")
	}
	if req.UseMemory {
		memoryText := strings.TrimSpace(string(chatMemory))
		if memoryText == "" {
			memoryText = "(empty)"
		}
		inputParts = append(inputParts, "", "CHAT MEMORY (meta/memory.md):", memoryText)
	} else {
		inputParts = append(inputParts, "", "CHAT MEMORY:", "(disabled for this message)")
	}
	if projectModeFilePath != "" {
		inputParts = append(inputParts, "", "PROJECT MODE FILE (projectwork/"+projectModeFilePath+"):", projectModeFileText)
	}
	inputParts = append(inputParts, "", "USER MESSAGE:", strings.TrimSpace(req.Prompt))
	input := strings.Join(inputParts, "\n")
	reply, err := a.callStructuredTextModel(context.Background(), model, instructions, input, false, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	visibleReply, memoryText, hasMemoryBlock := extractChatMemoryBlock(reply)
	memoryWarning := ""
	memoryUpdated := false
	if req.UseMemory {
		if hasMemoryBlock {
			if err := os.WriteFile(memoryPath, []byte(memoryText), 0o644); err != nil {
				memoryWarning = "Could not save memory.md."
				a.logf(modelIDString(model.ID), "warn", "Could not save chat memory.md: %v", err)
			} else {
				memoryUpdated = true
			}
		} else {
			memoryWarning = "AI response did not include a memory.md update block; previous memory was preserved."
			a.logf(modelIDString(model.ID), "warn", "Chat memory.md was enabled, but response did not include memory markers")
		}
	}
	a.logf(modelIDString(model.ID), "info", "Chat to AI used for project %s", projectName)
	writeJSON(w, http.StatusOK, chatResponse{Reply: visibleReply, MemoryUpdated: memoryUpdated, MemoryWarning: memoryWarning})
}

func workModeJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reply": map[string]any{"type": "string"},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":         map[string]any{"type": "string"},
						"action":       map[string]any{"type": "string", "enum": []string{"create", "overwrite", "delete"}},
						"content":      map[string]any{"type": "string"},
						"artifact_ref": map[string]any{"type": "string"},
					},
					"required":             []string{"path", "action"},
					"additionalProperties": false,
				},
			},
			"artifacts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":        map[string]any{"type": "string"},
						"encoding":  map[string]any{"type": "string"},
						"mime_type": map[string]any{"type": "string"},
						"data":      map[string]any{"type": "string"},
					},
					"required":             []string{"id", "encoding", "data"},
					"additionalProperties": false,
				},
			},
			"memory":   map[string]any{"type": "string"},
			"warnings": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"reply", "files"},
		"additionalProperties": false,
	}
}

func workModeObserverWorkerJSONSchema() map[string]any {
	schema := cloneAnyMap(workModeJSONSchema())
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		schema["properties"] = props
	}
	props["reply"] = map[string]any{"type": "string", "description": "The latest user-facing Worker answer. In Observer Review Mode this may describe draft files in tmp-work, but it must not claim those drafts were merged into the project."}
	props["files"] = map[string]any{"type": "array", "description": "Worker-only file operations for intended projectwork-relative paths. AgentGO routes these operations into tmp-work during Observer Review Mode.", "items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":         map[string]any{"type": "string", "description": "Intended projectwork-relative path such as index.html or src/game.js. Never include tmp-work/ and never use absolute or traversal paths."},
			"action":       map[string]any{"type": "string", "enum": []string{"create", "overwrite", "delete"}},
			"content":      map[string]any{"type": "string", "description": "Full file content for create/overwrite text files."},
			"artifact_ref": map[string]any{"type": "string", "description": "Artifact id for binary/file outputs when applicable."},
		},
		"required":             []string{"path", "action"},
		"additionalProperties": false,
	}}
	props["memory"] = map[string]any{"type": "string", "description": "Worker-owned compact memory.md update only when memory is enabled. Leave empty when memory is disabled."}
	props["warnings"] = map[string]any{"type": "array", "description": "Non-fatal Worker warnings for AgentGO logs.", "items": map[string]any{"type": "string"}}
	props["review_complete"] = map[string]any{"type": "boolean", "description": "true when the latest Worker reply and tmp-work draft files are ready for user review; false when another Observer review could materially improve the result."}
	schema["required"] = []string{"reply", "files", "review_complete"}
	return schema
}

func workModeObserverJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reply":           map[string]any{"type": "string", "description": "Concise advisory review notes for the Worker. Do not include file operations or memory updates."},
			"has_input":       map[string]any{"type": "boolean", "description": "true only for material corrections or improvements the Worker should consider; false when there is nothing important enough to justify another Worker pass."},
			"recommendations": map[string]any{"type": "array", "description": "Optional concise, actionable recommendations. Each item should be material and specific.", "items": map[string]any{"type": "string"}},
			"warnings":        map[string]any{"type": "array", "description": "Non-fatal Observer warnings for AgentGO logs.", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"reply", "has_input"},
		"additionalProperties": false,
	}
}

func stripSingleFullMarkdownJSONFence(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return "", false
	}
	opener := strings.TrimSpace(lines[0])
	language := strings.TrimSpace(strings.TrimPrefix(opener, "```"))
	if language != "" && !strings.EqualFold(language, "json") {
		return "", false
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n")), true
}

func workModeJSONCandidate(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("empty Work Mode response")
	}
	if json.Valid([]byte(raw)) {
		return raw, nil
	}
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "\ufeff"))
	if trimmed == "" {
		return "", errors.New("empty Work Mode response")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	if unfenced, ok := stripSingleFullMarkdownJSONFence(trimmed); ok {
		if unfenced == "" {
			return "", errors.New("empty JSON inside Work Mode markdown fence")
		}
		if json.Valid([]byte(unfenced)) {
			return unfenced, nil
		}
		if err := explainInvalidJSON(unfenced); err != nil {
			return "", fmt.Errorf("invalid JSON inside Work Mode markdown fence: %w", err)
		}
		return "", errors.New("invalid JSON inside Work Mode markdown fence")
	}
	if jsonText, _, _, ok := extractJSONObjectFromText(trimmed); ok {
		return jsonText, nil
	}
	if candidate, _, _, ok := extractBalancedJSONObjectCandidate(trimmed); ok {
		if err := explainInvalidJSON(candidate); err != nil {
			return "", fmt.Errorf("no valid JSON object found in Work Mode response: %w", err)
		}
	}
	return "", errors.New("no valid JSON object found in Work Mode response")
}

func parseWorkModeAIResponse(raw string) (workModeAIResponse, error) {
	var resp workModeAIResponse
	jsonText, err := workModeJSONCandidate(raw)
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal([]byte(jsonText), &resp); err != nil {
		return resp, err
	}
	if resp.Files == nil {
		resp.Files = []builderFileOp{}
	}
	resp.Reply = strings.TrimSpace(resp.Reply)
	return resp, nil
}

func requireWorkModeJSONBoolFieldInCandidate(jsonText, field string) error {
	field = strings.TrimSpace(field)
	if field == "" {
		return errors.New("missing required boolean field name")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonText), &obj); err != nil {
		return err
	}
	rawValue, ok := obj[field]
	if !ok {
		return fmt.Errorf("missing required boolean field %q", field)
	}
	var value bool
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return fmt.Errorf("field %q must be a boolean", field)
	}
	return nil
}

func requireWorkModeJSONBoolField(raw, field string) error {
	jsonText, err := workModeJSONCandidate(raw)
	if err != nil {
		return err
	}
	return requireWorkModeJSONBoolFieldInCandidate(jsonText, field)
}

func parseWorkModeObserverResponse(raw string) (workModeObserverResponse, error) {
	var resp workModeObserverResponse
	jsonText, err := workModeJSONCandidate(raw)
	if err != nil {
		return resp, err
	}
	if err := requireWorkModeJSONBoolFieldInCandidate(jsonText, "has_input"); err != nil {
		return resp, err
	}
	if err := json.Unmarshal([]byte(jsonText), &resp); err != nil {
		return resp, err
	}
	resp.Reply = strings.TrimSpace(resp.Reply)
	if resp.Recommendations == nil {
		resp.Recommendations = []string{}
	}
	trimmedRecommendations := []string{}
	for _, recommendation := range resp.Recommendations {
		recommendation = strings.TrimSpace(recommendation)
		if recommendation != "" {
			trimmedRecommendations = append(trimmedRecommendations, recommendation)
		}
	}
	resp.Recommendations = trimmedRecommendations
	return resp, nil
}

func parseWorkModeAdapterResponse(resp adapters.Response, projectworkRoot, projectName string) (workModeAIResponse, error) {
	parsed, err := parseWorkModeAIResponse(resp.Text)
	if err != nil {
		if len(resp.FileData) == 0 {
			return workModeAIResponse{}, err
		}
		synthetic, synthErr := synthesizeWorkModeBinaryResponse(resp, projectworkRoot, projectName)
		if synthErr != nil {
			return workModeAIResponse{}, fmt.Errorf("%v; binary fallback failed: %w", err, synthErr)
		}
		return synthetic, nil
	}
	if len(resp.FileData) == 0 {
		return parsed, nil
	}
	synthetic, synthErr := synthesizeWorkModeBinaryResponse(resp, projectworkRoot, projectName)
	if synthErr != nil {
		parsed.Warnings = append(parsed.Warnings, "Direct binary output was returned with the JSON response, but AgentGO could not prepare it as a Work Mode file: "+synthErr.Error())
		return parsed, nil
	}
	parsed.Files = append(parsed.Files, synthetic.Files...)
	parsed.Artifacts = append(parsed.Artifacts, synthetic.Artifacts...)
	parsed.Warnings = append(parsed.Warnings, synthetic.Warnings...)
	if strings.TrimSpace(parsed.Reply) == "" {
		parsed.Reply = synthetic.Reply
	}
	return parsed, nil
}

func synthesizeWorkModeBinaryResponse(resp adapters.Response, projectworkRoot, projectName string) (workModeAIResponse, error) {
	if len(resp.FileData) == 0 {
		return workModeAIResponse{}, errors.New("missing binary file data")
	}
	relPath, usedProvidedName, err := chooseWorkModeBinaryOutputPath(projectworkRoot, projectName, resp.FileName, resp.FileMIMEType)
	if err != nil {
		return workModeAIResponse{}, err
	}
	mimeType := detectBuilderBinaryMIMEType(resp.FileMIMEType, relPath, resp.FileData)
	artifactID := "work_mode_adapter_returned_file"
	reply := fmt.Sprintf("The model returned a direct binary file. AgentGO prepared it as %s.", filepath.ToSlash(relPath))
	warnings := []string{}
	if !usedProvidedName && strings.TrimSpace(resp.FileName) != "" {
		warnings = append(warnings, "Provider filename was unsafe, protected, or unusable, so AgentGO assigned a safe Work Mode filename.")
	}
	return workModeAIResponse{
		Reply: reply,
		Files: []builderFileOp{{
			Path:        filepath.ToSlash(relPath),
			Action:      "create",
			ArtifactRef: artifactID,
		}},
		Artifacts: []builderArtifact{{
			ID:       artifactID,
			Encoding: "base64",
			MIMEType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(resp.FileData),
		}},
		Warnings: warnings,
	}, nil
}

func chooseWorkModeBinaryOutputPath(projectworkRoot, projectName, name, mimeType string) (string, bool, error) {
	baseNames := []struct {
		name     string
		provided bool
	}{}
	cleanName := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if cleanName != "" {
		cleanName = path.Clean(cleanName)
		if cleanName != "." && cleanName != "/" && cleanName != ".." && !strings.HasPrefix(cleanName, "../") && !path.IsAbs(cleanName) {
			if filepath.Ext(cleanName) == "" {
				if ext := preferredBuilderBinaryExtension(mimeType); ext != "" {
					cleanName += ext
				}
			}
			baseNames = append(baseNames, struct {
				name     string
				provided bool
			}{name: cleanName, provided: true})
		}
	}
	baseNames = append(baseNames, struct {
		name     string
		provided bool
	}{name: defaultBuilderBinaryFilename(mimeType), provided: false})

	for _, candidate := range baseNames {
		rel, err := normalizeWorkModeProjectworkRel(candidate.name, projectName)
		if err != nil {
			continue
		}
		unique, err := uniqueWorkModeOutputPath(projectworkRoot, rel)
		if err != nil {
			continue
		}
		return filepath.ToSlash(unique), candidate.provided, nil
	}
	return "", false, errors.New("could not choose safe Work Mode binary output path")
}

func uniqueWorkModeOutputPath(projectworkRoot, rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return "", errors.New("empty output path")
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	name := path.Base(rel)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem = "returned_file"
	}
	for i := 0; i < 1000; i++ {
		candidateName := name
		if i > 0 {
			candidateName = fmt.Sprintf("%s_%d%s", stem, i+1, ext)
		}
		candidate := candidateName
		if dir != "" {
			candidate = path.Join(dir, candidateName)
		}
		target, err := safeJoin(projectworkRoot, candidate)
		if err != nil {
			return "", err
		}
		if err := rejectSymlinkPath(projectworkRoot, target); err != nil {
			continue
		}
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.ToSlash(candidate), nil
		}
		if err != nil {
			return "", err
		}
		if info.IsDir() || isSymlinkMode(info.Mode()) {
			continue
		}
	}
	return "", errors.New("could not find unused Work Mode output path")
}

func isHiddenWorkModePath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func normalizeWorkModeProjectworkRel(raw, projectName string) (string, error) {
	clean := filepath.ToSlash(strings.TrimSpace(raw))
	clean = strings.ReplaceAll(clean, "\\", "/")
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" {
		return "", errors.New("empty file path")
	}
	prefix := path.Join("projects", projectName, "projectwork")
	if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
		clean = strings.TrimPrefix(clean, prefix)
		clean = strings.TrimPrefix(clean, "/")
	}
	clean = path.Clean(clean)
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", errors.New("path must stay inside projectwork")
	}
	if isHiddenWorkModePath(clean) {
		return "", errors.New("hidden files are not allowed in Work Mode")
	}
	if strings.EqualFold(path.Base(clean), "project.json") {
		return "", errors.New("project.json is protected")
	}
	return clean, nil
}

func normalizeWorkModeSelectedFiles(input []string, projectName string) ([]string, map[string]bool, error) {
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, item := range input {
		rel, err := normalizeWorkModeProjectworkRel(item, projectName)
		if err != nil {
			return nil, nil, err
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out, seen, nil
}

func buildWorkModeInstructions(model ModelConfig, includeRoleContext, useMemory bool, responseMode string, updateableFiles []string) string {
	fileMode := strings.TrimSpace(`WORK MODE FILE OPERATIONS

Use the "reply" field for normal chat responses, explanations, summaries, brainstorming, reviews, and discussion.

You must output valid JSON only, with no markdown outside the JSON.

Use this exact response shape:

{
  "reply": "User-facing response or summary of actions here.",
  "files": []
}

If you create or overwrite files, include them in "files":

{
  "reply": "User-facing response or summary of actions here.",
  "files": [
    {
      "path": "folder/filename.ext",
      "action": "create",
      "content": "Full, complete file content here."
    }
  ]
}

Allowed file actions are exactly:
- "create"
- "overwrite"

FILE ARRAY RULES

Keep "files": [] unless the user clearly requests one of these:
1. a new durable projectwork file or artifact
2. an existing Work Mode projectwork file to be modified

Selecting or attaching a file gives you context. It does not by itself mean the user wants the file edited.

CREATE FILES

Use action "create" when the user clearly asks to create, build, generate, save, export, or write a new durable file or artifact.

For created files:
- Use only safe projectwork-relative paths, such as "notes.md", "snake_game.html", or "games/snake_game.html".
- Do not use absolute paths.
- Do not use directory traversal such as "../".
- Do not use "create" to replace an existing file.
- Do not create files unnecessarily.

UPDATE FILES

Use action "overwrite" only when the user clearly asks to edit, fix, revise, rewrite, refactor, continue, or replace an existing Work Mode projectwork file.

You may only overwrite files listed in UPDATEABLE PROJECTWORK FILES.

For overwritten files:
- Use the exact projectwork-relative path shown in UPDATEABLE PROJECTWORK FILES.
- Provide the full replacement content for the file.
- Do not return partial patches, diffs, excerpts, placeholders, or instructions instead of full file content.
- Do not overwrite any file that was not sent to you in this Work Mode run.
- If the user asks you to modify a file that is not listed in UPDATEABLE PROJECTWORK FILES, ask them to select or pass that file first.

Temporary attachments cannot be overwritten. If the user wants a modified version of a temporary attachment, create a new projectwork file instead.

GENERAL CONSTRAINTS

Do not create or update files unnecessarily.

Do not claim that a file was created or updated in "reply" unless the matching operation is included in "files".

Do not request or attempt file deletion. AgentGO Work Mode does not currently support AI-driven deletes.`)
	if len(updateableFiles) > 0 {
		lines := []string{"UPDATEABLE PROJECTWORK FILES", "", "The following projectwork files were sent to you in this Work Mode run and may be overwritten if the user clearly requested edits:"}
		for _, file := range updateableFiles {
			file = strings.TrimSpace(filepath.ToSlash(file))
			if file != "" {
				lines = append(lines, "- "+file)
			}
		}
		fileMode += "\n\n" + strings.Join(lines, "\n")
	}
	instructions := strings.TrimSpace(`You are the selected AI Builder for this AgentGO Work Mode conversation.

ROLE
- Treat this as direct work with the user inside the active project.
- Answer the user naturally in reply.
- Use the provided selected projectwork files and temporary attachments as context.

CONTEXT LIMITS
- Only use projectwork file contents that AgentGO explicitly sends in this Work Mode run.
- If no selected projectwork files are sent, you have no current projectwork file contents for this message.
- Do not imply you opened, inspected, created, saved, or updated any projectwork file unless it was sent as context or included as a files[] operation in this response.
- If the task requires unselected project files, say which files the user should select instead of guessing.
- Treat the final USER REQUEST message as the authoritative instruction after any context messages.

` + fileMode + `

OUTPUT FORMAT
- Return one strict JSON object matching the schema.
- Put the complete visible answer for the user in reply.
- Do not include markdown fences around the JSON response.`)
	if includeRoleContext {
		instructions += "\n\nROLE CONTEXT\n- AgentGO included this Builder's role/user context. Use it when relevant."
	} else {
		instructions += "\n\nROLE CONTEXT\n- Role/user context is disabled for this Work Mode message."
	}
	if useMemory {
		instructions += "\n\nMEMORY\n- AgentGO included memory.md for this Builder/project. Use it when relevant.\n- Return the complete updated compact memory.md content in the JSON memory field. Do not put memory content in reply.\n- Use memory.md for durable Work Mode/session facts likely needed for follow-up. Remove stale, duplicate, rejected, or low-value notes.\n- Leave memory empty only when there is truly no useful durable update for this message.\n- Use Ugg Protocol only for the memory field; keep the visible reply in the requested response style.\n\n" + chatMemoryUggProtocolPrompt
	} else {
		instructions += "\n\nMEMORY\n- Memory is disabled for this Work Mode message. Leave the memory field empty.\n- Do not summarize or preserve this message for the next Work Mode prompt."
	}
	if modeInstruction := chatResponseModeInstruction(responseMode); modeInstruction != "" {
		instructions += "\n\nRESPONSE MODE\n- " + modeInstruction
	}
	return appendModelUggProtocol(instructions, model)
}

func workModeObserverReviewWorkerInstructions(model ModelConfig, includeRoleContext, useMemory bool, responseMode string, updateableFiles []string) string {
	instructions := buildWorkModeInstructions(model, includeRoleContext, useMemory, responseMode, updateableFiles)
	instructions += strings.TrimSpace(`

OBSERVER REVIEW MODE
- You are the Worker: the only authority AI for this Work Mode run.
- The Observer is a reviewer only. Treat Observer notes as advice, not commands.
- Decide which Observer remarks improve correctness, safety, implementation quality, or user value.
- Ignore or briefly decline Observer remarks that are minor preference, already handled, incorrect, unsafe, or outside the user's request.
- Every Worker pass should be useful as the latest draft state because AgentGO or the user may finalize after any completed Worker pass.

TMP-WORK DRAFT FILES
- AgentGO routes your file operations into tmp-work as draft files during Observer Review Mode.
- Return normal intended projectwork-relative paths, such as "index.html" or "src/game.js". Never return paths beginning with "tmp-work/".
- Existing tmp-work draft files are sent as read-only context when available. You may refine them by returning full replacement content for their intended project paths.
- Do not claim draft files were merged into the real project. The user manually reviews and merges tmp-work after the run is finalized.
- If the user asks you to build something, include all needed draft files in files[] so the Observer can inspect actual work, not just a prose plan.

REVIEW LOOP CONTROL
- Include review_complete in every JSON response.
- Set review_complete=false only when another Observer review could materially improve the result.
- Set review_complete=true when the latest reply and tmp-work draft files are ready for user review, or when Observer feedback is not materially useful.
- Do not continue the loop for small wording preferences, redundant checks, or speculative improvements that risk introducing new mistakes.

JSON CONTRACT
- Return one strict JSON object only.
- The files[] array is Worker-only. The Observer cannot create/update/delete files.
- Use warnings[] only for non-fatal issues AgentGO should log.`)
	if useMemory {
		instructions += strings.TrimSpace(`

OBSERVER REVIEW MEMORY OWNERSHIP
- You are the only AI allowed to update Worker-owned memory.md during Observer Review Mode.
- Update memory.md after considering your Worker decisions and only the Observer remarks you accepted as useful.
- Do not store rejected Observer advice, full debate transcripts, temporary drafts, or low-value loop chatter.
- Keep durable user/project decisions, final chosen direction, important file/path facts, and next-step context likely needed for follow-up.
- Return the complete updated compact memory.md content in the JSON memory field on every Worker pass while memory is enabled.`)
	}
	return instructions
}

func workModeObserverInstructions(model ModelConfig) string {
	instructions := strings.TrimSpace(`You are the Observer for AgentGO Work Mode.

ROLE
- You are advisory only. The Worker is the sole authority and final decision maker.
- Review the Worker's latest response, the current tmp-work draft files, selected project context, temporary attachments, and transcript.
- Find material errors, missing requirements, unsafe assumptions, weak implementation details, broken files, incomplete files, or important improvements.
- Be concise, specific, and actionable. Prefer numbered or short bullet-style notes inside reply.

STRICT LIMITS
- You cannot create, overwrite, delete, rename, move, merge, save, or apply files.
- You cannot update memory. Any memory.md content AgentGO sends you is read-only Worker-owned context.
- You cannot finalize the answer.
- AgentGO will ignore any file, memory, or finalization operation you attempt to return.

HAS_INPUT RULE
- Set has_input=true only when your feedback is material enough to justify another Worker pass.
- Set has_input=false when the Worker output and tmp-work files look acceptable, when your remaining feedback is minor style/preference, or when another pass would likely add cost/risk without meaningful benefit.
- Do not mark has_input=true just to praise the Worker, repeat the user's request, or request tiny wording polish.

OUTPUT FORMAT
Return one strict JSON object only, with no markdown outside JSON:
{
  "reply": "Concise review notes for the Worker.",
  "has_input": true,
  "recommendations": ["Optional material recommendation 1"]
}`)
	return appendModelUggProtocol(instructions, model)
}

func workModeUniqueRelPaths(pathsIn ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range pathsIn {
		for _, rel := range group {
			rel = cleanSlashPath(rel)
			if rel == "" || seen[rel] || strings.HasPrefix(rel, workModeTmpWorkDirName+"/") || rel == workModeTmpWorkDirName {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func workModeListRelFilesUnderRoot(root string) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("missing file root")
	}
	if err := rejectSymlinkPath(root, root); err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	out := []string{}
	err := filepath.WalkDir(root, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || fullPath == root || entry == nil {
			return walkErr
		}
		if isSymlinkDirEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, fullPath)
		if err != nil {
			return nil
		}
		rel = cleanSlashPath(rel)
		if rel == "" || isHiddenWorkModePath(rel) || strings.EqualFold(path.Base(rel), "project.json") {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func workModePrefixedTmpWorkState(tmpRels []string) []string {
	out := make([]string, 0, len(tmpRels))
	seen := map[string]bool{}
	for _, rel := range tmpRels {
		rel = cleanSlashPath(rel)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, path.Join(workModeTmpWorkDirName, rel))
	}
	sort.Strings(out)
	return out
}

func workModeObserverWorkerTask(pass int, userPrompt string) string {
	if pass <= 1 {
		return strings.TrimSpace("FINAL USER REQUEST:\n" + strings.TrimSpace(userPrompt) + `

WORKER PASS 1 TASK:
Create the first Worker draft response and any draft files requested by the user. Return file operations for intended projectwork-relative paths so AgentGO can place them in tmp-work. Set review_complete=true only if the task is already ready for user inspection without Observer review; otherwise set review_complete=false.`)
	}
	return strings.TrimSpace(fmt.Sprintf(`WORKER PASS %d TASK:
Consider the Observer feedback and the current tmp-work draft files. Accept only feedback that materially improves correctness, safety, implementation quality, or user value. Revise tmp-work files if useful by returning full updated file content for intended project paths. Set review_complete=true if the latest answer and tmp-work draft files are ready for user review; otherwise set review_complete=false to request another Observer review.`, pass))
}

func workModeObserverTask(pass int) string {
	return strings.TrimSpace(fmt.Sprintf(`OBSERVER PASS %d TASK:
Review the latest Worker response and current tmp-work draft files. Return has_input=true only for material corrections or improvements. Return has_input=false if nothing important needs to be changed or another Worker pass is not worth the added cost/risk. Use recommendations[] for concise actionable items when helpful.`, pass))
}

func workModeReviewTranscript(projectName, userPrompt string, messages []workModeReviewMessage) string {
	lines := []string{
		"WORK MODE OBSERVER REVIEW TRANSCRIPT",
		"PROJECT: " + strings.TrimSpace(projectName),
		"",
		"[User / Final Request]",
		strings.TrimSpace(userPrompt),
	}
	for _, msg := range messages {
		owner := strings.TrimSpace(msg.Owner)
		if owner == "" {
			owner = "AgentGO"
		}
		label := owner
		if msg.Pass > 0 {
			label = fmt.Sprintf("%s pass %d", owner, msg.Pass)
		}
		if msg.HasInput != nil {
			label += fmt.Sprintf(" · has_input=%v", *msg.HasInput)
		}
		lines = append(lines, "", "["+label+"]", strings.TrimSpace(msg.Reply))
		if len(msg.Recommendations) > 0 {
			lines = append(lines, "RECOMMENDATIONS:")
			for _, recommendation := range msg.Recommendations {
				recommendation = strings.TrimSpace(recommendation)
				if recommendation != "" {
					lines = append(lines, "- "+recommendation)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func workModeMaybeJSONBoolPtr(value bool) *bool {
	v := value
	return &v
}

func (a *App) workModeSessionStateForExecution(projectName, executionID string) (workModeSessionState, bool) {
	projectName = strings.TrimSpace(projectName)
	executionID = strings.TrimSpace(executionID)
	if projectName == "" || executionID == "" {
		return workModeSessionState{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.workModeSessionsByProject[projectName]
	if !ok || strings.TrimSpace(state.ExecutionID) != executionID {
		return workModeSessionState{}, false
	}
	return state.clone(), true
}

func (a *App) waitForWorkModeObserverLoopClearance(ctx context.Context, projectName, executionID string) (workModeSessionState, string, error) {
	for {
		state, ok := a.workModeSessionStateForExecution(projectName, executionID)
		if !ok {
			return workModeSessionState{}, "stale", errors.New("Work Mode session changed while Observer Review was running")
		}
		switch strings.TrimSpace(state.Status) {
		case workModeStatusEmergencyStopped:
			return state, "emergency", context.Canceled
		case workModeStatusFinalized:
			return state, "finalized", nil
		case workModeStatusPausedAfterCurrent:
			select {
			case <-ctx.Done():
				return state, "canceled", ctx.Err()
			case <-time.After(300 * time.Millisecond):
				continue
			}
		default:
			return state, "running", nil
		}
	}
}

func (a *App) setWorkModeObserverActiveCall(projectName, executionID, modelID, owner string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setActiveCancelLocked(modelID, projectName, executionID, cancel)
	state := a.workModeSessionsByProject[projectName]
	if strings.TrimSpace(state.ExecutionID) == executionID {
		state.ActiveCallOwner = owner
		state.Status = workModeStatusRunning
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		a.workModeSessionsByProject[projectName] = state.clone()
	}
}

func (a *App) clearWorkModeObserverActiveCall(projectName, executionID, modelID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clearActiveCancelLocked(modelID, executionID)
	state := a.workModeSessionsByProject[projectName]
	if strings.TrimSpace(state.ExecutionID) == executionID {
		state.ActiveCallOwner = workModeCallOwnerNone
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		a.workModeSessionsByProject[projectName] = state.clone()
	}
}

func workModeReviewStatusAfterCompletedCall(currentStatus string) string {
	switch strings.TrimSpace(currentStatus) {
	case workModeStatusPausedAfterCurrent, workModeStatusFinalized, workModeStatusEmergencyStopped:
		return strings.TrimSpace(currentStatus)
	default:
		return workModeStatusRunning
	}
}

func (a *App) updateWorkModeObserverReviewProgressLocked(projectName, executionID string, mutate func(*workModeSessionState)) workModeSessionState {
	projectName = strings.TrimSpace(projectName)
	state := a.workModeSessionsByProject[projectName]
	if strings.TrimSpace(state.ExecutionID) != strings.TrimSpace(executionID) {
		return state.clone()
	}
	state.Status = workModeReviewStatusAfterCompletedCall(state.Status)
	state.ActiveCallOwner = workModeCallOwnerNone
	if mutate != nil {
		mutate(&state)
	}
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	a.workModeSessionsByProject[projectName] = state.clone()
	return state.clone()
}

func workModeBlockedOutputFromOp(rel string, op builderFileOp, artifactBytes map[string][]byte, reason string) workModeBlockedFileOutput {
	action := strings.ToLower(strings.TrimSpace(op.Action))
	out := workModeBlockedFileOutput{Path: rel, Action: action, Reason: reason}
	if strings.TrimSpace(op.Content) != "" {
		out.Content = op.Content
		return out
	}
	ref := strings.TrimSpace(op.ArtifactRef)
	if ref == "" {
		return out
	}
	data, ok := artifactBytes[ref]
	if !ok {
		out.ContentOmitted = true
		out.Content = "[artifact data unavailable]"
		return out
	}
	if utf8.Valid(data) {
		out.Content = string(data)
		return out
	}
	out.ContentOmitted = true
	out.Content = fmt.Sprintf("[binary artifact omitted; %d bytes]", len(data))
	return out
}

func workModeApplyFileOps(projectworkRoot, projectName string, ops []builderFileOp, artifacts []builderArtifact, limits ProjectLimits, updateable map[string]bool) ([]string, []string, []workModeBlockedFileOutput, []workModeDiffFile, error) {
	limits = normalizeProjectLimits(limits)
	artifactBytes, artifactMeta, err := decodeBuilderArtifacts(artifacts)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	changed := []string{}
	skipped := []string{}
	blocked := []workModeBlockedFileOutput{}
	diffs := []workModeDiffFile{}
	filesToWrite := 0
	totalPayloadBytes := 0
	type pendingOp struct {
		rel     string
		op      builderFileOp
		payload []byte
	}
	pending := []pendingOp{}
	for _, op := range ops {
		action := strings.ToLower(strings.TrimSpace(op.Action))
		rel, err := normalizeWorkModeProjectworkRel(op.Path, projectName)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: invalid path", strings.TrimSpace(op.Path)))
			continue
		}
		if action == "delete" {
			skipped = append(skipped, rel+": delete is not supported in Work Mode")
			continue
		}
		if action != "create" && action != "overwrite" {
			skipped = append(skipped, rel+": unsupported action")
			continue
		}
		if action == "overwrite" && !updateable[rel] {
			reason := "update skipped because the file was not sent to the AI in this Work Mode run"
			skipped = append(skipped, rel+": "+reason)
			blocked = append(blocked, workModeBlockedOutputFromOp(rel, op, artifactBytes, reason))
			continue
		}
		payload := []byte(op.Content)
		if ref := strings.TrimSpace(op.ArtifactRef); ref != "" {
			data, ok := artifactBytes[ref]
			if !ok {
				skipped = append(skipped, rel+": missing artifact")
				continue
			}
			if op.Content != "" {
				skipped = append(skipped, rel+": provide content or artifact_ref, not both")
				continue
			}
			payload = data
			artifact := artifactMeta[ref]
			if workModeLooksLikeImageOutput(rel, artifact.MIMEType) && !workModePayloadLooksLikeImage(data, artifact.MIMEType) {
				reason := "image output skipped because the artifact data is not a valid image payload"
				skipped = append(skipped, rel+": "+reason)
				blocked = append(blocked, workModeBlockedOutputFromOp(rel, op, artifactBytes, reason))
				continue
			}
		}
		if action == "create" && len(payload) == 0 && strings.TrimSpace(op.ArtifactRef) == "" {
			// Allow intentionally empty new files.
		}
		if len(payload) > limits.MaxFileSizeKB*1024 {
			return changed, skipped, blocked, diffs, fmt.Errorf("Work Mode rejected: file %q exceeds max_file_size_kb (%d bytes > %d KB)", rel, len(payload), limits.MaxFileSizeKB)
		}
		filesToWrite++
		totalPayloadBytes += len(payload)
		pending = append(pending, pendingOp{rel: rel, op: op, payload: payload})
	}
	if filesToWrite > limits.MaxFiles {
		return changed, skipped, blocked, diffs, fmt.Errorf("Work Mode rejected: max_files exceeded (%d > %d)", filesToWrite, limits.MaxFiles)
	}
	if totalPayloadBytes > limits.MaxPayloadKB*1024 {
		return changed, skipped, blocked, diffs, fmt.Errorf("Work Mode rejected: max_payload_kb exceeded (%d bytes > %d KB)", totalPayloadBytes, limits.MaxPayloadKB)
	}
	for _, item := range pending {
		target, err := safeJoin(projectworkRoot, item.rel)
		if err != nil {
			skipped = append(skipped, item.rel+": invalid target")
			continue
		}
		action := strings.ToLower(strings.TrimSpace(item.op.Action))
		if err := rejectSymlinkPath(projectworkRoot, target); err != nil {
			skipped = append(skipped, item.rel+": unsupported symlink path")
			continue
		}
		info, statErr := os.Stat(target)
		if action == "create" {
			if statErr == nil {
				skipped = append(skipped, item.rel+": file already exists")
				continue
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return changed, skipped, blocked, diffs, statErr
			}
		} else if action == "overwrite" {
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					skipped = append(skipped, item.rel+": file not found")
					continue
				}
				return changed, skipped, blocked, diffs, statErr
			}
			if info.IsDir() {
				skipped = append(skipped, item.rel+": cannot update a folder")
				continue
			}
		}
		previous := []byte{}
		previousOmitted := false
		if action == "overwrite" {
			previous, err = readFileUnderRoot(projectworkRoot, target)
			if err != nil {
				return changed, skipped, blocked, diffs, err
			}
			if !utf8.Valid(previous) {
				previousOmitted = true
			}
		}
		currentOmitted := !utf8.Valid(item.payload)
		if err := writeFileUnderRoot(projectworkRoot, target, item.payload, 0o644); err != nil {
			return changed, skipped, blocked, diffs, err
		}
		changed = append(changed, item.rel)
		if !currentOmitted && !previousOmitted {
			diffs = append(diffs, workModeDiffFile{Path: item.rel, Previous: string(previous), Current: string(item.payload)})
		} else if currentOmitted || previousOmitted {
			diffs = append(diffs, workModeDiffFile{Path: item.rel, PreviousOmitted: previousOmitted, CurrentOmitted: currentOmitted})
		}
	}
	return changed, skipped, blocked, diffs, nil
}

func workModeInlineFilesForChanged(projectworkRoot, projectName string, changed []string) []workModeInlineFileOutput {
	out := []workModeInlineFileOutput{}
	seen := map[string]bool{}
	for _, rel := range changed {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		target, err := safeJoin(projectworkRoot, rel)
		if err != nil {
			continue
		}
		data, err := readFileUnderRoot(projectworkRoot, target)
		if err != nil {
			continue
		}
		contentType := detectContentType(rel, data)
		previewKind := previewKindForContentType(contentType)
		if previewKind != "image" || !workModePayloadLooksLikeImage(data, contentType) {
			continue
		}
		workRel := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", filepath.FromSlash(rel)))
		out = append(out, workModeInlineFileOutput{
			Path:        workRel,
			RelPath:     rel,
			PreviewKind: previewKind,
			ContentType: contentType,
			BlobURL:     buildBlobURL(workRel),
		})
	}
	return out
}

func approximateWorkModeOutputTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	charEstimate := (len([]rune(text)) + 3) / 4
	wordEstimate := len(strings.Fields(text)) * 4 / 3
	if wordEstimate > charEstimate {
		return wordEstimate
	}
	return charEstimate
}

func workModeOutputLimitWarning(model ModelConfig, resp adapters.Response) string {
	maxTokens := model.MaxOutputTokens
	if maxTokens <= 0 {
		return ""
	}
	estimated := approximateWorkModeOutputTokens(resp.Text)
	if estimated <= 0 {
		return ""
	}
	threshold := int(math.Ceil(float64(maxTokens) * 0.90))
	if threshold < 1 {
		threshold = maxTokens
	}
	if estimated < threshold {
		return ""
	}
	return "It looks like you may be brushing up against your default max token output. You can tell AgentGO to ask the AI to increase this limit in your model setup area."
}

func normalizeWorkModeMaxPasses(value int) int {
	if value <= 0 {
		return 3
	}
	if value > 100 {
		return 100
	}
	return value
}

func cloneWorkModeReviewMessages(in []workModeReviewMessage) []workModeReviewMessage {
	if len(in) == 0 {
		return nil
	}
	out := make([]workModeReviewMessage, len(in))
	for i, msg := range in {
		out[i] = msg
		if msg.HasInput != nil {
			v := *msg.HasInput
			out[i].HasInput = &v
		}
		if len(msg.Recommendations) > 0 {
			out[i].Recommendations = append([]string{}, msg.Recommendations...)
		}
		if len(msg.ChangedFiles) > 0 {
			out[i].ChangedFiles = append([]string{}, msg.ChangedFiles...)
		}
		if len(msg.SkippedFiles) > 0 {
			out[i].SkippedFiles = append([]string{}, msg.SkippedFiles...)
		}
		if len(msg.BlockedFiles) > 0 {
			out[i].BlockedFiles = append([]workModeBlockedFileOutput{}, msg.BlockedFiles...)
		}
		if len(msg.InlineFiles) > 0 {
			out[i].InlineFiles = append([]workModeInlineFileOutput{}, msg.InlineFiles...)
		}
	}
	return out
}

func (s workModeSessionState) clone() workModeSessionState {
	if len(s.LatestWorkerFileState) > 0 {
		s.LatestWorkerFileState = append([]string{}, s.LatestWorkerFileState...)
	}
	if s.ObserverHasInput != nil {
		v := *s.ObserverHasInput
		s.ObserverHasInput = &v
	}
	s.ReviewMessages = cloneWorkModeReviewMessages(s.ReviewMessages)
	return s
}

func newWorkModeSessionState(projectName string, roles workModeRoleSelection, observerReview bool, maxPasses int, prompt string) workModeSessionState {
	mode := workModeModeNormal
	if observerReview {
		mode = workModeModeObserverReview
	}
	state := workModeSessionState{
		ProjectName:     strings.TrimSpace(projectName),
		Mode:            mode,
		Status:          workModeStatusRunning,
		WorkerID:        modelIDString(roles.Worker.ID),
		WorkerLabel:     strings.TrimSpace(roles.Worker.Label),
		ObserverReview:  observerReview,
		CurrentPass:     0,
		MaxPasses:       normalizeWorkModeMaxPasses(maxPasses),
		ActiveCallOwner: workModeCallOwnerNone,
		InitialPrompt:   strings.TrimSpace(prompt),
		UpdatedAt:       time.Now().Format(time.RFC3339),
	}
	if roles.HasObserver {
		state.ObserverID = modelIDString(roles.Observer.ID)
		state.ObserverLabel = strings.TrimSpace(roles.Observer.Label)
	}
	return state
}

func (a *App) resolveWorkModeRoles(workerModelID, observerModelID string, requireObserver bool) (workModeRoleSelection, int, error) {
	workerModelID = strings.TrimSpace(workerModelID)
	observerModelID = strings.TrimSpace(observerModelID)
	builders := a.activeBuilderModelsSorted()
	if len(builders) != 1 {
		return workModeRoleSelection{}, http.StatusBadRequest, errors.New("Work Mode requires exactly one active Builder AI.")
	}
	worker := builders[0]
	workerID := modelIDString(worker.ID)
	if workerModelID != "" && workerModelID != workerID {
		return workModeRoleSelection{}, http.StatusBadRequest, errors.New("The active Work Mode Builder changed. Reopen Work Mode and try again.")
	}
	roles := workModeRoleSelection{Worker: worker}
	reviewerID := strings.TrimSpace(a.getReviewerID())
	if reviewerID == "" {
		if requireObserver {
			return workModeRoleSelection{}, http.StatusBadRequest, errors.New("Observer Review Mode requires exactly one Observer.")
		}
		return roles, http.StatusOK, nil
	}
	reviewer, ok := a.findModel(reviewerID)
	if !ok {
		if requireObserver {
			return workModeRoleSelection{}, http.StatusBadRequest, errors.New("The selected Observer could not be found. Reopen Work Mode and try again.")
		}
		return roles, http.StatusOK, nil
	}
	if observerModelID != "" && observerModelID != reviewerID {
		return workModeRoleSelection{}, http.StatusBadRequest, errors.New("The active Work Mode Observer changed. Reopen Work Mode and try again.")
	}
	roles.Observer = reviewer
	roles.HasObserver = true
	return roles, http.StatusOK, nil
}

func (a *App) setWorkModeSessionStateLocked(projectName string, state workModeSessionState) workModeSessionState {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = strings.TrimSpace(state.ProjectName)
	}
	if projectName == "" {
		return state.clone()
	}
	if a.workModeSessionsByProject == nil {
		a.workModeSessionsByProject = map[string]workModeSessionState{}
	}
	state.ProjectName = projectName
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	a.workModeSessionsByProject[projectName] = state.clone()
	return state.clone()
}

func (a *App) updateWorkModeSessionStatusLocked(projectName, status, activeCallOwner string) workModeSessionState {
	projectName = strings.TrimSpace(projectName)
	if a.workModeSessionsByProject == nil {
		a.workModeSessionsByProject = map[string]workModeSessionState{}
	}
	state := a.workModeSessionsByProject[projectName]
	if strings.TrimSpace(state.ProjectName) == "" {
		state.ProjectName = projectName
		state.Mode = workModeModeNormal
		state.MaxPasses = 3
	}
	if strings.TrimSpace(status) != "" {
		state.Status = strings.TrimSpace(status)
	}
	if strings.TrimSpace(activeCallOwner) != "" {
		state.ActiveCallOwner = strings.TrimSpace(activeCallOwner)
	}
	if strings.TrimSpace(state.ActiveCallOwner) == "" {
		state.ActiveCallOwner = workModeCallOwnerNone
	}
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	a.workModeSessionsByProject[projectName] = state.clone()
	return state.clone()
}

func (a *App) getWorkModeSessionState(projectName string) (workModeSessionState, bool) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return workModeSessionState{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.workModeSessionsByProject[projectName]
	if !ok {
		return workModeSessionState{}, false
	}
	return state.clone(), true
}

func (a *App) markWorkModeSessionsEmergencyStoppedLocked(projectName string) {
	if a.workModeSessionsByProject == nil {
		return
	}
	projectName = strings.TrimSpace(projectName)
	for key, state := range a.workModeSessionsByProject {
		if projectName != "" && strings.TrimSpace(key) != projectName {
			continue
		}
		state.Status = workModeStatusEmergencyStopped
		state.ActiveCallOwner = workModeCallOwnerNone
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		a.workModeSessionsByProject[key] = state.clone()
	}
}

func (a *App) handleWorkModeSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projectName, err := a.requireActiveProject()
		if err != nil {
			http.Error(w, "Select an active project first.", http.StatusBadRequest)
			return
		}
		state, ok := a.getWorkModeSessionState(projectName)
		if !ok {
			roles, _, roleErr := a.resolveWorkModeRoles("", "", false)
			if roleErr == nil {
				state = newWorkModeSessionState(projectName, roles, false, 3, "")
				state.Status = workModeStatusFinalized
				state.ActiveCallOwner = workModeCallOwnerNone
			} else {
				state = workModeSessionState{ProjectName: projectName, Mode: workModeModeNormal, Status: workModeStatusFinalized, MaxPasses: 3, ActiveCallOwner: workModeCallOwnerNone, UpdatedAt: time.Now().Format(time.RFC3339)}
			}
		}
		writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: "Work Mode session state loaded."})
	case http.MethodPost:
		var req workModeSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		projectName, err := a.requireActiveProject()
		if err != nil {
			http.Error(w, "Select an active project first.", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "start"
		}
		requireObserver := req.ObserverReview || action == "start_observer"
		roles, status, err := a.resolveWorkModeRoles(req.WorkerModelID, req.ObserverModelID, requireObserver)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		observerReview := req.ObserverReview && roles.HasObserver
		var state workModeSessionState
		message := "Work Mode session state updated."
		switch action {
		case "start", "start_observer":
			state = newWorkModeSessionState(projectName, roles, observerReview, req.MaxPasses, req.Prompt)
			if observerReview {
				state.Mode = workModeModeObserverReview
				projectworkRoot, rootErr := a.projectWorkRoot(projectName)
				if rootErr != nil {
					http.Error(w, rootErr.Error(), http.StatusBadRequest)
					return
				}
				tmpWorkRoot, rootErr := workModeTmpWorkProjectRoot(projectworkRoot)
				if rootErr != nil {
					http.Error(w, rootErr.Error(), http.StatusBadRequest)
					return
				}
				if rootErr := os.MkdirAll(tmpWorkRoot, 0o755); rootErr != nil {
					http.Error(w, rootErr.Error(), http.StatusInternalServerError)
					return
				}
				message = "Observer Review state initialized. tmp-work is ready for Worker/Observer orchestration."
			} else {
				state.Mode = workModeModeNormal
				message = "Normal Work Mode state initialized."
			}
		case "pause":
			a.mu.Lock()
			state = a.updateWorkModeSessionStatusLocked(projectName, workModeStatusPausedAfterCurrent, "")
			a.mu.Unlock()
			message = "Pause requested. AgentGO will wait before the next Worker/Observer send."
			writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
			return
		case "resume", "unpause":
			a.mu.Lock()
			state = a.updateWorkModeSessionStatusLocked(projectName, workModeStatusRunning, workModeCallOwnerNone)
			a.mu.Unlock()
			message = "Observer Review session resumed."
			writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
			return
		case "finalize":
			a.mu.Lock()
			current := a.workModeSessionsByProject[projectName]
			a.cancelActiveCallsForProjectLocked(projectName, current.ExecutionID)
			state = a.updateWorkModeSessionStatusLocked(projectName, workModeStatusFinalized, workModeCallOwnerNone)
			a.mu.Unlock()
			if strings.TrimSpace(current.LatestWorkerMessage) == "" && current.CurrentPass <= 0 {
				message = "Observer Review finalized before the first Worker pass completed. No new Worker output was accepted."
			} else {
				message = "Observer Review session finalized. Review tmp-work, then merge selected draft files or use Merge all."
			}
			writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
			return
		case "emergency_stop":
			a.mu.Lock()
			state = a.updateWorkModeSessionStatusLocked(projectName, workModeStatusEmergencyStopped, workModeCallOwnerNone)
			a.mu.Unlock()
			message = "Work Mode session marked as emergency stopped."
			writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
			return
		case "clear":
			a.mu.Lock()
			if a.workModeSessionsByProject != nil {
				delete(a.workModeSessionsByProject, projectName)
			}
			a.mu.Unlock()
			state = workModeSessionState{ProjectName: projectName, Mode: workModeModeNormal, Status: workModeStatusFinalized, MaxPasses: 3, ActiveCallOwner: workModeCallOwnerNone, UpdatedAt: time.Now().Format(time.RFC3339)}
			message = "Work Mode session state cleared."
			writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
			return
		default:
			http.Error(w, "unknown Work Mode session action", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		state = a.setWorkModeSessionStateLocked(projectName, state)
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, workModeSessionResponse{OK: true, State: state, Message: message})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWorkModeObserverReviewSend(w http.ResponseWriter, req workModeRequest, projectName string) {
	roles, status, roleErr := a.resolveWorkModeRoles(req.ModelID, req.ObserverModelID, true)
	if roleErr != nil {
		http.Error(w, roleErr.Error(), status)
		return
	}
	worker := roles.Worker
	observer := roles.Observer
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpWorkRoot, err := workModeTmpWorkProjectRoot(projectworkRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(tmpWorkRoot, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	selectedFiles, _, err := normalizeWorkModeSelectedFiles(req.SelectedFiles, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, workerMetaRoot, err := a.projectPaths(worker, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, observerMetaRoot, err := a.projectPaths(observer, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	includeRoleContext := true
	if req.IncludeRoleContext != nil {
		includeRoleContext = *req.IncludeRoleContext
	}
	workerUserContext := []byte("{}")
	observerUserContext := []byte("{}")
	if includeRoleContext {
		workerUserContext, _ = os.ReadFile(filepath.Join(workerMetaRoot, "user_context.json"))
		observerUserContext, _ = os.ReadFile(filepath.Join(observerMetaRoot, "user_context.json"))
	}
	memoryPath := filepath.Join(workerMetaRoot, chatMemoryFileName)
	chatMemory := []byte("")
	if req.UseMemory {
		chatMemory, _ = os.ReadFile(memoryPath)
	}
	limits, err := a.loadProjectLimits(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workerTransport := adapters.ResolveTransportProfile(toAdapterModelConfig(worker))
	observerTransport := adapters.ResolveTransportProfile(toAdapterModelConfig(observer))
	maxPasses := normalizeWorkModeMaxPasses(req.MaxPasses)
	executionID := "workmode-observer-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	preStartState, hasPreStartState := a.getWorkModeSessionState(projectName)
	if hasPreStartState && preStartState.Mode == workModeModeObserverReview && preStartState.Status == workModeStatusFinalized && strings.TrimSpace(preStartState.LatestWorkerMessage) == "" && strings.TrimSpace(preStartState.InitialPrompt) == strings.TrimSpace(req.Prompt) {
		state := preStartState.clone()
		reply := "Observer Review ended before any Worker output was completed. No tmp-work changes were applied."
		writeJSON(w, http.StatusOK, workModeResponse{Reply: reply, AgentMessage: "Observer Review was finalized before the first Worker pass completed. No new Worker output was accepted.", State: &state})
		return
	}
	initialState := newWorkModeSessionState(projectName, roles, true, maxPasses, req.Prompt)
	initialState.ExecutionID = executionID
	initialState.ActiveCallOwner = workModeCallOwnerNone
	if hasPreStartState && preStartState.Mode == workModeModeObserverReview && strings.TrimSpace(preStartState.InitialPrompt) == strings.TrimSpace(req.Prompt) {
		if preStartState.Status == workModeStatusPausedAfterCurrent {
			initialState.Status = workModeStatusPausedAfterCurrent
		}
	}
	a.mu.Lock()
	a.setWorkModeSessionStateLocked(projectName, initialState)
	a.mu.Unlock()

	reviewMessages := []workModeReviewMessage{}
	allChanged := []string{}
	allSkipped := []string{}
	allBlocked := []workModeBlockedFileOutput{}
	allDiffs := []workModeDiffFile{}
	allInline := []workModeInlineFileOutput{}
	seenChanged := map[string]bool{}
	latestWorkerReply := ""
	latestWorkerFiles := []string{}
	memoryUpdated := false
	memoryWarning := ""
	agentMessages := []string{}

	appendChanged := func(paths []string) {
		for _, rel := range paths {
			rel = cleanSlashPath(rel)
			if rel == "" || seenChanged[rel] {
				continue
			}
			seenChanged[rel] = true
			allChanged = append(allChanged, rel)
		}
	}
	currentTmpState := func() []string {
		rels, err := workModeListRelFilesUnderRoot(tmpWorkRoot)
		if err != nil {
			a.logf(modelIDString(worker.ID), "warn", "Could not list tmp-work state: %v", err)
			return []string{}
		}
		return workModePrefixedTmpWorkState(rels)
	}
	writeFinal := func(reply, agentMessage string) {
		if strings.TrimSpace(reply) == "" {
			reply = latestWorkerReply
		}
		if strings.TrimSpace(reply) == "" {
			reply = "Observer Review ended before any Worker output was completed. No tmp-work changes were applied."
		}
		if strings.TrimSpace(agentMessage) != "" {
			agentMessages = append(agentMessages, strings.TrimSpace(agentMessage))
		}
		if req.UseMemory && !memoryUpdated && memoryWarning == "" && strings.TrimSpace(latestWorkerReply) != "" {
			memoryWarning = "Memory was on, but the Worker did not return a memory update."
		}
		latestTmpState := currentTmpState()
		a.mu.Lock()
		state := a.updateWorkModeSessionStatusLocked(projectName, workModeStatusFinalized, workModeCallOwnerNone)
		state.LatestWorkerMessage = strings.TrimSpace(latestWorkerReply)
		state.LatestWorkerFileState = latestTmpState
		state.ReviewMessages = cloneWorkModeReviewMessages(reviewMessages)
		state.ExecutionID = executionID
		state = a.setWorkModeSessionStateLocked(projectName, state)
		a.mu.Unlock()
		msg := strings.TrimSpace(strings.Join(agentMessages, "\n"))
		writeJSON(w, http.StatusOK, workModeResponse{Reply: reply, ChangedFiles: allChanged, SkippedFiles: allSkipped, BlockedFiles: allBlocked, InlineFiles: allInline, DiffFiles: allDiffs, AgentMessage: msg, MemoryUpdated: memoryUpdated, MemoryWarning: memoryWarning, State: &state, ReviewMessages: reviewMessages})
	}
	finalFromCancel := func() bool {
		state, ok := a.workModeSessionStateForExecution(projectName, executionID)
		if !ok {
			return false
		}
		if state.Status == workModeStatusFinalized {
			if strings.TrimSpace(latestWorkerReply) == "" {
				writeFinal(latestWorkerReply, "Observer Review was finalized before the first Worker pass completed. No new Worker output was accepted.")
			} else {
				writeFinal(latestWorkerReply, "Observer Review was finalized by the user. Review tmp-work, then merge selected draft files or use Merge all.")
			}
			return true
		}
		return false
	}
	buildSessionMessage := func(owner string, model ModelConfig, userContext []byte) adapters.Message {
		memoryText := strings.TrimSpace(string(chatMemory))
		if req.UseMemory && memoryText == "" {
			memoryText = "(empty)"
		}
		parts := []string{
			"AGENTGO WORK MODE OBSERVER REVIEW SESSION",
			"PROJECT: " + projectName,
			"WORKER: " + worker.Label,
			"OBSERVER: " + observer.Label,
			"CURRENT RECIPIENT: " + owner,
			"MODEL: " + model.Label,
		}
		if includeRoleContext {
			parts = append(parts, "", "MODEL USER CONTEXT (meta/user_context.json):", strings.TrimSpace(string(userContext)))
		} else {
			parts = append(parts, "", "MODEL USER CONTEXT:", "(disabled for this message)")
		}
		if req.UseMemory {
			memoryLabel := "WORK MODE MEMORY (Worker-owned meta/memory.md):"
			if strings.EqualFold(owner, "Observer") {
				memoryLabel = "WORK MODE MEMORY (read-only Worker-owned meta/memory.md; Observer cannot update it):"
			}
			parts = append(parts, "", memoryLabel, memoryText)
		} else {
			parts = append(parts, "", "WORK MODE MEMORY:", "(disabled for this message)")
		}
		return adapters.Message{Role: "user", Text: strings.Join(parts, "\n")}
	}
	buildContextMessages := func(profile adapters.TransportProfile, includeTmpWork bool) ([]adapters.Message, error) {
		messages := []adapters.Message{}
		if len(selectedFiles) > 0 {
			contextMessage, _, err := buildMultimodalContextMessage(projectworkRoot, selectedFiles, "SELECTED WORK MODE PROJECTWORK FILES:", profile, true, builderContextMaxTextBytes)
			if err != nil {
				return nil, err
			}
			messages = appendMessageIfPresent(messages, contextMessage)
		}
		if includeTmpWork {
			tmpRels, err := workModeListRelFilesUnderRoot(tmpWorkRoot)
			if err != nil {
				return nil, err
			}
			tmpMessage, _, err := buildMultimodalContextMessage(tmpWorkRoot, tmpRels, "CURRENT TMP-WORK DRAFT FILES (read-only context; Worker file ops target the intended project paths and AgentGO routes them to tmp-work):", profile, false, builderContextMaxTextBytes)
			if err != nil {
				return nil, err
			}
			messages = appendMessageIfPresent(messages, tmpMessage)
		}
		tempMessage, err := buildTemporaryAttachmentMessage(req.TemporaryAttachments, profile)
		if err != nil {
			return nil, err
		}
		messages = appendMessageIfPresent(messages, tempMessage)
		return messages, nil
	}

	for pass := 1; pass <= maxPasses; pass++ {
		state, clearance, err := a.waitForWorkModeObserverLoopClearance(context.Background(), projectName, executionID)
		if err != nil {
			if clearance == "emergency" || state.Status == workModeStatusEmergencyStopped {
				http.Error(w, "Work Mode request canceled", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if clearance == "finalized" {
			writeFinal(latestWorkerReply, "Observer Review was finalized by the user. Review tmp-work, then merge selected draft files or use Merge all.")
			return
		}
		tmpRelsBefore, err := workModeListRelFilesUnderRoot(tmpWorkRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updateableFiles := workModeUniqueRelPaths(selectedFiles, tmpRelsBefore)
		workerInstructions := workModeObserverReviewWorkerInstructions(worker, includeRoleContext, req.UseMemory, req.ResponseMode, updateableFiles)
		workerMessages := []adapters.Message{buildSessionMessage("Worker", worker, workerUserContext)}
		contextMessages, err := buildContextMessages(workerTransport, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		workerMessages = append(workerMessages, contextMessages...)
		if len(reviewMessages) > 0 {
			workerMessages = appendMessageIfPresent(workerMessages, adapters.Message{Role: "user", Text: workModeReviewTranscript(projectName, req.Prompt, reviewMessages)})
		}
		workerTask := workModeObserverWorkerTask(pass, req.Prompt)
		workerMessages = appendMessageIfPresent(workerMessages, adapters.Message{Role: "user", Text: workerTask})
		workerPayload := adapterRequestPayload{Instructions: strings.TrimSpace(workerInstructions), Messages: workerMessages, ExpectJSON: true, JSONSchema: workModeObserverWorkerJSONSchema()}
		ctx, cancel := context.WithCancel(context.Background())
		workerModelID := modelIDString(worker.ID)
		a.setWorkModeObserverActiveCall(projectName, executionID, workerModelID, workModeCallOwnerWorker, cancel)
		adapterResp, err := a.executeAdapterResponse(ctx, worker, workerPayload)
		a.clearWorkModeObserverActiveCall(projectName, executionID, workerModelID)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && finalFromCancel() {
				return
			}
			if errors.Is(err, context.Canceled) {
				http.Error(w, "Work Mode request canceled", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if finalFromCancel() {
			return
		}
		if err := requireWorkModeJSONBoolField(adapterResp.Text, "review_complete"); err != nil {
			a.logf(workerModelID, "warn", "Observer Review Worker response missing required review_complete boolean: %v", err)
			writeWorkModeJSONError(w, err, adapterResp.Text)
			return
		}
		parsed, err := parseWorkModeAdapterResponse(adapterResp, projectworkRoot, projectName)
		if err != nil {
			a.logf(workerModelID, "warn", "Observer Review Worker response parse failed: %v", err)
			writeWorkModeJSONError(w, err, adapterResp.Text)
			return
		}
		if finalFromCancel() {
			return
		}
		changed, skipped, blocked, diffFiles, err := workModeApplyFileOpsToTmpWork(projectworkRoot, projectName, parsed.Files, parsed.Artifacts, limits)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		inlineFiles := workModeInlineFilesForChanged(projectworkRoot, projectName, changed)
		appendChanged(changed)
		allSkipped = append(allSkipped, skipped...)
		allBlocked = append(allBlocked, blocked...)
		allDiffs = append(allDiffs, diffFiles...)
		allInline = append(allInline, inlineFiles...)
		latestWorkerReply = strings.TrimSpace(parsed.Reply)
		latestWorkerFiles = currentTmpState()
		if req.UseMemory {
			memoryText := strings.TrimSpace(parsed.Memory)
			if memoryText != "" {
				if err := os.WriteFile(memoryPath, []byte(memoryText), 0o644); err != nil {
					memoryWarning = "Could not save memory.md."
					a.logf(workerModelID, "warn", "Could not save Observer Review memory.md: %v", err)
				} else {
					chatMemory = []byte(memoryText)
					memoryUpdated = true
				}
			} else if memoryWarning == "" {
				memoryWarning = "Memory is on, but the Worker did not return a memory update for this pass. Previous memory was preserved."
			}
		}
		if warning := workModeOutputLimitWarning(worker, adapterResp); warning != "" {
			agentMessages = append(agentMessages, warning)
		}
		for _, warning := range parsed.Warnings {
			warning = strings.TrimSpace(warning)
			if warning != "" {
				a.logf(workerModelID, "warn", "Observer Review Worker warning: %s", warning)
			}
		}
		reviewMessages = append(reviewMessages, workModeReviewMessage{Owner: workModeCallOwnerWorker, Pass: pass, Reply: latestWorkerReply, ChangedFiles: changed, SkippedFiles: skipped, BlockedFiles: blocked, InlineFiles: inlineFiles})
		a.mu.Lock()
		state = a.updateWorkModeObserverReviewProgressLocked(projectName, executionID, func(s *workModeSessionState) {
			s.CurrentPass = pass
			s.LatestWorkerMessage = latestWorkerReply
			s.LatestWorkerFileState = latestWorkerFiles
			s.ReviewMessages = cloneWorkModeReviewMessages(reviewMessages)
		})
		a.mu.Unlock()
		if parsed.ReviewComplete {
			writeFinal(latestWorkerReply, "Worker marked the Observer Review run complete. Review tmp-work, then merge selected draft files or use Merge all.")
			return
		}
		if pass >= maxPasses {
			writeFinal(latestWorkerReply, fmt.Sprintf("Observer Review reached the hard limit of %d Worker pass(es). AgentGO stopped before calling the Observer again. Review tmp-work, then merge selected draft files or use Merge all.", maxPasses))
			return
		}

		state, clearance, err = a.waitForWorkModeObserverLoopClearance(context.Background(), projectName, executionID)
		if err != nil {
			if clearance == "emergency" || state.Status == workModeStatusEmergencyStopped {
				http.Error(w, "Work Mode request canceled", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if clearance == "finalized" {
			writeFinal(latestWorkerReply, "Observer Review was finalized by the user. Review tmp-work, then merge selected draft files or use Merge all.")
			return
		}
		observerInstructions := workModeObserverInstructions(observer)
		observerMessages := []adapters.Message{buildSessionMessage("Observer", observer, observerUserContext)}
		observerContextMessages, err := buildContextMessages(observerTransport, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		observerMessages = append(observerMessages, observerContextMessages...)
		observerMessages = appendMessageIfPresent(observerMessages, adapters.Message{Role: "user", Text: workModeReviewTranscript(projectName, req.Prompt, reviewMessages)})
		observerMessages = appendMessageIfPresent(observerMessages, adapters.Message{Role: "user", Text: workModeObserverTask(pass)})
		observerPayload := adapterRequestPayload{Instructions: strings.TrimSpace(observerInstructions), Messages: observerMessages, ExpectJSON: true, JSONSchema: workModeObserverJSONSchema()}
		obsCtx, obsCancel := context.WithCancel(context.Background())
		observerModelID := modelIDString(observer.ID)
		a.setWorkModeObserverActiveCall(projectName, executionID, observerModelID, workModeCallOwnerObserver, obsCancel)
		observerResp, err := a.executeAdapterResponse(obsCtx, observer, observerPayload)
		a.clearWorkModeObserverActiveCall(projectName, executionID, observerModelID)
		obsCancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && finalFromCancel() {
				return
			}
			if errors.Is(err, context.Canceled) {
				http.Error(w, "Work Mode request canceled", http.StatusGatewayTimeout)
				return
			}
			note := "Observer failed, so AgentGO finalized the latest completed Worker pass. Review tmp-work, then merge selected draft files or use Merge all."
			reviewMessages = append(reviewMessages, workModeReviewMessage{Owner: workModeCallOwnerObserver, Pass: pass, Reply: "Observer failed: " + err.Error(), HasInput: workModeMaybeJSONBoolPtr(false)})
			writeFinal(latestWorkerReply, note)
			return
		}
		if finalFromCancel() {
			return
		}
		observerParsed, err := parseWorkModeObserverResponse(observerResp.Text)
		if err != nil {
			note := "Observer returned invalid review JSON, so AgentGO finalized the latest completed Worker pass. Review tmp-work, then merge selected draft files or use Merge all."
			a.logf(observerModelID, "warn", "Observer Review Observer response parse failed: %v", err)
			reviewMessages = append(reviewMessages, workModeReviewMessage{Owner: workModeCallOwnerObserver, Pass: pass, Reply: "Observer JSON parse failed: " + err.Error(), HasInput: workModeMaybeJSONBoolPtr(false)})
			writeFinal(latestWorkerReply, note)
			return
		}
		for _, warning := range observerParsed.Warnings {
			warning = strings.TrimSpace(warning)
			if warning != "" {
				a.logf(observerModelID, "warn", "Observer Review Observer warning: %s", warning)
			}
		}
		if warning := workModeOutputLimitWarning(observer, observerResp); warning != "" {
			agentMessages = append(agentMessages, warning)
		}
		reviewMessages = append(reviewMessages, workModeReviewMessage{Owner: workModeCallOwnerObserver, Pass: pass, Reply: strings.TrimSpace(observerParsed.Reply), HasInput: workModeMaybeJSONBoolPtr(observerParsed.HasInput), Recommendations: observerParsed.Recommendations})
		a.mu.Lock()
		state = a.updateWorkModeObserverReviewProgressLocked(projectName, executionID, func(s *workModeSessionState) {
			s.LatestObserverMessage = strings.TrimSpace(observerParsed.Reply)
			s.ObserverHasInput = workModeMaybeJSONBoolPtr(observerParsed.HasInput)
			s.ReviewMessages = cloneWorkModeReviewMessages(reviewMessages)
		})
		a.mu.Unlock()
		if !observerParsed.HasInput {
			writeFinal(latestWorkerReply, "Observer marked has_input=false. AgentGO finalized the latest Worker pass. Review tmp-work, then merge selected draft files or use Merge all.")
			return
		}
	}
	writeFinal(latestWorkerReply, "Observer Review ended. Review tmp-work, then merge selected draft files or use Merge all.")
}

func (a *App) handleWorkModeSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ModelID = strings.TrimSpace(req.ModelID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ResponseMode = normalizeChatResponseMode(req.ResponseMode)
	if req.Prompt == "" {
		http.Error(w, "Enter a Work Mode prompt first.", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if req.ObserverReview {
		a.handleWorkModeObserverReviewSend(w, req, projectName)
		return
	}
	roles, status, roleErr := a.resolveWorkModeRoles(req.ModelID, "", false)
	if roleErr != nil {
		http.Error(w, roleErr.Error(), status)
		return
	}
	model := roles.Worker
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	selectedFiles, selectedSet, err := normalizeWorkModeSelectedFiles(req.SelectedFiles, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	includeRoleContext := true
	if req.IncludeRoleContext != nil {
		includeRoleContext = *req.IncludeRoleContext
	}
	userContext := []byte("{}")
	if includeRoleContext {
		userContext, _ = os.ReadFile(filepath.Join(metaRoot, "user_context.json"))
	}
	memoryPath := filepath.Join(metaRoot, chatMemoryFileName)
	chatMemory := []byte("")
	if req.UseMemory {
		chatMemory, _ = os.ReadFile(memoryPath)
	}
	transportProfile := adapters.ResolveTransportProfile(toAdapterModelConfig(model))
	extraMessages := []adapters.Message{}
	if len(selectedFiles) > 0 {
		contextMessage, _, err := buildMultimodalContextMessage(projectworkRoot, selectedFiles, "SELECTED WORK MODE PROJECTWORK FILES:", transportProfile, true, builderContextMaxTextBytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		extraMessages = appendMessageIfPresent(extraMessages, contextMessage)
	}
	tempMessage, err := buildTemporaryAttachmentMessage(req.TemporaryAttachments, transportProfile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	extraMessages = appendMessageIfPresent(extraMessages, tempMessage)
	inputParts := []string{
		"AGENTGO WORK MODE SESSION",
		"PROJECT: " + projectName,
		"MODEL: " + model.Label,
	}
	if includeRoleContext {
		inputParts = append(inputParts, "", "MODEL USER CONTEXT (meta/user_context.json):", strings.TrimSpace(string(userContext)))
	} else {
		inputParts = append(inputParts, "", "MODEL USER CONTEXT:", "(disabled for this message)")
	}
	if req.UseMemory {
		memoryText := strings.TrimSpace(string(chatMemory))
		if memoryText == "" {
			memoryText = "(empty)"
		}
		inputParts = append(inputParts, "", "WORK MODE MEMORY (meta/memory.md):", memoryText)
	} else {
		inputParts = append(inputParts, "", "WORK MODE MEMORY:", "(disabled for this message)")
	}
	sessionMessage := adapters.Message{Role: "user", Text: strings.Join(inputParts, "\n")}
	finalUserMessage := adapters.Message{Role: "user", Text: "FINAL USER REQUEST:\n" + req.Prompt}
	instructions := buildWorkModeInstructions(model, includeRoleContext, req.UseMemory, req.ResponseMode, selectedFiles)
	requestPayload := adapterRequestPayload{
		Instructions: strings.TrimSpace(instructions),
		Messages:     []adapters.Message{},
		ExpectJSON:   true,
		JSONSchema:   workModeJSONSchema(),
	}
	requestPayload.Messages = appendMessageIfPresent(requestPayload.Messages, sessionMessage)
	for _, message := range extraMessages {
		requestPayload.Messages = appendMessageIfPresent(requestPayload.Messages, message)
	}
	requestPayload.Messages = appendMessageIfPresent(requestPayload.Messages, finalUserMessage)
	ctx, cancel := context.WithCancel(context.Background())
	executionID := "workmode-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	a.mu.Lock()
	a.setActiveCancelLocked(modelIDString(model.ID), projectName, executionID, cancel)
	normalState := newWorkModeSessionState(projectName, roles, false, 3, req.Prompt)
	normalState.ExecutionID = executionID
	normalState.ActiveCallOwner = workModeCallOwnerWorker
	a.setWorkModeSessionStateLocked(projectName, normalState)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(modelIDString(model.ID), executionID)
		a.mu.Unlock()
		cancel()
	}()
	adapterResp, err := a.executeAdapterResponse(ctx, model, requestPayload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			a.mu.Lock()
			a.updateWorkModeSessionStatusLocked(projectName, workModeStatusEmergencyStopped, workModeCallOwnerNone)
			a.mu.Unlock()
			http.Error(w, "Work Mode request canceled", http.StatusGatewayTimeout)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	parsed, err := parseWorkModeAdapterResponse(adapterResp, projectworkRoot, projectName)
	if err != nil {
		a.logf(modelIDString(model.ID), "warn", "Work Mode response parse failed: %v", err)
		writeWorkModeJSONError(w, err, adapterResp.Text)
		return
	}
	limits, err := a.loadProjectLimits(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	changed, skipped, blocked, diffFiles, err := workModeApplyFileOps(projectworkRoot, projectName, parsed.Files, parsed.Artifacts, limits, selectedSet)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(changed) > 0 {
		if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
			a.logf(modelIDString(model.ID), "warn", "Could not sync Work Mode projectwork changes into active Builder workspace: %v", err)
		}
	}
	memoryUpdated := false
	memoryWarning := ""
	if req.UseMemory {
		memoryText := strings.TrimSpace(parsed.Memory)
		if memoryText != "" {
			if err := os.WriteFile(memoryPath, []byte(memoryText), 0o644); err != nil {
				memoryWarning = "Could not save memory.md."
				a.logf(modelIDString(model.ID), "warn", "Could not save Work Mode memory.md: %v", err)
			} else {
				memoryUpdated = true
			}
		} else {
			memoryWarning = "Memory was on, but the AI did not return a memory update."
		}
	}
	if len(parsed.Warnings) > 0 {
		for _, warning := range parsed.Warnings {
			warning = strings.TrimSpace(warning)
			if warning != "" {
				a.logf(modelIDString(model.ID), "warn", "Work Mode warning: %s", warning)
			}
		}
	}
	inlineFiles := workModeInlineFilesForChanged(projectworkRoot, projectName, changed)
	agentMessage := workModeOutputLimitWarning(model, adapterResp)
	a.mu.Lock()
	completedState := a.updateWorkModeSessionStatusLocked(projectName, workModeStatusFinalized, workModeCallOwnerNone)
	completedState.LatestWorkerMessage = strings.TrimSpace(parsed.Reply)
	completedState.LatestWorkerFileState = append([]string{}, changed...)
	completedState.CurrentPass = 1
	completedState.ExecutionID = executionID
	completedState = a.setWorkModeSessionStateLocked(projectName, completedState)
	a.mu.Unlock()
	a.logf(modelIDString(model.ID), "info", "Work Mode prompt completed for project %s; changed=%d skipped=%d", projectName, len(changed), len(skipped))
	writeJSON(w, http.StatusOK, workModeResponse{Reply: parsed.Reply, ChangedFiles: changed, SkippedFiles: skipped, BlockedFiles: blocked, InlineFiles: inlineFiles, DiffFiles: diffFiles, AgentMessage: agentMessage, MemoryUpdated: memoryUpdated, MemoryWarning: memoryWarning, State: &completedState})
}

func writeWorkModeJSONError(w http.ResponseWriter, parseErr error, rawResponse string) {
	message := "AgentGO could not read the AI's Work Mode JSON response. Expand details to inspect the parse error and raw AI response."
	parseText := "unknown JSON parse error"
	if parseErr != nil {
		parseText = parseErr.Error()
	}
	writeJSON(w, http.StatusBadGateway, workModeJSONErrorResponse{
		Error:       "work_mode_json_parse_failed",
		Message:     message,
		ParseError:  parseText,
		RawResponse: rawResponse,
	})
}

func (a *App) savePromptHelperRawResponse(model ModelConfig, projectName, responseText string) string {
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return ""
	}
	rawDir := promptHelperResponsesDir(metaRoot)
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return ""
	}
	timestamp := time.Now().Format("20060102_150405")
	rawFile := filepath.Join(rawDir, "prompt_helper_response_"+timestamp+".md")
	if err := os.WriteFile(rawFile, []byte(strings.TrimSpace(responseText)), 0o644); err != nil {
		return ""
	}
	if err := pruneResponseArchive(rawDir, a.cfg.MaxResponseHistory, "prompt_helper_response_", ".md", nil); err != nil {
		a.logf(modelIDString(model.ID), "warn", "Failed trimming Prompt Helper response history: %v", err)
	}
	return rawFile
}

func (a *App) logPromptHelperFailure(model ModelConfig, projectName, responseText string, err error) {
	if err != nil {
		a.logf(modelIDString(model.ID), "warn", "Prompt Helper failed for project %s: %v", projectName, err)
	}
	rawFile := a.savePromptHelperRawResponse(model, projectName, responseText)
	if rawFile != "" {
		a.logf(modelIDString(model.ID), "warn", "Prompt Helper raw response saved at %s", filepath.ToSlash(rawFile))
	}
	if strings.TrimSpace(responseText) != "" {
		a.logf(modelIDString(model.ID), "warn", "Prompt Helper response preview: %s", previewForLog(responseText, 500))
	}
}

func (a *App) handlePromptHelper(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req promptHelperRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ModelID = strings.TrimSpace(req.ModelID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.ModelID == "" || req.Prompt == "" {
		http.Error(w, "modelId and prompt are required", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	a.mu.RLock()
	enabled := a.toggles[req.ModelID]
	reviewer := a.reviewerID == req.ModelID
	a.mu.RUnlock()
	if !enabled && !reviewer {
		http.Error(w, "Activate this model or enable observer mode first.", http.StatusBadRequest)
		return
	}
	instructions, err := loadPromptHelperSystemPrompt(a.cfg, model.PromptMode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instructions = appendModelUggProtocol(instructions, model)
	inputParts := []string{
		"USER DRAFT PROMPT:",
		req.Prompt,
	}
	input := strings.Join(inputParts, "\n")
	responseText, err := a.callStructuredTextModel(context.Background(), model, instructions, input, false, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	responseText = strings.TrimSpace(responseText)
	clean := sanitizeModelJSONText(responseText)
	if clean == "" {
		err = errors.New("Prompt Helper returned an empty response.")
		a.logPromptHelperFailure(model, projectName, responseText, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var parsed promptHelperResponse
	if json.Valid([]byte(clean)) {
		parsed, err = decodePromptHelperResponse(clean)
		if err != nil {
			a.logPromptHelperFailure(model, projectName, responseText, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else if jsonText, _, _, ok := extractJSONObjectFromText(clean); ok && json.Valid([]byte(jsonText)) {
		parsed, err = decodePromptHelperResponse(jsonText)
		if err != nil {
			a.logPromptHelperFailure(model, projectName, responseText, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		parsed = promptHelperResponse{RecommendedPrompt: clean}
	}
	parsed.RecommendedPrompt = strings.TrimSpace(parsed.RecommendedPrompt)
	parsed.WhySafer = strings.TrimSpace(parsed.WhySafer)
	parsed.Tip = strings.TrimSpace(parsed.Tip)
	if parsed.RecommendedPrompt == "" {
		http.Error(w, "Prompt Helper returned no recommended_prompt.", http.StatusBadGateway)
		return
	}
	if parsed.WhySafer == "" {
		parsed.WhySafer = "This rewrite keeps the task focused, preserves key constraints, and avoids AgentGO prompt wording that can conflict with downstream execution."
	}
	a.logf(modelIDString(model.ID), "info", "Prompt Helper used for project %s", projectName)
	writeJSON(w, http.StatusOK, parsed)
}

func (a *App) handleRoleIdeas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req roleIdeasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ModelID = strings.TrimSpace(req.ModelID)
	if req.ModelID == "" {
		http.Error(w, "modelId is required", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	a.mu.RLock()
	enabled := a.toggles[req.ModelID]
	reviewer := a.reviewerID == req.ModelID
	a.mu.RUnlock()
	if !enabled && !reviewer {
		http.Error(w, "Activate this model or enable reviewer mode first.", http.StatusBadRequest)
		return
	}

	payload := map[string]any{
		"agentgo": map[string]any{
			"product": "AgentGo",
			"version": a.release.Label(),
			"feature": "ai_role_ideas",
		},
		"request": map[string]any{
			"project_name":        projectName,
			"project_description": strings.TrimSpace(req.ProjectDescription),
			"help_needed":         strings.TrimSpace(req.HelpNeeded),
			"thinking_style":      strings.TrimSpace(req.ThinkingStyle),
			"extra_note":          strings.TrimSpace(req.ExtraNote),
		},
		"response_requirements": map[string]any{
			"format":                   "json",
			"return_top_n":             10,
			"keys_required_per_role":   []string{"title", "purpose", "why_useful", "when_to_choose", "thinking_type", "behavior_suggestions"},
			"behavior_suggestion_keys": []string{"tone", "verbosity", "creativity"},
		},
	}
	payloadBytes, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instructions := strings.TrimSpace(`You are the AgentGO Role Generator.

Your job is to generate exactly 10 distinct AI role profiles based on the user's request.

Return raw, valid JSON only.
Do not use markdown.
Do not wrap the JSON in code fences.
Do not add any text before or after the JSON.
The first character of your output must be { and the last character must be }.

Output exactly this JSON structure:

{
  "agentgo_response": {
    "feature": "ai_role_ideas",
    "status": "ok"
  },
  "ai_roles": [
    {
      "title": "...",
      "purpose": "...",
      "why_useful": "...",
      "when_to_choose": "...",
      "thinking_type": "...",
      "behavior_suggestions": {
        "tone": "...",
        "verbosity": "...",
        "creativity": "..."
      }
    }
  ]
}

Requirements:
- Return exactly 10 objects in "ai_roles".
- Make all 10 roles meaningfully distinct in purpose, style, or problem-solving approach.
- Avoid duplicate or near-duplicate roles.
- Keep all strings short, clear, specific, and useful.
- Base the roles on the user's request. If the request is broad, offer a balanced spread of useful role options. If the request is narrow, still return 10 roles by varying specialty, emphasis, or working style.
- Do not include fields other than those shown above.
- Do not leave placeholder text like "...".

Allowed values:
- "thinking_type" must be exactly one of:
  critical, analytical, practical, creative, exploratory, balanced

- "behavior_suggestions.tone" must be exactly one of:
  professional, direct, concise, friendly, teaching

- "behavior_suggestions.verbosity" must be exactly one of:
  low, medium, high

- "behavior_suggestions.creativity" must be exactly one of:
  low, medium, high

If the user's request is unclear, still return valid JSON in the same structure and generate 10 broadly useful role options.`)
	responseText, err := a.callStructuredTextModel(context.Background(), model, instructions, string(payloadBytes), true, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	clean := sanitizeModelJSONText(responseText)
	var parsed roleIdeasResult
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		a.logf(modelIDString(model.ID), "warn", "Role idea generation returned invalid JSON for project %s: %v", projectName, err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"code":    "BadJSON",
			"message": "The AI returned invalid role data. Please try generating roles again.",
		})
		return
	}
	normalizeRoleIdeasResult(&parsed)
	if len(parsed.AIRoles) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"code":    "NoRoles",
			"message": "The AI returned no role ideas. Please try generating roles again.",
		})
		return
	}
	a.logf(modelIDString(model.ID), "info", "Generated %d AI role ideas for project %s", len(parsed.AIRoles), projectName)
	writeJSON(w, http.StatusOK, parsed)
}

func (a *App) handleRiskMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		resp := a.riskStateSnapshotLocked()
		a.mu.RUnlock()
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		var req struct {
			Enabled      bool     `json:"enabled"`
			Iterations   int      `json:"iterations"`
			Prompt       string   `json:"prompt"`
			ContextFiles []string `json:"contextFiles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if req.Enabled {
			if err := a.requireActiveProjectForSession(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		maxIterations := a.cfg.RiskModeMaxIterations
		a.mu.Lock()
		if req.Enabled {
			if req.Iterations < 1 || req.Iterations > maxIterations {
				a.mu.Unlock()
				http.Error(w, fmt.Sprintf("iterations must be 1-%d", maxIterations), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(a.reviewerID) == "" {
				a.mu.Unlock()
				http.Error(w, "Enable Observer Mode before starting Risk Mode.", http.StatusBadRequest)
				return
			}
			activeBuilders := 0
			for _, model := range a.cfg.Models {
				modelID := modelIDString(model.ID)
				if modelID == a.reviewerID {
					continue
				}
				if a.toggles[modelID] {
					activeBuilders++
				}
			}
			if activeBuilders < 1 {
				a.mu.Unlock()
				http.Error(w, "Activate at least one Builder AI before starting Risk Mode.", http.StatusBadRequest)
				return
			}
			a.riskModeEnabled = true
			a.riskIterationsTotal = req.Iterations
			a.riskIterationsRemain = req.Iterations
			a.riskOriginalPrompt = strings.TrimSpace(req.Prompt)
			a.riskContextFiles = normalizeRelativePaths(req.ContextFiles)
			a.riskBuilderIDs = nil
			a.riskCurrentIteration = 1
			a.riskStopReason = ""
			a.setRiskStatusLocked("RISK MODE",
				fmt.Sprintf("Iteration 1 / %d", a.riskIterationsTotal),
				"Waiting for Execute Prompt to complete.",
			)
		} else {
			a.clearRiskModeLocked()
		}
		resp := a.riskStateSnapshotLocked()
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project before executing a prompt.", http.StatusBadRequest)
		return
	}
	resp, execErr, status := a.startExecutionForCurrentConfig(projectName, req, executionSourceInfo{TriggerType: "manual"})
	if execErr != nil {
		http.Error(w, execErr.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) runExecuteRound(projectName, executionID, prompt string, contextFiles []string, temporaryAttachments []temporaryAttachmentInput, mediaInputRoles map[string]string, builders []ModelConfig, waveNumber int, wireTapEnabled bool) {
	var wg sync.WaitGroup
	resultsCh := make(chan modelRunResult, len(builders))
	for _, model := range builders {
		wg.Add(1)
		go func(m ModelConfig) {
			defer wg.Done()
			resultsCh <- a.runModelRequest(m, projectName, executionID, prompt, contextFiles, temporaryAttachments, mediaInputRoles, wireTapEnabled)
		}(model)
	}
	wg.Wait()
	close(resultsCh)

	results := make([]modelRunResult, 0, len(builders))
	if !a.isWaveExecutionCurrent(projectName, executionID) {
		a.logf("system", "warn", "Ignoring stale completion for project %s wave %d", projectName, waveNumber)
		return
	}
	mergeReadyCount := 0
	mediaSuccessCount := 0
	mergeReadyBuilderIDs := []string{}
	for result := range resultsCh {
		results = append(results, result)
		if result.Valid {
			mediaSuccessCount++
		}
		if result.PendingCount > 0 {
			mergeReadyCount++
			mergeReadyBuilderIDs = append(mergeReadyBuilderIDs, result.ModelID)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ModelLabel < results[j].ModelLabel })
	a.logBuilderResults(results)
	a.logf("system", "info", "Wave %d completed for project %s. builders=%d merge_ready=%d", waveNumber, projectName, len(builders), mergeReadyCount)

	if waveIncludesVideoGeneration(builders) || waveIncludesMeshGeneration(builders) {
		if mediaSuccessCount <= 0 {
			execState, _ := a.currentWaveExecution(projectName)
			a.mu.Lock()
			a.clearWaveExecutionLocked(projectName)
			a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, execState, waveNumber, "error", withWaveProgress("Media Generation Failed", execState.CurrentIndex, len(execState.Waves)), "", 0))
			a.mu.Unlock()
			a.finalizeActiveOutfitRunFailed(projectName, fmt.Sprintf("Wave %d returned no completed media job.", waveNumber))
			return
		}
		a.continueMediaGenerationAfterWave(projectName, waveNumber)
		return
	}

	a.mu.RLock()
	riskEnabled := a.riskModeEnabled
	currentIteration := a.riskCurrentIteration
	totalIterations := a.riskIterationsTotal
	a.mu.RUnlock()

	if mergeReadyCount <= 0 {
		execState, _ := a.currentWaveExecution(projectName)
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, execState, waveNumber, "error", withWaveProgress("No Merge Output", execState.CurrentIndex, len(execState.Waves)), "", 0))
		a.mu.Unlock()
		if resetCount, err := a.resetProjectAIContextsForWorkflowEnd(projectName); err != nil {
			a.logf("system", "warn", "Failed clearing ai_context.json/reviewer_context.json after no-merge workflow end: %v", err)
		} else {
			a.logf("system", "info", "Cleared ai_context.json and reviewer_context.json for %d model(s) after wave %d ended without merge-ready output", resetCount, waveNumber)
		}
		a.finalizeActiveOutfitRunFailed(projectName, fmt.Sprintf("Wave %d returned no mergeable Builder results.", waveNumber))
		if riskEnabled {
			reason := fmt.Sprintf("Wave %d returned no mergeable Builder results.", waveNumber)
			a.logRiskf("system", "warn", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
			a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), "Review Builder outputs manually before trying again.")
		}
		return
	}

	isFinalWave := a.isFinalWave(projectName)
	externalOutfitAutoMerge := a.activeExternalOutfitRunShouldAutoMerge(projectName)
	autoMergeSingle := (autoMergeSingleBuilderWavesEnabled(a.cfg) || externalOutfitAutoMerge) && len(builders) == 1 && mergeReadyCount == 1 && !(riskEnabled && isFinalWave)
	if autoMergeSingle {
		a.autoMergeSingleBuilderWave(projectName, mergeReadyBuilderIDs[0], waveNumber, riskEnabled)
		return
	}

	a.markWaveAwaitingMerge(projectName, waveNumber)

	reviewerID := a.getReviewerID()
	if reviewerID == "" {
		if riskEnabled {
			reason := "Observer mode is required for Risk Mode."
			a.logRiskf("system", "error", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
			a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), "Enable Observer Mode and try again.")
		}
		return
	}

	if mergeReadyCount == 1 {
		var sole modelRunResult
		failedCount := 0
		for _, result := range results {
			if result.PendingCount > 0 {
				sole = result
			} else {
				failedCount++
			}
		}
		recommended, fallbackErr := a.writeSingleCandidateObserverFallback(projectName, sole, failedCount)
		if fallbackErr != nil {
			a.logf(reviewerID, "warn", "Observer fallback failed after wave %d: %v", waveNumber, fallbackErr)
		} else {
			a.logf(reviewerID, "info", "Observer fallback preserved single merge-ready candidate %s after wave %d", recommended, waveNumber)
			if riskEnabled {
				a.logRiskf("system", "warn", "RISK %d/%d: Observer fallback preserved single merge-ready candidate %s after wave %d", currentIteration, totalIterations, recommended, waveNumber)
				a.handleRiskContinuation(projectName, recommended)
			}
			return
		}
	}

	if execState, ok := a.currentWaveExecution(projectName); ok {
		reviewState := "reviewing"
		reviewDetail := withWaveProgress("Observer Reviewing", execState.CurrentIndex, len(execState.Waves))
		if riskEnabled {
			reviewState = "risk"
			reviewDetail = withWaveProgress("Risk Review", execState.CurrentIndex, len(execState.Waves))
		}
		a.setWaveStatus(projectName, waveStatusFromExecution(projectName, execState, waveNumber, reviewState, reviewDetail, "", 0))
	}

	recommended, err := a.runReviewerEvaluation(projectName, prompt, contextFiles, builders, results)
	if err != nil {
		if execState, ok := a.currentWaveExecution(projectName); ok {
			a.mu.Lock()
			a.clearWaveExecutionLocked(projectName)
			a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, execState, waveNumber, "error", withWaveProgress("Observer Failed", execState.CurrentIndex, len(execState.Waves)), execState.CurrentPromptSource, execState.CurrentContextFilesUsed))
			a.mu.Unlock()
		}
		a.logf(reviewerID, "error", "Reviewer evaluation failed after wave %d: %v", waveNumber, err)
		if riskEnabled {
			reason := fmt.Sprintf("Observer evaluation failed after wave %d.", waveNumber)
			a.logRiskf("system", "error", "RISK %d/%d: %s %v", currentIteration, totalIterations, reason, err)
			a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), err.Error())
		}
		return
	}
	if strings.TrimSpace(recommended) == "" && riskEnabled && len(mergeReadyBuilderIDs) == 1 {
		recommended = mergeReadyBuilderIDs[0]
	}
	if strings.TrimSpace(recommended) == "" {
		if riskEnabled {
			reason := fmt.Sprintf("Observer did not recommend a merge candidate after wave %d.", waveNumber)
			a.logRiskf("system", "warn", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
			a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), "Risk Mode returned to normal mode.")
		}
		return
	}
	a.logf(reviewerID, "info", "Reviewer recommended %s after wave %d", recommended, waveNumber)
	if riskEnabled {
		a.logRiskf("system", "warn", "RISK %d/%d: Observer recommended merge candidate %s after wave %d", currentIteration, totalIterations, recommended, waveNumber)
		a.handleRiskContinuation(projectName, recommended)
		return
	}
	a.markWaveAwaitingMerge(projectName, waveNumber)
}
func (a *App) continueMediaGenerationAfterWave(projectName string, waveNumber int) {
	execState, ok := a.currentWaveExecution(projectName)
	if !ok {
		return
	}
	nextWave, started, err := a.continueWaveExecutionAfterMerge(projectName)
	if err != nil {
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, execState, waveNumber, "error", withWaveProgress("Media Wave Failed", execState.CurrentIndex, len(execState.Waves)), execState.CurrentPromptSource, execState.CurrentContextFilesUsed))
		a.mu.Unlock()
		a.finalizeActiveOutfitRunFailed(projectName, fmt.Sprintf("Wave %d completed, but the next media wave could not start.", waveNumber))
		a.logf("system", "error", "Media wave %d completed for project %s, but continuation failed: %v", waveNumber, projectName, err)
		return
	}
	if started {
		a.logf("system", "info", "Media wave %d completed for project %s; launching wave %d.", waveNumber, projectName, nextWave.Number)
		return
	}
	a.mu.Lock()
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, execState, waveNumber, "complete", withWaveProgress("Media Complete", execState.CurrentIndex, len(execState.Waves)), execState.CurrentPromptSource, execState.CurrentContextFilesUsed))
	a.mu.Unlock()
	a.finalizeActiveOutfitRunCompleted(projectName, "", "", "Completed media generation workflow.")
	a.logf("system", "info", "Media generation workflow complete for project %s after wave %d.", projectName, waveNumber)
}

func (a *App) logBuilderResults(results []modelRunResult) {
	if len(results) == 0 {
		return
	}
	lines := []string{"BUILDER RESULTS:"}
	for _, result := range results {
		if result.Valid {
			lines = append(lines, fmt.Sprintf("- %s: Valid", result.ModelLabel))
		} else {
			reason := "unknown error"
			if result.Err != nil {
				reason = result.Err.Error()
			}
			lines = append(lines, fmt.Sprintf("- %s: Failed (%s)", result.ModelLabel, reason))
		}
	}
	a.logf("system", "info", "%s", strings.Join(lines, "\n"))
}

func (a *App) getReviewerID() string { a.mu.RLock(); defer a.mu.RUnlock(); return a.reviewerID }

func buildTextPart(text string) adapters.Part {
	return adapters.Part{Kind: "text", Text: strings.TrimSpace(text)}
}

func buildTextMessage(role, text string) adapters.Message {
	text = strings.TrimSpace(text)
	if text == "" {
		return adapters.Message{Role: role}
	}
	return adapters.Message{Role: role, Parts: []adapters.Part{buildTextPart(text)}}
}

func supportsNativeInputImage(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func supportedOpenAIFileExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".pdf", ".doc", ".docx", ".rtf", ".odt", ".ppt", ".pptx", ".xls", ".xlsx", ".csv", ".tsv":
		return true
	default:
		return false
	}
}

func supportedOpenAIFileMIME(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "application/pdf",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/rtf",
		"text/rtf",
		"application/vnd.oasis.opendocument.text",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"text/csv",
		"application/csv",
		"text/tsv":
		return true
	default:
		return false
	}
}

func supportsNativeInputAudio(adapterName, contentType, relPath string) bool {
	switch strings.TrimSpace(adapterName) {
	case "gemini_generate_content", "custom_json", "custom_multipart":
	default:
		return false
	}
	cleanType := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(cleanType, "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".mp3", ".wav", ".m4a", ".aac", ".flac", ".ogg", ".oga", ".webm":
		return true
	default:
		return false
	}
}

func supportsNativeInputVideo(adapterName, contentType, relPath string) bool {
	switch strings.TrimSpace(adapterName) {
	case "gemini_generate_content", "custom_json", "custom_multipart":
	default:
		return false
	}
	cleanType := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(cleanType, "video/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".mp4", ".mov", ".m4v", ".mpeg", ".mpg", ".webm", ".avi":
		return true
	default:
		return false
	}
}

func supportsNativeInputFile(adapterName, contentType, relPath string) bool {
	cleanType := strings.ToLower(strings.TrimSpace(contentType))
	cleanAdapter := strings.TrimSpace(adapterName)
	switch cleanAdapter {
	case "openai_responses":
		if supportedOpenAIFileMIME(cleanType) {
			return true
		}
		return supportedOpenAIFileExtension(filepath.Ext(relPath))
	case "gemini_generate_content":
		if cleanType == "application/pdf" || supportedOpenAIFileMIME(cleanType) {
			return true
		}
		return supportedOpenAIFileExtension(filepath.Ext(relPath))
	case "anthropic_messages":
		return cleanType == "application/pdf" || strings.EqualFold(filepath.Ext(relPath), ".pdf")
	case "custom_json", "custom_multipart":
		return true
	default:
		return false
	}
}

func classifyBinaryInputKind(contentType string) string {
	clean := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(clean, "image/"):
		return "image"
	case strings.HasPrefix(clean, "audio/"):
		return "audio"
	case strings.HasPrefix(clean, "video/"):
		return "video"
	default:
		return "file"
	}
}

func formatCapabilityKinds(caps adapters.ModelCapabilities) string {
	kinds := []string{}
	if caps.SupportsTextIn {
		kinds = append(kinds, "text")
	}
	if caps.SupportsImageIn {
		kinds = append(kinds, "image")
	}
	if caps.SupportsAudioIn {
		kinds = append(kinds, "audio")
	}
	if caps.SupportsVideoIn {
		kinds = append(kinds, "video")
	}
	if caps.SupportsFileIn {
		kinds = append(kinds, "file")
	}
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, ", ")
}

func formatMultimodalReport(report multimodalAssemblyReport) string {
	parts := []string{fmt.Sprintf("Sent %d text file(s), %d native image file(s), %d native audio file(s), %d native video file(s), %d native document/file input(s), %d manifest-only file note(s)", report.TextFiles, report.ImageFiles, report.AudioFiles, report.VideoFiles, report.NativeFileFiles, report.ManifestOnlyFiles)}
	if report.SkippedFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) skipped by size/budget limits", report.SkippedFiles))
	}
	parts = append(parts,
		fmt.Sprintf("capability mode=%s", report.Profile.CapabilityMode),
		"effective inputs="+formatCapabilityKinds(report.Profile.EffectiveCapabilities),
	)
	return strings.Join(parts, "; ")
}

func buildMultimodalContextMessage(root string, relPaths []string, heading string, profile adapters.TransportProfile, editable bool, maxTextBytes int) (adapters.Message, multimodalAssemblyReport, error) {
	report := multimodalAssemblyReport{UsedPaths: []string{}, Profile: profile}
	message := adapters.Message{Role: "user", Parts: []adapters.Part{}}
	selected := normalizeRelativePaths(relPaths)
	manifest := make([]requestManifestEntry, 0, len(selected))
	textRemaining := maxTextBytes
	imageBytesTotal := 0
	imageCount := 0
	nativeBinaryBytesTotal := 0
	nativeBinaryCount := 0

	heading = strings.TrimSpace(heading)
	if heading == "" {
		heading = "FILE CONTEXT"
	}
	message.Parts = append(message.Parts, buildTextPart(heading))
	if len(selected) == 0 {
		message.Parts = append(message.Parts,
			buildTextPart("(no projectwork files selected)"),
			buildTextPart("FILE MANIFEST:\n{\n  \"selected_files\": []\n}"),
		)
		return message, report, nil
	}

	for _, rel := range selected {
		cleanRel := filepath.ToSlash(rel)
		entry := requestManifestEntry{Path: cleanRel, Editable: editable, Transport: "manifest_only"}
		full, err := safeJoin(root, rel)
		if err != nil {
			return adapters.Message{}, multimodalAssemblyReport{}, err
		}
		if err := rejectSymlinkPath(root, full); err != nil {
			entry.Kind = "unsupported"
			entry.Reason = "unsupported_symlink_path"
			report.ManifestOnlyFiles++
			report.SkippedFiles++
			manifest = append(manifest, entry)
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				entry.Kind = "missing"
				entry.Reason = "candidate_deleted_or_missing"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			return adapters.Message{}, multimodalAssemblyReport{}, err
		}
		if info.IsDir() {
			continue
		}
		data, err := readFileUnderRoot(root, full)
		if err != nil {
			return adapters.Message{}, multimodalAssemblyReport{}, err
		}
		entry.Size = int64(len(data))
		entry.MIMEType = detectContentType(cleanRel, data)
		if len(data) > multimodalMaxFileBytes {
			entry.Kind = classifyBinaryInputKind(entry.MIMEType)
			entry.Reason = "skipped_too_large"
			report.ManifestOnlyFiles++
			report.SkippedFiles++
			manifest = append(manifest, entry)
			continue
		}
		if isLikelyText(cleanRel, data, entry.MIMEType) {
			entry.Kind = "text"
			chunk := fmt.Sprintf("TEXT FILE: %s\n```\n%s\n```", cleanRel, string(data))
			if len(chunk) > textRemaining {
				entry.Reason = "skipped_text_budget"
				report.ManifestOnlyFiles++
				report.SkippedFiles++
				manifest = append(manifest, entry)
				continue
			}
			entry.Transport = "inline_text"
			message.Parts = append(message.Parts, buildTextPart(chunk))
			report.TextFiles++
			report.UsedPaths = append(report.UsedPaths, cleanRel)
			textRemaining -= len(chunk)
			manifest = append(manifest, entry)
			continue
		}

		entry.Kind = classifyBinaryInputKind(entry.MIMEType)
		switch entry.Kind {
		case "image":
			if !supportsNativeInputImage(entry.MIMEType) {
				entry.Reason = "unsupported_image_format"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !profile.AdapterCapabilities.SupportsImageIn {
				entry.Reason = "adapter_lacks_image_transport"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !profile.EffectiveCapabilities.SupportsImageIn {
				entry.Reason = "model_capability_disabled_image"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if imageCount >= multimodalMaxImageCount || imageBytesTotal+len(data) > multimodalMaxImageBytes {
				entry.Reason = "skipped_image_budget"
				report.ManifestOnlyFiles++
				report.SkippedFiles++
				manifest = append(manifest, entry)
				continue
			}
			entry.Transport = "native_image"
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("IMAGE FILE: %s\n(Attached below as native image input.)", cleanRel)),
				adapters.Part{Kind: "image", Name: filepath.Base(cleanRel), RelPath: cleanRel, MIMEType: entry.MIMEType, Data: data},
			)
			report.ImageFiles++
			report.UsedPaths = append(report.UsedPaths, cleanRel)
			imageCount++
			imageBytesTotal += len(data)
			manifest = append(manifest, entry)
		case "audio":
			if !profile.AdapterCapabilities.SupportsAudioIn {
				entry.Reason = "adapter_lacks_audio_transport"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !profile.EffectiveCapabilities.SupportsAudioIn {
				entry.Reason = "model_capability_disabled_audio"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !supportsNativeInputAudio(profile.Adapter, entry.MIMEType, cleanRel) {
				entry.Reason = "unsupported_audio_format"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if nativeBinaryCount >= multimodalMaxNativeBinaryCount || nativeBinaryBytesTotal+len(data) > multimodalMaxNativeBinaryBytes {
				entry.Reason = "skipped_binary_budget"
				report.ManifestOnlyFiles++
				report.SkippedFiles++
				manifest = append(manifest, entry)
				continue
			}
			entry.Transport = "native_audio"
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("AUDIO FILE: %s\n(Attached below as native audio input.)", cleanRel)),
				adapters.Part{Kind: "audio", Name: filepath.Base(cleanRel), RelPath: cleanRel, MIMEType: entry.MIMEType, Data: data},
			)
			report.AudioFiles++
			report.UsedPaths = append(report.UsedPaths, cleanRel)
			nativeBinaryCount++
			nativeBinaryBytesTotal += len(data)
			manifest = append(manifest, entry)
		case "video":
			if !profile.AdapterCapabilities.SupportsVideoIn {
				entry.Reason = "adapter_lacks_video_transport"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !profile.EffectiveCapabilities.SupportsVideoIn {
				entry.Reason = "model_capability_disabled_video"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !supportsNativeInputVideo(profile.Adapter, entry.MIMEType, cleanRel) {
				entry.Reason = "unsupported_video_format"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if nativeBinaryCount >= multimodalMaxNativeBinaryCount || nativeBinaryBytesTotal+len(data) > multimodalMaxNativeBinaryBytes {
				entry.Reason = "skipped_binary_budget"
				report.ManifestOnlyFiles++
				report.SkippedFiles++
				manifest = append(manifest, entry)
				continue
			}
			entry.Transport = "native_video"
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("VIDEO FILE: %s\n(Attached below as native video input.)", cleanRel)),
				adapters.Part{Kind: "video", Name: filepath.Base(cleanRel), RelPath: cleanRel, MIMEType: entry.MIMEType, Data: data},
			)
			report.VideoFiles++
			report.UsedPaths = append(report.UsedPaths, cleanRel)
			nativeBinaryCount++
			nativeBinaryBytesTotal += len(data)
			manifest = append(manifest, entry)
		default:
			if !profile.AdapterCapabilities.SupportsFileIn {
				entry.Reason = "adapter_lacks_file_transport"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !profile.EffectiveCapabilities.SupportsFileIn {
				entry.Reason = "model_capability_disabled_file"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if !supportsNativeInputFile(profile.Adapter, entry.MIMEType, cleanRel) {
				entry.Reason = "unsupported_file_format"
				report.ManifestOnlyFiles++
				manifest = append(manifest, entry)
				continue
			}
			if nativeBinaryCount >= multimodalMaxNativeBinaryCount || nativeBinaryBytesTotal+len(data) > multimodalMaxNativeBinaryBytes {
				entry.Reason = "skipped_binary_budget"
				report.ManifestOnlyFiles++
				report.SkippedFiles++
				manifest = append(manifest, entry)
				continue
			}
			entry.Transport = "native_file"
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("FILE INPUT: %s\n(Attached below as native document/file input.)", cleanRel)),
				adapters.Part{Kind: "file", Name: filepath.Base(cleanRel), RelPath: cleanRel, MIMEType: entry.MIMEType, Data: data},
			)
			report.NativeFileFiles++
			report.UsedPaths = append(report.UsedPaths, cleanRel)
			nativeBinaryCount++
			nativeBinaryBytesTotal += len(data)
			manifest = append(manifest, entry)
		}
	}

	manifestPayload := map[string]any{
		"capability_mode":  profile.CapabilityMode,
		"effective_inputs": profile.EffectiveCapabilities,
		"selected_files":   manifest,
	}
	encoded, err := json.MarshalIndent(manifestPayload, "", "  ")
	if err != nil {
		return adapters.Message{}, multimodalAssemblyReport{}, err
	}
	message.Parts = append(message.Parts, buildTextPart("FILE MANIFEST:\n"+string(encoded)))
	return message, report, nil
}

func normalizeTemporaryAttachments(input []temporaryAttachmentInput) ([]temporaryAttachmentInput, error) {
	if len(input) == 0 {
		return nil, nil
	}
	if len(input) > temporaryAttachmentMaxCount {
		return nil, fmt.Errorf("temporary attachments limit exceeded: %d > %d", len(input), temporaryAttachmentMaxCount)
	}
	out := make([]temporaryAttachmentInput, 0, len(input))
	imageTotal := 0
	for i, item := range input {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		name = filepath.Base(name)
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		mimeType := strings.ToLower(strings.TrimSpace(item.MIMEType))
		text := strings.TrimSpace(item.Text)
		data := strings.TrimSpace(item.Data)
		if strings.HasPrefix(data, "data:") {
			if comma := strings.Index(data, ","); comma >= 0 {
				data = data[comma+1:]
			}
		}
		switch kind {
		case "text", "log":
			if text == "" {
				return nil, fmt.Errorf("temporary attachment %q is empty", name)
			}
			if len([]byte(text)) > temporaryAttachmentMaxTextBytes {
				return nil, fmt.Errorf("temporary text attachment %q exceeds %d bytes", name, temporaryAttachmentMaxTextBytes)
			}
			if mimeType == "" {
				mimeType = "text/plain"
			}
			out = append(out, temporaryAttachmentInput{ID: strings.TrimSpace(item.ID), Name: name, Kind: "text", MIMEType: mimeType, Text: text, SizeBytes: int64(len([]byte(text)))})
		case "image", "screenshot":
			if data == "" {
				return nil, fmt.Errorf("temporary image attachment %q is empty", name)
			}
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return nil, fmt.Errorf("temporary image attachment %q is not valid base64", name)
			}
			if len(decoded) > temporaryAttachmentMaxImageBytes {
				return nil, fmt.Errorf("temporary image attachment %q exceeds %d bytes after compression", name, temporaryAttachmentMaxImageBytes)
			}
			imageTotal += len(decoded)
			if imageTotal > temporaryAttachmentMaxImageTotalBytes {
				return nil, fmt.Errorf("temporary image attachments exceed total image budget of %d bytes", temporaryAttachmentMaxImageTotalBytes)
			}
			if mimeType == "" {
				mimeType = detectContentType(name, decoded)
			}
			if !supportsNativeInputImage(mimeType) {
				return nil, fmt.Errorf("temporary image attachment %q must be png, jpeg, webp, or gif", name)
			}
			out = append(out, temporaryAttachmentInput{ID: strings.TrimSpace(item.ID), Name: name, Kind: "image", MIMEType: mimeType, Data: base64.StdEncoding.EncodeToString(decoded), SizeBytes: int64(len(decoded))})
		case "pdf", "file":
			if data == "" {
				return nil, fmt.Errorf("temporary PDF attachment %q is empty", name)
			}
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return nil, fmt.Errorf("temporary PDF attachment %q is not valid base64", name)
			}
			if len(decoded) > multimodalMaxFileBytes {
				return nil, fmt.Errorf("temporary PDF attachment %q exceeds %d bytes", name, multimodalMaxFileBytes)
			}
			if mimeType == "" {
				mimeType = "application/pdf"
			}
			if mimeType != "application/pdf" && !strings.EqualFold(filepath.Ext(name), ".pdf") {
				return nil, fmt.Errorf("temporary file attachment %q must be a PDF", name)
			}
			out = append(out, temporaryAttachmentInput{ID: strings.TrimSpace(item.ID), Name: name, Kind: "pdf", MIMEType: "application/pdf", Data: base64.StdEncoding.EncodeToString(decoded), SizeBytes: int64(len(decoded))})
		case "":
			return nil, fmt.Errorf("temporary attachment %q is missing kind", name)
		default:
			return nil, fmt.Errorf("temporary attachment %q has unsupported kind %q; use image, pdf, or text/log", name, kind)
		}
	}
	return out, nil
}

func buildTemporaryAttachmentMessage(attachments []temporaryAttachmentInput, profile adapters.TransportProfile) (adapters.Message, error) {
	clean, err := normalizeTemporaryAttachments(attachments)
	if err != nil {
		return adapters.Message{}, err
	}
	message := adapters.Message{Role: "user", Parts: []adapters.Part{}}
	if len(clean) == 0 {
		return message, nil
	}
	message.Parts = append(message.Parts, buildTextPart(`TEMPORARY ATTACHMENTS
These attachments are input-only reference context for this run. Use them to understand the user's objective. Do not copy them into project files, do not return them in file operations, and do not include their raw content in changed-file output unless the user explicitly asks to add an attachment to the project.`))
	manifest := make([]map[string]any, 0, len(clean))
	for _, item := range clean {
		name := filepath.Base(strings.TrimSpace(item.Name))
		switch strings.TrimSpace(item.Kind) {
		case "text":
			message.Parts = append(message.Parts, buildTextPart(fmt.Sprintf("TEMPORARY TEXT ATTACHMENT: %s\n```\n%s\n```", name, strings.TrimSpace(item.Text))))
			manifest = append(manifest, map[string]any{"name": name, "kind": "text", "mime_type": item.MIMEType, "size_bytes": item.SizeBytes})
		case "image":
			if !profile.AdapterCapabilities.SupportsImageIn || !profile.EffectiveCapabilities.SupportsImageIn {
				return adapters.Message{}, fmt.Errorf("Temporary image attachment %q cannot be sent because this Builder does not currently accept images. Enable manual image input / Accept Images for this model, or remove the image attachment.", name)
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Data))
			if err != nil {
				return adapters.Message{}, fmt.Errorf("temporary image attachment %q is not valid base64", name)
			}
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("TEMPORARY IMAGE ATTACHMENT: %s\n(Attached below as native image input. Input-only reference; do not return it.)", name)),
				adapters.Part{Kind: "image", Name: name, RelPath: "temporary/" + name, MIMEType: item.MIMEType, Data: decoded},
			)
			manifest = append(manifest, map[string]any{"name": name, "kind": "image", "mime_type": item.MIMEType, "size_bytes": len(decoded), "transport": "native_image"})
		case "pdf":
			if !profile.AdapterCapabilities.SupportsFileIn || !profile.EffectiveCapabilities.SupportsFileIn {
				return adapters.Message{}, fmt.Errorf("Temporary PDF attachment %q cannot be sent because this Builder does not currently accept file inputs. Enable file input for this model, or remove the PDF.", name)
			}
			if !supportsNativeInputFile(profile.Adapter, item.MIMEType, name) {
				return adapters.Message{}, fmt.Errorf("Temporary PDF attachment %q is not supported by this Builder adapter.", name)
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Data))
			if err != nil {
				return adapters.Message{}, fmt.Errorf("temporary PDF attachment %q is not valid base64", name)
			}
			message.Parts = append(message.Parts,
				buildTextPart(fmt.Sprintf("TEMPORARY PDF ATTACHMENT: %s\n(Attached below as native file input. Input-only reference; do not return it.)", name)),
				adapters.Part{Kind: "file", Name: name, RelPath: "temporary/" + name, MIMEType: "application/pdf", Data: decoded},
			)
			manifest = append(manifest, map[string]any{"name": name, "kind": "pdf", "mime_type": "application/pdf", "size_bytes": len(decoded), "transport": "native_file"})
		}
	}
	encoded, err := json.MarshalIndent(map[string]any{"temporary_attachments": manifest}, "", "  ")
	if err != nil {
		return adapters.Message{}, err
	}
	message.Parts = append(message.Parts, buildTextPart("TEMPORARY ATTACHMENT MANIFEST:\n"+string(encoded)))
	return message, nil
}

func appendMessageIfPresent(messages []adapters.Message, message adapters.Message) []adapters.Message {
	if len(message.Parts) > 0 || strings.TrimSpace(message.Text) != "" {
		return append(messages, message)
	}
	return messages
}

func buildSelectedContextAndTemporaryAttachmentMessages(projectworkRoot string, contextFiles []string, temporaryAttachments []temporaryAttachmentInput, profile adapters.TransportProfile, heading string, editable bool) ([]adapters.Message, multimodalAssemblyReport, error) {
	messages := []adapters.Message{}
	contextMessage, contextReport, err := buildMultimodalContextMessage(projectworkRoot, contextFiles, heading, profile, editable, builderContextMaxTextBytes)
	if err != nil {
		return nil, multimodalAssemblyReport{}, err
	}
	messages = appendMessageIfPresent(messages, contextMessage)
	tempMessage, err := buildTemporaryAttachmentMessage(temporaryAttachments, profile)
	if err != nil {
		return nil, multimodalAssemblyReport{}, err
	}
	messages = appendMessageIfPresent(messages, tempMessage)
	return messages, contextReport, nil
}

func formatTemporaryAttachmentNames(attachments []temporaryAttachmentInput) string {
	clean, err := normalizeTemporaryAttachments(attachments)
	if err != nil || len(clean) == 0 {
		return "none"
	}
	names := make([]string, 0, len(clean))
	for _, item := range clean {
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = strings.TrimSpace(item.Kind)
		}
		names = append(names, label)
	}
	return strings.Join(names, ", ")
}

func builderJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":         map[string]any{"type": "string"},
						"action":       map[string]any{"type": "string", "enum": []string{"create", "overwrite", "delete"}},
						"content":      map[string]any{"type": "string"},
						"artifact_ref": map[string]any{"type": "string"},
					},
					"required":             []string{"path", "action"},
					"additionalProperties": false,
				},
			},
			"artifacts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":        map[string]any{"type": "string"},
						"encoding":  map[string]any{"type": "string"},
						"mime_type": map[string]any{"type": "string"},
						"data":      map[string]any{"type": "string"},
					},
					"required":             []string{"id", "encoding", "data"},
					"additionalProperties": false,
				},
			},
			"ai_context": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agentgo_file":      map[string]any{"type": "string", "enum": []string{"ai_context"}},
					"file_version":      map[string]any{"type": "integer", "enum": []int{agentGOToolVersion}},
					"terminology":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"architecture":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"prior_changes":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"known_issues":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"risks_constraints": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required":             []string{"agentgo_file", "file_version", "terminology", "architecture", "prior_changes", "known_issues", "risks_constraints"},
				"additionalProperties": false,
			},
			"builder_report": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status":          map[string]any{"type": "string", "enum": []string{"completed", "partial", "blocked"}},
					"summary":         map[string]any{"type": "string"},
					"changed_files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"issues_found":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"recommendations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"next_steps":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"additionalProperties": false,
			},
			"notes":      map[string]any{"type": "string"},
			"warnings":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"confidence": map[string]any{"type": "number"},
		},
		"required":             []string{"summary", "files", "ai_context"},
		"additionalProperties": false,
	}
}

func reviewerJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"overview": map[string]any{"type": "string"},
			"models": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"model":       map[string]any{"type": "string"},
						"grade":       map[string]any{"type": "integer"},
						"summary":     map[string]any{"type": "string"},
						"upgrades":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"misses":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"merge_ready": map[string]any{"type": "boolean"},
					},
					"required":             []string{"model", "grade", "summary", "upgrades", "misses", "merge_ready"},
					"additionalProperties": false,
				},
			},
			"recommended_candidate": map[string]any{"type": "string"},
			"reasoning":             map[string]any{"type": "string"},
			"next_prompt":           map[string]any{"type": "string"},
			"alternate_next_prompts": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"confidence": map[string]any{"type": "number"},
		},
		"required":             []string{"overview", "models", "recommended_candidate", "reasoning", "next_prompt", "alternate_next_prompts"},
		"additionalProperties": false,
	}
}

func loadSystemPromptForMode(cfg AppConfig, role, promptMode string) (string, error) {
	switch normalizePromptMode(promptMode) {
	case promptModeNone:
		return "", nil
	case promptModeLow:
		return loadPromptFile(systemPromptPath(cfg, role, promptWeightLow))
	default:
		return loadPromptFile(systemPromptPath(cfg, role, promptWeightBalanced))
	}
}

func loadBuilderSystemPrompt(cfg AppConfig, promptMode string) (string, error) {
	return loadSystemPromptForMode(cfg, promptRoleBuilder, promptMode)
}

func loadReviewerSystemPrompt(cfg AppConfig, promptMode string) (string, error) {
	return loadSystemPromptForMode(cfg, promptRoleObserver, promptMode)
}

func loadPromptHelperSystemPrompt(cfg AppConfig, promptMode string) (string, error) {
	return loadSystemPromptForMode(cfg, promptRoleHelper, promptMode)
}

func appendModelUggProtocol(instructions string, model ModelConfig) string {
	if !model.UseUggPrompt {
		return strings.TrimSpace(instructions)
	}
	return joinNonEmptyWithDoubleNewlines(strings.TrimSpace(instructions), modelUggProtocolPrompt)
}

const deadDropUploadedFileOnlyInstruction = "All revisions, edits, and updates must be applied only to the uploaded DeadDrop file. Return one complete finalized replacement for that file only, with no markdown fences, commentary, prefixes, or extra text."

func loadDeadDropSystemPrompt(cfg AppConfig, promptMode string) (string, error) {
	mode := normalizePromptMode(promptMode)
	switch mode {
	case promptModeLow:
		return loadPromptFile(filepath.Join(systemPromptsDir, deadDropPromptLowFile))
	case promptModeBalanced:
		return loadPromptFile(filepath.Join(systemPromptsDir, deadDropPromptHighFile))
	case promptModeNone:
		return "", errors.New("DeadDrop requires all active builders to use Low or Balanced system prompt mode.")
	default:
		return loadPromptFile(filepath.Join(systemPromptsDir, deadDropPromptHighFile))
	}
}

func normalizeDeadDropStopScore(score int) int {
	if score < 1 || score > 100 {
		return 95
	}
	return score
}

func effectiveDeadDropStopScore(configuredStopScore, cycleNumber, waveIndex int) int {
	configuredStopScore = normalizeDeadDropStopScore(configuredStopScore)
	if cycleNumber == 1 && waveIndex == 0 {
		return 100
	}
	return configuredStopScore
}

func normalizeDeadDropRevisionLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "medium"
	}
}

func deadDropAdjustmentInstruction(level string) string {
	switch normalizeDeadDropRevisionLevel(level) {
	case "low":
		return "Make lightweight, high-value improvements only."
	case "high":
		return "Make broader but still purposeful revisions when they clearly improve the result."
	default:
		return "Make meaningful improvements that materially help the user's goal."
	}
}

func buildBuilderRequestPayload(cfg AppConfig, model ModelConfig, prompt, userContext, aiContext string, extraMessages []adapters.Message) (adapterRequestPayload, error) {
	instructions, err := loadBuilderSystemPrompt(cfg, model.PromptMode)
	if err != nil {
		return adapterRequestPayload{}, err
	}
	instructions = appendModelUggProtocol(instructions, model)
	messages := []adapters.Message{
		buildTextMessage("user", wrapBuilderObjectivePrompt(prompt)),
		buildTextMessage("user", `MODEL USER CONTEXT (meta/user_context.json):
`+strings.TrimSpace(userContext)),
		buildTextMessage("user", `MODEL AI CONTEXT (meta/ai_context.json):
ai_context.json is strict project memory. Keep returned ai_context in the canonical schema: agentgo_file, file_version, terminology, architecture, prior_changes, known_issues, risks_constraints. Keep durable project facts only; revise changed facts; remove stale/resolved issues; avoid duplicates; never store temporary attachment content, chat transcripts, or private reasoning. AgentGO will sanitize, dedupe, trim, and may preserve previous memory if the returned memory is empty or invalid.
`+strings.TrimSpace(aiContext)),
	}
	for _, message := range extraMessages {
		messages = appendMessageIfPresent(messages, message)
	}
	return adapterRequestPayload{Instructions: instructions, Messages: messages, ExpectJSON: true, JSONSchema: builderJSONSchema()}, nil
}

func wrapBuilderObjectivePrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		trimmed = "(no objective prompt provided)"
	}
	return strings.TrimSpace(`CURRENT OBJECTIVE
- Treat the following user prompt as the task for this Builder run.

EXECUTION FOCUS
- Satisfy the user's task directly.
- Avoid unrelated scope or speculative extras.
- Use the smallest correct change set that fully completes the requested outcome.

USER PROMPT
` + trimmed)
}

func buildReviewerRequestPayload(cfg AppConfig, model ModelConfig, messages []adapters.Message) (adapterRequestPayload, error) {
	instructions, err := loadReviewerSystemPrompt(cfg, model.PromptMode)
	if err != nil {
		return adapterRequestPayload{}, err
	}
	instructions = appendModelUggProtocol(instructions, model)
	return adapterRequestPayload{Instructions: instructions, Messages: messages, ExpectJSON: true, JSONSchema: reviewerJSONSchema()}, nil
}

func (a *App) runModelRequest(model ModelConfig, projectName, executionID, prompt string, contextFiles []string, temporaryAttachments []temporaryAttachmentInput, mediaInputRoles map[string]string, wireTapEnabled bool) modelRunResult {
	result := modelRunResult{ModelID: modelIDString(model.ID), ModelLabel: model.Label}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(modelIDString(model.ID), projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(modelIDString(model.ID), executionID)
		a.mu.Unlock()
	}()
	defer cancel()

	if modelIsVideoGeneration(model) {
		return a.runVideoModelRequest(model, projectName, executionID, prompt, contextFiles, mediaInputRoles)
	}
	if modelIsMeshGeneration(model) {
		return a.runMeshModelRequest(model, projectName, executionID, prompt, contextFiles)
	}
	baseEntry := diagnosticsEntry{Mode: "builder", Target: model.Label, ModelID: modelIDString(model.ID), ModelLabel: model.Label, Project: projectName, Prompt: strings.TrimSpace(prompt)}
	baseEntry = a.decorateDiagnosticsWithCurrentWave(projectName, baseEntry)

	a.logf(modelIDString(model.ID), "info", "Starting request for %s in project %s", model.Label, projectName)

	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		result.Err = fmt.Errorf("projectwork folder error: %w", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	projectRoot, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		result.Err = fmt.Errorf("project folder error: %w", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	userContextPath := filepath.Join(metaRoot, "user_context.json")
	aiContextPath := filepath.Join(metaRoot, "ai_context.json")
	userContext, _ := os.ReadFile(userContextPath)
	aiContext, _ := os.ReadFile(aiContextPath)
	transportProfile := adapters.ResolveTransportProfile(toAdapterModelConfig(model))
	extraMessages, contextReport, err := buildSelectedContextAndTemporaryAttachmentMessages(projectworkRoot, contextFiles, temporaryAttachments, transportProfile, "SELECTED PROJECTWORK CONTEXT FILES:", true)
	if err != nil {
		result.Err = fmt.Errorf("context assembly error: %w", err)
		baseEntry.Files = a.projectContextDiagnosticsFiles(projectName, nil)
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	if wireTapEnabled {
		sliceMessage, sliceErr := a.buildWireTapRuntimeSliceMessage(ctx, model, projectName, prompt)
		if sliceErr != nil {
			result.Err = fmt.Errorf("WireTap selection failed: %w", sliceErr)
			baseEntry.Files = a.projectContextDiagnosticsFiles(projectName, contextReport.UsedPaths)
			a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()))
			a.logf(modelIDString(model.ID), "error", "%v", result.Err)
			return result
		}
		extraMessages = appendMessageIfPresent(extraMessages, sliceMessage)
	}
	requestPayload, err := buildBuilderRequestPayload(a.cfg, model, prompt, string(userContext), string(aiContext), extraMessages)
	baseEntry.Files = a.projectContextDiagnosticsFiles(projectName, contextReport.UsedPaths)
	baseEntry.Files = appendUniqueDiagnosticsFile(baseEntry.Files, makeDiagnosticsFileRef(filepath.ToSlash(filepath.Join(a.relativeMetaPath(model, projectName), "user_context.json"))))
	baseEntry.Files = appendUniqueDiagnosticsFile(baseEntry.Files, makeDiagnosticsFileRef(filepath.ToSlash(filepath.Join(a.relativeMetaPath(model, projectName), "ai_context.json"))))
	baseEntry.StatusMessage = formatMultimodalReport(contextReport)
	if err != nil {
		result.Err = err
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()).withStatusMessage(baseEntry.StatusMessage))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	baseEntry.SystemPrompt = strings.TrimSpace(requestPayload.Instructions)
	a.publishDiagnostics(baseEntry.withStage("Assembled").withStatusMessage(baseEntry.StatusMessage))
	a.publishDiagnostics(baseEntry.withStage("Sent").withStatusMessage(baseEntry.StatusMessage))

	adapterResp, err := a.executeAdapterResponse(ctx, model, requestPayload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			result.Err = errors.New("request canceled")
			a.logf(modelIDString(model.ID), "warn", "Request canceled")
		} else {
			result.Err = err
			a.logf(modelIDString(model.ID), "error", "Request failed: %v", err)
		}
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(result.Err.Error()))
		return result
	}
	responseText := strings.TrimSpace(adapterResp.Text)
	responsePreview := formatBuilderDiagnosticsResponse(adapterResp)
	rawResponse := formatBuilderRawResponseDocument(adapterResp)
	a.publishDiagnostics(baseEntry.withStage("Response Received").withResponse(responsePreview))
	if !a.isWaveExecutionCurrent(projectName, executionID) {
		result.Err = errors.New("stale execution")
		a.logf(modelIDString(model.ID), "warn", "Discarded stale response for project %s", projectName)
		return result
	}

	rawDir := builderResponsesDir(metaRoot)
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		result.Err = fmt.Errorf("failed to create builder response archive: %w", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	timestamp := time.Now().Format("20060102_150405")
	rawFile := filepath.Join(rawDir, "response_"+timestamp+".md")
	if err := os.WriteFile(rawFile, []byte(rawResponse), 0o644); err != nil {
		result.Err = fmt.Errorf("failed to save raw response: %w", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		a.logf(modelIDString(model.ID), "warn", "Raw builder response saved at %s", filepath.ToSlash(rawFile))
		return result
	}

	if err := pruneResponseArchive(rawDir, a.cfg.MaxResponseHistory, "response_", ".md", nil); err != nil {
		a.logf(modelIDString(model.ID), "warn", "Failed trimming builder response history: %v", err)
	}
	builderOutput := newBuilderOutputState(model, projectName, rawFile, rawResponse)
	defer func() {
		if !builderOutput.HasResponse {
			return
		}
		if err := writeBuilderOutputState(metaRoot, builderOutput); err != nil {
			a.logf(modelIDString(model.ID), "warn", "Failed to save latest builder output state: %v", err)
		}
	}()

	parsed, parseMeta, err := parseBuilderAdapterResponse(adapterResp, projectRoot)
	builderOutput.UserFacingResponse = strings.TrimSpace(parseMeta.UserFacingText)
	if err != nil {
		truncationHint := builderInvalidJSONTruncationHint(responseText)
		result.Err = fmt.Errorf("invalid builder response: %w", err)
		builderOutput.Kind = "invalid_response"
		builderOutput.StatusLabel = "Unread builder response"
		builderOutput.StatusMessage = "AgentGO could not parse the execute output into the required builder JSON format."
		if strings.TrimSpace(truncationHint) != "" {
			builderOutput.StatusMessage = truncationHint
		}
		builderOutput.Error = result.Err.Error()
		if builderOutput.UserFacingResponse == "" {
			builderOutput.UserFacingResponse = strings.TrimSpace(responsePreview)
		}
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(joinNonEmptyWithDoubleNewlines(result.Err.Error(), truncationHint)))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		if strings.TrimSpace(truncationHint) != "" {
			a.logf("warning", "toast", "%s", truncationHint)
			a.logf(modelIDString(model.ID), "warn", "%s", truncationHint)
		}
		a.logf(modelIDString(model.ID), "warn", "Raw builder response saved at %s", filepath.ToSlash(rawFile))
		a.logf(modelIDString(model.ID), "warn", "Response preview: %s", previewForLog(responsePreview, 500))
		return result
	}
	if note := sanitizeBuilderResponseAIContext(&parsed, metaRoot); strings.TrimSpace(note) != "" {
		a.logf(modelIDString(model.ID), "info", "Builder ai_context sanitized: %s", note)
	}
	updateBuilderOutputFromParsed(&builderOutput, parsed, parseMeta)
	if parseMeta.Normalized {
		a.logf(modelIDString(model.ID), "info", "Builder schema normalized before merge validation (%s)", parseMeta.NormalizationNote)
	}
	if err := validateBuilderResponse(parsed); err != nil {
		result.Err = fmt.Errorf("builder validation failed: %w", err)
		builderOutput.Kind = "invalid_response"
		builderOutput.StatusLabel = "Unread builder response"
		builderOutput.StatusMessage = "AgentGO read the builder JSON, but it did not pass validation for merge handling."
		builderOutput.Error = result.Err.Error()
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		a.logf(modelIDString(model.ID), "warn", "Raw builder response saved at %s", filepath.ToSlash(rawFile))
		a.logf(modelIDString(model.ID), "warn", "Response preview: %s", previewForLog(responseText, 500))
		return result
	}
	if !a.isWaveExecutionCurrent(projectName, executionID) {
		builderOutput.HasResponse = false
		result.Err = errors.New("stale execution")
		a.logf(modelIDString(model.ID), "warn", "Discarded stale builder update for project %s", projectName)
		return result
	}
	limits, err := a.loadProjectLimits(projectName)
	if err != nil {
		result.Err = fmt.Errorf("failed loading project limits: %w", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}
	applied, err := applyBuilderResponse(projectRoot, metaRoot, parsed, limits)
	if err != nil {
		result.Err = fmt.Errorf("failed applying builder response: %w", err)
		builderOutput.Kind = "apply_failed"
		builderOutput.StatusLabel = "Unread builder response"
		builderOutput.StatusMessage = "AgentGO understood the builder output, but could not apply it to the model workspace."
		builderOutput.Error = result.Err.Error()
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(result.Err.Error()))
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		return result
	}

	returnedFiles := summarizeBuilderReturnedFiles(model, projectName, projectRoot, parsed)
	builderOutput.ReturnedFiles = returnedFiles
	if len(returnedFiles) > 0 {
		a.logf(modelIDString(model.ID), "info", "Returned files: %s", summarizeReturnedFilesForLog(returnedFiles))
	}

	result.Valid = true
	result.AppliedOperations = applied
	pendingCount := 0
	if applied > 0 {
		projectworkRoot, joinErr := a.projectWorkRoot(projectName)
		if joinErr != nil {
			a.logf(modelIDString(model.ID), "warn", "Unable to inspect pending merge state: %v", joinErr)
		} else if diffFiles, diffErr := buildDiffFilesForRoots(model, projectName, projectRoot, projectworkRoot); diffErr != nil {
			a.logf(modelIDString(model.ID), "warn", "Unable to inspect pending merge state: %v", diffErr)
		} else {
			pendingCount = len(diffFiles)
		}
	}
	if !a.isWaveExecutionCurrent(projectName, executionID) {
		builderOutput.HasResponse = false
		result.Err = errors.New("stale execution")
		a.logf(modelIDString(model.ID), "warn", "Discarded stale merge-ready state for project %s", projectName)
		return result
	}
	builderOutput.AppliedOps = applied
	builderOutput.PendingCount = pendingCount
	result.PendingCount = pendingCount
	builderOutput.StatusLabel = "Unread builder response"
	if pendingCount > 0 {
		builderOutput.Kind = "merge_ready"
		if pendingCount == 1 {
			builderOutput.StatusMessage = "1 mergeable file change is ready for review."
		} else {
			builderOutput.StatusMessage = fmt.Sprintf("%d mergeable file changes are ready for review.", pendingCount)
		}
	} else if len(parsed.Files) == 0 {
		builderOutput.Kind = "text_only"
		builderOutput.StatusMessage = "No mergeable files were returned in this execute response."
	} else {
		builderOutput.Kind = "text_only"
		builderOutput.StatusMessage = "No mergeable file changes are currently pending for this execute response."
	}
	a.setPendingMergeCount(projectName, modelIDString(model.ID), pendingCount)
	parsedNote := strings.TrimSpace(builderOutput.StatusMessage)
	if applied > 0 {
		parsedNote = strings.TrimSpace(fmt.Sprintf("Applied %d file operation(s). %s", applied, parsedNote))
	}
	a.publishDiagnostics(baseEntry.withStage("Parsed").withStatusMessage(parsedNote))
	a.logf(modelIDString(model.ID), "info", "Completed request for project %s. Raw response saved to %s, applied %d file operations", projectName, filepath.Base(rawFile), applied)
	return result
}

func newBuilderOutputState(model ModelConfig, projectName, rawFile, rawResponse string) builderOutputState {
	return builderOutputState{
		ModelID:            modelIDString(model.ID),
		ModelLabel:         model.Label,
		Project:            projectName,
		HasResponse:        true,
		Unread:             true,
		Timestamp:          time.Now().Format(time.RFC3339),
		RawFile:            filepath.Base(rawFile),
		RawResponse:        strings.TrimSpace(rawResponse),
		UserFacingResponse: "",
		Warnings:           []string{},
		AIContextRisks:     []string{},
		AIContextNext:      []string{},
		ReturnedFiles:      []builderReturnedFile{},
	}
}

func (a *App) writeCypherEnrichmentFailureOutput(model ModelConfig, projectName string, roundErr cypherEnrichmentRoundError) error {
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return err
	}
	message := "Cypher enrichment failed: invalid AI JSON. Last safe Cypher.json was preserved."
	details := strings.TrimSpace(roundErr.Error())
	if strings.TrimSpace(roundErr.ParseDetail) != "" {
		if details != "" {
			details += "\n"
		}
		details += strings.TrimSpace(roundErr.ParseDetail)
	}
	state := builderOutputState{
		ModelID:            modelIDString(model.ID),
		ModelLabel:         model.Label,
		Project:            projectName,
		HasResponse:        true,
		Unread:             true,
		Kind:               "cypher_error",
		StatusLabel:        "Unread Cypher error",
		StatusMessage:      message,
		Timestamp:          time.Now().Format(time.RFC3339),
		RawResponse:        strings.TrimSpace(roundErr.ResponsePreview),
		UserFacingResponse: message,
		Summary:            message,
		Warnings:           []string{},
		AIContextRisks:     []string{},
		AIContextNext:      []string{},
		ReturnedFiles:      []builderReturnedFile{},
		Error:              details,
	}
	return writeBuilderOutputState(metaRoot, state)
}

const (
	aiContextFileIdentity       = "ai_context"
	aiContextMaxEntriesPerField = 20
	aiContextMaxEntryLength     = 240
	builderReportDefaultStatus  = "completed"
)

var aiContextDedupeStripRE = regexp.MustCompile(`[^a-z0-9]+`)

func trimStringList(items []string, maxItems, maxLen int) []string {
	if maxItems <= 0 {
		maxItems = len(items)
	}
	if maxLen <= 0 {
		maxLen = 240
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		clean := strings.Join(strings.Fields(strings.TrimSpace(item)), " ")
		if clean == "" {
			continue
		}
		if len([]rune(clean)) > maxLen {
			runes := []rune(clean)
			clean = strings.TrimSpace(string(runes[:maxLen])) + "…"
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
		if len(out) >= maxItems {
			break
		}
	}
	if len(out) == 0 {
		return []string{}
	}
	return out
}

func aiContextDedupeKey(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	lower = aiContextDedupeStripRE.ReplaceAllString(lower, " ")
	return strings.Join(strings.Fields(lower), " ")
}

func aiContextKeysNearDuplicate(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	shorter, longer := a, b
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < 24 {
		return false
	}
	return strings.Contains(longer, shorter)
}

func shouldDropAIContextEntry(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return true
	}
	blocked := []string{
		"temporary attachment",
		"temporary text attachment",
		"temporary image attachment",
		"attachment manifest",
		"data:image/",
		"base64,",
		"full chat transcript",
		"private reasoning",
	}
	for _, marker := range blocked {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func trimAIContextStringList(items []string, maxItems, maxLen int) []string {
	if maxItems <= 0 {
		maxItems = aiContextMaxEntriesPerField
	}
	if maxLen <= 0 {
		maxLen = aiContextMaxEntryLength
	}
	out := make([]string, 0, len(items))
	seenKeys := make([]string, 0, len(items))
	for _, item := range items {
		clean := strings.Join(strings.Fields(strings.TrimSpace(item)), " ")
		if clean == "" || shouldDropAIContextEntry(clean) {
			continue
		}
		if strings.Contains(clean, "```") {
			continue
		}
		if len([]rune(clean)) > maxLen {
			runes := []rune(clean)
			clean = strings.TrimSpace(string(runes[:maxLen])) + "…"
		}
		key := aiContextDedupeKey(clean)
		if key == "" {
			continue
		}
		duplicate := false
		for _, seen := range seenKeys {
			if aiContextKeysNearDuplicate(key, seen) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		seenKeys = append(seenKeys, key)
		out = append(out, clean)
		if len(out) >= maxItems {
			break
		}
	}
	if len(out) == 0 {
		return []string{}
	}
	return out
}

func aiContextEntryCount(ctx builderAIContext) int {
	return len(ctx.Terminology) + len(ctx.Architecture) + len(ctx.PriorChanges) + len(ctx.KnownIssues) + len(ctx.RisksConstraints)
}

func defaultAIContext() builderAIContext {
	return builderAIContext{
		AgentGOFile:      aiContextFileIdentity,
		FileVersion:      agentGOToolVersion,
		Terminology:      []string{},
		Architecture:     []string{},
		PriorChanges:     []string{},
		KnownIssues:      []string{},
		RisksConstraints: []string{},
	}
}

func normalizeAIContext(ctx builderAIContext) builderAIContext {
	return builderAIContext{
		AgentGOFile:      aiContextFileIdentity,
		FileVersion:      agentGOToolVersion,
		Terminology:      trimAIContextStringList(ctx.Terminology, aiContextMaxEntriesPerField, aiContextMaxEntryLength),
		Architecture:     trimAIContextStringList(ctx.Architecture, aiContextMaxEntriesPerField, aiContextMaxEntryLength),
		PriorChanges:     trimAIContextStringList(ctx.PriorChanges, aiContextMaxEntriesPerField, aiContextMaxEntryLength),
		KnownIssues:      trimAIContextStringList(ctx.KnownIssues, aiContextMaxEntriesPerField, aiContextMaxEntryLength),
		RisksConstraints: trimAIContextStringList(ctx.RisksConstraints, aiContextMaxEntriesPerField, aiContextMaxEntryLength),
	}
}

func defaultAIContextJSON() []byte {
	ctx := defaultAIContext()
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return []byte("{\"agentgo_file\":\"ai_context\",\"file_version\":1,\"terminology\":[],\"architecture\":[],\"prior_changes\":[],\"known_issues\":[],\"risks_constraints\":[]}\\n")
	}
	return append(data, '\n')
}

func formatAIContextObject(ctx builderAIContext) string {
	ctx = normalizeAIContext(ctx)
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return string(defaultAIContextJSON())
	}
	return strings.TrimSpace(string(data)) + "\n"
}

func parseAIContextText(text string) (builderAIContext, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return defaultAIContext(), false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return defaultAIContext(), false
	}
	migrated, _ := migrateAIContextMap(raw)
	data, err := json.Marshal(migrated)
	if err != nil {
		return defaultAIContext(), false
	}
	var ctx builderAIContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return defaultAIContext(), false
	}
	return normalizeAIContext(ctx), true
}

func readAIContextFile(path string) (builderAIContext, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultAIContext(), false
	}
	return parseAIContextText(string(data))
}

func sanitizeBuilderResponseAIContext(resp *builderResponse, metaRoot string) string {
	if resp == nil {
		return ""
	}
	previous, hasPrevious := readAIContextFile(filepath.Join(metaRoot, "ai_context.json"))
	returned := normalizeAIContext(resp.AIContext)
	if hasPrevious && aiContextEntryCount(previous) > 0 && aiContextEntryCount(returned) == 0 {
		resp.AIContext = previous
		return "preserved previous ai_context.json because Builder returned empty or invalid project memory"
	}
	resp.AIContext = returned
	return "sanitized ai_context.json project memory"
}

func aiContextSummaryText(ctx builderAIContext) string {
	ctx = normalizeAIContext(ctx)
	sections := []struct {
		Title string
		Items []string
	}{
		{"Terminology", ctx.Terminology},
		{"Architecture", ctx.Architecture},
		{"Prior Changes", ctx.PriorChanges},
		{"Known Issues", ctx.KnownIssues},
		{"Risks / Constraints", ctx.RisksConstraints},
	}
	parts := []string{}
	for _, section := range sections {
		if len(section.Items) == 0 {
			continue
		}
		parts = append(parts, section.Title+": "+strings.Join(section.Items, "; "))
	}
	return strings.Join(parts, "\n")
}

func normalizeBuilderReportStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "partial":
		return "partial"
	case "blocked":
		return "blocked"
	case "completed", "":
		return builderReportDefaultStatus
	default:
		return builderReportDefaultStatus
	}
}

func builderReportHasContent(report builderReport) bool {
	report = normalizeBuilderReport(report)
	return strings.TrimSpace(report.Summary) != "" || len(report.ChangedFiles) > 0 || len(report.IssuesFound) > 0 || len(report.Recommendations) > 0 || len(report.NextSteps) > 0
}

func normalizeBuilderReport(report builderReport) builderReport {
	return builderReport{
		Status:          normalizeBuilderReportStatus(report.Status),
		Summary:         strings.TrimSpace(report.Summary),
		ChangedFiles:    trimStringList(report.ChangedFiles, 24, 240),
		IssuesFound:     trimStringList(report.IssuesFound, 24, 240),
		Recommendations: trimStringList(report.Recommendations, 24, 240),
		NextSteps:       trimStringList(report.NextSteps, 24, 240),
	}
}

func updateBuilderOutputFromParsed(state *builderOutputState, parsed builderResponse, meta builderParseMeta) {
	if state == nil {
		return
	}
	state.Summary = strings.TrimSpace(parsed.Summary)
	state.Notes = strings.TrimSpace(parsed.Notes)
	state.Warnings = append([]string{}, parsed.Warnings...)
	normalizedAIContext := normalizeAIContext(parsed.AIContext)
	state.AIContextSummary = aiContextSummaryText(normalizedAIContext)
	state.AIContextRisks = append([]string{}, normalizedAIContext.RisksConstraints...)
	state.AIContextNext = []string{}
	state.BuilderReport = normalizeBuilderReport(parsed.BuilderReport)
	state.FileCount = len(parsed.Files)
	state.ArtifactCount = len(parsed.Artifacts)
	if state.UserFacingResponse == "" {
		state.UserFacingResponse = strings.TrimSpace(meta.UserFacingText)
	}
	if state.UserFacingResponse == "" {
		state.UserFacingResponse = strings.TrimSpace(parsed.Notes)
	}
}

func (a *App) clearRiskModeLocked() {
	a.riskModeEnabled = false
	a.riskIterationsTotal = 0
	a.riskIterationsRemain = 0
	a.riskOriginalPrompt = ""
	a.riskContextFiles = nil
	a.riskBuilderIDs = nil
	a.riskCurrentIteration = 0
	a.riskStatusTitle = ""
	a.riskStatusLines = nil
	a.riskStopReason = ""
	a.riskLastUpdated = ""
}

func (a *App) setActiveCancelLocked(modelID, projectName, executionID string, cancel context.CancelFunc) {
	if a.activeCancels == nil {
		a.activeCancels = map[string]activeCancelEntry{}
	}
	a.activeCancels[modelID] = activeCancelEntry{ExecutionID: strings.TrimSpace(executionID), ProjectName: strings.TrimSpace(projectName), Cancel: cancel}
}

func (a *App) clearActiveCancelLocked(modelID, executionID string) {
	entry, ok := a.activeCancels[modelID]
	if !ok {
		return
	}
	if strings.TrimSpace(executionID) != "" && strings.TrimSpace(entry.ExecutionID) != "" && strings.TrimSpace(entry.ExecutionID) != strings.TrimSpace(executionID) {
		return
	}
	delete(a.activeCancels, modelID)
}

func (a *App) cancelActiveCallsForProjectLocked(projectName, executionID string) int {
	projectName = strings.TrimSpace(projectName)
	executionID = strings.TrimSpace(executionID)
	count := 0
	for modelID, entry := range a.activeCancels {
		if projectName != "" && strings.TrimSpace(entry.ProjectName) != projectName {
			continue
		}
		if executionID != "" && strings.TrimSpace(entry.ExecutionID) != "" && strings.TrimSpace(entry.ExecutionID) != executionID {
			continue
		}
		if entry.Cancel != nil {
			entry.Cancel()
			count++
		}
		delete(a.activeCancels, modelID)
	}
	return count
}

func (a *App) isWaveExecutionCurrent(projectName, executionID string) bool {
	projectName = strings.TrimSpace(projectName)
	executionID = strings.TrimSpace(executionID)
	if projectName == "" || executionID == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.waveExecutionsByProject[projectName]
	if !ok {
		return false
	}
	return strings.TrimSpace(state.ExecutionID) == executionID
}

func waveProgressPosition(currentIndex, totalWaves int) int {
	if totalWaves <= 0 {
		return 0
	}
	position := currentIndex + 1
	if position < 1 {
		position = 1
	}
	if position > totalWaves {
		position = totalWaves
	}
	return position
}

func totalLoopCycles(loopCount int) int {
	if loopCount < 0 {
		return 1
	}
	return loopCount + 1
}

func populateWaveStatusProgress(state waveStatusState, execState waveExecutionState) waveStatusState {
	if state.CurrentWavePosition <= 0 {
		state.CurrentWavePosition = waveProgressPosition(execState.CurrentIndex, len(execState.Waves))
	}
	if state.TotalWaves <= 0 {
		state.TotalWaves = len(execState.Waves)
	}
	if state.CurrentLoop < 0 {
		state.CurrentLoop = 0
	}
	if state.CurrentLoop == 0 && execState.CycleNumber > 0 {
		state.CurrentLoop = execState.CycleNumber
	}
	if state.TotalLoops <= 0 {
		state.TotalLoops = totalLoopCycles(execState.LoopCount)
	}
	return state
}

func waveStatusFromExecution(projectName string, execState waveExecutionState, currentWave int, stateName, detail, promptSource string, contextFilesUsed int) waveStatusState {
	state := waveStatusState{
		ProjectName:      strings.TrimSpace(projectName),
		Visible:          true,
		CurrentWave:      currentWave,
		State:            strings.TrimSpace(stateName),
		Detail:           strings.TrimSpace(detail),
		PromptSource:     strings.TrimSpace(promptSource),
		ContextFilesUsed: contextFilesUsed,
	}
	return populateWaveStatusProgress(state, execState)
}

func waveProgressLabel(currentIndex, totalWaves int) string {
	position := waveProgressPosition(currentIndex, totalWaves)
	if position <= 0 || totalWaves <= 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", position, totalWaves)
}

func withWaveProgress(base string, currentIndex, totalWaves int) string {
	base = strings.TrimSpace(base)
	progress := waveProgressLabel(currentIndex, totalWaves)
	if progress == "" {
		return base
	}
	if base == "" {
		return progress
	}
	return fmt.Sprintf("%s · %s", base, progress)
}

func sanitizeRiskStatusLines(lines []string) []string {
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		clean = append(clean, line)
	}
	return clean
}

func shortRiskText(value string, limit int) string {
	value = strings.TrimSpace(firstLine(value))
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-1]) + "…"
}

func (a *App) setRiskStatusLocked(title string, lines ...string) {
	a.riskStatusTitle = strings.TrimSpace(title)
	a.riskStatusLines = sanitizeRiskStatusLines(lines)
	a.riskLastUpdated = time.Now().Format(time.RFC3339)
}

func (a *App) riskIterationDisplayLocked() int {
	if a.riskIterationsTotal <= 0 {
		return 0
	}
	current := a.riskIterationsTotal - a.riskIterationsRemain + 1
	if current < 1 {
		current = 1
	}
	if current > a.riskIterationsTotal {
		current = a.riskIterationsTotal
	}
	return current
}

func (a *App) stopRiskModeLocked(reason string, lines ...string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Risk Mode stopped."
	}
	cleanLines := sanitizeRiskStatusLines(lines)
	if len(cleanLines) == 0 || cleanLines[0] != reason {
		cleanLines = append([]string{reason}, cleanLines...)
	}
	a.riskModeEnabled = false
	a.riskIterationsRemain = 0
	a.riskCurrentIteration = 0
	a.riskBuilderIDs = nil
	a.riskStopReason = reason
	a.setRiskStatusLocked("RISK MODE ENDED", cleanLines...)
}

func (a *App) riskStateSnapshotLocked() riskStateResponse {
	currentIteration := a.riskCurrentIteration
	if currentIteration <= 0 && a.riskModeEnabled {
		currentIteration = a.riskIterationDisplayLocked()
	}
	return riskStateResponse{
		Enabled:             a.riskModeEnabled,
		IterationsRemaining: a.riskIterationsRemain,
		IterationsTotal:     a.riskIterationsTotal,
		OriginalPrompt:      a.riskOriginalPrompt,
		CurrentIteration:    currentIteration,
		StatusTitle:         a.riskStatusTitle,
		StatusLines:         append([]string(nil), a.riskStatusLines...),
		StopReason:          a.riskStopReason,
		ShowBubble:          a.riskModeEnabled || strings.TrimSpace(a.riskStatusTitle) != "" || len(a.riskStatusLines) > 0,
	}
}

func (a *App) stopRiskMode(reason string, lines ...string) {
	a.mu.Lock()
	a.stopRiskModeLocked(reason, lines...)
	a.mu.Unlock()
}
func firstLine(s string) string {
	parts := strings.Split(strings.TrimSpace(s), "\n")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (a *App) runReviewerEvaluation(projectName, prompt string, contextFiles []string, builders []ModelConfig, results []modelRunResult) (string, error) {
	reviewerID := a.getReviewerID()
	if reviewerID == "" {
		return "", nil
	}
	reviewer, ok := a.findModel(reviewerID)
	if !ok {
		return "", errors.New("reviewer model not found")
	}
	baseEntry := diagnosticsEntry{Mode: "observer", Target: "Observer", ModelID: modelIDString(reviewer.ID), ModelLabel: reviewer.Label, Project: projectName, Prompt: strings.TrimSpace(prompt)}
	baseEntry = a.decorateDiagnosticsWithCurrentWave(projectName, baseEntry)
	executionID := "reviewer-" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	if state, ok := a.currentWaveExecution(projectName); ok && strings.TrimSpace(state.ExecutionID) != "" {
		executionID = state.ExecutionID + "-reviewer"
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(reviewerID, projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(reviewerID, executionID)
		a.mu.Unlock()
	}()
	defer cancel()

	_, reviewerMeta, err := a.projectPaths(reviewer, projectName)
	if err != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(err.Error()))
		return "", err
	}
	messages, diagMeta, titleToID, mergeReadyTitles, err := a.buildReviewerPayload(projectName, prompt, contextFiles, builders, results)
	baseEntry.Files = diagMeta.Files
	baseEntry.Candidates = diagMeta.Candidates
	baseEntry.ReviewInputs = diagMeta.ReviewInputs
	baseEntry.StatusMessage = strings.TrimSpace(diagMeta.StatusMessage)
	if err != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(err.Error()).withStatusMessage(baseEntry.StatusMessage))
		return "", err
	}
	requestPayload, err := buildReviewerRequestPayload(a.cfg, reviewer, messages)
	if err != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(err.Error()).withStatusMessage(baseEntry.StatusMessage))
		return "", err
	}
	baseEntry.SystemPrompt = strings.TrimSpace(requestPayload.Instructions)
	a.publishDiagnostics(baseEntry.withStage("Assembled").withStatusMessage(baseEntry.StatusMessage))
	a.publishDiagnostics(baseEntry.withStage("Sent").withStatusMessage(baseEntry.StatusMessage))

	responseText, err := a.executeAdapterText(ctx, reviewer, requestPayload)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, context.Canceled) {
			reason = "request canceled"
		}
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(reason))
		return "", err
	}
	responseText = strings.TrimSpace(responseText)
	a.publishDiagnostics(baseEntry.withStage("Response Received").withResponse(responseText))
	timestamp := time.Now().Format("20060102_150405")
	reviewsDir := reviewerReviewsDir(reviewerMeta)
	if err := os.MkdirAll(reviewsDir, 0o755); err != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
		return "", err
	}
	rawFile := reviewerRawResponsePath(reviewerMeta, timestamp)
	_ = os.WriteFile(rawFile, []byte(responseText), 0o644)
	if err := trimReviewerHistory(reviewerMeta, a.cfg.MaxResponseHistory); err != nil {
		a.logf(modelIDString(reviewer.ID), "warn", "Failed trimming reviewer history: %v", err)
	}
	parsed, err := parseReviewerResponse(responseText)
	if err != nil {
		reason := fmt.Sprintf("invalid reviewer response: %v", err)
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(reason))
		return "", fmt.Errorf("invalid reviewer response: %w", err)
	}
	normalizeReviewerResponseNames(&parsed, titleToID)
	validationErr := validateReviewerResponse(parsed, titleToID, mergeReadyTitles)
	if validationErr == nil {
		encoded, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
			return "", err
		}
		encoded = append(encoded, '\n')
		archivePath := reviewerArchivePath(reviewerMeta, timestamp)
		if err := atomicWriteFile(archivePath, encoded, 0o644); err != nil {
			a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
			return "", err
		}
		if err := atomicWriteFile(reviewerLatestPath(reviewerMeta), encoded, 0o644); err != nil {
			a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
			return "", err
		}
		if err := trimReviewerHistory(reviewerMeta, a.cfg.MaxResponseHistory); err != nil {
			a.logf(modelIDString(reviewer.ID), "warn", "Failed trimming reviewer history: %v", err)
		}
	}
	state := buildReviewerOutputStateFromParsed(reviewer, projectName, responseText, parsed, titleToID, mergeReadyTitles)
	for _, item := range parsed.Models {
		agentMergeReady := mergeReadyTitles[item.Model]
		if item.MergeReady != agentMergeReady {
			a.logf(modelIDString(reviewer.ID), "warn", "Observer merge_ready mismatch for %s: reviewer=%t agentgo=%t", item.Model, item.MergeReady, agentMergeReady)
		}
	}
	if validationErr != nil {
		state = buildIncompleteReviewerOutputState(reviewer, projectName, responseText, parsed, titleToID, mergeReadyTitles, validationErr)
		if err := writeReviewerOutputState(reviewerMeta, state); err != nil {
			a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
			return "", err
		}
		a.publishDiagnostics(baseEntry.withStage("Parsed With Warnings").withResponse(responseText).withStatusMessage(strings.TrimSpace(state.FallbackNote)))
		return "", nil
	}
	recommendedID := state.RecommendedModelID
	if strings.TrimSpace(recommendedID) == "" {
		reason := fmt.Sprintf("reviewer recommended unknown candidate %q", parsed.RecommendedCandidate)
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(reason))
		return "", fmt.Errorf(reason)
	}
	if err := writeReviewerOutputState(reviewerMeta, state); err != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responseText).withReason(err.Error()))
		return "", err
	}
	a.publishDiagnostics(baseEntry.withStage("Parsed").withStatusMessage(fmt.Sprintf("Recommended merge candidate: %s", parsed.RecommendedCandidate)))
	return recommendedID, nil
}

func pruneResponseArchive(root string, limit int, prefix string, suffix string, keep map[string]bool) error {
	if limit <= 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	type archiveFile struct {
		name string
		mod  time.Time
	}
	files := []archiveFile{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if keep != nil && keep[name] {
			continue
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if suffix != "" && !strings.HasSuffix(strings.ToLower(name), strings.ToLower(suffix)) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, archiveFile{name: name, mod: info.ModTime()})
	}
	if len(files) <= limit {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod.Equal(files[j].mod) {
			return files[i].name > files[j].name
		}
		return files[i].mod.After(files[j].mod)
	})
	for _, file := range files[limit:] {
		if err := os.Remove(filepath.Join(root, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func trimReviewerHistory(metaRoot string, limit int) error {
	reviewsDir := reviewerReviewsDir(metaRoot)
	if err := pruneResponseArchive(reviewsDir, limit, "reviewer_", ".json", map[string]bool{"reviewer_latest.json": true}); err != nil {
		return err
	}
	return pruneResponseArchive(reviewsDir, limit, "response_", ".md", nil)
}

func (a *App) buildReviewerPayload(projectName, prompt string, contextFiles []string, builders []ModelConfig, results []modelRunResult) ([]adapters.Message, reviewerDiagnosticsMeta, map[string]string, map[string]bool, error) {
	type reviewCandidate struct {
		Model              string          `json:"model"`
		MergeReady         bool            `json:"merge_ready"`
		PendingCount       int             `json:"pending_count"`
		ResultStatus       string          `json:"result_status"`
		StatusMessage      string          `json:"status_message,omitempty"`
		UserFacingResponse string          `json:"user_facing_response,omitempty"`
		RawResponse        string          `json:"raw_response,omitempty"`
		Summary            string          `json:"summary,omitempty"`
		Notes              string          `json:"notes,omitempty"`
		Warnings           []string        `json:"warnings,omitempty"`
		AIContext          string          `json:"ai_context,omitempty"`
		Diffs              []string        `json:"diffs"`
		ExecuteOutput      json.RawMessage `json:"execute_output,omitempty"`
	}
	type reviewPackage struct {
		ReviewMode         string            `json:"review_mode"`
		CurrentPrompt      string            `json:"current_prompt"`
		BaselineBundleNote string            `json:"baseline_bundle_note,omitempty"`
		ReviewerContext    json.RawMessage   `json:"reviewer_context,omitempty"`
		Candidates         []reviewCandidate `json:"candidates"`
	}

	diagMeta := reviewerDiagnosticsMeta{Files: []diagnosticsFileRef{}, ReviewInputs: []string{}, Candidates: []string{}}
	payload := reviewPackage{ReviewMode: "observer_compare_and_recommend", CurrentPrompt: strings.TrimSpace(prompt)}
	messages := []adapters.Message{}
	reviewerID := a.getReviewerID()
	reviewerTransportProfile := adapters.ResolveTransportProfile(adapters.ModelConfig{})
	if reviewerID != "" {
		if reviewer, ok := a.findModel(reviewerID); ok {
			reviewerTransportProfile = adapters.ResolveTransportProfile(toAdapterModelConfig(reviewer))
			if reviewerContextPath := filepath.ToSlash(filepath.Join(a.relativeMetaPath(reviewer, projectName), "reviewer_context.json")); reviewerContextPath != "" {
				if data, err := os.ReadFile(filepath.Join(a.cfg.WorkRoot, reviewerContextPath)); err == nil {
					trimmed := bytes.TrimSpace(data)
					if len(trimmed) > 0 && json.Valid(trimmed) && string(trimmed) != "{}" {
						payload.ReviewerContext = json.RawMessage(trimmed)
						diagMeta.Files = appendUniqueDiagnosticsFile(diagMeta.Files, makeDiagnosticsFileRef(reviewerContextPath))
						diagMeta.ReviewInputs = append(diagMeta.ReviewInputs, "Reviewer context included.")
					}
				}
			}
		}
	}

	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err == nil {
		baselineMessage, baselineReport, bundleErr := buildMultimodalContextMessage(projectworkRoot, contextFiles, "BASELINE PROJECTWORK CONTEXT:", reviewerTransportProfile, true, reviewerBaselineMaxTextBytes)
		if bundleErr != nil {
			return nil, reviewerDiagnosticsMeta{}, nil, nil, bundleErr
		}
		payload.BaselineBundleNote = "See the attached baseline projectwork bundle and file manifest in the next observer message."
		messages = append(messages, baselineMessage)
		diagMeta.Files = append(diagMeta.Files, a.projectContextDiagnosticsFiles(projectName, baselineReport.UsedPaths)...)
		diagMeta.ReviewInputs = append(diagMeta.ReviewInputs, "Baseline bundle: "+formatMultimodalReport(baselineReport))
	}

	resultByID := map[string]modelRunResult{}
	for _, result := range results {
		resultByID[result.ModelID] = result
	}
	titleToID := map[string]string{}
	mergeReadyTitles := map[string]bool{}
	mergeReadyCount := 0
	candidateBundleCount := 0
	for _, model := range builders {
		modelID := modelIDString(model.ID)
		result := resultByID[modelID]
		diffs := []string{}
		changedPaths := []string{}
		if diffRows, diffErr := a.buildModelDiff(model); diffErr == nil {
			for _, df := range diffRows {
				diffs = append(diffs, fmt.Sprintf("%s %s", df.Status, df.Path))
				changedPaths = append(changedPaths, df.Path)
			}
		}
		projectRoot, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return nil, reviewerDiagnosticsMeta{}, nil, nil, err
		}
		aiCtx, _ := os.ReadFile(filepath.Join(metaRoot, "ai_context.json"))
		builderState, _ := readBuilderOutputState(metaRoot)
		status := "processed"
		if result.Err != nil {
			status = result.Err.Error()
		} else if strings.TrimSpace(builderState.Kind) != "" {
			status = builderState.Kind
		}
		var executeOutput json.RawMessage
		if builderState.HasResponse {
			if b, err := json.Marshal(builderState); err == nil {
				executeOutput = json.RawMessage(b)
			}
		}
		payload.Candidates = append(payload.Candidates, reviewCandidate{
			Model:              model.Label,
			MergeReady:         result.PendingCount > 0,
			PendingCount:       result.PendingCount,
			ResultStatus:       status,
			StatusMessage:      strings.TrimSpace(builderState.StatusMessage),
			UserFacingResponse: strings.TrimSpace(builderState.UserFacingResponse),
			RawResponse:        strings.TrimSpace(builderState.RawResponse),
			Summary:            strings.TrimSpace(builderState.Summary),
			Notes:              strings.TrimSpace(builderState.Notes),
			Warnings:           append([]string{}, builderState.Warnings...),
			AIContext:          strings.TrimSpace(string(aiCtx)),
			Diffs:              diffs,
			ExecuteOutput:      executeOutput,
		})
		bundleHeading := fmt.Sprintf("CANDIDATE BUNDLE: %s\nMerge ready: %t\nPending changed files: %d\nChanged candidate files and image attachments follow with a manifest below.", model.Label, result.PendingCount > 0, len(changedPaths))
		candidateMessage, candidateReport, bundleErr := buildMultimodalContextMessage(projectRoot, changedPaths, bundleHeading, reviewerTransportProfile, true, reviewerCandidateMaxTextBytes)
		if bundleErr != nil {
			return nil, reviewerDiagnosticsMeta{}, nil, nil, bundleErr
		}
		messages = append(messages, candidateMessage)
		candidateBundleCount++
		diagMeta.Files = append(diagMeta.Files, a.builderDiagnosticsFiles(projectName, model, changedPaths)...)
		diagMeta.ReviewInputs = append(diagMeta.ReviewInputs, fmt.Sprintf("%s bundle: %s", model.Label, formatMultimodalReport(candidateReport)))
		titleToID[model.Label] = modelID
		diagMeta.Candidates = append(diagMeta.Candidates, model.Label)
		if result.PendingCount > 0 {
			mergeReadyTitles[model.Label] = true
			mergeReadyCount++
		}
	}
	if len(diagMeta.Candidates) > 0 {
		diagMeta.ReviewInputs = append(diagMeta.ReviewInputs, fmt.Sprintf("Evaluating: %s", strings.Join(diagMeta.Candidates, ", ")))
	}
	diagMeta.ReviewInputs = append(diagMeta.ReviewInputs,
		fmt.Sprintf("Builder outputs included: %d", len(payload.Candidates)),
		"Builder AI context included for each candidate.",
	)
	if mergeReadyCount > 0 {
		diagMeta.ReviewInputs = append(diagMeta.ReviewInputs, fmt.Sprintf("Diff summaries included for %d merge-ready candidate(s).", mergeReadyCount))
	}
	diagMeta.StatusMessage = fmt.Sprintf("Baseline bundle plus %d candidate bundle(s); observer effective inputs=%s", candidateBundleCount, formatCapabilityKinds(reviewerTransportProfile.EffectiveCapabilities))
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, reviewerDiagnosticsMeta{}, nil, nil, err
	}
	messages = append([]adapters.Message{buildTextMessage("user", "REVIEW PACKAGE:\n"+string(encoded))}, messages...)
	return messages, diagMeta, titleToID, mergeReadyTitles, nil
}

func (a *App) mergeModelIntoProjectwork(modelID string, files []string) (int, error) {
	copied, _, err := a.mergeModelIntoProjectworkDetailed(modelID, files)
	return copied, err
}

func (a *App) mergeModelIntoProjectworkDetailed(modelID string, files []string) (int, mergeSummary, error) {
	model, ok := a.findModel(modelID)
	if !ok {
		return 0, mergeSummary{}, errors.New("unknown model")
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		return 0, mergeSummary{}, err
	}
	summary, lastMergedFiles, copied, err := a.applyMergeToProjectwork(model, projectName, files)
	if err != nil {
		return 0, mergeSummary{}, err
	}
	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		return copied, summary, err
	}
	if propagated, err := a.propagateMergedAIContextToBuilders(projectName, model); err != nil {
		a.logf(modelID, "warn", "Could not propagate merge winner ai_context.json after merge: %v", err)
	} else if propagated > 0 {
		a.logf(modelID, "info", "Propagated merge winner ai_context.json to %d Builder model(s) for project %s", propagated, projectName)
	}
	a.setLastMergeState(projectName, lastMergedFiles, summary.Files.Deleted, summary)
	a.refreshCypherAfterProjectworkMerge(projectName, summary)
	a.clearPendingMergeState(projectName)
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(projectName); clearErr != nil {
		a.logf(modelID, "warn", "Failed clearing Builder response cards after merge: %v", clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf(modelID, "info", "Cleared %d Builder response card(s) after merge for project %s", clearedBuilderResponses, projectName)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(projectName); clearErr != nil {
		a.logf(modelID, "warn", "Failed clearing Observer report(s) after merge: %v", clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf(modelID, "info", "Cleared %d Observer report(s) after merge for project %s", clearedReviewerReports, projectName)
	}
	return copied, summary, nil
}

func (a *App) refreshCypherAfterProjectworkMerge(projectName string, summary mergeSummary) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return
	}
	changed := len(summary.Files.Added) + len(summary.Files.Modified) + len(summary.Files.Deleted)
	if changed == 0 {
		return
	}
	projectRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		a.logf("system", "warn", "Could not refresh Cypher after merge for %s: %v", projectName, err)
		return
	}
	manifestPath := filepath.Join(projectRoot, cypherManifestFileName)
	previous, exists, err := readCypherManifest(manifestPath)
	if err != nil {
		a.logf("system", "warn", "Could not read Cypher after merge for %s: %v", projectName, err)
		return
	}
	if !exists {
		return
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		a.logf("system", "warn", "Could not refresh Cypher after merge for %s: %v", projectName, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	manifest, err := a.buildCypherManifest(ctx, projectName, projectRoot, projectworkRoot, previous, true)
	if err != nil {
		a.logf("system", "warn", "Could not rebuild Cypher after merge for %s: %v", projectName, err)
		return
	}
	if err := writeCypherManifest(manifestPath, manifest); err != nil {
		a.logf("system", "warn", "Could not save refreshed Cypher after merge for %s: %v", projectName, err)
		return
	}
	a.logf("system", "info", "Cypher refreshed after merge for project %s; changed summaries are marked stale for future enrichment.", projectName)
}

func (a *App) applyMergeToProjectwork(model ModelConfig, projectName string, files []string) (mergeSummary, []string, int, error) {
	src, _, err := a.projectPaths(model, projectName)
	if err != nil {
		return mergeSummary{}, nil, 0, err
	}
	dst, err := a.projectWorkRoot(projectName)
	if err != nil {
		return mergeSummary{}, nil, 0, err
	}
	diffFiles, err := buildDiffFilesForRoots(model, projectName, src, dst)
	if err != nil {
		return mergeSummary{}, nil, 0, err
	}
	selected := map[string]bool{}
	if len(files) == 0 {
		for _, df := range diffFiles {
			selected[df.Path] = true
		}
	} else {
		for _, rel := range normalizeRelativePaths(files) {
			selected[rel] = true
		}
	}
	summary := mergeSummary{
		Type:        "merge_summary",
		SourceModel: modelIDString(model.ID),
		MergeMode:   "selective",
		Files: mergeSummaryFiles{
			Added:    []string{},
			Modified: []string{},
			Deleted:  []string{},
			Skipped:  []string{},
		},
		PostMerge: mergeSummaryPost{
			ProjectworkUpdated:            true,
			ActiveBuilderProjectsResynced: true,
		},
		Instruction: "Use /projects/<project>/projectwork as the source of truth. Only the listed added, modified, and deleted files were kept. Active Builder workspaces are synchronized after merge; inactive models sync when activated.",
	}
	lastMergedFiles := []string{}
	seenLast := map[string]bool{}
	for _, df := range diffFiles {
		if selected[df.Path] {
			switch df.Status {
			case "added":
				summary.Files.Added = append(summary.Files.Added, df.Path)
				if !seenLast[df.Path] {
					lastMergedFiles = append(lastMergedFiles, df.Path)
					seenLast[df.Path] = true
				}
			case "modified":
				summary.Files.Modified = append(summary.Files.Modified, df.Path)
				if !seenLast[df.Path] {
					lastMergedFiles = append(lastMergedFiles, df.Path)
					seenLast[df.Path] = true
				}
			case "deleted":
				summary.Files.Deleted = append(summary.Files.Deleted, df.Path)
			}
		} else {
			summary.Files.Skipped = append(summary.Files.Skipped, df.Path)
		}
	}
	if len(files) == 0 {
		summary.MergeMode = "full"
	}
	var copied int
	if len(files) == 0 {
		copied, err = syncDirContents(src, dst)
	} else {
		copied, err = syncSelectedFiles(src, dst, files)
	}
	if err != nil {
		return mergeSummary{}, nil, 0, err
	}
	return summary, lastMergedFiles, copied, nil
}

func (a *App) setLastMergedFiles(projectName string, files []string) {
	a.setLastMergeState(projectName, files, nil, mergeSummary{})
}

func (a *App) setLastMergeState(projectName string, files []string, deleted []string, summary mergeSummary) {
	projectName = strings.TrimSpace(projectName)
	clean := append([]string(nil), normalizeRelativePaths(files)...)
	cleanDeleted := append([]string(nil), normalizeRelativePaths(deleted)...)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(clean) == 0 {
		delete(a.lastMergedFilesByProject, projectName)
	} else {
		a.lastMergedFilesByProject[projectName] = clean
	}
	if len(cleanDeleted) == 0 {
		delete(a.lastMergedDeletesByProject, projectName)
	} else {
		a.lastMergedDeletesByProject[projectName] = cleanDeleted
	}
	if strings.TrimSpace(summary.Type) == "" {
		delete(a.lastMergeSummaryByProject, projectName)
	} else {
		a.lastMergeSummaryByProject[projectName] = summary
	}
}

func (a *App) clearLastMergedFiles(projectName string) {
	projectName = strings.TrimSpace(projectName)
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.lastMergedFilesByProject, projectName)
	delete(a.lastMergedDeletesByProject, projectName)
	delete(a.lastMergeSummaryByProject, projectName)
}

func (a *App) clearAllLastMergedFiles() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastMergedFilesByProject = map[string][]string{}
	a.lastMergedDeletesByProject = map[string][]string{}
	a.lastMergeSummaryByProject = map[string]mergeSummary{}
}

func (a *App) currentLastMergedFiles(projectName string) []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.lastMergedFilesByProject[strings.TrimSpace(projectName)]...)
}

func (a *App) currentLastMergedDeletedFiles(projectName string) []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.lastMergedDeletesByProject[strings.TrimSpace(projectName)]...)
}

func (a *App) currentLastMergeSummary(projectName string) mergeSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastMergeSummaryByProject[strings.TrimSpace(projectName)]
}

func (a *App) setPendingMergeCount(projectName, modelID string, count int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if count <= 0 {
		if a.pendingMergeCountsByProject[projectName] != nil {
			delete(a.pendingMergeCountsByProject[projectName], modelID)
			if len(a.pendingMergeCountsByProject[projectName]) == 0 {
				delete(a.pendingMergeCountsByProject, projectName)
			}
		}
		return
	}
	if a.pendingMergeCountsByProject[projectName] == nil {
		a.pendingMergeCountsByProject[projectName] = map[string]int{}
	}
	a.pendingMergeCountsByProject[projectName][modelID] = count
}

func (a *App) clearPendingMergeCount(projectName, modelID string) {
	a.setPendingMergeCount(projectName, modelID, 0)
}

func (a *App) clearPendingMergeState(projectName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if projectName == "" {
		a.pendingMergeCountsByProject = map[string]map[string]int{}
		return
	}
	delete(a.pendingMergeCountsByProject, projectName)
}

func (a *App) pendingMergeCount(projectName, modelID string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.pendingMergeCountsByProject[projectName] == nil {
		return 0
	}
	return a.pendingMergeCountsByProject[projectName][modelID]
}

func (a *App) pendingMergeTotal(projectName string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	total := 0
	for _, count := range a.pendingMergeCountsByProject[projectName] {
		if count > 0 {
			total++
		}
	}
	return total
}

func (a *App) handleRiskContinuation(projectName, recommendedID string) {
	a.mu.RLock()
	if !a.riskModeEnabled || a.riskIterationsRemain <= 0 {
		a.mu.RUnlock()
		return
	}
	currentIteration := a.riskCurrentIteration
	if currentIteration <= 0 {
		currentIteration = a.riskIterationDisplayLocked()
	}
	totalIterations := a.riskIterationsTotal
	builderIDs := append([]string(nil), a.riskBuilderIDs...)
	waveState, hasWaveState := a.waveExecutionsByProject[strings.TrimSpace(projectName)]
	a.mu.RUnlock()

	recommendedModel, ok := a.findModel(recommendedID)
	if !ok {
		reason := "Observer recommended an unknown Builder."
		a.logRiskf("system", "error", "RISK %d/%d: %s (%s)", currentIteration, totalIterations, reason, recommendedID)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), recommendedID)
		return
	}
	recommendedLabel := recommendedModel.Label
	if a.pendingMergeCount(projectName, recommendedID) <= 0 {
		reason := fmt.Sprintf("Observer recommended %s, but no mergeable output is available.", recommendedLabel)
		a.logRiskf("system", "error", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), "Risk Mode returned to normal mode.")
		return
	}

	a.mu.Lock()
	if a.riskModeEnabled {
		a.setRiskStatusLocked("RISK MODE",
			fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations),
			fmt.Sprintf("Observer selected: %s", recommendedLabel),
			"Applying recommended merge.",
		)
	}
	a.mu.Unlock()

	if _, err := a.mergeModelIntoProjectwork(recommendedID, nil); err != nil {
		reason := fmt.Sprintf("Automatic merge failed for %s.", recommendedLabel)
		a.logRiskf("system", "error", "RISK %d/%d: %s %v", currentIteration, totalIterations, reason, err)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), err.Error())
		return
	}
	a.logRiskf("system", "warn", "RISK %d/%d: Merged %s", currentIteration, totalIterations, recommendedLabel)
	a.clearReviewerReportForProject(projectName)

	if hasWaveState && waveState.CurrentIndex+1 < len(waveState.Waves) {
		nextWave, started, err := a.continueWaveExecutionAfterMerge(projectName)
		if err != nil {
			reason := "Could not launch the next populated wave."
			a.logRiskf("system", "error", "RISK %d/%d: %s %v", currentIteration, totalIterations, reason, err)
			a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), err.Error())
			return
		}
		if started {
			a.mu.Lock()
			if a.riskModeEnabled {
				a.setRiskStatusLocked("RISK MODE",
					fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations),
					fmt.Sprintf("Merged: %s", recommendedLabel),
					fmt.Sprintf("Launching wave %d", nextWave.Number),
				)
			}
			a.mu.Unlock()
			a.logRiskf("system", "warn", "RISK %d/%d: Launching next populated wave %d", currentIteration, totalIterations, nextWave.Number)
			return
		}
	}

	remaining := 0
	a.mu.Lock()
	if !a.riskModeEnabled || a.riskIterationsRemain <= 0 {
		a.mu.Unlock()
		return
	}
	a.riskIterationsRemain--
	remaining = a.riskIterationsRemain
	a.mu.Unlock()

	if remaining <= 0 {
		reason := "Completed all requested Risk Mode iterations."
		a.setWaveStatus(projectName, waveStatusFromExecution(projectName, waveState, waveState.CurrentWave, "complete", withWaveProgress("Complete", waveState.CurrentIndex, len(waveState.Waves)), "", 0))
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.mu.Unlock()
		a.logf("system", "info", "Preserved ai_context.json and reviewer_context.json after Risk Mode completed for project %s", projectName)
		a.finalizeActiveOutfitRunCompleted(projectName, recommendedID, recommendedLabel, fmt.Sprintf("Completed after Risk Mode selected %s.", recommendedLabel))
		a.logRiskf("system", "warn", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), fmt.Sprintf("Last merge: %s", recommendedLabel))
		return
	}

	nextPrompt, err := a.readReviewerNextPrompt(projectName)
	if err != nil || strings.TrimSpace(nextPrompt) == "" {
		reason := "Observer did not provide a recommended next prompt."
		if err != nil {
			a.logRiskf("system", "warn", "RISK %d/%d: %s %v", currentIteration, totalIterations, reason, err)
		} else {
			a.logRiskf("system", "warn", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
		}
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), fmt.Sprintf("Last merge: %s", recommendedLabel))
		return
	}

	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		reason := "Could not prepare Builder workspaces for the next Risk Mode iteration."
		a.logRiskf("system", "error", "RISK %d/%d: %s %v", currentIteration, totalIterations, reason, err)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), err.Error())
		return
	}

	reviewerID := a.getReviewerID()
	builders := make([]ModelConfig, 0, len(builderIDs))
	if len(builderIDs) > 0 {
		for _, modelID := range builderIDs {
			if modelID == reviewerID {
				continue
			}
			if model, ok := a.findModel(modelID); ok {
				builders = append(builders, model)
			}
		}
	}
	if len(builders) == 0 {
		reason := "No active Builder set is available for the next Risk Mode iteration."
		a.logRiskf("system", "error", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), fmt.Sprintf("Last merge: %s", recommendedLabel))
		return
	}
	waves := buildExecutionWaves(builders)
	if len(waves) == 0 {
		reason := "No populated Builder waves are available for the next Risk Mode iteration."
		a.logRiskf("system", "error", "RISK %d/%d: %s", currentIteration, totalIterations, reason)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", currentIteration, totalIterations), fmt.Sprintf("Last merge: %s", recommendedLabel))
		return
	}

	nextIteration := currentIteration + 1
	seedWavePrompts := map[int]string{waves[0].Number: strings.TrimSpace(nextPrompt)}
	state := waveExecutionState{
		ProjectName:      projectName,
		ExecutionID:      fmt.Sprintf("%s-%d", projectName, time.Now().UTC().UnixNano()),
		RootPrompt:       strings.TrimSpace(nextPrompt),
		ContextFiles:     nil,
		WavePrompts:      seedWavePrompts,
		WaveContextFiles: map[int][]string{},
		Waves:            waves,
		CurrentIndex:     0,
		CurrentWave:      waves[0].Number,
		AwaitingMerge:    false,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	a.mu.Lock()
	if a.riskModeEnabled {
		a.riskCurrentIteration = nextIteration
		a.setWaveExecutionLocked(projectName, state)
		a.setRiskStatusLocked("RISK MODE",
			fmt.Sprintf("Iteration %d / %d", nextIteration, totalIterations),
			fmt.Sprintf("Merged: %s", recommendedLabel),
			fmt.Sprintf("Next prompt: %s", shortRiskText(nextPrompt, 110)),
			fmt.Sprintf("Launching wave %d.", waves[0].Number),
		)
	}
	a.mu.Unlock()
	a.logRiskf("system", "warn", "RISK %d/%d: Next prompt selected: %s", currentIteration, totalIterations, shortRiskText(nextPrompt, 140))
	if err := a.launchWaveExecution(projectName, state); err != nil {
		reason := "Could not launch the first wave of the next Risk Mode iteration."
		a.logRiskf("system", "error", "RISK %d/%d: %s %v", nextIteration, totalIterations, reason, err)
		a.stopRiskMode(reason, fmt.Sprintf("Iteration %d / %d", nextIteration, totalIterations), err.Error())
		return
	}
}

type adapterRequestPayload struct {
	Instructions string
	Messages     []adapters.Message
	ExpectJSON   bool
	JSONSchema   map[string]any
}

// executeAdapterText sends one assembled AgentGO request through the selected backend adapter.
func (a *App) executeAdapterResponse(ctx context.Context, model ModelConfig, payload adapterRequestPayload) (adapters.Response, error) {
	resp, err := adapters.Execute(ctx, toAdapterModelConfig(model), adapters.Request{
		Instructions: payload.Instructions,
		Messages:     payload.Messages,
		ExpectJSON:   payload.ExpectJSON,
		JSONSchema:   cloneAnyMap(payload.JSONSchema),
	})
	if err != nil {
		return adapters.Response{}, err
	}
	resp.Text = strings.TrimSpace(resp.Text)
	a.addSessionTokenUsage(estimateAdapterPayloadTokens(payload), estimateTextTokens(resp.Text))
	return resp, nil
}

func (a *App) executeAdapterText(ctx context.Context, model ModelConfig, payload adapterRequestPayload) (string, error) {
	resp, err := a.executeAdapterResponse(ctx, model, payload)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func formatBuilderDiagnosticsResponse(resp adapters.Response) string {
	text := strings.TrimSpace(resp.Text)
	if text != "" {
		return text
	}
	if len(resp.FileData) == 0 {
		return ""
	}
	name := strings.TrimSpace(resp.FileName)
	if name == "" {
		name = defaultBuilderBinaryFilename(resp.FileMIMEType)
	}
	mimeType := detectBuilderBinaryMIMEType(resp.FileMIMEType, name, resp.FileData)
	return fmt.Sprintf("Provider returned binary output: %s (%s, %d bytes)", filepath.ToSlash(name), mimeType, len(resp.FileData))
}

func formatBuilderRawResponseDocument(resp adapters.Response) string {
	text := strings.TrimSpace(resp.Text)
	if text != "" {
		return text
	}
	if len(resp.FileData) == 0 {
		return ""
	}
	name := strings.TrimSpace(resp.FileName)
	if name == "" {
		name = defaultBuilderBinaryFilename(resp.FileMIMEType)
	}
	mimeType := detectBuilderBinaryMIMEType(resp.FileMIMEType, name, resp.FileData)
	return strings.TrimSpace(fmt.Sprintf(`# Builder Adapter Response

Provider returned binary output directly.

- Filename: %q
- MIME type: %q
- Size: %d bytes
- Text response: none returned
`, filepath.ToSlash(name), mimeType, len(resp.FileData)))
}

func parseBuilderAdapterResponse(resp adapters.Response, projectRoot string) (builderResponse, builderParseMeta, error) {
	responseText := strings.TrimSpace(resp.Text)
	parsed, meta, err := parseBuilderResponse(responseText)
	if err == nil {
		return parsed, meta, nil
	}
	if len(resp.FileData) == 0 {
		return builderResponse{}, builderParseMeta{}, err
	}
	synthetic, synthMeta, synthErr := synthesizeBuilderBinaryResponse(resp, projectRoot)
	if synthErr != nil {
		return builderResponse{}, builderParseMeta{}, fmt.Errorf("%v; binary fallback failed: %w", err, synthErr)
	}
	return synthetic, synthMeta, nil
}

func synthesizeBuilderBinaryResponse(resp adapters.Response, projectRoot string) (builderResponse, builderParseMeta, error) {
	if len(resp.FileData) == 0 {
		return builderResponse{}, builderParseMeta{}, errors.New("missing binary file data")
	}
	relPath, usedProvidedName, err := chooseBuilderBinaryOutputPath(projectRoot, resp.FileName, resp.FileMIMEType)
	if err != nil {
		return builderResponse{}, builderParseMeta{}, err
	}
	target, err := safeJoin(projectRoot, relPath)
	if err != nil {
		return builderResponse{}, builderParseMeta{}, err
	}
	if err := rejectSymlinkPath(projectRoot, target); err != nil {
		return builderResponse{}, builderParseMeta{}, err
	}
	action := "create"
	if info, statErr := os.Stat(target); statErr == nil && !info.IsDir() {
		action = "overwrite"
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return builderResponse{}, builderParseMeta{}, statErr
	}
	mimeType := detectBuilderBinaryMIMEType(resp.FileMIMEType, relPath, resp.FileData)
	artifactID := "adapter_returned_file"
	note := fmt.Sprintf("AgentGO accepted a direct binary file returned by the provider and saved it as %s.", filepath.ToSlash(relPath))
	warnings := []string{}
	if !usedProvidedName && strings.TrimSpace(resp.FileName) != "" {
		warnings = append(warnings, "Provider filename was unsafe or unusable, so AgentGO assigned a safe output name.")
	}
	return builderResponse{
		AgentGOTool: agentGOToolBuilder,
		ToolVersion: agentGOToolVersion,
		Summary:     fmt.Sprintf("Saved returned binary file to %s.", filepath.ToSlash(relPath)),
		Files: []builderFileOp{{
			Path:        filepath.ToSlash(relPath),
			Action:      action,
			ArtifactRef: artifactID,
		}},
		Artifacts: []builderArtifact{{
			ID:       artifactID,
			Encoding: "base64",
			MIMEType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(resp.FileData),
		}},
		AIContext: builderAIContext{
			AgentGOFile:      aiContextFileIdentity,
			FileVersion:      agentGOToolVersion,
			Terminology:      []string{},
			Architecture:     []string{},
			PriorChanges:     []string{fmt.Sprintf("The provider returned a direct binary artifact saved at %s.", filepath.ToSlash(relPath))},
			KnownIssues:      []string{},
			RisksConstraints: []string{},
		},
		Notes:    note,
		Warnings: warnings,
	}, builderParseMeta{UserFacingText: note}, nil
}

func chooseBuilderBinaryOutputPath(projectRoot, name, mimeType string) (string, bool, error) {
	cleanName := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if cleanName != "" {
		cleanName = path.Clean(cleanName)
		if cleanName != "." && cleanName != "/" && cleanName != ".." && !strings.HasPrefix(cleanName, "../") && !path.IsAbs(cleanName) {
			if filepath.Ext(cleanName) == "" {
				if ext := preferredBuilderBinaryExtension(mimeType); ext != "" {
					cleanName += ext
				}
			}
			if target, err := safeJoin(projectRoot, cleanName); err == nil {
				if err := rejectSymlinkPath(projectRoot, target); err == nil {
					return filepath.ToSlash(cleanName), true, nil
				}
			}
		}
	}
	fallback := defaultBuilderBinaryFilename(mimeType)
	target, err := safeJoin(projectRoot, fallback)
	if err != nil {
		return "", false, err
	}
	if err := rejectSymlinkPath(projectRoot, target); err != nil {
		return "", false, err
	}
	return filepath.ToSlash(fallback), false, nil
}

func defaultBuilderBinaryFilename(mimeType string) string {
	baseType := normalizedBuilderMIMEType(mimeType)
	base := "returned_file"
	switch {
	case strings.HasPrefix(baseType, "image/"):
		base = "returned_image"
	case strings.HasPrefix(baseType, "audio/"):
		base = "returned_audio"
	case strings.HasPrefix(baseType, "video/"):
		base = "returned_video"
	}
	ext := preferredBuilderBinaryExtension(baseType)
	if ext == "" {
		ext = ".bin"
	}
	return sanitizeImportedFilename(base + ext)
}

func preferredBuilderBinaryExtension(mimeType string) string {
	baseType := normalizedBuilderMIMEType(mimeType)
	switch baseType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "text/plain":
		return ".txt"
	}
	if exts, err := mime.ExtensionsByType(baseType); err == nil && len(exts) > 0 {
		preferred := exts[0]
		for _, ext := range exts {
			if len(ext) < len(preferred) {
				preferred = ext
			}
		}
		return preferred
	}
	return ""
}

func detectBuilderBinaryMIMEType(providedMIME, fileName string, data []byte) string {
	if normalized := normalizedBuilderMIMEType(providedMIME); normalized != "" {
		return normalized
	}
	if extType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))); extType != "" {
		if normalized := normalizedBuilderMIMEType(extType); normalized != "" {
			return normalized
		}
	}
	if len(data) > 0 {
		return normalizedBuilderMIMEType(http.DetectContentType(data))
	}
	return "application/octet-stream"
}

func normalizedBuilderMIMEType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(value); err == nil && strings.TrimSpace(mediaType) != "" {
		return strings.TrimSpace(mediaType)
	}
	return value
}

func estimateAdapterPayloadTokens(payload adapterRequestPayload) int {
	total := estimateTextTokens(payload.Instructions)
	for _, msg := range payload.Messages {
		total += estimateAdapterMessageTokens(msg)
	}
	return total
}

func estimateAdapterMessageTokens(msg adapters.Message) int {
	total := estimateTextTokens(msg.Text)
	for _, part := range msg.Parts {
		if strings.EqualFold(strings.TrimSpace(part.Kind), "text") {
			total += estimateTextTokens(part.Text)
		}
	}
	return total
}

func estimateTextTokens(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimate := (runeCount + 3) / 4
	if estimate < 1 {
		return 1
	}
	return estimate
}

// toAdapterModelConfig copies the saved model settings into the shared adapter config shape.
func toAdapterModelConfig(model ModelConfig) adapters.ModelConfig {
	return adapters.ModelConfig{
		Label:                  model.Label,
		StrictStructuredOutput: cloneBoolPointer(model.StrictStructuredOutput),
		VideoGeneration:        model.VideoGeneration,
		VideoPromptOnly:        model.VideoPromptOnly,
		VideoStartFrame:        model.VideoStartFrame,
		VideoEndFrame:          model.VideoEndFrame,
		VideoIngredients:       model.VideoIngredients,
		VideoDuration:          model.VideoDuration,
		VideoAspectRatio:       model.VideoAspectRatio,
		VideoResolution:        model.VideoResolution,
		VideoOutputFormat:      model.VideoOutputFormat,
		VideoFPS:               model.VideoFPS,
		VideoQuality:           model.VideoQuality,
		MeshGeneration:         model.MeshGeneration,
		MeshPromptOnly:         model.MeshPromptOnly,
		MeshImageInput:         model.MeshImageInput,
		MeshMultiImage:         model.MeshMultiImage,
		MeshQuality:            model.MeshQuality,
		MeshOutputFormat:       model.MeshOutputFormat,
		Provider:               model.Provider,
		Adapter:                model.Adapter,
		APIUser:                model.APIUser,
		APIPass:                model.APIPass,
		APIKey:                 model.APIKey,
		APIKeyEnv:              model.APIKeyEnv,
		AuthType:               model.AuthType,
		AuthHeader:             model.AuthHeader,
		BaseURL:                model.BaseURL,
		APIPath:                model.APIPath,
		ModelName:              model.ModelName,
		Headers:                cloneStringMap(model.Headers),
		MaxOutputTokens:        model.MaxOutputTokens,
		TimeoutSeconds:         model.TimeoutSeconds,
		RequestDefaults:        adapters.RequestDefaults{Temperature: model.RequestDefaults.Temperature},
		ProviderOptions:        cloneAnyMap(model.ProviderOptions),
		CapabilityMode:         model.CapabilityMode,
		Capabilities: adapters.ModelCapabilities{
			SupportsTextIn:    model.Capabilities.SupportsTextIn,
			SupportsImageIn:   model.Capabilities.SupportsImageIn,
			SupportsAudioIn:   model.Capabilities.SupportsAudioIn,
			SupportsVideoIn:   model.Capabilities.SupportsVideoIn,
			SupportsFileIn:    model.Capabilities.SupportsFileIn,
			SupportsBinaryOut: model.Capabilities.SupportsBinaryOut,
		},
	}
}

// cloneStringMap copies a string map so adapter calls cannot mutate saved config state.
func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// cloneAnyMap copies a generic option map so adapter calls use isolated request state.
func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneBoolPointer(src *bool) *bool {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func (a *App) callStructuredTextModel(ctx context.Context, model ModelConfig, instructions, input string, expectJSON bool, jsonSchema map[string]any) (string, error) {
	return a.executeAdapterText(ctx, model, adapterRequestPayload{
		Instructions: strings.TrimSpace(instructions),
		Messages:     []adapters.Message{{Role: "user", Text: strings.TrimSpace(input)}},
		ExpectJSON:   expectJSON,
		JSONSchema:   cloneAnyMap(jsonSchema),
	})
}

func (a *App) callStructuredTextModelWithMessages(ctx context.Context, model ModelConfig, instructions, input string, extraMessages []adapters.Message, expectJSON bool, jsonSchema map[string]any) (string, error) {
	messages := []adapters.Message{{Role: "user", Text: strings.TrimSpace(input)}}
	for _, message := range extraMessages {
		messages = appendMessageIfPresent(messages, message)
	}
	return a.executeAdapterText(ctx, model, adapterRequestPayload{
		Instructions: strings.TrimSpace(instructions),
		Messages:     messages,
		ExpectJSON:   expectJSON,
		JSONSchema:   cloneAnyMap(jsonSchema),
	})
}

func sanitizeModelJSONText(raw string) string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
				lines = lines[:len(lines)-1]
			}
			trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	return trimmed
}

func splitBuilderResponse(raw string) (string, builderParseMeta, error) {
	trimmed := sanitizeModelJSONText(raw)
	meta := builderParseMeta{}
	if trimmed == "" {
		return "", meta, errors.New("empty response")
	}
	if json.Valid([]byte(trimmed)) {
		meta.JSONText = trimmed
		return trimmed, meta, nil
	}
	jsonText, start, end, ok := extractJSONObjectFromText(trimmed)
	if !ok {
		candidate, candStart, candEnd, found := extractBalancedJSONObjectCandidate(trimmed)
		if !found {
			meta.UserFacingText = trimmed
			return "", meta, errors.New("no valid json object found in response")
		}
		meta.JSONText = candidate
		meta.UserFacingText = joinNonEmptyWithDoubleNewlines(strings.TrimSpace(trimmed[:candStart]), strings.TrimSpace(trimmed[candEnd:]))
		if err := explainInvalidJSON(candidate); err != nil {
			return "", meta, fmt.Errorf("no valid json object found in response (%v)", err)
		}
		return "", meta, errors.New("no valid json object found in response")
	}
	meta.JSONText = jsonText
	meta.UserFacingText = joinNonEmptyWithDoubleNewlines(strings.TrimSpace(trimmed[:start]), strings.TrimSpace(trimmed[end:]))
	return jsonText, meta, nil
}

func extractJSONObjectFromText(raw string) (string, int, int, bool) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := strings.TrimSpace(raw[start : i+1])
				if json.Valid([]byte(candidate)) {
					return candidate, start, i + 1, true
				}
			}
		}
	}
	return "", 0, 0, false
}

func extractBalancedJSONObjectCandidate(raw string) (string, int, int, bool) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := strings.TrimSpace(raw[start : i+1])
				return candidate, start, i + 1, true
			}
		}
	}
	return "", 0, 0, false
}

func explainInvalidJSON(candidate string) error {
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("extra tokens after JSON object")
	}
	return errors.New("unknown JSON formatting issue")
}

func builderInvalidJSONTruncationHint(raw string) string {
	if looksLikeIncompleteJSONObject(raw) {
		return "Invalid Builder JSON: response may be truncated. Increase Max Output Tokens or reduce output size."
	}
	return ""
}

func looksLikeIncompleteJSONObject(raw string) bool {
	trimmed := sanitizeModelJSONText(raw)
	start := strings.IndexByte(trimmed, '{')
	if start < 0 {
		return false
	}
	depth := 0
	inString := false
	escaped := false
	sawObject := false
	for i := start; i < len(trimmed); i++ {
		ch := trimmed[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
			sawObject = true
		case '}':
			if depth > 0 {
				depth--
			}
			if depth == 0 && sawObject {
				return false
			}
		}
	}
	return sawObject && (depth > 0 || inString)
}

func joinNonEmptyWithDoubleNewlines(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clean = append(clean, part)
	}
	return strings.TrimSpace(strings.Join(clean, "\n\n"))
}

func previewForLog(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func diagnosticsResponsePreview(raw string, max int) (string, string) {
	clean := strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n")
	if max <= 0 || len(clean) <= max {
		return clean, "Received Response"
	}
	preview := clean[:max] + "..."
	label := fmt.Sprintf("Received Response Preview (truncated to %d of %d bytes)", max, len(clean))
	return fmt.Sprintf("AgentGO diagnostics preview truncated to %d of %d bytes. The trailing ... was added by AgentGO, not the AI.\n\n%s", max, len(clean), preview), label
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type stoppedDoubleTapRef struct {
		ProjectName string
		ExecutionID string
	}
	doubleTapStops := []stoppedDoubleTapRef{}
	a.mu.Lock()
	activeProject := strings.TrimSpace(a.activeProjectName)
	count := len(a.activeCancels)
	seenDoubleTapStops := map[string]bool{}
	for id, entry := range a.activeCancels {
		if entry.Cancel != nil {
			entry.Cancel()
		}
		if doubleTapExecutionIDLooksLikeDoubleTap(entry.ExecutionID) {
			key := strings.TrimSpace(entry.ProjectName) + "|" + strings.TrimSpace(entry.ExecutionID)
			if !seenDoubleTapStops[key] {
				doubleTapStops = append(doubleTapStops, stoppedDoubleTapRef{ProjectName: entry.ProjectName, ExecutionID: entry.ExecutionID})
				seenDoubleTapStops[key] = true
			}
		}
		delete(a.activeCancels, id)
	}
	for projectName, state := range a.waveExecutionsByProject {
		if strings.EqualFold(strings.TrimSpace(state.CurrentPromptSource), "doubletap") || doubleTapExecutionIDLooksLikeDoubleTap(state.ExecutionID) {
			key := strings.TrimSpace(projectName) + "|" + strings.TrimSpace(state.ExecutionID)
			if !seenDoubleTapStops[key] {
				doubleTapStops = append(doubleTapStops, stoppedDoubleTapRef{ProjectName: projectName, ExecutionID: state.ExecutionID})
				seenDoubleTapStops[key] = true
			}
		}
	}
	wasRisk := a.riskModeEnabled
	if wasRisk {
		a.stopRiskModeLocked("Emergency Stop triggered.", "All active model connections were canceled.")
	} else {
		a.clearRiskModeLocked()
	}
	for modelID := range a.toggles {
		a.toggles[modelID] = false
	}
	a.reviewerID = ""
	a.clearAllWaveExecutionsLocked()
	a.resetTokenUsageHierarchyLocked()
	a.markWorkModeSessionsEmergencyStoppedLocked(activeProject)
	if activeProject != "" {
		a.setWaveStatusLocked(activeProject, waveStatusState{ProjectName: activeProject, Visible: false, State: "", Detail: ""})
	}
	a.mu.Unlock()
	for _, stopRef := range doubleTapStops {
		a.recordDoubleTapStopped(stopRef.ProjectName, stopRef.ExecutionID, "Emergency Stop triggered by user.")
	}
	resetCount := 0
	if activeProject != "" {
		a.clearLastMergedFiles(activeProject)
		a.finalizeActiveOutfitRunStopped(activeProject, "Emergency Stop triggered.")
		if cleared, err := a.resetProjectAIContextsForWorkflowEnd(activeProject); err != nil {
			a.logf("system", "warn", "Failed clearing ai_context.json/reviewer_context.json during Emergency Stop: %v", err)
		} else {
			resetCount = cleared
		}
	}
	a.logf("system", "warn", "Emergency stop triggered. Canceled %d request(s). Risk mode active=%v reset_runtime_contexts=%d active_models_deactivated=true", count, wasRisk, resetCount)
	if wasRisk {
		a.logRiskf("system", "warn", "RISK MODE ENDED: Emergency Stop canceled %d active request(s)", count)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": count, "riskModeStopped": wasRisk, "resetAIContexts": resetCount})
}

func (a *App) handleEndWorkflowEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	resetCount, err := a.endWorkflowCycleWithoutMerge(projectName, "Do Not Merge selected.")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Ended workflow cycle without merge for project %s and cleared ai_context.json plus reviewer_context.json for %d model(s)", projectName, resetCount)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resetAIContexts": resetCount})
}

func (a *App) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := a.readKnowledgeFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (a *App) handleKnowledgeNotesSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req notesSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	notesPath, err := appRootFilePath(knowledgeNotesFilename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := atomicWriteFile(notesPath, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "defaultTab": knowledgeDefaultTab(req.Content), "savedAt": time.Now().Format(time.RFC3339)})
}

func (e diagnosticsEntry) withStage(stage string) diagnosticsEntry {
	e.Stage = strings.TrimSpace(stage)
	return e
}

func (e diagnosticsEntry) withReason(reason string) diagnosticsEntry {
	e.Reason = strings.TrimSpace(reason)
	return e
}

func (e diagnosticsEntry) withResponse(response string) diagnosticsEntry {
	e.Response = strings.TrimSpace(response)
	return e
}

func (e diagnosticsEntry) withResponseLabel(label string) diagnosticsEntry {
	e.ResponseLabel = strings.TrimSpace(label)
	return e
}

func (e diagnosticsEntry) withStatusMessage(message string) diagnosticsEntry {
	e.StatusMessage = strings.TrimSpace(message)
	return e
}

func (e diagnosticsEntry) withPrompt(prompt string) diagnosticsEntry {
	e.Prompt = strings.TrimSpace(prompt)
	return e
}

func (e diagnosticsEntry) withSystemPrompt(prompt string) diagnosticsEntry {
	e.SystemPrompt = strings.TrimSpace(prompt)
	return e
}

func (a *App) decorateDiagnosticsWithCurrentWave(projectName string, entry diagnosticsEntry) diagnosticsEntry {
	state, ok := a.currentWaveExecution(projectName)
	if !ok {
		status, statusOK := a.currentWaveStatus(projectName)
		if !statusOK {
			return entry
		}
		entry.WaveNumber = status.CurrentWave
		entry.PromptSource = strings.TrimSpace(status.PromptSource)
		entry.ContextFilesUsed = status.ContextFilesUsed
		return entry
	}
	entry.WaveNumber = state.CurrentWave
	entry.PromptSource = strings.TrimSpace(state.CurrentPromptSource)
	entry.ContextFilesUsed = state.CurrentContextFilesUsed
	return entry
}

func makeDiagnosticsFileRef(path string) diagnosticsFileRef {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	return diagnosticsFileRef{Name: filepath.Base(clean), Path: clean}
}

func appendUniqueDiagnosticsFile(list []diagnosticsFileRef, ref diagnosticsFileRef) []diagnosticsFileRef {
	if strings.TrimSpace(ref.Path) == "" {
		return list
	}
	for _, item := range list {
		if item.Path == ref.Path {
			return list
		}
	}
	return append(list, ref)
}

func (a *App) relativeMetaPath(model ModelConfig, projectName string) string {
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(a.cfg.WorkRoot, metaRoot)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (a *App) projectContextDiagnosticsFiles(projectName string, relPaths []string) []diagnosticsFileRef {
	refs := []diagnosticsFileRef{}
	for _, rel := range normalizeRelativePaths(relPaths) {
		refs = appendUniqueDiagnosticsFile(refs, makeDiagnosticsFileRef(filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", rel))))
	}
	return refs
}

func (a *App) builderDiagnosticsFiles(projectName string, model ModelConfig, relPaths []string) []diagnosticsFileRef {
	refs := a.projectContextDiagnosticsFiles(projectName, relPaths)
	metaRoot := a.relativeMetaPath(model, projectName)
	if metaRoot == "" {
		return refs
	}
	refs = appendUniqueDiagnosticsFile(refs, makeDiagnosticsFileRef(filepath.ToSlash(filepath.Join(metaRoot, "user_context.json"))))
	refs = appendUniqueDiagnosticsFile(refs, makeDiagnosticsFileRef(filepath.ToSlash(filepath.Join(metaRoot, "ai_context.json"))))
	return refs
}

func (a *App) publishDiagnostics(entry diagnosticsEntry) {
	entry.Target = strings.TrimSpace(entry.Target)
	entry.ModelID = strings.TrimSpace(entry.ModelID)
	entry.ModelLabel = strings.TrimSpace(entry.ModelLabel)
	entry.Stage = strings.TrimSpace(entry.Stage)
	if entry.Target == "" || entry.Stage == "" {
		return
	}
	a.mu.Lock()
	a.diagSubscriberTopID++
	entry.ID = fmt.Sprintf("diag-%d", a.diagSubscriberTopID)
	subscribers := make([]chan diagnosticsEntry, 0, len(a.diagSubscribers))
	for _, ch := range a.diagSubscribers {
		subscribers = append(subscribers, ch)
	}
	a.mu.Unlock()
	for _, ch := range subscribers {
		select {
		case ch <- entry:
		default:
		}
	}
}

func (a *App) handleDiagnosticsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan diagnosticsEntry, 32)
	a.mu.Lock()
	a.diagSubscriberTopID++
	subscriberID := a.diagSubscriberTopID
	a.diagSubscribers[subscriberID] = ch
	a.mu.Unlock()
	a.httpMu.Lock()
	a.activeDiagnosticsStreams++
	a.httpMu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.diagSubscribers, subscriberID)
		a.mu.Unlock()
		a.httpMu.Lock()
		if a.activeDiagnosticsStreams > 0 {
			a.activeDiagnosticsStreams--
		}
		a.httpMu.Unlock()
	}()

	if _, err := fmt.Fprint(w, ": diagnostics\n\n"); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case entry := <-ch:
			payload, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *App) handleDiagnosticsFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := filepath.ToSlash(strings.TrimSpace(r.URL.Query().Get("path")))
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "../") || strings.Contains(rel, "..\\") {
		http.Error(w, "invalid diagnostics path", http.StatusBadRequest)
		return
	}
	base := filepath.Base(rel)
	dir := filepath.Base(filepath.Dir(rel))
	baseLower := strings.ToLower(base)
	isCypherDiagnostic := strings.HasPrefix(base, "cypher_") && strings.HasSuffix(baseLower, ".json") && dir == "cypher_diagnostics"
	isLegacyWireTapDiagnostic := strings.HasPrefix(base, "wiretap_") && strings.HasSuffix(baseLower, ".json") && dir == "wiretap_diagnostics"
	isProjectWireTapDiagnostic := false
	parts := strings.Split(rel, "/")
	if len(parts) == 4 && parts[0] == "projects" && isValidProjectName(parts[1]) && parts[2] == "diagnostics" {
		isProjectWireTapDiagnostic = strings.HasPrefix(base, "wiretap_") && strings.HasSuffix(baseLower, ".json")
	}
	if !isCypherDiagnostic && !isLegacyWireTapDiagnostic && !isProjectWireTapDiagnostic {
		http.Error(w, "not an AgentGO diagnostics file", http.StatusBadRequest)
		return
	}
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	data, err := readFileUnderRoot(a.cfg.WorkRoot, full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content, isText, contentType, imageDataURL, previewKind := buildPreviewPayload(rel, data, true)
	writeJSON(w, http.StatusOK, fileResponse{Path: rel, Content: content, ContentType: contentType, IsText: isText, ImageDataURL: imageDataURL, PreviewKind: previewKind, BlobURL: buildBlobURL(rel), SizeBytes: int64(len(data))})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := defaultLogResponseLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	if limit > maxLogResponseLimit {
		limit = maxLogResponseLimit
	}
	sinceSeq := uint64(0)
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		parsed, err := strconv.ParseUint(rawSince, 10, 64)
		if err != nil {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
		sinceSeq = parsed
	}
	objectResponse := strings.TrimSpace(r.URL.Query().Get("format")) == "object" || strings.TrimSpace(r.URL.Query().Get("since")) != ""

	a.mu.RLock()
	logs := make([]LogEntry, 0, len(a.logs))
	for _, entry := range a.logs {
		if sinceSeq > 0 && entry.Seq <= sinceSeq {
			continue
		}
		logs = append(logs, entry)
	}
	nextSeq := a.logSeq
	a.mu.RUnlock()
	if len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}
	if objectResponse {
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "nextSeq": nextSeq, "limit": limit})
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (a *App) handleToastLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message string `json:"message"`
		Variant string `json:"variant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	variant := strings.ToLower(strings.TrimSpace(req.Variant))
	if variant == "" {
		variant = "default"
	}
	if len(variant) > 40 {
		variant = variant[:40]
	}
	a.logf(variant, "toast", "%s", message)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) clearLogs() {
	a.mu.Lock()
	a.logs = nil
	a.mu.Unlock()
}

func (a *App) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.clearLogs()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleListFiles(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cleanRel := strings.Trim(filepath.ToSlash(rel), "/")
			if cleanRel == "" || strings.HasSuffix(cleanRel, "/tmp-work") || filepath.Base(cleanRel) == "tmp-work" {
				writeJSON(w, http.StatusOK, listDirResponse{CurrentPath: filepath.ToSlash(rel), Entries: []fileEntry{}})
				return
			}
			http.Error(w, "directory not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	out := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		if isSymlinkDirEntry(entry) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		p := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		out = append(out, fileEntry{
			Name:    entry.Name(),
			Path:    strings.TrimPrefix(p, "/"),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	isRoot := strings.Trim(filepath.ToSlash(rel), "/") == ""
	sort.Slice(out, func(i, j int) bool {
		nameI := strings.ToLower(out[i].Name)
		nameJ := strings.ToLower(out[j].Name)
		if isRoot {
			rank := func(entry fileEntry, lowerName string) int {
				if !entry.IsDir {
					return 3
				}
				switch lowerName {
				case "projects":
					return 0
				case "outfits":
					return 1
				case "mastermind":
					return 2
				default:
					return 3
				}
			}
			rankI := rank(out[i], nameI)
			rankJ := rank(out[j], nameJ)
			if rankI != rankJ {
				return rankI < rankJ
			}
		}
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return nameI < nameJ
	})
	writeJSON(w, http.StatusOK, listDirResponse{CurrentPath: filepath.ToSlash(rel), Entries: out})
}

func (a *App) handleReadFile(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	data, err := readFileUnderRoot(a.cfg.WorkRoot, full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content, isText, contentType, imageDataURL, previewKind := buildPreviewPayload(rel, data, true)
	writeJSON(w, http.StatusOK, fileResponse{Path: filepath.ToSlash(rel), Content: content, ContentType: contentType, IsText: isText, ImageDataURL: imageDataURL, PreviewKind: previewKind, BlobURL: buildBlobURL(rel), SizeBytes: int64(len(data))})
}

func (a *App) handleWorkModeSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) < 3 {
		writeJSON(w, http.StatusOK, workModeSearchResponse{Query: query, Results: []workModeSearchResult{}})
		return
	}
	projectName, projectworkRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lowerQuery := strings.ToLower(query)
	const maxWorkModeSearchFileBytes = int64(2 * 1024 * 1024)
	rootPrefix := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork"))
	resp := workModeSearchResponse{Query: query, Results: []workModeSearchResult{}}
	err = filepath.WalkDir(projectworkRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if fullPath == projectworkRoot {
			return nil
		}
		if isSymlinkDirEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(projectworkRoot, fullPath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isHiddenWorkModePath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxWorkModeSearchFileBytes {
			return nil
		}
		data, err := readFileUnderRoot(projectworkRoot, fullPath)
		if err != nil {
			return nil
		}
		content, isText, _, _, previewKind := buildPreviewPayload(rel, data, true)
		if !isText || previewKind != "text" {
			return nil
		}
		count := strings.Count(strings.ToLower(content), lowerQuery)
		if count <= 0 {
			return nil
		}
		resp.Results = append(resp.Results, workModeSearchResult{Path: filepath.ToSlash(filepath.Join(rootPrefix, rel)), Count: count})
		resp.TotalMatches += count
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sort.Slice(resp.Results, func(i, j int) bool {
		return strings.ToLower(resp.Results[i].Path) < strings.ToLower(resp.Results[j].Path)
	})
	resp.FileCount = len(resp.Results)
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleFileBlob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	data, err := readFileUnderRoot(a.cfg.WorkRoot, full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	contentType := detectContentType(rel, data)
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, filepath.Base(rel)))
	http.ServeContent(w, r, filepath.Base(rel), time.Time{}, bytes.NewReader(data))
}

func (a *App) handleSaveFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req fileSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	rel := strings.TrimSpace(req.Path)
	if err := a.rejectLockedTmpWorkMutation(rel); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := atomicWriteFileUnderRoot(a.cfg.WorkRoot, full, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.invalidateDeadDropStatusCache("")
	a.logf("system", "info", "File saved: %s", filepath.ToSlash(rel))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func normalizeWorkModeCreateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", errors.New("name must not include path separators or traversal")
	}
	if strings.HasPrefix(name, ".") {
		return "", errors.New("hidden files and folders are not shown in Work Mode")
	}
	if strings.EqualFold(name, "project.json") {
		return "", errors.New("project.json is protected")
	}
	return name, nil
}

func projectworkRelFromWorkRelAllowRoot(workRel, projectName string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(filepath.ToSlash(workRel)), "\\", "/"))
	prefix := path.Join("projects", projectName, "projectwork")
	if clean == "." || clean == "" {
		return "", errors.New("folder must be inside the active project's projectwork folder")
	}
	if clean == prefix {
		return "", nil
	}
	if !strings.HasPrefix(clean, prefix+"/") {
		return "", errors.New("folder must be inside the active project's projectwork folder")
	}
	rel := strings.TrimPrefix(clean, prefix+"/")
	if rel == "" || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", errors.New("invalid projectwork folder path")
	}
	return rel, nil
}

func (a *App) handleCreateFileItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req fileCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectName, projectworkRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.rejectLockedTmpWorkMutation(req.ParentPath); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	parentRel, err := projectworkRelFromWorkRelAllowRoot(req.ParentPath, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, err := normalizeWorkModeCreateName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	itemType := strings.ToLower(strings.TrimSpace(req.ItemType))
	if itemType != "file" && itemType != "folder" {
		http.Error(w, "itemType must be file or folder", http.StatusBadRequest)
		return
	}
	parentFull, err := safeJoin(projectworkRoot, parentRel)
	if err != nil {
		http.Error(w, "invalid parent folder", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, parentFull); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(parentFull)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "parent folder not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !info.IsDir() {
		http.Error(w, "parent path is not a folder", http.StatusBadRequest)
		return
	}
	targetFull, err := safeJoin(parentFull, name)
	if err != nil {
		http.Error(w, "invalid item name", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, targetFull); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(targetFull); err == nil {
		http.Error(w, "item already exists", http.StatusConflict)
		return
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if itemType == "folder" {
		if err := os.Mkdir(targetFull, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		file, err := os.OpenFile(targetFull, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := file.Close(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	projectRel := path.Join(parentRel, name)
	workRel := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", filepath.FromSlash(projectRel)))
	a.invalidateDeadDropStatusCache(projectName)
	a.logf("system", "info", "Work Mode %s created: %s", itemType, workRel)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": workRel, "itemType": itemType})
}

func normalizeProjectworkRenamePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("new path is required")
	}
	if strings.HasSuffix(value, "/") || strings.HasSuffix(value, "\\") {
		return "", errors.New("new path must include a filename")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) || filepath.VolumeName(value) != "" {
		return "", errors.New("new path must be relative to projectwork")
	}
	clean := path.Clean(value)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", errors.New("new path cannot leave projectwork")
	}
	base := path.Base(clean)
	if base == "." || base == ".." || base == "/" || strings.TrimSpace(base) == "" {
		return "", errors.New("new path must include a valid name")
	}
	if strings.HasPrefix(base, ".") {
		return "", errors.New("hidden files and folders are not shown in Work Mode")
	}
	if strings.EqualFold(base, "project.json") {
		return "", errors.New("project.json is protected")
	}
	return clean, nil
}

func projectworkRelFromWorkRel(workRel, projectName string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(filepath.ToSlash(workRel)), "\\", "/"))
	prefix := path.Join("projects", projectName, "projectwork")
	if clean == "." || clean == prefix || !strings.HasPrefix(clean, prefix+"/") {
		return "", errors.New("file must be inside the active project's projectwork folder")
	}
	rel := strings.TrimPrefix(clean, prefix+"/")
	if rel == "" || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", errors.New("invalid projectwork file path")
	}
	return rel, nil
}

func (a *App) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req fileRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectName, projectworkRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	oldWorkRel := strings.TrimSpace(req.Path)
	if err := a.rejectLockedTmpWorkMutation(oldWorkRel); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	oldProjectRel, err := projectworkRelFromWorkRel(oldWorkRel, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newProjectRel, err := normalizeProjectworkRenamePath(req.NewPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newWorkRelCandidate := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", filepath.FromSlash(newProjectRel)))
	if err := a.rejectLockedTmpWorkMutation(newWorkRelCandidate); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	oldFull, err := safeJoin(projectworkRoot, oldProjectRel)
	if err != nil {
		http.Error(w, "invalid current path", http.StatusBadRequest)
		return
	}
	newFull, err := safeJoin(projectworkRoot, newProjectRel)
	if err != nil {
		http.Error(w, "invalid new path", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, oldFull); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, filepath.Dir(newFull)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(oldFull)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.EqualFold(path.Base(oldProjectRel), "project.json") {
		http.Error(w, "project.json is protected", http.StatusBadRequest)
		return
	}
	if oldProjectRel == "" || oldProjectRel == "." {
		http.Error(w, "projectwork root cannot be renamed", http.StatusBadRequest)
		return
	}
	if oldFull == newFull {
		a.invalidateDeadDropStatusCache(projectName)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": filepath.ToSlash(oldWorkRel), "projectworkPath": oldProjectRel})
		return
	}
	if _, err := os.Lstat(newFull); err == nil {
		http.Error(w, "target file already exists", http.StatusConflict)
		return
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(newFull), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, filepath.Dir(newFull)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newWorkRel := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", filepath.FromSlash(newProjectRel)))
	itemLabel := "File"
	if info.IsDir() {
		itemLabel = "Folder"
	}
	a.invalidateDeadDropStatusCache(projectName)
	a.logf("system", "info", "%s renamed: %s -> %s", itemLabel, filepath.ToSlash(oldWorkRel), newWorkRel)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": newWorkRel, "projectworkPath": newProjectRel})
}

func (a *App) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req fileDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	rel := strings.TrimSpace(req.Path)
	if err := a.rejectLockedTmpWorkMutation(rel); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(a.cfg.WorkRoot, full); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		if isProtectedFileManagerFolder(rel) {
			http.Error(w, "protected folder cannot be deleted", http.StatusForbidden)
			return
		}
		entries, err := os.ReadDir(full)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(entries) > 0 {
			http.Error(w, "folder is not empty", http.StatusBadRequest)
			return
		}
		if err := removeFileUnderRoot(a.cfg.WorkRoot, full); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.invalidateDeadDropStatusCache("")
		a.logf("system", "warn", "Folder deleted: %s", filepath.ToSlash(rel))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deletedType": "folder"})
		return
	}
	if isProtectedFileManagerFile(rel) {
		http.Error(w, "protected file cannot be deleted", http.StatusForbidden)
		return
	}
	if err := removeFileUnderRoot(a.cfg.WorkRoot, full); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.invalidateDeadDropStatusCache("")
	a.logf("system", "warn", "File deleted: %s", filepath.ToSlash(rel))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deletedType": "file"})
}

func isExactDeadDropCandidateName(name string) bool {
	clean := strings.TrimSpace(filepath.Base(filepath.ToSlash(name)))
	if clean == "" {
		return false
	}
	ext := filepath.Ext(clean)
	if ext == "" || ext == "." {
		return false
	}
	return strings.TrimSuffix(clean, ext) == "DeadDrop"
}

func deadDropSourceKind(path string) string {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(path)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return "image"
	default:
		return "text"
	}
}

func (a *App) deadDropRoot(projectName string) (string, error) {
	if !isValidProjectName(projectName) {
		return "", errors.New("invalid project name")
	}
	return safeJoin(a.cfg.WorkRoot, "projects", projectName, "deaddrop")
}

func deadDropRevisionNumberFromName(name string) (int, bool) {
	clean := strings.TrimSpace(filepath.Base(filepath.ToSlash(name)))
	ext := filepath.Ext(clean)
	if ext == "" || ext == "." {
		return 0, false
	}
	base := strings.TrimSuffix(clean, ext)
	if !strings.HasPrefix(base, "DeadDrop_") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(base, "DeadDrop_"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (a *App) deadDropRevisions(projectName string) ([]deadDropRevisionInfo, error) {
	deadDropRoot, err := a.deadDropRoot(projectName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(deadDropRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []deadDropRevisionInfo{}, nil
		}
		return nil, err
	}
	revisions := make([]deadDropRevisionInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		rev, ok := deadDropRevisionNumberFromName(entry.Name())
		if !ok {
			continue
		}
		revisions = append(revisions, deadDropRevisionInfo{
			Path:     filepath.ToSlash(filepath.Join("projects", projectName, "deaddrop", entry.Name())),
			FileName: entry.Name(),
			Revision: rev,
		})
	}
	sort.Slice(revisions, func(i, j int) bool {
		if revisions[i].Revision == revisions[j].Revision {
			return revisions[i].FileName < revisions[j].FileName
		}
		return revisions[i].Revision < revisions[j].Revision
	})
	return revisions, nil
}

func (a *App) nextDeadDropRevisionNumber(projectName string) (int, error) {
	revisions, err := a.deadDropRevisions(projectName)
	if err != nil {
		return 0, err
	}
	if len(revisions) == 0 {
		return 0, nil
	}
	return revisions[len(revisions)-1].Revision + 1, nil
}

func (a *App) projectWorkDeadDropMatches(projectName string) ([]string, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return nil, err
	}
	matches := []string{}
	err = filepath.WalkDir(projectworkRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if isSymlinkDirEntry(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isExactDeadDropCandidateName(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(a.cfg.WorkRoot, path)
		if err != nil {
			return nil
		}
		matches = append(matches, filepath.ToSlash(rel))
		if len(matches) >= maxDeadDropStatusMatches {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func (a *App) currentDeadDropSource(projectName string) (string, string, error) {
	deadDropRoot, err := a.deadDropRoot(projectName)
	if err != nil {
		return "", "", err
	}
	entries, err := os.ReadDir(deadDropRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", "", err
	}
	for _, entry := range entries {
		if isSymlinkDirEntry(entry) || entry.IsDir() || !isExactDeadDropCandidateName(entry.Name()) {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("projects", projectName, "deaddrop", entry.Name()))
		return rel, deadDropSourceKind(entry.Name()), nil
	}
	return "", "", nil
}

func (a *App) ensureLatestDeadDropRevisionSource(projectName string) (string, string, error) {
	sourceRel, _, err := a.currentDeadDropSource(projectName)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(sourceRel) == "" {
		return "", "", errors.New("DeadDrop source file is not set")
	}
	revisions, err := a.deadDropRevisions(projectName)
	if err != nil {
		return "", "", err
	}
	if len(revisions) == 0 {
		sourceFull, err := safeJoin(a.cfg.WorkRoot, sourceRel)
		if err != nil {
			return "", "", err
		}
		ext := filepath.Ext(sourceFull)
		if ext == "" || ext == "." {
			return "", "", errors.New("DeadDrop source file is missing an extension")
		}
		data, err := readFileUnderRoot(a.cfg.WorkRoot, sourceFull)
		if err != nil {
			return "", "", err
		}
		deadDropRoot, err := a.deadDropRoot(projectName)
		if err != nil {
			return "", "", err
		}
		if err := os.MkdirAll(deadDropRoot, 0o755); err != nil {
			return "", "", err
		}
		revisionZeroFull, err := safeJoin(deadDropRoot, "DeadDrop_0"+ext)
		if err != nil {
			return "", "", err
		}
		if err := atomicWriteFile(revisionZeroFull, data, 0o644); err != nil {
			return "", "", err
		}
		revisionZeroRel, _ := filepath.Rel(a.cfg.WorkRoot, revisionZeroFull)
		revisions = []deadDropRevisionInfo{{
			Path:     filepath.ToSlash(revisionZeroRel),
			FileName: filepath.Base(revisionZeroFull),
			Revision: 0,
		}}
		a.logf("system", "info", "DeadDrop revision 0 created from original source for project %s.", projectName)
	}
	latest := revisions[len(revisions)-1]
	return filepath.ToSlash(latest.Path), deadDropSourceKind(latest.FileName), nil
}

func (a *App) handleDeadDropStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	a.deadDropStatusMu.Lock()
	if cached, ok := a.deadDropStatusCache[projectName]; ok && time.Since(cached.CachedAt) < deadDropStatusCacheTTL {
		resp := cached.Response
		a.deadDropStatusMu.Unlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	a.deadDropStatusMu.Unlock()
	sourcePath, sourceKind, err := a.currentDeadDropSource(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	matches, err := a.projectWorkDeadDropMatches(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	revisions, err := a.deadDropRevisions(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nextRevision := 0
	if len(revisions) > 0 {
		nextRevision = revisions[len(revisions)-1].Revision + 1
	}
	resp := deadDropStatusResponse{
		Project:            projectName,
		HasSource:          strings.TrimSpace(sourcePath) != "",
		SourcePath:         sourcePath,
		SourceKind:         sourceKind,
		ProjectworkMatches: matches,
		RevisionCount:      len(revisions),
		NextRevision:       nextRevision,
	}
	a.deadDropStatusMu.Lock()
	a.deadDropStatusCache[projectName] = deadDropStatusCacheEntry{Response: resp, CachedAt: time.Now()}
	a.deadDropStatusMu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) setDeadDropSourceFromData(projectName, baseName string, data []byte) (string, string, string, error) {
	projectName = strings.TrimSpace(projectName)
	baseName = filepath.Base(strings.TrimSpace(baseName))
	if projectName == "" {
		return "", "", "", errors.New("project name is required")
	}
	if baseName == "" || !isExactDeadDropCandidateName(baseName) {
		return "", "", "", errors.New("only exact-case DeadDrop.<ext> files can be set as deaddrop")
	}
	deadDropRoot, err := a.deadDropRoot(projectName)
	if err != nil {
		return "", "", "", err
	}
	if err := os.RemoveAll(deadDropRoot); err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(deadDropRoot, 0o755); err != nil {
		return "", "", "", err
	}
	deadDropBase, err := safeJoin(deadDropRoot, baseName)
	if err != nil {
		return "", "", "", err
	}
	ext := filepath.Ext(baseName)
	revisionZero, err := safeJoin(deadDropRoot, "DeadDrop_0"+ext)
	if err != nil {
		return "", "", "", err
	}
	if err := atomicWriteFile(revisionZero, data, 0o644); err != nil {
		return "", "", "", err
	}
	if err := atomicWriteFile(deadDropBase, data, 0o644); err != nil {
		return "", "", "", err
	}
	baseRel, _ := filepath.Rel(a.cfg.WorkRoot, deadDropBase)
	revRel, _ := filepath.Rel(a.cfg.WorkRoot, revisionZero)
	return filepath.ToSlash(baseRel), filepath.ToSlash(revRel), deadDropSourceKind(baseName), nil
}

func (a *App) handleSetDeadDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, projectworkRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	var req deadDropSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	rel := strings.TrimSpace(req.Path)
	if rel == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	sourceFile, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	projectworkAbs, err := filepath.Abs(projectworkRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sourceAbs, err := filepath.Abs(sourceFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sourceAbs != projectworkAbs && !strings.HasPrefix(sourceAbs, projectworkAbs+string(os.PathSeparator)) {
		http.Error(w, "DeadDrop source must live inside the active project's projectwork folder", http.StatusBadRequest)
		return
	}
	if !isExactDeadDropCandidateName(filepath.Base(sourceAbs)) {
		http.Error(w, "Only exact-case DeadDrop.<ext> files can be set as deaddrop", http.StatusBadRequest)
		return
	}
	data, err := readFileUnderRoot(projectworkRoot, sourceAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	baseRel, revRel, sourceKind, err := a.setDeadDropSourceFromData(projectName, filepath.Base(sourceAbs), data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := removeFileUnderRoot(projectworkRoot, sourceAbs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.invalidateDeadDropStatusCache(projectName)
	a.logf("system", "info", "Set %s as DeadDrop for project %s and reset deaddrop history", filepath.ToSlash(rel), projectName)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"project":          projectName,
		"sourcePath":       baseRel,
		"revisionZeroPath": revRel,
		"sourceKind":       sourceKind,
		"revisionCount":    1,
		"nextRevision":     1,
	})
}

func deadDropJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"score":             map[string]any{"type": "number"},
			"returned_file":     map[string]any{"type": "boolean"},
			"improvements_made": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"handoff_notes":     map[string]any{"type": "string"},
			"file_content":      map[string]any{"type": "string"},
		},
		"required":             []string{"score", "returned_file", "improvements_made", "handoff_notes"},
		"additionalProperties": false,
	}
}

func buildDeadDropRequestPayload(cfg AppConfig, model ModelConfig, prompt, userContext, aiContext, sourceRel, sourceKind string, stopScore int, revisionLevel string, contextMessage adapters.Message) (adapterRequestPayload, error) {
	instructions, err := loadDeadDropSystemPrompt(cfg, model.PromptMode)
	if err != nil {
		return adapterRequestPayload{}, err
	}
	instructions = joinNonEmptyWithDoubleNewlines(instructions, deadDropUploadedFileOnlyInstruction)
	instructions = appendModelUggProtocol(instructions, model)
	stopScore = normalizeDeadDropStopScore(stopScore)
	revisionLevel = normalizeDeadDropRevisionLevel(revisionLevel)
	instructions = strings.ReplaceAll(instructions, "{{DEADDROP_STOP_SCORE}}", strconv.Itoa(stopScore))
	instructions = strings.ReplaceAll(instructions, "{{DEADDROP_ADJUSTMENT_INSTRUCTION}}", deadDropAdjustmentInstruction(revisionLevel))
	messages := []adapters.Message{
		buildTextMessage("user", strings.TrimSpace(`DEADDROP OBJECTIVE
The user is running an AgentGO DeadDrop revision pass.
Treat the following prompt as the specific improvement target for this pass.
Revise the provided file to better satisfy that target.
DEADDROP USER PROMPT
`+strings.TrimSpace(prompt))),
		buildTextMessage("user", `MODEL USER CONTEXT (meta/user_context.json):
`+strings.TrimSpace(userContext)),
		buildTextMessage("user", `MODEL AI CONTEXT (meta/ai_context.json):
`+strings.TrimSpace(aiContext)),
		buildTextMessage("user", fmt.Sprintf(`DEADDROP TARGET FILE
All revisions, edits, and updates must be applied only to this uploaded DeadDrop file.
Revise this file and return one complete finalized replacement for this file only.
Path: %s
Kind: %s`, strings.TrimSpace(sourceRel), strings.TrimSpace(sourceKind))),
	}
	if len(contextMessage.Parts) > 0 || strings.TrimSpace(contextMessage.Text) != "" {
		messages = append(messages, contextMessage)
	}
	return adapterRequestPayload{Instructions: instructions, Messages: messages, ExpectJSON: true, JSONSchema: deadDropJSONSchema()}, nil
}

func parseDeadDropResponse(raw string) (deadDropResponsePayload, error) {
	var resp deadDropResponsePayload
	trimmed := sanitizeModelJSONText(raw)
	if trimmed == "" {
		return resp, errors.New("empty response")
	}
	if !json.Valid([]byte(trimmed)) {
		if jsonText, _, _, ok := extractJSONObjectFromText(trimmed); ok {
			trimmed = jsonText
		} else if candidate, _, _, found := extractBalancedJSONObjectCandidate(trimmed); found {
			trimmed = candidate
		} else {
			return resp, errors.New("no valid json object found in response")
		}
	}
	normalized, _, _, err := normalizeAgentGOToolResponseJSON(trimmed, agentGOToolDeadDrop)
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal([]byte(normalized), &resp); err != nil {
		return resp, err
	}
	return resp, nil
}

func validateDeadDropResponse(resp deadDropResponsePayload, sourceRel, sourceKind string, returnedBinary []byte, returnedBinaryName, returnedBinaryMIME string) error {
	if err := validateAgentGOToolHeader(resp.AgentGOTool, resp.ToolVersion, agentGOToolDeadDrop); err != nil {
		return err
	}
	if resp.Score < 0 || resp.Score > 100 {
		return errors.New("score must be between 0 and 100")
	}
	if resp.ImprovementsMade == nil {
		return errors.New("improvements_made is required")
	}
	if strings.TrimSpace(resp.HandoffNotes) == "" {
		return errors.New("handoff_notes is required")
	}
	if !resp.ReturnedFile {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "image":
		return validateDeadDropReturnedImageBinaryForSource(returnedBinary, returnedBinaryName, returnedBinaryMIME, filepath.Ext(sourceRel))
	default:
		if resp.FileContent == nil {
			return errors.New("file_content is required when returned_file is true for text")
		}
		if strings.TrimSpace(*resp.FileContent) == "" {
			return errors.New("file_content is required when returned_file is true for text")
		}
	}
	return nil
}

func deadDropCanonicalImageExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".jpeg":
		return ".jpg"
	default:
		return strings.ToLower(strings.TrimSpace(ext))
	}
}

func deadDropAllowedImageExtension(ext string) bool {
	switch deadDropCanonicalImageExt(ext) {
	case ".png", ".jpg", ".webp":
		return true
	default:
		return false
	}
}

func deadDropPreferredImageExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return ext
	default:
		return ""
	}
}

func deadDropImageExtensionForMIME(mimeType string) string {
	clean := strings.ToLower(strings.TrimSpace(strings.Split(strings.TrimSpace(mimeType), ";")[0]))
	switch clean {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func validateDeadDropReturnedImageBinary(data []byte, fileName, mimeType string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("no binary file returned")
	}
	mimeExt := deadDropImageExtensionForMIME(mimeType)
	if mimeExt == "" {
		if strings.TrimSpace(mimeType) == "" {
			return "", errors.New("returned file is missing image MIME type")
		}
		return "", errors.New("returned file is not an allowed image type")
	}
	namedExt := deadDropPreferredImageExtension(filepath.Ext(strings.TrimSpace(fileName)))
	if namedExt != "" && !deadDropAllowedImageExtension(namedExt) {
		return "", errors.New("returned file is not an allowed image type")
	}
	if namedExt != "" && deadDropCanonicalImageExt(namedExt) != deadDropCanonicalImageExt(mimeExt) {
		return "", errors.New("MIME type does not match extension")
	}
	if namedExt != "" {
		return namedExt, nil
	}
	return mimeExt, nil
}

func validateDeadDropReturnedImageBinaryForSource(data []byte, fileName, mimeType, sourceExt string) error {
	sourceExt = strings.ToLower(strings.TrimSpace(sourceExt))
	if sourceExt == "" || sourceExt == "." {
		return errors.New("DeadDrop image source is missing an extension")
	}
	if !deadDropAllowedImageExtension(sourceExt) && sourceExt != ".jpeg" {
		return errors.New("DeadDrop image source extension is not supported")
	}
	returnedExt, err := validateDeadDropReturnedImageBinary(data, fileName, mimeType)
	if err != nil {
		return err
	}
	if deadDropCanonicalImageExt(returnedExt) != deadDropCanonicalImageExt(sourceExt) {
		return fmt.Errorf("returned image type must preserve original DeadDrop extension %s", sourceExt)
	}
	return nil
}

func deadDropFormatMismatchLogMessage(sourceKind string, err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(sourceKind))
	formatMismatch := false
	if kind == "image" {
		formatMismatch = strings.Contains(message, "binary") ||
			strings.Contains(message, "image") ||
			strings.Contains(message, "mime") ||
			strings.Contains(message, "extension") ||
			strings.Contains(message, "file_content")
	} else {
		formatMismatch = strings.Contains(message, "file_content is required") ||
			strings.Contains(message, "binary") ||
			strings.Contains(message, "image") ||
			strings.Contains(message, "mime")
	}
	if !formatMismatch {
		return ""
	}
	if kind == "" {
		kind = "unknown"
	}
	return fmt.Sprintf("returned file format did not match original DeadDrop format (source kind: %s): %v", kind, err)
}

func buildDeadDropAIContextPayload(currentAIContext string, resp deadDropResponsePayload) string {
	ctx, ok := parseAIContextText(currentAIContext)
	if !ok {
		ctx = defaultAIContext()
	}
	if len(resp.ImprovementsMade) > 0 {
		merged := append([]string{}, resp.ImprovementsMade...)
		merged = append(merged, ctx.PriorChanges...)
		ctx.PriorChanges = trimAIContextStringList(merged, aiContextMaxEntriesPerField, aiContextMaxEntryLength)
	}
	if note := strings.TrimSpace(resp.HandoffNotes); note != "" {
		merged := append([]string{note}, ctx.RisksConstraints...)
		ctx.RisksConstraints = trimAIContextStringList(merged, aiContextMaxEntriesPerField, aiContextMaxEntryLength)
	}
	return formatAIContextObject(ctx)
}

func deadDropFailureReason(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "unknown failure"
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "no binary file returned") {
		return "no binary file returned"
	}
	if strings.Contains(lower, "missing image mime type") {
		return "returned file is missing image MIME type"
	}
	if strings.Contains(lower, "not an allowed image type") {
		return "returned file is not an allowed image type"
	}
	if strings.Contains(lower, "mime type does not match extension") {
		return "MIME type does not match extension"
	}
	if strings.Contains(lower, "file_content") {
		return "missing or unusable returned file"
	}
	if strings.Contains(lower, "invalid deaddrop response") {
		return "malformed response"
	}
	if strings.Contains(lower, "request canceled") {
		return "request canceled"
	}
	return message
}

func summarizeDeadDropImprovements(items []string) string {
	trimmed := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			trimmed = append(trimmed, item)
		}
	}
	if len(trimmed) == 0 {
		return ""
	}
	return previewForLog(strings.Join(trimmed, "; "), 220)
}

func (a *App) readDeadDropSourceFile(projectName string) (string, string, string, []byte, error) {
	sourceRel, sourceKind, err := a.ensureLatestDeadDropRevisionSource(projectName)
	if err != nil {
		return "", "", "", nil, err
	}
	full, err := safeJoin(a.cfg.WorkRoot, sourceRel)
	if err != nil {
		return "", "", "", nil, err
	}
	data, err := readFileUnderRoot(a.cfg.WorkRoot, full)
	if err != nil {
		return "", "", "", nil, err
	}
	contentType := detectContentType(sourceRel, data)
	if strings.TrimSpace(sourceKind) == "" {
		sourceKind = deadDropSourceKind(sourceRel)
	}
	return sourceRel, sourceKind, contentType, data, nil
}

func (a *App) activeDeadDropBuilders() ([]ModelConfig, error) {
	a.mu.RLock()
	reviewerID := a.reviewerID
	a.mu.RUnlock()
	builders := []ModelConfig{}
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		a.mu.RLock()
		enabled := a.toggles[modelID]
		a.mu.RUnlock()
		if modelID == reviewerID || !enabled {
			continue
		}
		builders = append(builders, model)
	}
	if len(builders) == 0 {
		return nil, errors.New("You must have at least one enabled model.")
	}
	for _, model := range builders {
		if normalizePromptMode(model.PromptMode) == promptModeNone {
			return nil, errors.New("DeadDrop requires all active builders to use Low or Balanced system prompt mode.")
		}
	}
	waves := buildExecutionWaves(builders)
	for _, wave := range waves {
		if len(wave.BuilderIDs) > 1 {
			return nil, errors.New("DeadDrop does not support multi-model waves.")
		}
	}
	return builders, nil
}

func formatModelLabelList(labels []string) string {
	cleaned := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		cleaned = append(cleaned, label)
	}
	switch len(cleaned) {
	case 0:
		return "No models"
	case 1:
		return cleaned[0]
	case 2:
		return cleaned[0] + " and " + cleaned[1]
	default:
		return strings.Join(cleaned[:len(cleaned)-1], ", ") + ", and " + cleaned[len(cleaned)-1]
	}
}

func deadDropBuildersMissingCapability(builders []ModelConfig, supported func(ModelConfig) bool) []string {
	labels := make([]string, 0)
	for _, model := range builders {
		if supported(model) {
			continue
		}
		label := strings.TrimSpace(model.Label)
		if label == "" {
			label = fmt.Sprintf("Model %d", model.ID)
		}
		labels = append(labels, label)
	}
	return labels
}

func (a *App) validateDeadDropExecutionRequest(projectName string, req deadDropExecuteRequest) (string, string, []ModelConfig, []executionWave, error, int) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return "", "", nil, nil, errors.New("Select an active project first."), http.StatusBadRequest
	}
	if a.waveExecutionInProgress(projectName) {
		return "", "", nil, nil, errors.New("A run is already active for this project. Merge the current wave or press Emergency Stop before starting DeadDrop."), http.StatusConflict
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", "", nil, nil, errors.New("You must supply a prompt to tell the Builder AIs what they will revise and build on."), http.StatusBadRequest
	}
	sourceRel, sourceKind, _, _, err := a.readDeadDropSourceFile(projectName)
	if err != nil {
		return "", "", nil, nil, errors.New("You must upload or put a DeadDrop.<ext> file in your projectwork folder."), http.StatusBadRequest
	}
	builders, err := a.activeDeadDropBuilders()
	if err != nil {
		return "", "", nil, nil, err, http.StatusBadRequest
	}
	if strings.EqualFold(strings.TrimSpace(sourceKind), "image") {
		missingImageIn := deadDropBuildersMissingCapability(builders, func(model ModelConfig) bool {
			return model.Capabilities.SupportsImageIn
		})
		if len(missingImageIn) > 0 {
			return "", "", nil, nil, fmt.Errorf("DeadDrop image run blocked: %s must enable Accept Images.", formatModelLabelList(missingImageIn)), http.StatusBadRequest
		}
		missingBinaryOut := deadDropBuildersMissingCapability(builders, func(model ModelConfig) bool {
			return model.Capabilities.SupportsBinaryOut
		})
		if len(missingBinaryOut) > 0 {
			return "", "", nil, nil, fmt.Errorf("DeadDrop image run blocked: %s must enable Return Binary Outputs.", formatModelLabelList(missingBinaryOut)), http.StatusBadRequest
		}
	}
	waves := buildExecutionWaves(builders)
	if len(waves) == 0 {
		return "", "", nil, nil, errors.New("No populated Builder waves are available for DeadDrop."), http.StatusBadRequest
	}
	return sourceRel, sourceKind, builders, waves, nil, http.StatusOK
}

func (a *App) startDeadDropExecution(projectName string, req deadDropExecuteRequest, source executionSourceInfo) (executeResponse, error, int) {
	projectName = strings.TrimSpace(projectName)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.LoopCount = normalizeLoopCount(req.LoopCount)
	req.StopScore = normalizeDeadDropStopScore(req.StopScore)
	req.RevisionLevel = normalizeDeadDropRevisionLevel(req.RevisionLevel)
	sourceRel, sourceKind, builders, waves, execErr, status := a.validateDeadDropExecutionRequest(projectName, req)
	if execErr != nil {
		return executeResponse{}, execErr, status
	}
	firstWave := waves[0]
	started := append([]string(nil), firstWave.BuilderLabels...)
	queued := []string{}
	for _, wave := range waves[1:] {
		queued = append(queued, wave.BuilderLabels...)
	}
	state := waveExecutionState{
		ProjectName:             projectName,
		ExecutionID:             fmt.Sprintf("deaddrop-%s-%d", projectName, time.Now().UTC().UnixNano()),
		RootPrompt:              req.Prompt,
		ContextFiles:            []string{sourceRel},
		Waves:                   waves,
		CurrentIndex:            0,
		CurrentWave:             firstWave.Number,
		CurrentPromptSource:     "deaddrop",
		CurrentContextFilesUsed: 1,
		LoopCount:               req.LoopCount,
		LoopsRemaining:          req.LoopCount,
		CycleNumber:             1,
		AwaitingMerge:           false,
		StartedAt:               time.Now().UTC().Format(time.RFC3339),
	}
	a.mu.Lock()
	a.setWaveExecutionLocked(projectName, state)
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, firstWave.Number, "running", withWaveProgress("DeadDrop Running", state.CurrentIndex, len(state.Waves)), "deaddrop", 1))
	a.mu.Unlock()
	logPrefix := "DeadDrop started"
	if source.TriggerType == "timer" {
		logPrefix = fmt.Sprintf("Timer trigger started DeadDrop outfit %s (%s)", strings.TrimSpace(source.OutfitID), strings.TrimSpace(source.OutfitName))
	} else if source.TriggerType == "webhook" {
		logPrefix = fmt.Sprintf("Webhook trigger started DeadDrop outfit %s (%s)", strings.TrimSpace(source.OutfitID), strings.TrimSpace(source.OutfitName))
	}
	a.logf("system", "info", "%s for project %s. source=%s kind=%s first_wave=%d total_waves=%d loops=%d builders=%d", logPrefix, projectName, sourceRel, sourceKind, firstWave.Number, len(waves), state.LoopCount, len(builders))
	go a.runDeadDropExecution(projectName, state.ExecutionID, req.Prompt, sourceRel, sourceKind, builders, req.StopScore, req.RevisionLevel)
	return executeResponse{Started: started, Skipped: []string{}, WaveStarted: firstWave.Number, TotalWaves: len(waves), RemainingWaves: remainingWaveNumbers(waves, 1), QueuedBuilders: queued, ContextFilesUsed: 1}, nil, http.StatusOK
}

func (a *App) handleDeadDropExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req deadDropExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	a.invalidateDeadDropStatusCache(projectName)
	resp, execErr, status := a.startDeadDropExecution(projectName, req, executionSourceInfo{TriggerType: "manual"})
	if execErr != nil {
		http.Error(w, execErr.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) deadDropNextActiveIndex(waves []executionWave, active map[string]ModelConfig, start int) (int, executionWave, bool) {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(waves); i++ {
		for _, id := range waves[i].BuilderIDs {
			if _, ok := active[id]; ok {
				return i, waves[i], true
			}
		}
	}
	return 0, executionWave{}, false
}

func (a *App) advanceDeadDropExecution(projectName, executionID string, active map[string]ModelConfig) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.waveExecutionsByProject[projectName]
	if !ok || strings.TrimSpace(state.ExecutionID) != strings.TrimSpace(executionID) {
		return false, nil
	}
	if len(active) == 0 {
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, state.CurrentWave, "complete", withWaveProgress("DeadDrop Complete", state.CurrentIndex, len(state.Waves)), "deaddrop", 1))
		return false, nil
	}
	if nextIndex, nextWave, ok := a.deadDropNextActiveIndex(state.Waves, active, state.CurrentIndex+1); ok {
		state.CurrentIndex = nextIndex
		state.CurrentWave = nextWave.Number
		state.CurrentPromptSource = "deaddrop"
		state.CurrentContextFilesUsed = 1
		a.setWaveExecutionLocked(projectName, state)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, nextWave.Number, "running", withWaveProgress("DeadDrop Running", state.CurrentIndex, len(state.Waves)), "deaddrop", 1))
		return true, nil
	}
	if state.LoopsRemaining > 0 {
		state.LoopsRemaining--
		state.CycleNumber++
		if nextIndex, nextWave, ok := a.deadDropNextActiveIndex(state.Waves, active, 0); ok {
			state.CurrentIndex = nextIndex
			state.CurrentWave = nextWave.Number
			state.CurrentPromptSource = "deaddrop"
			state.CurrentContextFilesUsed = 1
			a.setWaveExecutionLocked(projectName, state)
			a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, nextWave.Number, "running", withWaveProgress("DeadDrop Running", state.CurrentIndex, len(state.Waves)), "deaddrop", 1))
			return true, nil
		}
	}
	a.clearWaveExecutionLocked(projectName)
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, state.CurrentWave, "complete", withWaveProgress("DeadDrop Complete", state.CurrentIndex, len(state.Waves)), "deaddrop", 1))
	return false, nil
}

func (a *App) writeDeadDropAIContextForModel(model ModelConfig, projectName, aiContext string) error {
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		return err
	}
	ctx, ok := parseAIContextText(aiContext)
	if !ok {
		ctx = defaultAIContext()
	}
	return atomicWriteFile(filepath.Join(metaRoot, "ai_context.json"), []byte(formatAIContextObject(ctx)), 0o644)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (a *App) applyDeadDropRevision(projectName, sourceRel, sourceKind string, resp deadDropResponsePayload, returnedBinary []byte, returnedBinaryName, returnedBinaryMIME string) (string, string, int, error) {
	sourceFull, err := safeJoin(a.cfg.WorkRoot, sourceRel)
	if err != nil {
		return "", "", 0, err
	}
	ext := filepath.Ext(sourceFull)
	if ext == "" || ext == "." {
		return "", "", 0, errors.New("DeadDrop source file is missing an extension")
	}
	var data []byte
	switch strings.ToLower(strings.TrimSpace(sourceKind)) {
	case "image":
		if err := validateDeadDropReturnedImageBinaryForSource(returnedBinary, returnedBinaryName, returnedBinaryMIME, ext); err != nil {
			return "", "", 0, err
		}
		data = append([]byte(nil), returnedBinary...)
	default:
		data = []byte(derefString(resp.FileContent))
	}
	deadDropRoot, err := a.deadDropRoot(projectName)
	if err != nil {
		return "", "", 0, err
	}
	if err := os.MkdirAll(deadDropRoot, 0o755); err != nil {
		return "", "", 0, err
	}
	revisionNumber, err := a.nextDeadDropRevisionNumber(projectName)
	if err != nil {
		return "", "", 0, err
	}
	snapshotName := fmt.Sprintf("DeadDrop_%d%s", revisionNumber, ext)
	snapshotFull, err := safeJoin(deadDropRoot, snapshotName)
	if err != nil {
		return "", "", 0, err
	}
	if err := atomicWriteFile(snapshotFull, data, 0o644); err != nil {
		return "", "", 0, err
	}
	newSourceName := fmt.Sprintf("DeadDrop%s", ext)
	newSourceFull, err := safeJoin(deadDropRoot, newSourceName)
	if err != nil {
		return "", "", 0, err
	}
	if err := atomicWriteFile(newSourceFull, data, 0o644); err != nil {
		return "", "", 0, err
	}
	snapshotRel, _ := filepath.Rel(a.cfg.WorkRoot, snapshotFull)
	return filepath.ToSlash(snapshotRel), filepath.ToSlash(snapshotRel), revisionNumber, nil
}

func (a *App) runDeadDropModelStep(model ModelConfig, projectName, executionID, prompt, sourceRel, sourceKind, currentAIContext string, stopScore int, revisionLevel string, cycleNumber, waveNumber int) deadDropStepResult {
	result := deadDropStepResult{ModelID: modelIDString(model.ID), ModelLabel: model.Label, Dropout: true}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(result.ModelID, projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(result.ModelID, executionID)
		a.mu.Unlock()
		cancel()
	}()
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		result.Err = err
		return result
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		result.Err = err
		return result
	}
	if strings.TrimSpace(currentAIContext) == "" {
		currentAIContext = string(defaultAIContextJSON())
	}
	if err := a.writeDeadDropAIContextForModel(model, projectName, currentAIContext); err != nil {
		a.logf(result.ModelID, "warn", "DeadDrop could not seed ai_context.json before wave %d loop %d: %v", waveNumber, cycleNumber, err)
	}
	userContext, _ := os.ReadFile(filepath.Join(metaRoot, "user_context.json"))
	transportProfile := adapters.ResolveTransportProfile(toAdapterModelConfig(model))
	contextMessage, _, err := buildMultimodalContextMessage(a.cfg.WorkRoot, []string{sourceRel}, "CURRENT DEADDROP FILE\nThis is the current source file for this DeadDrop pass. All edits must be made only to this uploaded DeadDrop file.", transportProfile, true, builderContextMaxTextBytes)
	if err != nil {
		result.Err = err
		return result
	}
	requestPayload, err := buildDeadDropRequestPayload(a.cfg, model, prompt, string(userContext), currentAIContext, sourceRel, sourceKind, stopScore, revisionLevel, contextMessage)
	if err != nil {
		result.Err = err
		return result
	}
	adapterResp, err := a.executeAdapterResponse(ctx, model, requestPayload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			result.Err = errors.New("request canceled")
		} else {
			result.Err = err
		}
		return result
	}
	responseText := strings.TrimSpace(adapterResp.Text)
	result.ReturnedBinaryData = append([]byte(nil), adapterResp.FileData...)
	result.ReturnedBinaryName = strings.TrimSpace(adapterResp.FileName)
	result.ReturnedBinaryMIME = strings.TrimSpace(adapterResp.FileMIMEType)
	rawDir := deadDropResponsesDir(metaRoot)
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		result.Err = err
		return result
	}
	timestamp := time.Now().Format("20060102_150405")
	rawFile := filepath.Join(rawDir, "deaddrop_response_"+timestamp+".md")
	if writeErr := os.WriteFile(rawFile, []byte(responseText), 0o644); writeErr == nil {
		result.ResponseRawFile = rawFile
		if err := pruneResponseArchive(rawDir, a.cfg.MaxResponseHistory, "deaddrop_response_", ".md", nil); err != nil {
			a.logf(result.ModelID, "warn", "Failed trimming DeadDrop response history: %v", err)
		}
	}
	parsed, err := parseDeadDropResponse(responseText)
	if err != nil {
		result.Err = fmt.Errorf("invalid DeadDrop response: %w", err)
		return result
	}
	if err := validateDeadDropResponse(parsed, sourceRel, sourceKind, adapterResp.FileData, adapterResp.FileName, adapterResp.FileMIMEType); err != nil {
		if rejectMessage := deadDropFormatMismatchLogMessage(sourceKind, err); rejectMessage != "" {
			a.logf(result.ModelID, "error", "**DeadDrop rejected:** %s", rejectMessage)
		}
		result.Err = fmt.Errorf("invalid DeadDrop response: %w", err)
		return result
	}
	result.Score = parsed.Score
	result.Improvements = append([]string(nil), parsed.ImprovementsMade...)
	result.HandoffJSON = buildDeadDropAIContextPayload(currentAIContext, parsed)
	if err := a.writeDeadDropAIContextForModel(model, projectName, result.HandoffJSON); err != nil {
		a.logf(result.ModelID, "warn", "DeadDrop could not save ai_context.json after wave %d loop %d: %v", waveNumber, cycleNumber, err)
	}
	if parsed.ReturnedFile {
		newSource, snapshotRel, revisionNumber, applyErr := a.applyDeadDropRevision(projectName, sourceRel, sourceKind, parsed, adapterResp.FileData, adapterResp.FileName, adapterResp.FileMIMEType)
		if applyErr != nil {
			if rejectMessage := deadDropFormatMismatchLogMessage(sourceKind, applyErr); rejectMessage != "" {
				a.logf(result.ModelID, "error", "**DeadDrop rejected:** %s", rejectMessage)
			}
			result.Err = fmt.Errorf("failed applying DeadDrop revision: %w", applyErr)
			return result
		}
		result.ReturnedFile = true
		result.NewSourcePath = newSource
		result.SnapshotPath = snapshotRel
		result.RevisionNumber = revisionNumber
	}
	if parsed.Score >= float64(stopScore) || !parsed.ReturnedFile {
		result.Dropout = true
	} else {
		result.Dropout = false
	}
	return result
}

func (a *App) runDeadDropExecution(projectName, executionID, prompt, sourceRel, sourceKind string, builders []ModelConfig, stopScore int, revisionLevel string) {
	active := map[string]ModelConfig{}
	for _, model := range builders {
		active[modelIDString(model.ID)] = model
	}
	currentSourceRel := sourceRel
	currentAIContext := string(defaultAIContextJSON())
	for {
		if !a.isWaveExecutionCurrent(projectName, executionID) {
			return
		}
		state, ok := a.currentWaveExecution(projectName)
		if !ok || strings.TrimSpace(state.ExecutionID) != strings.TrimSpace(executionID) {
			return
		}
		if len(active) == 0 {
			a.mu.Lock()
			if liveState, ok := a.waveExecutionsByProject[projectName]; ok && strings.TrimSpace(liveState.ExecutionID) == strings.TrimSpace(executionID) {
				a.clearWaveExecutionLocked(projectName)
				a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, liveState, liveState.CurrentWave, "complete", withWaveProgress("DeadDrop Complete", liveState.CurrentIndex, len(liveState.Waves)), "deaddrop", 1))
			}
			a.mu.Unlock()
			a.logf("system", "info", "DeadDrop completed for project %s because all builders dropped out.", projectName)
			a.finalizeActiveDeadDropOutfitRunCompleted(projectName, "DeadDrop completed after all builders dropped out.")
			return
		}
		index, wave, found := a.deadDropNextActiveIndex(state.Waves, active, state.CurrentIndex)
		if !found {
			advanced, err := a.advanceDeadDropExecution(projectName, executionID, active)
			if err != nil {
				a.logf("system", "error", "DeadDrop advance failed for project %s: %v", projectName, err)
				a.finalizeActiveDeadDropOutfitRunFailed(projectName, err.Error())
				return
			}
			if !advanced {
				summary := "DeadDrop completed because the configured loop count was exhausted."
				if len(active) == 0 {
					summary = "DeadDrop completed because every builder dropped out."
					a.logf("system", "info", "DeadDrop completed for project %s because every builder dropped out.", projectName)
				} else {
					a.logf("system", "info", "DeadDrop completed for project %s because the configured loop count was exhausted.", projectName)
				}
				a.finalizeActiveDeadDropOutfitRunCompleted(projectName, summary)
				return
			}
			continue
		}
		if index != state.CurrentIndex {
			a.mu.Lock()
			liveState, ok := a.waveExecutionsByProject[projectName]
			if !ok || strings.TrimSpace(liveState.ExecutionID) != strings.TrimSpace(executionID) {
				a.mu.Unlock()
				return
			}
			liveState.CurrentIndex = index
			liveState.CurrentWave = wave.Number
			liveState.CurrentPromptSource = "deaddrop"
			liveState.CurrentContextFilesUsed = 1
			a.setWaveExecutionLocked(projectName, liveState)
			a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, liveState, wave.Number, "running", withWaveProgress("DeadDrop Running", liveState.CurrentIndex, len(liveState.Waves)), "deaddrop", 1))
			a.mu.Unlock()
			state = liveState
		}
		modelID := wave.BuilderIDs[0]
		model, ok := active[modelID]
		if !ok {
			advanced, err := a.advanceDeadDropExecution(projectName, executionID, active)
			if err != nil {
				a.finalizeActiveDeadDropOutfitRunFailed(projectName, err.Error())
				return
			}
			if !advanced {
				a.finalizeActiveDeadDropOutfitRunCompleted(projectName, "DeadDrop completed.")
				return
			}
			continue
		}
		a.logf("system", "info", "DeadDrop loop %d wave %d started with %s on %s", state.CycleNumber, wave.Number, model.Label, filepath.Base(currentSourceRel))
		effectiveStopScore := effectiveDeadDropStopScore(stopScore, state.CycleNumber, state.CurrentIndex)
		if effectiveStopScore != normalizeDeadDropStopScore(stopScore) {
			a.logf("system", "info", "DeadDrop first wave requires score=100 before dropout so the user gets at least one revision attempt.")
		}
		step := a.runDeadDropModelStep(model, projectName, executionID, prompt, currentSourceRel, sourceKind, currentAIContext, effectiveStopScore, revisionLevel, state.CycleNumber, wave.Number)
		if !a.isWaveExecutionCurrent(projectName, executionID) {
			return
		}
		if step.Err != nil {
			a.logf(step.ModelID, "warn", "DeadDrop loop %d wave %d dropped %s after %s: %v", state.CycleNumber, wave.Number, step.ModelLabel, deadDropFailureReason(step.Err), step.Err)
			delete(active, step.ModelID)
		} else {
			if strings.TrimSpace(step.HandoffJSON) != "" {
				currentAIContext = step.HandoffJSON
			}
			fileState := "no-file"
			if step.ReturnedFile {
				fileState = "returned-file"
				currentSourceRel = step.NewSourcePath
				a.logf(step.ModelID, "info", "DeadDrop wrote revision %d for project %s at %s", step.RevisionNumber, projectName, step.SnapshotPath)
			}
			improvements := summarizeDeadDropImprovements(step.Improvements)
			a.logf(step.ModelID, "info", "DeadDrop loop %d wave %d grade=%.1f/100 stop=%d %s", state.CycleNumber, wave.Number, step.Score, effectiveStopScore, fileState)
			if improvements != "" {
				a.logf(step.ModelID, "info", "DeadDrop loop %d wave %d improvements=%s", state.CycleNumber, wave.Number, improvements)
			}
			if step.Dropout {
				delete(active, step.ModelID)
				a.logf("system", "info", "DeadDrop dropped %s from future loops after loop %d wave %d.", step.ModelLabel, state.CycleNumber, wave.Number)
			}
		}
		advanced, err := a.advanceDeadDropExecution(projectName, executionID, active)
		if err != nil {
			a.logf("system", "error", "DeadDrop advance failed for project %s: %v", projectName, err)
			a.finalizeActiveDeadDropOutfitRunFailed(projectName, err.Error())
			return
		}
		if !advanced {
			summary := "DeadDrop completed because the configured loop count was exhausted."
			if len(active) == 0 {
				summary = "DeadDrop completed because every builder dropped out."
				a.logf("system", "info", "DeadDrop completed for project %s because every builder dropped out.", projectName)
			} else {
				a.logf("system", "info", "DeadDrop completed for project %s because the configured loop count was exhausted.", projectName)
			}
			a.finalizeActiveDeadDropOutfitRunCompleted(projectName, summary)
			return
		}
	}
}

func sanitizeImportedFilename(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(filepath.Clean(filepath.FromSlash(name)))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "downloaded_file"
	}
	return name
}

func unzipArchiveInto(zipPath, dstRoot string) (int, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	count := 0
	for _, f := range zr.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if name == "" || name == "." || strings.HasPrefix(name, "..") || f.FileInfo().IsDir() || isZipSymlink(f) {
			continue
		}
		target, err := safeJoin(dstRoot, name)
		if err != nil {
			return count, err
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return count, err
		}
		if err := atomicWriteFileUnderRoot(dstRoot, target, data, 0o644); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func copyWorkingTreeInto(srcRoot, dstRoot string) (int, error) {
	files, err := collectWorkspaceFiles(srcRoot)
	if err != nil {
		return 0, err
	}
	count := 0
	for rel, data := range files {
		parts := strings.Split(rel, "/")
		skip := false
		for _, part := range parts {
			if part == ".git" {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		target, err := safeJoin(dstRoot, rel)
		if err != nil {
			return count, err
		}
		if err := atomicWriteFileUnderRoot(dstRoot, target, data, 0o644); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

func (a *App) activeProjectWorkRoot() (string, string, error) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		return "", "", err
	}
	projectRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return "", "", err
	}
	return projectName, projectRoot, nil
}

func workModeTmpWorkProjectRelPrefix() string {
	return workModeTmpWorkDirName
}

func workModeTmpWorkWorkRelPrefix(projectName string) string {
	return filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", workModeTmpWorkDirName))
}

func workModeTmpWorkProjectRoot(projectworkRoot string) (string, error) {
	return safeJoin(projectworkRoot, workModeTmpWorkDirName)
}

func cleanSlashPath(value string) string {
	clean := strings.ReplaceAll(strings.TrimSpace(filepath.ToSlash(value)), "\\", "/")
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" {
		return ""
	}
	clean = path.Clean(clean)
	if clean == "." {
		return ""
	}
	return clean
}

func parseWorkModeTmpWorkRel(workRel string) (projectName string, tmpRel string, ok bool) {
	clean := cleanSlashPath(workRel)
	if clean == "" {
		return "", "", false
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 4 || parts[0] != "projects" || parts[2] != "projectwork" || parts[3] != workModeTmpWorkDirName {
		return "", "", false
	}
	projectName = parts[1]
	if !isValidProjectName(projectName) {
		return "", "", false
	}
	if len(parts) == 4 {
		return projectName, "", true
	}
	tmpRel = path.Clean(strings.Join(parts[4:], "/"))
	if tmpRel == "." || tmpRel == "" || tmpRel == ".." || strings.HasPrefix(tmpRel, "../") || strings.HasPrefix(tmpRel, "/") {
		return "", "", false
	}
	return projectName, tmpRel, true
}

func isWorkModeTmpWorkWriteLockedState(state workModeSessionState) bool {
	if state.Mode != workModeModeObserverReview {
		return false
	}
	switch state.Status {
	case workModeStatusRunning, workModeStatusPausedAfterCurrent, workModeStatusFinalizing:
		return true
	default:
		return false
	}
}

func (a *App) isWorkModeTmpWorkWriteLocked(projectName string) bool {
	state, ok := a.getWorkModeSessionState(projectName)
	return ok && isWorkModeTmpWorkWriteLockedState(state)
}

func (a *App) rejectLockedTmpWorkMutation(workRel string) error {
	projectName, _, ok := parseWorkModeTmpWorkRel(workRel)
	if !ok {
		return nil
	}
	if a.isWorkModeTmpWorkWriteLocked(projectName) {
		return errors.New("tmp-work is read-only while Observer Review Mode is running or paused")
	}
	return nil
}

func normalizeWorkModeTmpWorkFileRel(raw, projectName string) (string, error) {
	parsedProject, tmpRel, ok := parseWorkModeTmpWorkRel(raw)
	if !ok {
		return "", errors.New("file must be inside tmp-work")
	}
	if parsedProject != projectName {
		return "", errors.New("tmp-work file must be inside the active project")
	}
	if tmpRel == "" {
		return "", errors.New("select a tmp-work file")
	}
	clean, err := normalizeWorkModeProjectworkRel(tmpRel, projectName)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(clean, workModeTmpWorkDirName+"/") || clean == workModeTmpWorkDirName {
		return "", errors.New("nested tmp-work paths are not allowed")
	}
	return clean, nil
}

func projectWorkRelForTmpWorkFile(projectName, tmpRel string) string {
	return filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", filepath.FromSlash(tmpRel)))
}

func tmpWorkRelForProjectWorkFile(projectName, tmpRel string) string {
	return filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", workModeTmpWorkDirName, filepath.FromSlash(tmpRel)))
}

func prefixedTmpWorkChangedPaths(projectName string, changed []string) []string {
	out := make([]string, 0, len(changed))
	seen := map[string]bool{}
	for _, rel := range changed {
		rel = cleanSlashPath(rel)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, path.Join(workModeTmpWorkDirName, rel))
	}
	return out
}

func prefixedTmpWorkDiffs(diffs []workModeDiffFile) []workModeDiffFile {
	out := make([]workModeDiffFile, 0, len(diffs))
	for _, diff := range diffs {
		diff.Path = path.Join(workModeTmpWorkDirName, cleanSlashPath(diff.Path))
		out = append(out, diff)
	}
	return out
}

func workModeExistingTmpWorkUpdateable(tmpWorkRoot string, ops []builderFileOp, projectName string) map[string]bool {
	updateable := map[string]bool{}
	for _, op := range ops {
		rel, err := normalizeWorkModeProjectworkRel(op.Path, projectName)
		if err == nil && rel != "" {
			updateable[rel] = true
		}
	}
	_ = filepath.WalkDir(tmpWorkRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || fullPath == tmpWorkRoot || entry == nil || entry.IsDir() || isSymlinkDirEntry(entry) {
			return nil
		}
		rel, err := filepath.Rel(tmpWorkRoot, fullPath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel != "" && !isHiddenWorkModePath(rel) {
			updateable[rel] = true
		}
		return nil
	})
	return updateable
}

func isWorkModeTmpWorkRelativePath(rel string) bool {
	rel = cleanSlashPath(rel)
	return rel == workModeTmpWorkDirName || strings.HasPrefix(rel, workModeTmpWorkDirName+"/")
}

func filterWorkModeTmpWorkDraftFileOps(ops []builderFileOp, projectName string) ([]builderFileOp, []string, []workModeBlockedFileOutput) {
	accepted := make([]builderFileOp, 0, len(ops))
	skipped := []string{}
	blocked := []workModeBlockedFileOutput{}
	for _, op := range ops {
		rel, err := normalizeWorkModeProjectworkRel(op.Path, projectName)
		if err == nil && isWorkModeTmpWorkRelativePath(rel) {
			reason := "Worker draft file ops must use intended project paths; tmp-work paths are managed by AgentGO"
			action := strings.ToLower(strings.TrimSpace(op.Action))
			skipped = append(skipped, rel+": "+reason)
			blockedOutput := workModeBlockedFileOutput{Path: rel, Action: action, Reason: reason}
			if strings.TrimSpace(op.Content) != "" {
				blockedOutput.Content = op.Content
			} else if strings.TrimSpace(op.ArtifactRef) != "" {
				blockedOutput.ContentOmitted = true
				blockedOutput.Content = "[artifact output omitted]"
			}
			blocked = append(blocked, blockedOutput)
			continue
		}
		accepted = append(accepted, op)
	}
	return accepted, skipped, blocked
}

func seedTmpWorkOverwriteSources(projectworkRoot, tmpWorkRoot, projectName string, ops []builderFileOp) error {
	for _, op := range ops {
		if !strings.EqualFold(strings.TrimSpace(op.Action), "overwrite") {
			continue
		}
		rel, err := normalizeWorkModeProjectworkRel(op.Path, projectName)
		if err != nil || rel == "" {
			continue
		}
		tmpTarget, err := safeJoin(tmpWorkRoot, rel)
		if err != nil {
			continue
		}
		if err := rejectSymlinkPath(tmpWorkRoot, tmpTarget); err != nil {
			continue
		}
		if _, err := os.Stat(tmpTarget); err == nil {
			continue
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		source, err := safeJoin(projectworkRoot, rel)
		if err != nil {
			continue
		}
		if err := rejectSymlinkPath(projectworkRoot, source); err != nil {
			continue
		}
		data, err := readFileUnderRoot(projectworkRoot, source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := writeFileUnderRoot(tmpWorkRoot, tmpTarget, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func workModeApplyFileOpsToTmpWork(projectworkRoot, projectName string, ops []builderFileOp, artifacts []builderArtifact, limits ProjectLimits) ([]string, []string, []workModeBlockedFileOutput, []workModeDiffFile, error) {
	tmpWorkRoot, err := workModeTmpWorkProjectRoot(projectworkRoot)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := os.MkdirAll(tmpWorkRoot, 0o755); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := rejectSymlinkPath(projectworkRoot, tmpWorkRoot); err != nil {
		return nil, nil, nil, nil, err
	}
	filteredOps, draftSkipped, draftBlocked := filterWorkModeTmpWorkDraftFileOps(ops, projectName)
	if err := seedTmpWorkOverwriteSources(projectworkRoot, tmpWorkRoot, projectName, filteredOps); err != nil {
		return nil, draftSkipped, draftBlocked, nil, err
	}
	updateable := workModeExistingTmpWorkUpdateable(tmpWorkRoot, filteredOps, projectName)
	changed, skipped, blocked, diffs, err := workModeApplyFileOps(tmpWorkRoot, projectName, filteredOps, artifacts, limits, updateable)
	skipped = append(draftSkipped, skipped...)
	blocked = append(draftBlocked, blocked...)
	if err != nil {
		return nil, skipped, blocked, diffs, err
	}
	return prefixedTmpWorkChangedPaths(projectName, changed), skipped, blocked, prefixedTmpWorkDiffs(diffs), nil
}

func pruneEmptyDirsUnderRoot(root string, start string) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	current, err := filepath.Abs(start)
	if err != nil {
		return
	}
	for current != rootAbs && strings.HasPrefix(current, rootAbs+string(os.PathSeparator)) {
		_ = os.Remove(current)
		current = filepath.Dir(current)
	}
}

func (a *App) mergeTmpWorkFile(projectName, tmpRel string) (string, string, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return "", "", err
	}
	tmpWorkRoot, err := workModeTmpWorkProjectRoot(projectworkRoot)
	if err != nil {
		return "", "", err
	}
	tmpRel, err = normalizeWorkModeProjectworkRel(tmpRel, projectName)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(tmpRel, workModeTmpWorkDirName+"/") || tmpRel == workModeTmpWorkDirName {
		return "", "", errors.New("invalid nested tmp-work path")
	}
	source, err := safeJoin(tmpWorkRoot, tmpRel)
	if err != nil {
		return "", "", err
	}
	if err := rejectSymlinkPath(tmpWorkRoot, source); err != nil {
		return "", "", err
	}
	info, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", errors.New("tmp-work file not found")
		}
		return "", "", err
	}
	if info.IsDir() {
		return "", "", errors.New("select a tmp-work file, not a folder")
	}
	data, err := readFileUnderRoot(tmpWorkRoot, source)
	if err != nil {
		return "", "", err
	}
	target, err := safeJoin(projectworkRoot, tmpRel)
	if err != nil {
		return "", "", err
	}
	if err := rejectSymlinkPath(projectworkRoot, target); err != nil {
		return "", "", err
	}
	if err := writeFileUnderRoot(projectworkRoot, target, data, 0o644); err != nil {
		return "", "", err
	}
	if err := removeFileUnderRoot(tmpWorkRoot, source); err != nil {
		return "", "", err
	}
	pruneEmptyDirsUnderRoot(tmpWorkRoot, filepath.Dir(source))
	return tmpWorkRelForProjectWorkFile(projectName, tmpRel), projectWorkRelForTmpWorkFile(projectName, tmpRel), nil
}

func (a *App) handleWorkModeTmpWorkMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workModeTmpWorkMergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if a.isWorkModeTmpWorkWriteLocked(projectName) {
		http.Error(w, "tmp-work is read-only while Observer Review Mode is running or paused", http.StatusConflict)
		return
	}
	tmpRel, err := normalizeWorkModeTmpWorkFileRel(req.Path, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sourcePath, targetPath, err := a.mergeTmpWorkFile(projectName, tmpRel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		a.logf("system", "warn", "Could not sync tmp-work merge into active Builder workspace: %v", err)
	}
	a.logf("system", "info", "tmp-work merged: %s -> %s", sourcePath, targetPath)
	writeJSON(w, http.StatusOK, workModeTmpWorkMergeResponse{OK: true, SourcePath: sourcePath, TargetPath: targetPath, MergedFiles: []string{targetPath}, Message: "tmp-work file merged."})
}

func (a *App) handleWorkModeTmpWorkMergeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if a.isWorkModeTmpWorkWriteLocked(projectName) {
		http.Error(w, "tmp-work is read-only while Observer Review Mode is running or paused", http.StatusConflict)
		return
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tmpWorkRoot, err := workModeTmpWorkProjectRoot(projectworkRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(projectworkRoot, tmpWorkRoot); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	merged := []string{}
	if _, err := os.Stat(tmpWorkRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, workModeTmpWorkMergeResponse{OK: true, MergedFiles: merged, Message: "tmp-work is empty."})
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rels := []string{}
	walkErr := filepath.WalkDir(tmpWorkRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || fullPath == tmpWorkRoot || entry == nil {
			return walkErr
		}
		if isSymlinkDirEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(tmpWorkRoot, fullPath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "" || isHiddenWorkModePath(rel) || strings.EqualFold(path.Base(rel), "project.json") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if walkErr != nil {
		http.Error(w, walkErr.Error(), http.StatusBadRequest)
		return
	}
	sort.Strings(rels)
	for _, rel := range rels {
		_, targetPath, err := a.mergeTmpWorkFile(projectName, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		merged = append(merged, targetPath)
	}
	if len(merged) > 0 {
		if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
			a.logf("system", "warn", "Could not sync tmp-work merge-all into active Builder workspace: %v", err)
		}
	}
	a.logf("system", "info", "tmp-work merge-all completed for project %s; merged=%d", projectName, len(merged))
	writeJSON(w, http.StatusOK, workModeTmpWorkMergeResponse{OK: true, MergedFiles: merged, Message: fmt.Sprintf("Merged %d tmp-work file(s).", len(merged))})
}

func (a *App) activeProjectRoot() (string, string, error) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		return "", "", err
	}
	projectRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", "", err
	}
	return projectName, projectRoot, nil
}

func (a *App) handleProjectImportUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, projectRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer src.Close()
	name := sanitizeImportedFilename(header.Filename)
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		tmp, err := os.CreateTemp("", "agentgo-upload-*.zip")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpPath := tmp.Name()
		if _, err := io.Copy(tmp, src); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmp.Close()
		defer os.Remove(tmpPath)
		count, err := unzipArchiveInto(tmpPath, projectRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.logf("system", "info", "ZIP imported into active project: %s (%d files)", projectName, count)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "kind": "zip"})
		return
	}
	data, err := io.ReadAll(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	target, err := safeJoin(projectRoot, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := atomicWriteFileUnderRoot(projectRoot, target, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "File uploaded into active project: %s (%s)", projectName, name)
	relPath := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", name))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": 1, "kind": "file", "path": relPath, "deadDropCandidate": isExactDeadDropCandidateName(name)})
}

func (a *App) handleProjectImportGit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, projectRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	var req projectImportGitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	repoURL := strings.TrimSpace(req.RepoURL)
	if repoURL == "" {
		http.Error(w, "repoUrl is required", http.StatusBadRequest)
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		http.Error(w, "git is not installed", http.StatusBadRequest)
		return
	}
	tmpDir, err := os.MkdirTemp("", "agentgo-git-import-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)
	cloneDir := filepath.Join(tmpDir, "repo")
	args := []string{"clone", "--depth", "1"}
	if branch := strings.TrimSpace(req.Branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, cloneDir)
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		http.Error(w, message, http.StatusBadGateway)
		return
	}
	count, err := copyWorkingTreeInto(cloneDir, projectRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Git import completed for active project: %s (%d files)", projectName, count)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count})
}

func filenameFromURLResponse(resourceURL string, resp *http.Response) string {
	if cd := strings.TrimSpace(resp.Header.Get("Content-Disposition")); cd != "" {
		lower := strings.ToLower(cd)
		if idx := strings.Index(lower, "filename="); idx >= 0 {
			name := strings.TrimSpace(cd[idx+len("filename="):])
			name = strings.Trim(name, `"'; `)
			if name != "" {
				return sanitizeImportedFilename(name)
			}
		}
	}
	if parsed, err := url.Parse(resourceURL); err == nil {
		base := path.Base(parsed.Path)
		if base != "." && base != "/" && strings.TrimSpace(base) != "" {
			return sanitizeImportedFilename(base)
		}
		host := strings.ReplaceAll(parsed.Hostname(), ".", "_")
		if host == "" {
			host = "downloaded_file"
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		switch {
		case strings.Contains(ct, "html"):
			return host + ".html"
		case strings.Contains(ct, "json"):
			return host + ".json"
		case strings.Contains(ct, "javascript"):
			return host + ".js"
		case strings.Contains(ct, "css"):
			return host + ".css"
		case strings.Contains(ct, "png"):
			return host + ".png"
		case strings.Contains(ct, "jpeg"):
			return host + ".jpg"
		case strings.Contains(ct, "gif"):
			return host + ".gif"
		case strings.Contains(ct, "svg"):
			return host + ".svg"
		default:
			return host + ".txt"
		}
	}
	return "downloaded_file.txt"
}

func (a *App) handleProjectImportURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, projectRoot, err := a.activeProjectWorkRoot()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	var req projectImportURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	resourceURL := strings.TrimSpace(req.ResourceURL)
	if resourceURL == "" {
		http.Error(w, "resourceUrl is required", http.StatusBadRequest)
		return
	}
	if !strings.Contains(resourceURL, "://") {
		resourceURL = "https://" + resourceURL
	}
	resp, err := http.Get(resourceURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("remote server returned %s", resp.Status), http.StatusBadGateway)
		return
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name := filenameFromURLResponse(resourceURL, resp)
	target, err := safeJoin(projectRoot, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := atomicWriteFileUnderRoot(projectRoot, target, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Web resource fetched into active project: %s (%s)", projectName, name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "filename": name})
}

func buildProjectZip(srcRoot string, writer io.Writer) error {
	zw := zip.NewWriter(writer)
	defer zw.Close()
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isSymlinkDirEntry(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "" || rel == "." {
			return nil
		}
		fh, err := zw.Create(rel)
		if err != nil {
			return err
		}
		if err := rejectSymlinkPath(srcRoot, path); err != nil {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(fh, file)
		return err
	})
}

func (a *App) handleProjectDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, projectRoot, err := a.activeProjectRoot()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s.zip", projectName, timestamp)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	a.logf("system", "info", "Project exported: %s (%s)", projectName, filename)
	if err := buildProjectZip(projectRoot, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

const (
	cypherManifestFileName            = "Cypher.json"
	wireTapFileName                   = "WireTap.json"
	wireTapSystemID                   = "wiretap"
	wireTapDefaultMaxEntries          = 200
	wireTapHardMaxEntries             = 5000
	wireTapDefaultRuntimeSliceEntries = 75
	wireTapMaxRuntimeSliceEntries     = 150
	wireTapHardMaxRuntimeSliceEntries = 150
	wireTapBuildBatchSize             = 50
	wireTapMaxBuildRounds             = 12
	cypherSystemID                    = "cypher"
	cypherActionMaxRequestedBytes     = 300000
	cypherActionMaxRunLogEntries      = 20
	cypherActionMaxRunLogChars        = 24000
	cypherMaxEnrichmentRounds         = 50
	cypherHashPrefix                  = "sha256:"
	cypherDefaultContentDomain        = "mixed"
	cypherPositionEncoding            = "UTF-8-byte-offset"
	cypherDefaultInstructions         = "Cypher is AgentGO's project map. Use it to request relevant files before editing. Non-code summaries use Ugg Protocol. Preserve code, quotes, and literal blocks exactly."
)

const cypherPurposePrompt = `CYPHER PURPOSE
Cypher is AgentGO's project map. Cypher summaries are retrieval/routing metadata. They must help future AI Builders decide whether to request a file for a later user task. Summarize what each file contains and controls, not whether it is good. Capture enough named, structural, functional, and conceptual hooks that indirect user questions can find the file later. For every attached file, identify the strongest routing hooks present in actual file content: named entities, exported symbols, functions, classes, components, handlers, routes, APIs, UI controls, user actions, events, payloads, state, errors, config fields, places, characters, objects, lore terms, timeline events, relationships, continuity facts, dependencies, referenced files, data flow, side effects, responsibilities, constraints, and distinctive secondary details when useful for retrieval. Do not produce filename-only summaries. Do not summarize only the main idea if important routing hooks would be lost. Do not review quality, style, or correctness unless the file itself is about those things.`

const cypherReviewStatusPrompt = `CYPHER REVIEW STATUS
AgentGO owns ai_reviewed and sets it after verifying file content was reviewed. Do not update ai_reviewed.
summary_status:
- "complete" when a usable routing summary was written.
- "empty" when no summary exists yet.
- "skipped" when file was intentionally not reviewed.
- "failed" when review was attempted but failed.
- Preserve "stale" unless you reviewed current file content; then replace with "complete", "skipped", or "failed".`

const cypherUggProtocolPrompt = `SYSTEM INSTRUCTION: CYPHER UGG PROTOCOL

Role:
Use Ugg Protocol ONLY when writing Cypher summary metadata for changed, created, deleted, or reviewed files. Normal project files, code comments, user-facing prose, commit-style notes, explanations, and normal Builder output must remain normal professional text/code in the style requested by the user. Smart dense style: maximum meaning, minimum filler.

Core Rules:
- Drop articles, filler, pleasantries, hedging, apologies.
- Fragments OK.
- Short words when accurate.
- Technical terms exact.
- Names, paths, APIs, functions, classes, characters, places exact.
- Prefer noun/verb fragments.
- Avoid "is/are" unless clarity requires it.

Summary Style:
- For JSON arrays: use short bullet-like entries.
- For JSON strings: use dense fragment text.
- Code file pattern: [thing] [action/purpose]. [risk/dependency]. [next useful fact].
- Content file pattern: [symbol/entity/thing] [action/state/purpose]. [result/risk]. [next useful fact].

Examples:
Bad: "The protagonist discovers the magic stone, which helps him..."
Good: "Boy find stone. Stop bad wizard."

Code Integrity Firewall:
- Never rewrite code, quotes, or literal blocks while summarizing.
- Preserve exact syntax, spacing, comments, and quoted text when included.
- Code remains modern, precise, professional.

Safety:
- Only update Cypher descriptive fields.
- Never change AgentGO-owned fields: paths, hashes, sizes, exclusions, transfer permissions, project root, version, generator fields.
- If file unreadable, corrupted, secret-like, or security-sensitive, mark summary with concise warning and request no unsafe content.`

const cypherEnrichmentSystemPrompt = cypherUggProtocolPrompt + "\n\n" + `CYPHER PHASE 1 ENRICHMENT MODE
You receive one project file. Return only the AI-owned metadata object AgentGO asks for. AgentGO owns file identity, file ordering, saved manifest fields, review status, and all safety metadata.

Return strict valid JSON only. No markdown fences. No prose before or after JSON. No trailing commas. Use double quotes for all JSON strings and property names.

Required JSON shape:
{
  "summary": "...",
  "anchors": [],
  "symbols": [],
  "continuity": {
    "characters": [],
    "relationships": [],
    "timeline_events": [],
    "locations": [],
    "rules": [],
    "contradictions": []
  }
}

Field intent:
- summary: Dense, useful routing summary of what the provided file contains, controls, defines, or changes. Include exact names, APIs, functions, classes, UI controls, settings, characters, places, lore terms, events, constraints, and mechanics that help a later Builder decide whether this file matters. Summaries should only use context available in the provided file.
- anchors: String array only. Important searchable handles from the file. Use short strings for major concepts, scenes, features, systems, sections, named events, recurring objects, UI areas, endpoints, settings, or unique terms.
- symbols: For code, include functions, classes, structs, interfaces, types, constants, endpoints, commands, config keys, major variables, data shapes, and exported or central identifiers. For prose/non-code, include analogous named structures such as chapters, scenes, artifacts, spells, organizations, lore terms, recurring concepts, or section labels.
- continuity.characters: String array only. People, creatures, agents, models, users, or important entities introduced, used, or materially changed in this file.
- continuity.relationships: String array only. Relationships, dependencies, alliances, conflicts, ownership links, parent/child links, caller/callee-style links, or cause/effect links stated inside this file. Use compact strings like "Rowan -> villagers: accepted by community after fixing bell and well".
- continuity.timeline_events: String array only. Important events, state changes, decisions, scene beats, migrations, version changes, or sequence-sensitive facts in this file.
- continuity.locations: String array only. Physical places, files/directories, modules, services, UI areas, settings screens, or other meaningful locations named or controlled by this file.
- continuity.rules: String array only. Hard constraints, mechanics, invariants, business rules, lore rules, validation rules, safety rules, or behavior that must stay consistent later.
- continuity.contradictions: String array only. Internal conflicts, ambiguous facts, impossible states, mismatches, TODO-like uncertainty, or contradictions visible inside this file.

Rules:
- Every required key must exist.
- summary must be a non-empty JSON string.
- Arrays may be empty when a category has no supported information.
- Use compact strings in anchors and symbols.
- anchors and all continuity arrays must contain strings only, not objects.
- Do not return {"name":"...","summary":""} metadata objects; put the useful text directly in the string.
- Prefer exact names over vague labels.
- Use only context available in the provided file.
- Return only the JSON object shown above.`
const cypherRankingSystemPrompt = `CYPHER PHASE 2 FILE RANKING
You are ranking project files for a later one-file-at-a-time Cypher Action run.

You receive the user's objective and an AI-facing Cypher project map containing file paths, summaries, anchors, symbols, and continuity notes.

Return strict valid JSON only. No markdown fences. No prose before or after JSON. No trailing commas. Use double quotes for all JSON strings and property names.

Required JSON shape:
{
  "ranked_files": [
    { "path": "project-relative/path.txt", "inference_importance": 1 }
  ],
  "search_terms": []
}

Rules:
- Rank only files that seem relevant to the user's objective from Cypher metadata.
- inference_importance is 0 to 5. Return only files with inference_importance greater than 0.
- 5 means highly likely needed, 3 means possibly needed, 1 means weak but plausible.
- search_terms may contain 0 to 5 concise literal search terms AgentGO should search in project files.
- Only include search terms that are likely to deterministically find relevant files Cypher summaries may have missed.
- Do not pad search_terms with random prompt words.
- Do not return summaries, anchors, continuity, file operations, requested_file, or prose.`

const cypherActionSystemPrompt = `CYPHER ACTION MODE
You are performing the user's request using Cypher, AgentGO's project map.

Cypher metadata gives you file names, summaries, anchors, symbols, continuity notes, importance scores, and a temporary run log. Cypher metadata is useful for deciding what to inspect, but it is not the same as reading a file's full content.

AgentGO also provides system-owned read tracking fields in the action context:
- actual_files_read: files whose full content AgentGO has actually attached during this Cypher Action run.
- current_attached_file: the one file whose full content is attached below in this current round, or an empty string.
- remaining_ranked_candidates: ranked files still available to request that are not in actual_files_read.

Use the provided Cypher metadata and importance scores to decide whether you need to inspect another file. Use full attached file content only when a file appears in actual_files_read/current_attached_file.

Request to read any ranked file you need to confidently complete the user’s task. You may skip ranked files that you do not think will help.

FILE REQUEST RULE: If you require a file, request your most desired file now. You can request additional files in subsequent turns.

Return strict valid JSON only. No markdown fences. No prose before or after JSON. No trailing commas. Use double quotes for all JSON strings and property names.

Required JSON shape:
{
  "file_operations": [],
  "requested_file": "",
  "run_log_entry": {
    "summary": "",
    "updated": [],
    "created": [],
    "deleted": [],
    "reason": ""
  },
  "final_response": ""
}

File operation objects use this shape:
{
  "action": "create|overwrite|delete",
  "path": "project-relative/path.txt",
  "content": "full replacement content for create or overwrite"
}

Rules:
- Request exactly one file at a time using requested_file, or return an empty requested_file when done.
- requested_file should be a single project-relative string path, not an array.
- An empty requested_file means you are done requesting files and the Cypher Action run should complete.
- Creating a file does not keep the run alive by itself. If you create a file but still need to inspect or work on another file, you must also set requested_file to the next file.
- file_operations may be empty when you only need to request a file, create no files, or finish without edits.
- run_log_entry.summary is required every response. Log what this step did or decided.
- Use run_log_entry.updated, created, and deleted to list project-relative paths changed by this response.
- Use final_response only when requested_file is empty and the run should complete.
- If no file is attached, you may request one file, create a new file, or finish. You may not update or delete existing files.
- If one file is attached, you may overwrite or delete the attached file. You may also create new files when needed.
- Use file_operations action "overwrite" for updating the attached existing file.
- Use file_operations action "create" only for new files.
- Use file_operations action "delete" only for the attached existing file.
- For create operations, file operation paths must be project-relative paths inside projectwork.
- Use the temporary run log as newer than Cypher summaries for changes made during this active run.
- Keep run_log_entry concise but specific enough that the next action call can avoid repeating work already done.
- Normal project files, code, prose, commit messages, PR text, and user-facing output must remain normal professional text/code in the style requested by the user.

Actual-read reporting rules:
- Never claim you read, inspected, opened, or requested a file unless that path appears in actual_files_read.
- Cypher summaries/metadata do not count as reading a file. If you only used Cypher metadata for a file, describe it as "identified from Cypher metadata" or "relevant but not read".
- If the task needs chapter-level, code-level, or quote-level evidence, request the actual file before making final claims about that file.
- When writing a report section such as "Files Requested", "Files Read", or "Files Actually Read", include only actual_files_read paths.
- If relevant files remain but are not needed or not read, list them separately as metadata candidates, not as read files.`

const wireTapResearchSystemPrompt = `AGENTGO WIRETAP BUILD MODE
You are building AgentGO WireTap.

AgentGO WireTap is a reasoning-context manifest, not a surveillance tool, bibliography, paper list, citation database, or literature review.
Return strict JSON only. No markdown fences. No prose outside JSON.
The response must be valid JSON parseable by JSON.parse/json.Unmarshal. Escape every inner double quote inside string values as \" or rewrite with apostrophes. Prefer apostrophes instead of quotation marks inside JSON string values. Do not include raw tabs, raw newlines inside strings, comments, trailing commas, or control characters.

Purpose:
- Reduce hallucination.
- Compress broad topic context into compact, high-signal information pieces.
- Keep answer-phase context compact so the model can reason over the most relevant information without distraction.
- Use the user-provided Research Tags as the scope boundary.
- New Research Tags mean a fresh scoped AgentGO WireTap rebuild, not append to old scope.

Use your internal knowledge to identify compact, high-value information pieces related to the Research Tags. Do not try to retrieve or list papers unless a source is genuinely useful as an anchor. AgentGO WireTap should help later answer-phase models reason from compressed, relevant knowledge rather than from a large pile of documents.

Prioritize:
- mechanisms
- definitions
- constraints
- equations
- model assumptions
- objections and counterarguments
- controversies
- implications
- relationships between concepts
- useful source hints
- known limits or uncertainty

Do not force every entry to be a research paper. Include source anchors only when useful.

Anti-hallucination rules:
- Do not invent exact source titles, DOIs, authors, journals, years, or URLs.
- If exact source metadata is uncertain, mark basis="source_hint" or basis="model_knowledge", not basis="source_verified".
- Use basis="source_verified" only when you are confident the cited source details are exact and include actual source metadata in sources.
- Do not label speculative physics as established fact.
- Phrase speculative material as "model proposes," "theory suggests," or "one possible implication is."

Duplicate avoidance and diversity:
- Treat existing AgentGO WireTap entries as an exclusion and coverage guide.
- Do not return entries that repeat the same source, same claim, or same reasoning role.
- Do not reword an existing entry just to fill the target count.
- In later build rounds, use the existing entries to infer which clusters and reasoning roles are already well covered.
- Prefer the strongest missing information pieces from under-covered clusters and reasoning roles.
- Do not add weak, irrelevant, or speculative filler just to increase variety.
- A new entry is useful only if it adds distinct reasoning value.
- If fewer than the requested number of genuinely useful new entries remain, return fewer, set status="complete_for_current_scope", and explain why.

Entry style:
- Compact, dense, useful.
- One information piece per entry.
- Each entry should be useful on its own.
- Prefer high-value reasoning hooks over long explanations.
- Generic background is only acceptable when necessary to reason about the Research Tags.
- Treat max_entries as a top-N curation cap, not a first-N stopping threshold.

Required JSON shape:
{
  "wiretap": { ...complete AgentGO WireTap JSON object... },
  "status": "continue" or "complete_for_current_scope",
  "added_entries": 0,
  "target_entries_requested": 50,
  "entries_returned": 50,
  "exhaustion_reason": "",
  "notes": "short note"
}

AgentGO WireTap entries should include id, batch_index, batch_target, tags, kind, claim, evidence_summary, reasoning_value, basis, certainty, confidence, relevance, related_entries, novelty_reason, duplicate_check, and source_hints when helpful. Entries with basis="source_verified" must include actual source metadata in sources. Other entries may include source_hints, source_status, evidence_type, primary_source_key, and last_verified when helpful.

Suggested entry fields:
{
  "id": "WT-0001",
  "batch_index": 1,
  "batch_target": 50,
  "tags": [],
  "kind": "mechanism | definition | constraint | equation | objection | relationship | implication | controversy | source_hint | assumption",
  "claim": "Compact high-value information piece.",
  "evidence_summary": "Compact supporting explanation or context.",
  "reasoning_value": "Why this helps a later AI answer questions.",
  "basis": "model_knowledge | source_hint | source_verified | derived_relation | user_provided",
  "certainty": "established | proposed | speculative | disputed | uncertain",
  "confidence": "high | medium | low",
  "relevance": "direct | supporting | background",
  "source_hints": [],
  "sources": [],
  "related_entries": [],
  "duplicate_check": "new_claim | related_but_distinct | new_source_new_claim",
  "novelty_reason": "Why this is not a duplicate of existing AgentGO WireTap entries."
}`

const wireTapSelectionSystemPrompt = `AGENTGO WIRETAP USE MODE PASS 1: RELEVANCE SELECTION ONLY
You are selecting AgentGO WireTap entries for an answer.
Return strict JSON only. No markdown fences. No prose outside JSON.

You will receive:
1. The user's original prompt.
2. The full AgentGO WireTap manifest.

Do not answer the user yet.
Your only task is to select the AgentGO WireTap entry IDs most useful for answering the user's prompt.

Select a compact, high-signal slice. Prefer fewer highly relevant entries over a large noisy selection.
Do not include entries only because they share a tag.
Do not include background entries unless they are needed to answer the prompt.
If AgentGO WireTap lacks important information, report what is missing.

Include entries that provide:
- required facts
- key mechanisms
- definitions
- constraints
- equations
- relevant objections
- disputed points
- relationships needed for reasoning

Return tiers:
{
  "required_entries": ["WT-0001"],
  "possibly_relevant_entries": ["WT-0002"],
  "background_entries": ["WT-0003"],
  "excluded_entries": ["WT-0004"],
  "missing_needed_evidence": ["short missing evidence note"],
  "notes": "short selection note"
}

Rules:
- required_entries: needed to answer safely.
- possibly_relevant_entries: may help but not central.
- background_entries: useful orientation only.
- excluded_entries: explicitly not needed or distracting.
- missing_needed_evidence: evidence the answer may need that AgentGO WireTap does not contain.
- Aim for the default runtime slice limit and never exceed max_runtime_slice_entries across required + possible + background.
- Prefer a smaller, sharper slice over a larger noisy slice.`

func normalizeWireTapTagsFromRequest(req WireTapBuildRequest) []string {
	raw := append([]string{}, req.Tags...)
	if strings.TrimSpace(req.ResearchTags) != "" {
		raw = append(raw, strings.Split(req.ResearchTags, ",")...)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, tag := range raw {
		clean := strings.TrimSpace(tag)
		clean = strings.Trim(clean, "[]")
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func (a *App) wireTapPath(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, wireTapFileName), nil
}

func (a *App) projectDiagnosticsDir(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "diagnostics"), nil
}

func (a *App) clearProjectDiagnosticsDir(projectName string) error {
	dir, err := a.projectDiagnosticsDir(projectName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

func emptyWireTapDocument(projectName string, tags []string, limits WireTapLimits) WireTapDocument {
	now := time.Now().UTC().Format(time.RFC3339)
	return WireTapDocument{
		AgentGOFile:                wireTapSystemID,
		FileVersion:                1,
		WireTapVersion:             1,
		Project:                    strings.TrimSpace(projectName),
		ResearchTags:               append([]string(nil), tags...),
		MaxEntries:                 limits.MaxEntries,
		DefaultRuntimeSliceEntries: limits.DefaultRuntimeSliceEntries,
		MaxRuntimeSliceEntries:     limits.MaxRuntimeSliceEntries,
		CreatedAt:                  now,
		UpdatedAt:                  now,
		CompletionStatus:           "building",
		KnownGaps:                  []string{},
		Entries:                    []WireTapEntry{},
	}
}

func normalizeWireTapDocument(doc WireTapDocument, projectName string, tags []string, limits WireTapLimits, preserveCreatedAt string) WireTapDocument {
	if strings.TrimSpace(preserveCreatedAt) == "" {
		preserveCreatedAt = strings.TrimSpace(doc.CreatedAt)
	}
	if strings.TrimSpace(preserveCreatedAt) == "" {
		preserveCreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	doc.AgentGOFile = wireTapSystemID
	doc.FileVersion = 1
	doc.WireTapVersion = 1
	doc.Project = strings.TrimSpace(projectName)
	doc.ResearchTags = append([]string(nil), tags...)
	doc.MaxEntries = limits.MaxEntries
	doc.DefaultRuntimeSliceEntries = limits.DefaultRuntimeSliceEntries
	doc.MaxRuntimeSliceEntries = limits.MaxRuntimeSliceEntries
	doc.CreatedAt = preserveCreatedAt
	doc.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	doc.CompletionStatus = strings.TrimSpace(doc.CompletionStatus)
	if doc.CompletionStatus == "" {
		doc.CompletionStatus = "building"
	}
	doc.ScopeSummary = strings.TrimSpace(doc.ScopeSummary)
	for idx := range doc.KnownGaps {
		doc.KnownGaps[idx] = strings.TrimSpace(doc.KnownGaps[idx])
	}
	doc.KnownGaps = compactStringSlice(doc.KnownGaps)
	doc.Entries = normalizeWireTapEntries(doc.Entries, limits.MaxEntries)
	return doc
}

func compactStringSlice(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func normalizeWireTapEntries(entries []WireTapEntry, maxEntries int) []WireTapEntry {
	if maxEntries <= 0 {
		maxEntries = wireTapDefaultMaxEntries
	}
	seenID := map[string]bool{}
	seenClaim := map[string]bool{}
	out := []WireTapEntry{}
	for _, entry := range entries {
		entry.ID = strings.ToUpper(strings.TrimSpace(entry.ID))
		entry.Claim = strings.TrimSpace(entry.Claim)
		entry.Kind = strings.ToLower(strings.TrimSpace(entry.Kind))
		entry.Category = strings.TrimSpace(entry.Category)
		entry.EvidenceSummary = strings.TrimSpace(entry.EvidenceSummary)
		entry.ReasoningValue = strings.TrimSpace(entry.ReasoningValue)
		entry.Basis = strings.ToLower(strings.TrimSpace(entry.Basis))
		entry.Certainty = strings.ToLower(strings.TrimSpace(entry.Certainty))
		entry.Confidence = strings.ToLower(strings.TrimSpace(entry.Confidence))
		entry.Relevance = strings.TrimSpace(entry.Relevance)
		entry.Status = strings.ToLower(strings.TrimSpace(entry.Status))
		entry.QualityTier = strings.ToLower(strings.TrimSpace(entry.QualityTier))
		entry.SourceStatus = strings.ToLower(strings.TrimSpace(entry.SourceStatus))
		entry.EvidenceType = strings.ToLower(strings.TrimSpace(entry.EvidenceType))
		entry.PrimarySourceKey = strings.TrimSpace(entry.PrimarySourceKey)
		entry.NoveltyReason = strings.TrimSpace(entry.NoveltyReason)
		entry.DuplicateCheck = strings.ToLower(strings.TrimSpace(entry.DuplicateCheck))
		entry.LastVerified = strings.TrimSpace(entry.LastVerified)
		entry.Notes = strings.TrimSpace(entry.Notes)
		entry.Tags = compactStringSlice(entry.Tags)
		entry.SourceHints = compactStringSlice(entry.SourceHints)
		entry.RelatedEntries = compactStringSlice(entry.RelatedEntries)
		if entry.Claim == "" && entry.EvidenceSummary == "" {
			continue
		}
		if entry.ID == "" || seenID[entry.ID] {
			entry.ID = fmt.Sprintf("WT-%04d", len(out)+1)
		}
		claimKey := strings.ToLower(entry.Claim)
		if claimKey != "" && seenClaim[claimKey] {
			continue
		}
		if entry.Kind == "" && entry.Category != "" {
			entry.Kind = strings.ToLower(strings.TrimSpace(entry.Category))
		}
		if entry.Kind == "" {
			entry.Kind = "information_piece"
		}
		if entry.Category == "" {
			entry.Category = entry.Kind
		}
		if entry.Confidence == "" {
			entry.Confidence = "medium"
		}
		if entry.Basis == "" {
			entry.Basis = "model_knowledge"
		}
		if entry.Certainty == "" && entry.Status != "" {
			entry.Certainty = entry.Status
		}
		if entry.Certainty == "" {
			entry.Certainty = "uncertain"
		}
		if entry.Status == "" {
			entry.Status = entry.Certainty
		}
		for idx := range entry.Sources {
			entry.Sources[idx].Title = strings.TrimSpace(entry.Sources[idx].Title)
			entry.Sources[idx].AuthorOrOrg = strings.TrimSpace(entry.Sources[idx].AuthorOrOrg)
			entry.Sources[idx].Year = strings.TrimSpace(entry.Sources[idx].Year)
			entry.Sources[idx].URLOrDOI = strings.TrimSpace(entry.Sources[idx].URLOrDOI)
			entry.Sources[idx].Notes = strings.TrimSpace(entry.Sources[idx].Notes)
		}
		if entry.PrimarySourceKey == "" {
			entry.PrimarySourceKey = wireTapPrimarySourceKey(entry)
		}
		seenID[entry.ID] = true
		if claimKey != "" {
			seenClaim[claimKey] = true
		}
		out = append(out, entry)
		if len(out) >= maxEntries {
			break
		}
	}
	return out
}

type wireTapMergeStats struct {
	Returned        int
	AcceptedNew     int
	DuplicateSource int
	DuplicateClaim  int
	Invalid         int
	Rejected        int
	TotalBefore     int
	TotalAfter      int
	Reasons         map[string]int
}

func (s wireTapMergeStats) reasonSummary() string {
	if len(s.Reasons) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.Reasons))
	for key := range s.Reasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, s.Reasons[key]))
	}
	return strings.Join(parts, ", ")
}

func (s wireTapMergeStats) logSummary(target int) string {
	return fmt.Sprintf("returned=%d accepted_new=%d duplicate_source=%d duplicate_claim=%d invalid=%d rejected=%d total_before=%d total_after=%d target=%d", s.Returned, s.AcceptedNew, s.DuplicateSource, s.DuplicateClaim, s.Invalid, s.Rejected, s.TotalBefore, s.TotalAfter, target)
}

func (s *wireTapMergeStats) addReason(reason string) {
	clean := strings.TrimSpace(reason)
	if clean == "" {
		clean = "rejected"
	}
	if s.Reasons == nil {
		s.Reasons = map[string]int{}
	}
	s.Reasons[clean]++
}

func normalizeWireTapComparable(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.TrimPrefix(clean, "doi:")
	clean = strings.TrimPrefix(clean, "https://doi.org/")
	clean = strings.TrimPrefix(clean, "http://doi.org/")
	clean = strings.TrimPrefix(clean, "https://dx.doi.org/")
	clean = strings.TrimPrefix(clean, "http://dx.doi.org/")
	clean = strings.TrimPrefix(clean, "arxiv:")
	clean = strings.TrimPrefix(clean, "https://arxiv.org/abs/")
	clean = strings.TrimPrefix(clean, "http://arxiv.org/abs/")
	var b strings.Builder
	lastSpace := false
	for _, r := range clean {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if r == '/' || r == '.' || r == '-' || r == '_' || r == ':' {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func wireTapClaimKey(entry WireTapEntry) string {
	return normalizeWireTapComparable(entry.Claim)
}

func wireTapSourceKeyFromSource(src WireTapSource) string {
	if key := normalizeWireTapComparable(src.URLOrDOI); key != "" {
		return "ref:" + key
	}
	title := normalizeWireTapComparable(src.Title)
	if title == "" {
		return ""
	}
	author := normalizeWireTapComparable(src.AuthorOrOrg)
	year := normalizeWireTapComparable(src.Year)
	if author != "" || year != "" {
		return "title_author_year:" + strings.TrimSpace(title+"|"+author+"|"+year)
	}
	return "title:" + title
}

func wireTapSourceHasVerifiedMetadata(src WireTapSource) bool {
	if normalizeWireTapComparable(src.URLOrDOI) != "" {
		return true
	}
	title := normalizeWireTapComparable(src.Title)
	if title == "" {
		return false
	}
	return normalizeWireTapComparable(src.AuthorOrOrg) != "" || normalizeWireTapComparable(src.Year) != ""
}

func wireTapEntryHasVerifiedSourceMetadata(entry WireTapEntry) bool {
	for _, src := range entry.Sources {
		if wireTapSourceHasVerifiedMetadata(src) {
			return true
		}
	}
	return false
}

func wireTapFallbackUnverifiedBasis(entry WireTapEntry) string {
	status := strings.ToLower(strings.TrimSpace(entry.SourceStatus))
	if len(entry.SourceHints) > 0 || entry.PrimarySourceKey != "" || len(entry.Sources) > 0 || status == "source_hint" || status == "needs_verification" {
		return "source_hint"
	}
	return "model_knowledge"
}

func wireTapPrimarySourceKey(entry WireTapEntry) string {
	if key := normalizeWireTapComparable(entry.PrimarySourceKey); key != "" {
		if strings.HasPrefix(key, "ref:") || strings.HasPrefix(key, "title:") || strings.HasPrefix(key, "title_author_year:") || strings.HasPrefix(key, "primary:") {
			return key
		}
		for _, src := range entry.Sources {
			if sourceKey := wireTapSourceKeyFromSource(src); sourceKey != "" {
				if strings.HasSuffix(sourceKey, ":"+key) || strings.Contains(sourceKey, "|"+key+"|") || strings.Contains(sourceKey, key) {
					return sourceKey
				}
			}
		}
		if strings.Contains(key, ".") || strings.Contains(key, "/") || strings.Contains(key, "10.") || strings.Contains(key, "arxiv") {
			return "ref:" + key
		}
		return "primary:" + key
	}
	for _, src := range entry.Sources {
		if key := wireTapSourceKeyFromSource(src); key != "" {
			return key
		}
	}
	for _, hint := range entry.SourceHints {
		if key := normalizeWireTapComparable(hint); key != "" {
			return "hint:" + key
		}
	}
	return ""
}

func normalizeWireTapEntryForMerge(entry WireTapEntry) WireTapEntry {
	entry.ID = strings.ToUpper(strings.TrimSpace(entry.ID))
	entry.Claim = strings.TrimSpace(entry.Claim)
	entry.Kind = strings.ToLower(strings.TrimSpace(entry.Kind))
	entry.Category = strings.TrimSpace(entry.Category)
	entry.EvidenceSummary = strings.TrimSpace(entry.EvidenceSummary)
	entry.ReasoningValue = strings.TrimSpace(entry.ReasoningValue)
	entry.Basis = strings.ToLower(strings.TrimSpace(entry.Basis))
	entry.Certainty = strings.ToLower(strings.TrimSpace(entry.Certainty))
	entry.Confidence = strings.ToLower(strings.TrimSpace(entry.Confidence))
	entry.Relevance = strings.TrimSpace(entry.Relevance)
	entry.Status = strings.ToLower(strings.TrimSpace(entry.Status))
	entry.QualityTier = strings.ToLower(strings.TrimSpace(entry.QualityTier))
	entry.SourceStatus = strings.ToLower(strings.TrimSpace(entry.SourceStatus))
	entry.EvidenceType = strings.ToLower(strings.TrimSpace(entry.EvidenceType))
	entry.PrimarySourceKey = strings.TrimSpace(entry.PrimarySourceKey)
	entry.NoveltyReason = strings.TrimSpace(entry.NoveltyReason)
	entry.DuplicateCheck = strings.ToLower(strings.TrimSpace(entry.DuplicateCheck))
	entry.LastVerified = strings.TrimSpace(entry.LastVerified)
	entry.Notes = strings.TrimSpace(entry.Notes)
	entry.Tags = compactStringSlice(entry.Tags)
	entry.SourceHints = compactStringSlice(entry.SourceHints)
	entry.RelatedEntries = compactStringSlice(entry.RelatedEntries)
	for idx := range entry.Sources {
		entry.Sources[idx].Title = strings.TrimSpace(entry.Sources[idx].Title)
		entry.Sources[idx].AuthorOrOrg = strings.TrimSpace(entry.Sources[idx].AuthorOrOrg)
		entry.Sources[idx].Year = strings.TrimSpace(entry.Sources[idx].Year)
		entry.Sources[idx].URLOrDOI = strings.TrimSpace(entry.Sources[idx].URLOrDOI)
		entry.Sources[idx].Notes = strings.TrimSpace(entry.Sources[idx].Notes)
	}
	if entry.Kind == "" && entry.Category != "" {
		entry.Kind = strings.ToLower(strings.TrimSpace(entry.Category))
	}
	if entry.Kind == "" {
		entry.Kind = "information_piece"
	}
	if entry.Category == "" {
		entry.Category = entry.Kind
	}
	if entry.Confidence == "" {
		entry.Confidence = "medium"
	}
	if entry.Basis == "" {
		entry.Basis = "model_knowledge"
	}
	if entry.Certainty == "" && entry.Status != "" {
		entry.Certainty = entry.Status
	}
	if entry.Certainty == "" {
		entry.Certainty = "uncertain"
	}
	if entry.Status == "" {
		entry.Status = entry.Certainty
	}
	if entry.Basis == "source_verified" {
		if wireTapEntryHasVerifiedSourceMetadata(entry) {
			if entry.SourceStatus == "" || entry.SourceStatus == "needs_verification" || entry.SourceStatus == "source_hint" {
				entry.SourceStatus = "verified"
			}
		} else {
			entry.Basis = wireTapFallbackUnverifiedBasis(entry)
			if entry.SourceStatus == "source_verified" || entry.SourceStatus == "verified" {
				entry.SourceStatus = "needs_verification"
			}
		}
	}
	if entry.SourceStatus == "" && (len(entry.Sources) > 0 || entry.Basis == "source_verified" || entry.Basis == "source_hint") {
		if entry.Basis == "source_verified" {
			entry.SourceStatus = "verified"
		} else {
			entry.SourceStatus = "needs_verification"
		}
	}
	if entry.PrimarySourceKey == "" {
		entry.PrimarySourceKey = wireTapPrimarySourceKey(entry)
	}
	return entry
}

func mergeWireTapEntries(base []WireTapEntry, incoming []WireTapEntry, maxEntries int) []WireTapEntry {
	merged, _ := mergeWireTapEntriesDetailed(base, incoming, maxEntries)
	return merged
}

func mergeWireTapEntriesDetailed(base []WireTapEntry, incoming []WireTapEntry, maxEntries int) ([]WireTapEntry, wireTapMergeStats) {
	if maxEntries <= 0 {
		maxEntries = wireTapDefaultMaxEntries
	}
	stats := wireTapMergeStats{Returned: len(incoming), Reasons: map[string]int{}}
	out := normalizeWireTapEntries(base, maxEntries)
	stats.TotalBefore = len(out)
	seenID := map[string]bool{}
	seenClaim := map[string]bool{}
	seenSource := map[string]bool{}
	for _, entry := range out {
		if entry.ID != "" {
			seenID[entry.ID] = true
		}
		if key := wireTapClaimKey(entry); key != "" {
			seenClaim[key] = true
		}
		if key := wireTapPrimarySourceKey(entry); key != "" {
			seenSource[key] = true
		}
	}
	for _, rawEntry := range incoming {
		entry := normalizeWireTapEntryForMerge(rawEntry)
		if entry.Claim == "" && entry.EvidenceSummary == "" {
			stats.Invalid++
			stats.addReason("invalid_missing_claim_and_evidence")
			continue
		}
		claimKey := wireTapClaimKey(entry)
		if claimKey != "" && seenClaim[claimKey] {
			stats.DuplicateClaim++
			stats.addReason("duplicate_normalized_claim")
			continue
		}
		sourceKey := wireTapPrimarySourceKey(entry)
		if sourceKey != "" && seenSource[sourceKey] {
			stats.DuplicateSource++
			stats.addReason("duplicate_primary_source_key")
			continue
		}
		if len(out) >= maxEntries {
			stats.Rejected++
			stats.addReason("rejected_at_max_entries")
			continue
		}
		if entry.ID == "" || seenID[entry.ID] {
			entry.ID = fmt.Sprintf("WT-%04d", len(out)+1)
		}
		out = append(out, entry)
		seenID[entry.ID] = true
		if claimKey != "" {
			seenClaim[claimKey] = true
		}
		if sourceKey != "" {
			seenSource[sourceKey] = true
		}
		stats.AcceptedNew++
	}
	stats.TotalAfter = len(out)
	stats.Rejected += stats.Returned - stats.AcceptedNew - stats.DuplicateSource - stats.DuplicateClaim - stats.Invalid - stats.Rejected
	if stats.Rejected < 0 {
		stats.Rejected = 0
	}
	return out, stats
}

func wireTapEntriesFingerprint(entries []WireTapEntry) string {
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func wireTapZeroAcceptedReason(raw string, returned []WireTapEntry) string {
	returnedCount := len(returned)
	if returnedCount == 0 {
		if strings.Contains(strings.ToLower(raw), "\"entries\"") {
			return "model response mentioned entries, but AgentGO could not parse them as a WireTap entries array; open the full raw diagnostics response for the exact shape"
		}
		return "model returned no WireTap entries"
	}
	reasons := map[string]int{}
	for _, entry := range returned {
		claim := strings.TrimSpace(entry.Claim)
		evidence := strings.TrimSpace(entry.EvidenceSummary)
		if claim == "" && evidence == "" {
			reasons["missing both claim and evidence_summary"]++
		}
	}
	if len(reasons) == 0 {
		return fmt.Sprintf("model returned %d entries, but validation accepted 0; open the full raw diagnostics response to inspect schema and fields", returnedCount)
	}
	parts := make([]string, 0, len(reasons))
	for reason, count := range reasons {
		parts = append(parts, fmt.Sprintf("%s: %d", reason, count))
	}
	sort.Strings(parts)
	return fmt.Sprintf("model returned %d entries, but validation accepted 0 (%s)", returnedCount, strings.Join(parts, "; "))
}

func readWireTapDocument(path string) (WireTapDocument, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WireTapDocument{}, false, nil
		}
		return WireTapDocument{}, false, err
	}
	var doc WireTapDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return WireTapDocument{}, true, err
	}
	return doc, true, nil
}

func writeWireTapDocument(path string, doc WireTapDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0o644)
}

func (a *App) ensureWireTapReadyForUse(projectName string) error {
	path, err := a.wireTapPath(projectName)
	if err != nil {
		return err
	}
	doc, exists, err := readWireTapDocument(path)
	if err != nil {
		return fmt.Errorf("could not read WireTap.json: %w", err)
	}
	if !exists {
		return errors.New("WireTap.json does not exist. Click WireTap and enter Research Tags to build it first.")
	}
	if len(doc.Entries) == 0 {
		return errors.New("WireTap.json has no research entries. Rebuild WireTap with Research Tags before arming it.")
	}
	return nil
}

func (a *App) handleWireTapStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	if projectName == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": "", "exists": false, "ready": false})
		return
	}
	path, err := a.wireTapPath(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc, exists, err := readWireTapDocument(path)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": projectName, "exists": true, "ready": false, "error": err.Error()})
		return
	}
	relPath, _ := filepath.Rel(a.cfg.WorkRoot, path)
	limits := normalizeWireTapLimits(WireTapLimits{MaxEntries: doc.MaxEntries, DefaultRuntimeSliceEntries: doc.DefaultRuntimeSliceEntries, MaxRuntimeSliceEntries: doc.MaxRuntimeSliceEntries})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                     true,
		"project":                projectName,
		"path":                   filepath.ToSlash(relPath),
		"exists":                 exists,
		"ready":                  exists && len(doc.Entries) > 0,
		"entryCount":             len(doc.Entries),
		"maxEntries":             limits.MaxEntries,
		"runtimeEntries":         limits.DefaultRuntimeSliceEntries,
		"maxRuntimeSliceEntries": limits.MaxRuntimeSliceEntries,
		"researchTags":           doc.ResearchTags,
		"status":                 doc.CompletionStatus,
	})
}

func (a *App) handleWireTapBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	if projectName == "" {
		http.Error(w, "You must select a project.", http.StatusBadRequest)
		return
	}
	if a.waveExecutionInProgress(projectName) {
		http.Error(w, "A run is already active for this project. Wait for it to finish or press Emergency Stop.", http.StatusConflict)
		return
	}
	var req WireTapBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	tags := normalizeWireTapTagsFromRequest(req)
	if len(tags) == 0 {
		http.Error(w, "Enter one or more Research Tags to build WireTap.", http.StatusBadRequest)
		return
	}
	model, ok := a.firstActiveBuilderModel()
	if !ok {
		http.Error(w, "Activate at least one Builder AI before using WireTap.", http.StatusBadRequest)
		return
	}
	limits := normalizeWireTapLimits(a.cfg.WireTap)
	if req.MaxEntries > 0 {
		limits.MaxEntries = req.MaxEntries
		limits = normalizeWireTapLimits(limits)
	}
	if req.RuntimeEntries > 0 {
		limits.DefaultRuntimeSliceEntries = req.RuntimeEntries
		if limits.DefaultRuntimeSliceEntries > limits.MaxRuntimeSliceEntries {
			limits.DefaultRuntimeSliceEntries = limits.MaxRuntimeSliceEntries
		}
		limits = normalizeWireTapLimits(limits)
	}
	path, err := a.wireTapPath(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.clearProjectDiagnosticsDir(projectName); err != nil {
		http.Error(w, fmt.Sprintf("could not prepare project diagnostics folder: %v", err), http.StatusInternalServerError)
		return
	}
	_, previousExists, readErr := readWireTapDocument(path)
	if readErr != nil {
		a.logf("system", "warn", "Replacing unreadable WireTap.json for project %s: %v", projectName, readErr)
	}
	executionID := fmt.Sprintf("wiretap-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(r.Context())
	modelID := modelIDString(model.ID)
	a.mu.Lock()
	a.setActiveCancelLocked(modelID, projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.clearActiveCancelLocked(modelID, executionID)
		a.mu.Unlock()
	}()
	doc, complete, err := a.runWireTapResearchBuild(ctx, model, projectName, tags, limits)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			http.Error(w, "WireTap build canceled.", http.StatusRequestTimeout)
			return
		}
		http.Error(w, fmt.Sprintf("WireTap build failed: %v", err), http.StatusBadGateway)
		return
	}
	if err := writeWireTapDocument(path, doc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	relPath, _ := filepath.Rel(a.cfg.WorkRoot, path)
	message := "WireTap rebuilt and armed for normal Execute Prompt."
	if !complete {
		message = "WireTap rebuilt with current research entries. More enrichment may still be useful."
	}
	a.logf("system", "info", "WireTap rebuilt for project %s by %s: tags=%s entries=%d complete=%v", projectName, model.Label, strings.Join(tags, ", "), len(doc.Entries), complete)
	writeJSON(w, http.StatusOK, WireTapBuildResponse{
		OK:                     true,
		Project:                projectName,
		Path:                   filepath.ToSlash(relPath),
		Ready:                  len(doc.Entries) > 0,
		Enabled:                len(doc.Entries) > 0,
		Created:                !previousExists,
		EntryCount:             len(doc.Entries),
		MaxEntries:             doc.MaxEntries,
		RuntimeEntries:         doc.DefaultRuntimeSliceEntries,
		MaxRuntimeSliceEntries: doc.MaxRuntimeSliceEntries,
		Status:                 doc.CompletionStatus,
		Message:                message,
		WireTap:                doc,
	})
}

func wireTapBuildBatchTarget(maxEntries int, currentEntries int) int {
	remaining := maxEntries - currentEntries
	if remaining <= 0 {
		return wireTapBuildBatchSize
	}
	if remaining < wireTapBuildBatchSize {
		return remaining
	}
	return wireTapBuildBatchSize
}

func (a *App) runWireTapResearchBuild(ctx context.Context, model ModelConfig, projectName string, tags []string, limits WireTapLimits) (WireTapDocument, bool, error) {
	current := emptyWireTapDocument(projectName, tags, limits)
	complete := false
	for round := 1; round <= wireTapMaxBuildRounds; round++ {
		select {
		case <-ctx.Done():
			return current, complete, ctx.Err()
		default:
		}
		batchTarget := wireTapBuildBatchTarget(limits.MaxEntries, len(current.Entries))
		input, err := buildWireTapResearchInput(projectName, tags, current, limits, round, batchTarget)
		if err != nil {
			return current, complete, err
		}
		a.logf(modelIDString(model.ID), "info", "WireTap build round %d sending request. entries=%d max=%d target=%d", round, len(current.Entries), limits.MaxEntries, batchTarget)
		a.publishDiagnostics(wireTapDiagnostics(projectName, model, fmt.Sprintf("Build Round %d Sent", round)).withPrompt(previewForLog(input, 1200)).withSystemPrompt(previewForLog(wireTapResearchSystemPrompt, 1200)).withStatusMessage(fmt.Sprintf("Research tags: %s · target new entries: %d", strings.Join(tags, ", "), batchTarget)))
		responseText, err := a.callStructuredTextModel(ctx, model, wireTapResearchSystemPrompt, input, true, nil)
		if err != nil {
			a.publishDiagnostics(wireTapDiagnostics(projectName, model, fmt.Sprintf("Build Round %d Failed", round)).withReason(err.Error()))
			return current, complete, err
		}
		preview, label := diagnosticsResponsePreview(responseText, 1600)
		rawRef, _, rawSaveErr := a.saveWireTapRawDiagnosticResponse(model, projectName, fmt.Sprintf("build round %d response", round), responseText)
		parsed, err := parseWireTapResearchResponse(responseText)
		if err != nil {
			diag := wireTapDiagnostics(projectName, model, fmt.Sprintf("Build Round %d Parse Failed", round)).withResponse(preview).withResponseLabel(label).withReason(err.Error())
			if rawRef.Path != "" {
				diag.Files = appendUniqueDiagnosticsFile(diag.Files, rawRef)
			}
			if rawSaveErr != nil {
				diag.StatusMessage = fmt.Sprintf("Could not save full raw response: %v", rawSaveErr)
			}
			a.publishDiagnostics(diag)
			return current, complete, err
		}
		previousCount := len(current.Entries)
		previousFingerprint := wireTapEntriesFingerprint(current.Entries)
		returnedCount := parsed.EntriesReturned
		if returnedCount <= 0 {
			returnedCount = len(parsed.WireTap.Entries)
		}
		candidate := normalizeWireTapDocument(parsed.WireTap, projectName, tags, limits, current.CreatedAt)
		candidateRawCount := len(candidate.Entries)
		mergeStats := wireTapMergeStats{Returned: returnedCount, TotalBefore: previousCount, TotalAfter: len(candidate.Entries), Reasons: map[string]int{}}
		if len(current.Entries) > 0 {
			merged, stats := mergeWireTapEntriesDetailed(current.Entries, candidate.Entries, limits.MaxEntries)
			candidate.Entries = merged
			mergeStats = stats
		} else {
			candidate.Entries, mergeStats = mergeWireTapEntriesDetailed(nil, candidate.Entries, limits.MaxEntries)
		}
		acceptedCount := len(candidate.Entries)
		acceptedDelta := mergeStats.AcceptedNew
		status := strings.ToLower(strings.TrimSpace(parsed.Status))
		if status == "" {
			status = strings.ToLower(strings.TrimSpace(candidate.CompletionStatus))
		}
		if status == "complete" {
			status = "complete_for_current_scope"
		}
		if status != "complete_for_current_scope" {
			status = "continue"
		}
		candidate.CompletionStatus = status
		mergeSummary := mergeStats.logSummary(batchTarget)
		a.logf(modelIDString(model.ID), "info", "WireTap build round %d merge result: %s", round, mergeSummary)
		diagStatus := fmt.Sprintf("Entries: %d / %d · added=%d/%d · status=%s · %s", acceptedCount, limits.MaxEntries, acceptedDelta, batchTarget, status, mergeSummary)
		if reasons := mergeStats.reasonSummary(); reasons != "" {
			diagStatus = strings.TrimSpace(diagStatus + " · reasons: " + reasons)
		}
		diag := wireTapDiagnostics(projectName, model, fmt.Sprintf("Build Round %d Parsed", round)).withResponse(preview).withResponseLabel(label).withStatusMessage(diagStatus)
		if previousCount > 0 && candidateRawCount < previousCount {
			message := fmt.Sprintf("model returned replacement/partial ledger with %d entries; preserving accumulated %d WireTap entries before merge", candidateRawCount, previousCount)
			a.logf(modelIDString(model.ID), "warn", "WireTap build round %d %s", round, message)
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + " · " + message)
		}
		if rawRef.Path != "" {
			diag.Files = appendUniqueDiagnosticsFile(diag.Files, rawRef)
		}
		if rawSaveErr != nil {
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + fmt.Sprintf(" · Could not save full raw response: %v", rawSaveErr))
		}
		if acceptedCount == 0 {
			reason := wireTapZeroAcceptedReason(responseText, parsed.WireTap.Entries)
			if reasons := mergeStats.reasonSummary(); reasons != "" {
				reason = strings.TrimSpace(reason + "; merge reasons: " + reasons)
			}
			diag.Stage = fmt.Sprintf("Build Round %d Stopped", round)
			diag.Reason = reason
			a.publishDiagnostics(diag)
			return current, complete, fmt.Errorf("WireTap build stopped: %s", reason)
		}
		if acceptedDelta == 0 && previousCount > 0 {
			reason := fmt.Sprintf("no new accepted entries or curation changes were added this round; %s", mergeSummary)
			if reasons := mergeStats.reasonSummary(); reasons != "" {
				reason = strings.TrimSpace(reason + "; reasons: " + reasons)
			}
			if previousFingerprint != wireTapEntriesFingerprint(candidate.Entries) {
				reason = strings.TrimSpace(reason + "; existing entries were preserved/curated but no new entries were accepted")
			}
			diag.Stage = fmt.Sprintf("Build Round %d Complete", round)
			diag.Reason = reason
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + " · no new accepted entries; complete_for_current_scope")
			candidate.CompletionStatus = "complete_for_current_scope"
			current = candidate
			complete = true
			a.publishDiagnostics(diag)
			break
		}
		if returnedCount < batchTarget {
			diag.Stage = fmt.Sprintf("Build Round %d Complete", round)
			detail := strings.TrimSpace(parsed.ExhaustionReason)
			if detail == "" {
				detail = strings.TrimSpace(parsed.Notes)
			}
			if detail == "" {
				detail = fmt.Sprintf("model returned fewer entries than requested; returned=%d target=%d accepted_new=%d", returnedCount, batchTarget, acceptedDelta)
			}
			diag.Reason = detail
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + " · returned below target; complete_for_current_scope")
			candidate.CompletionStatus = "complete_for_current_scope"
			current = candidate
			complete = true
			a.publishDiagnostics(diag)
			break
		}
		exhaustionReason := strings.TrimSpace(parsed.ExhaustionReason)
		if status == "complete_for_current_scope" && exhaustionReason != "" {
			diag.Stage = fmt.Sprintf("Build Round %d Complete", round)
			diag.Reason = exhaustionReason
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + " · model reported scope exhaustion; complete_for_current_scope")
			candidate.CompletionStatus = "complete_for_current_scope"
			current = candidate
			complete = true
			a.publishDiagnostics(diag)
			break
		}
		if status == "complete_for_current_scope" && exhaustionReason == "" {
			message := fmt.Sprintf("model returned complete_for_current_scope without exhaustion_reason; continuing because returned=%d accepted_new=%d target=%d", returnedCount, acceptedDelta, batchTarget)
			a.logf(modelIDString(model.ID), "warn", "WireTap build round %d %s", round, message)
			diag.StatusMessage = strings.TrimSpace(diag.StatusMessage + " · " + message)
			candidate.CompletionStatus = "continue"
			status = "continue"
		}
		current = candidate
		a.publishDiagnostics(diag)
		if len(current.Entries) >= limits.MaxEntries && round >= 2 {
			// At the cap, require the AI to curate top-N rather than append forever. One extra round after reaching cap is enough for v1.
			complete = false
			break
		}
	}
	if complete {
		current.CompletionStatus = "complete_for_current_scope"
	} else if strings.TrimSpace(current.CompletionStatus) == "" {
		current.CompletionStatus = "continue"
	}
	current = normalizeWireTapDocument(current, projectName, tags, limits, current.CreatedAt)
	return current, complete, nil
}

func buildWireTapResearchInput(projectName string, tags []string, current WireTapDocument, limits WireTapLimits, round int, batchTarget int) (string, error) {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(fmt.Sprintf(`AGENTGO WIRETAP BUILD REQUEST
Project: %s
Research Tags: %s
Build round: %d
Existing entries: %d / %d
Target new entries this round: %d
Default runtime slice entries: %d
Max runtime slice entries: %d

Instructions:
- Rebuild/curate AgentGO WireTap only for these Research Tags.
- Generate exactly %d new high-value information entries this round, or replace weaker entries with stronger entries when at the cap.
- Do not default to 10 entries. The target for this round is %d.
- Every returned entry must include batch_index and batch_target. Use batch_index values 1 through %d for this round. Use batch_target=%d on each entry.
- Use your internal knowledge to identify compact, high-value information pieces related to the Research Tags.
- Do not try to retrieve or list papers unless a source is genuinely useful as an anchor.
- Do not force every entry to be a research paper. Entries may be based on model_knowledge, source_hint, source_verified, derived_relation, or user_provided.
- AgentGO WireTap should help later answer-phase models reason from compressed, relevant knowledge rather than from a large pile of documents.
- A high-value AgentGO WireTap entry directly matches one or more Research Tags, is useful for future reasoning/comparison/criticism/hypothesis evaluation, is non-duplicate, and adds a distinct mechanism, definition, equation, objection, constraint, relationship, implication, controversy, model assumption, source hint, or uncertainty.
- Generic background is only acceptable when necessary to reason about the Research Tags.
- Treat CURRENT AGENTGO WIRETAP JSON as an exclusion and coverage guide. Do not repeat already listed sources by DOI, URL, arXiv ID, title+author+year, normalized title, source_hint, claim, or reasoning role.
- This is build round %d with %d existing entries. If the current AgentGO WireTap already strongly covers obvious clusters, prefer the strongest missing information pieces from under-covered clusters and reasoning roles.
- Prefer diversity by reasoning value: mechanisms, definitions, equations, objections, constraints, edge cases, failure modes, competing models, observational signatures, terminology, and relationships between Research Tags.
- Do not add weak, irrelevant, or speculative filler just to increase variety. A new entry is useful only if it adds distinct reasoning value.
- Before returning, perform a duplicate sweep against CURRENT AGENTGO WIRETAP JSON and against the new entries in this response. If any candidate is a duplicate source, duplicate claim, or duplicate reasoning role, replace it with a genuinely new high-value entry before final output.
- Do not set status="complete_for_current_scope" merely because some candidates were duplicates. Replace duplicates until the target is met unless the scoped knowledge space is genuinely exhausted.
- Return %d genuinely new entries whenever status="continue".
- For each new entry include kind, claim, evidence_summary, reasoning_value, basis, certainty, confidence, relevance, novelty_reason, and duplicate_check.
- Use basis="source_verified" only when you know exact source metadata and include it in sources. Otherwise use basis="source_hint", basis="model_knowledge", or basis="derived_relation".
- Do not invent bibliographic data. If exact source data is not known, use source_hints or basis="model_knowledge" instead of fabricated sources.
- Do not label speculative material as established. Use certainty="proposed", "speculative", "disputed", or "uncertain" as appropriate.
- JSON safety: do not use unescaped double quotes inside claim, evidence_summary, reasoning_value, notes, titles, novelty_reason, exhaustion_reason, or any other string. Use apostrophes for quoted concepts such as 'bag of gold', or escape quotes as \"bag of gold\".
- Before writing entries, consider a broader internal candidate pool and keep the strongest entries by tag relevance, confidence, reasoning usefulness, non-duplication, expert-angle coverage, and compactness. Do not output the candidate pool.
- If fewer than %d genuinely new useful entries exist for this scope, set status to "complete_for_current_scope", set entries_returned to the number returned, and provide a specific exhaustion_reason.
- If status is "continue", entries_returned must equal target_entries_requested.
- Do not preserve unrelated prior scope.
- Treat max entries as top-N cap.
- Return the complete updated AgentGO WireTap object inside {"wiretap": ...}.
- Also return target_entries_requested, entries_returned, and exhaustion_reason at the top level.
- Final self-check before returning: the exact response must be a single valid JSON object with no markdown, no comments, no trailing commas, no raw control characters, and no unescaped quotation marks inside string values.

CURRENT AGENTGO WIRETAP JSON:
%s`, projectName, strings.Join(tags, ", "), round, len(current.Entries), limits.MaxEntries, batchTarget, limits.DefaultRuntimeSliceEntries, limits.MaxRuntimeSliceEntries, batchTarget, batchTarget, batchTarget, batchTarget, round, len(current.Entries), batchTarget, batchTarget, string(data))), nil
}

func parseWireTapResearchResponse(raw string) (wireTapAIResearchResponse, error) {
	clean, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		if detail := wireTapInvalidJSONDetail(raw); detail != "" {
			return wireTapAIResearchResponse{}, fmt.Errorf("invalid WireTap JSON: %s", detail)
		}
		return wireTapAIResearchResponse{}, fmt.Errorf("invalid WireTap JSON: %w", err)
	}
	parsed, err := parseWireTapResearchResponseJSON([]byte(clean))
	if err == nil {
		return parsed, nil
	}
	if detail := jsonErrorContext(clean, err, 360); detail != "" {
		return wireTapAIResearchResponse{}, fmt.Errorf("invalid WireTap response shape: %v. %s", err, detail)
	}
	return wireTapAIResearchResponse{}, fmt.Errorf("invalid WireTap response shape: %w", err)
}

func wireTapInvalidJSONDetail(raw string) string {
	detail := cypherInvalidJSONDetail(raw)
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return detail + " Hint: check for unescaped double quotes inside JSON string values; prefer apostrophes or escape inner quotes as \\\"."
}

func parseWireTapResearchResponseJSON(data []byte) (wireTapAIResearchResponse, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return wireTapAIResearchResponse{}, err
	}
	if len(root) == 0 {
		return wireTapAIResearchResponse{}, errors.New("empty JSON object")
	}
	status := rawStringFieldCI(root, "status")
	notes := rawStringFieldCI(root, "notes")
	exhaustionReason := firstNonEmpty(rawStringFieldCI(root, "exhaustion_reason"), rawStringFieldCI(root, "exhaustionReason"))
	addedEntries := rawIntFieldCI(root, "added_entries")
	targetEntriesRequested := rawIntFieldCI(root, "target_entries_requested")
	entriesReturned := rawIntFieldCI(root, "entries_returned")
	var doc WireTapDocument
	var docErr error
	if rawDoc, ok := rawFieldCI(root, "wiretap"); ok {
		doc, docErr = parseWireTapDocumentJSON(rawDoc)
		if docErr != nil {
			return wireTapAIResearchResponse{}, fmt.Errorf("wiretap object: %w", docErr)
		}
	} else if rawDoc, ok := rawFieldCI(root, "wireTap"); ok {
		doc, docErr = parseWireTapDocumentJSON(rawDoc)
		if docErr != nil {
			return wireTapAIResearchResponse{}, fmt.Errorf("wireTap object: %w", docErr)
		}
	} else if _, hasEntries := rawFieldCI(root, "entries"); hasEntries || rawStringFieldCI(root, "agentgo_file") != "" {
		doc, docErr = parseWireTapDocumentJSON(data)
		if docErr != nil {
			return wireTapAIResearchResponse{}, docErr
		}
	} else {
		return wireTapAIResearchResponse{}, errors.New("missing wiretap object or entries array")
	}
	if status == "" {
		status = doc.CompletionStatus
	}
	if entriesReturned == 0 {
		entriesReturned = len(doc.Entries)
	}
	return wireTapAIResearchResponse{WireTap: doc, Status: status, AddedEntries: addedEntries, TargetEntriesRequested: targetEntriesRequested, EntriesReturned: entriesReturned, ExhaustionReason: exhaustionReason, Notes: notes}, nil
}

func parseWireTapDocumentJSON(data []byte) (WireTapDocument, error) {
	var doc WireTapDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return WireTapDocument{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return WireTapDocument{}, err
	}
	if rawEntries, ok := rawFieldCI(root, "entries"); ok {
		entries, err := parseWireTapEntriesJSON(rawEntries)
		if err != nil {
			return WireTapDocument{}, fmt.Errorf("entries array: %w", err)
		}
		doc.Entries = entries
	}
	if len(doc.Entries) == 0 {
		return doc, nil
	}
	return doc, nil
}

func parseWireTapEntriesJSON(data json.RawMessage) ([]WireTapEntry, error) {
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(data, &rawEntries); err != nil {
		return nil, err
	}
	entries := make([]WireTapEntry, 0, len(rawEntries))
	for idx, rawEntry := range rawEntries {
		entry, err := parseWireTapEntryJSON(rawEntry)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", idx+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseWireTapEntryJSON(data json.RawMessage) (WireTapEntry, error) {
	var entry WireTapEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return WireTapEntry{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return WireTapEntry{}, err
	}
	if entry.ID == "" {
		entry.ID = rawStringFieldCI(root, "entry_id")
	}
	if entry.Claim == "" {
		entry.Claim = firstNonEmpty(rawStringFieldCI(root, "claim_text"), rawStringFieldCI(root, "fact"), rawStringFieldCI(root, "finding"))
	}
	if entry.EvidenceSummary == "" {
		entry.EvidenceSummary = firstNonEmpty(rawStringFieldCI(root, "evidence"), rawStringFieldCI(root, "summary"), rawStringFieldCI(root, "evidenceSummary"))
	}
	if rawSources, ok := rawFieldCI(root, "sources"); ok {
		entry.Sources = mergeWireTapSources(entry.Sources, parseWireTapSourcesFlexible(rawSources))
	}
	if len(entry.Sources) == 0 {
		if rawSource, ok := rawFieldCI(root, "source"); ok {
			entry.Sources = parseWireTapSourcesFlexible(rawSource)
		}
	}
	return entry, nil
}

func mergeWireTapSources(base []WireTapSource, aliases []WireTapSource) []WireTapSource {
	if len(base) == 0 {
		return aliases
	}
	for idx := range base {
		if idx >= len(aliases) {
			continue
		}
		if base[idx].Title == "" {
			base[idx].Title = aliases[idx].Title
		}
		if base[idx].AuthorOrOrg == "" {
			base[idx].AuthorOrOrg = aliases[idx].AuthorOrOrg
		}
		if base[idx].Year == "" {
			base[idx].Year = aliases[idx].Year
		}
		if base[idx].URLOrDOI == "" {
			base[idx].URLOrDOI = aliases[idx].URLOrDOI
		}
		if base[idx].Notes == "" {
			base[idx].Notes = aliases[idx].Notes
		}
	}
	if len(aliases) > len(base) {
		base = append(base, aliases[len(base):]...)
	}
	return base
}

func parseWireTapSourcesFlexible(data json.RawMessage) []WireTapSource {
	data = bytes.TrimSpace(data)
	var rawItems []json.RawMessage
	if len(data) > 0 && data[0] == '[' {
		_ = json.Unmarshal(data, &rawItems)
	} else {
		rawItems = []json.RawMessage{data}
	}
	sources := []WireTapSource{}
	for _, rawItem := range rawItems {
		var src WireTapSource
		if err := json.Unmarshal(rawItem, &src); err != nil {
			continue
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(rawItem, &fields)
		if src.AuthorOrOrg == "" {
			src.AuthorOrOrg = firstNonEmpty(rawFlexibleStringFieldCI(fields, "author_or_org"), rawFlexibleStringFieldCI(fields, "author"), rawFlexibleStringFieldCI(fields, "authors"), rawFlexibleStringFieldCI(fields, "org"), rawFlexibleStringFieldCI(fields, "organization"), rawFlexibleStringFieldCI(fields, "publisher"))
		}
		if src.URLOrDOI == "" {
			src.URLOrDOI = firstNonEmpty(rawFlexibleStringFieldCI(fields, "url_or_doi"), rawFlexibleStringFieldCI(fields, "url"), rawFlexibleStringFieldCI(fields, "doi"), rawFlexibleStringFieldCI(fields, "link"))
		}
		sources = append(sources, src)
	}
	return sources
}

func rawFieldCI(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	for key, value := range fields {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return value, true
		}
	}
	return nil, false
}

func rawStringFieldCI(fields map[string]json.RawMessage, name string) string {
	raw, ok := rawFieldCI(fields, name)
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

func rawFlexibleStringFieldCI(fields map[string]json.RawMessage, name string) string {
	raw, ok := rawFieldCI(fields, name)
	if !ok {
		return ""
	}
	return rawFlexibleString(raw)
}

func rawFlexibleString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return ""
	}
	switch v := value.(type) {
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func rawStringSliceFieldCI(fields map[string]json.RawMessage, name string) []string {
	raw, ok := rawFieldCI(fields, name)
	if !ok {
		return nil
	}
	return parseFlexibleStringSlice(raw)
}

func parseFlexibleStringSlice(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '[' {
		items := []json.RawMessage{}
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil
		}
		out := []string{}
		for _, item := range items {
			if text := rawFlexibleString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	text := rawFlexibleString(raw)
	if text == "" {
		return nil
	}
	if strings.Contains(text, ",") {
		return strings.Split(text, ",")
	}
	return []string{text}
}

func rawIntFieldCI(fields map[string]json.RawMessage, name string) int {
	raw, ok := rawFieldCI(fields, name)
	if !ok {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func wireTapDiagnostics(projectName string, model ModelConfig, stage string) diagnosticsEntry {
	return diagnosticsEntry{Mode: "wiretap", Target: "WireTap", ModelID: modelIDString(model.ID), ModelLabel: strings.TrimSpace(model.Label), Stage: strings.TrimSpace(stage), Project: strings.TrimSpace(projectName)}
}

func (a *App) buildWireTapRuntimeSliceMessage(ctx context.Context, model ModelConfig, projectName, prompt string) (adapters.Message, error) {
	path, err := a.wireTapPath(projectName)
	if err != nil {
		return adapters.Message{}, err
	}
	doc, exists, err := readWireTapDocument(path)
	if err != nil {
		return adapters.Message{}, fmt.Errorf("could not read WireTap.json: %w", err)
	}
	if !exists || len(doc.Entries) == 0 {
		return adapters.Message{}, errors.New("WireTap.json is missing or empty")
	}
	limits := normalizeWireTapLimits(WireTapLimits{MaxEntries: doc.MaxEntries, DefaultRuntimeSliceEntries: doc.DefaultRuntimeSliceEntries, MaxRuntimeSliceEntries: doc.MaxRuntimeSliceEntries})
	if limits.MaxEntries <= 0 {
		limits = normalizeWireTapLimits(a.cfg.WireTap)
	}
	selection, err := a.runWireTapSelection(ctx, model, projectName, prompt, doc, limits)
	if err != nil {
		return adapters.Message{}, err
	}
	slice, stats := buildWireTapRuntimeSlice(doc, selection, limits)
	data, err := json.MarshalIndent(slice, "", "  ")
	if err != nil {
		return adapters.Message{}, err
	}
	a.logf(modelIDString(model.ID), "info", "WireTap runtime slice selected for project %s: selected=%d included=%d omitted=%d default_limit=%d hard_limit=%d required=%d/%d possible=%d/%d background=%d/%d", projectName, stats.SelectedTotal, stats.IncludedTotal, stats.OmittedTotal, stats.DefaultLimit, stats.HardLimit, stats.RequiredIncluded, stats.RequiredSelected, stats.PossibleIncluded, stats.PossibleSelected, stats.BackgroundIncluded, stats.BackgroundSelected)
	return buildTextMessage("user", `AGENTGO WIRETAP RUNTIME SLICE
You are answering the user's prompt using a focused AgentGO WireTap slice.
Use the AgentGO WireTap slice as compact, high-value reasoning context. Do not assume the slice is complete. Do not claim certainty beyond what the slice supports.
You may use outside/general knowledge if needed, but the default goal is to reason from the compact AgentGO WireTap slice.
If important information is missing from the slice, say what is missing. If the slice contains speculative or disputed entries, preserve that uncertainty in the answer.
Do not mention internal AgentGO WireTap mechanics unless relevant.

WireTap entry IDs such as WT-0001 are internal AgentGO evidence handles. Use them to select and reason over the provided runtime slice, but do not treat them as user-facing citations.

Final answer citation rules:
- Cite real source metadata only when a WireTap entry provides verified source metadata.
- Do not invent paper titles, authors, journals, years, DOIs, URLs, or citation details.
- If an entry has basis="source_hint", basis="model_knowledge", or no verified source metadata, use its claim as reasoning context only, not as a formal citation.
- Do not include a "References to WireTap Entries" section unless the user explicitly asks for traceability.
- When useful, briefly describe the evidence basis in plain language, such as "based on established theory", "model-level reasoning", or "source hints requiring verification".

`+string(data)), nil
}

func (a *App) runWireTapSelection(ctx context.Context, model ModelConfig, projectName, prompt string, doc WireTapDocument, limits WireTapLimits) (wireTapSelectionResponse, error) {
	input, err := buildWireTapSelectionInput(projectName, prompt, doc, limits)
	if err != nil {
		return wireTapSelectionResponse{}, err
	}
	a.publishDiagnostics(wireTapDiagnostics(projectName, model, "Selection Sent").withPrompt(previewForLog(input, 1200)).withSystemPrompt(previewForLog(wireTapSelectionSystemPrompt, 1200)))
	responseText, err := a.callStructuredTextModel(ctx, model, wireTapSelectionSystemPrompt, input, true, nil)
	if err != nil {
		a.publishDiagnostics(wireTapDiagnostics(projectName, model, "Selection Failed").withReason(err.Error()))
		return wireTapSelectionResponse{}, err
	}
	preview, label := diagnosticsResponsePreview(responseText, 1600)
	selection, err := parseWireTapSelectionResponse(responseText)
	if err != nil {
		a.publishDiagnostics(wireTapDiagnostics(projectName, model, "Selection Parse Failed").withResponse(preview).withResponseLabel(label).withReason(err.Error()))
		return wireTapSelectionResponse{}, err
	}
	a.publishDiagnostics(wireTapDiagnostics(projectName, model, "Selection Parsed").withResponse(preview).withResponseLabel(label).withStatusMessage(fmt.Sprintf("Required=%d possible=%d background=%d missing=%d", len(selection.RequiredEntries), len(selection.PossiblyRelevantEntries), len(selection.BackgroundEntries), len(selection.MissingNeededEvidence))))
	return selection, nil
}

func buildWireTapSelectionInput(projectName, prompt string, doc WireTapDocument, limits WireTapLimits) (string, error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(fmt.Sprintf(`AGENTGO WIRETAP SELECTION REQUEST
Project: %s
Default runtime slice entries: %d
Max runtime slice entries: %d

USER ORIGINAL PROMPT:
%s

FULL AGENTGO WIRETAP JSON:
%s`, projectName, limits.DefaultRuntimeSliceEntries, limits.MaxRuntimeSliceEntries, strings.TrimSpace(prompt), string(data))), nil
}

func parseWireTapSelectionResponse(raw string) (wireTapSelectionResponse, error) {
	clean, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		return wireTapSelectionResponse{}, err
	}
	var parsed wireTapSelectionResponse
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return wireTapSelectionResponse{}, err
	}
	parsed.RequiredEntries = compactWireTapIDs(parsed.RequiredEntries)
	parsed.PossiblyRelevantEntries = compactWireTapIDs(parsed.PossiblyRelevantEntries)
	parsed.BackgroundEntries = compactWireTapIDs(parsed.BackgroundEntries)
	parsed.ExcludedEntries = compactWireTapIDs(parsed.ExcludedEntries)
	parsed.MissingNeededEvidence = compactStringSlice(parsed.MissingNeededEvidence)
	parsed.Notes = strings.TrimSpace(parsed.Notes)
	return parsed, nil
}

func compactWireTapIDs(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		clean := strings.ToUpper(strings.TrimSpace(value))
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

type wireTapRuntimeSliceStats struct {
	RequiredSelected   int
	PossibleSelected   int
	BackgroundSelected int
	SelectedTotal      int
	RequiredIncluded   int
	PossibleIncluded   int
	BackgroundIncluded int
	IncludedTotal      int
	OmittedTotal       int
	DefaultLimit       int
	HardLimit          int
}

func buildWireTapRuntimeSlice(doc WireTapDocument, selection wireTapSelectionResponse, limits WireTapLimits) (map[string]any, wireTapRuntimeSliceStats) {
	entryByID := map[string]WireTapEntry{}
	for _, entry := range doc.Entries {
		entryByID[strings.ToUpper(strings.TrimSpace(entry.ID))] = entry
	}
	defaultLimit := limits.DefaultRuntimeSliceEntries
	if defaultLimit <= 0 {
		defaultLimit = wireTapDefaultRuntimeSliceEntries
	}
	hardLimit := limits.MaxRuntimeSliceEntries
	if hardLimit <= 0 {
		hardLimit = wireTapMaxRuntimeSliceEntries
	}
	if defaultLimit > hardLimit {
		defaultLimit = hardLimit
	}
	selectedIDs := []string{}
	appendTier := func(ids []string) []string {
		included := []string{}
		for _, id := range ids {
			id = strings.ToUpper(strings.TrimSpace(id))
			if id == "" || len(selectedIDs) >= defaultLimit {
				continue
			}
			if _, ok := entryByID[id]; !ok {
				continue
			}
			found := false
			for _, existing := range selectedIDs {
				if existing == id {
					found = true
					break
				}
			}
			if !found {
				selectedIDs = append(selectedIDs, id)
				included = append(included, id)
			}
		}
		return included
	}
	requiredIDs := appendTier(selection.RequiredEntries)
	possiblyRelevantIDs := appendTier(selection.PossiblyRelevantEntries)
	backgroundIDs := appendTier(selection.BackgroundEntries)
	entries := []WireTapEntry{}
	for _, id := range selectedIDs {
		entries = append(entries, entryByID[id])
	}
	stats := wireTapRuntimeSliceStats{
		RequiredSelected:   len(selection.RequiredEntries),
		PossibleSelected:   len(selection.PossiblyRelevantEntries),
		BackgroundSelected: len(selection.BackgroundEntries),
		RequiredIncluded:   len(requiredIDs),
		PossibleIncluded:   len(possiblyRelevantIDs),
		BackgroundIncluded: len(backgroundIDs),
		IncludedTotal:      len(entries),
		DefaultLimit:       defaultLimit,
		HardLimit:          hardLimit,
	}
	stats.SelectedTotal = stats.RequiredSelected + stats.PossibleSelected + stats.BackgroundSelected
	if stats.SelectedTotal > stats.IncludedTotal {
		stats.OmittedTotal = stats.SelectedTotal - stats.IncludedTotal
	}
	return map[string]any{
		"wiretap_runtime_slice":         true,
		"source_file":                   wireTapFileName,
		"research_tags":                 doc.ResearchTags,
		"default_runtime_slice_entries": defaultLimit,
		"max_runtime_slice_entries":     hardLimit,
		"required_entry_ids":            requiredIDs,
		"possibly_relevant_entry_ids":   possiblyRelevantIDs,
		"background_entry_ids":          backgroundIDs,
		"excluded_entry_ids":            selection.ExcludedEntries,
		"missing_needed_evidence":       selection.MissingNeededEvidence,
		"selection_notes":               selection.Notes,
		"entries":                       entries,
	}, stats
}

func (a *App) handleCypherStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	if projectName == "" {
		writeJSON(w, http.StatusOK, CypherStatusResponse{OK: true, Ready: false, Error: "You must select a project."})
		return
	}
	projectRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		writeJSON(w, http.StatusOK, CypherStatusResponse{OK: true, Ready: false, Error: err.Error()})
		return
	}
	manifestPath := filepath.Join(projectRoot, cypherManifestFileName)
	manifest, exists, err := readCypherManifest(manifestPath)
	if err != nil {
		writeJSON(w, http.StatusOK, CypherStatusResponse{OK: true, Ready: false, Error: err.Error()})
		return
	}
	if !exists {
		writeJSON(w, http.StatusOK, CypherStatusResponse{OK: true, Ready: false})
		return
	}
	relPath, _ := filepath.Rel(a.cfg.WorkRoot, manifestPath)
	writeJSON(w, http.StatusOK, CypherStatusResponse{
		OK:                   true,
		Ready:                !cypherManifestNeedsEnrichment(manifest),
		Path:                 filepath.ToSlash(relPath),
		FileCount:            manifest.FileCount,
		TransferableCount:    manifest.TransferableFileCount,
		LastBuilderSelection: normalizeCypherBuilderSelection(manifest.LastBuilderSelection),
	})
}

func (a *App) handleCypherBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	if projectName == "" {
		http.Error(w, "You must select a project.", http.StatusBadRequest)
		return
	}
	var req CypherBuildRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	activeBuilders := a.activeBuilderModelsSorted()
	if len(activeBuilders) < 1 {
		http.Error(w, "Must select at least one AI Builder", http.StatusBadRequest)
		return
	}
	if len(activeBuilders) > 2 {
		http.Error(w, "Only two maximum AI Builders allowed", http.StatusBadRequest)
		return
	}
	summaryModel := activeBuilders[0]
	workModel := activeBuilders[0]
	if strings.TrimSpace(req.SummaryBuilderID) != "" {
		selected, ok := a.activeBuilderModelByID(req.SummaryBuilderID)
		if !ok {
			http.Error(w, "Selected Cypher Summary Builder is not active.", http.StatusBadRequest)
			return
		}
		summaryModel = selected
	}
	if strings.TrimSpace(req.WorkBuilderID) != "" {
		selected, ok := a.activeBuilderModelByID(req.WorkBuilderID)
		if !ok {
			http.Error(w, "Selected Cypher Work Builder is not active.", http.StatusBadRequest)
			return
		}
		workModel = selected
	} else if len(activeBuilders) == 2 {
		workModel = activeBuilders[1]
	}
	selection := cypherBuilderSelectionForModels(summaryModel, workModel)
	a.deactivateUnselectedActiveBuilders(map[string]bool{selection.SummaryBuilderID: true, selection.WorkBuilderID: true})
	projectRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if empty, err := isDirEmpty(projectworkRoot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if empty {
		http.Error(w, "Your project folder can not be empty.", http.StatusBadRequest)
		return
	}

	executionID := fmt.Sprintf("cypher-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(r.Context())
	cypherActiveModelID := modelIDString(summaryModel.ID)
	a.mu.Lock()
	a.setActiveCancelLocked(cypherActiveModelID, projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.clearActiveCancelLocked(cypherActiveModelID, executionID)
		a.mu.Unlock()
	}()

	manifestPath := filepath.Join(projectRoot, cypherManifestFileName)
	previous, previousExists, err := readCypherManifest(manifestPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not read existing Cypher file: %v", err), http.StatusBadRequest)
		return
	}
	previous.LastBuilderSelection = selection
	manifest, err := a.buildCypherManifest(ctx, projectName, projectRoot, projectworkRoot, previous, previousExists)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			http.Error(w, "Cypher scan canceled.", http.StatusRequestTimeout)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	manifest.LastBuilderSelection = selection
	if len(manifest.Files) == 0 {
		http.Error(w, "Your project folder can not be empty.", http.StatusBadRequest)
		return
	}

	fileNamesChanged := !previousExists || !cypherFileListsEqual(previous.Files, manifest.Files)
	if err := writeCypherManifest(manifestPath, manifest); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !cypherManifestNeedsEnrichment(manifest) {
		relPath, _ := filepath.Rel(a.cfg.WorkRoot, manifestPath)
		enabled := !req.SummaryOnly
		message := "Cypher summaries already complete. Cypher armed for the next Execute Prompt."
		if req.SummaryOnly {
			message = "Cypher summaries already complete. Summary-only run finished."
		}
		a.logf("system", "info", "Cypher already complete for project %s: files=%d transferable=%d ready=true armed=%v", projectName, manifest.FileCount, manifest.TransferableFileCount, enabled)
		a.publishDiagnostics(cypherDiagnostics(projectName, summaryModel, "Already Complete").withStatusMessage("All transferable files are already AI-reviewed; skipping Cypher enrichment."))
		writeJSON(w, http.StatusOK, CypherBuildResponse{
			OK:                   true,
			Project:              projectName,
			Path:                 filepath.ToSlash(relPath),
			Ready:                true,
			Enabled:              enabled,
			Created:              !previousExists,
			FileNamesChanged:     fileNamesChanged,
			FileCount:            manifest.FileCount,
			TransferableCount:    manifest.TransferableFileCount,
			Message:              message,
			LastBuilderSelection: selection,
			Manifest:             manifest,
		})
		return
	}
	a.logf(modelIDString(summaryModel.ID), "info", "Cypher manifest updated for project %s. Starting summary enrichment with %s.", projectName, summaryModel.Label)
	a.publishDiagnostics(cypherDiagnostics(projectName, summaryModel, "Manifest Updated").withStatusMessage(fmt.Sprintf("Manifest files=%d transferable=%d. Starting summary enrichment.", manifest.FileCount, manifest.TransferableFileCount)).withResponse(fmt.Sprintf("Summary Builder: %s\nWork Builder: %s", strings.TrimSpace(summaryModel.Label), strings.TrimSpace(workModel.Label))))
	enrichedManifest, enrichmentComplete, err := a.runCypherEnrichment(ctx, summaryModel, projectName, manifest, projectworkRoot)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			a.publishDiagnostics(cypherDiagnostics(projectName, summaryModel, "Canceled").withReason("Cypher enrichment canceled."))
			http.Error(w, "Cypher enrichment canceled.", http.StatusRequestTimeout)
			return
		}
		a.logf(modelIDString(summaryModel.ID), "error", "Cypher enrichment failed for project %s: %v", projectName, err)
		failedEntry := cypherDiagnostics(projectName, summaryModel, "Failed").withReason(err.Error())
		var roundErr cypherEnrichmentRoundError
		if errors.As(err, &roundErr) {
			if strings.TrimSpace(roundErr.ResponsePreview) != "" {
				failedEntry = failedEntry.withResponse(roundErr.ResponsePreview).withResponseLabel(roundErr.ResponseLabel)
			}
			if strings.TrimSpace(roundErr.ParseDetail) != "" {
				failedEntry = failedEntry.withStatusMessage(fmt.Sprintf("Round %d parse detail: %s", roundErr.Round, roundErr.ParseDetail))
			}
			if writeErr := a.writeCypherEnrichmentFailureOutput(summaryModel, projectName, roundErr); writeErr != nil {
				a.logf(modelIDString(summaryModel.ID), "warn", "Failed to write Cypher failure card: %v", writeErr)
			}
		}
		a.publishDiagnostics(failedEntry)
		if errors.As(err, &roundErr) {
			http.Error(w, "Cypher enrichment failed: invalid AI JSON. Last safe Cypher.json was preserved.", http.StatusBadGateway)
		} else {
			http.Error(w, fmt.Sprintf("Cypher enrichment failed: %v", err), http.StatusBadGateway)
		}
		return
	}
	manifest = enrichedManifest
	manifest.LastBuilderSelection = selection
	ready := enrichmentComplete
	if err := writeCypherManifest(manifestPath, manifest); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	relPath, _ := filepath.Rel(a.cfg.WorkRoot, manifestPath)
	enabled := ready && !req.SummaryOnly
	message := "Cypher summaries complete. Cypher is armed for the next Execute Prompt."
	if req.SummaryOnly {
		message = "Cypher summaries complete. Summary-only run finished."
	} else if fileNamesChanged {
		message = "Cypher file list updated and summaries completed. Cypher is armed for the next Execute Prompt."
	}
	a.logf("system", "info", "Cypher updated for project %s: files=%d transferable=%d ready=%v armed=%v", projectName, manifest.FileCount, manifest.TransferableFileCount, ready, enabled)
	writeJSON(w, http.StatusOK, CypherBuildResponse{
		OK:                   true,
		Project:              projectName,
		Path:                 filepath.ToSlash(relPath),
		Ready:                ready,
		Enabled:              enabled,
		Created:              !previousExists,
		FileNamesChanged:     fileNamesChanged,
		FileCount:            manifest.FileCount,
		TransferableCount:    manifest.TransferableFileCount,
		Message:              message,
		LastBuilderSelection: selection,
		Manifest:             manifest,
	})
}

func (a *App) hasActiveBuilderModel() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	reviewerID := strings.TrimSpace(a.reviewerID)
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if modelID == reviewerID {
			continue
		}
		if a.toggles[modelID] {
			return true
		}
	}
	return false
}

func (a *App) firstActiveBuilderModel() (ModelConfig, bool) {
	a.mu.RLock()
	reviewerID := strings.TrimSpace(a.reviewerID)
	candidates := make([]ModelConfig, 0, len(a.cfg.Models))
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if modelID == "" || modelID == reviewerID {
			continue
		}
		if a.toggles[modelID] {
			candidates = append(candidates, model)
		}
	}
	a.mu.RUnlock()
	if len(candidates) == 0 {
		return ModelConfig{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftWave := normalizeRunOrder(candidates[i].RunOrder)
		rightWave := normalizeRunOrder(candidates[j].RunOrder)
		if leftWave != rightWave {
			return leftWave < rightWave
		}
		leftLabel := strings.ToLower(strings.TrimSpace(candidates[i].Label))
		rightLabel := strings.ToLower(strings.TrimSpace(candidates[j].Label))
		if leftLabel != rightLabel {
			return leftLabel < rightLabel
		}
		return modelIDString(candidates[i].ID) < modelIDString(candidates[j].ID)
	})
	return candidates[0], true
}

func (a *App) activeBuilderModelsSorted() []ModelConfig {
	a.mu.RLock()
	reviewerID := strings.TrimSpace(a.reviewerID)
	candidates := make([]ModelConfig, 0, len(a.cfg.Models))
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if modelID == "" || modelID == reviewerID {
			continue
		}
		if a.toggles[modelID] {
			candidates = append(candidates, model)
		}
	}
	a.mu.RUnlock()
	sort.SliceStable(candidates, func(i, j int) bool {
		leftWave := normalizeRunOrder(candidates[i].RunOrder)
		rightWave := normalizeRunOrder(candidates[j].RunOrder)
		if leftWave != rightWave {
			return leftWave < rightWave
		}
		leftLabel := strings.ToLower(strings.TrimSpace(candidates[i].Label))
		rightLabel := strings.ToLower(strings.TrimSpace(candidates[j].Label))
		if leftLabel != rightLabel {
			return leftLabel < rightLabel
		}
		return modelIDString(candidates[i].ID) < modelIDString(candidates[j].ID)
	})
	return candidates
}

func (a *App) activeBuilderModelByID(modelID string) (ModelConfig, bool) {
	cleanID := strings.TrimSpace(modelID)
	if cleanID == "" {
		return ModelConfig{}, false
	}
	for _, model := range a.activeBuilderModelsSorted() {
		if modelIDString(model.ID) == cleanID {
			return model, true
		}
	}
	return ModelConfig{}, false
}

func cypherBuilderSelectionForModels(summaryModel, workModel ModelConfig) CypherBuilderSelection {
	return CypherBuilderSelection{
		SummaryBuilderID:    modelIDString(summaryModel.ID),
		SummaryBuilderLabel: strings.TrimSpace(summaryModel.Label),
		WorkBuilderID:       modelIDString(workModel.ID),
		WorkBuilderLabel:    strings.TrimSpace(workModel.Label),
	}
}

func normalizeCypherBuilderSelection(selection CypherBuilderSelection) CypherBuilderSelection {
	selection.SummaryBuilderID = strings.TrimSpace(selection.SummaryBuilderID)
	selection.SummaryBuilderLabel = strings.TrimSpace(selection.SummaryBuilderLabel)
	selection.WorkBuilderID = strings.TrimSpace(selection.WorkBuilderID)
	selection.WorkBuilderLabel = strings.TrimSpace(selection.WorkBuilderLabel)
	return selection
}

func (a *App) deactivateUnselectedActiveBuilders(keep map[string]bool) {
	a.mu.Lock()
	deactivated := []string{}
	reviewerID := strings.TrimSpace(a.reviewerID)
	projectName := strings.TrimSpace(a.activeProjectName)
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if modelID == "" || modelID == reviewerID || !a.toggles[modelID] || keep[modelID] {
			continue
		}
		a.toggles[modelID] = false
		deactivated = append(deactivated, modelID)
	}
	a.mu.Unlock()
	if projectName != "" {
		for _, modelID := range deactivated {
			a.clearPendingMergeCount(projectName, modelID)
		}
	}
}

func cypherJSONTextFromModelResponse(raw string) (string, error) {
	clean := sanitizeModelJSONText(raw)
	if clean == "" {
		return "", errors.New("empty response")
	}
	if json.Valid([]byte(clean)) {
		return clean, nil
	}
	if extracted, _, _, ok := extractJSONObjectFromText(clean); ok {
		return extracted, nil
	}
	if candidate, _, _, ok := extractBalancedJSONObjectCandidate(clean); ok {
		if err := explainInvalidJSON(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", errors.New("no valid json object found in response")
}

func parseCypherObjectOrPatch(raw json.RawMessage, fullJSON string, base CypherManifest) (CypherManifest, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		var direct CypherManifest
		if err := json.Unmarshal([]byte(fullJSON), &direct); err == nil && direct.CypherVersion != 0 {
			return direct, false, nil
		}
		if base.CypherVersion != 0 {
			return base, true, nil
		}
		return CypherManifest{}, false, errors.New("AI Cypher response missing cypher object")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if base.CypherVersion != 0 {
			return base, true, nil
		}
		return CypherManifest{}, false, errors.New("AI Cypher response has null cypher object")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return CypherManifest{}, false, fmt.Errorf("AI returned invalid cypher object: %w", err)
	}

	// Partial Cypher patches are common during enrichment/execution. Treat them as
	// descriptive updates first so AI-owned mistakes in AgentGO-owned fields (for
	// example importance: "high") cannot break parsing before AgentGO restores
	// protected manifest metadata.
	if base.CypherVersion != 0 {
		if _, hasVersion := object["cypher_version"]; !hasVersion {
			merged, err := mergeCypherPartialUpdate(base, trimmed)
			if err != nil {
				return CypherManifest{}, false, err
			}
			return merged, true, nil
		}
	}

	var full CypherManifest
	if err := json.Unmarshal(trimmed, &full); err == nil && full.CypherVersion != 0 {
		return full, false, nil
	} else if base.CypherVersion != 0 {
		merged, mergeErr := mergeCypherPartialUpdate(base, trimmed)
		if mergeErr == nil {
			return merged, true, nil
		}
		if err != nil {
			return CypherManifest{}, false, fmt.Errorf("AI returned invalid cypher object: %w", err)
		}
		return CypherManifest{}, false, mergeErr
	}

	return CypherManifest{}, false, errors.New("AI Cypher response cypher object is incomplete: missing cypher_version")
}

func mergeCypherPartialUpdate(base CypherManifest, raw json.RawMessage) (CypherManifest, error) {
	merged := base
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher object: %w", err)
	}
	if value, ok := object["summary"]; ok {
		var summary string
		if err := json.Unmarshal(value, &summary); err != nil {
			return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher summary: %w", err)
		}
		merged.Summary = strings.TrimSpace(summary)
	}
	if value, ok := object["external_symbols"]; ok {
		var symbols []CypherAnchor
		if err := json.Unmarshal(value, &symbols); err != nil {
			return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher external_symbols: %w", err)
		}
		merged.ExternalSymbols = symbols
	}
	filesRaw, ok := object["files"]
	if !ok {
		return merged, nil
	}
	var partialFiles []json.RawMessage
	if err := json.Unmarshal(filesRaw, &partialFiles); err != nil {
		return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher files: %w", err)
	}
	fileIndex := make(map[string]int, len(merged.Files))
	for idx, file := range merged.Files {
		fileIndex[filepath.ToSlash(strings.TrimSpace(file.Path))] = idx
	}
	for _, fileRaw := range partialFiles {
		var fileObject map[string]json.RawMessage
		if err := json.Unmarshal(fileRaw, &fileObject); err != nil {
			return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher file entry: %w", err)
		}
		pathRaw, ok := fileObject["path"]
		if !ok {
			return CypherManifest{}, errors.New("AI returned partial cypher file entry without path")
		}
		var rel string
		if err := json.Unmarshal(pathRaw, &rel); err != nil {
			return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher file path: %w", err)
		}
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		idx, ok := fileIndex[rel]
		if !ok {
			continue
		}
		if err := applyCypherFilePartialUpdate(&merged.Files[idx], fileObject); err != nil {
			return CypherManifest{}, fmt.Errorf("AI returned invalid partial cypher update for %s: %w", rel, err)
		}
	}
	return merged, nil
}

func canonicalizeCypherAgentGOFields(base, updated CypherManifest) CypherManifest {
	enforceCypherManifestIdentity(&base)
	enforceCypherManifestIdentity(&updated)

	canonical := base
	canonical.Summary = strings.TrimSpace(updated.Summary)
	canonical.ExternalSymbols = nonNilCypherAnchors(updated.ExternalSymbols)

	updatedByPath := make(map[string]CypherFileEntry, len(updated.Files))
	for _, file := range updated.Files {
		rel := filepath.ToSlash(strings.TrimSpace(file.Path))
		if rel != "" {
			updatedByPath[rel] = file
		}
	}
	for idx := range canonical.Files {
		rel := filepath.ToSlash(strings.TrimSpace(canonical.Files[idx].Path))
		if aiFile, ok := updatedByPath[rel]; ok {
			copyCypherAIFields(&canonical.Files[idx], aiFile)
		}
	}
	return canonical
}

func copyCypherAIFields(file *CypherFileEntry, aiFile CypherFileEntry) {
	if file == nil {
		return
	}
	file.Summary = strings.TrimSpace(aiFile.Summary)
	file.SummaryStatus = normalizeCypherSummaryStatus(aiFile.SummaryStatus)
	file.Anchors = normalizeCypherStringList(aiFile.Anchors)
	file.Symbols = normalizeCypherStringList(aiFile.Symbols)
	file.Continuity = nonNilCypherContinuity(aiFile.Continuity)
	file.Dependencies = normalizeRelativePaths(aiFile.Dependencies)
	file.ReferencedBy = normalizeRelativePaths(aiFile.ReferencedBy)
}

func applyCypherFilePartialUpdate(file *CypherFileEntry, object map[string]json.RawMessage) error {
	if value, ok := object["summary"]; ok {
		summary, err := parseCypherTextField(value)
		if err != nil {
			return err
		}
		file.Summary = summary
	}
	if value, ok := object["summary_status"]; ok {
		var status string
		if err := json.Unmarshal(value, &status); err != nil {
			return err
		}
		file.SummaryStatus = normalizeCypherSummaryStatus(status)
	}
	if value, ok := object["anchors"]; ok {
		anchors, err := parseCypherStringListField(value)
		if err != nil {
			return err
		}
		file.Anchors = normalizeCypherStringList(anchors)
	}
	if value, ok := object["symbols"]; ok {
		symbols, err := parseCypherStringListField(value)
		if err != nil {
			return err
		}
		file.Symbols = normalizeCypherStringList(symbols)
	}
	if value, ok := object["continuity"]; ok {
		var continuity CypherContinuity
		if err := json.Unmarshal(value, &continuity); err != nil {
			return err
		}
		file.Continuity = continuity
	}
	if value, ok := object["dependencies"]; ok {
		var dependencies []string
		if err := json.Unmarshal(value, &dependencies); err != nil {
			return err
		}
		file.Dependencies = normalizeRelativePaths(dependencies)
	}
	if value, ok := object["referenced_by"]; ok {
		var referencedBy []string
		if err := json.Unmarshal(value, &referencedBy); err != nil {
			return err
		}
		file.ReferencedBy = normalizeRelativePaths(referencedBy)
	}
	return nil
}

func normalizeCypherSummaryStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "complete", "fresh", "updated", "current", "reviewed":
		return "complete"
	case "partial", "incomplete", "truncated", "limited":
		return "partial"
	case "skipped", "skip":
		return "skipped"
	case "failed", "error", "unreadable":
		return "failed"
	case "stale":
		return "stale"
	case "deleted":
		return "deleted"
	case "", "empty", "missing":
		return "empty"
	default:
		return "partial"
	}
}

func cypherStatusIndicatesReviewed(status string) bool {
	switch normalizeCypherSummaryStatus(status) {
	case "complete":
		return true
	default:
		return false
	}
}

func cypherStatusNeedsReview(status string) bool {
	switch normalizeCypherSummaryStatus(status) {
	case "", "empty", "stale", "partial", "failed":
		return true
	default:
		return false
	}
}

func cypherContinuityIsEmpty(continuity CypherContinuity) bool {
	return len(continuity.Characters) == 0 &&
		len(continuity.Relationships) == 0 &&
		len(continuity.TimelineEvents) == 0 &&
		len(continuity.Locations) == 0 &&
		len(continuity.Rules) == 0 &&
		len(continuity.Contradictions) == 0
}

func cypherSummaryLooksWeak(file CypherFileEntry) bool {
	summary := strings.TrimSpace(file.Summary)
	if summary == "" {
		return true
	}
	lower := strings.ToLower(summary)
	switch lower {
	case "summary", "todo", "tbd", "n/a", "na", "none", "unknown", "placeholder", "empty":
		return true
	}
	pathClean := strings.ToLower(filepath.ToSlash(strings.TrimSpace(file.Path)))
	baseClean := strings.ToLower(strings.TrimSpace(path.Base(pathClean)))
	trimCutset := " ./\\\t\r\n"
	summaryClean := strings.ToLower(strings.Trim(summary, trimCutset))
	if summaryClean == pathClean || summaryClean == baseClean {
		return true
	}
	trimmedPath := strings.Trim(pathClean, trimCutset)
	if trimmedPath != "" && strings.EqualFold(summaryClean, trimmedPath) {
		return true
	}
	return false
}

func cypherStatusExplicitlyIncomplete(status string) bool {
	switch normalizeCypherSummaryStatus(status) {
	case "partial", "failed", "empty", "stale":
		return true
	default:
		return false
	}
}

func markCypherFileReviewed(file *CypherFileEntry) {
	if file == nil {
		return
	}
	file.AIReviewed = true
	if cypherSummaryLooksWeak(*file) || cypherStatusExplicitlyIncomplete(file.SummaryStatus) {
		file.SummaryStatus = "partial"
	} else if cypherStatusNeedsReview(file.SummaryStatus) {
		file.SummaryStatus = "complete"
	}
	file.Anchors = normalizeCypherStringList(file.Anchors)
	file.Symbols = normalizeCypherStringList(file.Symbols)
	file.Continuity = nonNilCypherContinuity(file.Continuity)
	file.Dependencies = normalizeRelativePaths(file.Dependencies)
	file.ReferencedBy = normalizeRelativePaths(file.ReferencedBy)
}

func applyCypherRawDescriptiveUpdate(base CypherManifest, raw json.RawMessage) (CypherManifest, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return base, nil
	}
	merged, _, err := parseCypherObjectOrPatch(raw, "", base)
	if err != nil {
		return base, err
	}
	return canonicalizeCypherAgentGOFields(base, merged), nil
}

func cypherFilesForBuilderOps(manifest CypherManifest, ops []builderFileOp) []CypherFileEntry {
	if len(ops) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, op := range ops {
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel != "" {
			wanted[rel] = true
		}
	}
	files := []CypherFileEntry{}
	for _, file := range manifest.Files {
		if wanted[filepath.ToSlash(strings.TrimSpace(file.Path))] {
			files = append(files, file)
		}
	}
	return files
}

func gateCypherFileReviewUpdates(base CypherManifest, updated *CypherManifest, reviewedFiles []CypherFileEntry) {
	if updated == nil || len(updated.Files) == 0 {
		return
	}
	reviewedByPath := make(map[string]bool, len(reviewedFiles))
	for _, file := range reviewedFiles {
		rel := filepath.ToSlash(strings.TrimSpace(file.Path))
		if rel != "" {
			reviewedByPath[rel] = true
		}
	}
	baseByPath := make(map[string]CypherFileEntry, len(base.Files))
	for _, file := range base.Files {
		baseByPath[filepath.ToSlash(strings.TrimSpace(file.Path))] = file
	}
	for idx := range updated.Files {
		rel := filepath.ToSlash(strings.TrimSpace(updated.Files[idx].Path))
		if reviewedByPath[rel] {
			continue
		}
		baseFile, ok := baseByPath[rel]
		if !ok {
			continue
		}
		restoreCypherFileReviewFields(&updated.Files[idx], baseFile)
	}
}

func restoreCypherFileReviewFields(file *CypherFileEntry, base CypherFileEntry) {
	if file == nil {
		return
	}
	file.Summary = base.Summary
	file.SummaryStatus = base.SummaryStatus
	file.AIReviewed = base.AIReviewed
	file.Importance = base.Importance
	file.Anchors = normalizeCypherStringList(base.Anchors)
	file.Symbols = normalizeCypherStringList(base.Symbols)
	file.Continuity = nonNilCypherContinuity(base.Continuity)
	file.Dependencies = normalizeRelativePaths(base.Dependencies)
	file.ReferencedBy = normalizeRelativePaths(base.ReferencedBy)
}

func markCypherReviewedFiles(manifest *CypherManifest, reviewedFiles []CypherFileEntry) {
	if manifest == nil || len(reviewedFiles) == 0 {
		return
	}
	reviewedByPath := make(map[string]bool, len(reviewedFiles))
	for _, file := range reviewedFiles {
		rel := filepath.ToSlash(strings.TrimSpace(file.Path))
		if rel != "" {
			reviewedByPath[rel] = true
		}
	}
	for idx := range manifest.Files {
		if reviewedByPath[filepath.ToSlash(strings.TrimSpace(manifest.Files[idx].Path))] {
			markCypherFileReviewed(&manifest.Files[idx])
		}
	}
}

func cypherFileNeedsEnrichment(file CypherFileEntry) bool {
	if !strings.EqualFold(strings.TrimSpace(file.Kind), "text") || !file.TransferAllowed || file.Excluded || file.NeverSend {
		return false
	}
	if !file.AIReviewed {
		return true
	}
	if cypherStatusNeedsReview(file.SummaryStatus) {
		return true
	}
	return cypherSummaryLooksWeak(file)
}

func cypherManifestNeedsEnrichment(manifest CypherManifest) bool {
	for _, file := range manifest.Files {
		if cypherFileNeedsEnrichment(file) {
			return true
		}
	}
	return false
}

func formatCypherJSON(manifest CypherManifest) (string, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type cypherEnrichmentWorklistView struct {
	Project                   string                     `json:"project"`
	ContentDomain             string                     `json:"content_domain"`
	Summary                   string                     `json:"summary,omitempty"`
	RemainingFilesToEnrich    int                        `json:"remaining_files_to_enrich"`
	CompletedSummariesOmitted bool                       `json:"completed_summaries_omitted"`
	Files                     []cypherEnrichmentFileView `json:"files"`
}

type cypherEnrichmentFileView struct {
	Path           string   `json:"path"`
	Language       string   `json:"language,omitempty"`
	ContentKind    string   `json:"content_kind,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	SizeBytes      int64    `json:"size_bytes"`
	SummaryStatus  string   `json:"summary_status"`
	CurrentSummary string   `json:"current_summary,omitempty"`
	CurrentAnchors []string `json:"current_anchors,omitempty"`
	CurrentSymbols []string `json:"current_symbols,omitempty"`
}

func cypherAnchorStrings(anchors []string) []string {
	return normalizeCypherStringList(anchors)
}

func cypherEnrichmentWorklistFiles(manifest CypherManifest) []CypherFileEntry {
	files := make([]CypherFileEntry, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		if cypherFileNeedsEnrichment(file) {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func cypherEnrichmentRequestManifest(manifest CypherManifest) CypherManifest {
	requestManifest := manifest
	requestManifest.Files = cypherEnrichmentWorklistFiles(manifest)
	requestManifest.DirectoryStructure = make([]string, 0, len(requestManifest.Files))
	requestManifest.FileCount = len(requestManifest.Files)
	requestManifest.TransferableFileCount = 0
	requestManifest.TokenEstimate = 0
	for _, file := range requestManifest.Files {
		requestManifest.DirectoryStructure = append(requestManifest.DirectoryStructure, file.Path)
		requestManifest.TokenEstimate += file.TokenEstimate
		if file.TransferAllowed && !file.Excluded && !file.NeverSend {
			requestManifest.TransferableFileCount++
		}
	}
	return requestManifest
}

func buildCypherEnrichmentWorklistView(manifest CypherManifest) cypherEnrichmentWorklistView {
	requestManifest := cypherEnrichmentRequestManifest(manifest)
	files := make([]cypherEnrichmentFileView, 0, len(requestManifest.Files))
	for _, file := range requestManifest.Files {
		files = append(files, cypherEnrichmentFileView{
			Path:           file.Path,
			Language:       file.Language,
			ContentKind:    file.ContentKind,
			Kind:           file.Kind,
			SizeBytes:      file.SizeBytes,
			SummaryStatus:  normalizeCypherSummaryStatus(file.SummaryStatus),
			CurrentSummary: strings.TrimSpace(file.Summary),
			CurrentAnchors: cypherAnchorStrings(file.Anchors),
			CurrentSymbols: normalizeCypherStringList(file.Symbols),
		})
	}
	return cypherEnrichmentWorklistView{
		Project:                   manifest.Project,
		ContentDomain:             manifest.ContentDomain,
		Summary:                   strings.TrimSpace(manifest.Summary),
		RemainingFilesToEnrich:    len(requestManifest.Files),
		CompletedSummariesOmitted: true,
		Files:                     files,
	}
}

func buildCypherEnrichmentInput(file CypherFileEntry, projectworkRoot string) (string, error) {
	path, err := safeJoin(projectworkRoot, file.Path)
	if err != nil {
		return "", err
	}
	data, err := readFileUnderRoot(projectworkRoot, path)
	if err != nil {
		return "", err
	}
	metadata := map[string]string{}
	if strings.TrimSpace(file.Language) != "" {
		metadata["language"] = strings.TrimSpace(file.Language)
	}
	if strings.TrimSpace(file.ContentKind) != "" {
		metadata["content_kind"] = strings.TrimSpace(file.ContentKind)
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("AGENTGO CYPHER PHASE 1 SINGLE-FILE ENRICHMENT\n")
	b.WriteString("AgentGO will save your returned metadata to the file entry it selected.\n")
	if len(metadata) > 0 {
		b.WriteString("\nFILE METADATA:\n```json\n")
		b.Write(metadataJSON)
		b.WriteString("\n```\n")
	}
	b.WriteString("\nPROVIDED FILE CONTENT:\n```\n")
	b.Write(data)
	b.WriteString("\n```\n")
	return b.String(), nil
}

type cypherEnrichmentMetadataUpdate struct {
	Summary    string
	Anchors    []string
	Symbols    []string
	Continuity CypherContinuity
}

func parseCypherEnrichmentMetadataResponse(raw string) (cypherEnrichmentMetadataUpdate, error) {
	clean, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("AI returned invalid Cypher enrichment JSON: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(clean), &object); err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("AI returned invalid Cypher enrichment JSON: %w", err)
	}
	requiredTopLevel := []string{"summary", "anchors", "symbols", "continuity"}
	for _, field := range requiredTopLevel {
		rawField, ok := object[field]
		if !ok || len(bytes.TrimSpace(rawField)) == 0 || bytes.Equal(bytes.TrimSpace(rawField), []byte("null")) {
			return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("AI returned invalid Cypher enrichment JSON: missing required field %q", field)
		}
	}
	summary, err := parseCypherTextField(object["summary"])
	if err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("summary: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return cypherEnrichmentMetadataUpdate{}, errors.New("AI returned invalid Cypher enrichment JSON: summary must be non-empty")
	}
	anchors, err := parseCypherStringListField(object["anchors"])
	if err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("anchors: %w", err)
	}
	symbols, err := parseCypherStringListField(object["symbols"])
	if err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("symbols: %w", err)
	}
	var continuityObject map[string]json.RawMessage
	if err := json.Unmarshal(object["continuity"], &continuityObject); err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("continuity: %w", err)
	}
	requiredContinuity := []string{"characters", "relationships", "timeline_events", "locations", "rules", "contradictions"}
	for _, field := range requiredContinuity {
		rawField, ok := continuityObject[field]
		if !ok || len(bytes.TrimSpace(rawField)) == 0 || bytes.Equal(bytes.TrimSpace(rawField), []byte("null")) {
			return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("AI returned invalid Cypher enrichment JSON: missing required continuity.%s", field)
		}
	}
	var continuity CypherContinuity
	if err := json.Unmarshal(object["continuity"], &continuity); err != nil {
		return cypherEnrichmentMetadataUpdate{}, fmt.Errorf("continuity: %w", err)
	}
	return cypherEnrichmentMetadataUpdate{
		Summary:    strings.TrimSpace(summary),
		Anchors:    normalizeCypherStringList(anchors),
		Symbols:    normalizeCypherStringList(symbols),
		Continuity: nonNilCypherContinuity(continuity),
	}, nil
}

func applyCypherEnrichmentMetadataUpdate(manifest *CypherManifest, file CypherFileEntry, update cypherEnrichmentMetadataUpdate) error {
	if manifest == nil {
		return errors.New("missing Cypher manifest")
	}
	rel := filepath.ToSlash(strings.TrimSpace(file.Path))
	if rel == "" {
		return errors.New("missing Cypher file path")
	}
	for idx := range manifest.Files {
		if filepath.ToSlash(strings.TrimSpace(manifest.Files[idx].Path)) != rel {
			continue
		}
		manifest.Files[idx].Summary = strings.TrimSpace(update.Summary)
		manifest.Files[idx].SummaryStatus = "complete"
		manifest.Files[idx].Anchors = normalizeCypherStringList(update.Anchors)
		manifest.Files[idx].Symbols = normalizeCypherStringList(update.Symbols)
		manifest.Files[idx].Continuity = nonNilCypherContinuity(update.Continuity)
		manifest.Files[idx].Dependencies = normalizeRelativePaths(manifest.Files[idx].Dependencies)
		manifest.Files[idx].ReferencedBy = normalizeRelativePaths(manifest.Files[idx].ReferencedBy)
		manifest.Files[idx].AIReviewed = true
		return nil
	}
	return fmt.Errorf("Cypher enrichment target file not found in manifest: %s", rel)
}

func nextCypherEnrichmentFile(manifest CypherManifest) (CypherFileEntry, bool) {
	files := cypherEnrichmentWorklistFiles(manifest)
	if len(files) == 0 {
		return CypherFileEntry{}, false
	}
	return files[0], true
}

type cypherEnrichmentRoundError struct {
	Round           int
	Err             error
	ResponsePreview string
	ResponseLabel   string
	ParseDetail     string
}

func (e cypherEnrichmentRoundError) Error() string {
	if e.Err == nil {
		return "Cypher enrichment failed"
	}
	return e.Err.Error()
}

func (e cypherEnrichmentRoundError) Unwrap() error { return e.Err }

func jsonErrorContext(candidate string, err error, contextBytes int) string {
	if strings.TrimSpace(candidate) == "" || err == nil {
		return ""
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) || syntaxErr.Offset <= 0 {
		return ""
	}
	offset := int(syntaxErr.Offset)
	if offset > len(candidate) {
		offset = len(candidate)
	}
	if contextBytes <= 0 {
		contextBytes = 240
	}
	start := offset - contextBytes
	if start < 0 {
		start = 0
	}
	end := offset + contextBytes
	if end > len(candidate) {
		end = len(candidate)
	}
	snippet := strings.ReplaceAll(candidate[start:end], "\r\n", "\n")
	return fmt.Sprintf("JSON parse location near byte %d: %s", offset, snippet)
}

func cypherInvalidJSONDetail(raw string) string {
	clean := sanitizeModelJSONText(raw)
	if strings.TrimSpace(clean) == "" {
		return "AI response was empty after sanitizing markdown/code fences."
	}
	candidate := clean
	if !json.Valid([]byte(candidate)) {
		if balanced, _, _, ok := extractBalancedJSONObjectCandidate(clean); ok {
			candidate = balanced
		}
	}
	err := explainInvalidJSON(candidate)
	if err == nil {
		return "AI response JSON was syntactically valid, but failed schema validation."
	}
	if detail := jsonErrorContext(candidate, err, 360); detail != "" {
		return fmt.Sprintf("%v. %s", err, detail)
	}
	return err.Error()
}

func cypherDiagnostics(projectName string, model ModelConfig, stage string) diagnosticsEntry {
	return diagnosticsEntry{
		Mode:       "cypher",
		Target:     "Cypher",
		ModelID:    modelIDString(model.ID),
		ModelLabel: strings.TrimSpace(model.Label),
		Stage:      strings.TrimSpace(stage),
		Project:    strings.TrimSpace(projectName),
	}
}

func cypherDiagnosticFileRefForRelativePath(relPath string) diagnosticsFileRef {
	clean := filepath.ToSlash(strings.TrimSpace(relPath))
	return makeDiagnosticsFileRef(clean)
}

func (a *App) saveCypherRawDiagnosticResponse(metaRoot string, model ModelConfig, projectName, label, raw string) (diagnosticsFileRef, string, error) {
	retention := a.cfg.MaxResponseHistory
	if retention <= 0 {
		return diagnosticsFileRef{}, "", nil
	}
	diagnosticsRoot := filepath.Join(metaRoot, "cypher_diagnostics")
	if err := os.MkdirAll(diagnosticsRoot, 0o755); err != nil {
		return diagnosticsFileRef{}, "", err
	}
	slug := strings.ToLower(strings.TrimSpace(label))
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = slugCleaner.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "response"
	}
	filename := fmt.Sprintf("cypher_%s_%s.json", slug, time.Now().Format("20060102_150405_000000000"))
	fullPath := filepath.Join(diagnosticsRoot, filename)
	if err := os.WriteFile(fullPath, []byte(strings.TrimSpace(raw)), 0o644); err != nil {
		return diagnosticsFileRef{}, "", err
	}
	if err := pruneCypherDiagnosticResponses(diagnosticsRoot, retention); err != nil {
		return diagnosticsFileRef{}, "", err
	}
	relMeta := a.relativeMetaPath(model, projectName)
	if relMeta == "" {
		return diagnosticsFileRef{Name: filename, Path: filepath.ToSlash(fullPath)}, filename, nil
	}
	relPath := filepath.ToSlash(filepath.Join(relMeta, "cypher_diagnostics", filename))
	return cypherDiagnosticFileRefForRelativePath(relPath), relPath, nil
}

func (a *App) saveWireTapRawDiagnosticResponse(model ModelConfig, projectName, label, raw string) (diagnosticsFileRef, string, error) {
	diagnosticsRoot, err := a.projectDiagnosticsDir(projectName)
	if err != nil {
		return diagnosticsFileRef{}, "", err
	}
	if err := os.MkdirAll(diagnosticsRoot, 0o755); err != nil {
		return diagnosticsFileRef{}, "", err
	}
	slug := strings.ToLower(strings.TrimSpace(label))
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = slugCleaner.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "response"
	}
	filename := fmt.Sprintf("wiretap_%s.json", slug)
	fullPath := filepath.Join(diagnosticsRoot, filename)
	if err := os.WriteFile(fullPath, []byte(strings.TrimSpace(raw)), 0o644); err != nil {
		return diagnosticsFileRef{}, "", err
	}
	relPath := filepath.ToSlash(filepath.Join("projects", projectName, "diagnostics", filename))
	return makeDiagnosticsFileRef(relPath), relPath, nil
}

func pruneWireTapDiagnosticResponses(root string, retention int) error {
	return prunePrefixedDiagnosticResponses(root, retention, "wiretap_")
}

func pruneCypherDiagnosticResponses(root string, retention int) error {
	return prunePrefixedDiagnosticResponses(root, retention, "cypher_")
}

func prunePrefixedDiagnosticResponses(root string, retention int, prefix string) error {
	if retention <= 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	type diagFile struct {
		name string
		mod  time.Time
	}
	files := []diagFile{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, diagFile{name: name, mod: info.ModTime()})
	}
	if len(files) <= retention {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.After(files[j].mod)
	})
	for _, file := range files[retention:] {
		if err := os.Remove(filepath.Join(root, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func cypherFileEntryPaths(files []CypherFileEntry) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func (a *App) runCypherEnrichment(ctx context.Context, model ModelConfig, projectName string, manifest CypherManifest, projectworkRoot string) (CypherManifest, bool, error) {
	current := manifest
	maxRounds := cypherMaxEnrichmentRounds
	if needed := len(cypherEnrichmentWorklistFiles(current)) + 5; needed > maxRounds {
		maxRounds = needed
	}
	for round := 0; round < maxRounds; round++ {
		roundNumber := round + 1
		select {
		case <-ctx.Done():
			return current, false, ctx.Err()
		default:
		}
		file, ok := nextCypherEnrichmentFile(current)
		if !ok {
			a.publishDiagnostics(cypherDiagnostics(projectName, model, "Complete").withStatusMessage("Cypher Phase 1 summaries complete. No files remain in the enrichment queue."))
			return current, true, nil
		}
		rel := filepath.ToSlash(strings.TrimSpace(file.Path))
		input, err := buildCypherEnrichmentInput(file, projectworkRoot)
		if err != nil {
			return current, false, err
		}
		remaining := len(cypherEnrichmentWorklistFiles(current))
		a.logf(modelIDString(model.ID), "info", "Cypher enrichment round %d sending one file for Phase 1 metadata. remaining_files=%d target=%s", roundNumber, remaining, rel)
		a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Sent", roundNumber)).withStatusMessage(fmt.Sprintf("Phase 1 single-file enrichment. Remaining files: %d", remaining)).withSystemPrompt(previewForLog(cypherEnrichmentSystemPrompt, 1200)).withPrompt(previewForLog(input, 1200)))
		responseText, err := a.callStructuredTextModel(ctx, model, cypherEnrichmentSystemPrompt, input, true, nil)
		if err != nil {
			a.logf(modelIDString(model.ID), "error", "Cypher enrichment round %d request failed for %s: %v", roundNumber, rel, err)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Failed", roundNumber)).withReason(err.Error()))
			return current, false, err
		}
		responsePreview, responseLabel := diagnosticsResponsePreview(responseText, 1200)
		if strings.TrimSpace(responseText) == "" {
			a.logf(modelIDString(model.ID), "warn", "Cypher enrichment round %d received empty AI response for %s.", roundNumber, rel)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Empty Response", roundNumber)).withReason("AI returned an empty response."))
		} else {
			a.logf(modelIDString(model.ID), "info", "Cypher enrichment round %d received AI response for %s (%d bytes).", roundNumber, rel, len(responseText))
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Response Received", roundNumber)).withResponse(responsePreview).withResponseLabel(responseLabel).withStatusMessage(fmt.Sprintf("Raw response bytes: %d", len(responseText))))
		}
		update, err := parseCypherEnrichmentMetadataResponse(responseText)
		if err != nil {
			parseDetail := cypherInvalidJSONDetail(responseText)
			diagnosticResponse, diagnosticResponseLabel := diagnosticsResponsePreview(responseText, 5000)
			reason := err.Error()
			if strings.TrimSpace(parseDetail) != "" {
				reason = reason + "\n" + parseDetail
			}
			a.logf(modelIDString(model.ID), "error", "Cypher enrichment round %d parse failed for %s: %v. Parse detail: %s. Response preview: %s", roundNumber, rel, err, parseDetail, previewForLog(responseText, 900))
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Parse Failed", roundNumber)).withResponse(diagnosticResponse).withResponseLabel(diagnosticResponseLabel).withReason(reason).withStatusMessage("Cypher Phase 1 expected one strict metadata JSON object for the attached file."))
			return current, false, cypherEnrichmentRoundError{Round: roundNumber, Err: err, ResponsePreview: diagnosticResponse, ResponseLabel: diagnosticResponseLabel, ParseDetail: parseDetail}
		}
		if err := applyCypherEnrichmentMetadataUpdate(&current, file, update); err != nil {
			a.logf(modelIDString(model.ID), "error", "Cypher enrichment round %d update failed for %s: %v", roundNumber, rel, err)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Update Failed", roundNumber)).withResponse(responsePreview).withResponseLabel(responseLabel).withReason(err.Error()))
			return current, false, err
		}
		current.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		a.logf(modelIDString(model.ID), "info", "Cypher enrichment round %d saved Phase 1 metadata for %s.", roundNumber, rel)
		a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Round %d Saved", roundNumber)).withStatusMessage("Saved required Phase 1 metadata for one file and marked it AI-reviewed.").withResponse(responsePreview).withResponseLabel(responseLabel))
	}
	return current, false, fmt.Errorf("Cypher enrichment exceeded %d rounds", maxRounds)
}

func currentAIContextText(metaRoot string) string {
	path := filepath.Join(metaRoot, "ai_context.json")
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return strings.TrimSpace(string(defaultAIContextJSON()))
	}
	ctx, ok := parseAIContextText(string(data))
	if !ok {
		return strings.TrimSpace(string(defaultAIContextJSON()))
	}
	formatted := formatAIContextObject(ctx)
	if strings.TrimSpace(formatted) != strings.TrimSpace(string(data)) {
		_ = atomicWriteFile(path, []byte(formatted), 0o644)
	}
	return strings.TrimSpace(formatted)
}

type cypherActionFileView struct {
	Path       string           `json:"path"`
	Importance CypherImportance `json:"importance"`
	Summary    string           `json:"summary"`
	Anchors    []string         `json:"anchors"`
	Symbols    []string         `json:"symbols"`
	Continuity CypherContinuity `json:"continuity"`
}

type cypherActionCandidateView struct {
	Path       string           `json:"path"`
	Importance CypherImportance `json:"importance"`
}

type cypherActionContextView struct {
	AvailableFiles            []cypherActionFileView      `json:"available_files"`
	ActualFilesRead           []string                    `json:"actual_files_read"`
	CurrentAttachedFile       string                      `json:"current_attached_file"`
	RemainingRankedCandidates []cypherActionCandidateView `json:"remaining_ranked_candidates"`
	RunLog                    []cypherActionRunLogEntry   `json:"run_log"`
}

type cypherActionRunLogEntry struct {
	Summary string   `json:"summary"`
	Updated []string `json:"updated"`
	Created []string `json:"created"`
	Deleted []string `json:"deleted"`
	Reason  string   `json:"reason"`
}

type cypherActionAIResponse struct {
	FileOperations []builderFileOp         `json:"file_operations"`
	RequestedFiles []string                `json:"-"`
	RunLogEntry    cypherActionRunLogEntry `json:"run_log_entry"`
	FinalResponse  string                  `json:"final_response"`
}

func normalizeCypherActionRunLogEntry(entry cypherActionRunLogEntry) cypherActionRunLogEntry {
	entry.Summary = strings.TrimSpace(entry.Summary)
	entry.Reason = strings.TrimSpace(entry.Reason)
	entry.Updated = normalizeRelativePaths(entry.Updated)
	entry.Created = normalizeRelativePaths(entry.Created)
	entry.Deleted = normalizeRelativePaths(entry.Deleted)
	if entry.Updated == nil {
		entry.Updated = []string{}
	}
	if entry.Created == nil {
		entry.Created = []string{}
	}
	if entry.Deleted == nil {
		entry.Deleted = []string{}
	}
	return entry
}

func normalizeCypherRequestedFilePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, rel := range paths {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		rel = strings.TrimPrefix(rel, "/")
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out
}

func parseCypherRequestedFileField(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []string{}, nil
	}
	var single string
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return normalizeCypherRequestedFilePaths([]string{single}), nil
	}
	var multiple []string
	if err := json.Unmarshal(trimmed, &multiple); err == nil {
		return normalizeCypherRequestedFilePaths(multiple), nil
	}
	return nil, errors.New("requested_file must be a string, empty string, null, or string array")
}

func parseCypherActionAIResponse(raw string) (cypherActionAIResponse, error) {
	clean, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		return cypherActionAIResponse{}, fmt.Errorf("AI returned invalid Cypher Action JSON: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(clean), &object); err != nil {
		return cypherActionAIResponse{}, fmt.Errorf("AI returned invalid Cypher Action JSON: %w", err)
	}
	for _, field := range []string{"file_operations", "requested_file", "run_log_entry", "final_response"} {
		if _, ok := object[field]; !ok {
			return cypherActionAIResponse{}, fmt.Errorf("AI returned invalid Cypher Action JSON: missing required field %q", field)
		}
	}
	var ops []builderFileOp
	if err := json.Unmarshal(object["file_operations"], &ops); err != nil {
		return cypherActionAIResponse{}, fmt.Errorf("file_operations: %w", err)
	}
	requested, err := parseCypherRequestedFileField(object["requested_file"])
	if err != nil {
		return cypherActionAIResponse{}, err
	}
	var logEntry cypherActionRunLogEntry
	if err := json.Unmarshal(object["run_log_entry"], &logEntry); err != nil {
		return cypherActionAIResponse{}, fmt.Errorf("run_log_entry: %w", err)
	}
	logEntry = normalizeCypherActionRunLogEntry(logEntry)
	if strings.TrimSpace(logEntry.Summary) == "" {
		return cypherActionAIResponse{}, errors.New("run_log_entry.summary is required")
	}
	finalResponse, err := parseCypherTextField(object["final_response"])
	if err != nil {
		return cypherActionAIResponse{}, fmt.Errorf("final_response: %w", err)
	}
	return cypherActionAIResponse{FileOperations: ops, RequestedFiles: requested, RunLogEntry: logEntry, FinalResponse: strings.TrimSpace(finalResponse)}, nil
}

type cypherRankingFileView struct {
	Path       string           `json:"path"`
	Summary    string           `json:"summary"`
	Anchors    []string         `json:"anchors"`
	Symbols    []string         `json:"symbols"`
	Continuity CypherContinuity `json:"continuity"`
}

type cypherRankingContextView struct {
	AvailableFiles []cypherRankingFileView `json:"available_files"`
}

type cypherRankingFileScore struct {
	Path                string `json:"path"`
	InferenceImportance int    `json:"inference_importance"`
}

type cypherRankingAIResponse struct {
	RankedFiles []cypherRankingFileScore `json:"ranked_files"`
	SearchTerms []string                 `json:"search_terms"`
}

func resetCypherImportance(manifest *CypherManifest) {
	if manifest == nil {
		return
	}
	for idx := range manifest.Files {
		manifest.Files[idx].Importance = CypherImportance{}
	}
}

func cypherRankingContextFromManifest(manifest CypherManifest) cypherRankingContextView {
	files := make([]cypherRankingFileView, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		if !cypherFileIsSendable(file) {
			continue
		}
		files = append(files, cypherRankingFileView{
			Path:       filepath.ToSlash(strings.TrimSpace(file.Path)),
			Summary:    strings.TrimSpace(file.Summary),
			Anchors:    cypherAnchorStrings(file.Anchors),
			Symbols:    normalizeCypherStringList(file.Symbols),
			Continuity: nonNilCypherContinuity(file.Continuity),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return cypherRankingContextView{AvailableFiles: files}
}

func buildCypherRankingInput(prompt string, manifest CypherManifest) (string, error) {
	contextJSON, err := json.MarshalIndent(cypherRankingContextFromManifest(manifest), "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("CURRENT USER OBJECTIVE:\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\nAGENTGO CYPHER PHASE 2 RANKING CONTEXT:\n")
	b.WriteString("This AI-facing Cypher context includes only file paths, summaries, anchors, symbols, and continuity notes. Rank likely-relevant files and provide only useful deterministic search terms.\n```json\n")
	b.Write(contextJSON)
	b.WriteString("\n```\n")
	return b.String(), nil
}

func parseCypherRankingAIResponse(raw string) (cypherRankingAIResponse, error) {
	clean, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		return cypherRankingAIResponse{}, fmt.Errorf("AI returned invalid Cypher ranking JSON: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(clean), &object); err != nil {
		return cypherRankingAIResponse{}, fmt.Errorf("AI returned invalid Cypher ranking JSON: %w", err)
	}
	if _, ok := object["ranked_files"]; !ok {
		return cypherRankingAIResponse{}, errors.New("AI returned invalid Cypher ranking JSON: missing required field \"ranked_files\"")
	}
	if _, ok := object["search_terms"]; !ok {
		return cypherRankingAIResponse{}, errors.New("AI returned invalid Cypher ranking JSON: missing required field \"search_terms\"")
	}
	var ranked []cypherRankingFileScore
	if err := json.Unmarshal(object["ranked_files"], &ranked); err != nil {
		return cypherRankingAIResponse{}, fmt.Errorf("ranked_files: %w", err)
	}
	var searchTerms []string
	if err := json.Unmarshal(object["search_terms"], &searchTerms); err != nil {
		return cypherRankingAIResponse{}, fmt.Errorf("search_terms: %w", err)
	}
	out := cypherRankingAIResponse{RankedFiles: []cypherRankingFileScore{}, SearchTerms: normalizeCypherSearchTerms(searchTerms)}
	seen := map[string]bool{}
	for _, score := range ranked {
		rel := filepath.ToSlash(strings.TrimSpace(score.Path))
		if rel == "" || seen[rel] {
			continue
		}
		score.InferenceImportance = clampCypherImportanceScore(score.InferenceImportance)
		if score.InferenceImportance <= 0 {
			continue
		}
		score.Path = rel
		seen[rel] = true
		out.RankedFiles = append(out.RankedFiles, score)
	}
	return out, nil
}

func normalizeCypherSearchTerms(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		clean := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if clean == "" {
			continue
		}
		if len([]rune(clean)) > 80 {
			runes := []rune(clean)
			clean = strings.TrimSpace(string(runes[:80]))
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func applyCypherInferenceRanking(manifest *CypherManifest, ranked []cypherRankingFileScore) {
	if manifest == nil || len(ranked) == 0 {
		return
	}
	byPath := map[string]int{}
	for _, score := range ranked {
		rel := filepath.ToSlash(strings.TrimSpace(score.Path))
		if rel == "" {
			continue
		}
		scoreValue := clampCypherImportanceScore(score.InferenceImportance)
		if scoreValue <= 0 {
			continue
		}
		if current := byPath[rel]; scoreValue > current {
			byPath[rel] = scoreValue
		}
	}
	for idx := range manifest.Files {
		rel := filepath.ToSlash(strings.TrimSpace(manifest.Files[idx].Path))
		if score, ok := byPath[rel]; ok && cypherFileIsSendable(manifest.Files[idx]) {
			manifest.Files[idx].Importance.Inference = score
		}
		manifest.Files[idx].Importance = normalizeCypherImportance(manifest.Files[idx].Importance)
	}
}

func applyCypherSearchImportance(projectworkRoot string, manifest *CypherManifest, searchTerms []string) error {
	if manifest == nil {
		return nil
	}
	terms := normalizeCypherSearchTerms(searchTerms)
	if len(terms) == 0 {
		for idx := range manifest.Files {
			manifest.Files[idx].Importance.Search = 0
		}
		return nil
	}
	for idx := range manifest.Files {
		manifest.Files[idx].Importance.Search = 0
		file := manifest.Files[idx]
		if !cypherFileIsSendable(file) {
			continue
		}
		full, err := safeJoin(projectworkRoot, file.Path)
		if err != nil {
			continue
		}
		data, err := readFileUnderRoot(projectworkRoot, full)
		if err != nil || !utf8.Valid(data) {
			continue
		}
		haystack := strings.ToLower(string(data))
		score := 0
		for _, term := range terms {
			needle := strings.ToLower(strings.TrimSpace(term))
			if needle != "" && strings.Contains(haystack, needle) {
				score++
			}
		}
		manifest.Files[idx].Importance.Search = clampCypherImportanceScore(score)
	}
	return nil
}

func cypherImportantFileCount(manifest CypherManifest) int {
	count := 0
	for _, file := range manifest.Files {
		importance := normalizeCypherImportance(file.Importance)
		if cypherFileIsSendable(file) && (importance.Inference > 0 || importance.Search > 0) {
			count++
		}
	}
	return count
}

func cypherImportanceBucketCounts(manifest CypherManifest) ([6]int, [6]int) {
	inferenceCounts := [6]int{}
	searchCounts := [6]int{}
	for _, file := range manifest.Files {
		if !cypherFileIsSendable(file) {
			continue
		}
		importance := normalizeCypherImportance(file.Importance)
		if importance.Inference > 0 {
			inferenceCounts[importance.Inference]++
		}
		if importance.Search > 0 {
			searchCounts[importance.Search]++
		}
	}
	return inferenceCounts, searchCounts
}

func formatCypherImportanceBuckets(counts [6]int) string {
	parts := []string{}
	for score := 5; score >= 1; score-- {
		if counts[score] > 0 {
			parts = append(parts, fmt.Sprintf("%d=%d files", score, counts[score]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func cypherImportanceDistributionMessage(manifest CypherManifest) string {
	inferenceCounts, searchCounts := cypherImportanceBucketCounts(manifest)
	return fmt.Sprintf("inference_importance: %s; search_importance: %s", formatCypherImportanceBuckets(inferenceCounts), formatCypherImportanceBuckets(searchCounts))
}

func cypherPhase2RankingReport(manifest CypherManifest, searchTerms []string) string {
	type rankedFile struct {
		path       string
		importance CypherImportance
	}
	files := []rankedFile{}
	for _, file := range manifest.Files {
		if !cypherFileIsSendable(file) {
			continue
		}
		importance := normalizeCypherImportance(file.Importance)
		if importance.Inference <= 0 && importance.Search <= 0 {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(file.Path))
		if path == "" {
			continue
		}
		files = append(files, rankedFile{path: path, importance: importance})
	}
	sort.Slice(files, func(i, j int) bool {
		return cypherImportanceSortLess(files[i].path, files[i].importance, files[j].path, files[j].importance)
	})
	var b strings.Builder
	cleanTerms := normalizeCypherStringList(searchTerms)
	if len(cleanTerms) == 0 {
		b.WriteString("Search terms: none\n")
	} else {
		b.WriteString("Search terms:\n")
		for _, term := range cleanTerms {
			b.WriteString("- ")
			b.WriteString(term)
			b.WriteString("\n")
		}
	}
	b.WriteString("\nRanked files by combined inference/search importance:\n")
	if len(files) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, file := range files {
			b.WriteString(fmt.Sprintf("- %s — inference=%d / search=%d / combined=%d\n", file.path, file.importance.Inference, file.importance.Search, cypherCombinedImportance(file.importance)))
		}
	}
	distribution := cypherImportanceDistributionMessage(manifest)
	if strings.TrimSpace(distribution) != "" {
		b.WriteString("\nDistribution: ")
		b.WriteString(distribution)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCypherPathList(paths []string, limit int) string {
	paths = normalizeRelativePaths(paths)
	if len(paths) == 0 {
		return "none"
	}
	if limit <= 0 || limit >= len(paths) {
		return strings.Join(paths, ", ")
	}
	shown := append([]string{}, paths[:limit]...)
	shown = append(shown, fmt.Sprintf("... +%d more", len(paths)-limit))
	return strings.Join(shown, ", ")
}

func cypherCombinedImportance(importance CypherImportance) int {
	importance = normalizeCypherImportance(importance)
	return importance.Inference + importance.Search
}

func cypherImportanceSortLess(leftPath string, leftImportance CypherImportance, rightPath string, rightImportance CypherImportance) bool {
	leftImportance = normalizeCypherImportance(leftImportance)
	rightImportance = normalizeCypherImportance(rightImportance)
	leftCombined := cypherCombinedImportance(leftImportance)
	rightCombined := cypherCombinedImportance(rightImportance)
	if leftCombined != rightCombined {
		return leftCombined > rightCombined
	}
	if leftImportance.Inference != rightImportance.Inference {
		return leftImportance.Inference > rightImportance.Inference
	}
	if leftImportance.Search != rightImportance.Search {
		return leftImportance.Search > rightImportance.Search
	}
	return leftPath < rightPath
}

func cypherActionReadCounts(actualFilesRead []string) map[string]int {
	counts := map[string]int{}
	for _, path := range actualFilesRead {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		counts[path]++
	}
	return counts
}

func cypherActionChangeCounts(runLog []cypherActionRunLogEntry) (map[string]int, map[string]int, map[string]int) {
	updated := map[string]int{}
	created := map[string]int{}
	deleted := map[string]int{}
	for _, entry := range normalizeCypherActionRunLog(runLog) {
		for _, path := range entry.Updated {
			updated[path]++
		}
		for _, path := range entry.Created {
			created[path]++
		}
		for _, path := range entry.Deleted {
			deleted[path]++
		}
	}
	return updated, created, deleted
}

func cypherFileImportanceByPath(manifest CypherManifest) map[string]CypherImportance {
	importanceByPath := map[string]CypherImportance{}
	for _, file := range manifest.Files {
		path := filepath.ToSlash(strings.TrimSpace(file.Path))
		if path == "" {
			continue
		}
		importanceByPath[path] = normalizeCypherImportance(file.Importance)
	}
	return importanceByPath
}

func cypherImportanceLabel(importance CypherImportance) string {
	importance = normalizeCypherImportance(importance)
	combined := cypherCombinedImportance(importance)
	switch {
	case combined >= 8 || importance.Inference >= 5 || importance.Search >= 5:
		return "highly important"
	case combined >= 4 || importance.Inference >= 3 || importance.Search >= 3:
		return "important"
	case combined > 0:
		return "lower importance"
	default:
		return "unranked"
	}
}

func pluralizeCount(verb string, count int) string {
	if count == 1 {
		return fmt.Sprintf("%s 1 time", verb)
	}
	return fmt.Sprintf("%s %d times", verb, count)
}

func cypherActionSessionStatusSummary(manifest CypherManifest, runLog []cypherActionRunLogEntry, actualFilesRead []string, currentAttachedPath string, multiFileRequestNote string) string {
	importanceByPath := cypherFileImportanceByPath(manifest)
	readCounts := cypherActionReadCounts(actualFilesRead)
	updatedCounts, createdCounts, deletedCounts := cypherActionChangeCounts(runLog)
	touchedSet := map[string]bool{}
	for path := range readCounts {
		touchedSet[path] = true
	}
	for path := range updatedCounts {
		touchedSet[path] = true
	}
	for path := range createdCounts {
		touchedSet[path] = true
	}
	for path := range deletedCounts {
		touchedSet[path] = true
	}
	touched := make([]string, 0, len(touchedSet))
	for path := range touchedSet {
		touched = append(touched, path)
	}
	sort.Slice(touched, func(i, j int) bool {
		leftPath := touched[i]
		rightPath := touched[j]
		return cypherImportanceSortLess(leftPath, importanceByPath[leftPath], rightPath, importanceByPath[rightPath])
	})

	actualSet := map[string]bool{}
	for path := range readCounts {
		actualSet[path] = true
	}
	type remainingCandidate struct {
		path       string
		importance CypherImportance
	}
	remaining := []remainingCandidate{}
	for _, file := range manifest.Files {
		if !cypherFileIsSendable(file) {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(file.Path))
		if path == "" || actualSet[path] {
			continue
		}
		importance := normalizeCypherImportance(file.Importance)
		if importance.Inference <= 0 && importance.Search <= 0 {
			continue
		}
		remaining = append(remaining, remainingCandidate{path: path, importance: importance})
	}
	sort.Slice(remaining, func(i, j int) bool {
		return cypherImportanceSortLess(remaining[i].path, remaining[i].importance, remaining[j].path, remaining[j].importance)
	})

	var b strings.Builder
	b.WriteString("CYPHER SESSION STATUS SUMMARY:\n")
	currentAttachedPath = filepath.ToSlash(strings.TrimSpace(currentAttachedPath))
	if currentAttachedPath == "" {
		b.WriteString("- Current full attached file: none.\n")
	} else {
		b.WriteString("- Current full attached file: ")
		b.WriteString(currentAttachedPath)
		b.WriteString(".\n")
	}
	if strings.TrimSpace(multiFileRequestNote) != "" {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(multiFileRequestNote))
		b.WriteString("\n")
	}
	if len(touched) == 0 {
		b.WriteString("- No files have been read or updated yet in this Cypher Action run.\n")
	} else {
		b.WriteString("- Files already read/updated this Cypher Action run:\n")
		limit := len(touched)
		if limit > 12 {
			limit = 12
		}
		for _, path := range touched[:limit] {
			importance := importanceByPath[path]
			parts := []string{}
			if count := readCounts[path]; count > 0 {
				parts = append(parts, pluralizeCount("read", count))
			}
			if count := updatedCounts[path]; count > 0 {
				parts = append(parts, pluralizeCount("updated", count))
			}
			if count := createdCounts[path]; count > 0 {
				parts = append(parts, pluralizeCount("created", count))
			}
			if count := deletedCounts[path]; count > 0 {
				parts = append(parts, pluralizeCount("deleted", count))
			}
			if len(parts) == 0 {
				parts = append(parts, "touched")
			}
			b.WriteString("  - ")
			b.WriteString(path)
			b.WriteString(" was ")
			b.WriteString(cypherImportanceLabel(importance))
			b.WriteString("; ")
			b.WriteString(strings.Join(parts, "; "))
			b.WriteString(".\n")
		}
		if len(touched) > limit {
			b.WriteString(fmt.Sprintf("  - ... +%d more touched files.\n", len(touched)-limit))
		}
	}
	if len(remaining) == 0 {
		b.WriteString("- No unread ranked candidates remain.\n")
	} else {
		b.WriteString("- Top unread ranked candidates by combined inference/search importance:\n")
		limit := len(remaining)
		if limit > 10 {
			limit = 10
		}
		for _, candidate := range remaining[:limit] {
			b.WriteString(fmt.Sprintf("  - %s (combined=%d, inference=%d, search=%d)\n", candidate.path, cypherCombinedImportance(candidate.importance), candidate.importance.Inference, candidate.importance.Search))
		}
		if len(remaining) > limit {
			b.WriteString(fmt.Sprintf("  - ... +%d more unread ranked candidates.\n", len(remaining)-limit))
		}
	}
	logs := normalizeCypherActionRunLog(runLog)
	if len(logs) == 0 {
		b.WriteString("- Cypher session update log: empty so far.\n")
	} else {
		b.WriteString("- Cypher session update log is included in the JSON run_log below. Treat it as newer than Cypher summaries when deciding what has already been done.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func cypherUnreadHighRankedCandidates(manifest CypherManifest, actualFilesRead []string, limit int) []string {
	actualSet := map[string]bool{}
	for _, path := range normalizeRelativePaths(actualFilesRead) {
		actualSet[filepath.ToSlash(strings.TrimSpace(path))] = true
	}
	type candidate struct {
		path       string
		importance CypherImportance
	}
	candidates := []candidate{}
	for _, file := range manifest.Files {
		if !cypherFileIsSendable(file) {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(file.Path))
		if path == "" || actualSet[path] {
			continue
		}
		importance := normalizeCypherImportance(file.Importance)
		if importance.Inference < 4 && importance.Search < 4 {
			continue
		}
		candidates = append(candidates, candidate{path: path, importance: importance})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return cypherImportanceSortLess(candidates[i].path, candidates[i].importance, candidates[j].path, candidates[j].importance)
	})
	out := []string{}
	for idx, candidate := range candidates {
		if limit > 0 && idx >= limit {
			out = append(out, fmt.Sprintf("... +%d more", len(candidates)-limit))
			break
		}
		out = append(out, fmt.Sprintf("%s(i=%d,s=%d)", candidate.path, candidate.importance.Inference, candidate.importance.Search))
	}
	return out
}

func cypherActionCompletionSummary(manifest CypherManifest, actualFilesRead []string) string {
	unreadHigh := cypherUnreadHighRankedCandidates(manifest, actualFilesRead, 10)
	unreadSummary := "none"
	if len(unreadHigh) > 0 {
		unreadSummary = strings.Join(unreadHigh, ", ")
	}
	return fmt.Sprintf("phase3_files=%d actual_files_read=%d requested_files=%s unread_high_ranked_candidates=%s", cypherImportantFileCount(manifest), len(normalizeRelativePaths(actualFilesRead)), formatCypherPathList(actualFilesRead, 12), unreadSummary)
}

func (a *App) runCypherPhase2Ranking(ctx context.Context, model ModelConfig, projectName, projectworkRoot, manifestPath, prompt string, manifest CypherManifest, metaRoot string) (CypherManifest, error) {
	resetCypherImportance(&manifest)
	input, err := buildCypherRankingInput(prompt, manifest)
	if err != nil {
		return manifest, err
	}
	a.logf(modelIDString(model.ID), "info", "Cypher Phase 2 ranking started for project %s.", projectName)
	a.publishDiagnostics(cypherDiagnostics(projectName, model, "Phase 2 Ranking Sent").withSystemPrompt(previewForLog(cypherRankingSystemPrompt, 1200)).withPrompt(previewForLog(input, 1200)).withStatusMessage("Ranking files by Cypher metadata and requesting up to 5 deterministic search terms."))
	responseText, err := a.callStructuredTextModel(ctx, model, cypherRankingSystemPrompt, input, true, nil)
	if err != nil {
		return manifest, err
	}
	responsePreview, responseLabel := diagnosticsResponsePreview(responseText, 1200)
	rawRef, rawRel, saveErr := a.saveCypherRawDiagnosticResponse(metaRoot, model, projectName, "phase 2 ranking response", responseText)
	status := fmt.Sprintf("Raw response bytes: %d", len(responseText))
	if saveErr != nil {
		status += fmt.Sprintf(". Failed saving full raw response: %v", saveErr)
	} else if strings.TrimSpace(rawRel) != "" {
		status += fmt.Sprintf(". Full raw response saved to %s", filepath.ToSlash(rawRel))
	}
	diag := cypherDiagnostics(projectName, model, "Phase 2 Ranking Response Received").withResponse(responsePreview).withResponseLabel(responseLabel).withStatusMessage(status)
	if strings.TrimSpace(rawRef.Path) != "" {
		diag.Files = appendUniqueDiagnosticsFile(diag.Files, rawRef)
	}
	a.publishDiagnostics(diag)
	parsed, err := parseCypherRankingAIResponse(responseText)
	if err != nil {
		parseDiag := cypherDiagnostics(projectName, model, "Phase 2 Ranking Parse Failed").withResponse(responsePreview).withResponseLabel(responseLabel).withReason(err.Error()).withStatusMessage("Cypher Phase 2 expected ranked_files and search_terms only. Cypher summaries were not modified.")
		if strings.TrimSpace(rawRef.Path) != "" {
			parseDiag.Files = appendUniqueDiagnosticsFile(parseDiag.Files, rawRef)
		}
		a.publishDiagnostics(parseDiag)
		return manifest, err
	}
	applyCypherInferenceRanking(&manifest, parsed.RankedFiles)
	if err := applyCypherSearchImportance(projectworkRoot, &manifest, parsed.SearchTerms); err != nil {
		return manifest, err
	}
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeCypherManifest(manifestPath, manifest); err != nil {
		return manifest, err
	}
	message := fmt.Sprintf("Phase 2 complete. inference_ranked=%d search_terms=%d phase3_files=%d", len(parsed.RankedFiles), len(parsed.SearchTerms), cypherImportantFileCount(manifest))
	distribution := cypherImportanceDistributionMessage(manifest)
	rankingReport := cypherPhase2RankingReport(manifest, parsed.SearchTerms)
	a.logf(modelIDString(model.ID), "info", "Cypher %s. %s", message, distribution)
	a.publishDiagnostics(cypherDiagnostics(projectName, model, "Phase 2 Ranking Saved").withStatusMessage(message + ". " + distribution).withResponse(rankingReport).withResponseLabel("Ranking details"))
	return manifest, nil
}

func cypherActionContextFromManifest(manifest CypherManifest, runLog []cypherActionRunLogEntry, actualFilesRead []string, currentAttachedPath string) cypherActionContextView {
	actual := normalizeRelativePaths(actualFilesRead)
	actualSet := map[string]bool{}
	for _, path := range actual {
		actualSet[filepath.ToSlash(strings.TrimSpace(path))] = true
	}
	currentAttached := filepath.ToSlash(strings.TrimSpace(currentAttachedPath))
	files := make([]cypherActionFileView, 0, len(manifest.Files))
	remaining := make([]cypherActionCandidateView, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		importance := normalizeCypherImportance(file.Importance)
		if !cypherFileIsSendable(file) || (importance.Inference <= 0 && importance.Search <= 0) {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(file.Path))
		files = append(files, cypherActionFileView{
			Path:       path,
			Importance: importance,
			Summary:    strings.TrimSpace(file.Summary),
			Anchors:    cypherAnchorStrings(file.Anchors),
			Symbols:    normalizeCypherStringList(file.Symbols),
			Continuity: nonNilCypherContinuity(file.Continuity),
		})
		if !actualSet[path] {
			remaining = append(remaining, cypherActionCandidateView{Path: path, Importance: importance})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return cypherImportanceSortLess(files[i].Path, files[i].Importance, files[j].Path, files[j].Importance)
	})
	sort.Slice(remaining, func(i, j int) bool {
		return cypherImportanceSortLess(remaining[i].Path, remaining[i].Importance, remaining[j].Path, remaining[j].Importance)
	})
	return cypherActionContextView{
		AvailableFiles:            files,
		ActualFilesRead:           actual,
		CurrentAttachedFile:       currentAttached,
		RemainingRankedCandidates: remaining,
		RunLog:                    normalizeCypherActionRunLog(runLog),
	}
}

func normalizeCypherActionRunLog(entries []cypherActionRunLogEntry) []cypherActionRunLogEntry {
	if len(entries) == 0 {
		return []cypherActionRunLogEntry{}
	}
	out := make([]cypherActionRunLogEntry, 0, len(entries))
	for _, entry := range entries {
		entry = normalizeCypherActionRunLogEntry(entry)
		if strings.TrimSpace(entry.Summary) == "" && strings.TrimSpace(entry.Reason) == "" && len(entry.Updated)+len(entry.Created)+len(entry.Deleted) == 0 {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func cypherActionRunLogLimitError(entries []cypherActionRunLogEntry) error {
	entries = normalizeCypherActionRunLog(entries)
	if len(entries) > cypherActionMaxRunLogEntries {
		return fmt.Errorf("Cypher Action run paused because the temporary run log reached %d entries. Re-run the prompt to continue; Cypher will enrich changed files first.", cypherActionMaxRunLogEntries)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if len(data) > cypherActionMaxRunLogChars {
		return fmt.Errorf("Cypher Action run paused because the temporary run log reached %d characters. Re-run the prompt to continue; Cypher will enrich changed files first.", cypherActionMaxRunLogChars)
	}
	return nil
}

func buildCypherActionInput(prompt string, manifest CypherManifest, attachedFile *CypherFileEntry, projectworkRoot string, runLog []cypherActionRunLogEntry, actualFilesRead []string, multiFileRequestNote string) (string, error) {
	currentAttachedPath := ""
	if attachedFile != nil {
		currentAttachedPath = filepath.ToSlash(strings.TrimSpace(attachedFile.Path))
	}
	contextJSON, err := json.MarshalIndent(cypherActionContextFromManifest(manifest, runLog, actualFilesRead, currentAttachedPath), "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("CURRENT USER OBJECTIVE:\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\nFILE REQUEST RULE: If you require a file, request your most desired file now. You can request additional files in subsequent turns.\n")
	b.WriteString("\n")
	b.WriteString(cypherActionSessionStatusSummary(manifest, runLog, actualFilesRead, currentAttachedPath, multiFileRequestNote))
	b.WriteString("\n\nAGENTGO CYPHER ACTION CONTEXT:\n")
	b.WriteString("Cypher is AgentGO's project map. This AI-facing Phase 3 slice includes ranked file names, importance scores, summaries, anchors, symbols, continuity notes, system-owned actual read tracking, remaining ranked candidates, and the temporary run log. Files with inference/search importance of 0/0 are omitted so you can focus on the user's task.\n")
	b.WriteString("IMPORTANT: actual_files_read is the only system-owned list of files whose full contents have actually been attached/read during this run. Cypher summaries are metadata only; they do not count as reading a file. Do not claim a file was read unless it appears in actual_files_read.\n```json\n")
	b.Write(contextJSON)
	b.WriteString("\n```\n")
	if attachedFile == nil {
		b.WriteString("\nNO FILE IS CURRENTLY ATTACHED. Request one file from remaining_ranked_candidates if full-file inspection is needed, create a new file if no existing file inspection is needed, or finish. If you create a file but are not finished, also request the next file. Use actual_files_read only for any report of files actually read.\n")
		return b.String(), nil
	}
	path, err := safeJoin(projectworkRoot, attachedFile.Path)
	if err != nil {
		return "", err
	}
	data, err := readFileUnderRoot(projectworkRoot, path)
	if err != nil {
		return "", err
	}
	b.WriteString("\nATTACHED FILE CONTENT:\n")
	b.WriteString("AgentGO attached file as requested: ")
	b.WriteString(filepath.ToSlash(attachedFile.Path))
	b.WriteString("\n")
	b.WriteString("The following file content is system-attached for current_attached_file and is included in actual_files_read. You may make file-specific claims about this file.\n")
	b.WriteString("--- FILE: ")
	b.WriteString(filepath.ToSlash(attachedFile.Path))
	b.WriteString(" ---\n```\n")
	b.Write(data)
	b.WriteString("\n```\n")
	return b.String(), nil
}

func cypherFileIsSendable(file CypherFileEntry) bool {
	return strings.EqualFold(strings.TrimSpace(file.Kind), "text") && file.TransferAllowed && !file.Excluded && !file.NeverSend && file.SizeBytes <= cypherActionMaxRequestedBytes
}

func cypherFileEntryByPath(manifest CypherManifest, rel string) (CypherFileEntry, bool) {
	clean := filepath.ToSlash(strings.TrimSpace(rel))
	for _, file := range manifest.Files {
		if filepath.ToSlash(strings.TrimSpace(file.Path)) == clean {
			return file, true
		}
	}
	return CypherFileEntry{}, false
}

func validateCypherActionPath(projectworkRoot, rel string) (string, string, error) {
	clean := filepath.ToSlash(strings.TrimSpace(rel))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" {
		return "", "", errors.New("path is empty")
	}
	if strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || path.IsAbs(clean) || filepath.IsAbs(clean) {
		return "", "", fmt.Errorf("paths outside projectwork are not allowed: %s", rel)
	}
	target, err := safeJoin(projectworkRoot, clean)
	if err != nil {
		return "", "", err
	}
	return clean, target, nil
}

func validateCypherActionRequestedFile(manifest CypherManifest, projectworkRoot string, requested []string, lastAttached string) (CypherFileEntry, []string, error) {
	normalized := normalizeCypherRequestedFilePaths(requested)
	if len(normalized) == 0 {
		return CypherFileEntry{}, nil, nil
	}
	var firstErr error
	for idx, rel := range normalized {
		clean, target, err := validateCypherActionPath(projectworkRoot, rel)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because %v", rel, err)
			}
			continue
		}
		if strings.TrimSpace(lastAttached) != "" && clean == filepath.ToSlash(strings.TrimSpace(lastAttached)) {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because immediate repeat requests are not allowed", clean)
			}
			continue
		}
		file, ok := cypherFileEntryByPath(manifest, clean)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because it is missing from Cypher", clean)
			}
			continue
		}
		if !cypherFileIsSendable(file) {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because it is binary, excluded, never-send, or over %d bytes", clean, cypherActionMaxRequestedBytes)
			}
			continue
		}
		importance := normalizeCypherImportance(file.Importance)
		if importance.Inference <= 0 && importance.Search <= 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because it was not included in the ranked Phase 3 Cypher slice", clean)
			}
			continue
		}
		if err := rejectSymlinkPath(projectworkRoot, target); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because it is an unsupported symlink path", clean)
			}
			continue
		}
		if info, statErr := os.Stat(target); statErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because it does not exist", clean)
			}
			continue
		} else if info.IsDir() {
			if firstErr == nil {
				firstErr = fmt.Errorf("Cypher Action stopped: AI requested invalid file %q because directories cannot be sent", clean)
			}
			continue
		}
		ignored := make([]string, 0, len(normalized)-1)
		for j, candidate := range normalized {
			if j != idx {
				ignored = append(ignored, candidate)
			}
		}
		return file, ignored, nil
	}
	if firstErr != nil {
		return CypherFileEntry{}, nil, firstErr
	}
	return CypherFileEntry{}, nil, nil
}

func normalizeCypherActionFileOperationTargets(attachedFile *CypherFileEntry, ops []builderFileOp) []builderFileOp {
	normalized := append([]builderFileOp{}, ops...)
	if attachedFile == nil {
		return normalized
	}
	attachedPath := filepath.ToSlash(strings.TrimSpace(attachedFile.Path))
	if attachedPath == "" {
		return normalized
	}
	for idx := range normalized {
		switch strings.ToLower(strings.TrimSpace(normalized[idx].Action)) {
		case "overwrite", "delete":
			normalized[idx].Path = attachedPath
		}
	}
	return normalized
}

func alignCypherRunLogEntryWithOperations(entry cypherActionRunLogEntry, ops []builderFileOp) cypherActionRunLogEntry {
	updated := []string{}
	created := []string{}
	deleted := []string{}
	for _, op := range ops {
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(op.Action)) {
		case "overwrite":
			updated = append(updated, rel)
		case "create":
			created = append(created, rel)
		case "delete":
			deleted = append(deleted, rel)
		}
	}
	if len(updated) > 0 {
		entry.Updated = normalizeRelativePaths(updated)
	}
	if len(created) > 0 {
		entry.Created = normalizeRelativePaths(created)
	}
	if len(deleted) > 0 {
		entry.Deleted = normalizeRelativePaths(deleted)
	}
	return normalizeCypherActionRunLogEntry(entry)
}

func validateCypherActionFileOperations(projectworkRoot string, attachedFile *CypherFileEntry, ops []builderFileOp) error {
	attachedPath := ""
	if attachedFile != nil {
		attachedPath = filepath.ToSlash(strings.TrimSpace(attachedFile.Path))
	}
	for i, op := range ops {
		clean, target, err := validateCypherActionPath(projectworkRoot, op.Path)
		if err != nil {
			return fmt.Errorf("file_operations[%d] invalid path %q: %w", i, op.Path, err)
		}
		if err := rejectSymlinkPath(projectworkRoot, target); err != nil {
			return fmt.Errorf("file_operations[%d] rejected unsupported symlink path %q: %w", i, clean, err)
		}
		info, statErr := os.Stat(target)
		exists := statErr == nil && !info.IsDir()
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("file_operations[%d] could not inspect %q: %w", i, clean, statErr)
		}
		switch strings.ToLower(strings.TrimSpace(op.Action)) {
		case "create":
			if exists {
				return fmt.Errorf("file_operations[%d] cannot create existing file %q; existing files must be inspected and overwritten as the attached file", i, clean)
			}
			if op.Content == "" && strings.TrimSpace(op.ArtifactRef) == "" {
				return fmt.Errorf("file_operations[%d] create %q requires content", i, clean)
			}
		case "overwrite":
			if attachedPath == "" || clean != attachedPath {
				return fmt.Errorf("file_operations[%d] cannot update %q because only the attached file may be updated", i, clean)
			}
			if !exists {
				return fmt.Errorf("file_operations[%d] cannot overwrite missing file %q", i, clean)
			}
			if op.Content == "" && strings.TrimSpace(op.ArtifactRef) == "" {
				return fmt.Errorf("file_operations[%d] overwrite %q requires content", i, clean)
			}
		case "delete":
			if attachedPath == "" || clean != attachedPath {
				return fmt.Errorf("file_operations[%d] cannot delete %q because only the attached file may be deleted", i, clean)
			}
			if !exists {
				return fmt.Errorf("file_operations[%d] cannot delete missing file %q", i, clean)
			}
			if strings.TrimSpace(op.Content) != "" || strings.TrimSpace(op.ArtifactRef) != "" {
				return fmt.Errorf("file_operations[%d] delete %q must not include content or artifact_ref", i, clean)
			}
		default:
			return fmt.Errorf("file_operations[%d].action invalid: got %q; action must be create, overwrite, or delete", i, op.Action)
		}
	}
	return nil
}

func applyCypherActionFileOperations(projectworkRoot string, ops []builderFileOp) (int, error) {
	count := 0
	for _, op := range ops {
		clean, target, err := validateCypherActionPath(projectworkRoot, op.Path)
		if err != nil {
			return count, err
		}
		switch strings.ToLower(strings.TrimSpace(op.Action)) {
		case "create", "overwrite":
			if strings.TrimSpace(op.ArtifactRef) != "" {
				return count, fmt.Errorf("Cypher Action file operation %q uses artifact_ref; Cypher Action currently accepts inline text content only", clean)
			}
			if err := writeFileUnderRoot(projectworkRoot, target, []byte(op.Content), 0o644); err != nil {
				return count, err
			}
		case "delete":
			if err := removeFileUnderRoot(projectworkRoot, target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return count, err
			}
		}
		count++
	}
	if err := removeEmptyDirs(projectworkRoot); err != nil {
		return count, err
	}
	return count, nil
}

func markCypherActionOperationFreshness(manifest *CypherManifest, ops []builderFileOp) {
	if manifest == nil || len(ops) == 0 {
		return
	}
	updated := map[string]bool{}
	created := map[string]bool{}
	for _, op := range ops {
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(op.Action)) {
		case "overwrite":
			updated[rel] = true
		case "create":
			created[rel] = true
		}
	}
	for idx := range manifest.Files {
		rel := filepath.ToSlash(strings.TrimSpace(manifest.Files[idx].Path))
		if updated[rel] {
			manifest.Files[idx].AIReviewed = false
			if strings.TrimSpace(manifest.Files[idx].Summary) != "" {
				manifest.Files[idx].SummaryStatus = "stale"
			} else {
				manifest.Files[idx].SummaryStatus = "empty"
			}
		}
		if created[rel] {
			manifest.Files[idx].AIReviewed = false
			manifest.Files[idx].SummaryStatus = "empty"
		}
	}
}

func cypherActionChangedFilesFromLog(runLog []cypherActionRunLogEntry) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range runLog {
		for _, rel := range append(append(append([]string{}, entry.Updated...), entry.Created...), entry.Deleted...) {
			clean := filepath.ToSlash(strings.TrimSpace(rel))
			if clean == "" || seen[clean] {
				continue
			}
			seen[clean] = true
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out
}

func cypherActionRunLogSummary(entries []cypherActionRunLogEntry) string {
	entries = normalizeCypherActionRunLog(entries)
	if len(entries) == 0 {
		return "Cypher Action has not recorded any work steps yet."
	}
	lines := make([]string, 0, len(entries))
	for i, entry := range entries {
		bits := []string{}
		if len(entry.Updated) > 0 {
			bits = append(bits, "updated="+strings.Join(entry.Updated, ", "))
		}
		if len(entry.Created) > 0 {
			bits = append(bits, "created="+strings.Join(entry.Created, ", "))
		}
		if len(entry.Deleted) > 0 {
			bits = append(bits, "deleted="+strings.Join(entry.Deleted, ", "))
		}
		line := fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(entry.Summary))
		if len(bits) > 0 {
			line += " (" + strings.Join(bits, "; ") + ")"
		}
		if strings.TrimSpace(entry.Reason) != "" {
			line += " Reason: " + strings.TrimSpace(entry.Reason)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func cypherActionBuilderReportStatus(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "blocked", "failed", "error":
		return "blocked"
	case "running", "partial":
		return "partial"
	default:
		return "completed"
	}
}

func (a *App) writeCypherActionBuilderOutput(metaRoot string, model ModelConfig, projectName, statusKind, statusMessage, finalResponse, rawResponse string, runLog []cypherActionRunLogEntry, warnings []string) error {
	runLog = normalizeCypherActionRunLog(runLog)
	changedFiles := cypherActionChangedFilesFromLog(runLog)
	reportStatus := cypherActionBuilderReportStatus(statusKind)
	summary := strings.TrimSpace(statusMessage)
	if summary == "" {
		summary = "Cypher Action update."
	}
	state := builderOutputState{
		ModelID:            modelIDString(model.ID),
		ModelLabel:         model.Label,
		Project:            projectName,
		HasResponse:        true,
		Unread:             true,
		Kind:               "cypher_action",
		StatusLabel:        "Unread Cypher Action report",
		StatusMessage:      summary,
		Timestamp:          time.Now().Format(time.RFC3339),
		RawResponse:        strings.TrimSpace(rawResponse),
		UserFacingResponse: strings.TrimSpace(finalResponse),
		Summary:            summary,
		Warnings:           normalizeCypherStringList(warnings),
		AIContextRisks:     []string{},
		AIContextNext:      []string{},
		ReturnedFiles:      []builderReturnedFile{},
		CypherRunLog:       runLog,
		BuilderReport: builderReport{
			Status:          reportStatus,
			Summary:         strings.TrimSpace(summary + "\n" + cypherActionRunLogSummary(runLog)),
			ChangedFiles:    changedFiles,
			IssuesFound:     []string{},
			Recommendations: []string{},
			NextSteps:       []string{},
		},
	}
	return writeBuilderOutputState(metaRoot, state)
}

func (a *App) refreshCypherManifestForAction(ctx context.Context, projectName, projectSettingsRoot, projectworkRoot string, manifest CypherManifest) (CypherManifest, error) {
	refreshed, err := a.buildCypherManifest(ctx, projectName, projectSettingsRoot, projectworkRoot, manifest, true)
	if err != nil {
		return CypherManifest{}, err
	}
	refreshed.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return refreshed, nil
}

func (a *App) ensureCypherActionManifestReady(ctx context.Context, model ModelConfig, projectName, projectSettingsRoot, projectworkRoot, manifestPath string, manifest CypherManifest) (CypherManifest, error) {
	refreshed, err := a.refreshCypherManifestForAction(ctx, projectName, projectSettingsRoot, projectworkRoot, manifest)
	if err != nil {
		return CypherManifest{}, err
	}
	if err := writeCypherManifest(manifestPath, refreshed); err != nil {
		return CypherManifest{}, err
	}
	if !cypherManifestNeedsEnrichment(refreshed) {
		return refreshed, nil
	}
	a.logf(modelIDString(model.ID), "info", "Cypher Action requires Phase 1 enrichment before work phase for project %s.", projectName)
	a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Enrichment Required").withStatusMessage("Cypher Action is running Phase 1 enrichment for stale, empty, or unreviewed files before the work phase."))
	enriched, complete, err := a.runCypherEnrichment(ctx, model, projectName, refreshed, projectworkRoot)
	if err != nil {
		return CypherManifest{}, fmt.Errorf("Cypher Action blocked: enrichment failed: %w", err)
	}
	if !complete || cypherManifestNeedsEnrichment(enriched) {
		return CypherManifest{}, errors.New("Cypher Action blocked: enrichment did not complete for every eligible file. Fix enrichment or rerun Cypher before executing with Cypher.")
	}
	enriched.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeCypherManifest(manifestPath, enriched); err != nil {
		return CypherManifest{}, err
	}
	return enriched, nil
}

func (a *App) runCypherExecution(projectName, executionID string, model ModelConfig, prompt string, contextFiles []string, temporaryAttachments []temporaryAttachmentInput) modelRunResult {
	result := modelRunResult{ModelID: modelIDString(model.ID), ModelLabel: model.Label}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(modelIDString(model.ID), projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(modelIDString(model.ID), executionID)
		a.mu.Unlock()
	}()
	defer cancel()

	projectSettingsRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		result.Err = err
		return result
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		result.Err = err
		return result
	}
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		result.Err = err
		return result
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		result.Err = err
		return result
	}
	manifestPath := filepath.Join(projectSettingsRoot, cypherManifestFileName)
	manifest, exists, err := readCypherManifest(manifestPath)
	if err != nil {
		result.Err = fmt.Errorf("read Cypher: %w", err)
		return result
	}
	if !exists {
		manifest = CypherManifest{}
	}
	summaryModel := model
	if selectedSummary, ok := a.activeBuilderModelByID(manifest.LastBuilderSelection.SummaryBuilderID); ok {
		summaryModel = selectedSummary
	}
	manifest, err = a.ensureCypherActionManifestReady(ctx, summaryModel, projectName, projectSettingsRoot, projectworkRoot, manifestPath, manifest)
	if err != nil {
		result.Err = err
		a.logf(modelIDString(model.ID), "error", "%v", err)
		a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Blocked").withReason(err.Error()).withStatusMessage("Cypher Action did not start because Phase 1 enrichment is incomplete."))
		_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", err.Error(), "", "", nil, []string{err.Error()})
		return result
	}
	manifest, err = a.runCypherPhase2Ranking(ctx, model, projectName, projectworkRoot, manifestPath, prompt, manifest, metaRoot)
	if err != nil {
		result.Err = fmt.Errorf("Cypher Phase 2 ranking failed: %w", err)
		a.logf(modelIDString(model.ID), "error", "%v", result.Err)
		a.publishDiagnostics(cypherDiagnostics(projectName, model, "Phase 2 Ranking Failed").withReason(result.Err.Error()).withStatusMessage("Cypher Action did not start because file ranking failed."))
		_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", result.Err.Error(), "", "", nil, []string{result.Err.Error()})
		return result
	}

	extraMessages := []adapters.Message{}
	ignoredContextFiles := len(contextFiles)
	ignoredTemporaryAttachments := len(temporaryAttachments)
	contextFiles = nil
	temporaryAttachments = nil

	a.logf(modelIDString(model.ID), "info", "Cypher Action started for project %s. ignored_context_files=%d ignored_temporary_attachments=%d", projectName, ignoredContextFiles, ignoredTemporaryAttachments)
	a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Started").withPrompt(previewForLog(prompt, 1200)).withStatusMessage(fmt.Sprintf("Cypher Action started. Available files=%d", len(cypherActionContextFromManifest(manifest, nil, nil, "").AvailableFiles))))

	runLog := []cypherActionRunLogEntry{}
	attachedFile := (*CypherFileEntry)(nil)
	actualFilesRead := []string{}
	lastAttachedPath := ""
	totalApplied := 0
	warnings := []string{}
	multiFileRequestNote := ""

	for round := 0; round < cypherMaxEnrichmentRounds; round++ {
		select {
		case <-ctx.Done():
			result.Err = errors.New("request canceled")
			return result
		default:
		}
		input, err := buildCypherActionInput(prompt, manifest, attachedFile, projectworkRoot, runLog, actualFilesRead, multiFileRequestNote)
		if err != nil {
			result.Err = err
			return result
		}
		multiFileRequestNote = ""
		stage := fmt.Sprintf("Action Round %d Sent", round+1)
		if attachedFile == nil {
			stage = fmt.Sprintf("Action Round %d Planning Sent", round+1)
		}
		status := fmt.Sprintf("No file attached; Builder may request one file, create a new file, or finish. actual_files_read=%d", len(actualFilesRead))
		if attachedFile != nil {
			status = fmt.Sprintf("Attached file: %s; actual_files_read=%d", attachedFile.Path, len(actualFilesRead))
		}
		diag := cypherDiagnostics(projectName, model, stage).withSystemPrompt(previewForLog(cypherActionSystemPrompt, 1200)).withPrompt(previewForLog(input, 1200)).withStatusMessage(status)
		if attachedFile != nil {
			diag.Files = a.projectContextDiagnosticsFiles(projectName, []string{attachedFile.Path})
		}
		a.publishDiagnostics(diag)

		responseText, err := a.callStructuredTextModelWithMessages(ctx, model, cypherActionSystemPrompt, input, extraMessages, true, nil)
		if err != nil {
			result.Err = err
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Failed", round+1)).withReason(err.Error()))
			return result
		}
		responsePreview, responseLabel := diagnosticsResponsePreview(responseText, 1200)
		rawRef, rawRel, saveErr := a.saveCypherRawDiagnosticResponse(metaRoot, model, projectName, fmt.Sprintf("action round %d response", round+1), responseText)
		receivedStatus := fmt.Sprintf("Raw response bytes: %d", len(responseText))
		if saveErr != nil {
			receivedStatus += fmt.Sprintf(". Failed saving full raw response: %v", saveErr)
		} else if strings.TrimSpace(rawRel) != "" {
			receivedStatus += fmt.Sprintf(". Full raw response saved to %s", filepath.ToSlash(rawRel))
		}
		receivedDiag := cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Response Received", round+1)).withResponse(responsePreview).withResponseLabel(responseLabel).withStatusMessage(receivedStatus)
		if strings.TrimSpace(rawRef.Path) != "" {
			receivedDiag.Files = appendUniqueDiagnosticsFile(receivedDiag.Files, rawRef)
		}
		a.publishDiagnostics(receivedDiag)

		parsed, err := parseCypherActionAIResponse(responseText)
		if err != nil {
			result.Err = err
			parseDiag := cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Parse Failed", round+1)).withResponse(responsePreview).withResponseLabel(responseLabel).withReason(err.Error()).withStatusMessage("Cypher Action received a response, but AgentGO could not parse the strict action schema.")
			if strings.TrimSpace(rawRef.Path) != "" {
				parseDiag.Files = appendUniqueDiagnosticsFile(parseDiag.Files, rawRef)
			}
			a.publishDiagnostics(parseDiag)
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", err.Error(), "", responseText, runLog, append(warnings, err.Error()))
			return result
		}

		parsed.FileOperations = normalizeCypherActionFileOperationTargets(attachedFile, parsed.FileOperations)
		parsed.RunLogEntry = alignCypherRunLogEntryWithOperations(parsed.RunLogEntry, parsed.FileOperations)

		if err := validateCypherActionFileOperations(projectworkRoot, attachedFile, parsed.FileOperations); err != nil {
			result.Err = err
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Operation Validation Failed", round+1)).withReason(err.Error()).withStatusMessage("Cypher Action rejected unsafe or out-of-scope file operations."))
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", err.Error(), "", responseText, runLog, append(warnings, err.Error()))
			return result
		}
		applied, err := applyCypherActionFileOperations(projectworkRoot, parsed.FileOperations)
		if err != nil {
			result.Err = fmt.Errorf("failed applying Cypher Action file operations: %w", err)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Apply Failed", round+1)).withReason(result.Err.Error()).withStatusMessage(fmt.Sprintf("Applied %d of %d file operation(s) before failure.", applied, len(parsed.FileOperations))))
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", result.Err.Error(), "", responseText, runLog, append(warnings, result.Err.Error()))
			return result
		}
		totalApplied += applied
		runLog = append(runLog, parsed.RunLogEntry)
		pauseErr := cypherActionRunLogLimitError(runLog)
		if pauseErr != nil {
			warnings = append(warnings, pauseErr.Error())
		}

		if len(parsed.FileOperations) > 0 {
			refreshed, err := a.refreshCypherManifestForAction(ctx, projectName, projectSettingsRoot, projectworkRoot, manifest)
			if err != nil {
				result.Err = fmt.Errorf("failed refreshing Cypher after action operations: %w", err)
				return result
			}
			markCypherActionOperationFreshness(&refreshed, parsed.FileOperations)
			manifest = refreshed
			if err := writeCypherManifest(manifestPath, manifest); err != nil {
				result.Err = err
				return result
			}
		}

		runStateJSON, _ := json.MarshalIndent(map[string]any{"round": round + 1, "response": parsed, "run_log": runLog, "actual_files_read": actualFilesRead}, "", "  ")
		roundStatus := fmt.Sprintf("Cypher Action round %d complete. Applied %d file operation(s). actual_files_read=%d", round+1, applied, len(actualFilesRead))
		_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "partial", roundStatus, "", string(runStateJSON), runLog, warnings)
		liveDiag := cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Log Updated", round+1)).withStatusMessage(roundStatus).withResponse(cypherActionRunLogSummary(runLog)).withResponseLabel("Temporary Cypher Action Run Log")
		if len(parsed.FileOperations) > 0 {
			liveDiag.Files = append(liveDiag.Files, a.projectContextDiagnosticsFiles(projectName, builderFileOpPaths(parsed.FileOperations))...)
		}
		a.publishDiagnostics(liveDiag)

		if pauseErr != nil {
			result.Valid = true
			result.AppliedOperations = totalApplied
			result.PendingCount = 0
			message := pauseErr.Error()
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "partial", message, strings.TrimSpace(parsed.FinalResponse), string(runStateJSON), runLog, warnings)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Paused").withReason(message).withStatusMessage("Cypher Action paused after applying valid current operations."))
			return result
		}

		nextFile, ignoredRequests, err := validateCypherActionRequestedFile(manifest, projectworkRoot, parsed.RequestedFiles, lastAttachedPath)
		if len(ignoredRequests) > 0 {
			warning := fmt.Sprintf("AI requested multiple files; AgentGO will send only the first valid requested file and defer extras: %s", strings.Join(ignoredRequests, ", "))
			warnings = append(warnings, warning)
			a.logf(modelIDString(model.ID), "warn", "%s", warning)
		}
		if err != nil {
			result.Err = err
			a.logf(modelIDString(model.ID), "warn", "%v", err)
			a.logf("warning", "toast", "%s", err.Error())
			a.publishDiagnostics(cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Invalid Request Stopped", round+1)).withReason(err.Error()).withStatusMessage("Cypher Action stopped because the AI requested an invalid file."))
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", err.Error(), strings.TrimSpace(parsed.FinalResponse), string(runStateJSON), runLog, append(warnings, err.Error()))
			return result
		}
		if strings.TrimSpace(nextFile.Path) == "" {
			finalResponse := strings.TrimSpace(parsed.FinalResponse)
			completeStatus := "Cypher Action completed."
			if finalResponse == "" {
				completeStatus = "Cypher Action completed without a final response."
				warnings = append(warnings, completeStatus)
			}
			result.Valid = true
			result.AppliedOperations = totalApplied
			result.PendingCount = 0
			completionSummary := cypherActionCompletionSummary(manifest, actualFilesRead)
			_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "completed", completeStatus, finalResponse, string(runStateJSON), runLog, warnings)
			a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Complete").withStatusMessage(completeStatus + " " + completionSummary).withResponse(cypherActionRunLogSummary(runLog) + "\n\n" + completionSummary).withResponseLabel("Action completion diagnostics"))
			a.logf(modelIDString(model.ID), "info", "Cypher Action complete for project %s. applied=%d pending=0. %s", projectName, totalApplied, completionSummary)
			return result
		}
		attachedFile = &nextFile
		lastAttachedPath = filepath.ToSlash(strings.TrimSpace(nextFile.Path))
		if len(ignoredRequests) > 0 {
			multiFileRequestNote = fmt.Sprintf("AgentGO can only send one file at a time. The previous response requested multiple files, so AgentGO sent the first valid requested file: %s. Other requested files not sent yet: %s. After reviewing the current file, request one additional file if required.", lastAttachedPath, strings.Join(ignoredRequests, ", "))
		}
		actualFilesRead = normalizeRelativePaths(append(actualFilesRead, lastAttachedPath))
		a.logf(modelIDString(model.ID), "info", "Cypher Action requested next file for project %s: %s (actual_files_read=%d)", projectName, nextFile.Path, len(actualFilesRead))
		nextDiag := cypherDiagnostics(projectName, model, fmt.Sprintf("Action Round %d Next File Requested", round+1)).withStatusMessage(fmt.Sprintf("Next file: %s; actual_files_read will include this file on the next round.", nextFile.Path))
		nextDiag.Files = a.projectContextDiagnosticsFiles(projectName, []string{nextFile.Path})
		a.publishDiagnostics(nextDiag)
	}
	result.Err = fmt.Errorf("Cypher Action exceeded %d rounds", cypherMaxEnrichmentRounds)
	_ = a.writeCypherActionBuilderOutput(metaRoot, model, projectName, "blocked", result.Err.Error(), "", "", runLog, append(warnings, result.Err.Error()))
	return result
}

func isDirEmpty(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), cypherManifestFileName) {
			continue
		}
		return false, nil
	}
	return true, nil
}

func enforceCypherManifestIdentity(manifest *CypherManifest) {
	if manifest == nil {
		return
	}
	manifest.AgentGOTool = agentGOToolCypher
	manifest.ToolVersion = agentGOToolVersion
}

func enforceCypherManifestSaveIdentity(manifest *CypherManifest) {
	enforceCypherManifestIdentity(manifest)
	if manifest == nil {
		return
	}
	manifest.CypherVersion = agentGOToolVersion
}

func readCypherManifest(path string) (CypherManifest, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CypherManifest{}, false, nil
		}
		return CypherManifest{}, false, err
	}
	var manifest CypherManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CypherManifest{}, true, err
	}
	enforceCypherManifestIdentity(&manifest)
	return manifest, true, nil
}

func writeCypherManifest(path string, manifest CypherManifest) error {
	enforceCypherManifestSaveIdentity(&manifest)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0o644)
}

func (a *App) buildCypherManifest(ctx context.Context, projectName, projectRoot, projectworkRoot string, previous CypherManifest, previousExists bool) (CypherManifest, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	createdAt := now
	if previousExists && strings.TrimSpace(previous.CreatedAt) != "" {
		createdAt = strings.TrimSpace(previous.CreatedAt)
	}
	projectRootDisplay := filepath.ToSlash(projectRoot)
	if rel, err := filepath.Rel(a.cfg.WorkRoot, projectRoot); err == nil {
		projectRootDisplay = "/work/" + filepath.ToSlash(rel)
	}
	manifest := CypherManifest{
		AgentGOTool:          agentGOToolCypher,
		ToolVersion:          agentGOToolVersion,
		CypherVersion:        agentGOToolVersion,
		Project:              projectName,
		ProjectRoot:          projectRootDisplay,
		ContentDomain:        cypherDefaultContentDomain,
		CreatedBy:            "AgentGO",
		GeneratorVersion:     a.release.Label(),
		CreatedAt:            createdAt,
		UpdatedAt:            now,
		TextEncoding:         "UTF-8",
		PositionEncoding:     cypherPositionEncoding,
		Summary:              strings.TrimSpace(previous.Summary),
		Instructions:         cypherDefaultInstructions,
		LastBuilderSelection: previous.LastBuilderSelection,
		DirectoryStructure:   []string{},
		Files:                []CypherFileEntry{},
		ExternalSymbols:      nonNilCypherAnchors(previous.ExternalSymbols),
		Git:                  a.readCypherGitInfo(projectRoot),
	}
	previousByPath := map[string]CypherFileEntry{}
	if previousExists {
		for _, file := range previous.Files {
			previousByPath[filepath.ToSlash(strings.TrimSpace(file.Path))] = file
		}
	}
	err := filepath.WalkDir(projectworkRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if isSymlinkDirEntry(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipCypherDir(name) && path != projectworkRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(name, cypherManifestFileName) || shouldSkipWorkspaceFile(name) {
			return nil
		}
		rel, err := filepath.Rel(projectworkRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		entry, err := buildCypherFileEntry(path, rel)
		if err != nil {
			return err
		}
		if previousEntry, ok := previousByPath[rel]; ok {
			entry = carryForwardCypherAIFields(entry, previousEntry)
			entry = preserveCypherUserExclusion(entry, previousEntry)
		}
		manifest.DirectoryStructure = append(manifest.DirectoryStructure, rel)
		manifest.Files = append(manifest.Files, entry)
		manifest.FileCount++
		manifest.TokenEstimate += entry.TokenEstimate
		if entry.TransferAllowed && !entry.Excluded && !entry.NeverSend {
			manifest.TransferableFileCount++
		}
		return nil
	})
	if err != nil {
		return CypherManifest{}, err
	}
	sort.Strings(manifest.DirectoryStructure)
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if manifest.Summary == "" {
		manifest.Summary = fmt.Sprintf("Cypher manifest for %s. Files indexed: %d. Transferable text files: %d.", projectName, manifest.FileCount, manifest.TransferableFileCount)
	}
	return manifest, nil
}

func shouldSkipCypherDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "tmp", "temp", "dist", "build", ".cache", ".agentgo":
		return true
	default:
		return false
	}
}

func buildCypherFileEntry(path, rel string) (CypherFileEntry, error) {
	info, err := os.Lstat(path)
	if err == nil && isSymlinkMode(info.Mode()) {
		return CypherFileEntry{}, fmt.Errorf("unsupported symlink path: %s", rel)
	}
	if err != nil {
		return CypherFileEntry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CypherFileEntry{}, err
	}
	contentType := detectContentType(rel, data)
	isText := isLikelyText(rel, data, contentType)
	kind := "binary"
	transferAllowed := false
	if isText {
		kind = "text"
		transferAllowed = info.Size() <= cypherActionMaxRequestedBytes
	}
	hash := sha256.Sum256(data)
	entry := CypherFileEntry{
		Path:            filepath.ToSlash(rel),
		Language:        cypherLanguageForPath(rel),
		ContentKind:     cypherContentKindForPath(rel, isText),
		Kind:            kind,
		TransferAllowed: transferAllowed,
		Excluded:        false,
		ExcludeReason:   "",
		NeverSend:       false,
		SizeBytes:       info.Size(),
		Hash:            cypherHashPrefix + fmt.Sprintf("%x", hash[:]),
		LastModified:    info.ModTime().UTC().Format(time.RFC3339),
		TokenEstimate:   estimateCypherTokens(info.Size(), isText),
		Summary:         "",
		SummaryStatus:   "empty",
		AIReviewed:      false,
		Importance:      CypherImportance{},
		Anchors:         []string{},
		Symbols:         []string{},
		Continuity:      emptyCypherContinuity(),
		Dependencies:    []string{},
		ReferencedBy:    []string{},
	}
	if !isText {
		entry.Summary = "Binary file. Not transferred as text."
		entry.SummaryStatus = "skipped"
		entry.AIReviewed = false
	} else if info.Size() > cypherActionMaxRequestedBytes {
		entry.Summary = fmt.Sprintf("Text file over %d bytes. Not transferred to AI.", cypherActionMaxRequestedBytes)
		entry.SummaryStatus = "skipped"
		entry.AIReviewed = false
	}
	return entry, nil
}

func carryForwardCypherAIFields(next, previous CypherFileEntry) CypherFileEntry {
	if !next.TransferAllowed {
		next.Importance = previous.Importance
		return next
	}
	next.Summary = strings.TrimSpace(previous.Summary)
	next.SummaryStatus = strings.TrimSpace(previous.SummaryStatus)
	if next.SummaryStatus == "" {
		next.SummaryStatus = "empty"
	}
	next.AIReviewed = previous.AIReviewed
	if !next.AIReviewed && strings.TrimSpace(next.Summary) != "" && cypherStatusIndicatesReviewed(next.SummaryStatus) {
		next.AIReviewed = true
	}
	if previous.Hash != "" && previous.Hash != next.Hash {
		next.AIReviewed = false
		if strings.TrimSpace(next.Summary) != "" || previous.AIReviewed {
			next.SummaryStatus = "stale"
		}
	}
	if cypherSummaryLooksWeak(next) || cypherStatusNeedsReview(next.SummaryStatus) {
		next.AIReviewed = false
	}
	next.Importance = previous.Importance
	next.Anchors = normalizeCypherStringList(previous.Anchors)
	next.Symbols = normalizeCypherStringList(previous.Symbols)
	next.Continuity = nonNilCypherContinuity(previous.Continuity)
	next.Dependencies = normalizeRelativePaths(previous.Dependencies)
	next.ReferencedBy = normalizeRelativePaths(previous.ReferencedBy)
	return next
}

func preserveCypherUserExclusion(next, previous CypherFileEntry) CypherFileEntry {
	if previous.NeverSend || previous.Excluded || strings.EqualFold(strings.TrimSpace(previous.ExclusionSource), "user") {
		next.NeverSend = true
		next.Excluded = true
		next.TransferAllowed = false
		next.ExclusionSource = "user"
		next.ExcludeReason = strings.TrimSpace(previous.ExcludeReason)
		if next.ExcludeReason == "" {
			next.ExcludeReason = "User denied AI access."
		}
		next.Summary = "User-excluded file. Never send to AI."
		next.SummaryStatus = "skipped"
		next.AIReviewed = false
	}
	return next
}

func estimateCypherTokens(size int64, isText bool) int {
	if !isText || size <= 0 {
		return 0
	}
	return int((size + 3) / 4)
}

func cypherLanguageForPath(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".py":
		return "python"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".svg":
		return "svg"
	case ".sql":
		return "sql"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".txt":
		return "text"
	default:
		return ""
	}
}

func cypherContentKindForPath(rel string, isText bool) string {
	if !isText {
		if strings.HasPrefix(detectContentType(rel, nil), "image/") {
			return "image_asset"
		}
		return "binary_asset"
	}
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".py", ".java", ".c", ".cpp", ".h", ".hpp", ".rs", ".rb", ".php", ".swift", ".kt":
		return "source_code"
	case ".md", ".markdown", ".txt":
		return "document"
	case ".json", ".yaml", ".yml", ".toml", ".xml":
		return "config_or_data"
	case ".html", ".css", ".svg":
		return "web_asset"
	default:
		return "text"
	}
}

func emptyCypherContinuity() CypherContinuity {
	return CypherContinuity{
		Characters:     []string{},
		Relationships:  []string{},
		TimelineEvents: []string{},
		Locations:      []string{},
		Rules:          []string{},
		Contradictions: []string{},
	}
}

func nonNilCypherAnchors(values []CypherAnchor) []CypherAnchor {
	if values == nil {
		return []CypherAnchor{}
	}
	return values
}

func nonNilCypherContinuity(value CypherContinuity) CypherContinuity {
	if value.Characters == nil {
		value.Characters = []string{}
	}
	if value.Relationships == nil {
		value.Relationships = []string{}
	}
	if value.TimelineEvents == nil {
		value.TimelineEvents = []string{}
	}
	if value.Locations == nil {
		value.Locations = []string{}
	}
	if value.Rules == nil {
		value.Rules = []string{}
	}
	if value.Contradictions == nil {
		value.Contradictions = []string{}
	}
	return value
}

func cypherFileListsEqual(a, b []CypherFileEntry) bool {
	pathsA := make([]string, 0, len(a))
	pathsB := make([]string, 0, len(b))
	for _, file := range a {
		pathsA = append(pathsA, filepath.ToSlash(strings.TrimSpace(file.Path)))
	}
	for _, file := range b {
		pathsB = append(pathsB, filepath.ToSlash(strings.TrimSpace(file.Path)))
	}
	sort.Strings(pathsA)
	sort.Strings(pathsB)
	if len(pathsA) != len(pathsB) {
		return false
	}
	for i := range pathsA {
		if pathsA[i] != pathsB[i] {
			return false
		}
	}
	return true
}

func (a *App) readCypherGitInfo(projectRoot string) CypherGitInfo {
	info := CypherGitInfo{RecentLogs: []CypherGitLogEntry{}}
	branchBytes, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err == nil {
		info.Branch = strings.TrimSpace(string(branchBytes))
	}
	commitBytes, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--short", "HEAD").Output()
	if err == nil {
		info.LastCommit = strings.TrimSpace(string(commitBytes))
	}
	return info
}

func validateCypherProtectedFields(original, updated CypherManifest) error {
	enforceCypherManifestIdentity(&original)
	enforceCypherManifestIdentity(&updated)
	if original.AgentGOTool != updated.AgentGOTool {
		return errors.New("Cypher update rejected: agentgo_tool is protected")
	}
	if original.ToolVersion != updated.ToolVersion {
		return errors.New("Cypher update rejected: tool_version is protected")
	}
	if original.CypherVersion != updated.CypherVersion {
		return errors.New("Cypher update rejected: cypher_version is protected")
	}
	if original.Project != updated.Project {
		return errors.New("Cypher update rejected: project is protected")
	}
	if original.ProjectRoot != updated.ProjectRoot {
		return errors.New("Cypher update rejected: project_root is protected")
	}
	if original.CreatedBy != updated.CreatedBy {
		return errors.New("Cypher update rejected: created_by is protected")
	}
	if original.GeneratorVersion != updated.GeneratorVersion {
		return errors.New("Cypher update rejected: generator_version is protected")
	}
	if original.CreatedAt != updated.CreatedAt {
		return errors.New("Cypher update rejected: created_at is protected")
	}
	if original.TextEncoding != updated.TextEncoding || original.PositionEncoding != updated.PositionEncoding {
		return errors.New("Cypher update rejected: encoding fields are protected")
	}
	if original.FileCount != updated.FileCount || original.TransferableFileCount != updated.TransferableFileCount || original.TokenEstimate != updated.TokenEstimate {
		return errors.New("Cypher update rejected: aggregate file counts are protected")
	}
	if !stringSlicesEqual(normalizeRelativePaths(original.DirectoryStructure), normalizeRelativePaths(updated.DirectoryStructure)) {
		return errors.New("Cypher update rejected: directory_structure is protected")
	}
	if len(original.Files) != len(updated.Files) {
		return errors.New("Cypher update rejected: file list is protected")
	}
	updatedByPath := map[string]CypherFileEntry{}
	for _, file := range updated.Files {
		updatedByPath[filepath.ToSlash(strings.TrimSpace(file.Path))] = file
	}
	for _, originalFile := range original.Files {
		updatedFile, ok := updatedByPath[filepath.ToSlash(strings.TrimSpace(originalFile.Path))]
		if !ok {
			return fmt.Errorf("Cypher update rejected: missing protected file %s", originalFile.Path)
		}
		if originalFile.Kind != updatedFile.Kind || originalFile.SizeBytes != updatedFile.SizeBytes || originalFile.Hash != updatedFile.Hash || originalFile.TransferAllowed != updatedFile.TransferAllowed || originalFile.Excluded != updatedFile.Excluded || originalFile.ExclusionSource != updatedFile.ExclusionSource || originalFile.ExcludeReason != updatedFile.ExcludeReason || originalFile.NeverSend != updatedFile.NeverSend {
			return fmt.Errorf("Cypher update rejected: protected metadata changed for %s", originalFile.Path)
		}
	}
	return nil
}

func (a *App) handleContextFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName := a.activeProject()
	if projectName == "" {
		writeJSON(w, http.StatusOK, contextFilesResponse{Root: "projectwork", Files: []contextFileEntry{}, LastMergedFiles: []string{}})
		return
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files, err := collectContextFileEntries(projectworkRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, contextFilesResponse{Root: "projectwork", Files: files, LastMergedFiles: a.currentLastMergedFiles(projectName)})
}

func buildDiffFilesForRoots(model ModelConfig, projectName, src, dst string) ([]diffFile, error) {
	srcFiles, err := collectWorkspaceFiles(src)
	if err != nil {
		return nil, err
	}
	dstFiles, err := collectWorkspaceFiles(dst)
	if err != nil {
		return nil, err
	}
	all := map[string]bool{}
	for rel := range srcFiles {
		all[rel] = true
	}
	for rel := range dstFiles {
		all[rel] = true
	}
	paths := make([]string, 0, len(all))
	for rel := range all {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	out := make([]diffFile, 0, len(paths))
	for _, rel := range paths {
		srcData, srcOK := srcFiles[rel]
		dstData, dstOK := dstFiles[rel]
		if srcOK && dstOK && bytes.Equal(srcData, dstData) {
			continue
		}
		status := "modified"
		targetLabel := filepath.ToSlash(filepath.Join("projectwork", rel))
		sourceLabel := filepath.ToSlash(filepath.Join(model.WorkDir, projectName, "project", rel))
		switch {
		case srcOK && !dstOK:
			status = "added"
			targetLabel = "(new file)"
		case !srcOK && dstOK:
			status = "deleted"
			sourceLabel = "(deleted in candidate)"
		}
		out = append(out, diffFile{
			Path:      rel,
			Status:    status,
			Source:    sourceLabel,
			Target:    targetLabel,
			DiffText:  buildUnifiedDiff(rel, dstData, srcData),
			Selected:  status != "deleted",
			ByteDelta: len(srcData) - len(dstData),
		})
	}
	return out, nil
}

func (a *App) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
	if modelID == "" {
		http.Error(w, "modelId is required", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	files, err := a.buildModelDiff(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, diffResponse{ModelID: modelID, Files: files})
}

func (a *App) buildModelDiff(model ModelConfig) ([]diffFile, error) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		return nil, err
	}
	src, _, err := a.projectPaths(model, projectName)
	if err != nil {
		return nil, err
	}
	dst, err := a.projectWorkRoot(projectName)
	if err != nil {
		return nil, err
	}
	return buildDiffFilesForRoots(model, projectName, src, dst)
}

func (a *App) attachReviewerCandidateReturnedFiles(projectName string, state *reviewerOutputState) {
	if state == nil || len(state.Candidates) == 0 {
		return
	}
	for idx := range state.Candidates {
		candidate := &state.Candidates[idx]
		candidate.ReturnedFiles = nil
		modelID := strings.TrimSpace(candidate.ModelID)
		if modelID == "" {
			continue
		}
		model, ok := a.findModel(modelID)
		if !ok {
			continue
		}
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			continue
		}
		builderState, err := readBuilderOutputState(metaRoot)
		if err != nil || !builderState.HasResponse || len(builderState.ReturnedFiles) == 0 {
			continue
		}
		candidate.ReturnedFiles = append([]builderReturnedFile(nil), builderState.ReturnedFiles...)
	}
}

func (a *App) handleDiffPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if modelID == "" || path == "" {
		http.Error(w, "modelId and path are required", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	preview, err := a.buildDiffPreview(model, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *App) handleObserverComparePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	leftModelID := strings.TrimSpace(r.URL.Query().Get("leftModelId"))
	leftPath := strings.TrimSpace(r.URL.Query().Get("leftPath"))
	rightModelID := strings.TrimSpace(r.URL.Query().Get("rightModelId"))
	rightPath := strings.TrimSpace(r.URL.Query().Get("rightPath"))
	if leftModelID == "" || leftPath == "" || rightModelID == "" || rightPath == "" {
		http.Error(w, "leftModelId, leftPath, rightModelId, and rightPath are required", http.StatusBadRequest)
		return
	}
	if leftModelID == rightModelID {
		http.Error(w, "choose files from two different builders", http.StatusBadRequest)
		return
	}
	leftModel, ok := a.findModel(leftModelID)
	if !ok {
		http.Error(w, "unknown left builder", http.StatusNotFound)
		return
	}
	rightModel, ok := a.findModel(rightModelID)
	if !ok {
		http.Error(w, "unknown right builder", http.StatusNotFound)
		return
	}
	preview, err := a.buildBuilderComparePreview(leftModel, leftPath, rightModel, rightPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *App) buildBuilderComparePreview(leftModel ModelConfig, leftRel string, rightModel ModelConfig, rightRel string) (builderComparePreviewResponse, error) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftRoot, _, err := a.projectPaths(leftModel, projectName)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	rightRoot, _, err := a.projectPaths(rightModel, projectName)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	projectWorkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(leftRel)))
	rightRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rightRel)))
	if leftRel == "." || leftRel == "" || strings.HasPrefix(leftRel, "../") {
		return builderComparePreviewResponse{}, errors.New("invalid left path")
	}
	if rightRel == "." || rightRel == "" || strings.HasPrefix(rightRel, "../") {
		return builderComparePreviewResponse{}, errors.New("invalid right path")
	}
	leftFile, err := safeJoin(leftRoot, leftRel)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	rightFile, err := safeJoin(rightRoot, rightRel)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftBaseFile, err := safeJoin(projectWorkRoot, leftRel)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	rightBaseFile, err := safeJoin(projectWorkRoot, rightRel)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftData, leftOK, err := readOptionalFile(leftRoot, leftFile)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	rightData, rightOK, err := readOptionalFile(rightRoot, rightFile)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftBaseData, leftBaseOK, err := readOptionalFile(projectWorkRoot, leftBaseFile)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	rightBaseData, rightBaseOK, err := readOptionalFile(projectWorkRoot, rightBaseFile)
	if err != nil {
		return builderComparePreviewResponse{}, err
	}
	leftText, leftIsText, leftType, leftImage, leftPreviewKind := buildPreviewPayload(leftRel, leftData, leftOK)
	rightText, rightIsText, rightType, rightImage, rightPreviewKind := buildPreviewPayload(rightRel, rightData, rightOK)
	leftBaseText, leftBaseIsText, _, _, _ := buildPreviewPayload(leftRel, leftBaseData, leftBaseOK)
	rightBaseText, rightBaseIsText, _, _, _ := buildPreviewPayload(rightRel, rightBaseData, rightBaseOK)
	leftBlobPath := ""
	if leftOK && leftPreviewKind != "text" && leftPreviewKind != "other" {
		leftBlobPath = filepath.ToSlash(filepath.Join(leftModel.WorkDir, projectName, "project", leftRel))
	}
	rightBlobPath := ""
	if rightOK && rightPreviewKind != "text" && rightPreviewKind != "other" {
		rightBlobPath = filepath.ToSlash(filepath.Join(rightModel.WorkDir, projectName, "project", rightRel))
	}
	return builderComparePreviewResponse{
		LeftModelID:       modelIDString(leftModel.ID),
		LeftModelLabel:    leftModel.Label,
		LeftPath:          filepath.ToSlash(filepath.Join(leftModel.WorkDir, projectName, "project", leftRel)),
		LeftExists:        leftOK,
		LeftIsText:        leftIsText,
		LeftContentType:   leftType,
		LeftContent:       leftText,
		LeftImageDataURL:  leftImage,
		LeftPreviewKind:   leftPreviewKind,
		LeftBlobPath:      leftBlobPath,
		LeftBaseExists:    leftBaseOK,
		LeftBaseIsText:    leftBaseIsText,
		LeftBaseContent:   leftBaseText,
		RightModelID:      modelIDString(rightModel.ID),
		RightModelLabel:   rightModel.Label,
		RightPath:         filepath.ToSlash(filepath.Join(rightModel.WorkDir, projectName, "project", rightRel)),
		RightExists:       rightOK,
		RightIsText:       rightIsText,
		RightContentType:  rightType,
		RightContent:      rightText,
		RightImageDataURL: rightImage,
		RightPreviewKind:  rightPreviewKind,
		RightBlobPath:     rightBlobPath,
		RightBaseExists:   rightBaseOK,
		RightBaseIsText:   rightBaseIsText,
		RightBaseContent:  rightBaseText,
	}, nil
}

func (a *App) buildDiffPreview(model ModelConfig, rel string) (diffPreviewResponse, error) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		return diffPreviewResponse{}, err
	}
	src, _, err := a.projectPaths(model, projectName)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	dst, err := a.projectWorkRoot(projectName)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		return diffPreviewResponse{}, errors.New("invalid path")
	}
	srcFile, err := safeJoin(src, rel)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	dstFile, err := safeJoin(dst, rel)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	srcData, srcOK, err := readOptionalFile(src, srcFile)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	dstData, dstOK, err := readOptionalFile(dst, dstFile)
	if err != nil {
		return diffPreviewResponse{}, err
	}
	if srcOK && dstOK && bytes.Equal(srcData, dstData) {
		return diffPreviewResponse{}, errors.New("no changes for selected file")
	}
	status := "modified"
	if srcOK && !dstOK {
		status = "added"
	} else if !srcOK && dstOK {
		status = "deleted"
	}
	currentText, currentIsText, currentType, currentImage, currentPreviewKind := buildPreviewPayload(rel, dstData, dstOK)
	candidateText, candidateIsText, candidateType, candidateImage, candidatePreviewKind := buildPreviewPayload(rel, srcData, srcOK)
	currentBlobPath := ""
	if dstOK && currentPreviewKind != "text" && currentPreviewKind != "other" {
		currentBlobPath = filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", rel))
	}
	candidateBlobPath := ""
	if srcOK && candidatePreviewKind != "text" && candidatePreviewKind != "other" {
		candidateBlobPath = filepath.ToSlash(filepath.Join(model.WorkDir, projectName, "project", rel))
	}
	return diffPreviewResponse{
		ModelID:               modelIDString(model.ID),
		Path:                  rel,
		Status:                status,
		CurrentPath:           filepath.ToSlash(filepath.Join("projectwork", rel)),
		CurrentExists:         dstOK,
		CurrentIsText:         currentIsText,
		CurrentContentType:    currentType,
		CurrentContent:        currentText,
		CurrentImageDataURL:   currentImage,
		CurrentPreviewKind:    currentPreviewKind,
		CurrentBlobPath:       currentBlobPath,
		CandidatePath:         filepath.ToSlash(filepath.Join(model.WorkDir, projectName, "project", rel)),
		CandidateExists:       srcOK,
		CandidateIsText:       candidateIsText,
		CandidateContentType:  candidateType,
		CandidateContent:      candidateText,
		CandidateImageDataURL: candidateImage,
		CandidatePreviewKind:  candidatePreviewKind,
		CandidateBlobPath:     candidateBlobPath,
	}, nil
}

func readOptionalFile(root, path string) ([]byte, bool, error) {
	if err := rejectSymlinkPath(root, path); err != nil {
		return nil, false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("%s is a directory", path)
	}
	data, err := readFileUnderRoot(root, path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func buildPreviewPayload(rel string, data []byte, exists bool) (content string, isText bool, contentType string, imageDataURL string, previewKind string) {
	if !exists {
		return "", false, "", "", ""
	}
	contentType = detectContentType(rel, data)
	if strings.HasPrefix(contentType, "image/") {
		if workModePayloadLooksLikeImage(data, contentType) {
			return "", false, contentType, "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), "image"
		}
		sniffedType := http.DetectContentType(data)
		if isLikelyText(rel, data, sniffedType) {
			return string(data), true, sniffedType, "", "text"
		}
		return "", false, sniffedType, "", previewKindForContentType(sniffedType)
	}
	if isLikelyText(rel, data, contentType) {
		return string(data), true, contentType, "", "text"
	}
	return "", false, contentType, "", previewKindForContentType(contentType)
}

func workModeLooksLikeImageOutput(rel, mimeType string) bool {
	baseType := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if strings.HasPrefix(baseType, "image/") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(filepath.Ext(rel))) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	default:
		return false
	}
}

func workModePayloadLooksLikeImage(data []byte, mimeType string) bool {
	if len(data) == 0 {
		return false
	}
	baseType := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if baseType == "image/svg+xml" {
		return bytes.Contains(bytes.ToLower(bytes.TrimSpace(data)), []byte("<svg"))
	}
	sniffed := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	if strings.HasPrefix(sniffed, "image/") {
		return true
	}
	if bytes.Contains(bytes.ToLower(bytes.TrimSpace(data)), []byte("<svg")) {
		return true
	}
	return false
}

func previewKindForContentType(contentType string) string {
	baseType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case strings.HasPrefix(baseType, "image/"):
		return "image"
	case strings.HasPrefix(baseType, "audio/"):
		return "audio"
	case strings.HasPrefix(baseType, "video/"):
		return "video"
	case baseType == "application/pdf":
		return "pdf"
	case baseType == "":
		return "other"
	default:
		return "other"
	}
}

func buildBlobURL(rel string) string {
	clean := filepath.ToSlash(strings.TrimSpace(rel))
	if clean == "" {
		return ""
	}
	return "/api/file/blob?path=" + url.QueryEscape(clean)
}

func detectContentType(rel string, data []byte) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel))); ct != "" {
		return ct
	}
	if len(data) == 0 {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(data)
}

func isLikelyText(rel string, data []byte, contentType string) bool {
	if strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "xml") || strings.Contains(contentType, "javascript") || strings.Contains(contentType, "typescript") || strings.Contains(contentType, "yaml") || strings.Contains(contentType, "x-sh") {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(data)
}

func (a *App) recalculatePendingMergeCount(model ModelConfig) (int, error) {
	files, err := a.buildModelDiff(model)
	if err != nil {
		return 0, err
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		return 0, err
	}
	a.setPendingMergeCount(projectName, modelIDString(model.ID), len(files))
	return len(files), nil
}

func (a *App) handleDiffCandidateSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	preview, err := a.buildDiffPreview(model, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !preview.CandidateExists || !preview.CandidateIsText {
		http.Error(w, "candidate file is not editable", http.StatusBadRequest)
		return
	}
	src, _, err := a.projectPaths(model, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(req.Path)))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	target, err := safeJoin(src, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := writeFileUnderRoot(src, target, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	count, err := a.recalculatePendingMergeCount(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf(req.ModelID, "info", "Saved edited candidate file %s", rel)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pendingCount": count})
}

func removeEmptyParents(root, start string) {
	root = filepath.Clean(root)
	current := filepath.Clean(start)
	for {
		if current == root || current == "." || current == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(current)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(current); err != nil {
			return
		}
		current = filepath.Dir(current)
	}
}

func (a *App) handleDiffCandidateDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	preview, err := a.buildDiffPreview(model, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if preview.Status == "deleted" {
		http.Error(w, "candidate delete is only available for added or modified files", http.StatusBadRequest)
		return
	}
	src, _, err := a.projectPaths(model, projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(req.Path)))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	candidateFile, err := safeJoin(src, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projectworkFile, err := safeJoin(projectworkRoot, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if preview.Status == "added" {
		if err := removeFileUnderRoot(src, candidateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		removeEmptyParents(src, filepath.Dir(candidateFile))
	} else {
		projectworkData, err := readFileUnderRoot(projectworkRoot, projectworkFile)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := writeFileUnderRoot(src, candidateFile, projectworkData, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	count, err := a.recalculatePendingMergeCount(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf(req.ModelID, "info", "Removed candidate merge file %s", rel)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pendingCount": count})
}

func (a *App) handleMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(req.ModelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project before merging.", http.StatusBadRequest)
		return
	}
	summary, lastMergedFiles, copied, err := a.applyMergeToProjectwork(model, projectName, req.Files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if propagated, err := a.propagateMergedAIContextToBuilders(projectName, model); err != nil {
		a.logf(req.ModelID, "warn", "Could not propagate merge winner ai_context.json after merge: %v", err)
	} else if propagated > 0 {
		a.logf(req.ModelID, "info", "Propagated merge winner ai_context.json to %d Builder model(s) for project %s", propagated, projectName)
	}
	a.setLastMergeState(projectName, lastMergedFiles, summary.Files.Deleted, summary)
	a.clearPendingMergeState(projectName)
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(projectName); clearErr != nil {
		a.logf(req.ModelID, "warn", "Failed clearing Builder response cards after merge: %v", clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf(req.ModelID, "info", "Cleared %d Builder response card(s) after merge for project %s", clearedBuilderResponses, projectName)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(projectName); clearErr != nil {
		a.logf(req.ModelID, "warn", "Failed clearing Observer report(s) after merge: %v", clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf(req.ModelID, "info", "Cleared %d Observer report(s) after merge for project %s", clearedReviewerReports, projectName)
	}

	a.logf(req.ModelID, "info", "Merged %d file changes from %s/%s/project into %s and synchronized active builder workspaces", copied, model.WorkDir, projectName, filepath.ToSlash(filepath.Join("projects", projectName, "projectwork")))
	completedWave := 0
	if waveState, ok := a.currentWaveExecution(projectName); ok {
		completedWave = waveState.CurrentWave
	}
	nextWave, started, nextErr := a.continueWaveExecutionAfterMerge(projectName)
	if nextErr != nil {
		http.Error(w, nextErr.Error(), http.StatusInternalServerError)
		return
	}
	resetCount := 0
	resp := map[string]any{"ok": true, "copied": copied, "resetAIContexts": resetCount, "lastMergedFiles": lastMergedFiles}
	if started {
		if reviewerID := a.getReviewerID(); reviewerID != "" {
			if reviewer, ok := a.findModel(reviewerID); ok {
				if _, metaRoot, err := a.projectPaths(reviewer, projectName); err == nil {
					if err := clearReviewerOutputState(metaRoot); err != nil {
						a.logf(reviewerID, "warn", "Failed clearing observer report after merge: %v", err)
					}
				}
			}
		}
		resp["nextWaveStarted"] = true
		resp["nextWave"] = nextWave.Number
		resp["nextWaveBuilders"] = nextWave.BuilderLabels
		a.logf("system", "info", "Preserved ai_context.json because workflow continues to wave %d after merge in project %s", nextWave.Number, projectName)
		a.logf("system", "info", "Launching next populated wave %d after manual merge in project %s", nextWave.Number, projectName)
	} else {
		resp["resetAIContexts"] = resetCount
		completedIndex := 0
		totalWaves := 0
		if waveState, ok := a.currentWaveExecution(projectName); ok {
			completedIndex = waveState.CurrentIndex
			totalWaves = len(waveState.Waves)
		}
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		status := waveStatusState{ProjectName: projectName, Visible: true, CurrentWave: completedWave, State: "complete", Detail: withWaveProgress("Complete", completedIndex, totalWaves), CurrentWavePosition: waveProgressPosition(completedIndex, totalWaves), TotalWaves: totalWaves}
		if status.TotalLoops <= 0 {
			status.TotalLoops = 1
		}
		a.setWaveStatusLocked(projectName, status)
		a.mu.Unlock()
		a.finalizeActiveOutfitRunCompleted(projectName, req.ModelID, model.Label, fmt.Sprintf("Completed after manual merge of %s.", model.Label))
		a.logf("system", "info", "Preserved ai_context.json and reviewer_context.json after the manual merge ended the workflow cycle for project %s", projectName)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleBypassMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project before bypassing a merge.", http.StatusBadRequest)
		return
	}
	state, ok := a.currentWaveExecution(projectName)
	if !ok {
		http.Error(w, "No active wave is waiting for a merge decision.", http.StatusConflict)
		return
	}
	if !state.AwaitingMerge || a.pendingMergeTotal(projectName) <= 0 {
		http.Error(w, "No pending merge candidates are available to bypass.", http.StatusConflict)
		return
	}
	a.mu.RLock()
	riskEnabled := a.riskModeEnabled
	a.mu.RUnlock()
	if riskEnabled {
		http.Error(w, "Bypass Merge is not available during Risk Mode.", http.StatusConflict)
		return
	}

	currentWave := state.CurrentWave
	if currentWave <= 0 && state.CurrentIndex >= 0 && state.CurrentIndex < len(state.Waves) {
		currentWave = state.Waves[state.CurrentIndex].Number
	}
	if restored, restoreErr := a.restoreModelAIContextSnapshots(projectName, state.AIContextBaselines); restoreErr != nil {
		http.Error(w, restoreErr.Error(), http.StatusInternalServerError)
		return
	} else if restored > 0 {
		a.logf("system", "info", "Restored ai_context.json for %d Builder model(s) after bypassing merge candidates in project %s", restored, projectName)
	}
	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.clearPendingMergeState(projectName)
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(projectName); clearErr != nil {
		a.logf("system", "warn", "Failed clearing Builder response cards after bypassing merge candidates: %v", clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf("system", "info", "Cleared %d Builder response card(s) after bypassing merge candidates for project %s", clearedBuilderResponses, projectName)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(projectName); clearErr != nil {
		a.logf("system", "warn", "Failed clearing Observer report(s) after bypassing merge candidates: %v", clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf("system", "info", "Cleared %d Observer report(s) after bypassing merge candidates for project %s", clearedReviewerReports, projectName)
	}
	a.logf("system", "info", "Bypassed merge candidates for project %s after wave %d; projectwork remained unchanged", projectName, currentWave)

	nextWave, started, nextErr := a.continueWaveExecutionAfterMerge(projectName)
	if nextErr != nil {
		http.Error(w, nextErr.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"ok": true, "bypassed": true, "projectworkUnchanged": true}
	if started {
		resp["nextWaveStarted"] = true
		resp["nextWave"] = nextWave.Number
		resp["nextWaveBuilders"] = nextWave.BuilderLabels
		a.logf("system", "info", "Launching next populated wave %d after bypassing merge candidates in project %s", nextWave.Number, projectName)
	} else {
		completedIndex := state.CurrentIndex
		totalWaves := len(state.Waves)
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		status := waveStatusState{ProjectName: projectName, Visible: true, CurrentWave: currentWave, State: "complete", Detail: withWaveProgress("Complete", completedIndex, totalWaves), CurrentWavePosition: waveProgressPosition(completedIndex, totalWaves), TotalWaves: totalWaves}
		if status.TotalLoops <= 0 {
			status.TotalLoops = 1
		}
		a.setWaveStatusLocked(projectName, status)
		a.mu.Unlock()
		a.finalizeActiveOutfitRunCompleted(projectName, "system", "Bypass Merge", "Completed after bypassing merge candidates.")
		a.logf("system", "info", "Bypassed merge candidates and completed the wave flow for project %s", projectName)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) resetAllAIContexts(projectName string, summary mergeSummary) (int, error) {
	reset := 0
	ctx := defaultAIContext()
	change := strings.TrimSpace(fmt.Sprintf("Merged %s output into projectwork using %s merge mode.", summary.SourceModel, summary.MergeMode))
	if change != "" {
		ctx.PriorChanges = []string{change}
	}
	if len(summary.Files.Added) > 0 || len(summary.Files.Modified) > 0 || len(summary.Files.Deleted) > 0 {
		ctx.Architecture = []string{fmt.Sprintf("Last merge files: added=%d modified=%d deleted=%d.", len(summary.Files.Added), len(summary.Files.Modified), len(summary.Files.Deleted))}
	}
	aiData, err := json.MarshalIndent(normalizeAIContext(ctx), "", "  ")
	if err != nil {
		return reset, err
	}
	aiData = append(aiData, '\n')
	for _, model := range a.cfg.Models {
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return reset, err
		}
		if err := os.MkdirAll(metaRoot, 0o755); err != nil {
			return reset, err
		}
		if err := os.MkdirAll(reviewerReviewsDir(metaRoot), 0o755); err != nil {
			return reset, err
		}
		if err := ensureFile(filepath.Join(metaRoot, "user_context.json"), "{}\n"); err != nil {
			return reset, err
		}
		if err := ensureFile(filepath.Join(metaRoot, "reviewer_context.json"), "{}\n"); err != nil {
			return reset, err
		}
		if err := atomicWriteFile(filepath.Join(metaRoot, "ai_context.json"), aiData, 0o644); err != nil {
			return reset, err
		}
		reset++
	}
	return reset, nil
}

func (a *App) resetProjectAIContextsToEmpty(projectName string) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return 0, nil
	}
	reset := 0
	data := []byte("{}\n")
	aiData := defaultAIContextJSON()
	for _, model := range a.cfg.Models {
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return reset, err
		}
		if err := os.MkdirAll(metaRoot, 0o755); err != nil {
			return reset, err
		}
		if err := os.MkdirAll(reviewerReviewsDir(metaRoot), 0o755); err != nil {
			return reset, err
		}
		if err := ensureFile(filepath.Join(metaRoot, "user_context.json"), "{}\n"); err != nil {
			return reset, err
		}
		if err := atomicWriteFile(filepath.Join(metaRoot, "reviewer_context.json"), data, 0o644); err != nil {
			return reset, err
		}
		if err := atomicWriteFile(filepath.Join(metaRoot, "ai_context.json"), aiData, 0o644); err != nil {
			return reset, err
		}
		reset++
	}
	return reset, nil
}

func (a *App) resetModelAIContextToEmpty(projectName string, model ModelConfig) error {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return nil
	}
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(reviewerReviewsDir(metaRoot), 0o755); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(metaRoot, "user_context.json"), "{}\n"); err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(metaRoot, "reviewer_context.json"), []byte("{}\n"), 0o644); err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(metaRoot, "ai_context.json"), defaultAIContextJSON(), 0o644)
}

func (a *App) resetModelAIContextsToEmpty(projectName string, models []ModelConfig) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" || len(models) == 0 {
		return 0, nil
	}
	reset := 0
	seen := map[string]bool{}
	reviewerID := a.getReviewerID()
	for _, model := range models {
		modelID := modelIDString(model.ID)
		if modelID == "" || seen[modelID] || modelID == reviewerID {
			continue
		}
		seen[modelID] = true
		if err := a.resetModelAIContextToEmpty(projectName, model); err != nil {
			return reset, err
		}
		reset++
	}
	return reset, nil
}

func (a *App) resetAllProjectsAIContextsToEmpty() (int, error) {
	projects, err := a.listProjects()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, project := range projects {
		count, err := a.resetProjectAIContextsToEmpty(project.Name)
		total += count
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func fileHasMeaningfulJSON(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
		if anyToString(raw["agentgo_file"]) == aiContextFileIdentity {
			for _, key := range []string{"terminology", "architecture", "prior_changes", "known_issues", "risks_constraints"} {
				if len(coerceAnyStringList(raw[key])) > 0 {
					return true
				}
			}
			return false
		}
	}
	return true
}

func (a *App) handleClearRunContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	resetCount, err := a.resetProjectAIContextsToEmpty(projectName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.clearLastMergedFiles(projectName)
	a.logf("system", "info", "Cleared ai_context.json and reviewer_context.json plus session-scoped merged context files for %d model(s) in project %s", resetCount, projectName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "clearedModels": resetCount})
}

func (a *App) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projects, err := a.listProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func (a *App) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := a.createProject(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.saveProjectLimits(name, req.Limits); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.resetProjectSessionState()
	a.clearLastMergedFiles(name)
	if err := a.setActiveProject(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(name); clearErr != nil {
		a.logf("system", "warn", "Failed clearing Builder response cards after creating project %s: %v", name, clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf("system", "info", "Cleared %d Builder response card(s) after creating project %s", clearedBuilderResponses, name)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(name); clearErr != nil {
		a.logf("system", "warn", "Failed clearing Observer report(s) after creating project %s: %v", name, clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf("system", "info", "Cleared %d Observer report(s) after creating project %s", clearedReviewerReports, name)
	}
	a.logf("system", "info", "Created project %s", name)
	projects, _ := a.listProjects()
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func (a *App) handleSelectProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectSelectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	projects, err := a.listProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for _, p := range projects {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if err := a.ensureProjectScaffold(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.resetProjectSessionState()
	if err := a.setActiveProject(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(name); clearErr != nil {
		a.logf("system", "warn", "Failed clearing stale Builder response cards after project switch: %v", clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf("system", "info", "Cleared %d stale Builder response card(s) for project %s after project switch", clearedBuilderResponses, name)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(name); clearErr != nil {
		a.logf("system", "warn", "Failed clearing stale Observer report(s) after project switch: %v", clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf("system", "info", "Cleared %d stale Observer report(s) for project %s after project switch", clearedReviewerReports, name)
	}
	a.logf("system", "info", "Selected active project %s", name)
	projects, _ = a.listProjects()
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func (a *App) handleSessionUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.sessionTokenUsageSnapshot())
}

func (a *App) sessionTokenUsageSnapshot() tokenUsageEstimate {
	a.mu.RLock()
	defer a.mu.RUnlock()
	snapshot := a.sessionTokenEstimate
	snapshot.Loop = cloneTokenUsageBreakdown(a.currentLoopTokenEstimate)
	snapshot.Wave = cloneTokenUsageBreakdown(a.currentWaveTokenEstimate)
	return snapshot
}

func (a *App) resetSessionTokenUsage() {
	a.mu.Lock()
	a.sessionTokenEstimate = tokenUsageEstimate{}
	a.currentLoopTokenEstimate = nil
	a.currentWaveTokenEstimate = nil
	a.tokenUsageRunProject = ""
	a.tokenUsageExecutionID = ""
	a.tokenUsageLoopLabel = ""
	a.tokenUsageWaveLabel = ""
	a.mu.Unlock()
}

func (a *App) resetTokenUsageHierarchyLocked() {
	a.currentLoopTokenEstimate = nil
	a.currentWaveTokenEstimate = nil
	a.tokenUsageRunProject = ""
	a.tokenUsageExecutionID = ""
	a.tokenUsageLoopLabel = ""
	a.tokenUsageWaveLabel = ""
}

func (a *App) syncTokenUsageHierarchyLocked(projectName string, status waveStatusState) {
	projectName = strings.TrimSpace(projectName)
	stateName := strings.ToLower(strings.TrimSpace(status.State))
	active := projectName != "" && status.Visible && (stateName == "running" || stateName == "risk" || stateName == "waiting" || stateName == "reviewing")
	if !active {
		a.resetTokenUsageHierarchyLocked()
		return
	}
	executionID := ""
	if execState, ok := a.waveExecutionsByProject[projectName]; ok {
		executionID = strings.TrimSpace(execState.ExecutionID)
	}
	if executionID == "" {
		executionID = projectName
	}
	loopLabel := fmt.Sprintf("Loops %d", compatMaxInt(1, status.CurrentLoop))
	waveLabel := fmt.Sprintf("Wave %d", compatMaxInt(0, status.CurrentWave))
	projectChanged := a.tokenUsageRunProject != projectName
	executionChanged := projectChanged || a.tokenUsageExecutionID != executionID
	waveChanged := executionChanged || a.tokenUsageWaveLabel != waveLabel
	if executionChanged {
		a.currentLoopTokenEstimate = &tokenUsageBreakdown{Label: loopLabel}
		a.tokenUsageExecutionID = executionID
	} else if a.currentLoopTokenEstimate != nil {
		a.currentLoopTokenEstimate.Label = loopLabel
	}
	a.tokenUsageLoopLabel = loopLabel
	if waveChanged {
		a.currentWaveTokenEstimate = &tokenUsageBreakdown{Label: waveLabel}
	}
	if a.currentWaveTokenEstimate != nil {
		a.currentWaveTokenEstimate.Label = waveLabel
	}
	a.tokenUsageWaveLabel = waveLabel
	a.tokenUsageRunProject = projectName
}

func cloneTokenUsageBreakdown(src *tokenUsageBreakdown) *tokenUsageBreakdown {
	if src == nil {
		return nil
	}
	dup := *src
	return &dup
}

func (a *App) addSessionTokenUsage(inputTokens, outputTokens int) {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if inputTokens == 0 && outputTokens == 0 {
		return
	}
	a.mu.Lock()
	a.sessionTokenEstimate.InputTokens += inputTokens
	a.sessionTokenEstimate.OutputTokens += outputTokens
	a.sessionTokenEstimate.HasUsage = a.sessionTokenEstimate.InputTokens > 0 || a.sessionTokenEstimate.OutputTokens > 0
	a.sessionTokenEstimate.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if a.currentLoopTokenEstimate != nil {
		a.currentLoopTokenEstimate.InputTokens += inputTokens
		a.currentLoopTokenEstimate.OutputTokens += outputTokens
		a.currentLoopTokenEstimate.HasUsage = a.currentLoopTokenEstimate.InputTokens > 0 || a.currentLoopTokenEstimate.OutputTokens > 0
	}
	if a.currentWaveTokenEstimate != nil {
		a.currentWaveTokenEstimate.InputTokens += inputTokens
		a.currentWaveTokenEstimate.OutputTokens += outputTokens
		a.currentWaveTokenEstimate.HasUsage = a.currentWaveTokenEstimate.InputTokens > 0 || a.currentWaveTokenEstimate.OutputTokens > 0
	}
	a.mu.Unlock()
}

func (a *App) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.clearLogs()
	if err := a.clearAllBuilderOutputStates(); err != nil {
		a.logf("system", "warn", "Failed clearing builder response cards during session reset: %v", err)
	}
	if err := a.clearAllReviewerOutputStates(); err != nil {
		a.logf("system", "warn", "Failed clearing observer reports during session reset: %v", err)
	}
	a.resetProjectSessionState()
	a.clearAllLastMergedFiles()
	a.resetSessionTokenUsage()
	a.logf("system", "info", "Reset session state, cleared active project, cleared saved builder response cards plus observer reports, cleared session-scoped context selections, and reset estimated session token usage")
	projects, _ := a.listProjects()
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func (a *App) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := a.validateProjectName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	projects, err := a.listProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for _, p := range projects {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if err := a.saveProjectLimits(name, req.Limits); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Updated project settings for %s", name)
	projects, _ = a.listProjects()
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func (a *App) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := a.validateProjectName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, model := range a.cfg.Models {
		projectRoot, err := safeJoin(a.cfg.WorkRoot, model.WorkDir, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.RemoveAll(projectRoot); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	projectsRoot, err := safeJoin(a.cfg.WorkRoot, "projects", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(projectsRoot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.activeProject() == name {
		if err := a.setActiveProject(""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	a.logf("system", "warn", "Deleted project %s", name)
	projects, _ := a.listProjects()
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: a.activeProject(), Projects: projects})
}

func modelIDString(id int) string { return strconv.Itoa(id) }

func (a *App) findModel(id string) (ModelConfig, bool) {
	for _, m := range a.cfg.Models {
		if modelIDString(m.ID) == strings.TrimSpace(id) {
			return m, true
		}
	}
	return ModelConfig{}, false
}

func shouldSkipWorkspaceFile(base string) bool {
	if strings.HasPrefix(base, "response_") {
		return true
	}
	return false
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agentgo-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func builderOutputStatePath(metaRoot string) string {
	return filepath.Join(metaRoot, "latest_builder_output.json")
}

func readBuilderOutputState(metaRoot string) (builderOutputState, error) {
	var state builderOutputState
	data, err := os.ReadFile(builderOutputStatePath(metaRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return builderOutputState{}, err
	}
	return state, nil
}

func writeBuilderOutputState(metaRoot string, state builderOutputState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(builderOutputStatePath(metaRoot), data, 0o644)
}

func clearBuilderOutputState(metaRoot string) error {
	err := os.Remove(builderOutputStatePath(metaRoot))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (a *App) clearBuilderResponseStateForModel(projectName string, model ModelConfig) error {
	_, metaRoot, err := a.projectPaths(model, projectName)
	if err != nil {
		return err
	}
	return clearBuilderOutputState(metaRoot)
}

func (a *App) clearAllBuilderResponseStatesForProject(projectName string) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return 0, nil
	}
	cleared := 0
	seen := map[string]bool{}
	for _, model := range a.cfg.Models {
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return cleared, err
		}
		cleanMetaRoot := filepath.Clean(metaRoot)
		if seen[cleanMetaRoot] {
			continue
		}
		seen[cleanMetaRoot] = true
		state, err := readBuilderOutputState(cleanMetaRoot)
		if err != nil {
			return cleared, err
		}
		if !state.HasResponse {
			continue
		}
		if err := clearBuilderOutputState(cleanMetaRoot); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

func (a *App) clearReviewerOutputStatesForProject(projectName string) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return 0, nil
	}
	cleared := 0
	seen := map[string]bool{}
	for _, model := range a.cfg.Models {
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return cleared, err
		}
		cleanMetaRoot := filepath.Clean(metaRoot)
		if seen[cleanMetaRoot] {
			continue
		}
		seen[cleanMetaRoot] = true
		state, err := readReviewerOutputState(cleanMetaRoot)
		if err != nil {
			return cleared, err
		}
		if !state.HasReport {
			continue
		}
		if err := clearReviewerOutputState(cleanMetaRoot); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

func reviewerOutputStatePath(metaRoot string) string {
	return filepath.Join(metaRoot, "latest_reviewer_output.json")
}

func readReviewerOutputState(metaRoot string) (reviewerOutputState, error) {
	var state reviewerOutputState
	data, err := os.ReadFile(reviewerOutputStatePath(metaRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return reviewerOutputState{}, err
	}
	return state, nil
}

func writeReviewerOutputState(metaRoot string, state reviewerOutputState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(reviewerOutputStatePath(metaRoot), data, 0o644)
}

func clearReviewerOutputState(metaRoot string) error {
	err := os.Remove(reviewerOutputStatePath(metaRoot))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func newReviewerOutputState(model ModelConfig, projectName, rawResponse string) reviewerOutputState {
	return reviewerOutputState{
		ModelID:       modelIDString(model.ID),
		ModelLabel:    model.Label,
		Project:       projectName,
		HasReport:     true,
		Unread:        true,
		Timestamp:     time.Now().Format(time.RFC3339),
		RawResponse:   strings.TrimSpace(rawResponse),
		Candidates:    []reviewerCandidateState{},
		PromptOptions: []string{},
	}
}

func (a *App) clearAllReviewerOutputStates() error {
	var firstErr error
	seen := map[string]bool{}
	walkErr := filepath.WalkDir(a.cfg.WorkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() || filepath.Base(path) != "latest_reviewer_output.json" {
			return nil
		}
		cleanPath := filepath.Clean(path)
		if seen[cleanPath] {
			return nil
		}
		seen[cleanPath] = true
		if removeErr := os.Remove(cleanPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
			firstErr = removeErr
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return firstErr
}

func (a *App) clearAllBuilderOutputStates() error {
	var firstErr error
	seen := map[string]bool{}
	walkErr := filepath.WalkDir(a.cfg.WorkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() || filepath.Base(path) != "latest_builder_output.json" {
			return nil
		}
		cleanPath := filepath.Clean(path)
		if seen[cleanPath] {
			return nil
		}
		seen[cleanPath] = true
		if removeErr := os.Remove(cleanPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
			firstErr = removeErr
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return firstErr
}

type syncStats struct {
	Writes  int
	Deletes int
	Skips   int
}

func logSyncPlan(src, dst string, stats syncStats) {
	log.Printf("[INFO] sync: src=%s dst=%s writes=%d deletes=%d skips=%d", src, dst, stats.Writes, stats.Deletes, stats.Skips)
}

func logSyncResult(src, dst string, stats syncStats) {
	log.Printf("[INFO] sync complete: src=%s dst=%s writes=%d deletes=%d skips=%d", src, dst, stats.Writes, stats.Deletes, stats.Skips)
}

type workspaceCollectOptions struct {
	SkipGeneratedMediaDirs bool
}

func isGeneratedMediaSyncDir(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel))), "/")
	if rel == "" || rel == "." {
		return false
	}
	first, _, _ := strings.Cut(rel, "/")
	switch strings.ToLower(first) {
	case "videos", "3dmesh", "video_jobs", "mesh_jobs", "3d_mesh_jobs":
		return true
	default:
		return false
	}
}

func collectWorkspaceFiles(root string) (map[string][]byte, error) {
	return collectWorkspaceFilesWithOptions(root, workspaceCollectOptions{})
}

func collectWorkspaceFilesWithOptions(root string, opts workspaceCollectOptions) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isSymlinkDirEntry(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel := ""
		if path != root {
			var relErr error
			rel, relErr = filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
		}
		if d.IsDir() {
			if opts.SkipGeneratedMediaDirs && isGeneratedMediaSyncDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if shouldSkipWorkspaceFile(base) {
			return nil
		}
		data, err := readFileUnderRoot(root, path)
		if err != nil {
			return nil
		}
		files[rel] = data
		return nil
	})
	return files, err
}

func syncDirContents(src, dst string) (int, error) {
	return syncDirContentsWithOptions(src, dst, workspaceCollectOptions{})
}

func syncDirContentsForProjectSync(src, dst string) (int, error) {
	return syncDirContentsWithOptions(src, dst, workspaceCollectOptions{SkipGeneratedMediaDirs: true})
}

func syncDirContentsWithOptions(src, dst string, opts workspaceCollectOptions) (int, error) {
	srcFiles, err := collectWorkspaceFilesWithOptions(src, opts)
	if err != nil {
		return 0, err
	}
	dstFiles, err := collectWorkspaceFilesWithOptions(dst, opts)
	if err != nil {
		return 0, err
	}
	stats := syncStats{}
	for rel, data := range srcFiles {
		if existing, ok := dstFiles[rel]; ok && bytes.Equal(existing, data) {
			stats.Skips++
			continue
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return stats.Writes + stats.Deletes, err
		}
		if err := atomicWriteFileUnderRoot(dst, target, data, 0o644); err != nil {
			stats.Skips++
			continue
		}
		stats.Writes++
	}
	for rel := range dstFiles {
		if _, ok := srcFiles[rel]; ok {
			continue
		}
		stats.Deletes++
	}
	logSyncPlan(src, dst, stats)
	for rel := range dstFiles {
		if _, ok := srcFiles[rel]; ok {
			continue
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return stats.Writes + stats.Deletes, err
		}
		if err := removeFileUnderRoot(dst, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			stats.Skips++
			continue
		}
	}
	if err := removeEmptyDirs(dst); err != nil {
		return stats.Writes + stats.Deletes, err
	}
	logSyncResult(src, dst, stats)
	return stats.Writes + stats.Deletes, nil
}

func syncSelectedFiles(src, dst string, relPaths []string) (int, error) {
	stats := syncStats{}
	deleteTargets := []string{}
	seen := map[string]bool{}
	for _, rel := range normalizeRelativePaths(relPaths) {
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		srcFile, err := safeJoin(src, rel)
		if err != nil {
			return stats.Writes + stats.Deletes, err
		}
		if err := rejectSymlinkPath(src, srcFile); err != nil {
			stats.Skips++
			continue
		}
		if info, err := os.Stat(srcFile); err == nil {
			if info.IsDir() {
				continue
			}
			data, err := readFileUnderRoot(src, srcFile)
			if err != nil {
				stats.Skips++
				continue
			}
			dstFile, err := safeJoin(dst, rel)
			if err != nil {
				return stats.Writes + stats.Deletes, err
			}
			if existing, err := readFileUnderRoot(dst, dstFile); err == nil && bytes.Equal(existing, data) {
				stats.Skips++
				continue
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				stats.Skips++
				continue
			}
			if err := atomicWriteFileUnderRoot(dst, dstFile, data, 0o644); err != nil {
				stats.Skips++
				continue
			}
			stats.Writes++
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return stats.Writes + stats.Deletes, err
		}
		deleteTargets = append(deleteTargets, rel)
		stats.Deletes++
	}
	logSyncPlan(src, dst, stats)
	for _, rel := range deleteTargets {
		dstFile, err := safeJoin(dst, rel)
		if err != nil {
			return stats.Writes + stats.Deletes, err
		}
		if err := removeFileUnderRoot(dst, dstFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			stats.Skips++
			continue
		}
	}
	if err := removeEmptyDirs(dst); err != nil {
		return stats.Writes + stats.Deletes, err
	}
	logSyncResult(src, dst, stats)
	return stats.Writes + stats.Deletes, nil
}

func removeEmptyDirs(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isSymlinkDirEntry(d) {
			return nil
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (a *App) syncBuilderProjectsFromProjectwork(projectName string, models []ModelConfig) (int, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return 0, err
	}
	count := 0
	reviewerID := a.getReviewerID()
	seen := map[string]bool{}
	for _, model := range models {
		modelID := modelIDString(model.ID)
		if modelID == "" || modelID == reviewerID || seen[modelID] {
			continue
		}
		seen[modelID] = true
		projectRoot, _, err := a.projectPaths(model, projectName)
		if err != nil {
			return count, err
		}
		targetLabel := strings.TrimSpace(model.Label)
		if targetLabel == "" {
			targetLabel = modelID
		}
		a.logf("system", "info", "Synchronizing projectwork into active model %s (%s) for project %s", targetLabel, modelID, projectName)
		n, err := syncDirContentsForProjectSync(projectworkRoot, projectRoot)
		if err != nil {
			return count, fmt.Errorf("sync projectwork -> %s failed: %w", modelID, err)
		}
		count += n
	}
	return count, nil
}

func (a *App) activeBuilderModelsSnapshot() []ModelConfig {
	a.mu.RLock()
	reviewerID := a.reviewerID
	togglesSnapshot := map[string]bool{}
	for modelID, enabled := range a.toggles {
		togglesSnapshot[modelID] = enabled
	}
	a.mu.RUnlock()

	models := make([]ModelConfig, 0, len(a.cfg.Models))
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if modelID == "" || modelID == reviewerID || !togglesSnapshot[modelID] {
			continue
		}
		models = append(models, model)
	}
	return models
}

func (a *App) syncActiveBuilderProjectsFromProjectwork(projectName string) (int, error) {
	return a.syncBuilderProjectsFromProjectwork(projectName, a.activeBuilderModelsSnapshot())
}

func (a *App) syncAllBuilderProjectsFromProjectwork(projectName string) (int, error) {
	return a.syncBuilderProjectsFromProjectwork(projectName, a.cfg.Models)
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (a *App) snapshotModelAIContexts(projectName string, models []ModelConfig) map[string]string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" || len(models) == 0 {
		return nil
	}
	snapshots := map[string]string{}
	seen := map[string]bool{}
	for _, model := range models {
		modelID := modelIDString(model.ID)
		if modelID == "" || seen[modelID] {
			continue
		}
		seen[modelID] = true
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			a.logf("system", "warn", "Could not snapshot ai_context.json for %s before wave run: %v", model.Label, err)
			continue
		}
		data, err := os.ReadFile(filepath.Join(metaRoot, "ai_context.json"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				a.logf("system", "warn", "Could not read ai_context.json for %s before wave run: %v", model.Label, err)
			}
			data = defaultAIContextJSON()
		}
		snapshots[modelID] = string(data)
	}
	if len(snapshots) == 0 {
		return nil
	}
	return snapshots
}

func (a *App) restoreModelAIContextSnapshots(projectName string, snapshots map[string]string) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" || len(snapshots) == 0 {
		return 0, nil
	}
	restored := 0
	for modelID, data := range snapshots {
		model, ok := a.findModel(modelID)
		if !ok {
			continue
		}
		_, metaRoot, err := a.projectPaths(model, projectName)
		if err != nil {
			return restored, err
		}
		if strings.TrimSpace(data) == "" {
			data = string(defaultAIContextJSON())
		}
		if err := atomicWriteFile(filepath.Join(metaRoot, "ai_context.json"), []byte(data), 0o644); err != nil {
			return restored, err
		}
		restored++
	}
	return restored, nil
}

func (a *App) builderModelsForCurrentWaveExecution(projectName string) []ModelConfig {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return a.activeBuilderModelsSnapshot()
	}

	a.mu.RLock()
	state, ok := a.waveExecutionsByProject[projectName]
	reviewerID := a.reviewerID
	a.mu.RUnlock()
	if !ok || len(state.Waves) == 0 {
		return a.activeBuilderModelsSnapshot()
	}

	models := []ModelConfig{}
	seen := map[string]bool{}
	for _, wave := range state.Waves {
		for _, builderID := range wave.BuilderIDs {
			builderID = strings.TrimSpace(builderID)
			if builderID == "" || builderID == reviewerID || seen[builderID] {
				continue
			}
			model, ok := a.findModel(builderID)
			if !ok {
				continue
			}
			seen[builderID] = true
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return a.activeBuilderModelsSnapshot()
	}
	return models
}

func (a *App) propagateMergedAIContextToBuilders(projectName string, source ModelConfig) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return 0, nil
	}
	_, sourceMetaRoot, err := a.projectPaths(source, projectName)
	if err != nil {
		return 0, err
	}
	sourcePath := filepath.Join(sourceMetaRoot, "ai_context.json")
	ctx, ok := readAIContextFile(sourcePath)
	if !ok {
		return 0, fmt.Errorf("merge winner ai_context.json is missing or invalid: %s", sourcePath)
	}
	data := []byte(formatAIContextObject(ctx))
	targets := a.builderModelsForCurrentWaveExecution(projectName)
	reviewerID := a.getReviewerID()
	seen := map[string]bool{}
	copied := 0
	for _, target := range targets {
		targetID := modelIDString(target.ID)
		if targetID == "" || targetID == reviewerID || seen[targetID] {
			continue
		}
		seen[targetID] = true
		_, targetMetaRoot, err := a.projectPaths(target, projectName)
		if err != nil {
			return copied, err
		}
		if err := os.MkdirAll(targetMetaRoot, 0o755); err != nil {
			return copied, err
		}
		if err := atomicWriteFile(filepath.Join(targetMetaRoot, "ai_context.json"), data, 0o644); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

func (a *App) readReviewerNextPrompt(projectName string) (string, error) {
	reviewerID := a.getReviewerID()
	if reviewerID == "" {
		return "", errors.New("reviewer not enabled")
	}
	reviewer, ok := a.findModel(reviewerID)
	if !ok {
		return "", errors.New("reviewer model not found")
	}
	_, metaRoot, err := a.projectPaths(reviewer, projectName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(reviewerLatestPath(metaRoot))
	if err != nil {
		return "", err
	}
	var parsed struct {
		NextPrompt string `json:"next_prompt"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	parsed.NextPrompt = strings.TrimSpace(parsed.NextPrompt)
	if parsed.NextPrompt == "" {
		return "", errors.New("reviewer next_prompt is empty")
	}
	return parsed.NextPrompt, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(want) {
			return true
		}
	}
	return false
}

func normalizeRelativePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, rel := range paths {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		rel = strings.TrimPrefix(rel, "/")
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseWaveNumberKey(value string) (int, bool) {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return 0, false
	}
	n, err := strconv.Atoi(clean)
	if err != nil {
		return 0, false
	}
	if n < 0 || n > 99 {
		return 0, false
	}
	return n, true
}

func normalizeWavePromptMap(values map[string]string) map[int]string {
	if len(values) == 0 {
		return nil
	}
	out := map[int]string{}
	for key, value := range values {
		wave, ok := parseWaveNumberKey(key)
		if !ok {
			continue
		}
		out[wave] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeWaveContextFileMap(values map[string][]string) map[int][]string {
	if len(values) == 0 {
		return nil
	}
	out := map[int][]string{}
	for key, files := range values {
		wave, ok := parseWaveNumberKey(key)
		if !ok {
			continue
		}
		out[wave] = normalizeRelativePaths(files)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func copyWavePromptMap(values map[int]string) map[int]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[int]string, len(values))
	for key, value := range values {
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func copyWaveContextFileMap(values map[int][]string) map[int][]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[int][]string, len(values))
	for key, files := range values {
		out[key] = append([]string(nil), normalizeRelativePaths(files)...)
	}
	return out
}

func normalizeMediaInputRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "start_frame":
		return "start_frame"
	case "end_frame":
		return "end_frame"
	default:
		return ""
	}
}

func normalizeWaveMediaInputRoleMap(values map[string]map[string]string) map[int]map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[int]map[string]string{}
	for key, roles := range values {
		wave, ok := parseWaveNumberKey(key)
		if !ok || len(roles) == 0 {
			continue
		}
		clean := map[string]string{}
		for path, role := range roles {
			normalizedPath := normalizeRelativePaths([]string{path})
			if len(normalizedPath) == 0 {
				continue
			}
			normalizedRole := normalizeMediaInputRole(role)
			if normalizedRole == "" {
				continue
			}
			clean[normalizedPath[0]] = normalizedRole
		}
		if len(clean) > 0 {
			out[wave] = clean
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func copyWaveMediaInputRoleMap(values map[int]map[string]string) map[int]map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[int]map[string]string, len(values))
	for key, roles := range values {
		clean := map[string]string{}
		for path, role := range roles {
			normalizedPath := normalizeRelativePaths([]string{path})
			if len(normalizedPath) == 0 {
				continue
			}
			normalizedRole := normalizeMediaInputRole(role)
			if normalizedRole == "" {
				continue
			}
			clean[normalizedPath[0]] = normalizedRole
		}
		if len(clean) > 0 {
			out[key] = clean
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func combineRelativePathSets(groups ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range groups {
		for _, rel := range normalizeRelativePaths(group) {
			if seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out
}

func isLikelyTextFileForListing(path, rel string, size int64) bool {
	if size == 0 {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	sampleSize := int64(8192)
	if size < sampleSize {
		sampleSize = size
	}
	buf := make([]byte, sampleSize)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	buf = buf[:n]
	contentType := detectContentType(rel, buf)
	return isLikelyText(rel, buf, contentType)
}

func collectContextFileEntries(root string) ([]contextFileEntry, error) {
	out := []contextFileEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isSymlinkDirEntry(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		cleanRel := filepath.ToSlash(rel)
		out = append(out, contextFileEntry{Path: cleanRel, Size: info.Size(), IsText: isLikelyTextFileForListing(path, cleanRel, info.Size()) && info.Size() <= chatProjectFileMaxBytes})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func buildUnifiedDiff(rel string, oldData, newData []byte) string {
	oldLines := splitLines(string(oldData))
	newLines := splitLines(string(newData))
	ops := diffLines(oldLines, newLines)

	var b strings.Builder
	b.WriteString("--- projectwork/")
	b.WriteString(rel)
	b.WriteString("\n+++ candidate/")
	b.WriteString(rel)
	b.WriteString("\n")
	for _, op := range ops {
		switch op.Kind {
		case "equal":
			for _, line := range op.Lines {
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		case "delete":
			for _, line := range op.Lines {
				b.WriteString("- ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		case "insert":
			for _, line := range op.Lines {
				b.WriteString("+ ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

type diffOp struct {
	Kind  string
	Lines []string
}

func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	ops := []diffOp{}
	appendLine := func(kind, line string) {
		if len(ops) > 0 && ops[len(ops)-1].Kind == kind {
			ops[len(ops)-1].Lines = append(ops[len(ops)-1].Lines, line)
			return
		}
		ops = append(ops, diffOp{Kind: kind, Lines: []string{line}})
	}

	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			appendLine("equal", a[i])
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			appendLine("delete", a[i])
			i++
		} else {
			appendLine("insert", b[j])
			j++
		}
	}
	for ; i < n; i++ {
		appendLine("delete", a[i])
	}
	for ; j < m; j++ {
		appendLine("insert", b[j])
	}
	return ops
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(s, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return []string{}
	}
	return parts
}

type builderParseMeta struct {
	JSONText          string
	UserFacingText    string
	Normalized        bool
	NormalizationNote string
}

func parseBuilderResponse(raw string) (builderResponse, builderParseMeta, error) {
	var resp builderResponse
	jsonText, meta, err := splitBuilderResponse(raw)
	if err != nil {
		return resp, meta, err
	}
	normalizedText, normalized, note, err := normalizeBuilderResponseJSON(jsonText)
	if err != nil {
		return resp, meta, err
	}
	if normalized {
		meta.Normalized = true
		meta.NormalizationNote = note
		meta.JSONText = normalizedText
	} else {
		meta.JSONText = jsonText
	}
	if err := json.Unmarshal([]byte(meta.JSONText), &resp); err != nil {
		return resp, meta, err
	}
	return resp, meta, nil
}

func normalizeBuilderResponseJSON(jsonText string) (string, bool, string, error) {
	trimmed := strings.TrimSpace(jsonText)
	if trimmed == "" {
		return jsonText, false, "", nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return jsonText, false, "", err
	}
	normalized := raw
	repairedAny := false
	notes := make([]string, 0, 4)
	if fixed, repaired, note := repairAgentGOToolHeader(normalized, agentGOToolBuilder); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderSummaryField(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderTopLevelFields(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderMissingFileActions(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderFilesArray(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderDropEmptyWriteEntries(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderEmptyAIContextClosure(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderInvalidAIContextClosure(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderEmptyNotesClosure(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if fixed, repaired, note := repairBuilderRequiredArrays(normalized); repaired {
		normalized = fixed
		repairedAny = true
		if strings.TrimSpace(note) != "" {
			notes = append(notes, note)
		}
	}
	if !repairedAny {
		return jsonText, false, "", nil
	}
	buf, err := json.Marshal(normalized)
	if err != nil {
		return jsonText, false, "", err
	}
	return string(buf), true, strings.Join(notes, "; "), nil
}

func repairAgentGOToolHeader(raw map[string]any, toolName string) (map[string]any, bool, string) {
	changed := false
	if raw == nil {
		raw = map[string]any{}
		changed = true
	}
	toolName = strings.TrimSpace(toolName)
	if current, ok := raw["agentgo_tool"].(string); !ok || strings.TrimSpace(current) != toolName {
		raw["agentgo_tool"] = toolName
		changed = true
	}
	if !isAgentGOVersionOneValue(raw["tool_version"]) {
		raw["tool_version"] = agentGOToolVersion
		changed = true
	}
	if _, ok := raw["schema_version"]; ok {
		delete(raw, "schema_version")
		changed = true
	}
	if !changed {
		return raw, false, ""
	}
	return raw, true, "normalized AgentGO tool header"
}

func normalizeAgentGOToolResponseJSON(jsonText, toolName string) (string, bool, string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonText), &raw); err != nil {
		return jsonText, false, "", err
	}
	normalized, changed, note := repairAgentGOToolHeader(raw, toolName)
	if !changed {
		return jsonText, false, "", nil
	}
	buf, err := json.Marshal(normalized)
	if err != nil {
		return jsonText, false, "", err
	}
	return string(buf), true, note, nil
}

func isAgentGOVersionOneValue(value any) bool {
	switch v := value.(type) {
	case int:
		return v == agentGOToolVersion
	case int64:
		return v == int64(agentGOToolVersion)
	case float64:
		return v == float64(agentGOToolVersion)
	case json.Number:
		return strings.TrimSpace(v.String()) == strconv.Itoa(agentGOToolVersion)
	case string:
		return strings.TrimSpace(v) == strconv.Itoa(agentGOToolVersion)
	default:
		return false
	}
}

func repairBuilderSummaryField(raw map[string]any) (map[string]any, bool, string) {
	value, ok := raw["summary"]
	if !ok || value == nil {
		return raw, false, ""
	}
	if summary, ok := value.(string); ok && strings.TrimSpace(summary) != "" {
		return raw, false, ""
	}
	normalized := normalizeLooseSummaryValue(value)
	if normalized == "" {
		return raw, false, ""
	}
	raw["summary"] = normalized
	return raw, true, "normalized non-string summary into plain text"
}

func normalizeLooseSummaryValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			keyText := strings.TrimSpace(key)
			valueText := normalizeLooseSummaryValue(v[key])
			switch {
			case keyText != "" && valueText != "":
				parts = append(parts, keyText+": "+valueText)
			case keyText != "":
				parts = append(parts, keyText)
			case valueText != "":
				parts = append(parts, valueText)
			}
		}
		return strings.Join(parts, "; ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			itemText := normalizeLooseSummaryValue(item)
			if itemText != "" {
				parts = append(parts, itemText)
			}
		}
		return strings.Join(parts, "; ")
	case json.Number:
		return v.String()
	case float64, float32, int, int64, int32, uint, uint64, uint32, bool:
		return strings.TrimSpace(fmt.Sprint(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func repairBuilderMissingFileActions(raw map[string]any) (map[string]any, bool, string) {
	files, ok := raw["files"].([]any)
	if !ok || len(files) == 0 {
		return raw, false, ""
	}
	repairedIndexes := make([]string, 0, len(files))
	for i, item := range files {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(anyToString(entry["action"])))
		if action != "" {
			continue
		}
		path := strings.TrimSpace(anyToString(entry["path"]))
		if path == "" {
			continue
		}
		hasContent := false
		if content, ok := entry["content"].(string); ok && content != "" {
			hasContent = true
		}
		hasArtifact := strings.TrimSpace(anyToString(entry["artifact_ref"])) != ""
		if hasContent == hasArtifact {
			continue
		}
		entry["action"] = "create"
		files[i] = entry
		repairedIndexes = append(repairedIndexes, strconv.Itoa(i))
	}
	if len(repairedIndexes) == 0 {
		return raw, false, ""
	}
	raw["files"] = files
	return raw, true, "filled missing files[].action as create for indexes " + strings.Join(repairedIndexes, ", ")
}

func repairBuilderFilesArray(raw map[string]any) (map[string]any, bool, string) {
	filesValue, exists := raw["files"]
	if !exists || filesValue == nil {
		return raw, false, ""
	}
	files, ok := filesValue.([]any)
	if !ok {
		entry, single := filesValue.(map[string]any)
		if !single || !looksLikeLikelyBuilderFileEntry(entry) {
			return raw, false, ""
		}
		raw["files"] = []any{entry}
		return raw, true, "wrapped a single files object into files[]"
	}
	if len(files) == 0 {
		return raw, false, ""
	}
	kept := make([]any, 0, len(files))
	validCount := 0
	droppedCount := 0
	for _, item := range files {
		entry, ok := item.(map[string]any)
		if !ok {
			droppedCount++
			continue
		}
		if looksLikeBuilderMetaPseudoFile(entry) && !looksLikeLikelyBuilderFileEntry(entry) {
			droppedCount++
			continue
		}
		if !looksLikeLikelyBuilderFileEntry(entry) {
			droppedCount++
			continue
		}
		kept = append(kept, entry)
		validCount++
	}
	if droppedCount == 0 || validCount == 0 {
		return raw, false, ""
	}
	raw["files"] = kept
	entryWord := "entries"
	if validCount == 1 {
		entryWord = "entry"
	}
	invalidWord := "entries"
	if droppedCount == 1 {
		invalidWord = "entry"
	}
	return raw, true, fmt.Sprintf("sanitized files[] by keeping %d likely file %s and dropping %d invalid mixed %s", validCount, entryWord, droppedCount, invalidWord)
}

func looksLikeLikelyBuilderFileEntry(entry map[string]any) bool {
	if strings.TrimSpace(anyToString(entry["path"])) != "" {
		return true
	}
	action := strings.ToLower(strings.TrimSpace(anyToString(entry["action"])))
	if action == "create" || action == "overwrite" || action == "delete" {
		return true
	}
	if content, ok := entry["content"].(string); ok && strings.TrimSpace(content) != "" {
		return true
	}
	if strings.TrimSpace(anyToString(entry["artifact_ref"])) != "" {
		return true
	}
	return false
}

func repairBuilderDropEmptyWriteEntries(raw map[string]any) (map[string]any, bool, string) {
	files, ok := raw["files"].([]any)
	if !ok || len(files) == 0 {
		return raw, false, ""
	}
	kept := make([]any, 0, len(files))
	droppedIndexes := make([]string, 0, len(files))
	for i, item := range files {
		entry, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		action := strings.ToLower(strings.TrimSpace(anyToString(entry["action"])))
		path := strings.TrimSpace(anyToString(entry["path"]))
		hasArtifact := strings.TrimSpace(anyToString(entry["artifact_ref"])) != ""
		content, hasContentField := entry["content"].(string)
		isObviouslyEmptyWrite := (action == "create" || action == "overwrite") &&
			path != "" &&
			!hasArtifact &&
			(!hasContentField || strings.TrimSpace(content) == "")
		if isObviouslyEmptyWrite {
			droppedIndexes = append(droppedIndexes, strconv.Itoa(i))
			continue
		}
		kept = append(kept, item)
	}
	if len(droppedIndexes) == 0 || len(kept) == 0 {
		return raw, false, ""
	}
	raw["files"] = kept
	entryWord := "entries"
	if len(droppedIndexes) == 1 {
		entryWord = "entry"
	}
	return raw, true, "dropped obviously empty create/overwrite files[] " + entryWord + " at indexes " + strings.Join(droppedIndexes, ", ")
}

func repairBuilderTopLevelFields(raw map[string]any) (map[string]any, bool, string) {
	files, ok := raw["files"].([]any)
	if !ok || len(files) == 0 {
		return raw, false, ""
	}
	_, hasTopAIContext := raw["ai_context"]
	_, hasTopNotes := raw["notes"]
	_, hasTopWarnings := raw["warnings"]
	_, hasTopConfidence := raw["confidence"]
	_, hasTopArtifacts := raw["artifacts"]
	candidateIndex := -1
	for i, item := range files {
		entry, ok := item.(map[string]any)
		if !ok || !hasBuilderMetaFields(entry) {
			continue
		}
		needsLift := (!hasTopAIContext && entry["ai_context"] != nil) ||
			(!hasTopNotes && entry["notes"] != nil) ||
			(!hasTopWarnings && entry["warnings"] != nil) ||
			(!hasTopConfidence && entry["confidence"] != nil) ||
			(!hasTopArtifacts && entry["artifacts"] != nil)
		if !needsLift {
			continue
		}
		if candidateIndex != -1 {
			return raw, false, ""
		}
		candidateIndex = i
	}
	if candidateIndex == -1 {
		return raw, false, ""
	}
	entry, _ := files[candidateIndex].(map[string]any)
	repaired := false
	parts := []string{}
	if !hasTopAIContext {
		if value, ok := entry["ai_context"]; ok {
			raw["ai_context"] = value
			repaired = true
			parts = append(parts, "ai_context")
		}
	}
	if !hasTopNotes {
		if value, ok := entry["notes"]; ok {
			raw["notes"] = value
			repaired = true
			parts = append(parts, "notes")
		}
	}
	if !hasTopWarnings {
		if value, ok := entry["warnings"]; ok {
			raw["warnings"] = value
			repaired = true
			parts = append(parts, "warnings")
		}
	}
	if !hasTopConfidence {
		if value, ok := entry["confidence"]; ok {
			raw["confidence"] = value
			repaired = true
			parts = append(parts, "confidence")
		}
	}
	if !hasTopArtifacts {
		if value, ok := entry["artifacts"]; ok {
			raw["artifacts"] = value
			repaired = true
			parts = append(parts, "artifacts")
		}
	}
	if !repaired {
		return raw, false, ""
	}
	entry = stripBuilderMetaFields(entry)
	if looksLikeLikelyBuilderFileEntry(entry) {
		files[candidateIndex] = entry
		raw["files"] = files
	} else {
		cleaned := make([]any, 0, len(files)-1)
		for i, item := range files {
			if i == candidateIndex {
				continue
			}
			cleaned = append(cleaned, item)
		}
		raw["files"] = cleaned
	}
	note := "lifted misplaced " + strings.Join(parts, ", ") + " from malformed files[] entry"
	return raw, true, note
}

func aiContextMapFromBuilderContext(ctx builderAIContext) map[string]any {
	ctx = normalizeAIContext(ctx)
	return map[string]any{
		"agentgo_file":      ctx.AgentGOFile,
		"file_version":      ctx.FileVersion,
		"terminology":       stringSliceToAny(ctx.Terminology),
		"architecture":      stringSliceToAny(ctx.Architecture),
		"prior_changes":     stringSliceToAny(ctx.PriorChanges),
		"known_issues":      stringSliceToAny(ctx.KnownIssues),
		"risks_constraints": stringSliceToAny(ctx.RisksConstraints),
	}
}

func stringSliceToAny(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func defaultAIContextMap() map[string]any {
	return aiContextMapFromBuilderContext(defaultAIContext())
}

func coerceAnyStringList(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if clean := strings.TrimSpace(anyToString(item)); clean != "" {
				out = append(out, clean)
			}
		}
		return out
	case string:
		if clean := strings.TrimSpace(v); clean != "" {
			return []string{clean}
		}
	}
	return []string{}
}

func migrateAIContextMap(value map[string]any) (map[string]any, bool) {
	ctx := defaultAIContext()
	changed := false
	if anyToString(value["agentgo_file"]) != aiContextFileIdentity {
		changed = true
	}
	if n, ok := value["file_version"].(float64); !ok || int(n) != agentGOToolVersion {
		changed = true
	}
	ctx.Terminology = coerceAnyStringList(value["terminology"])
	ctx.Architecture = coerceAnyStringList(value["architecture"])
	ctx.PriorChanges = coerceAnyStringList(value["prior_changes"])
	ctx.KnownIssues = coerceAnyStringList(value["known_issues"])
	ctx.RisksConstraints = coerceAnyStringList(value["risks_constraints"])
	if summary := strings.TrimSpace(anyToString(value["summary"])); summary != "" && len(ctx.PriorChanges) == 0 {
		ctx.PriorChanges = append(ctx.PriorChanges, summary)
		changed = true
	}
	if improvements := coerceAnyStringList(value["improvements_made"]); len(improvements) > 0 && len(ctx.PriorChanges) == 0 {
		ctx.PriorChanges = append(ctx.PriorChanges, improvements...)
		changed = true
	}
	if handoff := strings.TrimSpace(anyToString(value["handoff_notes"])); handoff != "" && len(ctx.RisksConstraints) == 0 {
		ctx.RisksConstraints = append(ctx.RisksConstraints, handoff)
		changed = true
	}
	if risks := coerceAnyStringList(value["risks"]); len(risks) > 0 && len(ctx.RisksConstraints) == 0 {
		ctx.RisksConstraints = append(ctx.RisksConstraints, risks...)
		changed = true
	}
	for _, key := range []string{"terminology", "architecture", "prior_changes", "known_issues", "risks_constraints"} {
		if _, ok := value[key]; !ok {
			changed = true
		}
	}
	return aiContextMapFromBuilderContext(ctx), changed
}

func repairBuilderEmptyAIContextClosure(raw map[string]any) (map[string]any, bool, string) {
	value, exists := raw["ai_context"]
	if !exists {
		return raw, false, ""
	}
	if value == nil {
		raw["ai_context"] = defaultAIContextMap()
		return raw, true, "normalized null ai_context into strict ai_context object"
	}
	if items, ok := value.([]any); ok {
		if len(items) != 0 {
			return raw, false, ""
		}
		raw["ai_context"] = defaultAIContextMap()
		return raw, true, "normalized empty ai_context array into strict ai_context object"
	}
	if ctx, ok := value.(map[string]any); ok {
		migrated, changed := migrateAIContextMap(ctx)
		if changed {
			raw["ai_context"] = migrated
			return raw, true, "migrated ai_context into strict project-memory schema"
		}
	}
	return raw, false, ""
}

func repairBuilderInvalidAIContextClosure(raw map[string]any) (map[string]any, bool, string) {
	value, exists := raw["ai_context"]
	if !exists || value == nil {
		return raw, false, ""
	}
	if _, ok := value.(map[string]any); ok {
		return raw, false, ""
	}
	raw["ai_context"] = defaultAIContextMap()
	return raw, true, "replaced invalid ai_context shape with empty strict ai_context object"
}

func repairBuilderEmptyNotesClosure(raw map[string]any) (map[string]any, bool, string) {
	value, exists := raw["notes"]
	if !exists {
		return raw, false, ""
	}
	if value == nil {
		raw["notes"] = ""
		return raw, true, "normalized null notes into empty string"
	}
	if items, ok := value.([]any); ok {
		if len(items) != 0 {
			return raw, false, ""
		}
		raw["notes"] = ""
		return raw, true, "normalized empty notes array into empty string"
	}
	return raw, false, ""
}

func repairBuilderRequiredArrays(raw map[string]any) (map[string]any, bool, string) {
	ctx, ok := raw["ai_context"].(map[string]any)
	if !ok || ctx == nil {
		return raw, false, ""
	}
	migrated, changed := migrateAIContextMap(ctx)
	if !changed {
		return raw, false, ""
	}
	raw["ai_context"] = migrated
	return raw, true, "filled/migrated strict ai_context fields"
}

func looksLikeBuilderMetaPseudoFile(entry map[string]any) bool {
	action := strings.ToLower(strings.TrimSpace(anyToString(entry["action"])))
	validAction := action == "create" || action == "overwrite" || action == "delete"
	if validAction {
		return false
	}
	return hasBuilderMetaFields(entry)
}

func anyToString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func hasBuilderMetaFields(entry map[string]any) bool {
	_, hasAIContext := entry["ai_context"]
	_, hasNotes := entry["notes"]
	_, hasWarnings := entry["warnings"]
	_, hasConfidence := entry["confidence"]
	_, hasArtifacts := entry["artifacts"]
	return hasAIContext || hasNotes || hasWarnings || hasConfidence || hasArtifacts
}

func stripBuilderMetaFields(entry map[string]any) map[string]any {
	delete(entry, "ai_context")
	delete(entry, "notes")
	delete(entry, "warnings")
	delete(entry, "confidence")
	delete(entry, "artifacts")
	return entry
}

func decodePromptHelperResponse(clean string) (promptHelperResponse, error) {
	var direct promptHelperResponse
	if err := json.Unmarshal([]byte(clean), &direct); err == nil {
		return direct, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(clean), &raw); err != nil {
		return promptHelperResponse{}, fmt.Errorf("Prompt Helper returned invalid JSON: %v. Raw snippet: %s", err, previewForLog(clean, 280))
	}
	var parsed promptHelperResponse
	prompt, ok := coercePromptHelperTextField(raw["recommended_prompt"])
	if !ok || strings.TrimSpace(prompt) == "" {
		return promptHelperResponse{}, fmt.Errorf("Prompt Helper returned invalid recommended_prompt shape. Raw snippet: %s", previewForLog(clean, 280))
	}
	parsed.RecommendedPrompt = prompt
	if why, ok := coercePromptHelperTextField(raw["why_safer"]); ok {
		parsed.WhySafer = why
	}
	if tip, ok := coercePromptHelperTextField(raw["tip"]); ok {
		parsed.Tip = tip
	}
	return parsed, nil
}

func coercePromptHelperTextField(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		return strings.TrimSpace(v), true
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			part, ok := coercePromptHelperTextField(item)
			if !ok || strings.TrimSpace(part) == "" {
				return "", false
			}
			parts = append(parts, strings.TrimSpace(part))
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), len(parts) > 0
	case map[string]any:
		for _, key := range []string{"recommended_prompt", "prompt", "text", "content", "value"} {
			if nested, ok := v[key]; ok {
				return coercePromptHelperTextField(nested)
			}
		}
		return "", false
	default:
		return "", false
	}
}

func validateAgentGOToolHeader(toolName string, toolVersion int, expectedTool string) error {
	if strings.TrimSpace(toolName) != expectedTool {
		return fmt.Errorf("agentgo_tool must be %q", expectedTool)
	}
	if toolVersion != agentGOToolVersion {
		return fmt.Errorf("tool_version must be %d", agentGOToolVersion)
	}
	return nil
}

func validateBuilderResponse(resp builderResponse) error {
	if err := validateAgentGOToolHeader(resp.AgentGOTool, resp.ToolVersion, agentGOToolBuilder); err != nil {
		return err
	}
	if strings.TrimSpace(resp.Summary) == "" {
		return errors.New("summary is required")
	}
	if resp.Files == nil {
		return errors.New("files is required")
	}
	if strings.TrimSpace(resp.AIContext.AgentGOFile) != aiContextFileIdentity {
		return errors.New("ai_context.agentgo_file must be \"ai_context\"")
	}
	if resp.AIContext.FileVersion != agentGOToolVersion {
		return fmt.Errorf("ai_context.file_version must be %d", agentGOToolVersion)
	}
	if resp.AIContext.Terminology == nil {
		return errors.New("ai_context.terminology is required")
	}
	if resp.AIContext.Architecture == nil {
		return errors.New("ai_context.architecture is required")
	}
	if resp.AIContext.PriorChanges == nil {
		return errors.New("ai_context.prior_changes is required")
	}
	if resp.AIContext.KnownIssues == nil {
		return errors.New("ai_context.known_issues is required")
	}
	if resp.AIContext.RisksConstraints == nil {
		return errors.New("ai_context.risks_constraints is required")
	}
	artifactIDs := make(map[string]builderArtifact, len(resp.Artifacts))
	for i, artifact := range resp.Artifacts {
		id := strings.TrimSpace(artifact.ID)
		if id == "" {
			return fmt.Errorf("artifacts[%d].id is required", i)
		}
		if _, exists := artifactIDs[id]; exists {
			return fmt.Errorf("artifacts[%d].id %q is duplicated", i, id)
		}
		encoding := strings.ToLower(strings.TrimSpace(artifact.Encoding))
		if encoding == "" {
			return fmt.Errorf("artifacts[%d].encoding is required", i)
		}
		if encoding != "base64" {
			return fmt.Errorf("artifacts[%d].encoding must be base64", i)
		}
		if strings.TrimSpace(artifact.Data) == "" {
			return fmt.Errorf("artifacts[%d].data is required", i)
		}
		artifactIDs[id] = artifact
	}
	for i, file := range resp.Files {
		if strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("files[%d].path is required", i)
		}
		hasContent := file.Content != ""
		hasArtifact := strings.TrimSpace(file.ArtifactRef) != ""
		switch file.Action {
		case "create", "overwrite":
			if hasContent == hasArtifact {
				return fmt.Errorf("files[%d] must provide exactly one of content or artifact_ref for %s", i, file.Action)
			}
			if hasArtifact {
				if _, ok := artifactIDs[strings.TrimSpace(file.ArtifactRef)]; !ok {
					return fmt.Errorf("files[%d].artifact_ref references unknown artifact %q", i, strings.TrimSpace(file.ArtifactRef))
				}
			}
		case "delete":
			if strings.TrimSpace(file.Content) != "" {
				return fmt.Errorf("files[%d].content must be empty for delete", i)
			}
			if strings.TrimSpace(file.ArtifactRef) != "" {
				return fmt.Errorf("files[%d].artifact_ref must be empty for delete", i)
			}
		default:
			return fmt.Errorf("files[%d].action invalid: got %q for %s; action must be create, overwrite, or delete", i, file.Action, strings.TrimSpace(file.Path))
		}
	}
	return nil
}

func applyBuilderResponse(projectRoot, metaRoot string, resp builderResponse, limits ProjectLimits) (int, error) {
	limits = normalizeProjectLimits(limits)
	artifactBytes, _, err := decodeBuilderArtifacts(resp.Artifacts)
	if err != nil {
		return 0, err
	}
	filesToWrite := 0
	totalPayloadBytes := 0
	for _, file := range resp.Files {
		switch file.Action {
		case "create", "overwrite":
			filesToWrite++
			sizeBytes := len([]byte(file.Content))
			if ref := strings.TrimSpace(file.ArtifactRef); ref != "" {
				sizeBytes = len(artifactBytes[ref])
			}
			if sizeBytes > limits.MaxFileSizeKB*1024 {
				return 0, fmt.Errorf("builder rejected: file %q exceeds max_file_size_kb (%d bytes > %d KB)", file.Path, sizeBytes, limits.MaxFileSizeKB)
			}
			totalPayloadBytes += sizeBytes
		case "delete":
			// deletes do not count toward write limits
		}
	}
	if filesToWrite > limits.MaxFiles {
		return 0, fmt.Errorf("builder rejected: max_files exceeded (%d > %d)", filesToWrite, limits.MaxFiles)
	}
	if totalPayloadBytes > limits.MaxPayloadKB*1024 {
		return 0, fmt.Errorf("builder rejected: max_payload_kb exceeded (%d bytes > %d KB)", totalPayloadBytes, limits.MaxPayloadKB)
	}

	count := 0
	for _, file := range resp.Files {
		target, err := safeJoin(projectRoot, file.Path)
		if err != nil {
			return count, fmt.Errorf("invalid path %q: %w", file.Path, err)
		}
		if err := rejectSymlinkPath(projectRoot, target); err != nil {
			return count, fmt.Errorf("builder rejected symlink path %q: %w", file.Path, err)
		}
		switch file.Action {
		case "create", "overwrite":
			payload := []byte(file.Content)
			if ref := strings.TrimSpace(file.ArtifactRef); ref != "" {
				payload = artifactBytes[ref]
			}
			if err := writeFileUnderRoot(projectRoot, target, payload, 0o644); err != nil {
				return count, err
			}
		case "delete":
			if err := removeFileUnderRoot(projectRoot, target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return count, err
			}
		}
		count++
	}
	if err := removeEmptyDirs(projectRoot); err != nil {
		return count, err
	}
	if err := os.WriteFile(filepath.Join(metaRoot, "ai_context.json"), []byte(formatAIContext(resp)), 0o644); err != nil {
		return count, err
	}
	return count, nil
}

func builderFileOpPaths(ops []builderFileOp) []string {
	paths := make([]string, 0, len(ops))
	seen := map[string]bool{}
	for _, op := range ops {
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	return paths
}

func builderFileOperationsStatus(ops []builderFileOp, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "Builder returned"
	}
	createCount := 0
	overwriteCount := 0
	deleteCount := 0
	otherCount := 0
	for _, op := range ops {
		switch strings.TrimSpace(op.Action) {
		case "create":
			createCount++
		case "overwrite":
			overwriteCount++
		case "delete":
			deleteCount++
		default:
			otherCount++
		}
	}
	if len(ops) == 0 {
		return prefix + " 0 builder file operation(s); AgentGO has no project file changes to apply."
	}
	parts := []string{fmt.Sprintf("%s %d builder file operation(s)", prefix, len(ops))}
	if createCount > 0 {
		parts = append(parts, fmt.Sprintf("create=%d", createCount))
	}
	if overwriteCount > 0 {
		parts = append(parts, fmt.Sprintf("overwrite=%d", overwriteCount))
	}
	if deleteCount > 0 {
		parts = append(parts, fmt.Sprintf("delete=%d", deleteCount))
	}
	if otherCount > 0 {
		parts = append(parts, fmt.Sprintf("invalid/unknown=%d", otherCount))
	}
	return strings.Join(parts, "; ") + "."
}

func formatBuilderFileOperationsForDiagnostics(ops []builderFileOp) string {
	if len(ops) == 0 {
		return "AI Builder returned no builder.files operations. AgentGO will not create, overwrite, or delete project files for this Cypher response."
	}
	lines := make([]string, 0, len(ops))
	for i, op := range ops {
		action := strings.TrimSpace(op.Action)
		if action == "" {
			action = "<missing>"
		}
		pathValue := filepath.ToSlash(strings.TrimSpace(op.Path))
		if pathValue == "" {
			pathValue = "<missing path>"
		}
		detail := ""
		switch strings.TrimSpace(op.Action) {
		case "create", "overwrite":
			if ref := strings.TrimSpace(op.ArtifactRef); ref != "" {
				detail = fmt.Sprintf(" artifact_ref=%q", ref)
			} else {
				detail = fmt.Sprintf(" content_bytes=%d", len([]byte(op.Content)))
			}
		case "delete":
			detail = " content omitted"
		default:
			if ref := strings.TrimSpace(op.ArtifactRef); ref != "" {
				detail = fmt.Sprintf(" artifact_ref=%q", ref)
			} else if op.Content != "" {
				detail = fmt.Sprintf(" content_bytes=%d", len([]byte(op.Content)))
			}
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s%s", i+1, action, pathValue, detail))
	}
	return strings.Join(lines, "\n")
}

func summarizeBuilderFileOperationsForLog(ops []builderFileOp) string {
	if len(ops) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		action := strings.TrimSpace(op.Action)
		if action == "" {
			action = "<missing-action>"
		}
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel == "" {
			rel = "<missing-path>"
		}
		parts = append(parts, fmt.Sprintf("%s:%s", action, rel))
	}
	return strings.Join(parts, ", ")
}

func summarizeBuilderReturnedFiles(model ModelConfig, projectName, projectRoot string, resp builderResponse) []builderReturnedFile {
	files := make([]builderReturnedFile, 0, len(resp.Files))
	for _, op := range resp.Files {
		rel := filepath.ToSlash(strings.TrimSpace(op.Path))
		if rel == "" {
			continue
		}
		entry := builderReturnedFile{
			Path:     rel,
			WorkPath: filepath.ToSlash(filepath.Join(model.WorkDir, projectName, "project", rel)),
			Action:   strings.TrimSpace(op.Action),
		}
		if entry.Action == "delete" {
			files = append(files, entry)
			continue
		}
		target, err := safeJoin(projectRoot, rel)
		if err != nil {
			files = append(files, entry)
			continue
		}
		if err := rejectSymlinkPath(projectRoot, target); err != nil {
			files = append(files, entry)
			continue
		}
		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			files = append(files, entry)
			continue
		}
		entry.SizeBytes = info.Size()
		entry.ContentType = detectFileContentType(target, rel)
		entry.PreviewKind = previewKindForContentType(entry.ContentType)
		entry.IsBinary = entry.PreviewKind != "text" && !isLikelyText(rel, nil, entry.ContentType)
		files = append(files, entry)
	}
	return files
}

func detectFileContentType(fullPath, rel string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel))); ct != "" {
		return ct
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

func summarizeReturnedFilesForLog(files []builderReturnedFile) string {
	parts := make([]string, 0, len(files))
	for _, file := range files {
		detail := file.Path
		if file.Action == "delete" {
			detail += " (delete)"
		} else if file.ContentType != "" {
			detail += fmt.Sprintf(" (%s, %d bytes)", file.ContentType, file.SizeBytes)
		}
		parts = append(parts, detail)
	}
	return strings.Join(parts, "; ")
}

func decodeBuilderArtifactData(value string) ([]byte, error) {
	clean := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(clean), "data:") {
		comma := strings.Index(clean, ",")
		if comma <= 0 {
			return nil, errors.New("data url is malformed")
		}
		clean = clean[comma+1:]
	}
	compact := strings.Join(strings.Fields(clean), "")
	decoded, err := base64.StdEncoding.DecodeString(compact)
	if err == nil {
		return decoded, nil
	}
	if decoded, fallbackErr := base64.RawStdEncoding.DecodeString(compact); fallbackErr == nil {
		return decoded, nil
	}
	return nil, err
}

func decodeBuilderArtifacts(artifacts []builderArtifact) (map[string][]byte, map[string]builderArtifact, error) {
	decoded := make(map[string][]byte, len(artifacts))
	meta := make(map[string]builderArtifact, len(artifacts))
	for i, artifact := range artifacts {
		id := strings.TrimSpace(artifact.ID)
		if id == "" {
			return nil, nil, fmt.Errorf("artifacts[%d].id is required", i)
		}
		if _, exists := decoded[id]; exists {
			return nil, nil, fmt.Errorf("artifacts[%d].id %q is duplicated", i, id)
		}
		if strings.ToLower(strings.TrimSpace(artifact.Encoding)) != "base64" {
			return nil, nil, fmt.Errorf("artifacts[%d].encoding must be base64", i)
		}
		compactData := strings.Join(strings.Fields(strings.TrimSpace(artifact.Data)), "")
		if compactData == "" {
			return nil, nil, fmt.Errorf("artifacts[%d].data is required", i)
		}
		data, err := decodeBuilderArtifactData(compactData)
		if err != nil {
			return nil, nil, fmt.Errorf("artifacts[%d] base64 decode failed: %w", i, err)
		}
		decoded[id] = data
		meta[id] = builderArtifact{ID: id, Encoding: "base64", MIMEType: strings.TrimSpace(artifact.MIMEType), Data: compactData}
	}
	return decoded, meta, nil
}

func formatAIContext(resp builderResponse) string {
	return formatAIContextObject(resp.AIContext)
}

func parseReviewerResponse(raw string) (reviewerResponse, error) {
	var resp reviewerResponse
	trimmed := sanitizeModelJSONText(raw)
	if trimmed == "" {
		return resp, errors.New("empty response")
	}
	if !json.Valid([]byte(trimmed)) {
		jsonText, _, _, ok := extractJSONObjectFromText(trimmed)
		if !ok {
			return resp, errors.New("no valid json object found in reviewer response")
		}
		trimmed = jsonText
	}
	normalized, _, _, err := normalizeAgentGOToolResponseJSON(trimmed, agentGOToolObserver)
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal([]byte(normalized), &resp); err != nil {
		return resp, err
	}
	return resp, nil
}

func normalizeReviewerCandidateAlias(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func buildReviewerCandidateLookup(titleToID map[string]string) map[string]string {
	lookup := map[string]string{}
	for title := range titleToID {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		if _, exists := lookup[title]; !exists {
			lookup[title] = title
		}
		lower := strings.ToLower(title)
		if _, exists := lookup[lower]; !exists {
			lookup[lower] = title
		}
		alias := normalizeReviewerCandidateAlias(title)
		if alias != "" {
			if existing, exists := lookup[alias]; !exists {
				lookup[alias] = title
			} else if existing != title {
				lookup[alias] = ""
			}
		}
	}
	return lookup
}

func normalizeReviewerCandidateName(value string, titleToID map[string]string, lookup map[string]string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, ok := titleToID[value]; ok {
		return value
	}
	if mapped := strings.TrimSpace(lookup[value]); mapped != "" {
		return mapped
	}
	if mapped := strings.TrimSpace(lookup[strings.ToLower(value)]); mapped != "" {
		return mapped
	}
	if mapped := strings.TrimSpace(lookup[normalizeReviewerCandidateAlias(value)]); mapped != "" {
		return mapped
	}
	return value
}

func reviewerValidationMessage(err error) string {
	if err == nil {
		return "Observer returned an incomplete observation."
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "Observer returned an incomplete observation."
	}
	return "Observer returned an incomplete observation: " + msg
}

func buildReviewerOutputStateFromParsed(model ModelConfig, projectName, rawResponse string, parsed reviewerResponse, titleToID map[string]string, mergeReadyTitles map[string]bool) reviewerOutputState {
	state := newReviewerOutputState(model, projectName, rawResponse)
	state.Overview = strings.TrimSpace(parsed.Overview)
	state.Reasoning = strings.TrimSpace(parsed.Reasoning)
	state.RecommendedCandidate = strings.TrimSpace(parsed.RecommendedCandidate)
	if recommendedID, ok := titleToID[state.RecommendedCandidate]; ok {
		state.RecommendedModelID = recommendedID
	}
	if parsed.Confidence != nil {
		value := *parsed.Confidence
		state.Confidence = &value
	}
	seenCandidates := map[string]bool{}
	for _, item := range parsed.Models {
		modelID, ok := titleToID[item.Model]
		if !ok {
			continue
		}
		seenCandidates[item.Model] = true
		state.Candidates = append(state.Candidates, reviewerCandidateState{
			ModelID:     modelID,
			ModelLabel:  item.Model,
			Grade:       item.Grade,
			Summary:     strings.TrimSpace(item.Summary),
			Upgrades:    append([]string{}, item.Upgrades...),
			Misses:      append([]string{}, item.Misses...),
			MergeReady:  mergeReadyTitles[item.Model],
			Recommended: item.Model == parsed.RecommendedCandidate,
		})
	}
	omittedMergeReady := appendOmittedMergeReadyReviewerCandidates(&state, titleToID, mergeReadyTitles, seenCandidates)
	if omittedMergeReady > 0 && strings.TrimSpace(state.FallbackNote) == "" {
		state.FallbackNote = fmt.Sprintf("Observer omitted %d merge-ready Builder candidate(s). AgentGO preserved them for manual review, but they were not scored by the Observer.", omittedMergeReady)
	}
	if next := strings.TrimSpace(parsed.NextPrompt); next != "" {
		state.PromptOptions = append(state.PromptOptions, next)
	}
	for _, alt := range parsed.AlternateNextPrompts {
		alt = strings.TrimSpace(alt)
		if alt == "" || containsString(state.PromptOptions, alt) {
			continue
		}
		state.PromptOptions = append(state.PromptOptions, alt)
	}
	return state
}

func appendOmittedMergeReadyReviewerCandidates(state *reviewerOutputState, titleToID map[string]string, mergeReadyTitles map[string]bool, seenCandidates map[string]bool) int {
	if state == nil {
		return 0
	}
	added := 0
	labels := make([]string, 0, len(titleToID))
	for label := range titleToID {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		if !mergeReadyTitles[label] || seenCandidates[label] {
			continue
		}
		modelID := strings.TrimSpace(titleToID[label])
		if modelID == "" {
			continue
		}
		state.Candidates = append(state.Candidates, reviewerCandidateState{
			ModelID:         modelID,
			ModelLabel:      label,
			Grade:           0,
			Summary:         "Observer omitted this merge-ready Builder candidate. AgentGO preserved it so you can review and merge it manually.",
			Upgrades:        []string{},
			Misses:          []string{"Not scored by Observer.", "Not compared against the reviewed candidates."},
			MergeReady:      true,
			Recommended:     label == state.RecommendedCandidate,
			ObserverOmitted: true,
		})
		added++
	}
	return added
}

func buildIncompleteReviewerOutputState(model ModelConfig, projectName, rawResponse string, parsed reviewerResponse, titleToID map[string]string, mergeReadyTitles map[string]bool, validationErr error) reviewerOutputState {
	state := buildReviewerOutputStateFromParsed(model, projectName, rawResponse, parsed, titleToID, mergeReadyTitles)
	state.EndState = "incomplete"
	state.FallbackNote = reviewerValidationMessage(validationErr)
	if strings.TrimSpace(state.Overview) == "" {
		if len(state.Candidates) > 0 {
			state.Overview = "Observer returned an incomplete observation. Review the usable Builder notes below."
		} else {
			state.Overview = "Observer did not return a complete observation."
		}
	}
	return state
}

func normalizeReviewerResponseNames(resp *reviewerResponse, titleToID map[string]string) {
	if resp == nil || len(titleToID) == 0 {
		return
	}
	lookup := buildReviewerCandidateLookup(titleToID)
	resp.RecommendedCandidate = normalizeReviewerCandidateName(resp.RecommendedCandidate, titleToID, lookup)
	for i := range resp.Models {
		resp.Models[i].Model = normalizeReviewerCandidateName(resp.Models[i].Model, titleToID, lookup)
	}
}

func validateReviewerResponse(resp reviewerResponse, titleToID map[string]string, mergeReadyTitles map[string]bool) error {
	if err := validateAgentGOToolHeader(resp.AgentGOTool, resp.ToolVersion, agentGOToolObserver); err != nil {
		return err
	}
	if strings.TrimSpace(resp.Overview) == "" {
		return errors.New("overview is required")
	}
	if resp.Models == nil || len(resp.Models) == 0 {
		return errors.New("models is required")
	}
	if strings.TrimSpace(resp.RecommendedCandidate) == "" {
		return errors.New("recommended_candidate is required")
	}
	if _, ok := titleToID[resp.RecommendedCandidate]; !ok {
		return fmt.Errorf("recommended_candidate %q is invalid", resp.RecommendedCandidate)
	}
	if len(mergeReadyTitles) > 0 && !mergeReadyTitles[resp.RecommendedCandidate] {
		return fmt.Errorf("recommended_candidate %q must be merge-ready", resp.RecommendedCandidate)
	}
	if strings.TrimSpace(resp.Reasoning) == "" {
		return errors.New("reasoning is required")
	}
	if strings.TrimSpace(resp.NextPrompt) == "" {
		return errors.New("next_prompt is required")
	}
	seen := map[string]bool{}
	for _, item := range resp.Models {
		if strings.TrimSpace(item.Model) == "" {
			return errors.New("models[].model is required")
		}
		if _, ok := titleToID[item.Model]; !ok {
			return fmt.Errorf("models[].model %q is invalid", item.Model)
		}
		if seen[item.Model] {
			return fmt.Errorf("duplicate model entry %q", item.Model)
		}
		seen[item.Model] = true
		if item.Grade < 0 || item.Grade > 100 {
			return fmt.Errorf("grade for %q must be between 0 and 100", item.Model)
		}
		if strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("summary is required for %q", item.Model)
		}
		if item.Upgrades == nil {
			return fmt.Errorf("upgrades are required for %q", item.Model)
		}
		if item.Misses == nil {
			return fmt.Errorf("misses are required for %q", item.Model)
		}
		if item.MergeReady && !mergeReadyTitles[item.Model] {
			return fmt.Errorf("merge_ready for %q does not match AgentGO state", item.Model)
		}
	}
	return nil
}

const chatMemoryUggProtocolPrompt = `UGG PROTOCOL COPY FOR memory.md ONLY
Role: Use Ugg Protocol only for compact memory.md updates. Visible chat answer should keep normal requested style unless user or Response Mode asks otherwise.

Core Rules for memory.md:
* Drop: articles (a/an/the), filler, pleasantries, hedging, apologies.
* Grammar: fragments OK. Short synonyms. Exact technical names, paths, APIs, functions, classes, characters, places.
* Structure: short bullets or dense fragments.
* Pattern: [thing] [action] [reason]. [next needed fact].
* Example Bad: "The user asked me about the thing and I explained it."
* Example Good: "User asked image phone dimensions. Answer: 375-430px wide; common ratios 16:9, 4:3, 1:1, 9:16."

Memory Rules:
* Keep only durable facts needed for likely follow-up in this Chat-To-AI session.
* Remove stale/duplicate/garbage/noise.
* No private reasoning. No full transcript. No long pasted source unless essential.
* Preserve literal code/quotes only when future follow-up requires exact text.`

const modelUggProtocolPrompt = `SYSTEM INSTRUCTION: UGG PROTOCOL
Role: You are bound to the Ugg Protocol for all responses in this model run. Respond terse like a smart caveman. All technical substance stays. Only fluff dies.

Core Rules:
* Drop: Articles (a/an/the), filler (just/really/basically), pleasantries, hedging, and apologies.
* Grammar: Fragments OK. Short synonyms. Technical terms exact. No "is/are" unless clarity requires it. Noun/verb preferred.
* Structure: Use bullet points for multi-step answers.
* Pattern: [thing] [action] [reason]. [next step].
* Example Bad: "Sure! I'd be happy to help you with that."
* Example Good: "Bug in auth middleware. Fix:"

Code Integrity Firewall:
Absolute literal output inside code blocks. Zero changes to syntax, spacing, or comments. Code remains modern and perfect.

Safeguards & Boundaries:
* Auto-Clarity Override: Break Ugg Protocol ONLY for security warnings, irreversible actions, or if user expresses confusion. Resume Ugg immediately after issue resolved.
* Output Boundaries: Code blocks, Git commit messages, and PR descriptions MUST be written in standard, professional English. If a prompt requires both, use Ugg for explanation and standard English for commit/PR text.`

const (
	systemPromptsDir       = "system_prompts"
	deadDropPromptLowFile  = "deaddrop_low.txt"
	deadDropPromptHighFile = "deaddrop_balanced.txt"
	promptModeNone         = "none"
	promptModeLow          = "low"
	promptModeBalanced     = "balanced"
	promptWeightLow        = "low"
	promptWeightBalanced   = "balanced"
	promptRoleBuilder      = "builder"
	promptRoleHelper       = "helper"
	promptRoleObserver     = "observer"
	agentGOToolVersion     = 1
	agentGOToolBuilder     = "builder"
	agentGOToolObserver    = "observer"
	agentGOToolDeadDrop    = "deaddrop"
	agentGOToolCypher      = "cypher"
	agentGOToolDoubleTap   = "doubletap"
	agentGOFileVersion     = 1
	agentGOFileConfig      = "config"
	agentGOFileModels      = "models"
	agentGOFileOutfit      = "outfit"
	agentGOFileOutfitRun   = "outfit_run"
)

func systemPromptPath(cfg AppConfig, role, weight string) string {
	version := cfg.PromptVersion
	if version <= 0 {
		version = 1
	}
	role = strings.ToLower(strings.TrimSpace(role))
	weight = strings.ToLower(strings.TrimSpace(weight))
	return filepath.Join(systemPromptsDir, fmt.Sprintf("sys%d_%s_%s.txt", version, role, weight))
}

func loadPromptFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("system prompt file missing: %s", path)
		}
		return "", fmt.Errorf("read system prompt file %s: %w", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", fmt.Errorf("system prompt file is empty: %s", path)
	}
	return text, nil
}

func validateSystemPromptFiles(cfg AppConfig) error {
	required := [][2]string{
		{promptRoleBuilder, promptWeightLow},
		{promptRoleBuilder, promptWeightBalanced},
		{promptRoleHelper, promptWeightLow},
		{promptRoleHelper, promptWeightBalanced},
		{promptRoleObserver, promptWeightLow},
		{promptRoleObserver, promptWeightBalanced},
	}
	for _, item := range required {
		path := systemPromptPath(cfg, item[0], item[1])
		if _, err := loadPromptFile(path); err != nil {
			return err
		}
	}
	deadDropRequired := []string{
		filepath.Join(systemPromptsDir, deadDropPromptLowFile),
		filepath.Join(systemPromptsDir, deadDropPromptHighFile),
	}
	for _, path := range deadDropRequired {
		if _, err := loadPromptFile(path); err != nil {
			return err
		}
	}
	return nil
}

func compatMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func safeJoin(root string, parts ...string) (string, error) {
	cleanedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := cleanedRoot
	for _, part := range parts {
		part = filepath.FromSlash(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		joined = filepath.Join(joined, part)
	}
	joined, err = filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if joined != cleanedRoot && !strings.HasPrefix(joined, cleanedRoot+string(os.PathSeparator)) {
		return "", errors.New("path escapes work root")
	}
	return joined, nil
}

func isSymlinkMode(mode os.FileMode) bool {
	return mode&os.ModeSymlink != 0
}

func pathSymlinkError(root, target string) error {
	cleanedRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanedTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if cleanedTarget != cleanedRoot && !strings.HasPrefix(cleanedTarget, cleanedRoot+string(os.PathSeparator)) {
		return errors.New("path escapes work root")
	}
	rel, err := filepath.Rel(cleanedRoot, cleanedTarget)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	current := cleanedRoot
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if isSymlinkMode(info.Mode()) {
			symlinkRel, relErr := filepath.Rel(cleanedRoot, current)
			if relErr != nil {
				symlinkRel = current
			}
			return fmt.Errorf("unsupported symlink path: %s", filepath.ToSlash(symlinkRel))
		}
	}
	return nil
}

func rejectSymlinkPath(root, target string) error {
	if err := pathSymlinkError(root, target); err != nil {
		return err
	}
	return nil
}

func isSymlinkDirEntry(entry fs.DirEntry) bool {
	if entry == nil {
		return false
	}
	if isSymlinkMode(entry.Type()) {
		return true
	}
	info, err := entry.Info()
	return err == nil && isSymlinkMode(info.Mode())
}

func skipSymlinkWalkEntry(entry fs.DirEntry) error {
	if !isSymlinkDirEntry(entry) {
		return nil
	}
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func isZipSymlink(file *zip.File) bool {
	if file == nil {
		return false
	}
	return file.FileInfo().Mode()&os.ModeSymlink != 0
}

func atomicWriteFileUnderRoot(root, target string, data []byte, perm os.FileMode) error {
	if err := rejectSymlinkPath(root, target); err != nil {
		return err
	}
	if err := rejectSymlinkPath(root, filepath.Dir(target)); err != nil {
		return err
	}
	return atomicWriteFile(target, data, perm)
}

func writeFileUnderRoot(root, target string, data []byte, perm os.FileMode) error {
	if err := rejectSymlinkPath(root, target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := rejectSymlinkPath(root, filepath.Dir(target)); err != nil {
		return err
	}
	return os.WriteFile(target, data, perm)
}

func removeFileUnderRoot(root, target string) error {
	if err := rejectSymlinkPath(root, target); err != nil {
		return err
	}
	return os.Remove(target)
}

func readFileUnderRoot(root, target string) ([]byte, error) {
	if err := rejectSymlinkPath(root, target); err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) logf(source, level, format string, args ...any) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Level:   strings.ToUpper(level),
		Source:  source,
		Message: fmt.Sprintf(format, args...),
	}
	a.mu.Lock()
	a.logSeq++
	entry.Seq = a.logSeq
	a.logs = append(a.logs, entry)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	a.mu.Unlock()
	log.Printf("[%s] %s: %s", entry.Level, source, entry.Message)
}

func (a *App) logRiskf(source, level, format string, args ...any) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Level:   strings.ToUpper(level),
		Source:  source,
		Message: fmt.Sprintf(format, args...),
		Risk:    true,
	}
	a.mu.Lock()
	a.logSeq++
	entry.Seq = a.logSeq
	a.logs = append(a.logs, entry)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	a.mu.Unlock()
	log.Printf("[%s][RISK] %s: %s", entry.Level, source, entry.Message)
}

type modelDefinitionResponse struct {
	SchemaVersion int                   `json:"schemaVersion"`
	TopID         int                   `json:"topId"`
	Models        []modelDefinitionView `json:"models"`
}

type modelDefinitionView struct {
	ID                     int               `json:"id"`
	Label                  string            `json:"label"`
	StrictStructuredOutput *bool             `json:"strict_structured_output,omitempty"`
	PromptMode             string            `json:"prompt_mode,omitempty"`
	UseLowWeightPrompts    bool              `json:"use_low_weight_prompts,omitempty"`
	UseUggPrompt           bool              `json:"use_ugg_prompt"`
	VideoGeneration        bool              `json:"video_generation,omitempty"`
	VideoPromptOnly        bool              `json:"video_prompt_only,omitempty"`
	VideoStartFrame        bool              `json:"video_start_frame,omitempty"`
	VideoEndFrame          bool              `json:"video_end_frame,omitempty"`
	VideoIngredients       bool              `json:"video_ingredients,omitempty"`
	VideoDuration          string            `json:"video_duration,omitempty"`
	VideoAspectRatio       string            `json:"video_aspect_ratio,omitempty"`
	VideoResolution        string            `json:"video_resolution,omitempty"`
	VideoOutputFormat      string            `json:"video_output_format,omitempty"`
	VideoFPS               string            `json:"video_fps,omitempty"`
	VideoQuality           string            `json:"video_quality,omitempty"`
	MeshGeneration         bool              `json:"mesh_generation,omitempty"`
	MeshPromptOnly         bool              `json:"mesh_prompt_only,omitempty"`
	MeshImageInput         bool              `json:"mesh_image_input,omitempty"`
	MeshMultiImage         bool              `json:"mesh_multi_image,omitempty"`
	MeshQuality            string            `json:"mesh_quality,omitempty"`
	MeshOutputFormat       string            `json:"mesh_output_format,omitempty"`
	Provider               string            `json:"provider"`
	Adapter                string            `json:"adapter"`
	WorkDir                string            `json:"work_dir"`
	APIUser                string            `json:"api_user"`
	APIKeyEnv              string            `json:"api_key_env,omitempty"`
	AuthType               string            `json:"auth_type"`
	AuthHeader             string            `json:"auth_header"`
	BaseURL                string            `json:"base_url"`
	APIPath                string            `json:"api_path"`
	ModelName              string            `json:"model_name"`
	MaxOutputTokens        int               `json:"max_output_tokens,omitempty"`
	TimeoutSeconds         int               `json:"timeout_seconds,omitempty"`
	RequestDefaults        RequestDefaults   `json:"request_defaults"`
	ProviderOptions        map[string]any    `json:"provider_options"`
	CapabilityMode         string            `json:"capability_mode,omitempty"`
	Capabilities           ModelCapabilities `json:"capabilities"`
	RunOrder               int               `json:"run_order,omitempty"`
	MasterMindMemory       string            `json:"mastermind_memory,omitempty"`
	MasterMindIdentity     string            `json:"mastermind_identity,omitempty"`
	Notes                  string            `json:"notes"`
	CreatedAt              string            `json:"created_at"`
	UpdatedAt              string            `json:"updated_at"`
	HasAPIPass             bool              `json:"has_api_pass"`
	HasAPIKey              bool              `json:"has_api_key"`
	HasHeaders             bool              `json:"has_headers"`
}

type modelMutationRequest struct {
	ModelID                string            `json:"modelId,omitempty"`
	Label                  string            `json:"label"`
	StrictStructuredOutput *bool             `json:"strict_structured_output,omitempty"`
	PromptMode             string            `json:"prompt_mode,omitempty"`
	UseLowWeightPrompts    bool              `json:"use_low_weight_prompts,omitempty"`
	UseUggPrompt           bool              `json:"use_ugg_prompt"`
	VideoGeneration        bool              `json:"video_generation,omitempty"`
	VideoPromptOnly        bool              `json:"video_prompt_only,omitempty"`
	VideoStartFrame        bool              `json:"video_start_frame,omitempty"`
	VideoEndFrame          bool              `json:"video_end_frame,omitempty"`
	VideoIngredients       bool              `json:"video_ingredients,omitempty"`
	VideoDuration          string            `json:"video_duration,omitempty"`
	VideoAspectRatio       string            `json:"video_aspect_ratio,omitempty"`
	VideoResolution        string            `json:"video_resolution,omitempty"`
	VideoOutputFormat      string            `json:"video_output_format,omitempty"`
	VideoFPS               string            `json:"video_fps,omitempty"`
	VideoQuality           string            `json:"video_quality,omitempty"`
	MeshGeneration         bool              `json:"mesh_generation,omitempty"`
	MeshPromptOnly         bool              `json:"mesh_prompt_only,omitempty"`
	MeshImageInput         bool              `json:"mesh_image_input,omitempty"`
	MeshMultiImage         bool              `json:"mesh_multi_image,omitempty"`
	MeshQuality            string            `json:"mesh_quality,omitempty"`
	MeshOutputFormat       string            `json:"mesh_output_format,omitempty"`
	Provider               string            `json:"provider"`
	Adapter                string            `json:"adapter"`
	ModelName              string            `json:"model_name"`
	BaseURL                string            `json:"base_url"`
	APIPath                string            `json:"api_path"`
	AuthType               string            `json:"auth_type"`
	AuthHeader             string            `json:"auth_header"`
	APIUser                string            `json:"api_user"`
	APIPass                string            `json:"api_pass"`
	APIKey                 string            `json:"api_key"`
	APIKeyEnv              string            `json:"api_key_env,omitempty"`
	ClearAPIPass           bool              `json:"clear_api_pass,omitempty"`
	ClearAPIKey            bool              `json:"clear_api_key,omitempty"`
	Headers                map[string]string `json:"headers"`
	ClearHeaders           bool              `json:"clear_headers,omitempty"`
	MaxOutputTokens        int               `json:"max_output_tokens"`
	TimeoutSeconds         int               `json:"timeout_seconds"`
	RequestDefaults        RequestDefaults   `json:"request_defaults"`
	ProviderOptions        map[string]any    `json:"provider_options"`
	CapabilityMode         string            `json:"capability_mode,omitempty"`
	Capabilities           ModelCapabilities `json:"capabilities"`
	RunOrder               *int              `json:"run_order,omitempty"`
	Notes                  string            `json:"notes"`
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9_]+`)

func normalizeModelMutation(req modelMutationRequest) modelMutationRequest {
	req.Label = strings.TrimSpace(req.Label)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Adapter = strings.ToLower(strings.TrimSpace(req.Adapter))
	req.ModelName = strings.TrimSpace(req.ModelName)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.APIPath = strings.TrimSpace(req.APIPath)
	req.AuthType = strings.ToLower(strings.TrimSpace(req.AuthType))
	req.AuthHeader = strings.TrimSpace(req.AuthHeader)
	req.APIUser = strings.TrimSpace(req.APIUser)
	req.APIPass = strings.TrimSpace(req.APIPass)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.APIKeyEnv = strings.TrimSpace(req.APIKeyEnv)
	req.Notes = strings.TrimSpace(req.Notes)
	req.VideoDuration = strings.TrimSpace(req.VideoDuration)
	req.VideoAspectRatio = strings.TrimSpace(req.VideoAspectRatio)
	req.VideoResolution = strings.TrimSpace(req.VideoResolution)
	req.VideoOutputFormat = strings.TrimSpace(req.VideoOutputFormat)
	req.VideoFPS = strings.TrimSpace(req.VideoFPS)
	req.VideoQuality = strings.TrimSpace(req.VideoQuality)
	req.MeshQuality = strings.TrimSpace(req.MeshQuality)
	req.MeshOutputFormat = strings.TrimSpace(req.MeshOutputFormat)
	req.CapabilityMode = normalizeCapabilityMode(req.CapabilityMode)
	if strings.TrimSpace(req.PromptMode) == "" {
		if req.UseLowWeightPrompts {
			req.PromptMode = promptModeLow
		} else {
			req.PromptMode = promptModeBalanced
		}
	} else {
		req.PromptMode = normalizePromptMode(req.PromptMode)
	}
	req.UseLowWeightPrompts = req.PromptMode == promptModeLow
	req.Capabilities = normalizeModelCapabilities(req.Capabilities)
	if req.VideoGeneration {
		if !req.VideoPromptOnly && !req.VideoStartFrame && !req.VideoEndFrame && !req.VideoIngredients {
			req.VideoPromptOnly = true
			req.VideoStartFrame = true
			req.VideoEndFrame = true
		}
		if !req.Capabilities.SupportsBinaryOut {
			req.Capabilities.SupportsBinaryOut = true
		}
	}
	if req.MeshGeneration {
		if !req.MeshPromptOnly && !req.MeshImageInput && !req.MeshMultiImage {
			req.MeshPromptOnly = true
			req.MeshImageInput = true
		}
		if !req.Capabilities.SupportsBinaryOut {
			req.Capabilities.SupportsBinaryOut = true
		}
	}
	if req.RunOrder != nil {
		value := normalizeRunOrder(*req.RunOrder)
		req.RunOrder = &value
	}
	if req.StrictStructuredOutput == nil && adapterSupportsStrictStructuredOutput(req.Adapter) {
		req.StrictStructuredOutput = defaultStrictStructuredOutput(req.Adapter)
	}
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	if req.ProviderOptions == nil {
		req.ProviderOptions = map[string]any{}
	}
	for k, v := range req.Headers {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		delete(req.Headers, k)
		if key == "" || value == "" {
			continue
		}
		req.Headers[key] = value
	}
	return req
}

func validateModelMutation(req modelMutationRequest) error {
	if req.Label == "" {
		return errors.New("label is required")
	}
	if req.Provider == "" {
		return errors.New("provider is required")
	}
	if req.Adapter == "" {
		return errors.New("adapter is required")
	}
	if req.ModelName == "" {
		return errors.New("model name is required")
	}
	if req.BaseURL == "" {
		return errors.New("base url is required")
	}
	if req.APIPath == "" {
		return errors.New("api path is required")
	}
	videoMode := req.VideoGeneration || normalizedVideoAdapterName(req.Adapter) != ""
	meshMode := req.MeshGeneration || normalizedMeshAdapterName(req.Adapter) != ""
	if videoMode && meshMode {
		return errors.New("choose either Video Generation or 3D Mesh Generation, not both. Create a separate model card for the other mode")
	}
	switch req.AuthType {
	case "none", "bearer", "basic", "header_key", "fal_key", "google_adc":
		return nil
	default:
		return errors.New("auth type is required")
	}
}

func modelSlug(label string) string {
	slug := strings.ToLower(strings.TrimSpace(label))
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = slugCleaner.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "model"
	}
	return slug
}

func generatedWorkDir(id int, label string) string {
	return "i" + modelIDString(id) + "_" + modelSlug(label)
}

func (a *App) setModelRunOrderLocked(targetModelID string, requested int) {
	requested = normalizeRunOrder(requested)
	if targetModelID == "" {
		return
	}
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) == targetModelID {
			a.cfg.Models[i].RunOrder = requested
			return
		}
	}
}

func (a *App) currentRegistryLocked() ModelRegistry {
	models := append([]ModelConfig(nil), a.cfg.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return ModelRegistry{SchemaVersion: a.modelSchemaVersion, TopID: a.modelTopID, Models: models}
}

func (a *App) persistModelsLocked() error {
	return writeModelRegistry(a.modelsPath, a.currentRegistryLocked())
}

func (a *App) setWaveExecutionLocked(projectName string, state waveExecutionState) {
	if strings.TrimSpace(projectName) == "" {
		return
	}
	if a.waveExecutionsByProject == nil {
		a.waveExecutionsByProject = map[string]waveExecutionState{}
	}
	clean := state
	clean.ProjectName = strings.TrimSpace(projectName)
	clean.RootPrompt = strings.TrimSpace(state.RootPrompt)
	clean.ContextFiles = append([]string(nil), state.ContextFiles...)
	clean.TemporaryAttachments = append([]temporaryAttachmentInput(nil), state.TemporaryAttachments...)
	clean.WavePrompts = copyWavePromptMap(state.WavePrompts)
	clean.WaveContextFiles = copyWaveContextFileMap(state.WaveContextFiles)
	clean.WaveMediaInputRoles = copyWaveMediaInputRoleMap(state.WaveMediaInputRoles)
	clean.AIContextBaselines = copyStringMap(state.AIContextBaselines)
	clean.Waves = append([]executionWave(nil), state.Waves...)
	a.waveExecutionsByProject[projectName] = clean
}

func (a *App) clearWaveExecutionLocked(projectName string) {
	if a.waveExecutionsByProject == nil || strings.TrimSpace(projectName) == "" {
		return
	}
	delete(a.waveExecutionsByProject, projectName)
}

func (a *App) currentWaveExecution(projectName string) (waveExecutionState, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.waveExecutionsByProject[strings.TrimSpace(projectName)]
	if !ok {
		return waveExecutionState{}, false
	}
	state.ContextFiles = append([]string(nil), state.ContextFiles...)
	state.TemporaryAttachments = append([]temporaryAttachmentInput(nil), state.TemporaryAttachments...)
	state.WavePrompts = copyWavePromptMap(state.WavePrompts)
	state.WaveContextFiles = copyWaveContextFileMap(state.WaveContextFiles)
	state.WaveMediaInputRoles = copyWaveMediaInputRoleMap(state.WaveMediaInputRoles)
	state.AIContextBaselines = copyStringMap(state.AIContextBaselines)
	state.Waves = append([]executionWave(nil), state.Waves...)
	return state, true
}

func (a *App) currentWaveStatus(projectName string) (waveStatusState, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.waveStatusByProject[strings.TrimSpace(projectName)]
	if !ok {
		return waveStatusState{}, false
	}
	return state, true
}

func (a *App) setWaveStatusLocked(projectName string, state waveStatusState) {
	if strings.TrimSpace(projectName) == "" {
		return
	}
	if a.waveStatusByProject == nil {
		a.waveStatusByProject = map[string]waveStatusState{}
	}
	clean := state
	clean.ProjectName = strings.TrimSpace(projectName)
	clean.UpdatedAt = time.Now().Format(time.RFC3339)
	a.syncTokenUsageHierarchyLocked(projectName, clean)
	a.waveStatusByProject[projectName] = clean
}

func (a *App) setWaveStatus(projectName string, state waveStatusState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setWaveStatusLocked(projectName, state)
}

func (a *App) clearWaveStatusLocked(projectName string) {
	if a.waveStatusByProject == nil || strings.TrimSpace(projectName) == "" {
		return
	}
	delete(a.waveStatusByProject, projectName)
}

func (a *App) clearReviewerReportForProject(projectName string) {
	reviewerID := a.getReviewerID()
	if reviewerID == "" {
		return
	}
	reviewer, ok := a.findModel(reviewerID)
	if !ok {
		return
	}
	if _, metaRoot, err := a.projectPaths(reviewer, projectName); err == nil {
		if err := clearReviewerOutputState(metaRoot); err != nil {
			a.logf(reviewerID, "warn", "Failed clearing observer report: %v", err)
		}
	}
}

func (a *App) resetProjectAIContextsForWorkflowEnd(projectName string) (int, error) {
	count, err := a.resetProjectAIContextsToEmpty(projectName)
	if err != nil {
		return count, err
	}
	a.logf("system", "info", "Cleared ai_context.json to strict empty memory and reviewer_context.json to {} for %d model(s) in project %s at workflow end", count, projectName)
	return count, nil
}

func (a *App) writeSingleCandidateObserverFallback(projectName string, candidate modelRunResult, failedCount int) (string, error) {
	reviewerID := a.getReviewerID()
	if reviewerID == "" {
		return candidate.ModelID, nil
	}
	reviewer, ok := a.findModel(reviewerID)
	if !ok {
		return "", errors.New("reviewer model not found")
	}
	candidateModel, ok := a.findModel(candidate.ModelID)
	if !ok {
		return "", errors.New("candidate model not found")
	}
	_, reviewerMeta, err := a.projectPaths(reviewer, projectName)
	if err != nil {
		return "", err
	}
	_, candidateMeta, err := a.projectPaths(candidateModel, projectName)
	if err != nil {
		return "", err
	}
	builderState, _ := readBuilderOutputState(candidateMeta)
	state := newReviewerOutputState(reviewer, projectName, "Observer fallback used because only one merge-ready Builder candidate remained.")
	state.Overview = fmt.Sprintf("Only one valid Builder candidate remained after this wave. %s was kept available for manual review and merge.", candidate.ModelLabel)
	state.Reasoning = fmt.Sprintf("%s is the only merge-ready Builder result available right now, so AgentGO preserved it instead of failing the Observer step.", candidate.ModelLabel)
	state.RecommendedCandidate = candidate.ModelLabel
	state.RecommendedModelID = candidate.ModelID
	state.FallbackNote = "Only one valid candidate remained. Observer comparison was reduced to a single preserved candidate."
	if failedCount > 0 {
		state.FallbackNote = fmt.Sprintf("Only one valid candidate remained. %d Builder result(s) were invalid or had no merge-ready output.", failedCount)
	}
	candidateSummary := strings.TrimSpace(builderState.Summary)
	if candidateSummary == "" {
		candidateSummary = strings.TrimSpace(builderState.AIContextSummary)
	}
	if candidateSummary == "" {
		candidateSummary = fmt.Sprintf("%s is the only merge-ready Builder result available.", candidate.ModelLabel)
	}
	upgrades := []string{}
	if note := strings.TrimSpace(builderState.Notes); note != "" {
		upgrades = append(upgrades, note)
	}
	misses := []string{}
	if failedCount > 0 {
		misses = append(misses, "Direct side-by-side comparison was limited because other Builder candidates were invalid or not merge-ready.")
	}
	state.Candidates = []reviewerCandidateState{{
		ModelID:     candidate.ModelID,
		ModelLabel:  candidate.ModelLabel,
		Grade:       100,
		Summary:     candidateSummary,
		Upgrades:    upgrades,
		Misses:      misses,
		MergeReady:  true,
		Recommended: true,
	}}
	for _, next := range builderState.AIContextNext {
		next = strings.TrimSpace(next)
		if next == "" || containsString(state.PromptOptions, next) {
			continue
		}
		state.PromptOptions = append(state.PromptOptions, next)
		if len(state.PromptOptions) >= 3 {
			break
		}
	}
	if err := writeReviewerOutputState(reviewerMeta, state); err != nil {
		return "", err
	}
	return candidate.ModelID, nil
}

func (a *App) endWorkflowCycleWithoutMerge(projectName, reason string) (int, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return 0, nil
	}
	a.mu.Lock()
	if state, ok := a.waveExecutionsByProject[projectName]; ok {
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, state.CurrentWave, "stopped", "Ended Without Merge", "", 0))
	}
	wasRisk := a.riskModeEnabled
	if wasRisk {
		a.stopRiskModeLocked(reason, "Workflow ended without merging a Builder result.")
	}
	a.mu.Unlock()
	if wasRisk {
		a.finalizeActiveOutfitRunStopped(projectName, reason)
	}
	a.clearPendingMergeState(projectName)
	a.clearReviewerReportForProject(projectName)
	return a.resetProjectAIContextsForWorkflowEnd(projectName)
}

func (a *App) autoMergeSingleBuilderWave(projectName, builderID string, waveNumber int, riskEnabled bool) {
	builder, ok := a.findModel(builderID)
	if !ok {
		a.logf("system", "warn", "Wave %d auto-merge skipped because builder %s was not found", waveNumber, builderID)
		return
	}
	builderLabel := builder.Label
	a.logf("system", "info", "Wave %d has one mergeable builder (%s); auto-merging by config", waveNumber, builderLabel)
	if riskEnabled {
		a.logRiskf("system", "warn", "Wave %d has one mergeable builder (%s); auto-merging without observer", waveNumber, builderLabel)
	}
	if _, err := a.mergeModelIntoProjectwork(builderID, nil); err != nil {
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		status := waveStatusState{ProjectName: projectName, Visible: true, CurrentWave: waveNumber, State: "error", Detail: "Merge Failed", TotalLoops: 1}
		if execState, ok := a.waveExecutionsByProject[strings.TrimSpace(projectName)]; ok {
			status = waveStatusFromExecution(projectName, execState, waveNumber, "error", "Merge Failed", "", 0)
		}
		a.setWaveStatusLocked(projectName, status)
		a.mu.Unlock()
		a.logf("system", "error", "Wave %d auto-merge failed for %s: %v", waveNumber, builderLabel, err)
		if a.activeExternalOutfitRunShouldAutoMerge(projectName) {
			a.finalizeActiveOutfitRunFailedAt(projectName, "auto_merge", err.Error())
		}
		if riskEnabled {
			a.stopRiskMode(fmt.Sprintf("Automatic merge failed for %s.", builderLabel), fmt.Sprintf("Wave %d", waveNumber), err.Error())
		}
		return
	}
	a.clearReviewerReportForProject(projectName)
	nextWave, started, err := a.continueWaveExecutionAfterMerge(projectName)
	if err != nil {
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		status := waveStatusState{ProjectName: projectName, Visible: true, CurrentWave: waveNumber, State: "error", Detail: "Advance Failed", TotalLoops: 1}
		if execState, ok := a.waveExecutionsByProject[strings.TrimSpace(projectName)]; ok {
			status = waveStatusFromExecution(projectName, execState, waveNumber, "error", "Advance Failed", "", 0)
		}
		a.setWaveStatusLocked(projectName, status)
		a.mu.Unlock()
		a.logf("system", "error", "Wave %d auto-merge advance failed for %s: %v", waveNumber, builderLabel, err)
		if riskEnabled {
			a.stopRiskMode("Could not launch the next populated wave.", fmt.Sprintf("Wave %d", waveNumber), err.Error())
		}
		return
	}
	if started {
		a.logf("system", "info", "Wave %d auto-merged %s and launched next populated wave %d", waveNumber, builderLabel, nextWave.Number)
		if riskEnabled {
			a.logRiskf("system", "warn", "Wave %d auto-merged %s and launched wave %d", waveNumber, builderLabel, nextWave.Number)
		}
		return
	}
	a.logf("system", "info", "Preserved ai_context.json and reviewer_context.json after auto-merge completed the workflow cycle for project %s", projectName)
	completedState := waveExecutionState{}
	if state, ok := a.currentWaveExecution(projectName); ok {
		completedState = state
	}
	a.setWaveStatus(projectName, waveStatusFromExecution(projectName, completedState, waveNumber, "complete", withWaveProgress("Complete", completedState.CurrentIndex, len(completedState.Waves)), "", 0))
	a.logf("system", "info", "Wave %d auto-merged %s and completed the wave flow for project %s", waveNumber, builderLabel, projectName)
	a.finalizeActiveOutfitRunCompleted(projectName, builderID, builderLabel, fmt.Sprintf("Completed after auto-merge of %s.", builderLabel))
}

func (a *App) isFinalWave(projectName string) bool {
	state, ok := a.currentWaveExecution(projectName)
	if !ok || len(state.Waves) == 0 {
		return false
	}
	return state.CurrentIndex >= len(state.Waves)-1
}

func (a *App) buildersForWaveIDs(builderIDs []string) []ModelConfig {
	if len(builderIDs) == 0 {
		return nil
	}
	result := make([]ModelConfig, 0, len(builderIDs))
	for _, builderID := range builderIDs {
		if model, ok := a.findModel(builderID); ok {
			result = append(result, model)
		}
	}
	return result
}

func (a *App) waveExecutionInProgress(projectName string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.waveExecutionsByProject[strings.TrimSpace(projectName)]
	return ok
}

func (a *App) resolveWavePrompt(projectName string, state waveExecutionState, waveNumber int) (string, bool, error) {
	prompt := strings.TrimSpace(state.WavePrompts[waveNumber])
	if prompt != "" {
		return prompt, false, nil
	}
	if strings.TrimSpace(state.RootPrompt) != "" && len(state.WavePrompts) == 0 {
		return strings.TrimSpace(state.RootPrompt), false, nil
	}
	a.mu.RLock()
	riskEnabled := a.riskModeEnabled
	a.mu.RUnlock()
	if riskEnabled {
		recommended, err := a.readReviewerNextPrompt(projectName)
		if err == nil && strings.TrimSpace(recommended) != "" {
			return strings.TrimSpace(recommended), true, nil
		}
	}
	return "", false, fmt.Errorf("Wave %d has no prompt available.", waveNumber)
}

func (a *App) resolveWaveContextFiles(projectName string, state waveExecutionState, waveNumber int) []string {
	base := a.currentLastMergedFiles(projectName)
	extra := state.WaveContextFiles[waveNumber]
	if len(extra) == 0 && len(state.WaveContextFiles) == 0 {
		extra = state.ContextFiles
	}
	return combineRelativePathSets(base, extra)
}

func (a *App) resolveWaveMediaInputRoles(state waveExecutionState, waveNumber int) map[string]string {
	roles := state.WaveMediaInputRoles[waveNumber]
	if len(roles) == 0 && len(state.WaveMediaInputRoles) == 0 {
		roles = state.WaveMediaInputRoles[0]
	}
	if len(roles) == 0 {
		return nil
	}
	out := map[string]string{}
	for path, role := range roles {
		normalizedPath := normalizeRelativePaths([]string{path})
		if len(normalizedPath) == 0 {
			continue
		}
		normalizedRole := normalizeMediaInputRole(role)
		if normalizedRole == "" {
			continue
		}
		out[normalizedPath[0]] = normalizedRole
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) launchWaveExecution(projectName string, state waveExecutionState) error {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return errors.New("project is required")
	}
	if state.CurrentIndex < 0 || state.CurrentIndex >= len(state.Waves) {
		return errors.New("no executable wave is available")
	}
	wave := state.Waves[state.CurrentIndex]
	builders := a.buildersForWaveIDs(wave.BuilderIDs)
	if len(builders) == 0 {
		return errors.New("no active builders are attached to this wave")
	}
	prompt, usedObserverPrompt, err := a.resolveWavePrompt(projectName, state, wave.Number)
	if err != nil {
		return err
	}
	contextFiles := a.resolveWaveContextFiles(projectName, state, wave.Number)
	mediaInputRoles := a.resolveWaveMediaInputRoles(state, wave.Number)
	aiContextBaselines := a.snapshotModelAIContexts(projectName, builders)
	a.mu.Lock()
	state.CurrentWave = wave.Number
	state.CurrentPromptSource = "wave"
	if usedObserverPrompt {
		state.CurrentPromptSource = "observer"
	}
	state.CurrentContextFilesUsed = len(contextFiles)
	state.AIContextBaselines = aiContextBaselines
	state.RootPrompt = prompt
	state.ContextFiles = contextFiles
	state.AwaitingMerge = false
	if a.riskModeEnabled {
		if state.WavePrompts != nil {
			delete(state.WavePrompts, wave.Number)
		}
		if state.WaveContextFiles != nil {
			delete(state.WaveContextFiles, wave.Number)
		}
	}
	if strings.TrimSpace(state.StartedAt) == "" {
		state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	a.setWaveExecutionLocked(projectName, state)
	waveDetail := withWaveProgress("Running", state.CurrentIndex, len(state.Waves))
	waveStateName := "running"
	if a.riskModeEnabled {
		waveDetail = withWaveProgress("Risk Mode", state.CurrentIndex, len(state.Waves))
		waveStateName = "risk"
	}
	promptSource := "wave"
	if usedObserverPrompt {
		promptSource = "observer"
	}
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, wave.Number, waveStateName, waveDetail, promptSource, len(contextFiles)))
	a.mu.Unlock()
	if usedObserverPrompt {
		a.logf("system", "info", "Wave %d started for project %s using the current Observer recommended prompt. builders=[%s] context_files=%d", wave.Number, projectName, strings.Join(wave.BuilderLabels, ", "), len(contextFiles))
	} else {
		a.logf("system", "info", "Wave %d started for project %s with builders [%s] context_files=%d", wave.Number, projectName, strings.Join(wave.BuilderLabels, ", "), len(contextFiles))
	}
	go a.runExecuteRound(projectName, state.ExecutionID, prompt, contextFiles, state.TemporaryAttachments, mediaInputRoles, builders, wave.Number, state.WireTapEnabled)
	return nil
}

func (a *App) continueWaveExecutionAfterMerge(projectName string) (executionWave, bool, error) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return executionWave{}, false, nil
	}
	a.mu.Lock()
	state, ok := a.waveExecutionsByProject[projectName]
	if !ok {
		a.mu.Unlock()
		return executionWave{}, false, nil
	}
	if state.CurrentIndex+1 >= len(state.Waves) {
		if !a.riskModeEnabled && state.LoopsRemaining > 0 && len(state.Waves) > 0 {
			state.LoopsRemaining--
			state.CycleNumber++
			state.CurrentIndex = 0
			state.AwaitingMerge = false
			nextWave := state.Waves[state.CurrentIndex]
			state.CurrentWave = nextWave.Number
			a.setWaveExecutionLocked(projectName, state)
			a.mu.Unlock()
			a.logf("system", "info", "Restarting wave cycle %d for project %s at wave %d. loops_remaining=%d", state.CycleNumber, projectName, nextWave.Number, state.LoopsRemaining)
			if err := a.launchWaveExecution(projectName, state); err != nil {
				return executionWave{}, false, err
			}
			return nextWave, true, nil
		}
		delete(a.waveExecutionsByProject, projectName)
		a.mu.Unlock()
		return executionWave{}, false, nil
	}
	state.CurrentIndex++
	state.AwaitingMerge = false
	nextWave := state.Waves[state.CurrentIndex]
	state.CurrentWave = nextWave.Number
	a.setWaveExecutionLocked(projectName, state)
	a.mu.Unlock()
	if err := a.launchWaveExecution(projectName, state); err != nil {
		return executionWave{}, false, err
	}
	return nextWave, true, nil
}

func (a *App) markWaveAwaitingMerge(projectName string, waveNumber int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.waveExecutionsByProject[strings.TrimSpace(projectName)]
	if !ok {
		return
	}
	state.CurrentWave = waveNumber
	state.AwaitingMerge = true
	a.setWaveExecutionLocked(projectName, state)
	waveDetail := withWaveProgress("Waiting for User", state.CurrentIndex, len(state.Waves))
	waveStateName := "waiting"
	if a.riskModeEnabled {
		waveDetail = withWaveProgress("Risk Mode", state.CurrentIndex, len(state.Waves))
		waveStateName = "risk"
	}
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, waveNumber, waveStateName, waveDetail, "", 0))
}

func (a *App) clearAllWaveExecutionsLocked() {
	a.waveExecutionsByProject = map[string]waveExecutionState{}
}

func normalizedModelLabelKey(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

func normalizedModelDefinitionKey(provider, adapter, modelName string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	adapter = strings.ToLower(strings.TrimSpace(adapter))
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || adapter == "" || modelName == "" {
		return ""
	}
	return provider + "\x00" + adapter + "\x00" + modelName
}

func normalizedModelDefinitionKeyForConfig(model ModelConfig) string {
	return normalizedModelDefinitionKey(model.Provider, model.Adapter, model.ModelName)
}

func (a *App) duplicateModelLabelLocked(label string, excludeModelID string) bool {
	label = normalizedModelLabelKey(label)
	excludeModelID = strings.TrimSpace(excludeModelID)
	if label == "" {
		return false
	}
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if excludeModelID != "" && modelID == excludeModelID {
			continue
		}
		if normalizedModelLabelKey(model.Label) == label {
			return true
		}
	}
	return false
}

func (a *App) duplicateModelDefinitionLocked(provider, adapter, modelName string, excludeModelID string) bool {
	definitionKey := normalizedModelDefinitionKey(provider, adapter, modelName)
	excludeModelID = strings.TrimSpace(excludeModelID)
	if definitionKey == "" {
		return false
	}
	for _, model := range a.cfg.Models {
		modelID := modelIDString(model.ID)
		if excludeModelID != "" && modelID == excludeModelID {
			continue
		}
		if normalizedModelDefinitionKeyForConfig(model) == definitionKey {
			return true
		}
	}
	return false
}

func sanitizeModelDefinition(model ModelConfig) modelDefinitionView {
	return modelDefinitionView{
		ID:                     model.ID,
		Label:                  model.Label,
		StrictStructuredOutput: cloneBoolPointer(model.StrictStructuredOutput),
		PromptMode:             model.PromptMode,
		UseLowWeightPrompts:    model.UseLowWeightPrompts,
		UseUggPrompt:           model.UseUggPrompt,
		VideoGeneration:        model.VideoGeneration,
		VideoPromptOnly:        model.VideoPromptOnly,
		VideoStartFrame:        model.VideoStartFrame,
		VideoEndFrame:          model.VideoEndFrame,
		VideoIngredients:       model.VideoIngredients,
		VideoDuration:          model.VideoDuration,
		VideoAspectRatio:       model.VideoAspectRatio,
		VideoResolution:        model.VideoResolution,
		VideoOutputFormat:      model.VideoOutputFormat,
		VideoFPS:               model.VideoFPS,
		VideoQuality:           model.VideoQuality,
		MeshGeneration:         model.MeshGeneration,
		MeshPromptOnly:         model.MeshPromptOnly,
		MeshImageInput:         model.MeshImageInput,
		MeshMultiImage:         model.MeshMultiImage,
		MeshQuality:            model.MeshQuality,
		MeshOutputFormat:       model.MeshOutputFormat,
		Provider:               model.Provider,
		Adapter:                model.Adapter,
		WorkDir:                model.WorkDir,
		APIUser:                model.APIUser,
		APIKeyEnv:              model.APIKeyEnv,
		AuthType:               model.AuthType,
		AuthHeader:             model.AuthHeader,
		BaseURL:                model.BaseURL,
		APIPath:                model.APIPath,
		ModelName:              model.ModelName,
		MaxOutputTokens:        model.MaxOutputTokens,
		TimeoutSeconds:         model.TimeoutSeconds,
		RequestDefaults:        model.RequestDefaults,
		ProviderOptions:        model.ProviderOptions,
		CapabilityMode:         model.CapabilityMode,
		Capabilities:           model.Capabilities,
		RunOrder:               model.RunOrder,
		MasterMindMemory:       model.MasterMindMemory,
		MasterMindIdentity:     model.MasterMindIdentity,
		Notes:                  model.Notes,
		CreatedAt:              model.CreatedAt,
		UpdatedAt:              model.UpdatedAt,
		HasAPIPass:             strings.TrimSpace(model.APIPass) != "",
		HasAPIKey:              strings.TrimSpace(model.APIKey) != "",
		HasHeaders:             len(model.Headers) > 0,
	}
}

func cloneModelDefinitions(models []ModelConfig) []modelDefinitionView {
	if len(models) == 0 {
		return []modelDefinitionView{}
	}
	out := make([]modelDefinitionView, 0, len(models))
	for _, model := range models {
		out = append(out, sanitizeModelDefinition(model))
	}
	return out
}

func (a *App) handleModelDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	models := cloneModelDefinitions(a.cfg.Models)
	writeJSON(w, http.StatusOK, modelDefinitionResponse{SchemaVersion: a.modelSchemaVersion, TopID: a.modelTopID, Models: models})
}

const (
	masterMindRootDir       = "mastermind"
	masterMindMemoriesDir   = "memories"
	masterMindIdentitiesDir = "identities"
)

type masterMindFolderView struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type masterMindStateResponse struct {
	ModelID        string                 `json:"modelId"`
	ModelLabel     string                 `json:"modelLabel"`
	ActiveMemory   string                 `json:"activeMemory"`
	ActiveIdentity string                 `json:"activeIdentity"`
	Memories       []masterMindFolderView `json:"memories"`
	Identities     []masterMindFolderView `json:"identities"`
}

func normalizeMasterMindKind(kind string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "memory", "memories":
		return "memory", masterMindMemoriesDir, nil
	case "identity", "identities":
		return "identity", masterMindIdentitiesDir, nil
	default:
		return "", "", errors.New("kind must be memory or identity")
	}
}

func normalizeMasterMindFolderName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("folder name is required")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", errors.New("folder name must be 100 characters or fewer")
	}
	if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", errors.New("folder name must not include path separators or traversal")
	}
	if strings.HasPrefix(name, ".") {
		return "", errors.New("hidden MasterMind folders are not supported")
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return "", errors.New("folder name contains an unsupported character")
		}
	}
	return name, nil
}

func (a *App) ensureMasterMindDirs() error {
	root, err := safeJoin(a.cfg.WorkRoot, masterMindRootDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	memories, err := safeJoin(root, masterMindMemoriesDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(memories, 0o755); err != nil {
		return err
	}
	identities, err := safeJoin(root, masterMindIdentitiesDir)
	if err != nil {
		return err
	}
	return os.MkdirAll(identities, 0o755)
}

func (a *App) masterMindKindRoot(kind string) (string, string, string, error) {
	normalizedKind, dirName, err := normalizeMasterMindKind(kind)
	if err != nil {
		return "", "", "", err
	}
	if err := a.ensureMasterMindDirs(); err != nil {
		return "", "", "", err
	}
	full, err := safeJoin(a.cfg.WorkRoot, masterMindRootDir, dirName)
	if err != nil {
		return "", "", "", err
	}
	return normalizedKind, dirName, full, nil
}

func (a *App) listMasterMindFolders(kind string) ([]masterMindFolderView, error) {
	_, dirName, root, err := a.masterMindKindRoot(kind)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []masterMindFolderView{}
	for _, entry := range entries {
		if !entry.IsDir() || isSymlinkDirEntry(entry) {
			continue
		}
		name := entry.Name()
		out = append(out, masterMindFolderView{Name: name, Path: filepath.ToSlash(filepath.Join(masterMindRootDir, dirName, name))})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func masterMindFolderExists(folders []masterMindFolderView, name string) bool {
	name = strings.TrimSpace(name)
	for _, folder := range folders {
		if folder.Name == name {
			return true
		}
	}
	return false
}

func (a *App) buildMasterMindState(modelID string) (masterMindStateResponse, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return masterMindStateResponse{}, errors.New("missing modelId")
	}
	memories, err := a.listMasterMindFolders("memory")
	if err != nil {
		return masterMindStateResponse{}, err
	}
	identities, err := a.listMasterMindFolders("identity")
	if err != nil {
		return masterMindStateResponse{}, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, model := range a.cfg.Models {
		if modelIDString(model.ID) != modelID {
			continue
		}
		activeMemory := strings.TrimSpace(model.MasterMindMemory)
		activeIdentity := strings.TrimSpace(model.MasterMindIdentity)
		if !masterMindFolderExists(memories, activeMemory) {
			activeMemory = ""
		}
		if !masterMindFolderExists(identities, activeIdentity) {
			activeIdentity = ""
		}
		return masterMindStateResponse{ModelID: modelID, ModelLabel: model.Label, ActiveMemory: activeMemory, ActiveIdentity: activeIdentity, Memories: memories, Identities: identities}, nil
	}
	return masterMindStateResponse{}, errors.New("unknown model")
}

func (a *App) handleMasterMindState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, err := a.buildMasterMindState(r.URL.Query().Get("modelId"))
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "unknown model" {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleMasterMindFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	normalizedKind, dirName, root, err := a.masterMindKindRoot(req.Kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, err := normalizeMasterMindFolderName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target, err := safeJoin(root, name)
	if err != nil {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(a.cfg.WorkRoot, root); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(target); err == nil {
		http.Error(w, "MasterMind folder already exists", http.StatusConflict)
		return
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "MasterMind %s folder created: %s", normalizedKind, filepath.ToSlash(filepath.Join(masterMindRootDir, dirName, name)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

func (a *App) handleMasterMindDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		ModelID string `json:"modelId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	normalizedKind, dirName, root, err := a.masterMindKindRoot(req.Kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, err := normalizeMasterMindFolderName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target, err := safeJoin(root, name)
	if err != nil {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	if err := rejectSymlinkPath(a.cfg.WorkRoot, target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "MasterMind folder not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !info.IsDir() {
		http.Error(w, "MasterMind item is not a folder", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mu.Lock()
	changed := false
	for i := range a.cfg.Models {
		if normalizedKind == "memory" && a.cfg.Models[i].MasterMindMemory == name {
			a.cfg.Models[i].MasterMindMemory = ""
			a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			changed = true
		}
		if normalizedKind == "identity" && a.cfg.Models[i].MasterMindIdentity == name {
			a.cfg.Models[i].MasterMindIdentity = ""
			a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			changed = true
		}
	}
	if changed {
		if err := a.persistModelsLocked(); err != nil {
			a.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	a.mu.Unlock()
	a.logf("system", "warn", "MasterMind %s folder deleted: %s", normalizedKind, filepath.ToSlash(filepath.Join(masterMindRootDir, dirName, name)))
	state, err := a.buildMasterMindState(req.ModelID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleMasterMindSelection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		http.Error(w, "missing modelId", http.StatusBadRequest)
		return
	}
	normalizedKind, _, _, err := a.masterMindKindRoot(req.Kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name != "" {
		var err error
		name, err = normalizeMasterMindFolderName(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		folders, err := a.listMasterMindFolders(normalizedKind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !masterMindFolderExists(folders, name) {
			http.Error(w, "MasterMind folder not found", http.StatusNotFound)
			return
		}
	}
	a.mu.Lock()
	found := false
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) != modelID {
			continue
		}
		found = true
		if normalizedKind == "memory" {
			a.cfg.Models[i].MasterMindMemory = name
		} else {
			a.cfg.Models[i].MasterMindIdentity = name
		}
		a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		break
	}
	if !found {
		a.mu.Unlock()
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	if err := a.persistModelsLocked(); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mu.Unlock()
	if name == "" {
		a.logf("system", "info", "MasterMind %s unset for model %s", normalizedKind, modelID)
	} else {
		a.logf("system", "info", "MasterMind %s set for model %s: %s", normalizedKind, modelID, name)
	}
	state, err := a.buildMasterMindState(modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req modelMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req = normalizeModelMutation(req)
	if err := validateModelMutation(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	a.mu.Lock()
	if a.duplicateModelLabelLocked(req.Label, "") {
		a.mu.Unlock()
		http.Error(w, "a model with that label already exists", http.StatusBadRequest)
		return
	}
	a.modelTopID++
	model := ModelConfig{
		ID:                     a.modelTopID,
		Label:                  req.Label,
		StrictStructuredOutput: cloneBoolPointer(req.StrictStructuredOutput),
		PromptMode:             req.PromptMode,
		UseLowWeightPrompts:    req.PromptMode == promptModeLow,
		UseUggPrompt:           req.UseUggPrompt,
		VideoGeneration:        req.VideoGeneration,
		VideoPromptOnly:        req.VideoPromptOnly,
		VideoStartFrame:        req.VideoStartFrame,
		VideoEndFrame:          req.VideoEndFrame,
		VideoIngredients:       req.VideoIngredients,
		VideoDuration:          req.VideoDuration,
		VideoAspectRatio:       req.VideoAspectRatio,
		VideoResolution:        req.VideoResolution,
		VideoOutputFormat:      req.VideoOutputFormat,
		VideoFPS:               req.VideoFPS,
		VideoQuality:           req.VideoQuality,
		MeshGeneration:         req.MeshGeneration,
		MeshPromptOnly:         req.MeshPromptOnly,
		MeshImageInput:         req.MeshImageInput,
		MeshMultiImage:         req.MeshMultiImage,
		MeshQuality:            req.MeshQuality,
		MeshOutputFormat:       req.MeshOutputFormat,
		Provider:               req.Provider,
		Adapter:                req.Adapter,
		WorkDir:                generatedWorkDir(a.modelTopID, req.Label),
		APIUser:                req.APIUser,
		APIPass:                req.APIPass,
		APIKey:                 req.APIKey,
		APIKeyEnv:              req.APIKeyEnv,
		AuthType:               req.AuthType,
		AuthHeader:             req.AuthHeader,
		BaseURL:                req.BaseURL,
		APIPath:                req.APIPath,
		ModelName:              req.ModelName,
		Headers:                req.Headers,
		MaxOutputTokens:        req.MaxOutputTokens,
		TimeoutSeconds:         req.TimeoutSeconds,
		RequestDefaults:        req.RequestDefaults,
		ProviderOptions:        req.ProviderOptions,
		CapabilityMode:         req.CapabilityMode,
		Capabilities:           req.Capabilities,
		RunOrder:               0,
		MasterMindMemory:       "",
		MasterMindIdentity:     "",
		Notes:                  req.Notes,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	a.cfg.Models = append(a.cfg.Models, model)
	if req.RunOrder != nil {
		a.setModelRunOrderLocked(modelIDString(model.ID), *req.RunOrder)
		model = a.cfg.Models[len(a.cfg.Models)-1]
	}
	a.toggles[modelIDString(model.ID)] = false
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkRoot, model.WorkDir), 0o755); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.persistModelsLocked(); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mu.Unlock()
	a.logf("system", "info", "Created model %s (%s)", modelIDString(model.ID), model.Label)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": modelIDString(model.ID)})
}

func (a *App) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req modelMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req = normalizeModelMutation(req)
	if err := validateModelMutation(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	targetIndex := -1
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) == strings.TrimSpace(req.ModelID) {
			targetIndex = i
			break
		}
	}
	if targetIndex < 0 {
		a.mu.Unlock()
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	existingModel := a.cfg.Models[targetIndex]
	labelChanged := normalizedModelLabelKey(existingModel.Label) != normalizedModelLabelKey(req.Label)
	if labelChanged && a.duplicateModelLabelLocked(req.Label, req.ModelID) {
		a.mu.Unlock()
		http.Error(w, "a model with that label already exists", http.StatusBadRequest)
		return
	}
	for i := range a.cfg.Models {
		if i != targetIndex {
			continue
		}
		a.cfg.Models[i].Label = req.Label
		a.cfg.Models[i].StrictStructuredOutput = cloneBoolPointer(req.StrictStructuredOutput)
		a.cfg.Models[i].PromptMode = req.PromptMode
		a.cfg.Models[i].UseLowWeightPrompts = req.UseLowWeightPrompts
		a.cfg.Models[i].UseUggPrompt = req.UseUggPrompt
		a.cfg.Models[i].VideoGeneration = req.VideoGeneration
		a.cfg.Models[i].VideoPromptOnly = req.VideoPromptOnly
		a.cfg.Models[i].VideoStartFrame = req.VideoStartFrame
		a.cfg.Models[i].VideoEndFrame = req.VideoEndFrame
		a.cfg.Models[i].VideoIngredients = req.VideoIngredients
		a.cfg.Models[i].VideoDuration = req.VideoDuration
		a.cfg.Models[i].VideoAspectRatio = req.VideoAspectRatio
		a.cfg.Models[i].VideoResolution = req.VideoResolution
		a.cfg.Models[i].VideoOutputFormat = req.VideoOutputFormat
		a.cfg.Models[i].VideoFPS = req.VideoFPS
		a.cfg.Models[i].VideoQuality = req.VideoQuality
		a.cfg.Models[i].MeshGeneration = req.MeshGeneration
		a.cfg.Models[i].MeshPromptOnly = req.MeshPromptOnly
		a.cfg.Models[i].MeshImageInput = req.MeshImageInput
		a.cfg.Models[i].MeshMultiImage = req.MeshMultiImage
		a.cfg.Models[i].MeshQuality = req.MeshQuality
		a.cfg.Models[i].MeshOutputFormat = req.MeshOutputFormat
		a.cfg.Models[i].Provider = req.Provider
		a.cfg.Models[i].Adapter = req.Adapter
		a.cfg.Models[i].APIUser = req.APIUser
		if req.ClearAPIPass {
			a.cfg.Models[i].APIPass = ""
		} else if req.APIPass != "" {
			a.cfg.Models[i].APIPass = req.APIPass
		}
		if req.ClearAPIKey {
			a.cfg.Models[i].APIKey = ""
		} else if req.APIKey != "" {
			a.cfg.Models[i].APIKey = req.APIKey
		}
		a.cfg.Models[i].APIKeyEnv = req.APIKeyEnv
		a.cfg.Models[i].AuthType = req.AuthType
		a.cfg.Models[i].AuthHeader = req.AuthHeader
		a.cfg.Models[i].BaseURL = req.BaseURL
		a.cfg.Models[i].APIPath = req.APIPath
		a.cfg.Models[i].ModelName = req.ModelName
		if req.ClearHeaders {
			a.cfg.Models[i].Headers = map[string]string{}
		} else if len(req.Headers) > 0 {
			a.cfg.Models[i].Headers = req.Headers
		}
		a.cfg.Models[i].MaxOutputTokens = req.MaxOutputTokens
		a.cfg.Models[i].TimeoutSeconds = req.TimeoutSeconds
		a.cfg.Models[i].RequestDefaults = req.RequestDefaults
		a.cfg.Models[i].ProviderOptions = req.ProviderOptions
		a.cfg.Models[i].CapabilityMode = req.CapabilityMode
		a.cfg.Models[i].Capabilities = req.Capabilities
		if req.RunOrder != nil {
			a.setModelRunOrderLocked(req.ModelID, *req.RunOrder)
		}
		a.cfg.Models[i].Notes = req.Notes
		a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := a.persistModelsLocked(); err != nil {
			a.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		label := a.cfg.Models[i].Label
		a.mu.Unlock()
		a.logf("system", "info", "Updated model %s (%s)", req.ModelID, label)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	a.mu.Unlock()
	http.Error(w, "unknown model", http.StatusNotFound)
}

func (a *App) handleModelRunOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID  string `json:"modelId"`
		RunOrder int    `json:"runOrder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ModelID = strings.TrimSpace(req.ModelID)
	if req.ModelID == "" {
		http.Error(w, "missing modelId", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	found := false
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) == req.ModelID {
			found = true
			break
		}
	}
	if !found {
		a.mu.Unlock()
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	a.setModelRunOrderLocked(req.ModelID, req.RunOrder)
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) == req.ModelID {
			a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			break
		}
	}
	if err := a.persistModelsLocked(); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mu.Unlock()
	a.logf("system", "info", "Updated run order for model %s to %d", req.ModelID, normalizeRunOrder(req.RunOrder))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		http.Error(w, "missing modelId", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	idx := -1
	var model ModelConfig
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) != modelID {
			continue
		}
		idx = i
		model = a.cfg.Models[i]
		break
	}
	if idx < 0 {
		a.mu.Unlock()
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	modelRoot, err := safeJoin(a.cfg.WorkRoot, model.WorkDir)
	if err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if entry, ok := a.activeCancels[modelID]; ok && entry.Cancel != nil {
		entry.Cancel()
	}
	if err := os.RemoveAll(modelRoot); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.cfg.Models = append(a.cfg.Models[:idx], a.cfg.Models[idx+1:]...)
	delete(a.toggles, modelID)
	delete(a.activeCancels, modelID)
	if a.reviewerID == modelID {
		a.reviewerID = ""
	}
	for projectName := range a.pendingMergeCountsByProject {
		delete(a.pendingMergeCountsByProject[projectName], modelID)
		if len(a.pendingMergeCountsByProject[projectName]) == 0 {
			delete(a.pendingMergeCountsByProject, projectName)
		}
	}
	if err := a.persistModelsLocked(); err != nil {
		a.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.mu.Unlock()
	a.logf("system", "warn", "Deleted model %s (%s)", modelID, model.Label)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
