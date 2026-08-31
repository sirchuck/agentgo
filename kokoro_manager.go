package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentgo/internal/kokoroipc"
)

const (
	kokoroHandshakeTimeout    = 120 * time.Second
	kokoroShutdownTimeout     = 2 * time.Second
	kokoroRestartDelay        = 500 * time.Millisecond
	kokoroHeartbeatFreshLimit = 10 * time.Second
	kokoroStaleExitTimeout    = 5 * time.Second
	kokoroProcessStateFile    = "kokoro-helper-state.json"
	kokoroHeartbeatFile       = "kokoro-helper-heartbeat.json"
)

type kokoroRuntimeStatus struct {
	Started         bool
	Running         bool
	Available       bool
	HandshakeReady  bool
	ModelLoaded     bool
	State           string
	Message         string
	Voices          []string
	ProtocolVersion int
	HelperVersion   string
	HelperPath      string
}

type kokoroProcessRecord struct {
	PID        int    `json:"pid"`
	InstanceID string `json:"instance_id"`
	HelperPath string `json:"helper_path"`
	OwnerPID   int    `json:"owner_pid,omitempty"`
	OwnerPath  string `json:"owner_path,omitempty"`
	StartedAt  string `json:"started_at"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type kokoroCommandFactory func(helperPath, assetRoot, instanceID string) *exec.Cmd

type kokoroStreamEvent struct {
	Message *kokoroipc.Message
	Audio   []byte
}

type kokoroStream struct {
	events chan kokoroStreamEvent
	done   chan struct{}
}

type kokoroManager struct {
	assetRoot string
	appRoot   string
	logf      func(string, string, string, ...any)

	startMu sync.Mutex
	mu      sync.Mutex
	writeMu sync.Mutex

	commandFactory kokoroCommandFactory
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	controller     *agenticProcessController
	done           chan struct{}
	waiters        map[string]chan kokoroipc.Message
	streams        map[string]*kokoroStream
	seq            uint64

	started          bool
	running          bool
	available        bool
	handshakeReady   bool
	modelLoaded      bool
	state            string
	message          string
	voices           []string
	protocolVersion  int
	helperVersion    string
	helperPath       string
	instanceID       string
	intentionalStop  bool
	everReady        bool
	restartAttempted bool
	closed           bool
}

func newKokoroManager(appRoot string, logf func(string, string, string, ...any)) *kokoroManager {
	root := strings.TrimSpace(appRoot)
	if root == "" {
		root = "."
	}
	manager := &kokoroManager{
		appRoot:   root,
		assetRoot: kokoroAssetsDir(root),
		logf:      logf,
		state:     "not-started",
		waiters:   map[string]chan kokoroipc.Message{},
		streams:   map[string]*kokoroStream{},
	}
	manager.commandFactory = func(helperPath, assetRoot, instanceID string) *exec.Cmd {
		cmd := exec.Command(helperPath, "--assets", assetRoot, "--instance", instanceID)
		cmd.Dir = filepath.Dir(helperPath)
		cmd.Env = append(os.Environ(),
			"AGENTGO_KOKORO_ASSETS="+assetRoot,
			"AGENTGO_KOKORO_INSTANCE="+instanceID,
		)
		return cmd
	}
	return manager
}

func newKokoroInstanceID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func cleanKokoroVoiceList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		voice := strings.TrimSpace(value)
		if voice == "" || seen[voice] {
			continue
		}
		seen[voice] = true
		out = append(out, voice)
	}
	sort.Strings(out)
	return out
}

func kokoroCapabilitiesContain(values []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}

func (m *kokoroManager) processRecordPaths() (string, string) {
	runtimeDir := filepath.Join(m.assetRoot, kokoroRuntimeDirname)
	return filepath.Join(runtimeDir, kokoroProcessStateFile), filepath.Join(runtimeDir, kokoroHeartbeatFile)
}

func (m *kokoroManager) Status() kokoroRuntimeStatus {
	if m == nil {
		return kokoroRuntimeStatus{State: "unavailable", Message: "Kokoro manager is unavailable.", Voices: []string{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return kokoroRuntimeStatus{
		Started:         m.started,
		Running:         m.running,
		Available:       m.available,
		HandshakeReady:  m.handshakeReady,
		ModelLoaded:     m.modelLoaded,
		State:           m.state,
		Message:         m.message,
		Voices:          append([]string(nil), m.voices...),
		ProtocolVersion: m.protocolVersion,
		HelperVersion:   m.helperVersion,
		HelperPath:      m.helperPath,
	}
}

func (m *kokoroManager) setUnavailable(state, message string) {
	m.mu.Lock()
	m.available = false
	m.state = state
	m.message = strings.TrimSpace(message)
	m.mu.Unlock()
}

func (m *kokoroManager) discoverPaths() (string, string, error) {
	_, modelPath, voicesDir, _, _, _ := kokoroDefaultPaths(m.appRoot)
	if !regularFileExists(modelPath) {
		return modelPath, "", fmt.Errorf("Kokoro model is missing")
	}
	if len(kokoroVoiceFiles(voicesDir)) == 0 {
		return modelPath, "", fmt.Errorf("Kokoro voice files are missing")
	}
	helperPath, ok := findKokoroHelper(m.appRoot)
	if !ok {
		return modelPath, helperPath, fmt.Errorf("Kokoro helper is missing")
	}
	return modelPath, helperPath, nil
}

func (m *kokoroManager) Start() error {
	if m == nil {
		return fmt.Errorf("Kokoro manager is unavailable")
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("Kokoro manager is closed")
	}
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.intentionalStop = false
	m.state = "checking"
	m.message = "Checking local Kokoro assets."
	m.mu.Unlock()

	_, helperPath, err := m.discoverPaths()
	if err != nil {
		m.setUnavailable("missing", err.Error())
		return err
	}
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		m.setUnavailable("failed", "Kokoro helper path could not be resolved.")
		return err
	}
	assetRoot, err := filepath.Abs(m.assetRoot)
	if err != nil {
		assetRoot = m.assetRoot
	}

	if err := m.cleanupStaleHelper(helperPath); err != nil {
		// Never start a second ONNX model while ownership of a previous helper is
		// uncertain. A healthy helper already owned by this manager is reused by
		// the m.running check above; cross-process helpers cannot be safely reused
		// because their stdin/stdout IPC belongs to the owning AgentGO process.
		m.setUnavailable("busy", "A previous Kokoro helper is still active; AgentGO did not load a second model.")
		if m.logf != nil {
			m.logf("system", "warning", "Kokoro prior-helper cleanup blocked startup: %v", err)
		}
		return err
	}

	instanceID, err := newKokoroInstanceID()
	if err != nil {
		m.setUnavailable("failed", "Could not create a Kokoro helper instance identifier.")
		return err
	}
	factory := m.commandFactory
	if factory == nil {
		return fmt.Errorf("Kokoro command factory is unavailable")
	}
	cmd := factory(helperPath, assetRoot, instanceID)
	if cmd == nil {
		return fmt.Errorf("Kokoro command factory returned no command")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.setUnavailable("failed", "Could not open Kokoro helper input.")
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.setUnavailable("failed", "Could not open Kokoro helper output.")
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.setUnavailable("failed", "Could not open Kokoro helper diagnostics.")
		return err
	}
	controller, err := newAgenticProcessController(cmd)
	if err != nil {
		m.setUnavailable("failed", "Could not prepare Kokoro process containment.")
		return err
	}

	m.mu.Lock()
	m.state = "starting"
	m.message = "Starting the local Kokoro helper."
	m.helperPath = helperPath
	m.instanceID = instanceID
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		_ = controller.Close()
		m.setUnavailable("failed", "Kokoro helper could not be started. Using system speech.")
		return err
	}
	if err := controller.AfterStart(cmd); err != nil {
		controller.Force(cmd)
		_ = controller.Close()
		m.setUnavailable("failed", "Kokoro helper containment could not be established. Using system speech.")
		return err
	}

	done := make(chan struct{})
	m.mu.Lock()
	m.cmd = cmd
	m.stdin = stdin
	m.controller = controller
	m.done = done
	m.running = true
	m.handshakeReady = false
	m.modelLoaded = false
	m.available = false
	m.state = "initializing"
	m.message = "Kokoro helper is performing its initialization handshake."
	m.mu.Unlock()

	if err := m.writeProcessRecord(cmd.Process.Pid, instanceID, helperPath); err != nil && m.logf != nil {
		m.logf("system", "warning", "Could not record Kokoro helper ownership: %v", err)
	}

	go m.readLoop(stdout)
	go m.stderrLoop(stderr)
	go m.waitProcess(cmd, controller, done, instanceID)

	ctx, cancel := context.WithTimeout(context.Background(), kokoroHandshakeTimeout)
	defer cancel()
	response, err := m.request(ctx, kokoroipc.Message{Type: "initialize", Protocol: kokoroipc.ProtocolVersion, InstanceID: instanceID})
	if err != nil {
		m.setUnavailable("failed", "Kokoro helper initialization failed. Using system speech.")
		m.stopProcess(false)
		return fmt.Errorf("Kokoro initialization handshake: %w", err)
	}
	if response.Type != "ready" {
		err := fmt.Errorf("unexpected Kokoro handshake response %q", response.Type)
		m.setUnavailable("failed", "Kokoro helper returned an incompatible handshake. Using system speech.")
		m.stopProcess(false)
		return err
	}
	if response.Protocol != kokoroipc.ProtocolVersion {
		err := fmt.Errorf("Kokoro helper protocol %d does not match AgentGO protocol %d", response.Protocol, kokoroipc.ProtocolVersion)
		m.setUnavailable("incompatible", "Kokoro helper protocol is incompatible with this AgentGO version. Using system speech.")
		m.stopProcess(false)
		return err
	}
	voices := cleanKokoroVoiceList(response.Voices)
	if len(voices) == 0 {
		err := fmt.Errorf("Kokoro helper reported no voices")
		m.setUnavailable("failed", "Kokoro helper reported no available voices. Using system speech.")
		m.stopProcess(false)
		return err
	}
	synthesisCapable := kokoroCapabilitiesContain(response.Capabilities, "synthesize")

	m.mu.Lock()
	m.handshakeReady = true
	m.modelLoaded = response.ModelLoaded
	m.protocolVersion = response.Protocol
	m.helperVersion = strings.TrimSpace(response.HelperVersion)
	m.voices = voices
	m.everReady = true
	if response.ModelLoaded && synthesisCapable {
		m.available = true
		m.state = "ready"
		m.message = "Kokoro local neural speech is ready."
	} else {
		m.available = false
		m.state = "helper-ready"
		m.message = "Kokoro helper and voices are ready; local neural synthesis is not loaded yet. Using system speech."
	}
	m.mu.Unlock()

	if m.logf != nil {
		m.logf("system", "info", "Kokoro helper handshake ready with %d voice(s); model_loaded=%t", len(voices), response.ModelLoaded)
	}
	return nil
}

func (m *kokoroManager) readLoop(r io.Reader) {
	for {
		kind, payload, err := kokoroipc.ReadFrame(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && m.logf != nil {
				m.logf("system", "warning", "Kokoro IPC reader stopped: %v", err)
			}
			return
		}

		switch kind {
		case kokoroipc.FrameJSON:
			msg, decodeErr := kokoroipc.DecodeMessage(payload)
			if decodeErr != nil {
				if m.logf != nil {
					m.logf("system", "warning", "Kokoro IPC JSON decode failed: %v", decodeErr)
				}
				continue
			}
			ref := strings.TrimSpace(msg.Ref)
			if ref == "" {
				ref = strings.TrimSpace(msg.ID)
			}
			if ref == "" {
				continue
			}
			m.mu.Lock()
			stream := m.streams[ref]
			waiter := m.waiters[ref]
			m.mu.Unlock()
			if stream != nil {
				copyMsg := msg
				select {
				case stream.events <- kokoroStreamEvent{Message: &copyMsg}:
				case <-stream.done:
				}
				continue
			}
			if waiter != nil {
				select {
				case waiter <- msg:
				default:
				}
			}

		case kokoroipc.FrameAudio:
			audio, decodeErr := kokoroipc.DecodeAudioFrame(payload)
			if decodeErr != nil {
				if m.logf != nil {
					m.logf("system", "warning", "Kokoro IPC audio decode failed: %v", decodeErr)
				}
				continue
			}
			m.mu.Lock()
			stream := m.streams[strings.TrimSpace(audio.Ref)]
			m.mu.Unlock()
			if stream != nil {
				select {
				case stream.events <- kokoroStreamEvent{Audio: audio.Data}:
				case <-stream.done:
				}
			}
		}
	}
}

func (m *kokoroManager) stderrLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if m.logf != nil {
			m.logf("system", "info", "Kokoro helper: %s", line)
		} else {
			log.Printf("Kokoro helper: %s", line)
		}
	}
}

func (m *kokoroManager) request(ctx context.Context, message kokoroipc.Message) (kokoroipc.Message, error) {
	id := strings.TrimSpace(message.ID)
	if id == "" {
		id = fmt.Sprintf("kokoro-%d", atomic.AddUint64(&m.seq, 1))
		message.ID = id
	}
	if message.Protocol == 0 {
		message.Protocol = kokoroipc.ProtocolVersion
	}
	waiter := make(chan kokoroipc.Message, 1)

	m.mu.Lock()
	if !m.running || m.stdin == nil {
		m.mu.Unlock()
		return kokoroipc.Message{}, fmt.Errorf("Kokoro helper is not running")
	}
	m.waiters[id] = waiter
	stdin := m.stdin
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.waiters, id)
		m.mu.Unlock()
	}()

	m.writeMu.Lock()
	err := kokoroipc.WriteMessage(stdin, message)
	m.writeMu.Unlock()
	if err != nil {
		return kokoroipc.Message{}, fmt.Errorf("write Kokoro IPC request: %w", err)
	}

	select {
	case <-ctx.Done():
		return kokoroipc.Message{}, ctx.Err()
	case response := <-waiter:
		if response.Type == "error" {
			message := strings.TrimSpace(response.Error)
			if message == "" {
				message = strings.TrimSpace(response.Message)
			}
			if message == "" {
				message = "Kokoro helper error"
			}
			return response, errors.New(message)
		}
		return response, nil
	}
}

func (m *kokoroManager) sendMessage(message kokoroipc.Message) error {
	if message.Protocol == 0 {
		message.Protocol = kokoroipc.ProtocolVersion
	}
	m.mu.Lock()
	if !m.running || m.stdin == nil {
		m.mu.Unlock()
		return fmt.Errorf("Kokoro helper is not running")
	}
	stdin := m.stdin
	m.mu.Unlock()

	m.writeMu.Lock()
	err := kokoroipc.WriteMessage(stdin, message)
	m.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("write Kokoro IPC request: %w", err)
	}
	return nil
}

func (m *kokoroManager) cancelSynthesis(ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	id := fmt.Sprintf("kokoro-cancel-%d", atomic.AddUint64(&m.seq, 1))
	_ = m.sendMessage(kokoroipc.Message{
		Type:     "cancel",
		ID:       id,
		Ref:      ref,
		Protocol: kokoroipc.ProtocolVersion,
	})
}

func (m *kokoroManager) Synthesize(ctx context.Context, text, voice string, speed float32, emit func(kokoroStreamEvent) error) error {
	if m == nil {
		return fmt.Errorf("Kokoro manager is unavailable")
	}
	text = strings.TrimSpace(text)
	voice = strings.TrimSpace(voice)
	if text == "" {
		return fmt.Errorf("Kokoro synthesis text is empty")
	}
	if voice == "" {
		return fmt.Errorf("Kokoro voice is empty")
	}
	if emit == nil {
		return fmt.Errorf("Kokoro stream callback is unavailable")
	}

	id := fmt.Sprintf("kokoro-synth-%d", atomic.AddUint64(&m.seq, 1))
	stream := &kokoroStream{events: make(chan kokoroStreamEvent, 32), done: make(chan struct{})}
	m.mu.Lock()
	if !m.running || !m.available || m.stdin == nil {
		m.mu.Unlock()
		return fmt.Errorf("Kokoro local neural speech is not available")
	}
	voiceOK := false
	for _, availableVoice := range m.voices {
		if availableVoice == voice {
			voiceOK = true
			break
		}
	}
	if !voiceOK {
		m.mu.Unlock()
		return fmt.Errorf("Kokoro voice %q is not available", voice)
	}
	m.streams[id] = stream
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.streams[id] == stream {
			delete(m.streams, id)
		}
		m.mu.Unlock()
		close(stream.done)
	}()

	request := kokoroipc.Message{
		Type:     "synthesize",
		ID:       id,
		Protocol: kokoroipc.ProtocolVersion,
		Text:     text,
		Voice:    voice,
		Speed:    speed,
	}
	if err := m.sendMessage(request); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			m.cancelSynthesis(id)
			return ctx.Err()
		case event := <-stream.events:
			if err := emit(event); err != nil {
				m.cancelSynthesis(id)
				return err
			}
			if event.Message != nil {
				switch strings.ToLower(strings.TrimSpace(event.Message.Type)) {
				case "error":
					message := strings.TrimSpace(event.Message.Error)
					if message == "" {
						message = strings.TrimSpace(event.Message.Message)
					}
					if message == "" {
						message = "Kokoro synthesis failed"
					}
					return errors.New(message)
				case "cancelled":
					return context.Canceled
				case "complete":
					return nil
				}
			}
		}
	}
}

func (m *kokoroManager) waitProcess(cmd *exec.Cmd, controller *agenticProcessController, done chan struct{}, instanceID string) {
	err := cmd.Wait()
	_ = controller.Close()

	m.mu.Lock()
	if m.cmd != cmd {
		m.mu.Unlock()
		close(done)
		return
	}
	wasIntentional := m.intentionalStop || m.closed
	shouldRestart := !wasIntentional && m.everReady && !m.restartAttempted
	if shouldRestart {
		m.restartAttempted = true
	}
	m.cmd = nil
	m.stdin = nil
	m.controller = nil
	m.running = false
	m.available = false
	m.handshakeReady = false
	m.modelLoaded = false
	m.voices = nil
	if wasIntentional {
		m.state = "stopped"
		m.message = "Kokoro helper is stopped."
	} else {
		m.state = "failed"
		m.message = "Kokoro helper exited unexpectedly. Using system speech."
		if err != nil && m.logf != nil {
			m.logf("system", "warning", "Kokoro helper exited unexpectedly: %v", err)
		}
	}
	waiters := m.waiters
	streams := m.streams
	m.waiters = map[string]chan kokoroipc.Message{}
	m.streams = map[string]*kokoroStream{}
	m.mu.Unlock()

	m.removeProcessRecordIfOwned(instanceID)
	close(done)
	for _, waiter := range waiters {
		select {
		case waiter <- kokoroipc.Message{Type: "error", Error: "Kokoro helper exited"}:
		default:
		}
	}
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		msg := kokoroipc.Message{Type: "error", Error: "Kokoro helper exited"}
		select {
		case stream.events <- kokoroStreamEvent{Message: &msg}:
		case <-stream.done:
		default:
		}
	}

	if shouldRestart {
		if m.logf != nil {
			m.logf("system", "warning", "Kokoro helper exited unexpectedly; attempting one automatic restart")
		}
		time.Sleep(kokoroRestartDelay)
		if restartErr := m.Start(); restartErr != nil && m.logf != nil {
			m.logf("system", "warning", "Kokoro automatic restart failed: %v", restartErr)
		}
	}
}

func (m *kokoroManager) stopProcess(intentional bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if intentional {
		m.intentionalStop = true
	}
	cmd := m.cmd
	controller := m.controller
	done := m.done
	running := m.running
	m.mu.Unlock()
	if !running || cmd == nil || cmd.Process == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), kokoroShutdownTimeout/2)
	_, _ = m.request(ctx, kokoroipc.Message{Type: "shutdown", Protocol: kokoroipc.ProtocolVersion})
	cancel()

	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(kokoroShutdownTimeout):
		}
	}
	if controller != nil {
		controller.Force(cmd)
	} else {
		_ = cmd.Process.Kill()
	}
}

func (m *kokoroManager) Stop() {
	m.stopProcess(true)
}

func (m *kokoroManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.intentionalStop = true
	m.mu.Unlock()
	m.stopProcess(true)
}

func (m *kokoroManager) writeProcessRecord(pid int, instanceID, helperPath string) error {
	statePath, _ := m.processRecordPaths()
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	ownerPath, _ := os.Executable()
	if abs, err := filepath.Abs(ownerPath); err == nil {
		ownerPath = abs
	}
	record := kokoroProcessRecord{
		PID:        pid,
		InstanceID: instanceID,
		HelperPath: helperPath,
		OwnerPID:   os.Getpid(),
		OwnerPath:  ownerPath,
		StartedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath)
}

func (m *kokoroManager) removeProcessRecordIfOwned(instanceID string) {
	statePath, heartbeatPath := m.processRecordPaths()
	data, err := os.ReadFile(statePath)
	if err == nil {
		var record kokoroProcessRecord
		if json.Unmarshal(data, &record) == nil && record.InstanceID == instanceID {
			_ = os.Remove(statePath)
		}
	}
	data, err = os.ReadFile(heartbeatPath)
	if err == nil {
		var record kokoroProcessRecord
		if json.Unmarshal(data, &record) == nil && record.InstanceID == instanceID {
			_ = os.Remove(heartbeatPath)
		}
	}
}

func (m *kokoroManager) cleanupStaleHelper(expectedHelperPath string) error {
	statePath, heartbeatPath := m.processRecordPaths()
	stateData, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state kokoroProcessRecord
	if err := json.Unmarshal(stateData, &state); err != nil || state.PID <= 0 || strings.TrimSpace(state.InstanceID) == "" {
		_ = os.Remove(statePath)
		return nil
	}

	heartbeatData, heartbeatErr := os.ReadFile(heartbeatPath)
	var heartbeat kokoroProcessRecord
	heartbeatFresh := false
	if heartbeatErr == nil && json.Unmarshal(heartbeatData, &heartbeat) == nil && heartbeat.PID == state.PID && heartbeat.InstanceID == state.InstanceID {
		if updated, parseErr := time.Parse(time.RFC3339Nano, heartbeat.UpdatedAt); parseErr == nil {
			age := time.Since(updated)
			heartbeatFresh = age >= 0 && age <= kokoroHeartbeatFreshLimit
		}
	}

	runningPath, runningErr := kokoroProcessExecutablePath(state.PID)
	if runningErr != nil {
		// The recorded helper no longer exists, so its ownership files are stale.
		_ = os.Remove(statePath)
		_ = os.Remove(heartbeatPath)
		return nil
	}
	if !heartbeatFresh {
		// A PID may have been reused. Never terminate it without a live instance
		// heartbeat. Also refuse to start a second model while a process at that PID
		// still exists; this is safer than briefly double-loading Kokoro in RAM.
		return fmt.Errorf("recorded Kokoro helper PID %d is still running but its instance heartbeat is not fresh", state.PID)
	}
	if !sameKokoroExecutablePath(runningPath, expectedHelperPath) || !sameKokoroExecutablePath(state.HelperPath, expectedHelperPath) {
		return fmt.Errorf("recorded Kokoro PID %d did not match the expected helper executable; it was not terminated", state.PID)
	}

	if kokoroOwnerStillAlive(state) {
		return fmt.Errorf("Kokoro helper PID %d is owned by live AgentGO PID %d; it was left running", state.PID, state.OwnerPID)
	}

	process, err := os.FindProcess(state.PID)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("terminate stale Kokoro helper PID %d: %w", state.PID, err)
	}
	if err := waitForKokoroProcessExit(state.PID, kokoroStaleExitTimeout); err != nil {
		return err
	}
	_ = os.Remove(statePath)
	_ = os.Remove(heartbeatPath)
	if m.logf != nil {
		m.logf("system", "info", "Removed stale AgentGO-owned Kokoro helper PID %d and confirmed it exited before reload", state.PID)
	}
	return nil
}

func kokoroOwnerStillAlive(state kokoroProcessRecord) bool {
	if state.OwnerPID <= 0 || strings.TrimSpace(state.OwnerPath) == "" {
		// r445 and earlier records do not include owner identity. The live helper
		// heartbeat plus executable verification remains sufficient to identify the
		// helper itself; treat the missing owner as a legacy/orphan record.
		return false
	}
	runningOwnerPath, err := kokoroProcessExecutablePath(state.OwnerPID)
	if err != nil {
		return false
	}
	return sameKokoroExecutablePath(runningOwnerPath, state.OwnerPath)
}

func waitForKokoroProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := kokoroProcessExecutablePath(pid); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Kokoro helper PID %d did not exit within %s; refusing to load another model", pid, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func sameKokoroExecutablePath(a, b string) bool {
	cleanA, errA := filepath.Abs(strings.TrimSpace(a))
	cleanB, errB := filepath.Abs(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	cleanA = filepath.Clean(cleanA)
	cleanB = filepath.Clean(cleanB)
	if kokoroPathComparisonCaseInsensitive() {
		return strings.EqualFold(cleanA, cleanB)
	}
	return cleanA == cleanB
}
