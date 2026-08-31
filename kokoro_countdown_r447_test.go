package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKokoroCountdownR447ReturnsDirectlyToSpeaker(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)

	for _, want := range []string{
		`const WORK_MODE_KOKORO_PREBUFFER_SECONDS = 5;`,
		`const next = Math.max(1, WORK_MODE_KOKORO_PREBUFFER_SECONDS - elapsed);`,
		`session.countdownValue = null;`,
		`btn.innerHTML = state.preparing`,
		`: renderWorkModeSpeakerIcon();`,
		`const countdown = state.preparing ?`,
		`if (session.playRequested && !session.playing) {`,
		`startWorkModeKokoroPlayback(session).catch`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r447 Kokoro countdown missing %q", want)
		}
	}

	for _, obsolete := range []string{
		`WORK_MODE_KOKORO_READY_ZERO_MS`,
		`readyTransitionTimer`,
		`readyZero`,
		`showWorkModeKokoroReadyZero`,
		`session.countdownValue = 0;`,
	} {
		if strings.Contains(source, obsolete) {
			t.Fatalf("r447 Kokoro countdown still contains obsolete zero-state marker %q", obsolete)
		}
	}
}
