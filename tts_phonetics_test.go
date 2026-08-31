package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentgo/internal/kokoroipc"
)

func TestCustomPhoneticsParsesAndAppliesWholeWordRules(t *testing.T) {
	content := `# comments are ignored
AgentGO => Agent Go
SQL => See Quil
AgentGO Voice => Agent Go Voice
bad line
`
	rules, ignored := parsePhoneticRules(content)
	if ignored != 1 {
		t.Fatalf("ignored lines = %d, want 1", ignored)
	}
	if len(rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(rules))
	}
	if rules[0].Written != "AgentGO Voice" {
		t.Fatalf("longest rule should win first, got %q", rules[0].Written)
	}

	source := "AgentGO uses SQL. AgentGO2 stays unchanged. agentgo voice works."
	transform := applyPhoneticRules(source, rules)
	want := "Agent Go uses See Quil. AgentGO2 stays unchanged. Agent Go Voice works."
	if transform.Text != want {
		t.Fatalf("phonetic speech = %q, want %q", transform.Text, want)
	}
}

func TestCustomPhoneticsRemapsKokoroTimingToDisplayedSource(t *testing.T) {
	rules, _ := parsePhoneticRules("AgentGO => Agent Go\n")
	transform := applyPhoneticRules("AgentGO rocks.", rules)
	if transform.Text != "Agent Go rocks." {
		t.Fatalf("transformed text = %q", transform.Text)
	}

	message := kokoroipc.Message{
		Type:       "chunk_timing",
		ChunkIndex: 0,
		Timings: []kokoroipc.TimingSpan{
			{Kind: "word", CharStart: 0, CharEnd: 5, StartSample: 0, EndSample: 100},
			{Kind: "word", CharStart: 6, CharEnd: 8, StartSample: 100, EndSample: 200},
			{Kind: "word", CharStart: 9, CharEnd: 14, StartSample: 200, EndSample: 300},
		},
	}
	transform.remapMessage(&message)
	if got := message.Timings[0]; got.CharStart != 0 || got.CharEnd != 7 {
		t.Fatalf("Agent timing remap = %#v, want source span 0..7", got)
	}
	if got := message.Timings[1]; got.CharStart != 0 || got.CharEnd != 7 {
		t.Fatalf("Go timing remap = %#v, want source span 0..7", got)
	}
	if got := message.Timings[2]; got.CharStart != 8 || got.CharEnd != 13 {
		t.Fatalf("rocks timing remap = %#v, want source span 8..13", got)
	}

	chunk := kokoroipc.Message{Type: "chunk_start", CharStart: 0, CharEnd: utf16StringUnits(transform.Text)}
	transform.remapMessage(&chunk)
	if chunk.CharStart != 0 || chunk.CharEnd != utf16StringUnits("AgentGO rocks.") {
		t.Fatalf("chunk remap = %d..%d", chunk.CharStart, chunk.CharEnd)
	}
}

func TestCustomPhoneticsEndpointLoadsAndSavesFile(t *testing.T) {
	root := t.TempDir()
	app := &App{appRoot: root}

	get := httptest.NewRequest(http.MethodGet, "/api/config/tts/phonetics", nil)
	getRR := httptest.NewRecorder()
	app.handleTTSPhonetics(getRR, get)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getRR.Code, getRR.Body.String())
	}
	var initial phoneticsResponse
	if err := json.Unmarshal(getRR.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Path != phoneticsRelativePath || initial.Content != "" || initial.RuleCount != 0 {
		t.Fatalf("initial response = %#v", initial)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(phoneticsRelativePath))); !os.IsNotExist(err) {
		t.Fatalf("missing library should not be recreated from hard-coded rules; stat err=%v", err)
	}

	custom := "# CUSTOM PHONETICS\nFrostGrid => Frost Grid\nSQL => See Quil\n"
	post := httptest.NewRequest(http.MethodPost, "/api/config/tts/phonetics", strings.NewReader(`{"content":`+mustJSON(custom)+`}`))
	postRR := httptest.NewRecorder()
	app.handleTTSPhonetics(postRR, post)
	if postRR.Code != http.StatusOK {
		t.Fatalf("POST status = %d body=%s", postRR.Code, postRR.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(phoneticsRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("saved phonetics = %q, want %q", string(data), custom)
	}
}

func TestShippedCustomPhoneticsLibraryOwnsStarterRules(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("system_prompts", "phonetics.md"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"# CUSTOM PHONETICS",
		"# Tell AgentGO how words should be spoken.",
		"# Enter one replacement per line:",
		"# Written Word => Spoken Pronunciation",
		"AgentGO => Agent Go",
		"SQL => See Quil",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("phonetics.md missing %q", want)
		}
	}
}

func mustJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestCustomPhoneticsTemplateHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"Custom Phonetics",
		"yetiPhoneticsBtn",
		"phoneticsModal",
		"phoneticsEditor",
		"/api/config/tts/phonetics",
		"openAgentGOPhonetics",
		"saveAgentGOPhonetics",
		"AgentGO’s displayed response is never changed.",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("template missing %q", want)
		}
	}
}
