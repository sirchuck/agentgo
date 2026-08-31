package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agentgo/internal/kokoroipc"
)

const helperVersion = "agentgo-kokoro-helper-phase5"

var kokoroEngineFactory = newKokoroEngine

type helperAssetStatus struct {
	ModelPath string
	Voices    []string
}

type heartbeatRecord struct {
	PID        int    `json:"pid"`
	InstanceID string `json:"instance_id"`
	HelperPath string `json:"helper_path"`
	UpdatedAt  string `json:"updated_at"`
}

type helperServer struct {
	w          io.Writer
	assetRoot  string
	instanceID string

	writeMu  sync.Mutex
	engineMu sync.Mutex
	engine   kokoroEngine
	assets   helperAssetStatus

	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

func main() {
	assets := flag.String("assets", "", "AgentGO Kokoro asset directory")
	instance := flag.String("instance", "", "AgentGO helper instance identifier")
	flag.Parse()

	assetRoot := strings.TrimSpace(*assets)
	if assetRoot == "" {
		assetRoot = strings.TrimSpace(os.Getenv("AGENTGO_KOKORO_ASSETS"))
	}
	if assetRoot == "" {
		log.Print("Kokoro helper: missing --assets")
		os.Exit(2)
	}
	absoluteRoot, err := filepath.Abs(assetRoot)
	if err == nil {
		assetRoot = absoluteRoot
	}
	instanceID := strings.TrimSpace(*instance)
	if instanceID == "" {
		instanceID = strings.TrimSpace(os.Getenv("AGENTGO_KOKORO_INSTANCE"))
	}

	stopHeartbeat := startHeartbeat(assetRoot, instanceID)
	defer stopHeartbeat()

	if err := serve(os.Stdin, os.Stdout, assetRoot, instanceID); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("Kokoro helper stopped: %v", err)
		os.Exit(1)
	}
}

func discoverAssets(assetRoot string) (helperAssetStatus, error) {
	modelPath := filepath.Join(assetRoot, "model.onnx")
	info, err := os.Stat(modelPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		if err == nil {
			err = fmt.Errorf("model.onnx is not a non-empty regular file")
		}
		return helperAssetStatus{}, fmt.Errorf("Kokoro model unavailable: %w", err)
	}
	voicesDir := filepath.Join(assetRoot, "voices")
	entries, err := os.ReadDir(voicesDir)
	if err != nil {
		return helperAssetStatus{}, fmt.Errorf("Kokoro voices unavailable: %w", err)
	}
	voices := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".bin" {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if name != "" {
			voices = append(voices, name)
		}
	}
	sort.Strings(voices)
	if len(voices) == 0 {
		return helperAssetStatus{}, fmt.Errorf("no supported Kokoro .bin voice files were found in %s", voicesDir)
	}
	return helperAssetStatus{ModelPath: modelPath, Voices: voices}, nil
}

func serve(r io.Reader, w io.Writer, assetRoot, instanceID string) error {
	s := &helperServer{
		w:          w,
		assetRoot:  assetRoot,
		instanceID: instanceID,
		active:     map[string]context.CancelFunc{},
	}
	defer s.close()
	for {
		req, err := kokoroipc.ReadMessage(r)
		if err != nil {
			return err
		}
		ref := strings.TrimSpace(req.ID)
		if ref == "" {
			ref = strings.TrimSpace(req.Ref)
		}
		if req.Protocol != 0 && req.Protocol != kokoroipc.ProtocolVersion {
			if err := s.fail(ref, fmt.Sprintf("unsupported Kokoro IPC protocol %d", req.Protocol)); err != nil {
				return err
			}
			continue
		}
		switch strings.ToLower(strings.TrimSpace(req.Type)) {
		case "initialize":
			if err := s.initialize(ref); err != nil {
				if writeErr := s.fail(ref, err.Error()); writeErr != nil {
					return writeErr
				}
			}
		case "list_voices":
			assets, assetErr := discoverAssets(assetRoot)
			if assetErr != nil {
				if err := s.fail(ref, assetErr.Error()); err != nil {
					return err
				}
				continue
			}
			if err := s.respond(kokoroipc.Message{Type: "voices", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: instanceID, Voices: assets.Voices}); err != nil {
				return err
			}
		case "ping":
			if err := s.respond(kokoroipc.Message{Type: "pong", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: instanceID}); err != nil {
				return err
			}
		case "cancel":
			target := strings.TrimSpace(req.Ref)
			if target == "" {
				target = strings.TrimSpace(req.Message)
			}
			s.cancel(target)
			if err := s.respond(kokoroipc.Message{Type: "cancel_ack", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: instanceID, Message: target}); err != nil {
				return err
			}
		case "shutdown":
			s.cancelAll()
			_ = s.respond(kokoroipc.Message{Type: "bye", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: instanceID})
			return nil
		case "synthesize":
			if strings.TrimSpace(req.ID) == "" {
				if err := s.fail(ref, "synthesize requires a request id"); err != nil {
					return err
				}
				continue
			}
			go s.synthesize(req)
		default:
			if err := s.fail(ref, "unknown Kokoro IPC request: "+strings.TrimSpace(req.Type)); err != nil {
				return err
			}
		}
	}
}

func (s *helperServer) initialize(ref string) error {
	assets, err := discoverAssets(s.assetRoot)
	if err != nil {
		return err
	}
	s.engineMu.Lock()
	if s.engine == nil {
		s.engine, err = kokoroEngineFactory(s.assetRoot)
		if err == nil {
			s.assets = assets
		}
	}
	engine := s.engine
	s.engineMu.Unlock()
	if err != nil {
		return fmt.Errorf("Kokoro native runtime initialization failed: %w", err)
	}
	if engine == nil {
		return fmt.Errorf("Kokoro native runtime did not initialize")
	}
	capabilities := []string{"asset-probe", "framed-ipc", "list_voices", "ping", "shutdown", "voice-discovery", "synthesize", "cancel", "stream-audio-f32le", "chunk-source-spans"}
	if engine.TimingSupported() {
		capabilities = append(capabilities, "word-timing-source-spans")
	}
	return s.respond(kokoroipc.Message{
		Type:           "ready",
		Ref:            ref,
		Protocol:       kokoroipc.ProtocolVersion,
		HelperVersion:  helperVersion,
		RuntimeVersion: engine.RuntimeVersion(),
		InstanceID:     s.instanceID,
		ModelName:      filepath.Base(assets.ModelPath),
		ModelLoaded:    true,
		Voices:         assets.Voices,
		Capabilities:   capabilities,
		SampleRate:     kokoroSampleRate,
		Channels:       kokoroChannels,
		Encoding:       kokoroAudioEncoding,
		Message:        "AgentGO Kokoro local neural synthesis is ready.",
	})
}

func (s *helperServer) synthesize(req kokoroipc.Message) {
	ref := strings.TrimSpace(req.ID)
	ctx, cancel := context.WithCancel(context.Background())
	s.activeMu.Lock()
	if previous := s.active[ref]; previous != nil {
		previous()
	}
	s.active[ref] = cancel
	s.activeMu.Unlock()
	defer func() {
		cancel()
		s.activeMu.Lock()
		delete(s.active, ref)
		s.activeMu.Unlock()
	}()

	s.engineMu.Lock()
	engine := s.engine
	s.engineMu.Unlock()
	if engine == nil {
		_ = s.fail(ref, "Kokoro model is not loaded")
		return
	}
	if err := validateSynthesisRequest(req.Text, req.Voice); err != nil {
		_ = s.fail(ref, err.Error())
		return
	}

	if err := s.respond(kokoroipc.Message{
		Type:       "audio_start",
		Ref:        ref,
		Protocol:   kokoroipc.ProtocolVersion,
		InstanceID: s.instanceID,
		SampleRate: kokoroSampleRate,
		Channels:   kokoroChannels,
		Encoding:   kokoroAudioEncoding,
	}); err != nil {
		return
	}

	chunkIndex := 0
	totalSamples := 0
	err := engine.Synthesize(ctx, req.Text, req.Voice, clampKokoroSpeed(req.Speed), func(chunk kokoroSynthesisChunk) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		index := chunkIndex
		chunkIndex++
		if err := s.respond(kokoroipc.Message{
			Type:       "chunk_start",
			Ref:        ref,
			Protocol:   kokoroipc.ProtocolVersion,
			InstanceID: s.instanceID,
			Text:       chunk.Source.Text,
			ChunkIndex: index,
			CharStart:  chunk.Source.CharStart,
			CharEnd:    chunk.Source.CharEnd,
			SampleRate: kokoroSampleRate,
			Channels:   kokoroChannels,
			Encoding:   kokoroAudioEncoding,
		}); err != nil {
			return err
		}
		if len(chunk.Timings) > 0 {
			timings := make([]kokoroipc.TimingSpan, 0, len(chunk.Timings))
			for _, timing := range chunk.Timings {
				timings = append(timings, kokoroipc.TimingSpan{
					Kind:        "word",
					CharStart:   timing.CharStart,
					CharEnd:     timing.CharEnd,
					StartSample: timing.StartSample,
					EndSample:   timing.EndSample,
				})
			}
			if err := s.respond(kokoroipc.Message{
				Type:       "chunk_timing",
				Ref:        ref,
				Protocol:   kokoroipc.ProtocolVersion,
				InstanceID: s.instanceID,
				ChunkIndex: index,
				Timings:    timings,
			}); err != nil {
				return err
			}
		}
		for _, frame := range splitPCMFrames(chunk.PCM) {
			if err := ctx.Err(); err != nil {
				return err
			}
			totalSamples += len(frame)
			if err := s.respondAudio(ref, float32PCMBytes(frame)); err != nil {
				return err
			}
		}
		return s.respond(kokoroipc.Message{
			Type:         "chunk_end",
			Ref:          ref,
			Protocol:     kokoroipc.ProtocolVersion,
			InstanceID:   s.instanceID,
			ChunkIndex:   index,
			TotalSamples: len(chunk.PCM),
		})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			_ = s.respond(kokoroipc.Message{Type: "cancelled", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: s.instanceID})
			return
		}
		_ = s.fail(ref, err.Error())
		return
	}
	_ = s.respond(kokoroipc.Message{
		Type:         "complete",
		Ref:          ref,
		Protocol:     kokoroipc.ProtocolVersion,
		InstanceID:   s.instanceID,
		TotalSamples: totalSamples,
		SampleRate:   kokoroSampleRate,
		Channels:     kokoroChannels,
		Encoding:     kokoroAudioEncoding,
	})
}

func (s *helperServer) respond(msg kokoroipc.Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return kokoroipc.WriteMessage(s.w, msg)
}

func (s *helperServer) respondAudio(ref string, pcm []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return kokoroipc.WriteAudioFrame(s.w, ref, pcm)
}

func (s *helperServer) fail(ref, message string) error {
	return s.respond(kokoroipc.Message{Type: "error", Ref: ref, Protocol: kokoroipc.ProtocolVersion, InstanceID: s.instanceID, Error: message})
}

func (s *helperServer) cancel(ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	s.activeMu.Lock()
	cancel := s.active[ref]
	s.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *helperServer) cancelAll() {
	s.activeMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.active))
	for _, cancel := range s.active {
		cancels = append(cancels, cancel)
	}
	s.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *helperServer) close() {
	s.cancelAll()
	s.engineMu.Lock()
	engine := s.engine
	s.engine = nil
	s.engineMu.Unlock()
	if engine != nil {
		if err := engine.Close(); err != nil {
			log.Printf("Kokoro engine cleanup warning: %v", err)
		}
	}
}

func startHeartbeat(assetRoot, instanceID string) func() {
	if strings.TrimSpace(instanceID) == "" {
		return func() {}
	}
	runtimeDir := filepath.Join(assetRoot, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		log.Printf("Kokoro heartbeat directory unavailable: %v", err)
		return func() {}
	}
	path := filepath.Join(runtimeDir, "kokoro-helper-heartbeat.json")
	helperPath, _ := os.Executable()
	stop := make(chan struct{})
	done := make(chan struct{})
	write := func() {
		record := heartbeatRecord{PID: os.Getpid(), InstanceID: instanceID, HelperPath: helperPath, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		data, err := json.Marshal(record)
		if err != nil {
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err == nil {
			_ = os.Rename(tmp, path)
		}
	}
	write()
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				write()
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		data, err := os.ReadFile(path)
		if err == nil {
			var record heartbeatRecord
			if json.Unmarshal(data, &record) == nil && record.PID == os.Getpid() && record.InstanceID == instanceID {
				_ = os.Remove(path)
			}
		}
	}
}
