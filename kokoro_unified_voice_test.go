package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKokoroUnifiedVoiceSelectorA031(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`<optgroup label="Kokoro">`,
		`<optgroup label="Other">`,
		`id="yetiTTSVoiceSelect"`,
		`id="yetiTestVoiceBtn"`,
		`function saveAgentGOTTSUnifiedVoice(`,
		`tts_voice: choice.voice, tts_engine: 'auto'`,
		`tts_legacy_voice: choice.voice, tts_engine: 'legacy'`,
		`speakWorkModeReplyText(null, 'This is the selected agent go voice.', { restart:true })`,
		`Kokoro files are never downloaded automatically.`,
		`When Kokoro is installed, AgentGO loads the model into memory at startup and keeps it ready until AgentGO quits.`,
		`Speech Speed`,
		`Speech enabled`,
		`Word highlight`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("unified voice UI missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`id="yetiTTSEngineSelect"`,
		`id="yetiTTSLegacyVoiceSelect"`,
		`Advanced Speech Settings`,
		`>Speech Engine<`,
		`>Legacy Browser/System Voice<`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("unified voice UI still exposes obsolete control %q", forbidden)
		}
	}
}

func TestKokoroUnifiedVoiceLoadsInstalledKokoroRegardlessOfSpeechToggle(t *testing.T) {
	root := t.TempDir()
	if err := ensureKokoroAssetLayout(root); err != nil {
		t.Fatal(err)
	}
	_, modelPath, voicesDir, _, _, helperPath := kokoroDefaultPaths(root)
	if err := os.WriteFile(modelPath, []byte("test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voicesDir, "af_heart.bin"), []byte("test-voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(helperPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, []byte("test-helper"), 0o755); err != nil {
		t.Fatal(err)
	}

	disabled := false
	app := &App{appRoot: root, cfg: AppConfig{TTSEnabled: &disabled, TTSEngine: ttsEngineLegacy}}
	if !app.kokoroWanted() {
		t.Fatal("a complete local Kokoro install must load at AgentGO startup even when Speech Enabled is off")
	}

	enabled := true
	app.cfg.TTSEnabled = &enabled
	if !app.kokoroWanted() {
		t.Fatal("changing Speech Enabled must not control the Kokoro model lifetime")
	}

	if err := os.Remove(helperPath); err != nil {
		t.Fatal(err)
	}
	if app.kokoroWanted() {
		t.Fatal("an incomplete Kokoro install must not be started")
	}
}
