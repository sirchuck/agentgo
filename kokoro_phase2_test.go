package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentgo/internal/kokoroipc"
)

func prepareKokoroManagerTestAssets(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := ensureKokoroAssetLayout(root); err != nil {
		t.Fatal(err)
	}
	_, modelPath, voicesDir, _, _, helperPath := kokoroDefaultPaths(root)
	if err := os.WriteFile(modelPath, []byte("phase2-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voicesDir, "zz_local_file.bin"), []byte("voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(helperPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, []byte("placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeKokoroCommandFactory(mode string, starts *atomic.Int32) kokoroCommandFactory {
	return func(helperPath, assetRoot, instanceID string) *exec.Cmd {
		attempt := starts.Add(1)
		cmd := exec.Command(os.Args[0], "-test.run=TestKokoroPhase2FakeHelperProcess")
		cmd.Env = append(os.Environ(),
			"AGENTGO_KOKORO_FAKE_HELPER=1",
			"AGENTGO_KOKORO_FAKE_MODE="+mode,
			"AGENTGO_KOKORO_FAKE_ATTEMPT="+string(rune('0'+attempt)),
			"AGENTGO_KOKORO_INSTANCE="+instanceID,
		)
		return cmd
	}
}

func TestKokoroManagerHandshakeUsesHelperVoiceDiscovery(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	configPath := filepath.Join(root, "config.json")
	cfg := AppConfig{TTSVoice: "deleted_voice", TTSEngine: ttsEngineAuto}
	applyConfigDefaults(&cfg)
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{cfg: cfg, configPath: configPath, appRoot: root}
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("ready", &starts)
	app.kokoro = manager
	defer manager.Close()

	if err := manager.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	app.reconcileKokoroVoice(manager.Status().Voices)
	status := app.currentKokoroStatus()
	if !status.Available || !status.HelperRunning || !status.HandshakeReady || !status.ModelLoaded {
		t.Fatalf("status = %#v", status)
	}
	if len(status.Voices) != 2 || status.Voices[0] != "af_heart" || status.Voices[1] != "am_adam" {
		t.Fatalf("helper-discovered voices = %#v", status.Voices)
	}
	if status.Voices[0] == "zz_local_file" {
		t.Fatal("voice list came from filenames instead of the helper handshake")
	}

	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted AppConfig
	if err := json.Unmarshal(saved, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.TTSVoice != "af_heart" {
		t.Fatalf("persisted fallback voice = %q, want af_heart", persisted.TTSVoice)
	}
}

func TestKokoroManagerRequiresModelLoadedForAvailability(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("probe-only", &starts)
	defer manager.Close()

	if err := manager.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := manager.Status()
	if !status.Running || !status.HandshakeReady {
		t.Fatalf("helper handshake status = %#v", status)
	}
	if status.Available || status.ModelLoaded || status.State != "helper-ready" {
		t.Fatalf("probe-only helper was incorrectly marked available: %#v", status)
	}
}

func TestKokoroManagerOneAutomaticRestart(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("crash-once", &starts)
	defer manager.Close()

	if err := manager.Start(); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if starts.Load() >= 2 {
			status := manager.Status()
			if status.Running && status.HandshakeReady {
				if starts.Load() != 2 {
					t.Fatalf("helper starts = %d, want exactly 2", starts.Load())
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("automatic restart did not recover; starts=%d status=%#v", starts.Load(), manager.Status())
}

func TestKokoroPhase2FakeHelperProcess(t *testing.T) {
	if os.Getenv("AGENTGO_KOKORO_FAKE_HELPER") != "1" {
		return
	}
	instanceID := os.Getenv("AGENTGO_KOKORO_INSTANCE")
	mode := os.Getenv("AGENTGO_KOKORO_FAKE_MODE")
	attempt := os.Getenv("AGENTGO_KOKORO_FAKE_ATTEMPT")
	for {
		request, err := kokoroipc.ReadMessage(os.Stdin)
		if err != nil {
			os.Exit(0)
		}
		ref := request.ID
		switch request.Type {
		case "initialize":
			modelLoaded := mode != "probe-only"
			capabilities := []string{"ping", "list_voices", "shutdown"}
			if modelLoaded {
				capabilities = append(capabilities, "synthesize")
			}
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{
				Type:          "ready",
				Ref:           ref,
				Protocol:      kokoroipc.ProtocolVersion,
				HelperVersion: "fake-phase2",
				InstanceID:    instanceID,
				ModelLoaded:   modelLoaded,
				Voices:        []string{"am_adam", "af_heart", "af_heart"},
				Capabilities:  capabilities,
			})
			if mode == "crash-once" && attempt == "1" {
				time.Sleep(150 * time.Millisecond)
				os.Exit(23)
			}
		case "synthesize":
			if mode != "stream" && mode != "stream-timed" {
				_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "error", Ref: ref, Protocol: kokoroipc.ProtocolVersion, Error: "fake synthesis unavailable"})
				continue
			}
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "audio_start", Ref: ref, Protocol: kokoroipc.ProtocolVersion, SampleRate: 24000, Channels: 1, Encoding: "f32le"})
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "chunk_start", Ref: ref, Protocol: kokoroipc.ProtocolVersion, Text: request.Text, ChunkIndex: 0, CharStart: 0, CharEnd: len(request.Text)})
			if mode == "stream-timed" {
				_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{
					Type: "chunk_timing", Ref: ref, Protocol: kokoroipc.ProtocolVersion, ChunkIndex: 0,
					Timings: []kokoroipc.TimingSpan{{Kind: "word", CharStart: 0, CharEnd: 5, StartSample: 0, EndSample: 1}},
				})
			}
			pcm := make([]byte, 8)
			binary.LittleEndian.PutUint32(pcm[0:4], 0x3dcccccd)
			binary.LittleEndian.PutUint32(pcm[4:8], 0xbdcccccd)
			_ = kokoroipc.WriteAudioFrame(os.Stdout, ref, pcm)
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "chunk_end", Ref: ref, Protocol: kokoroipc.ProtocolVersion, ChunkIndex: 0, TotalSamples: 2})
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "complete", Ref: ref, Protocol: kokoroipc.ProtocolVersion, TotalSamples: 2})
		case "shutdown":
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "bye", Ref: ref, Protocol: kokoroipc.ProtocolVersion})
			os.Exit(0)
		case "ping":
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "pong", Ref: ref, Protocol: kokoroipc.ProtocolVersion})
		default:
			_ = kokoroipc.WriteMessage(os.Stdout, kokoroipc.Message{Type: "error", Ref: ref, Protocol: kokoroipc.ProtocolVersion, Error: "unsupported fake request " + strings.TrimSpace(request.Type)})
		}
	}
}

func TestKokoroManagerStreamsSynthesisFrames(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("stream", &starts)
	defer manager.Close()
	if err := manager.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var messageTypes []string
	var audioBytes int
	err := manager.Synthesize(context.Background(), "Hello from AgentGO.", "af_heart", 1.25, func(event kokoroStreamEvent) error {
		if event.Message != nil {
			messageTypes = append(messageTypes, event.Message.Type)
		}
		audioBytes += len(event.Audio)
		return nil
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if strings.Join(messageTypes, ",") != "audio_start,chunk_start,chunk_end,complete" {
		t.Fatalf("message types = %#v", messageTypes)
	}
	if audioBytes != 8 {
		t.Fatalf("audio bytes = %d, want 8", audioBytes)
	}
}
