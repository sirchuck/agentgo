package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKokoroStartupR446ClaimsListenersBeforeWarmup(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	bindPos := strings.Index(source, `listener, err := net.Listen("tcp", httpAddr)`)
	warmPos := strings.LastIndex(source, `app.syncKokoroLifecycle()`)
	if bindPos < 0 || warmPos < 0 {
		t.Fatalf("r446 startup markers missing: bind=%d warm=%d", bindPos, warmPos)
	}
	if bindPos >= warmPos {
		t.Fatalf("Kokoro warm-up occurs before AgentGO claims its listener: bind=%d warm=%d", bindPos, warmPos)
	}
	for _, want := range []string{
		`server.Serve(listener)`,
		`server.ServeTLS(listener, cfg.TLSCertFile, cfg.TLSKeyFile)`,
		`Kokoro background warm-up`,
	} {
		if !strings.Contains(source, want) && !strings.Contains(mustReadR446File(t, "tts_config.go"), want) {
			t.Fatalf("r446 non-blocking startup missing %q", want)
		}
	}
}

func TestKokoroStartupR446ProcessRecordTracksAgentGOOwner(t *testing.T) {
	root := t.TempDir()
	manager := newKokoroManager(root, nil)
	if err := manager.writeProcessRecord(12345, "r446-test-instance", filepath.Join(root, "kokoro-helper")); err != nil {
		t.Fatal(err)
	}
	statePath, _ := manager.processRecordPaths()
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var record kokoroProcessRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.OwnerPID != os.Getpid() {
		t.Fatalf("owner PID = %d, want %d", record.OwnerPID, os.Getpid())
	}
	if strings.TrimSpace(record.OwnerPath) == "" {
		t.Fatal("owner executable path was not recorded")
	}
}

func TestKokoroStartupR446ReusesRunningManagerHelper(t *testing.T) {
	root := prepareKokoroManagerTestAssets(t)
	manager := newKokoroManager(root, nil)
	var starts atomic.Int32
	manager.commandFactory = fakeKokoroCommandFactory("ready", &starts)
	defer manager.Close()

	if err := manager.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := manager.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("Kokoro helper starts = %d, want 1; a loaded helper should be reused", got)
	}
}

func TestKokoroStartupR446RefusesOverlappingModelReload(t *testing.T) {
	source := mustReadR446File(t, "kokoro_manager.go")
	killPos := strings.Index(source, `if err := process.Kill(); err != nil`)
	waitPos := strings.Index(source, `waitForKokoroProcessExit(state.PID, kokoroStaleExitTimeout)`)
	if killPos < 0 || waitPos < 0 || waitPos <= killPos {
		t.Fatalf("r446 stale-helper cleanup must kill then confirm exit before reload: kill=%d wait=%d", killPos, waitPos)
	}
	for _, want := range []string{
		`kokoroOwnerStillAlive(state)`,
		`AgentGO did not load a second model`,
		`refusing to load another model`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("r446 overlap protection missing %q", want)
		}
	}
}

func mustReadR446File(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
