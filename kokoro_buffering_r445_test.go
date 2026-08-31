package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKokoroBufferedPlaybackR445Template(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`const WORK_MODE_KOKORO_PREBUFFER_SECONDS = 5;`,
		`const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;`,
		`const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;`,
		`Math.max(1, WORK_MODE_KOKORO_PREBUFFER_SECONDS - elapsed)`,
		`session.countdownValue = null;`,
		`.work-mode-speech-countdown{color:#fff`,
		`session.bufferedSeconds + 0.001 >= WORK_MODE_KOKORO_PREBUFFER_SECONDS`,
		`voice:String(agentGOTTSVoice || '').trim()`,
		`speed:Number(agentGOTTSSpeed) || 1`,
		`voice:session.voice, speed:session.speed`,
		`prebufferWorkModeReplyMessage(index)`,
		`if (workModeKokoroIsEngaged() || workModeReplySpeechUtterance) return false;`,
		`if (!session.playRequested && session.bufferReady) break;`,
		`data-work-pause-message`,
		`await context.suspend()`,
		`window.speechSynthesis.pause()`,
		`.work-mode-ai-speak-btn.is-speaking svg{animation:agentgo-speaker-breathe`,
		`if (existing.playing || existing.paused || options.restart)`,
		`stopWorkModeReplySpeech({ silent:true });`,
		`if (workModeReplySpeechUtterance) stopWorkModeReplySpeech({ silent:true });`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("r445 buffered speech template missing %q", required)
		}
	}
}

func TestKokoroBufferedPlaybackR445JavaScriptChunking(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function workModeKokoroSplitText")
	end := strings.Index(source[start:], "function createWorkModeKokoroSession")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate r445 Kokoro text chunker")
	}
	chunker := source[start : start+end]
	script := `
const assert = require('node:assert/strict');
const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;
const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;
` + chunker + `
const shortText = 'One short sentence. Another short sentence. The file is phonetics.md and the next sentence continues normally.';
const shortChunks = workModeKokoroSplitText(shortText);
assert.ok(shortChunks.length >= 1);
for (const chunk of shortChunks) {
  assert.equal(chunk.text, shortText.slice(chunk.start, chunk.end));
}
for (let i = 1; i < shortChunks.length; i++) {
  assert.match(shortText.slice(shortChunks[i - 1].end, shortChunks[i].start), /^\s*$/);
}
const word = 'xxxxxxxxxxxxxxxxxxxxxxxx';
const longText = Array(10).fill(word).join(' ');
const longChunks = workModeKokoroSplitText(longText);
assert.ok(longChunks.length >= 2);
assert.equal(longChunks.map(chunk => chunk.text).join(' '), longText);
assert.ok(longChunks.every(chunk => !chunk.text.startsWith(' ') && !chunk.text.endsWith(' ')));
`
	cmd := exec.Command("node", "-e", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("r445 Kokoro JavaScript chunking failed: %v\n%s", err, output)
	}
}
