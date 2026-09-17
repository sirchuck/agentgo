package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkModeR455StickyResponseActionRail(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`.work-mode-message.ai{justify-self:start;max-width:min(770px,92%);padding:4px 0;color:rgba(245,247,251,.88);display:grid;grid-template-columns:38px minmax(0,720px);column-gap:10px;align-items:start}`,
		`.work-mode-message-actions.is-sticky-rail{grid-column:1;grid-row:1;display:flex;flex-direction:column;align-items:center;justify-content:center;align-self:start;position:sticky;top:calc(50vh - 52px);`,
		`work-mode-message-ai-content`,
		`renderWorkModeMessageActions(index, { speak: isWorkModeReplySpeechSupported(), rail: true })`,
		`${actions}<div class="work-mode-message-ai-content">`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r455 sticky response controls missing %q", want)
		}
	}
}

func TestWorkModeR455KokoroChunkerTargetsThenFinishesWord(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function workModeKokoroSplitText")
	if start < 0 {
		t.Fatal("could not locate Kokoro text chunker")
	}
	endRel := strings.Index(source[start:], "function createWorkModeKokoroSession")
	if endRel < 0 {
		t.Fatal("could not isolate Kokoro text chunker")
	}
	chunker := source[start : start+endRel]
	if strings.Contains(chunker, "lastNaturalBreak") || strings.Contains(chunker, "lastSoftBreak") {
		t.Fatal("r455 chunker should not search backward for sentence/soft breaks")
	}

	script := `
const assert = require('node:assert/strict');
const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;
const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;
` + chunker + `
const longWord = 'x'.repeat(110);
const text = 'Short heading. This punctuation must not end the first request. ' + longWord + ' then continue with enough ordinary words to create another request ' + 'alpha '.repeat(45).trim();
const chunks = workModeKokoroSplitText(text);
assert.ok(chunks.length >= 2, 'expected multiple Kokoro requests');
assert.ok(chunks[0].text.includes(longWord), 'the first request must continue beyond early punctuation until the post-target word boundary');
assert.ok([...chunks[0].text].length > 100, 'a word crossing the target must be finished rather than split');
assert.equal(chunks.map(chunk => chunk.text).join(' '), text, 'chunking must preserve source words');
assert.ok(chunks.every(chunk => !/^\s|\s$/.test(chunk.text)), 'request text should be trimmed at boundaries');
`
	cmd := exec.Command("node", "-e", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("r455 Kokoro JavaScript chunking failed: %v\n%s", err, output)
	}
}

func TestWorkModeR455MoonshineStatusIsVisible(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`Pronunciation: Moonshine G2P active for unknown English words.`,
		`Pronunciation: AgentGO standard fallback`,
		`agentGOKokoroStatus.g2p_available`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r455 Moonshine status UI missing %q", want)
		}
	}
}
