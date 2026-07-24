package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActiveSessionMismatchDiagnosticsSuppressDuplicatesAndLogRecovery(t *testing.T) {
	app := newActiveSessionTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/session/active", nil)
		req.Header.Set(activeSessionHeader, "stale-browser-session")
		req.RemoteAddr = "192.0.2.25:5000"
		rr := httptest.NewRecorder()
		app.handleActiveSession(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("mismatch status=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	logs := output.String()
	if count := strings.Count(logs, "AgentGO received stale session ID"); count != 1 {
		t.Fatalf("stale warning count=%d, want 1\n%s", count, logs)
	}
	if strings.Contains(logs, "stale-browser-session") || strings.Contains(logs, session.SessionID) {
		t.Fatalf("session secret appeared in diagnostics: %s", logs)
	}
	if !strings.Contains(logs, "route=/api/session/active") || !strings.Contains(logs, "repeated warnings for this session pair are suppressed") {
		t.Fatalf("stale warning missing route or suppression notice: %s", logs)
	}

	regularMatchedReq := httptest.NewRequest(http.MethodGet, "/api/session/active", nil)
	regularMatchedReq.Header.Set(activeSessionHeader, session.SessionID)
	regularMatchedRR := httptest.NewRecorder()
	app.handleActiveSession(regularMatchedRR, regularMatchedReq)
	if count := strings.Count(output.String(), "AgentGO session reconnected"); count != 0 {
		t.Fatalf("ordinary active-tab traffic incorrectly reset mismatch suppression: %s", output.String())
	}

	matchedReq := httptest.NewRequest(http.MethodGet, "/api/session/active", nil)
	matchedReq.Header.Set(activeSessionHeader, session.SessionID)
	matchedReq.Header.Set(activeSessionReconnectHeader, "1")
	matchedReq.RemoteAddr = "192.0.2.25:5000"
	matchedRR := httptest.NewRecorder()
	app.handleActiveSession(matchedRR, matchedReq)
	if matchedRR.Code != http.StatusOK {
		t.Fatalf("matched status=%d body=%s", matchedRR.Code, matchedRR.Body.String())
	}

	logs = output.String()
	if count := strings.Count(logs, "AgentGO session reconnected"); count != 1 {
		t.Fatalf("recovery log count=%d, want 1\n%s", count, logs)
	}
	if !strings.Contains(logs, "suppressed_mismatch_attempts=2") {
		t.Fatalf("recovery log missing suppressed count: %s", logs)
	}

	newStaleReq := httptest.NewRequest(http.MethodGet, "/api/session/active", nil)
	newStaleReq.Header.Set(activeSessionHeader, "different-stale-session")
	newStaleRR := httptest.NewRecorder()
	app.handleActiveSession(newStaleRR, newStaleReq)
	if count := strings.Count(output.String(), "AgentGO received stale session ID"); count != 2 {
		t.Fatalf("new stale pair did not produce one new warning; count=%d\n%s", count, output.String())
	}
}

func TestReconnectAttemptHeaderLogsMatchedMutationOnce(t *testing.T) {
	app := newActiveSessionTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/settings", strings.NewReader(`{"modelId":"1","memoryDefault":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(activeSessionHeader, session.SessionID)
	req.Header.Set(activeSessionReconnectHeader, "1")
	rr := httptest.NewRecorder()
	app.handleWorkModeSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("matched mutation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if count := strings.Count(output.String(), "AgentGO session reconnected"); count != 1 {
		t.Fatalf("matched reconnect log count=%d, want 1\n%s", count, output.String())
	}
}
