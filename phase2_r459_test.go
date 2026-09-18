package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR459PasswordFreeInstallShowsStartupSplashAndCreatesSession(t *testing.T) {
	app := &App{
		cfg:          AppConfig{Password: ""},
		authSessions: map[string]struct{}{},
	}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("APP"))
	}))

	landing := httptest.NewRecorder()
	protected.ServeHTTP(landing, httptest.NewRequest(http.MethodGet, "/", nil))
	if landing.Code != http.StatusOK {
		t.Fatalf("password-free splash status = %d", landing.Code)
	}
	body := landing.Body.String()
	for _, required := range []string{
		"You can set a password in your <strong>config.json</strong> file.",
		"/assets/yetiagent.png",
		"background-size:cover",
		"backdrop-filter:blur(12px)",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("password-free splash missing %q", required)
		}
	}
	if strings.Contains(body, `id="password"`) {
		t.Fatal("password-free splash should not display a password input")
	}
	if strings.Contains(body, "APP") {
		t.Fatal("password-free splash should appear before the main application on first load")
	}

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":""}`))
	protected.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("password-free Enter returned %d: %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != agentGOSessionCookieName || strings.TrimSpace(cookies[0].Value) == "" {
		t.Fatalf("password-free Enter did not create startup session cookie: %#v", cookies)
	}

	appPage := httptest.NewRecorder()
	appReq := httptest.NewRequest(http.MethodGet, "/", nil)
	appReq.AddCookie(cookies[0])
	protected.ServeHTTP(appPage, appReq)
	if got := appPage.Body.String(); got != "APP" {
		t.Fatalf("root after password-free Enter = %q, want APP", got)
	}
}

func TestR459ConfiguredPasswordKeepsPasswordEntryAndSplashArtwork(t *testing.T) {
	app := &App{
		cfg:          AppConfig{Password: "secret"},
		authSessions: map[string]struct{}{},
	}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("APP"))
	}))

	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `id="password"`) || !strings.Contains(body, `placeholder="Password"`) {
		t.Fatal("configured-password splash must keep the password field")
	}
	if strings.Contains(body, "You can set a password in your") {
		t.Fatal("configured-password splash should not show the password-free guidance")
	}
	if !strings.Contains(body, "/assets/yetiagent.png") {
		t.Fatal("configured-password splash should use the r459 artwork")
	}
}

func TestR459StartupArtworkRemainsPublicBeforeLogin(t *testing.T) {
	app := &App{cfg: AppConfig{Password: "secret"}, authSessions: map[string]struct{}{}}
	protected := app.withWebPasswordProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/yetiagent.png" {
			t.Fatalf("unexpected passthrough path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/yetiagent.png", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("startup artwork status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestR459ModelDiscoveryReturnsCredentialAwareErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","message":"x-api-key header is required"}`))
	}))
	defer server.Close()

	t.Run("missing credential", func(t *testing.T) {
		app := &App{}
		body := `{"provider":"claude","adapter":"anthropic_messages","base_url":` + stringMustJSON(server.URL) + `,"auth_type":"header_key","auth_header":"x-api-key"}`
		recorder := httptest.NewRecorder()
		app.handleModelDiscovery(recorder, httptest.NewRequest(http.MethodPost, "/api/models/discover", strings.NewReader(body)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["code"] != "authentication_required" {
			t.Fatalf("code = %#v, want authentication_required", payload["code"])
		}
		if strings.Contains(strings.ToLower(r459StringValue(payload["error"])), "x-api-key header is required") {
			t.Fatalf("user-facing error exposed provider JSON: %#v", payload["error"])
		}
		if !strings.Contains(r459StringValue(payload["detail"]), "x-api-key header is required") {
			t.Fatalf("technical detail should retain provider diagnostics: %#v", payload["detail"])
		}
	})

	t.Run("rejected credential", func(t *testing.T) {
		app := &App{}
		body := `{"provider":"claude","adapter":"anthropic_messages","base_url":` + stringMustJSON(server.URL) + `,"auth_type":"header_key","auth_header":"x-api-key","api_key":"wrong-key"}`
		recorder := httptest.NewRecorder()
		app.handleModelDiscovery(recorder, httptest.NewRequest(http.MethodPost, "/api/models/discover", strings.NewReader(body)))
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["code"] != "authentication_failed" {
			t.Fatalf("code = %#v, want authentication_failed", payload["code"])
		}
		if !strings.Contains(r459StringValue(payload["error"]), "Authentication failed") {
			t.Fatalf("friendly error = %#v", payload["error"])
		}
	})
}

func TestR459ModelDiscoveryUsesEnteredEnvironmentVariable(t *testing.T) {
	const envName = "AGENTGO_R459_DISCOVERY_KEY"
	t.Setenv(envName, "environment-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer environment-key" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"environment-model"}]}`))
	}))
	defer server.Close()

	result, err := discoverProviderModels(context.Background(), modelDiscoveryRequest{
		Provider:  "openai",
		Adapter:   "openai_responses",
		BaseURL:   server.URL,
		AuthType:  "bearer",
		APIKeyEnv: envName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 1 || result.Models[0].ModelName != "environment-model" {
		t.Fatalf("models = %#v", result.Models)
	}
}

func TestR459ModelDiscoveryFrontendHasInlineCredentialFlow(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, required := range []string{
		"renderModelDiscoveryCredentialPrompt",
		"modelNamePickerApiKey",
		"modelNamePickerApiKeyEnv",
		"Load Available Models",
		"normalKey.value = apiKey",
		"normalEnv.value = apiKeyEnv",
		"authentication_required",
		"authentication_failed",
		"Technical details",
		"Enter model name manually…",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("r459 inline model-discovery UI missing %q", required)
		}
	}
}

func TestR459StartupArtworkIsPackaged(t *testing.T) {
	info, err := os.Stat(filepath.Join("assets", "yetiagent.png"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 100_000 {
		t.Fatalf("startup artwork unexpectedly small: %d bytes", info.Size())
	}
}

func r459StringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
