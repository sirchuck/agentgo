package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR450PasswordUsesNamedEnvironmentVariableWhenPresent(t *testing.T) {
	const envName = "AGENTGO_R450_PASSWORD_TEST"
	t.Setenv(envName, "secret-from-environment")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"password":"`+envName+`","failed_password_count":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:          AppConfig{Password: envName, FailedPasswordCount: 2},
		configPath:   configPath,
		authSessions: map[string]struct{}{},
	}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"secret-from-environment"}`))
	protected.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("environment-backed password login returned %d: %s", login.Code, login.Body.String())
	}
	if app.cfg.FailedPasswordCount != 0 {
		t.Fatalf("successful environment-backed login should reset failed count, got %d", app.cfg.FailedPasswordCount)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["password"] != envName {
		t.Fatalf("resolved environment secret must not be written to config; password=%#v", raw["password"])
	}
	if raw["failed_password_count"] != float64(0) {
		t.Fatalf("successful login should persist failed_password_count=0, got %#v", raw["failed_password_count"])
	}
}

func TestR450PasswordFallsBackToLiteralWhenEnvironmentVariableMissingOrEmpty(t *testing.T) {
	const missingName = "AGENTGO_R450_MISSING_PASSWORD_TEST"
	_ = os.Unsetenv(missingName)
	if got := resolveConfiguredWebPassword(missingName); got != missingName {
		t.Fatalf("missing environment variable should use literal configured password: got %q want %q", got, missingName)
	}

	const emptyName = "AGENTGO_R450_EMPTY_PASSWORD_TEST"
	t.Setenv(emptyName, "")
	if got := resolveConfiguredWebPassword(emptyName); got != emptyName {
		t.Fatalf("empty environment variable should use literal configured password: got %q want %q", got, emptyName)
	}
}

func TestR450BlankConfiguredPasswordStillDisablesPasswordProtection(t *testing.T) {
	if got := resolveConfiguredWebPassword(""); got != "" {
		t.Fatalf("blank configured password should remain blank, got %q", got)
	}
	app := &App{cfg: AppConfig{Password: ""}}
	if app.passwordProtectionEnabled() {
		t.Fatal("blank password should keep web password protection disabled")
	}
}
