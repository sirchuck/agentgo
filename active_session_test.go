package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newActiveSessionTestApp(t *testing.T) *App {
	t.Helper()
	app := &App{
		cfg: AppConfig{
			WorkRoot: t.TempDir(),
			Models: []ModelConfig{
				{ID: 1, Label: "Builder", WorkDir: "models/builder"},
				{ID: 2, Label: "Second Builder", WorkDir: "models/second-builder"},
				{ID: 3, Label: "Observer", WorkDir: "models/observer"},
			},
		},
		toggles:                     map[string]bool{"1": false, "2": false, "3": false},
		activeCancels:               map[string]activeCancelEntry{},
		pendingMergeCountsByProject: map[string]map[string]int{},
		waveExecutionsByProject:     map[string]waveExecutionState{},
		waveStatusByProject:         map[string]waveStatusState{},
	}
	for _, name := range []string{"Demo", "Second"} {
		if err := app.ensureProjectScaffold(name); err != nil {
			t.Fatalf("ensure project %s: %v", name, err)
		}
	}
	app.activeProjectName = "Demo"
	return app
}

func TestActiveSessionPersistsAndRestoresProjectAIAndObserver(t *testing.T) {
	app := newActiveSessionTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.SessionID) != 48 {
		t.Fatalf("session ID length = %d, want 48 hex chars", len(session.SessionID))
	}
	app.mu.Lock()
	app.toggles["1"] = true
	app.toggles["2"] = true
	app.reviewerID = "3"
	app.mu.Unlock()
	if err := app.syncActiveSessionFromRuntime(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(app.activeSessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var persisted activeSessionFile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SessionID != session.SessionID || persisted.ActiveProject != "Demo" {
		t.Fatalf("persisted session = %#v", persisted)
	}
	if strings.Join(persisted.ActiveAIIDs, ",") != "1,2" || !persisted.Observer.Enabled || persisted.Observer.AIID != "3" {
		t.Fatalf("persisted AI state = %#v observer=%#v", persisted.ActiveAIIDs, persisted.Observer)
	}

	restored := &App{
		cfg:                         app.cfg,
		toggles:                     map[string]bool{"1": false, "2": false, "3": false},
		activeCancels:               map[string]activeCancelEntry{},
		pendingMergeCountsByProject: map[string]map[string]int{},
		waveExecutionsByProject:     map[string]waveExecutionState{},
		waveStatusByProject:         map[string]waveStatusState{},
	}
	if err := restored.restoreActiveSession(); err != nil {
		t.Fatal(err)
	}
	if restored.activeProject() != "Demo" || !restored.toggles["1"] || !restored.toggles["2"] || restored.toggles["3"] || restored.reviewerID != "3" {
		t.Fatalf("restored runtime project=%q toggles=%#v observer=%q", restored.activeProject(), restored.toggles, restored.reviewerID)
	}
	restoredSession, ok := restored.activeSessionSnapshot()
	if !ok || restoredSession.SessionID != session.SessionID {
		t.Fatalf("restored session = %#v ok=%v", restoredSession, ok)
	}
}

func TestActiveSessionStatusDoesNotRevealIDToUnattachedBrowser(t *testing.T) {
	app := newActiveSessionTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}

	unattached := httptest.NewRecorder()
	app.handleActiveSession(unattached, httptest.NewRequest(http.MethodGet, "/api/session/active", nil))
	if unattached.Code != http.StatusOK {
		t.Fatalf("unattached status=%d body=%s", unattached.Code, unattached.Body.String())
	}
	var noKey activeSessionStatusResponse
	if err := json.Unmarshal(unattached.Body.Bytes(), &noKey); err != nil {
		t.Fatal(err)
	}
	if noKey.Match || noKey.Status != "unattached" || noKey.Session == nil || noKey.Session.SessionID != "" || noKey.Session.ActiveProject != "Demo" {
		t.Fatalf("unattached response = %#v", noKey)
	}

	matchedRequest := httptest.NewRequest(http.MethodGet, "/api/session/active", nil)
	matchedRequest.Header.Set(activeSessionHeader, session.SessionID)
	matched := httptest.NewRecorder()
	app.handleActiveSession(matched, matchedRequest)
	var withKey activeSessionStatusResponse
	if err := json.Unmarshal(matched.Body.Bytes(), &withKey); err != nil {
		t.Fatal(err)
	}
	if !withKey.Match || withKey.Status != "matched" || withKey.Session == nil || withKey.Session.SessionID != session.SessionID {
		t.Fatalf("matched response = %#v", withKey)
	}
}

func TestSessionMismatchRejectsMutationWithoutReplacingBackendSession(t *testing.T) {
	app := newActiveSessionTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(app.activeSessionPath())
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/work-mode/settings", strings.NewReader(`{"modelId":"1","memoryDefault":"new"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(activeSessionHeader, "wrong-session")
	recorder := httptest.NewRecorder()
	app.handleWorkModeSettings(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "session_mismatch") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	after, err := os.ReadFile(app.activeSessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("mismatched browser changed activesession.json")
	}
	current, ok := app.activeSessionSnapshot()
	if !ok || current.SessionID != session.SessionID {
		t.Fatalf("active session changed: %#v ok=%v", current, ok)
	}
}

func TestRotatingActiveSessionChangesID(t *testing.T) {
	app := newActiveSessionTestApp(t)
	first, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.rotateActiveSession("Second")
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID || second.ActiveProject != "Second" {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestProjectSelectionCreatesAndRotatesBrowserSessionID(t *testing.T) {
	app := newActiveSessionTestApp(t)
	app.activeProjectName = ""

	firstRequest := httptest.NewRequest(http.MethodPost, "/api/projects/select", strings.NewReader(`{"name":"Demo"}`))
	firstRequest.Header.Set("Content-Type", "application/json")
	firstRecorder := httptest.NewRecorder()
	app.handleSelectProject(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first selection status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	var first projectListResponse
	if err := json.Unmarshal(firstRecorder.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.ActiveProject != "Demo" || first.SessionID == "" {
		t.Fatalf("first selection response = %#v", first)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/api/projects/select", strings.NewReader(`{"name":"Second"}`))
	secondRequest.Header.Set("Content-Type", "application/json")
	secondRequest.Header.Set(activeSessionHeader, first.SessionID)
	secondRecorder := httptest.NewRecorder()
	app.handleSelectProject(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second selection status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	var second projectListResponse
	if err := json.Unmarshal(secondRecorder.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.ActiveProject != "Second" || second.SessionID == "" || second.SessionID == first.SessionID {
		t.Fatalf("second selection response = %#v firstID=%q", second, first.SessionID)
	}
}

func TestActiveSessionPersistsAndRestoresFreshUnselectedSession(t *testing.T) {
	app := newActiveSessionTestApp(t)
	app.activeProjectName = ""
	session, err := app.rotateActiveSession("")
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID == "" || session.ActiveProject != "" {
		t.Fatalf("fresh session = %#v", session)
	}

	restored := &App{
		cfg:                         app.cfg,
		toggles:                     map[string]bool{"1": false, "2": false, "3": false},
		activeCancels:               map[string]activeCancelEntry{},
		pendingMergeCountsByProject: map[string]map[string]int{},
		waveExecutionsByProject:     map[string]waveExecutionState{},
		waveStatusByProject:         map[string]waveStatusState{},
	}
	if err := restored.restoreActiveSession(); err != nil {
		t.Fatal(err)
	}
	current, ok := restored.activeSessionSnapshot()
	if !ok || current.SessionID != session.SessionID || current.ActiveProject != "" || restored.activeProject() != "" {
		t.Fatalf("restored fresh session=%#v ok=%v activeProject=%q", current, ok, restored.activeProject())
	}
}

func TestFreshSessionClaimReplacesExistingSessionAndResetsRuntime(t *testing.T) {
	app := newActiveSessionTestApp(t)
	old, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.toggles["1"] = true
	app.reviewerID = "3"
	app.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/session/claim", strings.NewReader(`{"mode":"fresh","reason":"fresh page load","source":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleActiveSessionClaim(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response projectListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SessionID == "" || response.SessionID == old.SessionID || response.ActiveProject != "" {
		t.Fatalf("claim response=%#v old=%q", response, old.SessionID)
	}
	current, ok := app.activeSessionSnapshot()
	if !ok || current.SessionID != response.SessionID || current.ActiveProject != "" {
		t.Fatalf("current=%#v ok=%v", current, ok)
	}
	if app.activeProject() != "" || app.toggles["1"] || app.reviewerID != "" {
		t.Fatalf("runtime was not reset: project=%q toggles=%#v observer=%q", app.activeProject(), app.toggles, app.reviewerID)
	}
	data, err := os.ReadFile(app.activeSessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var persisted activeSessionFile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SessionID != response.SessionID || persisted.ActiveProject != "" {
		t.Fatalf("persisted=%#v", persisted)
	}
}

func TestStaleSessionTakeoverRequiresKeyAndCreatesFreshSession(t *testing.T) {
	app := newActiveSessionTestApp(t)
	old, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}

	missingKey := httptest.NewRequest(http.MethodPost, "/api/session/claim", strings.NewReader(`{"mode":"takeover"}`))
	missingKey.Header.Set("Content-Type", "application/json")
	missingRecorder := httptest.NewRecorder()
	app.handleActiveSessionClaim(missingRecorder, missingKey)
	if missingRecorder.Code != http.StatusConflict || !strings.Contains(missingRecorder.Body.String(), "session_takeover_requires_stale_key") {
		t.Fatalf("missing-key status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	takeover := httptest.NewRequest(http.MethodPost, "/api/session/claim", strings.NewReader(`{"mode":"takeover","reason":"stale tab takeover"}`))
	takeover.Header.Set("Content-Type", "application/json")
	takeover.Header.Set(activeSessionHeader, "stale-browser-key")
	takeoverRecorder := httptest.NewRecorder()
	app.handleActiveSessionClaim(takeoverRecorder, takeover)
	if takeoverRecorder.Code != http.StatusOK {
		t.Fatalf("takeover status=%d body=%s", takeoverRecorder.Code, takeoverRecorder.Body.String())
	}
	var response projectListResponse
	if err := json.Unmarshal(takeoverRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SessionID == "" || response.SessionID == old.SessionID || response.ActiveProject != "" {
		t.Fatalf("takeover response=%#v old=%q", response, old.SessionID)
	}
}
