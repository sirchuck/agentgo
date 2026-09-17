package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentgo/internal/kokoroipc"
)

type fakeKokoroEngine struct{ timed bool }

func (fakeKokoroEngine) RuntimeVersion() string  { return "test-runtime" }
func (e fakeKokoroEngine) TimingSupported() bool { return e.timed }
func (fakeKokoroEngine) G2PStatus() (bool, string) {
	return false, "Moonshine G2P is not installed; using AgentGO standard pronunciation fallback."
}
func (e fakeKokoroEngine) Synthesize(ctx context.Context, text, voice string, speed float32, emit func(kokoroSynthesisChunk) error) error {
	chunk := kokoroSynthesisChunk{Source: sourceChunk{Text: text, CharStart: 0, CharEnd: utf16Units(text)}, PCM: []float32{0.1, -0.1}}
	if e.timed {
		chunk.Timings = []kokoroWordTiming{{CharStart: 0, CharEnd: utf16Units(text), StartSample: 0, EndSample: 2}}
	}
	return emit(chunk)
}
func (fakeKokoroEngine) Close() error { return nil }

func withFakeKokoroEngine(t *testing.T) {
	t.Helper()
	previous := kokoroEngineFactory
	kokoroEngineFactory = func(string) (kokoroEngine, error) { return fakeKokoroEngine{}, nil }
	t.Cleanup(func() { kokoroEngineFactory = previous })
}

func makeHelperAssets(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "voices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "model.onnx"), []byte("phase2-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"am_adam.bin", "af_heart.bin", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(root, "voices", name), []byte("voice"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverAssetsSortsVoiceIDs(t *testing.T) {
	status, err := discoverAssets(makeHelperAssets(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Voices) != 2 || status.Voices[0] != "af_heart" || status.Voices[1] != "am_adam" {
		t.Fatalf("voices = %#v", status.Voices)
	}
}

func TestServeInitializeAndShutdown(t *testing.T) {
	withFakeKokoroEngine(t)
	root := makeHelperAssets(t)
	var input bytes.Buffer
	if err := kokoroipc.WriteMessage(&input, kokoroipc.Message{Type: "initialize", ID: "init-1", Protocol: kokoroipc.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if err := kokoroipc.WriteMessage(&input, kokoroipc.Message{Type: "shutdown", ID: "stop-1", Protocol: kokoroipc.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := serve(&input, &output, root, "test-instance"); err != nil {
		t.Fatal(err)
	}
	ready, err := kokoroipc.ReadMessage(&output)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.Ref != "init-1" || ready.Protocol != kokoroipc.ProtocolVersion || !ready.ModelLoaded {
		t.Fatalf("ready = %#v", ready)
	}
	if len(ready.Voices) != 2 {
		t.Fatalf("voices = %#v", ready.Voices)
	}
	if ready.G2PProvider != "standard" || ready.G2PAvailable || !strings.Contains(ready.G2PMessage, "standard pronunciation fallback") {
		t.Fatalf("G2P ready status = provider=%q available=%t message=%q", ready.G2PProvider, ready.G2PAvailable, ready.G2PMessage)
	}
	bye, err := kokoroipc.ReadMessage(&output)
	if err != nil {
		t.Fatal(err)
	}
	if bye.Type != "bye" || bye.Ref != "stop-1" {
		t.Fatalf("bye = %#v", bye)
	}
}

func TestNaturalSourceChunksUseUTF16Offsets(t *testing.T) {
	chunks := splitNaturalSourceText("Hello 😀 world. This is AgentGO speaking.")
	if len(chunks) < 1 {
		t.Fatal("expected source chunks")
	}
	for _, chunk := range chunks {
		if chunk.CharStart < 0 || chunk.CharEnd <= chunk.CharStart {
			t.Fatalf("invalid chunk offsets: %#v", chunk)
		}
	}
	if got := utf16Units("😀"); got != 2 {
		t.Fatalf("utf16Units emoji = %d, want 2", got)
	}
}

func TestNaturalSourceChunksGroupShortSentencesWithoutPeriodSpecialCases(t *testing.T) {
	text := "One short sentence. Another short sentence. The file is phonetics.md and the next sentence continues normally."
	chunks := splitNaturalSourceText(text)
	if len(chunks) != 1 {
		t.Fatalf("short sentences should remain one Kokoro request; chunks=%#v", chunks)
	}
	if chunks[0].Text != text {
		t.Fatalf("chunk text = %q, want %q", chunks[0].Text, text)
	}
}

func TestNaturalSourceChunksPreferWhitespaceNearTarget(t *testing.T) {
	word := strings.Repeat("x", 24)
	text := strings.Join([]string{word, word, word, word, word, word, word, word, word, word}, " ")
	chunks := splitNaturalSourceText(text)
	if len(chunks) < 2 {
		t.Fatalf("expected long text to be split; chunks=%#v", chunks)
	}
	if strings.Join(func() []string {
		parts := make([]string, len(chunks))
		for i, chunk := range chunks {
			parts[i] = chunk.Text
		}
		return parts
	}(), " ") != text {
		t.Fatalf("chunking changed or split source words: %#v", chunks)
	}
}

func TestEnglishPhonemizerProducesKokoroTokens(t *testing.T) {
	phonemes, tokens, err := phonemizeEnglish("AgentGO reads 42 dollars.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(phonemes) == "" || len(tokens) == 0 {
		t.Fatalf("phonemes=%q tokens=%v", phonemes, tokens)
	}
	if len(tokens) > kokoroMaxModelTokens {
		t.Fatalf("token count = %d", len(tokens))
	}
}

func TestSynthesizeWritesChunkMetadataAndPCMFrames(t *testing.T) {
	withFakeKokoroEngine(t)
	root := makeHelperAssets(t)
	var output bytes.Buffer
	s := &helperServer{
		w:          &output,
		assetRoot:  root,
		instanceID: "test-instance",
		active:     map[string]context.CancelFunc{},
		engine:     fakeKokoroEngine{},
	}
	s.synthesize(kokoroipc.Message{
		Type:     "synthesize",
		ID:       "synth-1",
		Protocol: kokoroipc.ProtocolVersion,
		Text:     "Hello AgentGO.",
		Voice:    "af_heart",
		Speed:    1.25,
	})

	var types []string
	var pcm []byte
	reader := bytes.NewReader(output.Bytes())
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
			msg, err := kokoroipc.DecodeMessage(payload)
			if err != nil {
				t.Fatal(err)
			}
			types = append(types, msg.Type)
			if msg.Ref != "synth-1" {
				t.Fatalf("message ref = %q", msg.Ref)
			}
		case kokoroipc.FrameAudio:
			frame, err := kokoroipc.DecodeAudioFrame(payload)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Ref != "synth-1" {
				t.Fatalf("audio ref = %q", frame.Ref)
			}
			pcm = append(pcm, frame.Data...)
		}
	}
	if got := strings.Join(types, ","); got != "audio_start,chunk_start,chunk_end,complete" {
		t.Fatalf("message sequence = %q", got)
	}
	if len(pcm) != 8 {
		t.Fatalf("PCM bytes = %d, want 8", len(pcm))
	}
}

func TestEnglishPhonemizerPreservesNormalizedSourceSpans(t *testing.T) {
	_, tokens, groups, err := phonemizeEnglishWithSource("Pay $42, e.g. today.")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 || len(groups) < 6 {
		t.Fatalf("tokens=%d groups=%#v", len(tokens), groups)
	}
	dollarStart := utf16Units("Pay ")
	dollarEnd := dollarStart + utf16Units("$42")
	dollarGroups := 0
	for _, group := range groups {
		if group.CharStart == dollarStart && group.CharEnd == dollarEnd {
			dollarGroups++
		}
	}
	// "forty two dollars" all map back to the original $42 source span.
	if dollarGroups < 3 {
		t.Fatalf("$42 mapped groups=%d, want >=3; groups=%#v", dollarGroups, groups)
	}
	egStart := utf16Units("Pay $42, ")
	egEnd := egStart + utf16Units("e.g.")
	egGroups := 0
	for _, group := range groups {
		if group.CharStart == egStart && group.CharEnd == egEnd {
			egGroups++
		}
	}
	if egGroups < 2 {
		t.Fatalf("e.g. mapped groups=%d, want >=2; groups=%#v", egGroups, groups)
	}
}

func TestBuildKokoroWordTimingsMapsSourceAndSamples(t *testing.T) {
	chunk := sourceChunk{Text: "Hello world", CharStart: 10, CharEnd: 21}
	groups := []phonemeSourceGroup{{CharStart: 0, CharEnd: 5, TokenStart: 0, TokenEnd: 2}, {CharStart: 6, CharEnd: 11, TokenStart: 3, TokenEnd: 5}}
	tokens := []int64{1, 2, 16, 3, 4}
	// BOS + five model tokens + EOS. The exact units do not matter because
	// AgentGO scales model duration units against the returned waveform length.
	durations := []float64{1, 2, 2, 1, 2, 2, 1}
	timings := buildKokoroWordTimings(chunk, groups, tokens, durations, 6000)
	if len(timings) != 2 {
		t.Fatalf("timings=%#v", timings)
	}
	if timings[0].CharStart != 10 || timings[0].CharEnd != 15 || timings[1].CharStart != 16 || timings[1].CharEnd != 21 {
		t.Fatalf("source spans=%#v", timings)
	}
	if timings[0].StartSample != 0 || timings[0].EndSample != 2700 || timings[1].StartSample != 3000 || timings[1].EndSample != 5400 {
		t.Fatalf("sample spans=%#v", timings)
	}
}

func TestBuildKokoroWordTimingsMergesNormalizedSourceSpan(t *testing.T) {
	chunk := sourceChunk{Text: "$42", CharStart: 8, CharEnd: 11}
	groups := []phonemeSourceGroup{
		{CharStart: 0, CharEnd: 3, TokenStart: 0, TokenEnd: 1},
		{CharStart: 0, CharEnd: 3, TokenStart: 2, TokenEnd: 3},
		{CharStart: 0, CharEnd: 3, TokenStart: 4, TokenEnd: 5},
	}
	tokens := []int64{1, 16, 2, 16, 3}
	durations := []float64{1, 2, 1, 2, 1, 2, 1}
	timings := buildKokoroWordTimings(chunk, groups, tokens, durations, 6000)
	if len(timings) != 1 {
		t.Fatalf("normalized timing spans=%#v", timings)
	}
	if timings[0].CharStart != 8 || timings[0].CharEnd != 11 || timings[0].StartSample != 0 || timings[0].EndSample <= 3000 {
		t.Fatalf("normalized timing=%#v", timings[0])
	}
}

func TestSynthesizeWritesExactTimingBeforeAudio(t *testing.T) {
	root := makeHelperAssets(t)
	var output bytes.Buffer
	s := &helperServer{w: &output, assetRoot: root, instanceID: "test-instance", active: map[string]context.CancelFunc{}, engine: fakeKokoroEngine{timed: true}}
	s.synthesize(kokoroipc.Message{Type: "synthesize", ID: "timed-1", Protocol: kokoroipc.ProtocolVersion, Text: "Hello", Voice: "af_heart", Speed: 1})
	reader := bytes.NewReader(output.Bytes())
	var types []string
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
			if len(msg.Timings) != 1 || msg.Timings[0].Kind != "word" || msg.Timings[0].CharEnd != utf16Units("Hello") || msg.Timings[0].EndSample != 2 {
				t.Fatalf("timing=%#v", msg.Timings)
			}
		}
	}
	if got := strings.Join(types, ","); got != "audio_start,chunk_start,chunk_timing,chunk_end,complete" {
		t.Fatalf("message sequence=%q", got)
	}
}

type recordingUnknownWordPronouncer struct {
	pronunciations map[string]string
	calls          []string
}

func (p *recordingUnknownWordPronouncer) Pronounce(word string) (string, bool) {
	p.calls = append(p.calls, word)
	ipa, ok := p.pronunciations[word]
	return ipa, ok
}

func TestR455UnknownNormalWordsUseOptionalG2PAfterCMUDict(t *testing.T) {
	pronouncer := &recordingUnknownWordPronouncer{pronunciations: map[string]string{
		"glyphs":     "ɡlɪfs",
		"descenders": "dɪsɛndɚz",
	}}
	phonemes, tokens, _, err := phonemizeEnglishWithSourceUsing("hello glyphs descenders HTML", pronouncer)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 || !strings.Contains(phonemes, "ɡlɪfs") || !strings.Contains(phonemes, "dɪsɛndɚz") {
		t.Fatalf("phonemes=%q tokens=%v", phonemes, tokens)
	}
	if got := strings.Join(pronouncer.calls, ","); got != "glyphs,descenders" {
		t.Fatalf("G2P calls=%q, want glyphs,descenders (CMU words and all-caps acronyms must bypass Moonshine)", got)
	}
}

func TestR455MoonshineIPANormalizesStressedERForKokoro(t *testing.T) {
	if got := normalizeMoonshineIPA("bɝd"); got != "bɜɹd" {
		t.Fatalf("normalizeMoonshineIPA = %q, want bɜɹd", got)
	}
}

func TestR455MoonshineAssetDiscoveryIsOptionalAndCompleteOnlyWithFullSet(t *testing.T) {
	assetRoot := t.TempDir()
	missing := discoverMoonshineG2PAssets(assetRoot)
	if missing.Complete || len(missing.Missing) != 4 {
		t.Fatalf("empty asset discovery=%#v", missing)
	}
	root := filepath.Join(assetRoot, filepath.FromSlash(moonshineG2PRelativeDir))
	for name, content := range map[string]string{
		"dict_filtered_heteronyms.tsv": "glyphs\tɡlɪfs\n",
		"g2p-config.json":              "{}\n",
		"oov/model.onnx":               "placeholder-model",
		"oov/onnx-config.json":         "{}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	complete := discoverMoonshineG2PAssets(assetRoot)
	if !complete.Complete || len(complete.Missing) != 0 {
		t.Fatalf("complete asset discovery=%#v", complete)
	}
}

func TestR455NaturalSourceChunksDoNotBreakEarlyAtSentencePunctuation(t *testing.T) {
	text := "Short sentence. Another sentence. " + strings.Repeat("alpha ", 40)
	chunks := splitNaturalSourceText(strings.TrimSpace(text))
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks; chunks=%#v", chunks)
	}
	firstRunes := len([]rune(chunks[0].Text))
	if firstRunes < kokoroTargetChunkRunes-1 {
		t.Fatalf("first helper chunk ended early at %d runes, target=%d: %q", firstRunes, kokoroTargetChunkRunes, chunks[0].Text)
	}
	if !strings.Contains(chunks[0].Text, "Another sentence.") {
		t.Fatalf("sentence punctuation caused an early helper split: %q", chunks[0].Text)
	}
}
