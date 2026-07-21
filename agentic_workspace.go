package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	agenticWorkspaceDirName       = "agentic-work"
	agenticWorkspaceTaskFileName  = "task.json"
	agenticWorkspaceFilesDirName  = "workspace"
	agenticWorkspaceSchemaVersion = 1

	agenticWorkspaceStatusActive          = "active"
	agenticWorkspaceStatusAwaitingCommand = "awaiting_manual_approval"
	agenticWorkspaceStatusNeedsUserInput  = "needs_user_input"
	agenticWorkspaceStatusAwaitingReview  = "awaiting_review"
	agenticWorkspaceStatusInterrupted     = "interrupted"
	agenticWorkspaceStatusMaximumRuns     = "maximum_runs_reached"
	agenticWorkspaceStatusPrepared        = "prepared_new_task"
)

const (
	agenticWorkspaceDecisionPending  = "pending"
	agenticWorkspaceDecisionMerged   = "merged"
	agenticWorkspaceDecisionRejected = "rejected"
)

const (
	agenticWorkspaceChangeAdded    = "added"
	agenticWorkspaceChangeModified = "modified"
	agenticWorkspaceChangeDeleted  = "deleted"
)

var (
	errAgenticMaximumRunsReached   = errors.New("agentic Full mode Maximum Runs reached")
	errAgenticUnresolvedTaskExists = errors.New("an unresolved agentic task must be resolved before another task can begin")
)

type agenticWorkspaceManifestEntry struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Binary    bool   `json:"binary,omitempty"`
	Mode      uint32 `json:"mode,omitempty"`
}

type agenticWorkspaceTask struct {
	SchemaVersion         int                                      `json:"schemaVersion"`
	SessionID             string                                   `json:"sessionId"`
	ProjectName           string                                   `json:"projectName"`
	Mode                  string                                   `json:"mode"`
	MaxRuns               int                                      `json:"maxRuns"`
	RunNumber             int                                      `json:"runNumber,omitempty"`
	Status                string                                   `json:"status"`
	Incomplete            bool                                     `json:"incomplete"`
	Summary               string                                   `json:"summary,omitempty"`
	OriginalPrompt        string                                   `json:"originalPrompt,omitempty"`
	LatestUserInstruction string                                   `json:"latestUserInstruction,omitempty"`
	ProgressSummary       string                                   `json:"progressSummary,omitempty"`
	LatestCommandResult   string                                   `json:"latestCommandResult,omitempty"`
	Workspace             map[string]any                           `json:"workspace"`
	TokenUsage            agenticTaskTokenUsage                    `json:"tokenUsage"`
	CreatedAt             string                                   `json:"createdAt"`
	UpdatedAt             string                                   `json:"updatedAt"`
	Baseline              map[string]agenticWorkspaceManifestEntry `json:"baseline"`
	Decisions             map[string]string                        `json:"decisions,omitempty"`
	PendingCommand        *workModeAgenticCommand                  `json:"pendingCommand,omitempty"`
	PendingCommandAt      string                                   `json:"pendingCommandAt,omitempty"`
}

type agenticWorkspaceChange struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Binary         bool   `json:"binary,omitempty"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	Decision       string `json:"decision"`
	Conflict       bool   `json:"conflict,omitempty"`
	ConflictReason string `json:"conflictReason,omitempty"`
}

type agenticWorkspaceReview struct {
	TaskID         string                   `json:"taskId"`
	Mode           string                   `json:"mode,omitempty"`
	Status         string                   `json:"status"`
	Incomplete     bool                     `json:"incomplete"`
	Summary        string                   `json:"summary,omitempty"`
	Workspace      string                   `json:"workspace"`
	CreatedAt      string                   `json:"createdAt,omitempty"`
	UpdatedAt      string                   `json:"updatedAt,omitempty"`
	RunNumber      int                      `json:"runNumber,omitempty"`
	MaxRuns        int                      `json:"maxRuns,omitempty"`
	Changes        []agenticWorkspaceChange `json:"changes"`
	AddedCount     int                      `json:"addedCount"`
	ModifiedCount  int                      `json:"modifiedCount"`
	DeletedCount   int                      `json:"deletedCount"`
	BinaryCount    int                      `json:"binaryCount"`
	UnchangedCount int                      `json:"unchangedCount"`
	PendingCount   int                      `json:"pendingCount"`
	ConflictCount  int                      `json:"conflictCount"`
	Resolved       bool                     `json:"resolved"`
	Deleted        bool                     `json:"deleted,omitempty"`
	Message        string                   `json:"message,omitempty"`
	PendingCommand *workModeAgenticCommand  `json:"pendingCommand,omitempty"`
	CommandRunning bool                     `json:"commandRunning,omitempty"`
	AuditRecords   []agenticAuditRecord     `json:"auditRecords,omitempty"`
	TokenUsage     agenticTaskTokenUsage    `json:"tokenUsage"`
}

type agenticWorkspacePathRequest struct {
	TaskID string `json:"taskId"`
	Path   string `json:"path"`
}

type agenticWorkspaceTaskRequest struct {
	TaskID string `json:"taskId"`
	Reason string `json:"reason,omitempty"`
}

func newAgenticWorkspaceSessionID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(buf)), nil
}

func validAgenticWorkspaceSessionID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) agenticWorkspaceProjectRoot(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return safeJoin(root, agenticWorkspaceDirName)
}

func (a *App) agenticWorkspaceTaskRoot(projectName, taskID string) (string, error) {
	if !validAgenticWorkspaceSessionID(taskID) {
		return "", errors.New("invalid agentic workspace task id")
	}
	root, err := a.agenticWorkspaceProjectRoot(projectName)
	if err != nil {
		return "", err
	}
	return safeJoin(root, taskID)
}

func (a *App) agenticWorkspaceFilesRoot(projectName, taskID string) (string, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return "", err
	}
	return safeJoin(taskRoot, agenticWorkspaceFilesDirName)
}

func agenticWorkspaceTaskPath(taskRoot string) string {
	return filepath.Join(taskRoot, agenticWorkspaceTaskFileName)
}

func agenticWorkspaceDisplayPath(taskID string) string {
	return filepath.ToSlash(filepath.Join(agenticWorkspaceDirName, taskID, agenticWorkspaceFilesDirName))
}

func shouldSkipAgenticWorkspaceSource(rel string, entry fs.DirEntry) bool {
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	if rel == "" || rel == "." {
		return false
	}
	first, _, _ := strings.Cut(rel, "/")
	return first == workModeTmpWorkDirName
}

func detectAgenticWorkspaceBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(sample)
}

func hashAgenticWorkspaceFile(root, filename string) (agenticWorkspaceManifestEntry, error) {
	if err := rejectSymlinkPath(root, filename); err != nil {
		return agenticWorkspaceManifestEntry{}, err
	}
	file, err := os.Open(filename)
	if err != nil {
		return agenticWorkspaceManifestEntry{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return agenticWorkspaceManifestEntry{}, err
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	sample := make([]byte, 0, 8192)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			if len(sample) < cap(sample) {
				remaining := cap(sample) - len(sample)
				if remaining > n {
					remaining = n
				}
				sample = append(sample, buf[:remaining]...)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return agenticWorkspaceManifestEntry{}, readErr
		}
	}
	return agenticWorkspaceManifestEntry{
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		SizeBytes: info.Size(),
		Binary:    detectAgenticWorkspaceBinary(sample),
		Mode:      uint32(info.Mode().Perm()),
	}, nil
}

func collectAgenticWorkspaceManifest(root string, skipTmpWork bool) (map[string]agenticWorkspaceManifestEntry, error) {
	manifest := map[string]agenticWorkspaceManifestEntry{}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return manifest, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(root, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == root {
			return nil
		}
		rel, err := filepath.Rel(root, fullPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipTmpWork && shouldSkipAgenticWorkspaceSource(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
		item, err := hashAgenticWorkspaceFile(root, fullPath)
		if err != nil {
			return err
		}
		manifest[rel] = item
		return nil
	})
	return manifest, err
}

func copyAgenticWorkspaceFile(srcRoot, srcPath, dstRoot, dstPath string, mode os.FileMode) error {
	if err := rejectSymlinkPath(srcRoot, srcPath); err != nil {
		return err
	}
	if err := rejectSymlinkPath(dstRoot, dstPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".agentgo-stage-*")
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
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if mode.Perm() == 0 {
		mode = 0o644
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return err
	}
	ok = true
	return nil
}

func copyAgenticWorkspaceBaseline(srcRoot, dstRoot string) (map[string]agenticWorkspaceManifestEntry, error) {
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return nil, err
	}
	manifest := map[string]agenticWorkspaceManifestEntry{}
	err := filepath.WalkDir(srcRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == srcRoot {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, fullPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipAgenticWorkspaceSource(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if isSymlinkDirEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target, err := safeJoin(dstRoot, rel)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyAgenticWorkspaceFile(srcRoot, fullPath, dstRoot, target, info.Mode()); err != nil {
			return err
		}
		item, err := hashAgenticWorkspaceFile(dstRoot, target)
		if err != nil {
			return err
		}
		manifest[rel] = item
		return nil
	})
	return manifest, err
}

func saveAgenticWorkspaceTask(taskRoot string, task agenticWorkspaceTask) error {
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if task.CreatedAt == "" {
		task.CreatedAt = task.UpdatedAt
	}
	if task.Baseline == nil {
		task.Baseline = map[string]agenticWorkspaceManifestEntry{}
	}
	if task.RunNumber <= 0 {
		task.RunNumber = 1
	}
	if task.Decisions == nil {
		task.Decisions = map[string]string{}
	}
	task.Workspace = normalizeAgenticEnvironmentWorkspace(task.Workspace)
	task.TokenUsage = normalizeAgenticTaskTokenUsage(task.TokenUsage)
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWriteFile(agenticWorkspaceTaskPath(taskRoot), data, 0o600); err != nil {
		return err
	}
	return os.Chmod(agenticWorkspaceTaskPath(taskRoot), 0o600)
}

func loadAgenticWorkspaceTask(taskRoot string) (agenticWorkspaceTask, error) {
	data, err := os.ReadFile(agenticWorkspaceTaskPath(taskRoot))
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	var task agenticWorkspaceTask
	if err := json.Unmarshal(data, &task); err != nil {
		return agenticWorkspaceTask{}, fmt.Errorf("invalid agentic workspace metadata: %w", err)
	}
	if task.SchemaVersion != agenticWorkspaceSchemaVersion || !validAgenticWorkspaceSessionID(task.SessionID) || !isValidProjectName(task.ProjectName) {
		return agenticWorkspaceTask{}, errors.New("invalid agentic workspace metadata")
	}
	if task.Baseline == nil {
		task.Baseline = map[string]agenticWorkspaceManifestEntry{}
	}
	if task.RunNumber <= 0 {
		task.RunNumber = 1
	}
	if task.Decisions == nil {
		task.Decisions = map[string]string{}
	}
	task.Workspace = normalizeAgenticEnvironmentWorkspace(task.Workspace)
	task.TokenUsage = normalizeAgenticTaskTokenUsage(task.TokenUsage)
	return task, nil
}

func (a *App) startAgenticWorkspaceTask(projectName, mode string, maxRuns int) (agenticWorkspaceTask, string, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	taskID, err := newAgenticWorkspaceSessionID()
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	workspaceRoot, err := a.agenticWorkspaceFilesRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	baseline, err := copyAgenticWorkspaceBaseline(projectworkRoot, workspaceRoot)
	if err != nil {
		_ = os.RemoveAll(taskRoot)
		return agenticWorkspaceTask{}, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	task := agenticWorkspaceTask{
		SchemaVersion: agenticWorkspaceSchemaVersion,
		SessionID:     taskID,
		ProjectName:   projectName,
		Mode:          mode,
		MaxRuns:       maxRuns,
		RunNumber:     1,
		Status:        agenticWorkspaceStatusActive,
		Incomplete:    true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Baseline:      baseline,
		Decisions:     map[string]string{},
		Workspace:     newAgenticEnvironmentWorkspace(),
	}
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		_ = os.RemoveAll(taskRoot)
		return agenticWorkspaceTask{}, "", err
	}
	return task, workspaceRoot, nil
}

func (a *App) loadAgenticWorkspaceTask(projectName, taskID string) (agenticWorkspaceTask, string, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	if task.ProjectName != projectName {
		return agenticWorkspaceTask{}, "", errors.New("agentic workspace belongs to another project")
	}
	workspaceRoot, err := a.agenticWorkspaceFilesRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, "", err
	}
	if info, err := os.Stat(workspaceRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("agentic workspace path is not a directory")
		}
		return agenticWorkspaceTask{}, "", fmt.Errorf("agentic workspace is unavailable: %w", err)
	}
	return task, workspaceRoot, nil
}

func (a *App) startOrLoadAgenticWorkspaceTask(projectName string, request workModeAgenticRequest) (agenticWorkspaceTask, string, bool, error) {
	if taskID := strings.TrimSpace(request.TaskID); taskID != "" {
		task, root, err := a.loadAgenticWorkspaceTask(projectName, taskID)
		if err != nil {
			return task, root, false, err
		}
		unresolved, listErr := a.listRecoverableAgenticTasks(projectName)
		if listErr != nil {
			return agenticWorkspaceTask{}, "", false, listErr
		}
		for _, unresolvedTask := range unresolved {
			if unresolvedTask.TaskID != taskID {
				return agenticWorkspaceTask{}, "", false, fmt.Errorf("%w: another staged task (%s) must be resolved first", errAgenticUnresolvedTaskExists, unresolvedTask.TaskID)
			}
		}
		if task.Mode != request.Mode {
			return agenticWorkspaceTask{}, "", false, errors.New("agentic terminal mode cannot change during an active staged task")
		}
		switch task.Status {
		case agenticWorkspaceStatusInterrupted, agenticWorkspaceStatusMaximumRuns, agenticWorkspaceStatusAwaitingReview, agenticWorkspaceStatusPrepared:
			return agenticWorkspaceTask{}, "", false, fmt.Errorf("%w: review and merge/reject staged changes, or discard the task", errAgenticUnresolvedTaskExists)
		}
		if task.Mode == workModeAgenticModeFull && task.RunNumber >= task.MaxRuns {
			return agenticWorkspaceTask{}, "", false, errAgenticMaximumRunsReached
		}
		return task, root, false, nil
	}
	unresolved, err := a.listRecoverableAgenticTasks(projectName)
	if err != nil {
		return agenticWorkspaceTask{}, "", false, err
	}
	if len(unresolved) > 0 {
		return agenticWorkspaceTask{}, "", false, fmt.Errorf("%w (%d unresolved): use Review Changes to merge/reject progress or Discard Task", errAgenticUnresolvedTaskExists, len(unresolved))
	}
	task, root, err := a.startAgenticWorkspaceTask(projectName, request.Mode, request.MaxRuns)
	return task, root, true, err
}

func (a *App) updateAgenticWorkspaceTask(projectName, taskID, status string, incomplete bool, summary, progressSummary string, workspace map[string]any) (agenticWorkspaceTask, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	task.Status = strings.TrimSpace(status)
	task.Incomplete = incomplete
	task.Summary = strings.TrimSpace(summary)
	if strings.TrimSpace(progressSummary) != "" {
		task.ProgressSummary = boundAgenticContinuityText(progressSummary, agenticProgressSummaryLimit)
	}
	if workspace != nil {
		task.Workspace = workspace
	}
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		return agenticWorkspaceTask{}, err
	}
	return loadAgenticWorkspaceTask(taskRoot)
}

func (a *App) reactivateAgenticWorkspaceTask(projectName, taskID string) (agenticWorkspaceTask, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	if task.ProjectName != projectName {
		return agenticWorkspaceTask{}, errors.New("agentic workspace belongs to another project")
	}
	if task.Mode == workModeAgenticModeFull && task.Status != agenticWorkspaceStatusPrepared && task.RunNumber >= task.MaxRuns {
		return agenticWorkspaceTask{}, errAgenticMaximumRunsReached
	}
	if task.Status != agenticWorkspaceStatusPrepared {
		task.RunNumber++
	}
	task.Status = agenticWorkspaceStatusActive
	task.Incomplete = true
	task.Summary = "Agentic continuation turn started."
	// A continuation may change files that were previously merged or rejected.
	// Reset decisions so every current staged difference must be reviewed again.
	task.Decisions = map[string]string{}
	task.PendingCommand = nil
	task.PendingCommandAt = ""
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		return agenticWorkspaceTask{}, err
	}
	return loadAgenticWorkspaceTask(taskRoot)
}

func agenticWorkspaceDecision(task agenticWorkspaceTask, rel string) string {
	decision := strings.ToLower(strings.TrimSpace(task.Decisions[rel]))
	switch decision {
	case agenticWorkspaceDecisionMerged, agenticWorkspaceDecisionRejected:
		return decision
	default:
		return agenticWorkspaceDecisionPending
	}
}

func agenticWorkspaceConflict(kind string, baseline, staged, canonical agenticWorkspaceManifestEntry, baselineOK, stagedOK, canonicalOK bool) (bool, string) {
	switch kind {
	case agenticWorkspaceChangeAdded:
		if !canonicalOK || (stagedOK && canonical.SHA256 == staged.SHA256) {
			return false, ""
		}
		return true, "A file was added to canonical projectwork after this task started."
	case agenticWorkspaceChangeModified:
		if canonicalOK && stagedOK && canonical.SHA256 == staged.SHA256 {
			return false, ""
		}
		if !canonicalOK {
			return true, "The canonical file was deleted after this task started."
		}
		if baselineOK && canonical.SHA256 != baseline.SHA256 {
			return true, "The canonical file changed after this task started."
		}
	case agenticWorkspaceChangeDeleted:
		if !canonicalOK {
			return false, ""
		}
		if baselineOK && canonical.SHA256 != baseline.SHA256 {
			return true, "The canonical file changed after this task started."
		}
	}
	return false, ""
}

func (a *App) buildAgenticWorkspaceReview(projectName, taskID string) (agenticWorkspaceReview, error) {
	task, workspaceRoot, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	staged, err := collectAgenticWorkspaceManifest(workspaceRoot, false)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	canonicalRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	canonical, err := collectAgenticWorkspaceManifest(canonicalRoot, true)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	keys := map[string]bool{}
	for rel := range task.Baseline {
		keys[rel] = true
	}
	for rel := range staged {
		keys[rel] = true
	}
	rels := make([]string, 0, len(keys))
	for rel := range keys {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	review := agenticWorkspaceReview{
		TaskID:         task.SessionID,
		Mode:           task.Mode,
		Status:         task.Status,
		Incomplete:     task.Incomplete,
		Summary:        task.Summary,
		Workspace:      agenticWorkspaceDisplayPath(task.SessionID),
		CreatedAt:      task.CreatedAt,
		UpdatedAt:      task.UpdatedAt,
		RunNumber:      task.RunNumber,
		MaxRuns:        task.MaxRuns,
		Changes:        []agenticWorkspaceChange{},
		PendingCommand: cloneWorkModeAgenticCommand(task.PendingCommand),
		CommandRunning: a.agenticManualCommandRunning(projectName, taskID),
		TokenUsage:     normalizeAgenticTaskTokenUsage(task.TokenUsage),
	}
	for _, rel := range rels {
		baseline, baselineOK := task.Baseline[rel]
		stage, stageOK := staged[rel]
		kind := ""
		switch {
		case !baselineOK && stageOK:
			kind = agenticWorkspaceChangeAdded
		case baselineOK && !stageOK:
			kind = agenticWorkspaceChangeDeleted
		case baselineOK && stageOK && baseline.SHA256 != stage.SHA256:
			kind = agenticWorkspaceChangeModified
		default:
			review.UnchangedCount++
			continue
		}
		canonicalItem, canonicalOK := canonical[rel]
		conflict, reason := agenticWorkspaceConflict(kind, baseline, stage, canonicalItem, baselineOK, stageOK, canonicalOK)
		binary := stage.Binary
		size := stage.SizeBytes
		if kind == agenticWorkspaceChangeDeleted {
			binary = baseline.Binary
			size = baseline.SizeBytes
		}
		change := agenticWorkspaceChange{
			Path: rel, Kind: kind, Binary: binary, SizeBytes: size,
			Decision: agenticWorkspaceDecision(task, rel), Conflict: conflict, ConflictReason: reason,
		}
		review.Changes = append(review.Changes, change)
		switch kind {
		case agenticWorkspaceChangeAdded:
			review.AddedCount++
		case agenticWorkspaceChangeModified:
			review.ModifiedCount++
		case agenticWorkspaceChangeDeleted:
			review.DeletedCount++
		}
		if binary {
			review.BinaryCount++
		}
		if change.Decision == agenticWorkspaceDecisionPending {
			review.PendingCount++
			if conflict {
				review.ConflictCount++
			}
		}
	}
	review.Resolved = !review.Incomplete && review.PendingCount == 0
	if taskRoot, rootErr := a.agenticWorkspaceTaskRoot(projectName, taskID); rootErr == nil {
		if records, auditErr := readAgenticAuditRecords(taskRoot); auditErr == nil {
			review.AuditRecords = records
		}
	}
	return review, nil
}

func normalizeAgenticWorkspaceChangePath(raw string) (string, error) {
	clean := cleanSlashPath(raw)
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", errors.New("invalid agentic workspace file path")
	}
	return clean, nil
}

func findAgenticWorkspaceChange(review agenticWorkspaceReview, rel string) (agenticWorkspaceChange, bool) {
	for _, change := range review.Changes {
		if change.Path == rel {
			return change, true
		}
	}
	return agenticWorkspaceChange{}, false
}

func (a *App) applyAgenticWorkspaceMerge(projectName, taskID string, change agenticWorkspaceChange) error {
	if change.Conflict {
		return errors.New(change.ConflictReason)
	}
	workspaceRoot, err := a.agenticWorkspaceFilesRoot(projectName, taskID)
	if err != nil {
		return err
	}
	canonicalRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return err
	}
	target, err := safeJoin(canonicalRoot, change.Path)
	if err != nil {
		return err
	}
	if err := rejectSymlinkPath(canonicalRoot, target); err != nil {
		return err
	}
	if change.Kind == agenticWorkspaceChangeDeleted {
		info, statErr := os.Lstat(target)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.IsDir() || isSymlinkMode(info.Mode()) {
			return errors.New("agentic workspace merge target is not a regular file")
		}
		if err := removeFileUnderRoot(canonicalRoot, target); err != nil {
			return err
		}
		pruneEmptyDirsUnderRoot(canonicalRoot, filepath.Dir(target))
		return nil
	}
	source, err := safeJoin(workspaceRoot, change.Path)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("agentic workspace source is not a regular file")
	}
	return copyAgenticWorkspaceFile(workspaceRoot, source, canonicalRoot, target, info.Mode())
}

func (a *App) saveAgenticWorkspaceDecision(projectName, taskID, rel, decision string) error {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return err
	}
	if task.Decisions == nil {
		task.Decisions = map[string]string{}
	}
	task.Decisions[rel] = decision
	return saveAgenticWorkspaceTask(taskRoot, task)
}

func (a *App) resolveAgenticWorkspaceIfComplete(projectName, taskID string, review agenticWorkspaceReview) (agenticWorkspaceReview, error) {
	if review.PendingCount != 0 {
		return review, nil
	}
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return review, err
	}
	review.Resolved = true
	review.Deleted = true
	review.Message = "All staged changes were resolved. The staged workspace was removed."
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "resolved", Message: review.Message, Workspace: auditWorkspaceSummary(review)})
	if records, auditErr := readAgenticAuditRecords(taskRoot); auditErr == nil {
		review.AuditRecords = records
	}
	if err := os.RemoveAll(taskRoot); err != nil {
		return review, err
	}
	a.clearAgenticLiveStream(projectName, taskID)
	a.clearAgenticSemiSessionAllowances(projectName, taskID)
	return review, nil
}

func (a *App) mergeAgenticWorkspaceChange(projectName, taskID, rawPath string) (agenticWorkspaceReview, error) {
	if err := a.ensureAgenticTaskIdle(projectName, taskID); err != nil {
		return agenticWorkspaceReview{}, err
	}

	rel, err := normalizeAgenticWorkspaceChangePath(rawPath)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	change, ok := findAgenticWorkspaceChange(review, rel)
	if !ok {
		return agenticWorkspaceReview{}, errors.New("staged change not found")
	}
	if change.Decision != agenticWorkspaceDecisionPending {
		return agenticWorkspaceReview{}, errors.New("staged change is already resolved")
	}
	if err := a.applyAgenticWorkspaceMerge(projectName, taskID, change); err != nil {
		return agenticWorkspaceReview{}, err
	}
	if err := a.saveAgenticWorkspaceDecision(projectName, taskID, rel, agenticWorkspaceDecisionMerged); err != nil {
		return agenticWorkspaceReview{}, err
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "merged", Decision: agenticWorkspaceDecisionMerged, Path: rel, Message: "The user merged one staged change into canonical projectwork."})
	review, err = a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	return a.resolveAgenticWorkspaceIfComplete(projectName, taskID, review)
}

func (a *App) rejectAgenticWorkspaceChange(projectName, taskID, rawPath string) (agenticWorkspaceReview, error) {
	if err := a.ensureAgenticTaskIdle(projectName, taskID); err != nil {
		return agenticWorkspaceReview{}, err
	}

	rel, err := normalizeAgenticWorkspaceChangePath(rawPath)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	change, ok := findAgenticWorkspaceChange(review, rel)
	if !ok {
		return agenticWorkspaceReview{}, errors.New("staged change not found")
	}
	if change.Decision != agenticWorkspaceDecisionPending {
		return agenticWorkspaceReview{}, errors.New("staged change is already resolved")
	}
	if err := a.saveAgenticWorkspaceDecision(projectName, taskID, rel, agenticWorkspaceDecisionRejected); err != nil {
		return agenticWorkspaceReview{}, err
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "rejected", Decision: agenticWorkspaceDecisionRejected, Path: rel, Message: "The user rejected one staged change."})
	review, err = a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	return a.resolveAgenticWorkspaceIfComplete(projectName, taskID, review)
}

func (a *App) mergeAllAgenticWorkspaceChanges(projectName, taskID string) (agenticWorkspaceReview, error) {
	if err := a.ensureAgenticTaskIdle(projectName, taskID); err != nil {
		return agenticWorkspaceReview{}, err
	}

	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	pending := make([]agenticWorkspaceChange, 0, review.PendingCount)
	for _, change := range review.Changes {
		if change.Decision != agenticWorkspaceDecisionPending {
			continue
		}
		if change.Conflict {
			return agenticWorkspaceReview{}, fmt.Errorf("cannot merge %s: %s", change.Path, change.ConflictReason)
		}
		pending = append(pending, change)
	}
	for _, change := range pending {
		if err := a.applyAgenticWorkspaceMerge(projectName, taskID, change); err != nil {
			return agenticWorkspaceReview{}, fmt.Errorf("merge %s: %w", change.Path, err)
		}
	}
	for _, change := range pending {
		if err := a.saveAgenticWorkspaceDecision(projectName, taskID, change.Path, agenticWorkspaceDecisionMerged); err != nil {
			return agenticWorkspaceReview{}, err
		}
	}
	if len(pending) > 0 {
		if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
			a.logf("system", "warn", "Could not sync agentic workspace merge-all into active Builder workspace: %v", err)
		}
		_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "merged_all", Decision: agenticWorkspaceDecisionMerged, Message: fmt.Sprintf("The user merged all %d pending staged changes into canonical projectwork.", len(pending))})
	}
	review, err = a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	return a.resolveAgenticWorkspaceIfComplete(projectName, taskID, review)
}

func (a *App) discardAgenticWorkspaceTask(projectName, taskID string) (agenticWorkspaceReview, error) {
	if err := a.ensureAgenticTaskIdle(projectName, taskID); err != nil {
		return agenticWorkspaceReview{}, err
	}

	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	review.Resolved = true
	review.Deleted = true
	review.Message = "The staged agentic task was discarded and its workspace was removed."
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "discarded", Decision: "discarded", Message: review.Message, Workspace: auditWorkspaceSummary(review)})
	if records, auditErr := readAgenticAuditRecords(taskRoot); auditErr == nil {
		review.AuditRecords = records
	}
	if err := os.RemoveAll(taskRoot); err != nil {
		return agenticWorkspaceReview{}, err
	}
	a.clearAgenticLiveStream(projectName, taskID)
	a.clearAgenticSemiSessionAllowances(projectName, taskID)
	return review, nil
}

func (a *App) interruptAgenticWorkspaceTask(projectName, taskID, reason string) (agenticWorkspaceReview, error) {
	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	if !task.Incomplete {
		return a.buildAgenticWorkspaceReview(projectName, taskID)
	}
	summary := strings.TrimSpace(reason)
	if summary == "" {
		summary = "The agentic task was interrupted before all work was completed."
	}
	if _, err := a.clearAgenticPendingCommand(projectName, taskID, agenticWorkspaceStatusInterrupted, summary, true); err != nil {
		return agenticWorkspaceReview{}, err
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindStop, Status: agenticWorkspaceStatusInterrupted, Level: "warning", Message: summary})
	a.clearAgenticSemiSessionAllowances(projectName, taskID)
	return a.buildAgenticWorkspaceReview(projectName, taskID)
}

func (a *App) cleanupAgenticWorkspaceWhenNoChanges(projectName, taskID string, review agenticWorkspaceReview) (agenticWorkspaceReview, error) {
	if review.Incomplete || len(review.Changes) != 0 {
		return review, nil
	}
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return review, err
	}
	review.Resolved = true
	review.Deleted = true
	review.Message = "The agentic task completed without staged file changes. The clean staged workspace was removed."
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "clean_complete", Message: review.Message, Workspace: auditWorkspaceSummary(review)})
	if records, auditErr := readAgenticAuditRecords(taskRoot); auditErr == nil {
		review.AuditRecords = records
	}
	if err := os.RemoveAll(taskRoot); err != nil {
		return review, err
	}
	a.clearAgenticLiveStream(projectName, taskID)
	a.clearAgenticSemiSessionAllowances(projectName, taskID)
	return review, nil
}

func decodeAgenticWorkspaceJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *App) handleAgenticWorkspaceReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (a *App) handleAgenticWorkspaceMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspacePathRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	review, err := a.mergeAgenticWorkspaceChange(projectName, req.TaskID, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if _, err := a.syncActiveBuilderProjectsFromProjectwork(projectName); err != nil {
		a.logf("system", "warn", "Could not sync agentic workspace merge into active Builder workspace: %v", err)
	}
	writeJSON(w, http.StatusOK, review)
}

func (a *App) handleAgenticWorkspaceMergeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspaceTaskRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	review, err := a.mergeAllAgenticWorkspaceChanges(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (a *App) handleAgenticWorkspaceReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspacePathRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	review, err := a.rejectAgenticWorkspaceChange(projectName, req.TaskID, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (a *App) handleAgenticWorkspaceDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspaceTaskRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	review, err := a.discardAgenticWorkspaceTask(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (a *App) handleAgenticWorkspaceInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspaceTaskRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	review, err := a.interruptAgenticWorkspaceTask(projectName, req.TaskID, req.Reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func agenticWorkspaceAllUpdateable(root string) (map[string]bool, error) {
	manifest, err := collectAgenticWorkspaceManifest(root, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(manifest))
	for rel := range manifest {
		out[path.Clean(filepath.ToSlash(rel))] = true
	}
	return out, nil
}

func isReservedAgenticWorkspaceOutputPath(rel string) bool {
	rel = cleanSlashPath(rel)
	first, _, _ := strings.Cut(rel, "/")
	return first == agenticWorkspaceDirName || first == workModeTmpWorkDirName
}

func filterAgenticWorkspaceFileOps(ops []builderFileOp, projectName string) ([]builderFileOp, []string, []workModeBlockedFileOutput) {
	accepted := make([]builderFileOp, 0, len(ops))
	skipped := []string{}
	blocked := []workModeBlockedFileOutput{}
	for _, op := range ops {
		rel, err := normalizeWorkModeProjectworkRel(op.Path, projectName)
		if err != nil || !isReservedAgenticWorkspaceOutputPath(rel) {
			accepted = append(accepted, op)
			continue
		}
		reason := "reserved AgentGO workspace paths cannot be written by the AI"
		action := strings.ToLower(strings.TrimSpace(op.Action))
		skipped = append(skipped, rel+": "+reason)
		item := workModeBlockedFileOutput{Path: rel, Action: action, Reason: reason}
		if strings.TrimSpace(op.Content) != "" {
			item.Content = op.Content
		} else if strings.TrimSpace(op.ArtifactRef) != "" {
			item.ContentOmitted = true
			item.Content = "[artifact output omitted]"
		}
		blocked = append(blocked, item)
	}
	return accepted, skipped, blocked
}
