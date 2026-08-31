package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentgo/internal/kokoroipc"
)

func startPhase3FakeKokoro(t *testing.T) (*App, *kokoroManager) {
	t.Helper()
	root := prepareKokoroManagerTestAssets(t)
	configPath := filepath.Join(root, "config.json")
	cfg := AppConfig{TTSVoice: "af_heart", TTSEngine: ttsEngineAuto, TTSSpeed: 1.25}
	applyConfigDefaults(&cfg)
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("stream", &starts)
	if err := manager.Start(); err != nil {
		manager.Close()
		t.Fatalf("Start: %v", err)
	}
	app := &App{cfg: cfg, configPath: configPath, appRoot: root, kokoro: manager}
	return app, manager
}

func TestKokoroSynthesisHTTPStreamsMetadataAndRawPCM(t *testing.T) {
	app, manager := startPhase3FakeKokoro(t)
	defer manager.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/tts/kokoro/synthesize", strings.NewReader(`{"text":"Hello from AgentGO.","voice":"af_heart","speed":1.25}`))
	rr := httptest.NewRecorder()
	app.handleKokoroSynthesis(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/vnd.agentgo.kokoro-stream" {
		t.Fatalf("content type = %q", got)
	}

	reader := bytes.NewReader(rr.Body.Bytes())
	var messages []string
	var audio []byte
	for {
		kind, payload, err := kokoroipc.ReadFrame(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case kokoroipc.FrameJSON:
			message, err := kokoroipc.DecodeMessage(payload)
			if err != nil {
				t.Fatal(err)
			}
			messages = append(messages, message.Type)
		case kokoroipc.FrameAudio:
			audio = append(audio, payload...)
		}
	}
	if strings.Join(messages, ",") != "audio_start,chunk_start,chunk_end,complete" {
		t.Fatalf("message sequence = %#v", messages)
	}
	if len(audio) != 8 {
		t.Fatalf("PCM bytes = %d, want 8", len(audio))
	}
}

func TestKokoroSynthesisHTTPRejectsUnavailableVoice(t *testing.T) {
	app, manager := startPhase3FakeKokoro(t)
	defer manager.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/tts/kokoro/synthesize", strings.NewReader(`{"text":"Hello","voice":"deleted_voice"}`))
	rr := httptest.NewRecorder()
	app.handleKokoroSynthesis(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestKokoroPhase3TemplateStreamingHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"/api/tts/kokoro/synthesize",
		"function speakWorkModeReplyTextKokoro(",
		"function readWorkModeKokoroStream(",
		"function updateWorkModeReplySpeechChunkHighlight(",
		"new AbortController()",
		"createBufferSource()",
		"getFloat32(index * 4, true)",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("template missing %q", want)
		}
	}
}

func TestKokoroSynthesisHTTPDoesNotDuplicateHelperErrorFrame(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	configPath := filepath.Join(root, "config.json")
	cfg := AppConfig{TTSVoice: "af_heart", TTSEngine: ttsEngineAuto, TTSSpeed: 1.0}
	applyConfigDefaults(&cfg)
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("ready", &starts)
	if err := manager.Start(); err != nil {
		manager.Close()
		t.Fatalf("Start: %v", err)
	}
	defer manager.Close()
	app := &App{cfg: cfg, configPath: configPath, appRoot: root, kokoro: manager}

	req := httptest.NewRequest(http.MethodPost, "/api/tts/kokoro/synthesize", strings.NewReader(`{"text":"This will fail once.","voice":"af_heart"}`))
	rr := httptest.NewRecorder()
	app.handleKokoroSynthesis(rr, req)
	reader := bytes.NewReader(rr.Body.Bytes())
	errorFrames := 0
	for {
		kind, payload, err := kokoroipc.ReadFrame(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if kind != kokoroipc.FrameJSON {
			continue
		}
		msg, err := kokoroipc.DecodeMessage(payload)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Type == "error" {
			errorFrames++
		}
	}
	if errorFrames != 1 {
		t.Fatalf("error frames = %d, want exactly 1", errorFrames)
	}
}
