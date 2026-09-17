package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKokoroFirstRequestR448UsesSmallerStartupChunk(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`const WORK_MODE_KOKORO_PREBUFFER_SECONDS = 5;`,
		`const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;`,
		`const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;`,
		`const currentTargetRunes = () => segments.length === 0`,
		`? WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES`,
		`: WORK_MODE_KOKORO_REQUEST_TARGET_RUNES;`,
		`const targetRunes = currentTargetRunes();`,
		`if (runeCount >= targetRunes && /\s/u.test(character))`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r448 first-request Kokoro chunking missing %q", want)
		}
	}
}

func TestKokoroFirstRequestR448JavaScriptChunking(t *testing.T) {
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
		t.Fatal("could not locate r448 Kokoro text chunker")
	}
	relEnd := strings.Index(source[start:], "function createWorkModeKokoroSession")
	if relEnd < 0 {
		t.Fatal("could not isolate r448 Kokoro text chunker")
	}
	chunker := source[start : start+relEnd]
	script := `
const assert = require('node:assert/strict');
const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;
const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;
` + chunker + `
const words = Array.from({length:80}, (_, i) => 'word' + String(i).padStart(2, '0'));
const text = words.join(' ');
const chunks = workModeKokoroSplitText(text);
assert.ok(chunks.length >= 2, 'expected multiple Kokoro requests');
assert.equal(chunks.map(chunk => chunk.text).join(' '), text, 'chunking must preserve all words');
assert.ok([...chunks[0].text].length >= 99, 'first Kokoro request should reach the startup target before taking a word boundary');
assert.ok([...chunks[1].text].length > [...chunks[0].text].length, 'later Kokoro requests should use the larger normal target');
assert.ok([...chunks[1].text].length >= 179, 'normal Kokoro request should reach the normal target before taking a word boundary');
`
	cmd := exec.Command("node", "-e", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("r448 first-request JavaScript chunking failed: %v\n%s", err, output)
	}
}
