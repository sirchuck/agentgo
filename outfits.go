package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const outfitSchemaVersion = 1

var outfitIDPattern = regexp.MustCompile(`^i\d+$`)

func normalizeOutfitDeliveryMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "callback":
		return "callback"
	default:
		return "none"
	}
}

func normalizeOutfitPayloadType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json"
	case "zip":
		return "zip"
	case "file_set":
		return "file_set"
	case "pull_urls", "notification":
		return "pull_urls"
	default:
		return ""
	}
}

func normalizeOutfitFileSetSelectorType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return "all"
	case "canonical":
		return "canonical"
	case "path_pattern":
		return "path_pattern"
	default:
		return ""
	}
}

func normalizeOutfitExecutionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deaddrop":
		return "deaddrop"
	default:
		return "standard"
	}
}

func normalizeDeadDropSourcePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "require_existing_deaddrop":
		return "require_existing_deaddrop"
	case "accept_webhook_upload":
		return "accept_webhook_upload"
	default:
		return "continue_current"
	}
}

func outfitSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = slugCleaner.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "outfit"
	}
	return slug
}

func normalizeOutfitLoopCount(value int) int {
	if value < 0 {
		return 0
	}
	if value > 99 {
		return 99
	}
	return value
}

type OutfitModelState struct {
	Label   string `json:"label,omitempty"`
	ModelID string `json:"modelId,omitempty"`
	Active  bool   `json:"active"`
	Wave    int    `json:"wave"`
}

type OutfitRecord struct {
	AgentGOFile           string              `json:"agentgo_file,omitempty"`
	FileVersion           int                 `json:"file_version,omitempty"`
	Version               int                 `json:"version,omitempty"`
	ID                    string              `json:"id"`
	Name                  string              `json:"name"`
	CreatedAt             string              `json:"created_at"`
	UpdatedAt             string              `json:"updated_at"`
	Project               string              `json:"project"`
	ExecutionMode         string              `json:"executionMode,omitempty"`
	LoopCount             int                 `json:"loopCount,omitempty"`
	Models                []OutfitModelState  `json:"models,omitempty"`
	ObserverModelLabel    string              `json:"observerModelLabel,omitempty"`
	ObserverModelID       string              `json:"observerModelId,omitempty"`
	WavePrompts           map[string]string   `json:"wavePrompts,omitempty"`
	WaveContextFiles      map[string][]string `json:"waveContextFiles,omitempty"`
	RiskModeEnabled       bool                `json:"riskModeEnabled"`
	RiskModeIterations    int                 `json:"riskModeIterations"`
	DeadDropPrompt        string              `json:"deadDropPrompt,omitempty"`
	DeadDropStopScore     int                 `json:"deadDropStopScore,omitempty"`
	DeadDropRevisionLevel string              `json:"deadDropRevisionLevel,omitempty"`
	DeadDropSourcePolicy  string              `json:"deadDropSourcePolicy,omitempty"`
	ClearProjectBeforeRun bool                `json:"clearProjectBeforeRun"`
	UseCypher             bool                `json:"useCypher"`
	TimerEnabled          bool                `json:"timerEnabled"`
	TimerCron             string              `json:"timerCron,omitempty"`
	TimerIterations       int                 `json:"timerIterations"`
	TimerLastAttemptAt    string              `json:"timerLastAttemptAt,omitempty"`
	WebhookEnabled        bool                `json:"webhookEnabled"`
	WebhookAPIKey         string              `json:"webhookApiKey,omitempty"`
	DeliveryMode          string              `json:"deliveryMode,omitempty"`
	CallbackURL           string              `json:"callbackUrl,omitempty"`
	CallbackHeaders       map[string]string   `json:"callbackHeaders,omitempty"`
	CallbackBearerToken   string              `json:"callbackBearerToken,omitempty"`
	PayloadType           string              `json:"payloadType,omitempty"`
	FileSetSelectorType   string              `json:"fileSetSelectorType,omitempty"`
	FileSetSelectorValue  string              `json:"fileSetSelectorValue,omitempty"`
}

type OutfitIssue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

type OutfitSummary struct {
	ActiveModels   int   `json:"activeModels"`
	PopulatedWaves []int `json:"populatedWaves,omitempty"`
}

type OutfitView struct {
	OutfitRecord
	Summary OutfitSummary `json:"summary"`
	Issues  []OutfitIssue `json:"issues,omitempty"`
}

type outfitsListResponse struct {
	Outfits []OutfitView `json:"outfits"`
}

type outfitSaveRequest struct {
	Name   string       `json:"name"`
	Outfit OutfitRecord `json:"outfit"`
}

type outfitIDRequest struct {
	ID string `json:"id"`
}

type outfitRenameRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type outfitDuplicateRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type outfitActionResponse struct {
	Outfit  OutfitView   `json:"outfit"`
	Outfits []OutfitView `json:"outfits"`
}

type outfitApplyResponse struct {
	Outfit             OutfitView `json:"outfit"`
	ActiveProject      string     `json:"activeProject"`
	MissingProjectName string     `json:"missingProjectName,omitempty"`
}

func normalizeOutfitRecord(in OutfitRecord) OutfitRecord {
	out := OutfitRecord{
		AgentGOFile:           agentGOFileOutfit,
		FileVersion:           agentGOFileVersion,
		Version:               outfitSchemaVersion,
		ID:                    strings.TrimSpace(in.ID),
		Name:                  strings.TrimSpace(in.Name),
		CreatedAt:             strings.TrimSpace(in.CreatedAt),
		UpdatedAt:             strings.TrimSpace(in.UpdatedAt),
		Project:               strings.TrimSpace(in.Project),
		ExecutionMode:         normalizeOutfitExecutionMode(in.ExecutionMode),
		LoopCount:             normalizeOutfitLoopCount(in.LoopCount),
		ObserverModelLabel:    strings.TrimSpace(in.ObserverModelLabel),
		RiskModeEnabled:       in.RiskModeEnabled,
		RiskModeIterations:    in.RiskModeIterations,
		DeadDropPrompt:        strings.ReplaceAll(strings.ReplaceAll(in.DeadDropPrompt, "\r\n", "\n"), "\r", "\n"),
		DeadDropStopScore:     normalizeDeadDropStopScore(in.DeadDropStopScore),
		DeadDropRevisionLevel: normalizeDeadDropRevisionLevel(in.DeadDropRevisionLevel),
		ClearProjectBeforeRun: in.ClearProjectBeforeRun,
		UseCypher:             in.UseCypher,
		TimerEnabled:          in.TimerEnabled,
		TimerCron:             strings.TrimSpace(strings.Join(strings.Fields(strings.TrimSpace(in.TimerCron)), " ")),
		TimerIterations:       in.TimerIterations,
		TimerLastAttemptAt:    strings.TrimSpace(in.TimerLastAttemptAt),
		WebhookEnabled:        in.WebhookEnabled,
		WebhookAPIKey:         strings.TrimSpace(in.WebhookAPIKey),
		DeliveryMode:          normalizeOutfitDeliveryMode(in.DeliveryMode),
		CallbackURL:           strings.TrimSpace(in.CallbackURL),
		CallbackBearerToken:   strings.TrimSpace(in.CallbackBearerToken),
		PayloadType:           normalizeOutfitPayloadType(in.PayloadType),
		FileSetSelectorType:   normalizeOutfitFileSetSelectorType(in.FileSetSelectorType),
		FileSetSelectorValue:  strings.TrimSpace(in.FileSetSelectorValue),
	}
	if out.RiskModeIterations < 1 {
		out.RiskModeIterations = 1
	}
	if out.TimerIterations < -1 {
		out.TimerIterations = 1
	}
	if in.CallbackHeaders != nil {
		out.CallbackHeaders = map[string]string{}
		for k, v := range in.CallbackHeaders {
			key := strings.TrimSpace(k)
			value := strings.TrimSpace(v)
			if key == "" || value == "" {
				continue
			}
			out.CallbackHeaders[key] = value
		}
		if len(out.CallbackHeaders) == 0 {
			out.CallbackHeaders = nil
		}
	}
	out.Models = []OutfitModelState{}
	seenModels := map[string]bool{}
	for _, model := range in.Models {
		label := strings.TrimSpace(model.Label)
		if label == "" || seenModels[label] {
			continue
		}
		seenModels[label] = true
		out.Models = append(out.Models, OutfitModelState{Label: label, Active: model.Active, Wave: normalizeRunOrder(model.Wave)})
	}
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Wave == out.Models[j].Wave {
			return strings.ToLower(out.Models[i].Label) < strings.ToLower(out.Models[j].Label)
		}
		return out.Models[i].Wave < out.Models[j].Wave
	})
	out.WavePrompts = map[string]string{}
	for key, value := range in.WavePrompts {
		normKey := strconv.Itoa(normalizeRunOrder(parseWaveMapKey(key)))
		out.WavePrompts[normKey] = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	}
	if len(out.WavePrompts) == 0 {
		out.WavePrompts = nil
	}
	out.WaveContextFiles = map[string][]string{}
	for key, files := range in.WaveContextFiles {
		normKey := strconv.Itoa(normalizeRunOrder(parseWaveMapKey(key)))
		out.WaveContextFiles[normKey] = normalizeRelativePaths(files)
	}
	if len(out.WaveContextFiles) == 0 {
		out.WaveContextFiles = nil
	}
	if out.ExecutionMode == "deaddrop" {
		out.ObserverModelLabel = ""
		out.ObserverModelID = ""
		out.RiskModeEnabled = false
		out.RiskModeIterations = 1
		out.WavePrompts = nil
		out.WaveContextFiles = nil
	} else {
		out.DeadDropPrompt = ""
		out.DeadDropStopScore = 0
		out.DeadDropRevisionLevel = ""
		out.DeadDropSourcePolicy = ""
	}
	return out
}

func isSensitiveOutfitHeaderName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, token := range []string{"authorization", "bearer", "token", "secret", "cookie", "api-key", "apikey", "x-api-key", "key"} {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func sanitizeOutfitForExport(in OutfitRecord) OutfitRecord {
	out := normalizeOutfitRecord(in)
	out.WebhookAPIKey = ""
	out.CallbackBearerToken = ""
	out.TimerLastAttemptAt = ""
	if len(out.CallbackHeaders) > 0 {
		clean := map[string]string{}
		for key, value := range out.CallbackHeaders {
			if isSensitiveOutfitHeaderName(key) {
				continue
			}
			trimmedValue := strings.TrimSpace(value)
			if strings.TrimSpace(key) == "" || trimmedValue == "" {
				continue
			}
			clean[strings.TrimSpace(key)] = trimmedValue
		}
		if len(clean) == 0 {
			out.CallbackHeaders = nil
		} else {
			out.CallbackHeaders = clean
		}
	}
	return out
}

func validatePortableOutfitRecord(in OutfitRecord) (OutfitRecord, error) {
	out := normalizeOutfitRecord(in)
	if strings.TrimSpace(out.Name) == "" {
		return OutfitRecord{}, errors.New("Outfit name is required.")
	}
	if out.TimerIterations == 0 || out.TimerIterations < -1 {
		return OutfitRecord{}, errors.New("Timer iterations must be -1 or a positive whole number.")
	}
	if out.TimerEnabled {
		if _, err := parseCronSchedule(out.TimerCron); err != nil {
			return OutfitRecord{}, err
		}
	}
	if out.ExecutionMode == "deaddrop" {
		out.FileSetSelectorType = ""
		out.FileSetSelectorValue = ""
	}
	if out.DeliveryMode == "callback" {
		if out.CallbackURL == "" {
			return OutfitRecord{}, errors.New("Callback URL is required when delivery mode is callback.")
		}
		if _, err := url.ParseRequestURI(out.CallbackURL); err != nil {
			return OutfitRecord{}, errors.New("Callback URL is invalid.")
		}
		if out.PayloadType == "" {
			return OutfitRecord{}, errors.New("Payload type is required when delivery mode is callback.")
		}
		if out.PayloadType == "file_set" {
			if out.FileSetSelectorType == "" {
				return OutfitRecord{}, errors.New("File-set selector type is required.")
			}
			if out.FileSetSelectorType == "path_pattern" && out.FileSetSelectorValue == "" {
				return OutfitRecord{}, errors.New("File-set selector value is required for relative path / glob selection.")
			}
		} else {
			out.FileSetSelectorType = ""
			out.FileSetSelectorValue = ""
		}
	} else {
		out.DeliveryMode = "none"
		out.CallbackURL = ""
		out.CallbackHeaders = nil
		out.CallbackBearerToken = ""
		out.PayloadType = ""
		out.FileSetSelectorType = ""
		out.FileSetSelectorValue = ""
	}
	out = sanitizeOutfitForExport(out)
	return out, nil
}

func parseWaveMapKey(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

func (a *App) legacyOutfitsDir() string {
	dir := filepath.Dir(a.modelsPath)
	if strings.TrimSpace(dir) == "" || dir == "." {
		return "Outfits"
	}
	return filepath.Join(dir, "Outfits")
}

func (a *App) outfitsDir() string {
	path, err := safeJoin(a.cfg.WorkRoot, "outfits")
	if err != nil {
		return filepath.Join(a.cfg.WorkRoot, "outfits")
	}
	return path
}

func (a *App) ensureOutfitsDir() error {
	if err := os.MkdirAll(a.outfitsDir(), 0o755); err != nil {
		return err
	}
	return a.migrateLegacyOutfitsIfNeeded()
}

func (a *App) migrateLegacyOutfitsIfNeeded() error {
	legacy := a.legacyOutfitsDir()
	if filepath.Clean(legacy) == filepath.Clean(a.outfitsDir()) {
		return nil
	}
	entries, err := os.ReadDir(legacy)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(legacy, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var outfit OutfitRecord
		if err := json.Unmarshal(data, &outfit); err != nil {
			continue
		}
		outfit = a.prepareOutfitRecord(outfit)
		if outfit.ID == "" {
			outfit.ID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if err := validateOutfitID(outfit.ID); err != nil {
			continue
		}
		target, pathErr := a.outfitPathForRecord(outfit)
		if pathErr != nil {
			continue
		}
		if _, statErr := os.Stat(target); statErr == nil {
			continue
		}
		if err := writeJSONFileAtomic(target, outfit); err != nil {
			return err
		}
		_ = os.Remove(path)
	}
	return nil
}

func validateOutfitID(id string) error {
	id = strings.TrimSpace(id)
	if !outfitIDPattern.MatchString(id) {
		return errors.New("invalid outfit id")
	}
	return nil
}

func outfitIDNumber(id string) int {
	if !strings.HasPrefix(strings.TrimSpace(id), "i") {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(id), "i"))
	return n
}

func (a *App) nextOutfitID() (string, error) {
	outfits, err := a.readAllOutfitRecords()
	if err != nil {
		return "", err
	}
	top := 0
	for _, outfit := range outfits {
		if n := outfitIDNumber(outfit.ID); n > top {
			top = n
		}
	}
	return fmt.Sprintf("i%d", top+1), nil
}

func (a *App) outfitPathForRecord(outfit OutfitRecord) (string, error) {
	outfit = normalizeOutfitRecord(outfit)
	if err := validateOutfitID(outfit.ID); err != nil {
		return "", err
	}
	return filepath.Join(a.outfitsDir(), outfit.ID+"_"+outfitSlug(outfit.Name)+".json"), nil
}

func (a *App) outfitPath(id string) (string, error) {
	if err := validateOutfitID(id); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(a.outfitsDir())
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if base == id || strings.HasPrefix(base, id+"_") {
			return filepath.Join(a.outfitsDir(), entry.Name()), nil
		}
	}
	return filepath.Join(a.outfitsDir(), id+".json"), nil
}

func (a *App) readAllOutfitRecords() ([]OutfitRecord, error) {
	if err := a.ensureOutfitsDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.outfitsDir())
	if err != nil {
		return nil, err
	}
	outfits := make([]OutfitRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(a.outfitsDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			a.logf("system", "warn", "Skipping unreadable outfit %s: %v", entry.Name(), err)
			continue
		}
		var outfit OutfitRecord
		if err := json.Unmarshal(data, &outfit); err != nil {
			a.logf("system", "warn", "Skipping invalid outfit %s: %v", entry.Name(), err)
			continue
		}
		outfit = a.prepareOutfitRecord(outfit)
		if outfit.ID == "" {
			outfit.ID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if err := validateOutfitID(outfit.ID); err != nil {
			a.logf("system", "warn", "Skipping outfit with invalid id %s: %v", outfit.ID, err)
			continue
		}
		outfits = append(outfits, outfit)
	}
	return outfits, nil
}

func (a *App) readOutfitRecord(id string) (OutfitRecord, error) {
	path, err := a.outfitPath(id)
	if err != nil {
		return OutfitRecord{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OutfitRecord{}, errors.New("outfit not found")
		}
		return OutfitRecord{}, err
	}
	var outfit OutfitRecord
	if err := json.Unmarshal(data, &outfit); err != nil {
		return OutfitRecord{}, err
	}
	outfit = a.prepareOutfitRecord(outfit)
	if outfit.ID == "" {
		outfit.ID = strings.TrimSpace(id)
	}
	return outfit, nil
}

func writeJSONFileAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tempPath := path + ".tmp"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (a *App) saveOutfitRecord(outfit OutfitRecord) (OutfitRecord, error) {
	outfit = a.prepareOutfitRecord(outfit)
	if err := a.validateOutfitForSave(outfit); err != nil {
		return OutfitRecord{}, err
	}
	if strings.TrimSpace(outfit.ID) == "" {
		return OutfitRecord{}, errors.New("outfit id is required")
	}
	if err := validateOutfitID(outfit.ID); err != nil {
		return OutfitRecord{}, err
	}
	oldPath, err := a.outfitPath(outfit.ID)
	if err != nil {
		return OutfitRecord{}, err
	}
	path, err := a.outfitPathForRecord(outfit)
	if err != nil {
		return OutfitRecord{}, err
	}
	if err := writeJSONFileAtomic(path, outfit); err != nil {
		return OutfitRecord{}, err
	}
	if filepath.Clean(oldPath) != filepath.Clean(path) {
		_ = os.Remove(oldPath)
	}
	return outfit, nil
}

func (a *App) deleteOutfitRecord(id string) error {
	path, err := a.outfitPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("outfit not found")
		}
		return err
	}
	return nil
}
func (a *App) modelLabelMaps() (map[string]string, map[string]string, map[string]int) {
	labelToID := map[string]string{}
	idToLabel := map[string]string{}
	duplicates := map[string]int{}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, model := range a.cfg.Models {
		label := strings.TrimSpace(model.Label)
		modelID := modelIDString(model.ID)
		if label != "" {
			if existing, ok := labelToID[label]; ok && existing != modelID {
				duplicates[label]++
			} else if _, dup := duplicates[label]; !dup {
				labelToID[label] = modelID
			}
		}
		idToLabel[modelID] = label
	}
	return labelToID, idToLabel, duplicates
}

func sortedDuplicateLabelList(duplicates map[string]int) []string {
	labels := make([]string, 0, len(duplicates))
	for label := range duplicates {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

func formatDuplicateOutfitLabels(duplicates map[string]int) string {
	return strings.Join(sortedDuplicateLabelList(duplicates), ", ")
}

func (a *App) prepareOutfitRecord(in OutfitRecord) OutfitRecord {
	_, idToLabel, _ := a.modelLabelMaps()
	prepared := in
	if strings.TrimSpace(prepared.ObserverModelLabel) == "" {
		prepared.ObserverModelLabel = strings.TrimSpace(idToLabel[strings.TrimSpace(prepared.ObserverModelID)])
	}
	prepared.ObserverModelID = ""
	models := make([]OutfitModelState, 0, len(prepared.Models))
	for _, model := range prepared.Models {
		label := strings.TrimSpace(model.Label)
		if label == "" {
			label = strings.TrimSpace(idToLabel[strings.TrimSpace(model.ModelID)])
		}
		models = append(models, OutfitModelState{Label: label, Active: model.Active, Wave: model.Wave})
	}
	prepared.Models = models
	return normalizeOutfitRecord(prepared)
}

func (a *App) validateOutfitForSave(outfit OutfitRecord) error {
	_, _, duplicates := a.modelLabelMaps()
	if len(duplicates) > 0 {
		return fmt.Errorf("cannot save outfit: duplicate model labels detected (%s)", formatDuplicateOutfitLabels(duplicates))
	}
	outfit = normalizeOutfitRecord(outfit)
	if outfit.ExecutionMode == "deaddrop" && len(outfit.Models) == 0 {
		return errors.New("cannot save DeadDrop Outfit: no active models selected")
	}
	return nil
}

func (a *App) validateOutfitForApply(outfit OutfitRecord) error {
	labelToID, _, duplicates := a.modelLabelMaps()
	if len(duplicates) > 0 {
		return fmt.Errorf("cannot load outfit: duplicate model labels detected (%s)", formatDuplicateOutfitLabels(duplicates))
	}
	outfit = normalizeOutfitRecord(outfit)
	missing := []string{}
	seen := map[string]bool{}
	for _, model := range outfit.Models {
		label := strings.TrimSpace(model.Label)
		if label == "" {
			continue
		}
		if _, ok := labelToID[label]; ok || seen[label] {
			continue
		}
		seen[label] = true
		missing = append(missing, label)
	}
	if outfit.ExecutionMode != "deaddrop" {
		observerLabel := strings.TrimSpace(outfit.ObserverModelLabel)
		if observerLabel != "" {
			if _, ok := labelToID[observerLabel]; !ok && !seen[observerLabel] {
				missing = append(missing, observerLabel)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if len(missing) == 1 {
			return fmt.Errorf("cannot load outfit: model label %s was not found", missing[0])
		}
		return fmt.Errorf("cannot load outfit: model labels not found (%s)", strings.Join(missing, ", "))
	}
	return nil
}

func (a *App) resolveOutfitModelID(label string) string {
	labelToID, _, _ := a.modelLabelMaps()
	return strings.TrimSpace(labelToID[strings.TrimSpace(label)])
}

func (a *App) outfitView(outfit OutfitRecord) OutfitView {
	outfit = normalizeOutfitRecord(outfit)
	summary := OutfitSummary{}
	waveSet := map[int]bool{}
	for _, model := range outfit.Models {
		if model.Active {
			summary.ActiveModels++
			waveSet[normalizeRunOrder(model.Wave)] = true
		}
	}
	for wave := range waveSet {
		summary.PopulatedWaves = append(summary.PopulatedWaves, wave)
	}
	sort.Ints(summary.PopulatedWaves)

	issues := make([]OutfitIssue, 0)
	project := strings.TrimSpace(outfit.Project)
	projectExists := false
	if project == "" {
		issues = append(issues, OutfitIssue{Code: "missing_project", Message: "No project is saved for this Outfit.", Blocking: true})
	} else if _, err := os.Stat(filepath.Join(a.cfg.WorkRoot, "projects", project)); err != nil {
		issues = append(issues, OutfitIssue{Code: "missing_project", Message: fmt.Sprintf("Project %s no longer exists.", project), Blocking: true})
	} else {
		projectExists = true
	}

	labelToID, _, duplicates := a.modelLabelMaps()
	for _, label := range sortedDuplicateLabelList(duplicates) {
		issues = append(issues, OutfitIssue{Code: "duplicate_model_label", Message: fmt.Sprintf("Model label %s is duplicated locally. Fix duplicate labels before saving or loading Outfits.", label), Blocking: true})
	}
	for _, model := range outfit.Models {
		label := strings.TrimSpace(model.Label)
		if label == "" {
			issues = append(issues, OutfitIssue{Code: "missing_model_label", Message: "A saved model entry is missing its label.", Blocking: true})
			continue
		}
		if _, ok := labelToID[label]; !ok {
			issues = append(issues, OutfitIssue{Code: "missing_model", Message: fmt.Sprintf("Model label %s is no longer available.", label), Blocking: true})
		}
	}
	if outfit.ExecutionMode != "deaddrop" {
		observerLabel := strings.TrimSpace(outfit.ObserverModelLabel)
		if observerLabel != "" {
			if _, ok := labelToID[observerLabel]; !ok {
				issues = append(issues, OutfitIssue{Code: "missing_observer_model", Message: fmt.Sprintf("Observer model label %s is no longer available.", observerLabel), Blocking: true})
			}
		}
	}

	if projectExists && outfit.ExecutionMode == "deaddrop" {
		sourcePolicy := normalizeDeadDropSourcePolicy(outfit.DeadDropSourcePolicy)
		if sourcePolicy != "accept_webhook_upload" {
			sourcePath, _, sourceErr := a.currentDeadDropSource(project)
			if sourceErr != nil {
				issues = append(issues, OutfitIssue{Code: "deaddrop_source_lookup_failed", Message: fmt.Sprintf("Could not inspect the current DeadDrop source: %v", sourceErr), Blocking: false})
			} else if strings.TrimSpace(sourcePath) == "" {
				issues = append(issues, OutfitIssue{Code: "missing_deaddrop_source", Message: "This DeadDrop Outfit needs an existing DeadDrop.<ext> in the project's deaddrop folder before it can run.", Blocking: false})
			}
		}
	}

	if projectExists && outfit.ExecutionMode != "deaddrop" {
		if projectworkRoot, err := a.projectWorkRoot(project); err == nil {
			missingFiles := []string{}
			for _, files := range outfit.WaveContextFiles {
				for _, rel := range files {
					resolved, joinErr := safeJoin(projectworkRoot, rel)
					if joinErr != nil {
						missingFiles = append(missingFiles, rel)
						continue
					}
					if _, statErr := os.Stat(resolved); statErr != nil {
						missingFiles = append(missingFiles, rel)
					}
				}
			}
			missingFiles = normalizeRelativePaths(missingFiles)
			for _, rel := range missingFiles {
				issues = append(issues, OutfitIssue{Code: "missing_context_file", Message: fmt.Sprintf("Included context file %s no longer exists.", rel), Blocking: true})
			}
		}
	}

	if outfit.UseCypher && outfit.ExecutionMode != "deaddrop" {
		if summary.ActiveModels != 1 {
			issues = append(issues, OutfitIssue{Code: "cypher_requires_single_builder", Message: "Cypher Outfits require exactly one active Builder model.", Blocking: true})
		}
		if strings.TrimSpace(outfit.ObserverModelLabel) != "" {
			issues = append(issues, OutfitIssue{Code: "cypher_blocks_observer", Message: "Cypher Outfits cannot use an Observer/Reviewer model.", Blocking: true})
		}
		if outfit.RiskModeEnabled {
			issues = append(issues, OutfitIssue{Code: "cypher_blocks_risk", Message: "Cypher Outfits cannot use Risk Mode.", Blocking: true})
		}
		if normalizeOutfitLoopCount(outfit.LoopCount) > 0 {
			issues = append(issues, OutfitIssue{Code: "cypher_blocks_loops", Message: "Cypher Outfits cannot use loops/cycles.", Blocking: true})
		}
		if len(summary.PopulatedWaves) > 1 {
			issues = append(issues, OutfitIssue{Code: "cypher_blocks_multi_wave", Message: "Cypher Outfits require one active wave.", Blocking: true})
		}
	}

	return OutfitView{OutfitRecord: outfit, Summary: summary, Issues: issues}
}

func (a *App) listOutfitViews() ([]OutfitView, error) {
	outfits, err := a.readAllOutfitRecords()
	if err != nil {
		return nil, err
	}
	views := make([]OutfitView, 0, len(outfits))
	for _, outfit := range outfits {
		views = append(views, a.outfitView(outfit))
	}
	sort.Slice(views, func(i, j int) bool {
		left := strings.TrimSpace(views[i].UpdatedAt)
		right := strings.TrimSpace(views[j].UpdatedAt)
		if left == right {
			return outfitIDNumber(views[i].ID) > outfitIDNumber(views[j].ID)
		}
		return left > right
	})
	return views, nil
}

func (a *App) clearExecutionStateForOutfitLoad() {
	a.mu.Lock()
	a.clearRiskModeLocked()
	a.pendingMergeCountsByProject = map[string]map[string]int{}
	a.clearAllWaveExecutionsLocked()
	a.waveStatusByProject = map[string]waveStatusState{}
	for _, model := range a.cfg.Models {
		a.toggles[modelIDString(model.ID)] = false
	}
	a.reviewerID = ""
	a.activeProjectName = ""
	a.mu.Unlock()
}

func (a *App) applyOutfitState(outfit OutfitRecord) (string, string, error) {
	outfit = a.prepareOutfitRecord(outfit)
	if err := a.validateOutfitForApply(outfit); err != nil {
		return "", "", err
	}
	a.mu.RLock()
	busy := len(a.activeCancels) > 0 || len(a.waveExecutionsByProject) > 0
	a.mu.RUnlock()
	if busy {
		return "", "", errors.New("stop the current run before loading an outfit")
	}

	projectName := strings.TrimSpace(outfit.Project)
	missingProjectName := ""
	if projectName != "" {
		if _, err := os.Stat(filepath.Join(a.cfg.WorkRoot, "projects", projectName)); err != nil {
			missingProjectName = projectName
			projectName = ""
		}
	}

	a.clearExecutionStateForOutfitLoad()

	a.mu.Lock()
	if projectName != "" {
		a.activeProjectName = projectName
	}
	desiredReviewer := ""
	if outfit.ExecutionMode != "deaddrop" {
		desiredReviewer = a.resolveOutfitModelID(outfit.ObserverModelLabel)
	}
	availableModelIDs := map[string]bool{}
	for i := range a.cfg.Models {
		modelID := modelIDString(a.cfg.Models[i].ID)
		availableModelIDs[modelID] = true
		a.toggles[modelID] = false
	}
	for _, model := range outfit.Models {
		modelID := a.resolveOutfitModelID(model.Label)
		if !availableModelIDs[modelID] {
			continue
		}
		a.setModelRunOrderLocked(modelID, model.Wave)
		a.toggles[modelID] = model.Active
	}
	if desiredReviewer != "" && availableModelIDs[desiredReviewer] {
		a.reviewerID = desiredReviewer
		a.toggles[desiredReviewer] = false
	} else {
		a.reviewerID = ""
	}
	if err := a.persistModelsLocked(); err != nil {
		a.mu.Unlock()
		return "", "", err
	}
	a.mu.Unlock()

	if projectName != "" {
		activeBuilders := a.activeBuilderModelsSnapshot()
		if _, err := a.syncBuilderProjectsFromProjectwork(projectName, activeBuilders); err != nil {
			return "", missingProjectName, err
		}
		resetCount, err := a.resetModelAIContextsToEmpty(projectName, activeBuilders)
		if err != nil {
			return "", missingProjectName, err
		}
		a.logf("system", "info", "Reset ai_context.json to strict empty memory and reviewer_context.json to {} for %d Outfit-activated model(s) in project %s", resetCount, projectName)
	}
	return projectName, missingProjectName, nil
}

func (a *App) applyOutfitRecord(outfit OutfitRecord) (string, string, error) {
	return a.applyOutfitState(outfit)
}

func (a *App) handleOutfits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfits, err := a.listOutfitViews()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, outfitsListResponse{Outfits: outfits})
}

func (a *App) handleCreateOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id, err := a.nextOutfitID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outfit := normalizeOutfitRecord(req.Outfit)
	outfit.ID = id
	outfit.Name = strings.TrimSpace(req.Name)
	if outfit.Name == "" {
		http.Error(w, "outfit name is required", http.StatusBadRequest)
		return
	}
	outfit.CreatedAt = now
	outfit.UpdatedAt = now
	outfit.AgentGOFile = agentGOFileOutfit
	outfit.FileVersion = agentGOFileVersion
	outfit.Version = outfitSchemaVersion
	saved, err := a.saveOutfitRecord(outfit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Saved new outfit %s (%s)", saved.ID, saved.Name)
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleUpdateOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	existing, err := a.readOutfitRecord(req.Outfit.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	updated := normalizeOutfitRecord(req.Outfit)
	updated.ID = existing.ID
	updated.Name = existing.Name
	updated.CreatedAt = existing.CreatedAt
	updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	updated.AgentGOFile = agentGOFileOutfit
	updated.FileVersion = agentGOFileVersion
	updated.Version = outfitSchemaVersion
	updated.TimerEnabled = existing.TimerEnabled
	updated.TimerCron = existing.TimerCron
	updated.TimerIterations = existing.TimerIterations
	updated.TimerLastAttemptAt = existing.TimerLastAttemptAt
	updated.WebhookEnabled = existing.WebhookEnabled
	updated.WebhookAPIKey = existing.WebhookAPIKey
	updated.DeliveryMode = existing.DeliveryMode
	updated.CallbackURL = existing.CallbackURL
	updated.CallbackHeaders = existing.CallbackHeaders
	updated.CallbackBearerToken = existing.CallbackBearerToken
	updated.PayloadType = existing.PayloadType
	updated.FileSetSelectorType = existing.FileSetSelectorType
	updated.FileSetSelectorValue = existing.FileSetSelectorValue
	saved, err := a.saveOutfitRecord(updated)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Updated outfit %s (%s)", saved.ID, saved.Name)
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleRenameOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	outfit, err := a.readOutfitRecord(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "outfit name is required", http.StatusBadRequest)
		return
	}
	outfit.Name = name
	outfit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	saved, err := a.saveOutfitRecord(outfit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Renamed outfit %s to %s", saved.ID, saved.Name)
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleDuplicateOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitDuplicateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	source, err := a.readOutfitRecord(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "outfit name is required", http.StatusBadRequest)
		return
	}
	id, err := a.nextOutfitID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	source.ID = id
	source.Name = name
	source.CreatedAt = now
	source.UpdatedAt = now
	source.AgentGOFile = agentGOFileOutfit
	source.FileVersion = agentGOFileVersion
	source.Version = outfitSchemaVersion
	source.TimerEnabled = false
	source.TimerLastAttemptAt = ""
	source.WebhookEnabled = false
	if key, keyErr := generateOutfitWebhookKey(); keyErr == nil {
		source.WebhookAPIKey = key
	} else {
		source.WebhookAPIKey = ""
	}
	saved, err := a.saveOutfitRecord(source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Duplicated outfit %s as %s (%s)", req.ID, saved.ID, saved.Name)
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleDeleteOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req outfitIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := a.deleteOutfitRecord(req.ID); err != nil {
		if err.Error() == "outfit not found" {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Deleted outfit %s", strings.TrimSpace(req.ID))
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitsListResponse{Outfits: outfits})
}

func (a *App) handleExportOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "outfit id is required", http.StatusBadRequest)
		return
	}
	outfit, err := a.readOutfitRecord(id)
	if err != nil {
		if err.Error() == "outfit not found" {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	exported := sanitizeOutfitForExport(outfit)
	data, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("outfit_%s.json", outfitSlug(exported.Name))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) handleImportOutfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.ensureOutfitsDir(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	var imported OutfitRecord
	if err := decoder.Decode(&imported); err != nil {
		http.Error(w, "Import file is not a valid Outfit JSON definition.", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "Import file must contain exactly one Outfit JSON object.", http.StatusBadRequest)
		return
	}
	portable, err := validatePortableOutfitRecord(a.prepareOutfitRecord(imported))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := a.nextOutfitID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	portable.ID = id
	portable.CreatedAt = now
	portable.UpdatedAt = now
	portable.AgentGOFile = agentGOFileOutfit
	portable.FileVersion = agentGOFileVersion
	portable.Version = outfitSchemaVersion
	portable.TimerLastAttemptAt = ""
	portable.WebhookAPIKey = ""
	if portable.WebhookEnabled {
		key, keyErr := generateOutfitWebhookKey()
		if keyErr != nil {
			http.Error(w, keyErr.Error(), http.StatusInternalServerError)
			return
		}
		portable.WebhookAPIKey = key
	}
	saved, err := a.saveOutfitRecord(portable)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Imported outfit %s (%s)", saved.ID, saved.Name)
	outfits, _ := a.listOutfitViews()
	writeJSON(w, http.StatusOK, outfitActionResponse{Outfit: a.outfitView(saved), Outfits: outfits})
}

func (a *App) handleApplyOutfit(w http.ResponseWriter, r *http.Request) {
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
	activeProject, missingProjectName, err := a.applyOutfitRecord(outfit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.logf("system", "info", "Loaded outfit %s (%s)", outfit.ID, outfit.Name)
	writeJSON(w, http.StatusOK, outfitApplyResponse{Outfit: a.outfitView(outfit), ActiveProject: activeProject, MissingProjectName: missingProjectName})
}
