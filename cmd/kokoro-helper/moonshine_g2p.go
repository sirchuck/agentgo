package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	ort "github.com/benedoc-inc/onnxer/onnxruntime"
)

const moonshineG2PRelativeDir = "g2p/moonshine/en_us"

type moonshineG2PAssets struct {
	Root       string
	Dictionary string
	Config     string
	Model      string
	ONNXConfig string
	Complete   bool
	Missing    []string
}

type moonshineOOVConfig struct {
	ConfigSchemaVersion int              `json:"config_schema_version"`
	ModelKind           string           `json:"model_kind"`
	CharVocab           map[string]int64 `json:"char_vocab"`
	PhonemeVocab        map[string]int64 `json:"phoneme_vocab"`
	TrainConfig         struct {
		MaxSeqLen int `json:"max_seq_len"`
	} `json:"train_config"`
	OOVIndex struct {
		MaxPhonemeLen int `json:"max_phoneme_len"`
	} `json:"oov_index"`
}

type moonshineG2P struct {
	mu sync.Mutex

	session    *ort.Session
	runtime    *ort.Runtime
	outputName string

	dictionary    map[string]string
	charVocab     map[string]int64
	phonemeByID   []string
	maxSeqLen     int
	maxPhonemeLen int
	charPad       int64
	charUnk       int64
	phonemePad    int64
	phonemeBOS    int64
	phonemeEOS    int64
	closed        bool
}

func regularHelperFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func discoverMoonshineG2PAssets(assetRoot string) moonshineG2PAssets {
	root := filepath.Join(assetRoot, filepath.FromSlash(moonshineG2PRelativeDir))
	assets := moonshineG2PAssets{
		Root:       root,
		Dictionary: filepath.Join(root, "dict_filtered_heteronyms.tsv"),
		Config:     filepath.Join(root, "g2p-config.json"),
	}
	modelCandidates := []string{
		filepath.Join(root, "oov", "model.onnx"),
		filepath.Join(root, "oov", "model.ort"),
	}
	for _, candidate := range modelCandidates {
		if regularHelperFile(candidate) {
			assets.Model = candidate
			break
		}
	}
	if assets.Model == "" {
		assets.Model = modelCandidates[0]
	}
	configCandidates := []string{
		filepath.Join(root, "oov", "onnx-config.json"),
		filepath.Join(root, "oov", "config_onnx.json"),
	}
	for _, candidate := range configCandidates {
		if regularHelperFile(candidate) {
			assets.ONNXConfig = candidate
			break
		}
	}
	if assets.ONNXConfig == "" {
		assets.ONNXConfig = configCandidates[0]
	}

	checks := []struct {
		label string
		path  string
	}{
		{"dict_filtered_heteronyms.tsv", assets.Dictionary},
		{"g2p-config.json", assets.Config},
		{"oov/model.onnx (or model.ort)", assets.Model},
		{"oov/onnx-config.json", assets.ONNXConfig},
	}
	for _, check := range checks {
		if !regularHelperFile(check.path) {
			assets.Missing = append(assets.Missing, check.label)
		}
	}
	assets.Complete = len(assets.Missing) == 0
	return assets
}

func loadMoonshineDictionary(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := make(map[string]string, 90000)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		word := strings.ToLower(strings.TrimSpace(parts[0]))
		if index := strings.LastIndex(word, "("); index > 0 && strings.HasSuffix(word, ")") {
			suffix := strings.TrimSuffix(word[index+1:], ")")
			isNumber := suffix != ""
			for _, r := range suffix {
				if !unicode.IsDigit(r) {
					isNumber = false
					break
				}
			}
			if isNumber {
				word = word[:index]
			}
		}
		ipa := strings.TrimSpace(parts[1])
		if word == "" || ipa == "" {
			continue
		}
		if _, exists := entries[word]; !exists {
			entries[word] = ipa
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("Moonshine dictionary contained no pronunciation rows")
	}
	return entries, nil
}

func loadMoonshineOOVConfig(path string) (moonshineOOVConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return moonshineOOVConfig{}, err
	}
	var config moonshineOOVConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return moonshineOOVConfig{}, fmt.Errorf("decode Moonshine OOV config: %w", err)
	}
	if config.ConfigSchemaVersion != 1 {
		return moonshineOOVConfig{}, fmt.Errorf("unsupported Moonshine OOV config schema %d", config.ConfigSchemaVersion)
	}
	if strings.ToLower(strings.TrimSpace(config.ModelKind)) != "oov" {
		return moonshineOOVConfig{}, fmt.Errorf("Moonshine config model_kind is %q, expected oov", config.ModelKind)
	}
	if config.TrainConfig.MaxSeqLen <= 0 || config.OOVIndex.MaxPhonemeLen <= 1 {
		return moonshineOOVConfig{}, fmt.Errorf("Moonshine OOV config has invalid sequence limits")
	}
	for _, required := range []string{"<pad>", "<unk>"} {
		if _, ok := config.CharVocab[required]; !ok {
			return moonshineOOVConfig{}, fmt.Errorf("Moonshine char_vocab is missing %s", required)
		}
	}
	for _, required := range []string{"<pad>", "<bos>", "<eos>"} {
		if _, ok := config.PhonemeVocab[required]; !ok {
			return moonshineOOVConfig{}, fmt.Errorf("Moonshine phoneme_vocab is missing %s", required)
		}
	}
	return config, nil
}

func moonshinePhonemeLookup(vocab map[string]int64) []string {
	maxID := int64(-1)
	for _, id := range vocab {
		if id > maxID {
			maxID = id
		}
	}
	if maxID < 0 || maxID > 1<<20 {
		return nil
	}
	out := make([]string, int(maxID)+1)
	for token, id := range vocab {
		if id >= 0 && int(id) < len(out) {
			out[id] = token
		}
	}
	return out
}

func newMoonshineG2P(assetRoot string, runtimeHandle *ort.Runtime, env *ort.Env) (*moonshineG2P, string) {
	assets := discoverMoonshineG2PAssets(assetRoot)
	if !assets.Complete {
		if len(assets.Missing) == 4 {
			return nil, "Moonshine G2P is not installed; using AgentGO standard pronunciation fallback."
		}
		return nil, "Moonshine G2P assets are incomplete (missing " + strings.Join(assets.Missing, ", ") + "); using AgentGO standard pronunciation fallback."
	}
	if runtimeHandle == nil || env == nil {
		return nil, "Moonshine G2P could not reuse the Kokoro ONNX runtime; using AgentGO standard pronunciation fallback."
	}

	dictionary, err := loadMoonshineDictionary(assets.Dictionary)
	if err != nil {
		return nil, fmt.Sprintf("Moonshine G2P dictionary could not be loaded (%v); using AgentGO standard pronunciation fallback.", err)
	}
	config, err := loadMoonshineOOVConfig(assets.ONNXConfig)
	if err != nil {
		return nil, fmt.Sprintf("Moonshine G2P configuration could not be loaded (%v); using AgentGO standard pronunciation fallback.", err)
	}
	session, err := runtimeHandle.NewSession(env, assets.Model, nil)
	if err != nil {
		return nil, fmt.Sprintf("Moonshine G2P model could not be loaded (%v); using AgentGO standard pronunciation fallback.", err)
	}
	inputs := make(map[string]bool)
	for _, name := range session.InputNames() {
		inputs[strings.TrimSpace(name)] = true
	}
	var missingInputs []string
	for _, name := range []string{"encoder_input_ids", "encoder_attention_mask", "decoder_input_ids", "decoder_attention_mask"} {
		if !inputs[name] {
			missingInputs = append(missingInputs, name)
		}
	}
	outputs := session.OutputNames()
	if len(missingInputs) > 0 || len(outputs) == 0 {
		session.Close()
		if len(missingInputs) > 0 {
			return nil, "Moonshine G2P model is incompatible (missing input(s): " + strings.Join(missingInputs, ", ") + "); using AgentGO standard pronunciation fallback."
		}
		return nil, "Moonshine G2P model exposes no output tensor; using AgentGO standard pronunciation fallback."
	}

	g2p := &moonshineG2P{
		session:       session,
		runtime:       runtimeHandle,
		outputName:    strings.TrimSpace(outputs[0]),
		dictionary:    dictionary,
		charVocab:     config.CharVocab,
		phonemeByID:   moonshinePhonemeLookup(config.PhonemeVocab),
		maxSeqLen:     config.TrainConfig.MaxSeqLen,
		maxPhonemeLen: config.OOVIndex.MaxPhonemeLen,
		charPad:       config.CharVocab["<pad>"],
		charUnk:       config.CharVocab["<unk>"],
		phonemePad:    config.PhonemeVocab["<pad>"],
		phonemeBOS:    config.PhonemeVocab["<bos>"],
		phonemeEOS:    config.PhonemeVocab["<eos>"],
	}
	if len(g2p.phonemeByID) == 0 {
		session.Close()
		return nil, "Moonshine G2P phoneme vocabulary is invalid; using AgentGO standard pronunciation fallback."
	}
	return g2p, "Moonshine G2P is available for unknown English words."
}

func (g *moonshineG2P) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	if g.session != nil {
		g.session.Close()
		g.session = nil
	}
}

func (g *moonshineG2P) Pronounce(word string) (string, bool) {
	if g == nil {
		return "", false
	}
	clean := strings.ToLower(strings.TrimSpace(strings.Trim(word, "'")))
	if clean == "" {
		return "", false
	}
	if ipa := strings.TrimSpace(g.dictionary[clean]); ipa != "" {
		return normalizeMoonshineIPA(ipa), true
	}
	ipa, err := g.predictOOV(clean)
	if err != nil || strings.TrimSpace(ipa) == "" {
		return "", false
	}
	return normalizeMoonshineIPA(ipa), true
}

func (g *moonshineG2P) predictOOV(word string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.session == nil || g.runtime == nil {
		return "", fmt.Errorf("Moonshine G2P is closed")
	}

	charIDs := make([]int64, 0, len([]rune(word)))
	for _, r := range word {
		id, ok := g.charVocab[string(r)]
		if !ok {
			id = g.charUnk
		}
		charIDs = append(charIDs, id)
		if len(charIDs) >= g.maxSeqLen {
			break
		}
	}
	if len(charIDs) == 0 {
		return "", fmt.Errorf("Moonshine G2P word is empty")
	}
	encoderIDs := make([]int64, g.maxSeqLen)
	encoderMask := make([]int64, g.maxSeqLen)
	for i := range encoderIDs {
		encoderIDs[i] = g.charPad
	}
	copy(encoderIDs, charIDs)
	for i := 0; i < len(charIDs); i++ {
		encoderMask[i] = 1
	}

	encIDsTensor, err := ort.NewTensorValue(g.runtime, encoderIDs, []int64{1, int64(g.maxSeqLen)})
	if err != nil {
		return "", fmt.Errorf("create Moonshine encoder_input_ids tensor: %w", err)
	}
	defer encIDsTensor.Close()
	encMaskTensor, err := ort.NewTensorValue(g.runtime, encoderMask, []int64{1, int64(g.maxSeqLen)})
	if err != nil {
		return "", fmt.Errorf("create Moonshine encoder_attention_mask tensor: %w", err)
	}
	defer encMaskTensor.Close()

	current := []int64{g.phonemeBOS}
	for step := 0; step < g.maxPhonemeLen; step++ {
		prefixLen := len(current)
		if prefixLen > g.maxPhonemeLen {
			break
		}
		decoderIDs := make([]int64, g.maxPhonemeLen)
		decoderMask := make([]int64, g.maxPhonemeLen)
		for i := range decoderIDs {
			decoderIDs[i] = g.phonemePad
		}
		copy(decoderIDs, current)
		for i := 0; i < prefixLen; i++ {
			decoderMask[i] = 1
		}

		decIDsTensor, err := ort.NewTensorValue(g.runtime, decoderIDs, []int64{1, int64(g.maxPhonemeLen)})
		if err != nil {
			return "", fmt.Errorf("create Moonshine decoder_input_ids tensor: %w", err)
		}
		decMaskTensor, err := ort.NewTensorValue(g.runtime, decoderMask, []int64{1, int64(g.maxPhonemeLen)})
		if err != nil {
			decIDsTensor.Close()
			return "", fmt.Errorf("create Moonshine decoder_attention_mask tensor: %w", err)
		}

		outputs, runErr := g.session.Run(context.Background(), map[string]*ort.Value{
			"encoder_input_ids":      encIDsTensor,
			"encoder_attention_mask": encMaskTensor,
			"decoder_input_ids":      decIDsTensor,
			"decoder_attention_mask": decMaskTensor,
		}, ort.WithOutputNames(g.outputName))
		decMaskTensor.Close()
		decIDsTensor.Close()
		if runErr != nil {
			return "", fmt.Errorf("run Moonshine OOV model: %w", runErr)
		}
		value := outputs[g.outputName]
		if value == nil {
			closeKokoroOutputs(outputs)
			return "", fmt.Errorf("Moonshine OOV model returned no logits")
		}
		logits, shape, dataErr := ort.GetTensorData[float32](value)
		if dataErr != nil {
			closeKokoroOutputs(outputs)
			return "", fmt.Errorf("read Moonshine OOV logits: %w", dataErr)
		}
		if len(shape) < 3 || len(logits) == 0 {
			closeKokoroOutputs(outputs)
			return "", fmt.Errorf("Moonshine OOV logits have unexpected shape %v", shape)
		}
		vocabSize := int(shape[len(shape)-1])
		if vocabSize <= 0 || prefixLen-1 >= int(shape[len(shape)-2]) {
			closeKokoroOutputs(outputs)
			return "", fmt.Errorf("Moonshine OOV logits have incompatible shape %v", shape)
		}
		offset := (prefixLen - 1) * vocabSize
		if offset < 0 || offset+vocabSize > len(logits) {
			closeKokoroOutputs(outputs)
			return "", fmt.Errorf("Moonshine OOV logits are shorter than expected")
		}
		next := 0
		best := logits[offset]
		for i := 1; i < vocabSize; i++ {
			if logits[offset+i] > best {
				best = logits[offset+i]
				next = i
			}
		}
		closeKokoroOutputs(outputs)
		nextID := int64(next)
		if nextID == g.phonemeEOS || nextID == g.phonemePad {
			break
		}
		current = append(current, nextID)
		if len(current) >= g.maxPhonemeLen {
			break
		}
	}

	parts := make([]string, 0, len(current)-1)
	for _, id := range current[1:] {
		if id == g.phonemeEOS || id == g.phonemePad || id < 0 || int(id) >= len(g.phonemeByID) {
			continue
		}
		token := g.phonemeByID[id]
		if token == "" || strings.HasPrefix(token, "<") {
			continue
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, ""), nil
}
