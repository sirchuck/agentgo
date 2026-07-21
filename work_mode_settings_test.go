package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWorkModeSettingsTestApp(t *testing.T) *App {
	t.Helper()
	app := &App{
		cfg: AppConfig{
			WorkRoot: t.TempDir(),
			Models: []ModelConfig{
				{ID: 1, Label: "Builder One", WorkDir: "models/builder-one"},
				{ID: 2, Label: "Builder Two", WorkDir: "models/builder-two"},
			},
		},
		toggles:       map[string]bool{"1": true, "2": false},
		activeCancels: map[string]activeCancelEntry{},
	}
	if err := app.ensureProjectScaffold("Demo"); err != nil {
		t.Fatalf("ensure project scaffold: %v", err)
	}
	app.activeProjectName = "Demo"
	return app
}

func TestWorkModeSettingsDefaultOffAndProjectContinuedMemory(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	settings, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if settings.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("memory default = %q, want off", settings.MemoryDefault)
	}

	settingsPath, err := app.workModeSettingsPath("Demo")
	if err != nil {
		t.Fatal(err)
	}
	continuedPath, err := app.workModeContinuedMemoryPath("Demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{settingsPath, continuedPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o, want private", path, info.Mode().Perm())
		}
	}

	const initial = "PROJECT GLOBAL CONTINUED MEMORY"
	if err := writeWorkModeMemoryFile(continuedPath, initial); err != nil {
		t.Fatal(err)
	}
	for _, model := range app.cfg.Models {
		_, metaRoot, pathErr := app.projectPaths(model, "Demo")
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		data, name, writePath, persist, resolveErr := resolveWorkModeRequestMemory(metaRoot, continuedPath, workModeRequest{UseMemory: true})
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if string(data) != initial || name != "" || writePath != continuedPath || !persist {
			t.Fatalf("model %d resolved data=%q name=%q path=%q persist=%v", model.ID, string(data), name, writePath, persist)
		}
	}
}

func TestNamedWorkModeMemoryIsUpdatedDirectly(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, fileName, err := normalizeWorkModeMemoryFileName("Planning")
	if err != nil {
		t.Fatal(err)
	}
	namedPath := filepath.Join(memoriesRoot, fileName)
	if err := writeWorkModeMemoryFile(namedPath, "NAMED START"); err != nil {
		t.Fatal(err)
	}
	continuedPath, err := app.workModeContinuedMemoryPath("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkModeMemoryFile(continuedPath, "CONTINUED UNCHANGED"); err != nil {
		t.Fatal(err)
	}

	data, displayName, writePath, persist, err := resolveWorkModeRequestMemory(metaRoot, continuedPath, workModeRequest{UseMemory: true, MemoryName: "Planning"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "NAMED START" || displayName != "Planning" || writePath != namedPath || !persist {
		t.Fatalf("resolved data=%q name=%q path=%q persist=%v", string(data), displayName, writePath, persist)
	}
	if err := writeWorkModeRequestMemory(writePath, persist, "NAMED UPDATED"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(namedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "NAMED UPDATED" {
		t.Fatalf("named memory = %q", string(updated))
	}
	continued, err := os.ReadFile(continuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(continued) != "CONTINUED UNCHANGED" {
		t.Fatalf("continued memory changed to %q", string(continued))
	}
}

func TestNamedDefaultBuilderMismatchDoesNotErasePreference(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, fileName, err := normalizeWorkModeMemoryFileName("Builder One Notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkModeMemoryFile(filepath.Join(memoriesRoot, fileName), "notes"); err != nil {
		t.Fatal(err)
	}
	configured := workModeSettingsFile{
		MemoryDefault:        workModeMemoryDefaultNamed,
		DefaultMemoryFile:    "Builder One Notes",
		DefaultMemoryModelID: "1",
	}
	if err := app.saveWorkModeSettings("Demo", configured); err != nil {
		t.Fatal(err)
	}

	response, err := app.workModeSettingsResponse("Demo", "2", "")
	if err != nil {
		t.Fatal(err)
	}
	if response.Settings.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("effective default = %q, want off for other Builder", response.Settings.MemoryDefault)
	}
	if !strings.Contains(response.Message, "different Builder") {
		t.Fatalf("message = %q", response.Message)
	}
	persisted, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MemoryDefault != workModeMemoryDefaultNamed || persisted.DefaultMemoryModelID != "1" || persisted.DefaultMemoryFile != "Builder One Notes" {
		t.Fatalf("named preference was erased: %#v", persisted)
	}
}

func TestMissingNamedDefaultResetsToOff(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	configured := workModeSettingsFile{
		MemoryDefault:        workModeMemoryDefaultNamed,
		DefaultMemoryFile:    "Missing Memory",
		DefaultMemoryModelID: "1",
	}
	if err := app.saveWorkModeSettings("Demo", configured); err != nil {
		t.Fatal(err)
	}
	response, err := app.workModeSettingsResponse("Demo", "1", "")
	if err != nil {
		t.Fatal(err)
	}
	if response.Settings.MemoryDefault != workModeMemoryDefaultOff || !strings.Contains(response.Message, "reset to Off") {
		t.Fatalf("response = %#v", response)
	}
	persisted, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("persisted default = %q, want off", persisted.MemoryDefault)
	}
}

func TestResolveWorkModeMemoryRejectsMissingNamedFile(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	continuedPath, err := app.workModeContinuedMemoryPath("Demo")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = resolveWorkModeRequestMemory(metaRoot, continuedPath, workModeRequest{UseMemory: true, MemoryName: "Missing"})
	if err == nil || !strings.Contains(err.Error(), "saved memory file was not found") {
		t.Fatalf("missing named memory error = %v", err)
	}
}

func TestDeletingNamedDefaultResetsProjectDefaultOff(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, fileName, err := normalizeWorkModeMemoryFileName("Delete Me")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkModeMemoryFile(filepath.Join(memoriesRoot, fileName), "memory"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveWorkModeSettings("Demo", workModeSettingsFile{
		MemoryDefault:        workModeMemoryDefaultNamed,
		DefaultMemoryFile:    "Delete Me",
		DefaultMemoryModelID: "1",
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/work-mode/memory/delete", strings.NewReader(`{"modelId":"1","name":"Delete Me"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(activeSessionHeader, session.SessionID)
	recorder := httptest.NewRecorder()
	app.handleWorkModeMemoryDelete(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	settings, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if settings.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("memory default = %q, want off", settings.MemoryDefault)
	}
}
