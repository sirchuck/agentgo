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

func startPhase4TimedFakeKokoro(t *testing.T) (*App, *kokoroManager) {
	t.Helper()
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
	manager.commandFactory = fakeKokoroCommandFactory("stream-timed", &starts)
	if err := manager.Start(); err != nil {
		manager.Close()
		t.Fatalf("Start: %v", err)
	}
	app := &App{cfg: cfg, configPath: configPath, appRoot: root, kokoro: manager}
	return app, manager
}

func TestKokoroPhase4HTTPPreservesWordTimingMetadata(t *testing.T) {
	app, manager := startPhase4TimedFakeKokoro(t)
	defer manager.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/tts/kokoro/synthesize", strings.NewReader(`{"text":"Hello AgentGO.","voice":"af_heart","speed":1.0}`))
	rr := httptest.NewRecorder()
	app.handleKokoroSynthesis(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	reader := bytes.NewReader(rr.Body.Bytes())
	var types []string
	var timing *kokoroipc.Message
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
		types = append(types, msg.Type)
		if msg.Type == "chunk_timing" {
			copy := msg
			timing = &copy
		}
	}
	if got := strings.Join(types, ","); got != "audio_start,chunk_start,chunk_timing,chunk_end,complete" {
		t.Fatalf("message sequence = %q", got)
	}
	if timing == nil || len(timing.Timings) != 1 {
		t.Fatalf("timing message = %#v", timing)
	}
	span := timing.Timings[0]
	if span.Kind != "word" || span.CharStart != 0 || span.CharEnd != 5 || span.StartSample != 0 || span.EndSample != 1 {
		t.Fatalf("timing span = %#v", span)
	}
}

func TestKokoroPhase4TemplateSchedulesExactSourceHighlights(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"function updateWorkModeReplySpeechExactHighlight(",
		"function scheduleWorkModeKokoroTimingHighlights(",
		"type === 'chunk_timing'",
		"timing.start_sample",
		"timing.end_sample",
		"timing.char_start",
		"timing.char_end",
		"chunk.playbackStartTime",
		"updateWorkModeReplySpeechChunkHighlight(chunk.charStart, chunk.charEnd, session)",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("template missing %q", want)
		}
	}
}

func TestKokoroPhase4DocumentationPinsTimestampFP32Candidate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", "kokoro", "Kokoro.md"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"onnx-community/Kokoro-82M-v1.0-ONNX-timestamped",
		"651ea8291843a92276a4a003581a215cb07d15e47dde6fcfb1b768f9a1682054",
		"pred_dur",
		"chunk/sentence highlighting",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Kokoro.md missing %q", want)
		}
	}
}
