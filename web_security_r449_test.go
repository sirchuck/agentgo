package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestR449AddsWebSecurityConfigFieldsWithoutDroppingExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"http_port":5226,"custom_future_field":"keep-me"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWebSecurityConfigFields(path); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["password"] != "" || raw["failed_password_count"] != float64(0) || raw["custom_future_field"] != "keep-me" {
		t.Fatalf("unexpected migrated config: %#v", raw)
	}
}

func TestR449PasswordGateAndFourFailureLockout(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"password":"secret","failed_password_count":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:          AppConfig{Password: "secret", FailedPasswordCount: 0},
		configPath:   configPath,
		authSessions: map[string]struct{}{},
		shutdownCh:   make(chan struct{}, 1),
	}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected"))
	}))

	home := httptest.NewRecorder()
	protected.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "AgentGO") || !strings.Contains(home.Body.String(), "Password") {
		t.Fatalf("expected password page, got status=%d body=%q", home.Code, home.Body.String())
	}

	api := httptest.NewRecorder()
	protected.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if api.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated browser API must be blocked, got %d", api.Code)
	}

	for want := 1; want <= 4; want++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		protected.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d returned %d", want, rec.Code)
		}
		if app.cfg.FailedPasswordCount != want {
			t.Fatalf("failed count=%d, want %d", app.cfg.FailedPasswordCount, want)
		}
	}

	locked := httptest.NewRecorder()
	protected.ServeHTTP(locked, httptest.NewRequest(http.MethodGet, "/", nil))
	if locked.Code != http.StatusLocked || !strings.Contains(locked.Body.String(), "failed_password_count") {
		t.Fatalf("expected locked page after fourth failure, got status=%d body=%q", locked.Code, locked.Body.String())
	}
}

func TestR449SuccessfulPasswordResetsCounterAndAuthorizesSession(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"password":"secret","failed_password_count":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:          AppConfig{Password: "secret", FailedPasswordCount: 3},
		configPath:   configPath,
		authSessions: map[string]struct{}{},
		shutdownCh:   make(chan struct{}, 1),
	}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected"))
	}))

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"secret"}`))
	protected.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK || app.cfg.FailedPasswordCount != 0 {
		t.Fatalf("successful login status=%d count=%d", login.Code, app.cfg.FailedPasswordCount)
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != agentGOSessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("expected HttpOnly AgentGO session cookie, got %#v", cookies)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.AddCookie(cookies[0])
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "protected" {
		t.Fatalf("authenticated request failed: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestR449ShutdownHandlerSignalsApplication(t *testing.T) {
	app := &App{shutdownCh: make(chan struct{}, 1)}
	rec := httptest.NewRecorder()
	app.handleShutdown(rec, httptest.NewRequest(http.MethodPost, "/api/shutdown", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("shutdown returned %d", rec.Code)
	}
	select {
	case <-app.shutdownCh:
	case <-time.After(time.Second):
		t.Fatal("shutdown signal was not delivered")
	}
}

func TestR449YetiMenuContainsShutdownUX(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`id="yetiShutdownBtn"`,
		`Are you sure you want to shutdown AgentGO?`,
		`Thanks for using AgentGO`,
		`fetch('/api/shutdown'`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r449 shutdown UI missing %q", want)
		}
	}
}
