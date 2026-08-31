package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	ort "github.com/benedoc-inc/onnxer/onnxruntime"
)

const kokoroONNXRuntimeAPIVersion uint32 = 23

type nativeKokoroEngine struct {
	mu                 sync.Mutex
	assetRoot          string
	modelPath          string
	runtimePath        string
	runtimeVer         string
	ortRuntime         *ort.Runtime
	ortEnv             *ort.Env
	session            *ort.Session
	outputNames        []string
	waveformOutputName string
	durationOutputName string
	voiceCache         map[string][]float32
	closed             bool
}

func newKokoroEngine(assetRoot string) (kokoroEngine, error) {
	assetRoot, err := filepath.Abs(strings.TrimSpace(assetRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve Kokoro asset directory: %w", err)
	}
	modelPath := filepath.Join(assetRoot, "model.onnx")
	if info, statErr := os.Stat(modelPath); statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		if statErr == nil {
			statErr = fmt.Errorf("model.onnx is empty or not a regular file")
		}
		return nil, fmt.Errorf("Kokoro model unavailable: %w", statErr)
	}
	runtimePath, err := findONNXRuntimeLibrary(assetRoot)
	if err != nil {
		return nil, err
	}

	ortRuntime, err := ort.NewRuntime(runtimePath, kokoroONNXRuntimeAPIVersion)
	if err != nil {
		return nil, fmt.Errorf("load ONNX Runtime from %s: %w", runtimePath, err)
	}
	cleanupRuntime := true
	defer func() {
		if cleanupRuntime {
			_ = ortRuntime.Close()
		}
	}()

	ortEnv, err := ortRuntime.NewEnv("agentgo-kokoro", ort.LoggingLevelWarning)
	if err != nil {
		return nil, fmt.Errorf("initialize ONNX Runtime environment: %w", err)
	}
	cleanupEnv := true
	defer func() {
		if cleanupEnv {
			ortEnv.Close()
		}
	}()
	_ = ortEnv.DisableTelemetry()

	session, err := ortRuntime.NewSession(ortEnv, modelPath, nil)
	if err != nil {
		return nil, fmt.Errorf("load Kokoro ONNX model: %w", err)
	}
	cleanupSession := true
	defer func() {
		if cleanupSession {
			session.Close()
		}
	}()

	if err := validateKokoroInputs(session.InputNames()); err != nil {
		return nil, err
	}
	outputNames, waveformOutputName, durationOutputName := discoverKokoroModelOutputs(session.OutputNames())
	if len(outputNames) == 0 || waveformOutputName == "" {
		return nil, fmt.Errorf("Kokoro ONNX model exposes no usable waveform output")
	}

	cleanupSession = false
	cleanupEnv = false
	cleanupRuntime = false
	return &nativeKokoroEngine{
		assetRoot:          assetRoot,
		modelPath:          modelPath,
		runtimePath:        runtimePath,
		runtimeVer:         strings.TrimSpace(ortRuntime.GetVersionString()),
		ortRuntime:         ortRuntime,
		ortEnv:             ortEnv,
		session:            session,
		outputNames:        outputNames,
		waveformOutputName: waveformOutputName,
		durationOutputName: durationOutputName,
		voiceCache:         map[string][]float32{},
	}, nil
}

func validateKokoroInputs(names []string) error {
	have := make(map[string]bool, len(names))
	for _, name := range names {
		have[strings.TrimSpace(name)] = true
	}
	var missing []string
	for _, required := range []string{"input_ids", "style", "speed"} {
		if !have[required] {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Kokoro ONNX model is incompatible; missing input(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func (e *nativeKokoroEngine) RuntimeVersion() string {
	if e == nil {
		return ""
	}
	return e.runtimeVer
}

func (e *nativeKokoroEngine) TimingSupported() bool {
	return e != nil && strings.TrimSpace(e.durationOutputName) != ""
}

func (e *nativeKokoroEngine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.session != nil {
		e.session.Close()
		e.session = nil
	}
	if e.ortEnv != nil {
		e.ortEnv.Close()
		e.ortEnv = nil
	}
	var runtimeErr error
	if e.ortRuntime != nil {
		runtimeErr = e.ortRuntime.Close()
		e.ortRuntime = nil
	}
	return runtimeErr
}

func (e *nativeKokoroEngine) Synthesize(ctx context.Context, text, voice string, speed float32, emit func(kokoroSynthesisChunk) error) error {
	if err := validateSynthesisRequest(text, voice); err != nil {
		return err
	}
	if emit == nil {
		return fmt.Errorf("Kokoro synthesis output callback is unavailable")
	}
	speed = clampKokoroSpeed(speed)
	voiceID, err := cleanVoiceID(voice)
	if err != nil {
		return err
	}

	chunks := splitNaturalSourceText(text)
	if len(chunks) == 0 {
		return fmt.Errorf("speech text produced no synthesis chunks")
	}
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.synthesizeSourceChunk(ctx, chunk, voiceID, speed, emit); err != nil {
			return err
		}
	}
	return nil
}

func (e *nativeKokoroEngine) synthesizeSourceChunk(ctx context.Context, chunk sourceChunk, voiceID string, speed float32, emit func(kokoroSynthesisChunk) error) error {
	_, tokens, groups, err := phonemizeEnglishWithSource(chunk.Text)
	if err != nil {
		return fmt.Errorf("phonemize %q: %w", truncateForError(chunk.Text), err)
	}
	if len(tokens) > kokoroMaxModelTokens {
		left, right, ok := splitOversizedSourceChunk(chunk)
		if !ok {
			return fmt.Errorf("Kokoro text chunk produced %d tokens; maximum is %d", len(tokens), kokoroMaxModelTokens)
		}
		if err := e.synthesizeSourceChunk(ctx, left, voiceID, speed, emit); err != nil {
			return err
		}
		return e.synthesizeSourceChunk(ctx, right, voiceID, speed, emit)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	samples, durations, err := e.runTokens(ctx, tokens, voiceID, speed)
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return fmt.Errorf("Kokoro returned empty audio")
	}
	timings := buildKokoroWordTimings(chunk, groups, tokens, durations, len(samples))
	return emit(kokoroSynthesisChunk{Source: chunk, PCM: samples, Timings: timings})
}

func (e *nativeKokoroEngine) runTokens(ctx context.Context, tokens []int64, voiceID string, speed float32) ([]float32, []float64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil || e.ortRuntime == nil {
		return nil, nil, fmt.Errorf("Kokoro engine is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	voiceData, err := e.loadVoiceLocked(voiceID)
	if err != nil {
		return nil, nil, err
	}
	const styleWidth = 256
	rows := len(voiceData) / styleWidth
	if rows <= len(tokens) {
		return nil, nil, fmt.Errorf("voice %s has %d style rows but synthesis needs row %d", voiceID, rows, len(tokens))
	}
	style := append([]float32(nil), voiceData[len(tokens)*styleWidth:(len(tokens)+1)*styleWidth]...)
	ids := make([]int64, len(tokens)+2)
	copy(ids[1:], tokens)

	runtimeHandle := e.ortRuntime
	idsTensor, err := ort.NewTensorValue(runtimeHandle, ids, []int64{1, int64(len(ids))})
	if err != nil {
		return nil, nil, fmt.Errorf("create Kokoro input_ids tensor: %w", err)
	}
	defer idsTensor.Close()
	styleTensor, err := ort.NewTensorValue(runtimeHandle, style, []int64{1, styleWidth})
	if err != nil {
		return nil, nil, fmt.Errorf("create Kokoro style tensor: %w", err)
	}
	defer styleTensor.Close()
	speedTensor, err := ort.NewTensorValue(runtimeHandle, []float32{speed}, []int64{1})
	if err != nil {
		return nil, nil, fmt.Errorf("create Kokoro speed tensor: %w", err)
	}
	defer speedTensor.Close()

	outputs, err := e.session.Run(ctx, map[string]*ort.Value{
		"input_ids": idsTensor,
		"style":     styleTensor,
		"speed":     speedTensor,
	}, ort.WithOutputNames(e.outputNames...))
	if err != nil {
		return nil, nil, fmt.Errorf("run Kokoro model: %w", err)
	}
	defer closeKokoroOutputs(outputs)

	waveform := outputs[e.waveformOutputName]
	if waveform == nil {
		return nil, nil, fmt.Errorf("Kokoro model returned no waveform tensor %q", e.waveformOutputName)
	}
	samples, _, err := ort.GetTensorData[float32](waveform)
	if err != nil {
		return nil, nil, fmt.Errorf("read Kokoro waveform output: %w", err)
	}
	var durations []float64
	if e.durationOutputName != "" {
		if durationValue := outputs[e.durationOutputName]; durationValue != nil {
			durations = kokoroDurationValues(durationValue)
		}
	}
	return samples, durations, nil
}

func closeKokoroOutputs(outputs map[string]*ort.Value) {
	for _, value := range outputs {
		if value != nil {
			value.Close()
		}
	}
}

func discoverKokoroModelOutputs(outputs []string) ([]string, string, string) {
	waveformName := ""
	durationName := ""
	cleaned := make([]string, 0, len(outputs))
	for _, raw := range outputs {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		cleaned = append(cleaned, name)
		lower := strings.ToLower(name)
		if waveformName == "" && (lower == "waveform" || strings.Contains(lower, "wave")) {
			waveformName = name
		}
		if durationName == "" && (lower == "pred_dur" || strings.Contains(lower, "duration") || strings.Contains(lower, "pred_dur") || strings.HasSuffix(lower, "_dur")) {
			durationName = name
		}
	}
	if waveformName == "" && len(cleaned) > 0 {
		waveformName = cleaned[0]
	}
	if durationName == "" && len(cleaned) == 2 {
		for _, name := range cleaned {
			if name != waveformName {
				durationName = name
				break
			}
		}
	}
	requested := []string{}
	if waveformName != "" {
		requested = append(requested, waveformName)
	}
	if durationName != "" && durationName != waveformName {
		requested = append(requested, durationName)
	} else {
		durationName = ""
	}
	return requested, waveformName, durationName
}

func kokoroDurationValues(value *ort.Value) []float64 {
	if value == nil {
		return nil
	}
	elementType, err := value.GetTensorElementType()
	if err != nil {
		return nil
	}
	switch elementType {
	case ort.ONNXTensorElementDataTypeFloat:
		data, _, err := ort.GetTensorData[float32](value)
		if err != nil {
			return nil
		}
		out := make([]float64, len(data))
		for i, item := range data {
			out[i] = float64(item)
		}
		return out
	case ort.ONNXTensorElementDataTypeDouble:
		data, _, err := ort.GetTensorData[float64](value)
		if err != nil {
			return nil
		}
		return data
	case ort.ONNXTensorElementDataTypeInt64:
		data, _, err := ort.GetTensorData[int64](value)
		if err != nil {
			return nil
		}
		out := make([]float64, len(data))
		for i, item := range data {
			out[i] = float64(item)
		}
		return out
	case ort.ONNXTensorElementDataTypeInt32:
		data, _, err := ort.GetTensorData[int32](value)
		if err != nil {
			return nil
		}
		out := make([]float64, len(data))
		for i, item := range data {
			out[i] = float64(item)
		}
		return out
	default:
		return nil
	}
}

func (e *nativeKokoroEngine) loadVoiceLocked(voiceID string) ([]float32, error) {
	if data := e.voiceCache[voiceID]; len(data) > 0 {
		return data, nil
	}
	path := filepath.Join(e.assetRoot, "voices", voiceID+".bin")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load Kokoro voice %s: %w", voiceID, err)
	}
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil, fmt.Errorf("Kokoro voice %s has invalid binary length %d", voiceID, len(raw))
	}
	values := make([]float32, len(raw)/4)
	for i := range values {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	if len(values)%256 != 0 {
		return nil, fmt.Errorf("Kokoro voice %s does not contain 256-value style rows", voiceID)
	}
	e.voiceCache[voiceID] = values
	return values, nil
}

func cleanVoiceID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value != filepath.Base(value) || strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("invalid Kokoro voice id")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("invalid Kokoro voice id %q", value)
	}
	return value, nil
}

func findONNXRuntimeLibrary(assetRoot string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("AGENTGO_ONNXRUNTIME_PATH")); override != "" {
		if info, err := os.Stat(override); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return override, nil
		}
		return "", fmt.Errorf("AGENTGO_ONNXRUNTIME_PATH does not point to a usable ONNX Runtime library")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	runtimeRoot := filepath.Join(assetRoot, "runtime")
	var names []string
	switch runtime.GOOS {
	case "windows":
		names = []string{"onnxruntime.dll"}
	case "darwin":
		names = []string{"libonnxruntime.1.23.2.dylib", "libonnxruntime.dylib"}
	default:
		names = []string{"libonnxruntime.so.1.23.2", "libonnxruntime.so"}
	}
	var checked []string
	for _, dir := range []string{filepath.Join(runtimeRoot, platform), runtimeRoot} {
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			checked = append(checked, candidate)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("ONNX Runtime 1.23.2 shared library is missing; checked %s", strings.Join(checked, ", "))
}

func splitOversizedSourceChunk(chunk sourceChunk) (sourceChunk, sourceChunk, bool) {
	text := chunk.Text
	if len(text) < 2 {
		return sourceChunk{}, sourceChunk{}, false
	}
	mid := len(text) / 2
	leftBreak := strings.LastIndexAny(text[:mid], " \t,;:")
	rightBreakRel := strings.IndexAny(text[mid:], " \t,;:")
	breakAt := leftBreak
	if breakAt < len(text)/4 && rightBreakRel >= 0 {
		breakAt = mid + rightBreakRel
	}
	if breakAt <= 0 || breakAt >= len(text)-1 {
		breakAt = mid
	}
	leftText := strings.TrimSpace(text[:breakAt])
	rightText := strings.TrimSpace(text[breakAt:])
	if leftText == "" || rightText == "" {
		return sourceChunk{}, sourceChunk{}, false
	}
	leftOffsetBytes := strings.Index(text, leftText)
	rightOffsetBytes := strings.LastIndex(text, rightText)
	leftStartDelta := utf16Units(text[:leftOffsetBytes])
	leftEndDelta := leftStartDelta + utf16Units(leftText)
	rightStartDelta := utf16Units(text[:rightOffsetBytes])
	rightEndDelta := rightStartDelta + utf16Units(rightText)
	left := sourceChunk{Text: leftText, CharStart: chunk.CharStart + leftStartDelta, CharEnd: chunk.CharStart + leftEndDelta}
	right := sourceChunk{Text: rightText, CharStart: chunk.CharStart + rightStartDelta, CharEnd: chunk.CharStart + rightEndDelta}
	return left, right, true
}

func truncateForError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 80 {
		return value
	}
	return value[:77] + "..."
}
