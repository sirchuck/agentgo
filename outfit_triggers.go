package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type cronPreviewRequest struct {
	Cron string `json:"cron"`
}

type cronPreviewResponse struct {
	Valid      bool   `json:"valid"`
	Summary    string `json:"summary,omitempty"`
	NextRun    string `json:"nextRun,omitempty"`
	NextRunISO string `json:"nextRunIso,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	Error      string `json:"error,omitempty"`
}

type outfitTriggerUpdateRequest struct {
	ID                   string            `json:"id"`
	TimerEnabled         bool              `json:"timerEnabled"`
	TimerCron            string            `json:"timerCron"`
	TimerIterations      int               `json:"timerIterations"`
	WebhookEnabled       bool              `json:"webhookEnabled"`
	DeliveryMode         string            `json:"deliveryMode"`
	CallbackURL          string            `json:"callbackUrl"`
	CallbackHeaders      map[string]string `json:"callbackHeaders"`
	CallbackBearerToken  string            `json:"callbackBearerToken"`
	PayloadType          string            `json:"payloadType"`
	FileSetSelectorType  string            `json:"fileSetSelectorType"`
	FileSetSelectorValue string            `json:"fileSetSelectorValue"`
	DeadDropSourcePolicy string            `json:"deadDropSourcePolicy"`
	UseCypher            bool              `json:"useCypher"`
}

type webhookRunResponse struct {
	Status   string `json:"status"`
	OutfitID string `json:"outfitId,omitempty"`
	Message  string `json:"message,omitempty"`
}

type cronField struct {
	Min      int
	Max      int
	Wildcard bool
	Allowed  map[int]bool
}

type cronSchedule struct {
	Minute  cronField
	Hour    cronField
	Day     cronField
	Month   cronField
	Weekday cronField
	Expr    string
}

type executionSourceInfo struct {
	TriggerType         string
	OutfitID            string
	OutfitName          string
	DeadDropUploadReady bool
}

type outfitRuntimeInput struct {
	RuntimePrompt          string
	RuntimePromptField     string
	OriginalObjective      string
	DiagnosticsText        string
	DiagnosticsFingerprint string
	AdditionalContext      string
	PreviousRunID          string
	CycleNumber            int
	MaxCycles              int
	HasRuntimePrompt       bool
	HasDiagnostics         bool
}

func normalizeRuntimePayloadKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	return key
}

func parseOutfitRuntimeIntValue(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		raw := strings.TrimSpace(fmt.Sprint(v))
		n, _ := strconv.Atoi(raw)
		return n
	}
}

func diagnosticsFingerprint(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func stringifyOutfitRuntimeValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		encoded, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return strings.TrimSpace(string(encoded))
	}
}

func parseOutfitRuntimeInput(payload any) outfitRuntimeInput {
	runtime := outfitRuntimeInput{}
	rawMap, ok := payload.(map[string]any)
	if !ok || len(rawMap) == 0 {
		return runtime
	}
	values := map[string]any{}
	for key, value := range rawMap {
		values[normalizeRuntimePayloadKey(key)] = value
	}
	promptKeys := []string{"runtime_prompt", "objective", "runtime_objective", "feature_request", "prompt", "task_goal", "goal", "prompt_override"}
	for _, key := range promptKeys {
		if value, ok := values[key]; ok {
			if text := stringifyOutfitRuntimeValue(value); text != "" {
				runtime.RuntimePrompt = text
				runtime.RuntimePromptField = key
				runtime.HasRuntimePrompt = true
				break
			}
		}
	}
	diagnosticKeys := []string{"diagnostics", "diagnostic", "compile_errors", "compile_error", "errors", "error", "test_output", "build_output", "lint_output", "warnings"}
	diagnostics := []string{}
	for _, key := range diagnosticKeys {
		if value, ok := values[key]; ok {
			if text := stringifyOutfitRuntimeValue(value); text != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s:\n%s", key, text))
			}
		}
	}
	if len(diagnostics) > 0 {
		runtime.DiagnosticsText = strings.Join(diagnostics, "\n\n")
		runtime.HasDiagnostics = true
	}
	contextKeys := []string{"additional_context", "context", "notes", "note"}
	for _, key := range contextKeys {
		if value, ok := values[key]; ok {
			if text := stringifyOutfitRuntimeValue(value); text != "" {
				runtime.AdditionalContext = text
				break
			}
		}
	}
	objectiveKeys := []string{"original_objective", "initial_objective", "root_objective", "feature_goal"}
	for _, key := range objectiveKeys {
		if value, ok := values[key]; ok {
			if text := stringifyOutfitRuntimeValue(value); text != "" {
				runtime.OriginalObjective = text
				break
			}
		}
	}
	if runtime.OriginalObjective == "" && runtime.HasRuntimePrompt {
		runtime.OriginalObjective = runtime.RuntimePrompt
	}
	previousKeys := []string{"previous_run_id", "previous_run", "parent_run_id"}
	for _, key := range previousKeys {
		if value, ok := values[key]; ok {
			if text := stringifyOutfitRuntimeValue(value); text != "" {
				runtime.PreviousRunID = text
				break
			}
		}
	}
	cycleKeys := []string{"cycle_number", "cycle", "iteration"}
	for _, key := range cycleKeys {
		if value, ok := values[key]; ok {
			if n := parseOutfitRuntimeIntValue(value); n > 0 {
				runtime.CycleNumber = n
				break
			}
		}
	}
	maxCycleKeys := []string{"max_cycles", "max_cycle", "cycle_limit", "iteration_limit"}
	for _, key := range maxCycleKeys {
		if value, ok := values[key]; ok {
			if n := parseOutfitRuntimeIntValue(value); n > 0 {
				runtime.MaxCycles = n
				break
			}
		}
	}
	runtime.DiagnosticsFingerprint = diagnosticsFingerprint(runtime.DiagnosticsText)
	return runtime
}

func composeOutfitRuntimePrompt(savedPrompt string, runtime outfitRuntimeInput, cypherEnabled bool) string {
	savedPrompt = strings.TrimSpace(savedPrompt)
	if !runtime.HasRuntimePrompt && !runtime.HasDiagnostics && strings.TrimSpace(runtime.AdditionalContext) == "" {
		return savedPrompt
	}
	var b strings.Builder
	if runtime.HasRuntimePrompt {
		b.WriteString("You are an AgentGO Builder running from an external Outfit trigger.\n")
		if cypherEnabled {
			b.WriteString("Use Cypher first and request only the project files needed for the runtime objective.\n")
		}
		b.WriteString("Patch the project with minimal, targeted changes. Preserve unrelated behavior. Return changed files only. Do not claim compile/test success unless diagnostics or verifier results support it.\n\n")
		b.WriteString("RUNTIME OBJECTIVE:\n")
		b.WriteString(runtime.RuntimePrompt)
	} else {
		b.WriteString(savedPrompt)
	}
	if original := strings.TrimSpace(runtime.OriginalObjective); original != "" && original != strings.TrimSpace(runtime.RuntimePrompt) {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("ORIGINAL OBJECTIVE TO PRESERVE ACROSS REPAIR CYCLES:\n")
		b.WriteString(original)
	}
	if runtime.HasDiagnostics {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("LATEST DIAGNOSTICS / VERIFIER OUTPUT:\n")
		b.WriteString(runtime.DiagnosticsText)
	}
	if extra := strings.TrimSpace(runtime.AdditionalContext); extra != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("ADDITIONAL CONTEXT:\n")
		b.WriteString(extra)
	}
	return strings.TrimSpace(b.String())
}

func applyOutfitRuntimePrompt(outfit OutfitRecord, req executeRequest, runtime outfitRuntimeInput) executeRequest {
	if !runtime.HasRuntimePrompt && !runtime.HasDiagnostics && strings.TrimSpace(runtime.AdditionalContext) == "" {
		return req
	}
	prompts := map[string]string{}
	for key, value := range req.WavePrompts {
		prompts[key] = value
	}
	if len(prompts) == 0 {
		prompts["0"] = strings.TrimSpace(req.Prompt)
	}
	for key, saved := range prompts {
		prompts[key] = composeOutfitRuntimePrompt(saved, runtime, outfit.UseCypher)
	}
	req.WavePrompts = prompts
	firstKey := ""
	firstWave := 1 << 30
	for key := range prompts {
		wave := parseWaveMapKey(key)
		if wave < firstWave {
			firstWave = wave
			firstKey = key
		}
	}
	if firstKey != "" {
		req.Prompt = strings.TrimSpace(prompts[firstKey])
	}
	return req
}

func generateOutfitWebhookKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func parseCronField(expr string, minVal, maxVal int, fieldName string, allowSevenAsSunday bool) (cronField, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return cronField{}, fmt.Errorf("%s field is required", fieldName)
	}
	allowed := map[int]bool{}
	field := cronField{Min: minVal, Max: maxVal, Allowed: allowed}
	parts := strings.Split(expr, ",")
	for _, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return cronField{}, fmt.Errorf("%s field contains an empty segment", fieldName)
		}
		base := part
		step := 1
		if strings.Contains(part, "/") {
			bits := strings.Split(part, "/")
			if len(bits) != 2 {
				return cronField{}, fmt.Errorf("%s field has an invalid step segment", fieldName)
			}
			base = strings.TrimSpace(bits[0])
			stepText := strings.TrimSpace(bits[1])
			stepValue, err := strconv.Atoi(stepText)
			if err != nil || stepValue <= 0 {
				return cronField{}, fmt.Errorf("%s field has an invalid step value", fieldName)
			}
			step = stepValue
		}
		if base == "*" {
			field.Wildcard = true
			for value := minVal; value <= maxVal; value += step {
				allowed[value] = true
			}
			continue
		}
		if strings.Contains(base, "-") {
			rangeBits := strings.Split(base, "-")
			if len(rangeBits) != 2 {
				return cronField{}, fmt.Errorf("%s field has an invalid range", fieldName)
			}
			start, err := strconv.Atoi(strings.TrimSpace(rangeBits[0]))
			if err != nil {
				return cronField{}, fmt.Errorf("%s field has an invalid range start", fieldName)
			}
			end, err := strconv.Atoi(strings.TrimSpace(rangeBits[1]))
			if err != nil {
				return cronField{}, fmt.Errorf("%s field has an invalid range end", fieldName)
			}
			if allowSevenAsSunday {
				if start == 7 {
					start = 0
				}
				if end == 7 {
					end = 0
				}
			}
			if start < minVal || start > maxVal || end < minVal || end > maxVal {
				return cronField{}, fmt.Errorf("%s field range is out of bounds", fieldName)
			}
			if end < start {
				return cronField{}, fmt.Errorf("%s field range must increase", fieldName)
			}
			for value := start; value <= end; value += step {
				allowed[value] = true
			}
			continue
		}
		value, err := strconv.Atoi(base)
		if err != nil {
			return cronField{}, fmt.Errorf("%s field has an invalid value", fieldName)
		}
		if allowSevenAsSunday && value == 7 {
			value = 0
		}
		if value < minVal || value > maxVal {
			return cronField{}, fmt.Errorf("%s field value is out of bounds", fieldName)
		}
		allowed[value] = true
	}
	if len(allowed) == 0 {
		return cronField{}, fmt.Errorf("%s field does not match any values", fieldName)
	}
	return field, nil
}

func parseCronSchedule(expr string) (cronSchedule, error) {
	expr = strings.TrimSpace(strings.Join(strings.Fields(strings.TrimSpace(expr)), " "))
	if expr == "" {
		return cronSchedule{}, errors.New("cron schedule is required")
	}
	for _, r := range expr {
		if (r >= '0' && r <= '9') || r == '*' || r == ',' || r == '-' || r == '/' || r == ' ' {
			continue
		}
		return cronSchedule{}, fmt.Errorf("cron schedule contains an unsupported character: %q", string(r))
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronSchedule{}, errors.New("cron schedule must have exactly 5 fields: minute hour day month weekday")
	}
	minute, err := parseCronField(parts[0], 0, 59, "Minute", false)
	if err != nil {
		return cronSchedule{}, err
	}
	hour, err := parseCronField(parts[1], 0, 23, "Hour", false)
	if err != nil {
		return cronSchedule{}, err
	}
	day, err := parseCronField(parts[2], 1, 31, "Day", false)
	if err != nil {
		return cronSchedule{}, err
	}
	month, err := parseCronField(parts[3], 1, 12, "Month", false)
	if err != nil {
		return cronSchedule{}, err
	}
	weekday, err := parseCronField(parts[4], 0, 6, "Weekday", true)
	if err != nil {
		return cronSchedule{}, err
	}
	return cronSchedule{Minute: minute, Hour: hour, Day: day, Month: month, Weekday: weekday, Expr: expr}, nil
}

func fieldMatches(field cronField, value int) bool { return field.Allowed[value] }

func (s cronSchedule) matches(t time.Time) bool {
	if !fieldMatches(s.Minute, t.Minute()) || !fieldMatches(s.Hour, t.Hour()) || !fieldMatches(s.Month, int(t.Month())) {
		return false
	}
	domMatch := fieldMatches(s.Day, t.Day())
	dowMatch := fieldMatches(s.Weekday, int(t.Weekday()))
	if s.Day.Wildcard && s.Weekday.Wildcard {
		return domMatch && dowMatch
	}
	if s.Day.Wildcard {
		return dowMatch
	}
	if s.Weekday.Wildcard {
		return domMatch
	}
	return domMatch || dowMatch
}

func (s cronSchedule) nextRunAfter(now time.Time) (time.Time, error) {
	candidate := now.In(time.Local).Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(2, 0, 0)
	for !candidate.After(limit) {
		if s.matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, errors.New("no matching future run found within two years")
}

func describeCronField(label, expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "*" {
		return fmt.Sprintf("%s any", label)
	}
	return fmt.Sprintf("%s %s", label, expr)
}

func (s cronSchedule) summary() string {
	parts := strings.Fields(strings.TrimSpace(s.Expr))
	if len(parts) != 5 {
		return ""
	}
	return fmt.Sprintf("Local cron schedule — %s, %s, %s, %s, %s.",
		describeCronField("minute", parts[0]),
		describeCronField("hour", parts[1]),
		describeCronField("day", parts[2]),
		describeCronField("month", parts[3]),
		describeCronField("weekday", parts[4]),
	)
}

func blockingOutfitIssueMessages(issues []OutfitIssue) []string {
	messages := []string{}
	for _, issue := range issues {
		if issue.Blocking && strings.TrimSpace(issue.Message) != "" {
			messages = append(messages, strings.TrimSpace(issue.Message))
		}
	}
	return messages
}

func (a *App) configureRiskModeFromOutfit(outfit OutfitRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clearRiskModeLocked()
	if !outfit.RiskModeEnabled {
		return nil
	}
	if strings.TrimSpace(a.reviewerID) == "" {
		return errors.New("Risk Mode requires an Observer model in the Outfit")
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
		return errors.New("Risk Mode requires at least one active Builder model in the Outfit")
	}
	iterations := outfit.RiskModeIterations
	if iterations < 1 {
		iterations = 1
	}
	if max := a.cfg.RiskModeMaxIterations; max > 0 && iterations > max {
		return fmt.Errorf("Risk Mode iterations must be 1-%d", max)
	}
	firstPrompt := ""
	if len(outfit.WavePrompts) > 0 {
		waves := make([]int, 0, len(outfit.WavePrompts))
		for key := range outfit.WavePrompts {
			waves = append(waves, parseWaveMapKey(key))
		}
		sort.Ints(waves)
		if len(waves) > 0 {
			firstPrompt = strings.TrimSpace(outfit.WavePrompts[strconv.Itoa(waves[0])])
		}
	}
	a.riskModeEnabled = true
	a.riskIterationsTotal = iterations
	a.riskIterationsRemain = iterations
	a.riskOriginalPrompt = firstPrompt
	a.riskContextFiles = nil
	a.riskBuilderIDs = nil
	a.riskCurrentIteration = 1
	a.riskStopReason = ""
	a.setRiskStatusLocked("RISK MODE", fmt.Sprintf("Iteration 1 / %d", iterations), "Waiting for the Outfit trigger to start.")
	return nil
}

func mediaGenerationKind(model ModelConfig) string {
	video := modelIsVideoGeneration(model)
	mesh := modelIsMeshGeneration(model)
	if video && mesh {
		return "hybrid"
	}
	if video {
		return "video"
	}
	if mesh {
		return "mesh"
	}
	return ""
}

func uniformMediaGenerationKind(builders []ModelConfig) (string, error) {
	mediaKind := ""
	standardCount := 0
	for _, model := range builders {
		kind := mediaGenerationKind(model)
		switch kind {
		case "hybrid":
			return "", fmt.Errorf("%s is configured as both video and 3D mesh. Create separate model definitions for each media type.", model.Label)
		case "":
			standardCount++
		case "video", "mesh":
			if mediaKind == "" {
				mediaKind = kind
			} else if mediaKind != kind {
				return "", errors.New("3D Mesh and Video models cannot run together. Use only one media type for a run.")
			}
		}
	}
	if mediaKind != "" && standardCount > 0 {
		return "", errors.New("Media generation runs require uniform model types. Disable non-3D models before running a 3D Mesh task, or disable non-video models before running a Video task.")
	}
	return mediaKind, nil
}

func (a *App) startExecutionForCurrentConfig(projectName string, req executeRequest, source executionSourceInfo) (executeResponse, error, int) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ContextFiles = normalizeRelativePaths(req.ContextFiles)
	temporaryAttachments, tempErr := normalizeTemporaryAttachments(req.TemporaryAttachments)
	if tempErr != nil {
		return executeResponse{}, tempErr, http.StatusBadRequest
	}
	req.TemporaryAttachments = temporaryAttachments
	req.LoopCount = normalizeLoopCount(req.LoopCount)
	wavePrompts := normalizeWavePromptMap(req.WavePrompts)
	waveContextFiles := normalizeWaveContextFileMap(req.WaveContextFiles)
	waveMediaInputRoles := normalizeWaveMediaInputRoleMap(req.WaveMediaInputRoles)
	if len(wavePrompts) == 0 && req.Prompt != "" {
		wavePrompts = map[int]string{0: req.Prompt}
	}
	if len(waveContextFiles) == 0 && len(req.ContextFiles) > 0 {
		waveContextFiles = map[int][]string{0: req.ContextFiles}
	}
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return executeResponse{}, errors.New("Select an active project before executing a prompt."), http.StatusBadRequest
	}
	if a.waveExecutionInProgress(projectName) {
		a.logf("system", "warn", "Execute Prompt blocked for project %s: wave execution already in progress", projectName)
		return executeResponse{}, errors.New("A wave execution is already in progress for this project. Merge the current wave or press Emergency Stop before starting a new prompt."), http.StatusConflict
	}
	started, skipped, queued, allBuilders := []string{}, []string{}, []string{}, []ModelConfig{}
	a.mu.RLock()
	reviewerID := a.reviewerID
	riskEnabled := a.riskModeEnabled
	a.mu.RUnlock()
	for _, model := range a.cfg.Models {
		a.mu.RLock()
		enabled := a.toggles[modelIDString(model.ID)]
		a.mu.RUnlock()
		if modelIDString(model.ID) == reviewerID {
			skipped = append(skipped, model.Label+" (Reviewer)")
			continue
		}
		if !enabled {
			skipped = append(skipped, model.Label)
			continue
		}
		allBuilders = append(allBuilders, model)
	}
	promptPresent := strings.TrimSpace(req.Prompt) != "" || len(wavePrompts) > 0
	a.logf("system", "info", "Execute Prompt request received for project %s. prompt_present=%v active_builders=%d cypher=%v wiretap=%v doubletap=%v doubletap_count=%d context_files=%d temporary_attachments=%d waves=%d loops=%d reviewer=%s risk=%v", projectName, promptPresent, len(allBuilders), req.CypherEnabled, req.WireTapEnabled, req.DoubleTapEnabled, req.DoubleTapCount, len(req.ContextFiles), len(req.TemporaryAttachments), len(wavePrompts), req.LoopCount, reviewerID, riskEnabled)
	if len(allBuilders) == 0 {
		a.logf("system", "warn", "Execute Prompt blocked for project %s: no active Builder AI", projectName)
		return executeResponse{}, errors.New("Activate at least one Builder AI before executing a prompt."), http.StatusBadRequest
	}
	mediaKind, mediaErr := uniformMediaGenerationKind(allBuilders)
	if mediaErr != nil {
		a.logf("system", "warn", "Execute Prompt blocked for project %s: %v", projectName, mediaErr)
		return executeResponse{}, mediaErr, http.StatusBadRequest
	}
	deepModeCount := 0
	if req.CypherEnabled {
		deepModeCount++
	}
	if req.WireTapEnabled {
		deepModeCount++
	}
	if req.DoubleTapEnabled {
		deepModeCount++
	}
	if deepModeCount > 1 {
		err := errors.New("Only one deep mode can be active. Disable Cypher, WireTap, or DoubleTap so only one remains armed.")
		a.logf("system", "warn", "Execute Prompt blocked for project %s: %v", projectName, err)
		return executeResponse{}, err, http.StatusBadRequest
	}
	if req.WireTapEnabled && mediaKind != "" {
		err := errors.New("WireTap can only run with normal text Builder runs. Disable WireTap before running 3D Mesh or Video generation.")
		a.logf("system", "warn", "Execute Prompt blocked for project %s: %v", projectName, err)
		return executeResponse{}, err, http.StatusBadRequest
	}
	if req.WireTapEnabled {
		if err := a.ensureWireTapReadyForUse(projectName); err != nil {
			a.logf("system", "warn", "Execute Prompt blocked for project %s: WireTap not ready: %v", projectName, err)
			return executeResponse{}, err, http.StatusBadRequest
		}
	}
	if req.DoubleTapEnabled && mediaKind != "" {
		return executeResponse{}, errors.New("DoubleTap can only run with normal text Builder runs. Disable DoubleTap before running 3D Mesh or Video generation."), http.StatusBadRequest
	}
	if mediaKind != "" {
		if req.CypherEnabled {
			return executeResponse{}, errors.New("Cypher cannot run with 3D Mesh or Video generation. Disable Cypher or use non-media builders."), http.StatusBadRequest
		}
		if reviewerID != "" {
			return executeResponse{}, errors.New("Observer mode is not available for 3D Mesh or Video generation. Disable Observer mode before running media builders."), http.StatusBadRequest
		}
		if riskEnabled {
			return executeResponse{}, errors.New("Risk Mode is not available for 3D Mesh or Video generation. Disable Risk Mode before running media builders."), http.StatusBadRequest
		}
		if req.LoopCount > 0 {
			return executeResponse{}, errors.New("Loops are not available for 3D Mesh or Video generation. Set loops to 0 before running media builders."), http.StatusBadRequest
		}
	}
	if _, err := a.syncBuilderProjectsFromProjectwork(projectName, allBuilders); err != nil {
		return executeResponse{}, err, http.StatusInternalServerError
	}
	if req.CypherEnabled {
		a.logf("system", "info", "Execute Prompt entering Cypher path for project %s with active_builders=%d", projectName, len(allBuilders))
		return a.startCypherExecutionForCurrentConfig(projectName, req, source, allBuilders, skipped)
	}
	if req.DoubleTapEnabled {
		a.logf("system", "info", "Execute Prompt entering DoubleTap path for project %s with active_builders=%d", projectName, len(allBuilders))
		return a.startDoubleTapExecutionForCurrentConfig(projectName, req, source, allBuilders, skipped)
	}
	waves := buildExecutionWaves(allBuilders)
	if len(waves) == 0 {
		return executeResponse{}, errors.New("No populated waves were found. Activate at least one Builder AI and assign it a wave number."), http.StatusBadRequest
	}
	if mediaKind != "" {
		rootPrompt := strings.TrimSpace(req.Prompt)
		if rootPrompt == "" {
			rootPrompt = strings.TrimSpace(wavePrompts[0])
		}
		if rootPrompt == "" {
			for _, prompt := range wavePrompts {
				if trimmed := strings.TrimSpace(prompt); trimmed != "" {
					rootPrompt = trimmed
					break
				}
			}
		}
		rootContextFiles := append([]string(nil), req.ContextFiles...)
		if len(rootContextFiles) == 0 {
			rootContextFiles = append([]string(nil), waveContextFiles[0]...)
		}
		for _, wave := range waves {
			if strings.TrimSpace(wavePrompts[wave.Number]) == "" && rootPrompt != "" {
				wavePrompts[wave.Number] = rootPrompt
			}
			if len(waveContextFiles[wave.Number]) == 0 && len(rootContextFiles) > 0 {
				waveContextFiles[wave.Number] = append([]string(nil), rootContextFiles...)
			}
			if mediaKind == "video" && len(waveMediaInputRoles[wave.Number]) == 0 && len(waveMediaInputRoles[0]) > 0 {
				waveMediaInputRoles[wave.Number] = copyWaveMediaInputRoleMap(map[int]map[string]string{0: waveMediaInputRoles[0]})[0]
			}
		}
		if mediaKind == "video" {
			for waveNumber, roles := range waveMediaInputRoles {
				contextSet := map[string]bool{}
				for _, path := range waveContextFiles[waveNumber] {
					contextSet[path] = true
				}
				startAssigned := ""
				endAssigned := ""
				clean := map[string]string{}
				for path, role := range roles {
					normalizedRole := normalizeMediaInputRole(role)
					if normalizedRole == "" || !contextSet[path] {
						continue
					}
					if normalizedRole == "start_frame" {
						if startAssigned != "" && startAssigned != path {
							return executeResponse{}, fmt.Errorf("Wave %d has more than one start frame selected. Keep only one Start Frame.", waveNumber), http.StatusBadRequest
						}
						startAssigned = path
					}
					if normalizedRole == "end_frame" {
						if endAssigned != "" && endAssigned != path {
							return executeResponse{}, fmt.Errorf("Wave %d has more than one end frame selected. Keep only one End Frame.", waveNumber), http.StatusBadRequest
						}
						endAssigned = path
					}
					clean[path] = normalizedRole
				}
				if len(clean) > 0 {
					waveMediaInputRoles[waveNumber] = clean
				} else {
					delete(waveMediaInputRoles, waveNumber)
				}
			}
		}
	}
	for _, wave := range waves {
		if strings.TrimSpace(wavePrompts[wave.Number]) == "" {
			return executeResponse{}, fmt.Errorf("Wave %d is active but has no prompt.", wave.Number), http.StatusBadRequest
		}
	}
	firstWave := waves[0]
	firstWaveSet := map[string]bool{}
	for _, builderID := range firstWave.BuilderIDs {
		firstWaveSet[builderID] = true
	}
	for _, model := range allBuilders {
		if firstWaveSet[modelIDString(model.ID)] {
			started = append(started, model.Label)
		} else {
			queued = append(queued, model.Label)
		}
	}
	firstPrompt := strings.TrimSpace(wavePrompts[firstWave.Number])
	firstContextFiles := append([]string(nil), waveContextFiles[firstWave.Number]...)
	pendingIgnored := a.pendingMergeTotal(projectName)
	if pendingIgnored > 0 {
		a.logf("system", "warn", "Executing with unmerged results (ignored). pending_models=%d", pendingIgnored)
	}
	a.clearPendingMergeState(projectName)
	if clearedBuilderResponses, clearErr := a.clearAllBuilderResponseStatesForProject(projectName); clearErr != nil {
		a.logf("system", "warn", "Failed clearing stale Builder response cards before fresh Execute Prompt run: %v", clearErr)
	} else if clearedBuilderResponses > 0 {
		a.logf("system", "info", "Cleared %d stale Builder response card(s) before fresh Execute Prompt run for project %s", clearedBuilderResponses, projectName)
	}
	if clearedReviewerReports, clearErr := a.clearReviewerOutputStatesForProject(projectName); clearErr != nil {
		a.logf("system", "warn", "Failed clearing stale Observer report(s) before fresh Execute Prompt run: %v", clearErr)
	} else if clearedReviewerReports > 0 {
		a.logf("system", "info", "Cleared %d stale Observer report(s) before fresh Execute Prompt run for project %s", clearedReviewerReports, projectName)
	}
	state := waveExecutionState{ProjectName: projectName, ExecutionID: fmt.Sprintf("%s-%d", projectName, time.Now().UTC().UnixNano()), RootPrompt: firstPrompt, ContextFiles: firstContextFiles, TemporaryAttachments: req.TemporaryAttachments, WireTapEnabled: req.WireTapEnabled, WavePrompts: wavePrompts, WaveContextFiles: waveContextFiles, WaveMediaInputRoles: waveMediaInputRoles, Waves: waves, CurrentIndex: 0, CurrentWave: firstWave.Number, LoopCount: req.LoopCount, LoopsRemaining: req.LoopCount, CycleNumber: 1, AwaitingMerge: false, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	a.mu.Lock()
	if a.riskModeEnabled {
		state.LoopCount = 0
		state.LoopsRemaining = 0
	}
	a.setWaveExecutionLocked(projectName, state)
	if a.riskModeEnabled {
		builderIDs := make([]string, 0, len(allBuilders))
		for _, model := range allBuilders {
			builderIDs = append(builderIDs, modelIDString(model.ID))
		}
		a.riskBuilderIDs = builderIDs
		a.riskCurrentIteration = a.riskIterationDisplayLocked()
		a.riskStopReason = ""
		iterationText := fmt.Sprintf("Iteration %d / %d", a.riskCurrentIteration, a.riskIterationsTotal)
		builderText := fmt.Sprintf("Wave %d builders: %s", firstWave.Number, strings.Join(started, ", "))
		promptText := "Prompt: " + shortRiskText(firstPrompt, 96)
		statusLine := "Running first populated wave."
		if source.TriggerType == "timer" {
			statusLine = "Timer trigger started the first populated wave."
		} else if source.TriggerType == "webhook" {
			statusLine = "Webhook trigger started the first populated wave."
		}
		if strings.TrimSpace(source.OutfitName) != "" {
			statusLine = fmt.Sprintf("Outfit %s started the first populated wave.", source.OutfitName)
		}
		a.setRiskStatusLocked("RISK MODE", iterationText, statusLine, builderText, promptText)
	}
	a.mu.Unlock()
	if err := a.launchWaveExecution(projectName, state); err != nil {
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.mu.Unlock()
		return executeResponse{}, err, http.StatusBadRequest
	}
	effectiveContextFiles := combineRelativePathSets(a.currentLastMergedFiles(projectName), firstContextFiles)
	contextMode := "prompt_only"
	if len(effectiveContextFiles) > 0 {
		contextMode = "selected_context"
	}
	logPrefix := "Execute prompt clicked"
	if source.TriggerType == "timer" {
		logPrefix = fmt.Sprintf("Timer trigger started outfit %s (%s)", strings.TrimSpace(source.OutfitID), strings.TrimSpace(source.OutfitName))
	} else if source.TriggerType == "webhook" {
		logPrefix = fmt.Sprintf("Webhook trigger started outfit %s (%s)", strings.TrimSpace(source.OutfitID), strings.TrimSpace(source.OutfitName))
	}
	loopLogCount := state.LoopCount
	a.logf("system", "info", "%s for project %s. execution_context=%s context_files=%d first_wave=%d total_waves=%d loops=%d started=%d queued=%d skipped=%d reviewer=%s", logPrefix, projectName, contextMode, len(effectiveContextFiles), firstWave.Number, len(waves), loopLogCount, len(started), len(queued), len(skipped), reviewerID)
	a.mu.RLock()
	riskEnabled = a.riskModeEnabled
	currentIteration := a.riskCurrentIteration
	totalIterations := a.riskIterationsTotal
	a.mu.RUnlock()
	if riskEnabled {
		riskLogPrefix := "Execute started"
		if source.TriggerType == "timer" {
			riskLogPrefix = fmt.Sprintf("Timer started outfit %s", strings.TrimSpace(source.OutfitID))
		} else if source.TriggerType == "webhook" {
			riskLogPrefix = fmt.Sprintf("Webhook started outfit %s", strings.TrimSpace(source.OutfitID))
		}
		a.logRiskf("system", "warn", "RISK %d/%d: %s at wave %d with builders [%s]", currentIteration, totalIterations, riskLogPrefix, firstWave.Number, strings.Join(started, ", "))
	}
	return executeResponse{Started: started, Skipped: skipped, WaveStarted: firstWave.Number, TotalWaves: len(waves), RemainingWaves: remainingWaveNumbers(waves, 1), QueuedBuilders: queued, ContextFilesUsed: len(effectiveContextFiles)}, nil, http.StatusOK
}

func (a *App) startCypherExecutionForCurrentConfig(projectName string, req executeRequest, source executionSourceInfo, builders []ModelConfig, skipped []string) (executeResponse, error, int) {
	externalOutfitRun := source.TriggerType != "manual" && strings.TrimSpace(source.OutfitID) != ""
	rootPrompt := strings.TrimSpace(req.Prompt)
	a.mu.RLock()
	reviewerID := strings.TrimSpace(a.reviewerID)
	riskEnabled := a.riskModeEnabled
	a.mu.RUnlock()
	a.logf("system", "info", "Cypher Execute Prompt gate check for project %s. prompt_present=%v builders=%d reviewer=%s risk=%v", projectName, rootPrompt != "", len(builders), reviewerID, riskEnabled)
	block := func(message string, status int) (executeResponse, error, int) {
		err := errors.New(message)
		a.logf("system", "warn", "Cypher Execute Prompt blocked for project %s: %v", projectName, err)
		return executeResponse{}, err, status
	}
	if rootPrompt == "" {
		return block("Prompt is required before executing with Cypher.", http.StatusBadRequest)
	}
	if reviewerID != "" {
		return block("Cypher Action requires one active Builder and no conflicting modes/context/media inputs. Disable Observer/Reviewer mode and try again.", http.StatusBadRequest)
	}
	if riskEnabled {
		return block("Cypher Action requires one active Builder and no conflicting modes/context/media inputs. Disable Risk Mode and try again.", http.StatusBadRequest)
	}
	if a.waveExecutionInProgress(projectName) {
		return block("A run is already active for this project. Press Emergency Stop before starting a new Cypher prompt.", http.StatusConflict)
	}
	if len(builders) < 1 {
		return block("Must select at least one AI Builder", http.StatusBadRequest)
	}
	if len(builders) > 2 {
		return block("Only two maximum AI Builders allowed", http.StatusBadRequest)
	}
	if req.LoopCount > 0 {
		return block("Cypher Action cannot run with Loops. Set Loops to 0 and try again.", http.StatusBadRequest)
	}
	if req.WireTapEnabled || req.DoubleTapEnabled {
		return block("Cypher Action cannot run with WireTap or DoubleTap. Arm only Cypher and try again.", http.StatusBadRequest)
	}
	if len(req.ContextFiles) > 0 || len(req.TemporaryAttachments) > 0 {
		return block("Cypher Action requires no selected context files or media/temporary attachments. Clear context/media inputs and try again.", http.StatusBadRequest)
	}
	waveContextFiles := normalizeWaveContextFileMap(req.WaveContextFiles)
	for _, files := range waveContextFiles {
		if len(files) > 0 {
			return block("Cypher Action requires no selected wave context files. Clear context files and try again.", http.StatusBadRequest)
		}
	}
	workBuilder := builders[0]
	projectRoot, err := a.projectSettingsDir(projectName)
	if err != nil {
		return executeResponse{}, err, http.StatusInternalServerError
	}
	manifest, exists, err := readCypherManifest(filepath.Join(projectRoot, cypherManifestFileName))
	if err != nil {
		return block(fmt.Sprintf("Could not read Cypher builder selection: %v", err), http.StatusBadRequest)
	}
	if exists {
		if selected, ok := a.activeBuilderModelByID(manifest.LastBuilderSelection.WorkBuilderID); ok {
			workBuilder = selected
		} else if len(builders) > 1 {
			return block("Cypher Work Builder from the last Cypher popup selection is not active. Click Cypher and select Summary/Work Builders again.", http.StatusBadRequest)
		}
	}
	waves := buildExecutionWaves([]ModelConfig{workBuilder})
	if len(waves) != 1 || len(waves[0].BuilderIDs) != 1 {
		return block("Cypher Action requires one selected Work Builder in one wave. Disable Waves/extra Builders and try again.", http.StatusBadRequest)
	}
	wavePrompts := normalizeWavePromptMap(req.WavePrompts)
	nonEmptyWavePrompts := 0
	for _, prompt := range wavePrompts {
		if strings.TrimSpace(prompt) != "" {
			nonEmptyWavePrompts++
		}
	}
	if nonEmptyWavePrompts > 1 {
		return block("Cypher Action cannot run with multiple Wave prompts. Use the main prompt only and try again.", http.StatusBadRequest)
	}
	a.clearPendingMergeState(projectName)
	a.clearLastMergedFiles(projectName)
	executionID := fmt.Sprintf("%s-cypher-%d", projectName, time.Now().UTC().UnixNano())
	firstWave := waves[0]
	started := append([]string{}, firstWave.BuilderLabels...)
	state := waveExecutionState{ProjectName: projectName, ExecutionID: executionID, RootPrompt: rootPrompt, ContextFiles: nil, TemporaryAttachments: nil, WireTapEnabled: false, WavePrompts: map[int]string{firstWave.Number: rootPrompt}, WaveContextFiles: map[int][]string{}, WaveMediaInputRoles: map[int]map[string]string{}, Waves: waves, CurrentIndex: 0, CurrentWave: firstWave.Number, CurrentPromptSource: "cypher", CurrentContextFilesUsed: 0, LoopCount: 0, LoopsRemaining: 0, CycleNumber: 1, AwaitingMerge: false, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	a.mu.Lock()
	a.setWaveExecutionLocked(projectName, state)
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, firstWave.Number, "running", withWaveProgress("Cypher Running", state.CurrentIndex, len(state.Waves)), "cypher", 0))
	a.mu.Unlock()
	a.logf("system", "info", "Cypher Execute Prompt started for project %s. work_builder=%s", projectName, workBuilder.Label)
	go a.runCypherWaveExecution(projectName, executionID, source, externalOutfitRun)
	return executeResponse{Started: started, Skipped: skipped, WaveStarted: firstWave.Number, TotalWaves: 1, RemainingWaves: []int{}, QueuedBuilders: []string{}, ContextFilesUsed: 0}, nil, http.StatusOK
}

func (a *App) runCypherWaveExecution(projectName, executionID string, source executionSourceInfo, externalOutfitRun bool) {
	failRun := func(state waveExecutionState, wave executionWave, model ModelConfig, err error) {
		modelID := modelIDString(model.ID)
		if modelID == "0" || strings.TrimSpace(modelID) == "" {
			modelID = "system"
		}
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, wave.Number, "error", withWaveProgress("Cypher Failed", state.CurrentIndex, len(state.Waves)), "cypher", 0))
		a.mu.Unlock()
		a.logf(modelID, "error", "Cypher execution failed: %v", err)
		if externalOutfitRun {
			a.finalizeActiveOutfitRunFailed(projectName, err.Error())
		}
	}
	completeRun := func(state waveExecutionState, wave executionWave, model ModelConfig, applied int) {
		a.mu.Lock()
		a.clearWaveExecutionLocked(projectName)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, wave.Number, "complete", withWaveProgress("Cypher Complete", state.CurrentIndex, len(state.Waves)), "cypher", 0))
		a.mu.Unlock()
		a.clearPendingMergeState(projectName)
		a.clearLastMergedFiles(projectName)
		a.logf("system", "info", "Cypher workflow complete for project %s. Preserved ai_context.json project memory; selected context files should be cleared in the browser; Cypher remains enabled", projectName)
		if externalOutfitRun {
			a.finalizeActiveOutfitRunCompleted(projectName, modelIDString(model.ID), model.Label, fmt.Sprintf("Completed Cypher Outfit run with %s. Applied %d file operation(s) directly to projectwork.", model.Label, applied))
		}
	}
	for {
		a.logf("system", "info", "Cypher Action checkpoint for project %s: loading wave state.", projectName)
		state, ok := a.currentWaveExecution(projectName)
		if !ok || state.ExecutionID != executionID {
			a.logf("system", "warn", "Cypher Action stopped for project %s: current execution state is no longer active.", projectName)
			return
		}
		if state.CurrentIndex < 0 || state.CurrentIndex >= len(state.Waves) {
			failRun(state, executionWave{}, ModelConfig{}, errors.New("Cypher execution has no current wave"))
			return
		}
		wave := state.Waves[state.CurrentIndex]
		builders := a.buildersForWaveIDs(wave.BuilderIDs)
		if len(builders) != 1 {
			failRun(state, wave, ModelConfig{}, fmt.Errorf("Cypher wave %d requires exactly one Builder, found %d", wave.Number, len(builders)))
			return
		}
		model := builders[0]
		prompt, _, err := a.resolveWavePrompt(projectName, state, wave.Number)
		if err != nil {
			failRun(state, wave, model, err)
			return
		}
		a.logf("system", "info", "Cypher Action checkpoint for project %s: synchronizing projectwork for builder %s.", projectName, model.Label)
		if _, err := a.syncBuilderProjectsFromProjectwork(projectName, []ModelConfig{model}); err != nil {
			failRun(state, wave, model, err)
			return
		}

		// Cypher owns its own retrieval loop. Do not resolve or attach user-selected
		// context files here. In particular, do not call resolveWaveContextFiles while
		// holding a.mu; that helper reads the last-merged file list and can deadlock if
		// called from inside the app mutex.
		contextFiles := []string(nil)
		temporaryAttachments := []temporaryAttachmentInput(nil)

		a.mu.Lock()
		liveState, ok := a.waveExecutionsByProject[projectName]
		if !ok || liveState.ExecutionID != executionID {
			a.mu.Unlock()
			return
		}
		liveState.CurrentWave = wave.Number
		liveState.CurrentPromptSource = "cypher"
		liveState.CurrentContextFilesUsed = 0
		liveState.RootPrompt = prompt
		liveState.ContextFiles = nil
		liveState.TemporaryAttachments = nil
		liveState.WaveContextFiles = map[int][]string{}
		liveState.WaveMediaInputRoles = map[int]map[string]string{}
		liveState.AwaitingMerge = false
		a.setWaveExecutionLocked(projectName, liveState)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, liveState, wave.Number, "running", withWaveProgress("Cypher Running", liveState.CurrentIndex, len(liveState.Waves)), "cypher", 0))
		a.mu.Unlock()
		a.logf("system", "info", "Cypher wave %d started for project %s with builder %s context_files=0 temporary_attachments=none", wave.Number, projectName, model.Label)
		a.logf("system", "info", "Cypher Action checkpoint for project %s: entering Phase 1/2/3 action engine.", projectName)
		a.publishDiagnostics(cypherDiagnostics(projectName, model, "Action Startup Checkpoint").withStatusMessage("Cypher state prepared with no user-selected context files or temporary attachments; entering Phase 1/2/3 action engine."))
		result := a.runCypherExecution(projectName, executionID, model, prompt, contextFiles, temporaryAttachments)
		if !a.isWaveExecutionCurrent(projectName, executionID) {
			return
		}
		if result.Err != nil || !result.Valid {
			if result.Err == nil {
				result.Err = errors.New("Cypher execution failed")
			}
			failRun(liveState, wave, model, result.Err)
			return
		}
		a.clearPendingMergeState(projectName)
		a.setPendingMergeCount(projectName, modelIDString(model.ID), 0)
		a.mu.Lock()
		current, ok := a.waveExecutionsByProject[projectName]
		if !ok || current.ExecutionID != executionID {
			a.mu.Unlock()
			return
		}
		if current.CurrentIndex+1 >= len(current.Waves) {
			if current.LoopsRemaining > 0 && len(current.Waves) > 0 {
				current.LoopsRemaining--
				current.CycleNumber++
				current.CurrentIndex = 0
				current.AwaitingMerge = false
				nextWave := current.Waves[current.CurrentIndex]
				current.CurrentWave = nextWave.Number
				a.setWaveExecutionLocked(projectName, current)
				a.mu.Unlock()
				a.logf("system", "info", "Restarting Cypher wave cycle %d for project %s at wave %d. loops_remaining=%d", current.CycleNumber, projectName, nextWave.Number, current.LoopsRemaining)
				continue
			}
			a.mu.Unlock()
			completeRun(current, wave, model, result.AppliedOperations)
			return
		}
		current.CurrentIndex++
		current.AwaitingMerge = false
		nextWave := current.Waves[current.CurrentIndex]
		current.CurrentWave = nextWave.Number
		a.setWaveExecutionLocked(projectName, current)
		a.mu.Unlock()
		a.logf("system", "info", "Cypher wave %d complete for project %s. Launching next wave %d", wave.Number, projectName, nextWave.Number)
	}
}

func buildStandardOutfitExecuteRequest(outfit OutfitRecord, runtime outfitRuntimeInput) executeRequest {
	outfit = normalizeOutfitRecord(outfit)
	req := executeRequest{
		WavePrompts:      outfit.WavePrompts,
		WaveContextFiles: outfit.WaveContextFiles,
		LoopCount:        outfit.LoopCount,
		CypherEnabled:    outfit.UseCypher,
	}
	req = applyOutfitRuntimePrompt(outfit, req, runtime)
	if strings.TrimSpace(req.Prompt) == "" {
		firstKey := ""
		firstWave := 1 << 30
		for key := range req.WavePrompts {
			wave := parseWaveMapKey(key)
			if wave < firstWave {
				firstWave = wave
				firstKey = key
			}
		}
		if firstKey != "" {
			req.Prompt = strings.TrimSpace(req.WavePrompts[firstKey])
		}
	}
	return applyOutfitDeliveryInstructions(outfit, req)
}

func (a *App) startStandardOutfitRun(projectName string, outfit OutfitRecord, source executionSourceInfo, runtime outfitRuntimeInput) (executeResponse, error, int) {
	return a.startExecutionForCurrentConfig(strings.TrimSpace(projectName), buildStandardOutfitExecuteRequest(outfit, runtime), source)
}

func (a *App) validateDeadDropOutfitSource(projectName string, outfit OutfitRecord, source executionSourceInfo) error {
	projectName = strings.TrimSpace(projectName)
	outfit = normalizeOutfitRecord(outfit)
	sourcePolicy := normalizeDeadDropSourcePolicy(outfit.DeadDropSourcePolicy)
	sourcePath, _, err := a.currentDeadDropSource(projectName)
	if err != nil {
		return err
	}
	hasCurrent := strings.TrimSpace(sourcePath) != ""
	switch sourcePolicy {
	case "accept_webhook_upload":
		if source.DeadDropUploadReady {
			return nil
		}
		return errors.New("this DeadDrop Outfit requires a DeadDrop.<ext> upload sent to its dedicated DeadDrop webhook route")
	case "require_existing_deaddrop":
		if hasCurrent {
			return nil
		}
		return errors.New("this DeadDrop Outfit requires an existing DeadDrop.<ext> in the project's deaddrop folder")
	default:
		if hasCurrent {
			return nil
		}
		return errors.New("this DeadDrop Outfit cannot continue because the project deaddrop folder does not contain a current DeadDrop.<ext>")
	}
}

func (a *App) startDeadDropForOutfit(projectName string, outfit OutfitRecord, source executionSourceInfo) (executeResponse, error, int) {
	outfit = normalizeOutfitRecord(outfit)
	if err := a.validateDeadDropOutfitSource(projectName, outfit, source); err != nil {
		return executeResponse{}, err, http.StatusBadRequest
	}
	req := deadDropExecuteRequest{
		Prompt:        strings.TrimSpace(outfit.DeadDropPrompt),
		LoopCount:     normalizeOutfitLoopCount(outfit.LoopCount),
		StopScore:     normalizeDeadDropStopScore(outfit.DeadDropStopScore),
		RevisionLevel: normalizeDeadDropRevisionLevel(outfit.DeadDropRevisionLevel),
	}
	return a.startDeadDropExecution(strings.TrimSpace(projectName), req, source)
}

func (a *App) applyTimerAttemptState(outfit OutfitRecord, minuteStamp string, accepted bool) {
	outfit = normalizeOutfitRecord(outfit)
	if minuteStamp != "" {
		outfit.TimerLastAttemptAt = minuteStamp
	}
	if accepted && outfit.TimerIterations > 0 {
		outfit.TimerIterations--
		if outfit.TimerIterations <= 0 {
			outfit.TimerIterations = 0
			outfit.TimerEnabled = false
		}
	}
	if _, err := a.saveOutfitRecord(outfit); err != nil {
		a.logf("system", "warn", "Could not persist timer state for outfit %s: %v", outfit.ID, err)
	}
}

func (a *App) startOutfitTriggerRun(outfit OutfitRecord, source executionSourceInfo, timerMinuteStamp string, triggerPayload any) (executeResponse, error, int, *outfitRunRecord) {
	outfit = normalizeOutfitRecord(outfit)
	runtime := parseOutfitRuntimeInput(triggerPayload)
	view := a.outfitView(outfit)
	if messages := blockingOutfitIssueMessages(view.Issues); len(messages) > 0 {
		if source.TriggerType == "timer" {
			a.applyTimerAttemptState(outfit, timerMinuteStamp, false)
		}
		return executeResponse{}, errors.New(strings.Join(messages, " ")), http.StatusBadRequest, nil
	}
	projectName, _, err := a.applyOutfitState(outfit)
	if err != nil {
		if source.TriggerType == "timer" {
			a.applyTimerAttemptState(outfit, timerMinuteStamp, false)
		}
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "current run") {
			status = http.StatusConflict
		}
		return executeResponse{}, err, status, nil
	}
	if outfit.ExecutionMode != "deaddrop" {
		if err := a.configureRiskModeFromOutfit(outfit); err != nil {
			if source.TriggerType == "timer" {
				a.applyTimerAttemptState(outfit, timerMinuteStamp, false)
			}
			return executeResponse{}, err, http.StatusBadRequest, nil
		}
	}
	runRecord, err := a.createAcceptedOutfitRun(outfit, source, triggerPayload)
	if err != nil {
		if source.TriggerType == "timer" {
			a.applyTimerAttemptState(outfit, timerMinuteStamp, false)
		}
		return executeResponse{}, err, http.StatusInternalServerError, nil
	}
	var resp executeResponse
	var execErr error
	status := http.StatusBadRequest
	switch normalizeOutfitExecutionMode(outfit.ExecutionMode) {
	case "deaddrop":
		resp, execErr, status = a.startDeadDropForOutfit(projectName, outfit, source)
	default:
		resp, execErr, status = a.startStandardOutfitRun(projectName, outfit, source, runtime)
	}
	if execErr != nil {
		a.finalizeAcceptedOutfitRunFailure(runRecord, execErr.Error())
	}
	if execErr == nil && status == http.StatusOK {
		a.markAcceptedOutfitRunRunning(runRecord)
	}
	if source.TriggerType == "timer" {
		a.applyTimerAttemptState(outfit, timerMinuteStamp, execErr == nil && status == http.StatusOK)
	}
	return resp, execErr, status, runRecord
}

func (a *App) timerTickStamp(now time.Time) string {
	return now.In(time.Local).Truncate(time.Minute).Format(time.RFC3339)
}

func (a *App) processOutfitTimerTick() {
	outfits, err := a.readAllOutfitRecords()
	if err != nil {
		a.logf("system", "warn", "Timer scheduler could not read outfits: %v", err)
		return
	}
	now := time.Now().In(time.Local)
	minuteStamp := a.timerTickStamp(now)
	for _, outfit := range outfits {
		outfit = normalizeOutfitRecord(outfit)
		if !outfit.TimerEnabled {
			continue
		}
		if outfit.TimerIterations == 0 {
			continue
		}
		schedule, err := parseCronSchedule(outfit.TimerCron)
		if err != nil {
			continue
		}
		if strings.TrimSpace(outfit.TimerLastAttemptAt) == minuteStamp {
			continue
		}
		if !schedule.matches(now) {
			continue
		}
		_, triggerErr, status, _ := a.startOutfitTriggerRun(outfit, executionSourceInfo{TriggerType: "timer", OutfitID: outfit.ID, OutfitName: outfit.Name}, minuteStamp, map[string]any{"timer_minute": minuteStamp})
		if triggerErr != nil {
			if status == http.StatusConflict {
				a.logf("system", "warn", "Timer skipped outfit %s (%s) because AgentGO is busy.", outfit.ID, outfit.Name)
			} else {
				a.logf("system", "warn", "Timer could not start outfit %s (%s): %v", outfit.ID, outfit.Name, triggerErr)
			}
		}
	}
}

func (a *App) startOutfitTimerLoop() {
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			a.processOutfitTimerTick()
		}
	}()
}

func (a *App) handleOutfitCronPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cronPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	schedule, err := parseCronSchedule(req.Cron)
	if err != nil {
		writeJSON(w, http.StatusOK, cronPreviewResponse{Valid: false, Error: err.Error()})
		return
	}
	nextRun, err := schedule.nextRunAfter(time.Now().In(time.Local))
	if err != nil {
		writeJSON(w, http.StatusOK, cronPreviewResponse{Valid: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cronPreviewResponse{Valid: true, Summary: schedule.summary(), NextRun: nextRun.Format("Mon Jan 2, 2006 3:04 PM MST"), NextRunISO: nextRun.Format(time.RFC3339), Timezone: time.Now().In(time.Local).Location().String()})
}

func (a *App) handleUpdateOutfitTriggers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitTriggerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	outfit, err := a.readOutfitRecord(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	outfit.TimerEnabled = req.TimerEnabled
	outfit.TimerCron = strings.TrimSpace(strings.Join(strings.Fields(strings.TrimSpace(req.TimerCron)), " "))
	outfit.TimerIterations = req.TimerIterations
	outfit.WebhookEnabled = req.WebhookEnabled
	outfit.UseCypher = req.UseCypher && normalizeOutfitExecutionMode(outfit.ExecutionMode) != "deaddrop"
	outfit.DeliveryMode = normalizeOutfitDeliveryMode(req.DeliveryMode)
	outfit.CallbackURL = strings.TrimSpace(req.CallbackURL)
	outfit.PayloadType = normalizeOutfitPayloadType(req.PayloadType)
	outfit.FileSetSelectorType = normalizeOutfitFileSetSelectorType(req.FileSetSelectorType)
	outfit.FileSetSelectorValue = strings.TrimSpace(req.FileSetSelectorValue)
	outfit.DeadDropSourcePolicy = normalizeDeadDropSourcePolicy(req.DeadDropSourcePolicy)
	if outfit.TimerIterations == 0 || outfit.TimerIterations < -1 {
		http.Error(w, "Timer iterations must be -1 or a positive whole number.", http.StatusBadRequest)
		return
	}
	if outfit.TimerEnabled {
		if _, err := parseCronSchedule(outfit.TimerCron); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if outfit.DeliveryMode == "callback" {
		if outfit.CallbackURL == "" {
			http.Error(w, "Callback URL is required when delivery mode is callback.", http.StatusBadRequest)
			return
		}
		if _, err := url.ParseRequestURI(outfit.CallbackURL); err != nil {
			http.Error(w, "Callback URL is invalid.", http.StatusBadRequest)
			return
		}
		if outfit.PayloadType == "" {
			http.Error(w, "Payload type is required when delivery mode is callback.", http.StatusBadRequest)
			return
		}
		if outfit.PayloadType == "file_set" {
			if outfit.FileSetSelectorType == "" {
				http.Error(w, "File-set selector type is required.", http.StatusBadRequest)
				return
			}
			if outfit.FileSetSelectorType == "path_pattern" && outfit.FileSetSelectorValue == "" {
				http.Error(w, "File-set selector value is required for relative path / glob selection.", http.StatusBadRequest)
				return
			}
		} else {
			outfit.FileSetSelectorType = ""
			outfit.FileSetSelectorValue = ""
		}
		headers := map[string]string{}
		for key, value := range req.CallbackHeaders {
			cleanKey := strings.TrimSpace(key)
			if cleanKey == "" {
				continue
			}
			headers[cleanKey] = strings.TrimSpace(value)
		}
		outfit.CallbackHeaders = headers
		outfit.CallbackBearerToken = strings.TrimSpace(req.CallbackBearerToken)
	} else {
		outfit.DeliveryMode = "none"
		outfit.CallbackURL = ""
		outfit.CallbackHeaders = nil
		outfit.CallbackBearerToken = ""
		outfit.PayloadType = ""
		outfit.FileSetSelectorType = ""
		outfit.FileSetSelectorValue = ""
	}
	if outfit.WebhookEnabled && strings.TrimSpace(outfit.WebhookAPIKey) == "" {
		key, keyErr := generateOutfitWebhookKey()
		if keyErr != nil {
			http.Error(w, keyErr.Error(), http.StatusInternalServerError)
			return
		}
		outfit.WebhookAPIKey = key
	}
	outfit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	saved, err := a.saveOutfitRecord(outfit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleRegenerateOutfitWebhookKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	outfit, err := a.readOutfitRecord(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	key, err := generateOutfitWebhookKey()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outfit.WebhookAPIKey = key
	outfit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	saved, err := a.saveOutfitRecord(outfit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleRunOutfitWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/outfits/run/"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, webhookRunResponse{Status: "error", Message: "Outfit ID is required."})
		return
	}
	outfit, err := a.readOutfitRecord(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, webhookRunResponse{Status: "error", OutfitID: id, Message: "Outfit not found."})
		return
	}
	if !outfit.WebhookEnabled {
		writeJSON(w, http.StatusForbidden, webhookRunResponse{Status: "error", OutfitID: outfit.ID, Message: "Webhook trigger is disabled for this Outfit."})
		return
	}
	providedKey := strings.TrimSpace(r.Header.Get("X-AgentGO-Outfit-Key"))
	if providedKey == "" || providedKey != strings.TrimSpace(outfit.WebhookAPIKey) {
		writeJSON(w, http.StatusUnauthorized, webhookRunResponse{Status: "error", OutfitID: outfit.ID, Message: "Invalid Outfit webhook API key."})
		return
	}
	_, runErr, status, _ := a.startOutfitTriggerRun(outfit, executionSourceInfo{TriggerType: "webhook", OutfitID: outfit.ID, OutfitName: outfit.Name}, "", nil)
	if runErr != nil {
		if status == http.StatusConflict {
			writeJSON(w, http.StatusConflict, webhookRunResponse{Status: "busy", OutfitID: outfit.ID, Message: "AgentGO is already running another workflow."})
			return
		}
		writeJSON(w, status, webhookRunResponse{Status: "error", OutfitID: outfit.ID, Message: runErr.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, webhookRunResponse{Status: "accepted", OutfitID: outfit.ID, Message: "Outfit run accepted."})
}
