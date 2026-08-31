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

func TestKokoroPhase1AssetLayoutAndStatus(t *testing.T) {
	root := t.TempDir()
	if err := ensureKokoroAssetLayout(root); err != nil {
		t.Fatalf("ensureKokoroAssetLayout: %v", err)
	}
	assetsDir, modelPath, voicesDir, runtimeDir, readmePath, helperPath := kokoroDefaultPaths(root)
	for _, dir := range []string{assetsDir, voicesDir, runtimeDir, filepath.Join(assetsDir, kokoroLicensesDirname)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("Kokoro directory %s missing: %v", dir, err)
		}
	}
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{kokoroKnownModelRepo, kokoroKnownModelSHA256, "assets/kokoro/model.onnx", "does **not** enforce that checksum"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("Kokoro.md missing %q", want)
		}
	}

	missing := inspectKokoroAssets(root)
	if missing.Available || missing.ModelPresent || missing.VoicesPresent || missing.AssetsPresent {
		t.Fatalf("unexpected Kokoro status before assets: %#v", missing)
	}
	if missing.State != "missing" || missing.ReadmePath != "assets/kokoro/Kokoro.md" {
		t.Fatalf("missing status = %#v", missing)
	}

	if err := os.WriteFile(modelPath, []byte("test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voicesDir, "af_heart.bin"), []byte("voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, []byte("helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	present := inspectKokoroAssets(root)
	if !present.ModelPresent || !present.VoicesPresent || !present.HelperPresent || !present.AssetsPresent {
		t.Fatalf("expected assets to be detected: %#v", present)
	}
	if present.Available {
		t.Fatal("Phase 1 must not mark Kokoro available before the helper handshake")
	}
	if present.State != "initializing" {
		t.Fatalf("state = %q, want initializing", present.State)
	}
}

func TestTTSPhase1DefaultsAndPersistence(t *testing.T) {
	cfg := AppConfig{TTSVoice: "Saved System Voice"}
	applyConfigDefaults(&cfg)
	if !ttsEnabled(cfg) || !ttsHighlightEnabled(cfg) {
		t.Fatalf("speech/highlight defaults = %v/%v, want true/true", ttsEnabled(cfg), ttsHighlightEnabled(cfg))
	}
	if cfg.TTSSpeed != 1.0 || cfg.TTSEngine != ttsEngineAuto {
		t.Fatalf("speed/engine defaults = %v/%q", cfg.TTSSpeed, cfg.TTSEngine)
	}
	if got := ttsLegacyVoice(cfg); got != "Saved System Voice" {
		t.Fatalf("legacy migration voice = %q", got)
	}

	root := t.TempDir()
	if err := ensureKokoroAssetLayout(root); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	initial := `{"agentgo_file":"config","file_version":1,"bind_host":"127.0.0.1","http_port":5226,"https_port":0,"tls_cert_file":"","tls_key_file":"","work_root":"work","tts_voice":"Saved System Voice","max_response_history":50,"risk_mode_max_iterations":10,"outfit_run_retention":50,"auto_merge_single_builder_waves":true,"prompt_version":1,"wiretap":{"max_wiretap_entries":200,"default_runtime_slice_entries":75,"max_runtime_slice_entries":150}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: cfg, configPath: configPath, appRoot: root}
	req := httptest.NewRequest(http.MethodPost, "/api/config/tts", strings.NewReader(`{"tts_enabled":false,"tts_speed":1.25,"tts_highlight":false,"tts_engine":"legacy","tts_legacy_voice":"Legacy Voice"}`))
	rr := httptest.NewRecorder()
	app.handleTTSConfig(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response ttsConfigResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TTSEnabled || response.TTSHighlight || response.TTSSpeed != 1.25 || response.TTSEngine != ttsEngineLegacy || response.TTSLegacyVoice != "Legacy Voice" {
		t.Fatalf("response = %#v", response)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["tts_voice"] != "Saved System Voice" || saved["tts_legacy_voice"] != "Legacy Voice" || saved["tts_speed"] != 1.25 || saved["tts_engine"] != "legacy" {
		t.Fatalf("saved TTS settings = %#v", saved)
	}
	if saved["tts_enabled"] != false || saved["tts_highlight"] != false {
		t.Fatalf("saved booleans = enabled %#v highlight %#v", saved["tts_enabled"], saved["tts_highlight"])
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/config/tts", strings.NewReader(`{"tts_speed":1.75}`))
	badRR := httptest.NewRecorder()
	app.handleTTSConfig(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("invalid speed status = %d, want 400", badRR.Code)
	}
}

func TestKokoroPhase1TemplateHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"Speech Speed",
		`min="0.75" max="1.50"`,
		`<optgroup label="Kokoro">`,
		`<optgroup label="Other">`,
		"Local Kokoro voices are not installed. Using system speech.",
		"assets/kokoro/Kokoro.md",
		"/api/tts/status",
		"tts_enabled",
		"tts_speed",
		"tts_highlight",
		"tts_legacy_voice",
		"function reconcileAgentGOKokoroVoice()",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("template missing %q", want)
		}
	}
}
