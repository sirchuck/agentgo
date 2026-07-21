package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	activeSessionFilename = "activesession.json"
	activeSessionHeader   = "X-AgentGO-Session-ID"
	activeSessionSchema   = 1
)

type activeSessionObserver struct {
	Enabled bool   `json:"enabled"`
	AIID    string `json:"ai_id,omitempty"`
}

type activeSessionFile struct {
	SchemaVersion int                   `json:"schema_version"`
	SessionID     string                `json:"session_id"`
	ActiveProject string                `json:"active_project"`
	ActiveAIIDs   []string              `json:"active_ai_ids"`
	Observer      activeSessionObserver `json:"observer"`
	CreatedAt     string                `json:"created_at"`
	UpdatedAt     string                `json:"updated_at"`
}

type activeSessionStatusResponse struct {
	Active  bool               `json:"active"`
	Match   bool               `json:"match"`
	Status  string             `json:"status"`
	Session *activeSessionFile `json:"session,omitempty"`
}

type activeSessionClaimRequest struct {
	Mode                string `json:"mode"`
	Reason              string `json:"reason,omitempty"`
	Source              string `json:"source,omitempty"`
	ClientActiveProject string `json:"clientActiveProject,omitempty"`
	Path                string `json:"path,omitempty"`
	VisibilityState     string `json:"visibilityState,omitempty"`
}

func newActiveSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (a *App) activeSessionPath() string {
	return filepath.Join(a.cfg.WorkRoot, activeSessionFilename)
}

func normalizeActiveSessionFile(session activeSessionFile) activeSessionFile {
	session.SchemaVersion = activeSessionSchema
	session.SessionID = strings.TrimSpace(session.SessionID)
	session.ActiveProject = strings.TrimSpace(session.ActiveProject)
	session.Observer.AIID = strings.TrimSpace(session.Observer.AIID)
	seen := map[string]bool{}
	ids := make([]string, 0, len(session.ActiveAIIDs))
	for _, id := range session.ActiveAIIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || id == session.Observer.AIID {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	session.ActiveAIIDs = ids
	if !session.Observer.Enabled {
		session.Observer.AIID = ""
	}
	return session
}

func (a *App) activeSessionSnapshot() (activeSessionFile, bool) {
	a.activeSessionMu.Lock()
	defer a.activeSessionMu.Unlock()
	if strings.TrimSpace(a.activeSession.SessionID) == "" {
		return activeSessionFile{}, false
	}
	copySession := a.activeSession
	copySession.ActiveAIIDs = append([]string(nil), a.activeSession.ActiveAIIDs...)
	return copySession, true
}

func (a *App) tryActiveSessionSnapshot() (activeSessionFile, bool, bool) {
	if !a.activeSessionMu.TryLock() {
		return activeSessionFile{}, false, false
	}
	defer a.activeSessionMu.Unlock()
	if strings.TrimSpace(a.activeSession.SessionID) == "" {
		return activeSessionFile{}, false, true
	}
	copySession := a.activeSession
	copySession.ActiveAIIDs = append([]string(nil), a.activeSession.ActiveAIIDs...)
	return copySession, true, true
}

func (a *App) persistActiveSession(session activeSessionFile) error {
	session = normalizeActiveSessionFile(session)
	if session.SessionID == "" {
		return errors.New("active session requires a session ID")
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Keep the on-disk record and in-memory snapshot ordered as one operation.
	// This mutex is dedicated to active-session persistence and never aliases the
	// broader application-state mutex.
	a.activeSessionMu.Lock()
	defer a.activeSessionMu.Unlock()
	if err := atomicWriteFile(a.activeSessionPath(), data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(a.activeSessionPath(), 0o600); err != nil {
		return err
	}
	a.activeSession = session
	return nil
}

func (a *App) clearActiveSession() error {
	a.activeSessionMu.Lock()
	defer a.activeSessionMu.Unlock()
	if err := os.Remove(a.activeSessionPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.activeSession = activeSessionFile{}
	return nil
}

func (a *App) rotateActiveSession(projectName string) (activeSessionFile, error) {
	id, err := newActiveSessionID()
	if err != nil {
		return activeSessionFile{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	session := activeSessionFile{
		SchemaVersion: activeSessionSchema,
		SessionID:     id,
		ActiveProject: strings.TrimSpace(projectName),
		ActiveAIIDs:   []string{},
		Observer:      activeSessionObserver{},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := a.persistActiveSession(session); err != nil {
		return activeSessionFile{}, err
	}
	return session, nil
}

func (a *App) syncActiveSessionFromRuntime() error {
	session, ok := a.activeSessionSnapshot()
	if !ok {
		return nil
	}
	a.mu.RLock()
	projectName := strings.TrimSpace(a.activeProjectName)
	builderIDs := []string{}
	for id, enabled := range a.toggles {
		if enabled && id != a.reviewerID {
			builderIDs = append(builderIDs, id)
		}
	}
	observerID := strings.TrimSpace(a.reviewerID)
	a.mu.RUnlock()
	if projectName == "" || projectName != session.ActiveProject {
		return nil
	}
	sort.Strings(builderIDs)
	session.ActiveAIIDs = builderIDs
	session.Observer = activeSessionObserver{Enabled: observerID != "", AIID: observerID}
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return a.persistActiveSession(session)
}

func (a *App) restoreActiveSession() error {
	data, err := os.ReadFile(a.activeSessionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var session activeSessionFile
	if err := json.Unmarshal(data, &session); err != nil {
		return err
	}
	session = normalizeActiveSessionFile(session)
	if session.SessionID == "" || (session.ActiveProject != "" && !isValidProjectName(session.ActiveProject)) {
		return a.clearActiveSession()
	}
	if session.ActiveProject == "" {
		session.ActiveAIIDs = []string{}
		session.Observer = activeSessionObserver{}
		session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return a.persistActiveSession(session)
	}
	projects, err := a.listProjects()
	if err != nil {
		return err
	}
	projectFound := false
	for _, project := range projects {
		if project.Name == session.ActiveProject {
			projectFound = true
			break
		}
	}
	if !projectFound {
		return a.clearActiveSession()
	}
	validModels := map[string]bool{}
	for _, model := range a.cfg.Models {
		validModels[modelIDString(model.ID)] = true
	}
	a.mu.Lock()
	a.activeProjectName = session.ActiveProject
	for id := range a.toggles {
		a.toggles[id] = false
	}
	restoredBuilderIDs := make([]string, 0, len(session.ActiveAIIDs))
	for _, id := range session.ActiveAIIDs {
		if validModels[id] {
			a.toggles[id] = true
			restoredBuilderIDs = append(restoredBuilderIDs, id)
		}
	}
	session.ActiveAIIDs = restoredBuilderIDs
	a.reviewerID = ""
	if session.Observer.Enabled && validModels[session.Observer.AIID] {
		a.reviewerID = session.Observer.AIID
		a.toggles[session.Observer.AIID] = false
	} else {
		session.Observer = activeSessionObserver{}
	}
	a.mu.Unlock()
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return a.persistActiveSession(session)
}

func activeSessionIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(activeSessionHeader))
}

func (a *App) activeSessionMatchesRequest(r *http.Request) bool {
	session, ok := a.activeSessionSnapshot()
	if !ok {
		return true
	}
	return activeSessionIDFromRequest(r) == session.SessionID
}

func (a *App) requireActiveSessionMatch(w http.ResponseWriter, r *http.Request) bool {
	if a.activeSessionMatchesRequest(r) {
		return true
	}
	if activeSessionIDFromRequest(r) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "session_unattached",
			"message": "This page is not attached to AgentGO's active session. Refresh to start a new session.",
		})
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":   "session_mismatch",
		"message": "A newer AgentGO session is active. Refresh this tab or choose Make This Tab Active.",
	})
	return false
}

func (a *App) handleActiveSessionClaim(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req activeSessionClaimRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	requestSessionID := activeSessionIDFromRequest(r)
	switch mode {
	case "fresh":
		if requestSessionID != "" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "session_claim_requires_empty_key",
				"message": "A fresh page claim must not include an existing session key.",
			})
			return
		}
	case "takeover":
		if requestSessionID == "" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "session_takeover_requires_stale_key",
				"message": "A stale session key is required to activate this tab.",
			})
			return
		}
	default:
		http.Error(w, "mode must be fresh or takeover", http.StatusBadRequest)
		return
	}

	diag := sessionResetDiagnostics{
		Reason:              strings.TrimSpace(req.Reason),
		Source:              strings.TrimSpace(req.Source),
		RemoteAddr:          r.RemoteAddr,
		UserAgent:           strings.TrimSpace(r.UserAgent()),
		Referer:             strings.TrimSpace(r.Referer()),
		Origin:              strings.TrimSpace(r.Header.Get("Origin")),
		ClientActiveProject: strings.TrimSpace(req.ClientActiveProject),
		ClientPath:          strings.TrimSpace(req.Path),
		VisibilityState:     strings.TrimSpace(req.VisibilityState),
	}
	if diag.Reason == "" {
		diag.Reason = "browser session claim"
	}
	if diag.Source == "" {
		diag.Source = "frontend.sessionClaim"
	}
	session, projects, err := a.resetBrowserSession(diag, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logf("system", "info", "Activated browser session %s using %s claim", session.SessionID, mode)
	writeJSON(w, http.StatusOK, projectListResponse{ActiveProject: "", Projects: projects, SessionID: session.SessionID})
}

func (a *App) handleActiveSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := a.activeSessionSnapshot()
	if !ok {
		writeJSON(w, http.StatusOK, activeSessionStatusResponse{Active: false, Match: activeSessionIDFromRequest(r) == "", Status: "none"})
		return
	}
	match := activeSessionIDFromRequest(r) == session.SessionID
	status := "mismatch"
	if match {
		status = "matched"
	} else if activeSessionIDFromRequest(r) == "" {
		status = "unattached"
	}
	// A stale or unattached browser may learn that a session exists and which
	// project owns it, but it must not receive the key needed to impersonate it.
	if !match {
		session.SessionID = ""
	}
	writeJSON(w, http.StatusOK, activeSessionStatusResponse{Active: true, Match: match, Status: status, Session: &session})
}
