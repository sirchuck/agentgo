package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"agentgo/internal/kokoroipc"
)

const (
	ttsEngineAuto     = "auto"
	ttsEngineLegacy   = "legacy"
	defaultTTSSpeed   = 1.0
	minimumTTSSpeed   = 0.75
	maximumTTSSpeed   = 1.50
	maximumTTSVoiceID = 256
)

type ttsConfigResponse struct {
	OK             bool              `json:"ok"`
	TTSEnabled     bool              `json:"tts_enabled"`
	TTSVoice       string            `json:"tts_voice"`
	TTSLegacyVoice string            `json:"tts_legacy_voice"`
	TTSSpeed       float64           `json:"tts_speed"`
	TTSHighlight   bool              `json:"tts_highlight"`
	TTSEngine      string            `json:"tts_engine"`
	Kokoro         kokoroAssetStatus `json:"kokoro"`
}

type ttsConfigRequest struct {
	TTSEnabled     *bool    `json:"tts_enabled"`
	TTSVoice       *string  `json:"tts_voice"`
	TTSLegacyVoice *string  `json:"tts_legacy_voice"`
	TTSSpeed       *float64 `json:"tts_speed"`
	TTSHighlight   *bool    `json:"tts_highlight"`
	TTSEngine      *string  `json:"tts_engine"`
}

func boolPointer(value bool) *bool {
	v := value
	return &v
}

func stringPointer(value string) *string {
	v := value
	return &v
}

func ttsEnabled(cfg AppConfig) bool {
	return cfg.TTSEnabled == nil || *cfg.TTSEnabled
}

func ttsHighlightEnabled(cfg AppConfig) bool {
	return cfg.TTSHighlight == nil || *cfg.TTSHighlight
}

func ttsLegacyVoice(cfg AppConfig) string {
	if cfg.TTSLegacyVoice != nil {
		return strings.TrimSpace(*cfg.TTSLegacyVoice)
	}
	// Migration path for r433 and older: tts_voice held the browser/system voice.
	return strings.TrimSpace(cfg.TTSVoice)
}

func normalizeTTSEngine(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ttsEngineLegacy:
		return ttsEngineLegacy
	default:
		return ttsEngineAuto
	}
}

func validTTSEngine(value string) bool {
	clean := strings.ToLower(strings.TrimSpace(value))
	return clean == ttsEngineAuto || clean == ttsEngineLegacy
}

func normalizeTTSSpeed(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return defaultTTSSpeed
	}
	if value < minimumTTSSpeed {
		return minimumTTSSpeed
	}
	if value > maximumTTSSpeed {
		return maximumTTSSpeed
	}
	return value
}

func (a *App) kokoroRoot() string {
	root := strings.TrimSpace(a.appRoot)
	if root == "" {
		return "."
	}
	return root
}

func (a *App) currentKokoroStatus() kokoroAssetStatus {
	status := inspectKokoroAssets(a.kokoroRoot())
	if a == nil || a.kokoro == nil {
		return status
	}
	runtimeStatus := a.kokoro.Status()
	if !runtimeStatus.Started {
		return status
	}
	status.HelperRunning = runtimeStatus.Running
	status.HandshakeReady = runtimeStatus.HandshakeReady
	status.ModelLoaded = runtimeStatus.ModelLoaded
	status.ProtocolVersion = runtimeStatus.ProtocolVersion
	status.HelperVersion = runtimeStatus.HelperVersion
	status.Available = runtimeStatus.Available
	if runtimeStatus.HandshakeReady {
		status.Voices = cleanKokoroVoiceList(runtimeStatus.Voices)
		status.G2PProvider = strings.TrimSpace(runtimeStatus.G2PProvider)
		if status.G2PProvider == "" {
			status.G2PProvider = "standard"
		}
		status.G2PAvailable = runtimeStatus.G2PAvailable
		if strings.TrimSpace(runtimeStatus.G2PMessage) != "" {
			status.G2PMessage = runtimeStatus.G2PMessage
		}
		if runtimeStatus.G2PAvailable {
			status.G2PState = "ready"
		} else if status.G2PState == "installed" {
			status.G2PState = "fallback"
		}
	}
	if runtimeStatus.HelperPath != "" {
		status.HelperPath = filepath.ToSlash(runtimeStatus.HelperPath)
	}
	switch runtimeStatus.State {
	case "missing":
		// Keep the more specific filesystem message from inspectKokoroAssets.
	case "":
	default:
		status.State = runtimeStatus.State
		if strings.TrimSpace(runtimeStatus.Message) != "" {
			status.Message = runtimeStatus.Message
		}
	}
	return status
}

func (a *App) kokoroWanted() bool {
	if a == nil {
		return false
	}
	// A complete local Kokoro installation is an explicit user choice. Load it
	// at AgentGO startup even when Speech Enabled is currently off or a browser
	// voice is selected, then keep the helper/model resident until AgentGO exits.
	status := inspectKokoroAssets(a.kokoroRoot())
	return status.ModelPresent && status.VoicesPresent && status.HelperPresent
}

func (a *App) syncKokoroLifecycle() {
	if a == nil || a.kokoro == nil {
		return
	}
	status := a.kokoro.Status()
	// Once started, Kokoro stays resident for the lifetime of AgentGO. Speech
	// settings control whether it is used, not whether the model remains loaded.
	if status.Running || (status.Started && status.State != "stopped") {
		return
	}
	if !a.kokoroWanted() {
		return
	}
	a.logf("system", "info", "Kokoro background warm-up started; AgentGO remains available while the model loads")
	go func() {
		if err := a.kokoro.Start(); err != nil {
			a.logf("system", "info", "Kokoro local TTS unavailable; legacy speech remains active: %v", err)
			return
		}
		a.reconcileKokoroVoice(a.kokoro.Status().Voices)
		a.logf("system", "info", "Kokoro background warm-up ready")
	}()
}

func (a *App) reconcileKokoroVoice(voices []string) {
	voices = cleanKokoroVoiceList(voices)
	if a == nil || len(voices) == 0 {
		return
	}
	a.mu.Lock()
	current := strings.TrimSpace(a.cfg.TTSVoice)
	for _, voice := range voices {
		if current == voice {
			a.mu.Unlock()
			return
		}
	}
	previous := a.cfg.TTSVoice
	a.cfg.TTSVoice = voices[0]
	cfg := a.cfg
	a.mu.Unlock()

	applyConfigDefaults(&cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		data = append(data, '\n')
		err = atomicWriteFile(a.configPath, data, 0o644)
	}
	if err != nil {
		a.mu.Lock()
		if a.cfg.TTSVoice == voices[0] {
			a.cfg.TTSVoice = previous
		}
		a.mu.Unlock()
		a.logf("system", "warning", "Could not persist Kokoro voice fallback: %v", err)
		return
	}
	a.logf("system", "info", "Saved Kokoro voice was unavailable; selected %s", voices[0])
}

func (a *App) currentTTSConfigResponse() ttsConfigResponse {
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	return ttsConfigResponse{
		OK:             true,
		TTSEnabled:     ttsEnabled(cfg),
		TTSVoice:       strings.TrimSpace(cfg.TTSVoice),
		TTSLegacyVoice: ttsLegacyVoice(cfg),
		TTSSpeed:       normalizeTTSSpeed(cfg.TTSSpeed),
		TTSHighlight:   ttsHighlightEnabled(cfg),
		TTSEngine:      normalizeTTSEngine(cfg.TTSEngine),
		Kokoro:         a.currentKokoroStatus(),
	}
}

func validateTTSVoiceValue(value string) (string, bool) {
	clean := strings.TrimSpace(value)
	return clean, len(clean) <= maximumTTSVoiceID
}

func (a *App) handleTTSConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.currentTTSConfigResponse())
	case http.MethodPost:
		var req ttsConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()

		if req.TTSEnabled != nil {
			cfg.TTSEnabled = boolPointer(*req.TTSEnabled)
		}
		if req.TTSVoice != nil {
			voice, ok := validateTTSVoiceValue(*req.TTSVoice)
			if !ok {
				http.Error(w, "tts_voice is too long", http.StatusBadRequest)
				return
			}
			cfg.TTSVoice = voice
		}
		if req.TTSLegacyVoice != nil {
			voice, ok := validateTTSVoiceValue(*req.TTSLegacyVoice)
			if !ok {
				http.Error(w, "tts_legacy_voice is too long", http.StatusBadRequest)
				return
			}
			cfg.TTSLegacyVoice = stringPointer(voice)
		}
		if req.TTSSpeed != nil {
			if math.IsNaN(*req.TTSSpeed) || math.IsInf(*req.TTSSpeed, 0) || *req.TTSSpeed < minimumTTSSpeed || *req.TTSSpeed > maximumTTSSpeed {
				http.Error(w, "tts_speed must be between 0.75 and 1.50", http.StatusBadRequest)
				return
			}
			cfg.TTSSpeed = *req.TTSSpeed
		}
		if req.TTSHighlight != nil {
			cfg.TTSHighlight = boolPointer(*req.TTSHighlight)
		}
		if req.TTSEngine != nil {
			if !validTTSEngine(*req.TTSEngine) {
				http.Error(w, "tts_engine must be auto or legacy", http.StatusBadRequest)
				return
			}
			cfg.TTSEngine = normalizeTTSEngine(*req.TTSEngine)
		}
		applyConfigDefaults(&cfg)
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data = append(data, '\n')
		if err := atomicWriteFile(a.configPath, data, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.mu.Lock()
		a.cfg.TTSEnabled = cfg.TTSEnabled
		a.cfg.TTSVoice = cfg.TTSVoice
		a.cfg.TTSLegacyVoice = cfg.TTSLegacyVoice
		a.cfg.TTSSpeed = cfg.TTSSpeed
		a.cfg.TTSHighlight = cfg.TTSHighlight
		a.cfg.TTSEngine = cfg.TTSEngine
		a.mu.Unlock()
		a.syncKokoroLifecycle()
		writeJSON(w, http.StatusOK, a.currentTTSConfigResponse())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTTSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.currentTTSConfigResponse())
}

type kokoroSynthesisRequest struct {
	Text  string  `json:"text"`
	Voice string  `json:"voice,omitempty"`
	Speed float64 `json:"speed,omitempty"`
}

func (a *App) handleKokoroSynthesis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a == nil || a.kokoro == nil {
		http.Error(w, "Kokoro local speech is unavailable", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var req kokoroSynthesisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid Kokoro synthesis request", http.StatusBadRequest)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		http.Error(w, "Kokoro synthesis text is empty", http.StatusBadRequest)
		return
	}

	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	if !ttsEnabled(cfg) || normalizeTTSEngine(cfg.TTSEngine) != ttsEngineAuto {
		http.Error(w, "Kokoro local speech is not selected", http.StatusServiceUnavailable)
		return
	}
	status := a.kokoro.Status()
	if !status.Available || !status.Running || !status.ModelLoaded {
		http.Error(w, "Kokoro local speech is not ready", http.StatusServiceUnavailable)
		return
	}

	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = strings.TrimSpace(cfg.TTSVoice)
	}
	if voice == "" && len(status.Voices) > 0 {
		voice = status.Voices[0]
	}
	voiceOK := false
	for _, availableVoice := range status.Voices {
		if strings.TrimSpace(availableVoice) == voice {
			voiceOK = true
			break
		}
	}
	if !voiceOK {
		http.Error(w, "selected Kokoro voice is unavailable", http.StatusBadRequest)
		return
	}

	speed := req.Speed
	if speed == 0 {
		speed = normalizeTTSSpeed(cfg.TTSSpeed)
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < minimumTTSSpeed || speed > maximumTTSSpeed {
		http.Error(w, "tts speed must be between 0.75 and 1.50", http.StatusBadRequest)
		return
	}

	phoneticsContent, phoneticsErr := a.readPhonetics()
	if phoneticsErr != nil {
		a.logf("system", "warning", "Could not read custom phonetics; speaking source text unchanged: %v", phoneticsErr)
	}
	phoneticRules, _ := parsePhoneticRules(phoneticsContent)
	phoneticSpeech := applyPhoneticRules(req.Text, phoneticRules)
	if strings.TrimSpace(phoneticSpeech.Text) == "" {
		phoneticSpeech = applyPhoneticRules(req.Text, nil)
	}

	w.Header().Set("Content-Type", "application/vnd.agentgo.kokoro-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)
	wroteFrame := false
	terminalErrorFrame := false
	writeEvent := func(event kokoroStreamEvent) error {
		var err error
		if event.Message != nil {
			message := *event.Message
			if len(event.Message.Timings) > 0 {
				message.Timings = append([]kokoroipc.TimingSpan(nil), event.Message.Timings...)
			}
			phoneticSpeech.remapMessage(&message)
			if strings.EqualFold(strings.TrimSpace(message.Type), "error") {
				terminalErrorFrame = true
			}
			payload, encodeErr := kokoroipc.EncodeMessage(message)
			if encodeErr != nil {
				return encodeErr
			}
			err = kokoroipc.WriteFrame(w, kokoroipc.FrameJSON, payload)
		} else if len(event.Audio) > 0 {
			err = kokoroipc.WriteFrame(w, kokoroipc.FrameAudio, event.Audio)
		}
		if err != nil {
			return err
		}
		wroteFrame = true
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	err := a.kokoro.Synthesize(r.Context(), phoneticSpeech.Text, voice, float32(speed), writeEvent)
	if err == nil || errorsIsContextCancellation(err) {
		return
	}
	if !wroteFrame {
		http.Error(w, "Kokoro synthesis failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if terminalErrorFrame {
		return
	}
	// The HTTP status is already committed once streaming begins. Send a final
	// protocol error frame so the browser can stop this playback cleanly.
	_ = writeEvent(kokoroStreamEvent{Message: &kokoroipc.Message{Type: "error", Error: err.Error(), Protocol: kokoroipc.ProtocolVersion}})
}

func errorsIsContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
