package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeWorkModeMaxPasses(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "default on zero", in: 0, want: 3},
		{name: "default on negative", in: -4, want: 3},
		{name: "keeps one", in: 1, want: 1},
		{name: "keeps middle", in: 42, want: 42},
		{name: "caps at 100", in: 101, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWorkModeMaxPasses(tt.in); got != tt.want {
				t.Fatalf("normalizeWorkModeMaxPasses(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseWorkModeObserverResponseRequiresExplicitHasInput(t *testing.T) {
	valid, err := parseWorkModeObserverResponse(`{"reply":"Looks good","has_input":false,"recommendations":["  ","Ship it"]}`)
	if err != nil {
		t.Fatalf("valid observer response failed: %v", err)
	}
	if valid.HasInput {
		t.Fatalf("HasInput = true, want false")
	}
	if len(valid.Recommendations) != 1 || valid.Recommendations[0] != "Ship it" {
		t.Fatalf("recommendations were not trimmed correctly: %#v", valid.Recommendations)
	}

	if _, err := parseWorkModeObserverResponse(`{"reply":"No flag"}`); err == nil || !strings.Contains(err.Error(), "has_input") {
		t.Fatalf("missing has_input error = %v, want has_input error", err)
	}
	if _, err := parseWorkModeObserverResponse(`{"reply":"Wrong type","has_input":"false"}`); err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("string has_input error = %v, want boolean type error", err)
	}
}

func TestRequireWorkModeJSONBoolFieldForWorkerReviewComplete(t *testing.T) {
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[],"review_complete":true}`, "review_complete"); err != nil {
		t.Fatalf("review_complete bool was rejected: %v", err)
	}
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[]}`, "review_complete"); err == nil || !strings.Contains(err.Error(), "review_complete") {
		t.Fatalf("missing review_complete error = %v, want review_complete error", err)
	}
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[],"review_complete":"true"}`, "review_complete"); err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("string review_complete error = %v, want boolean type error", err)
	}
}

func TestWorkModeApplyFileOpsToTmpWorkRoutesDraftsAndRejectsNestedTmpWork(t *testing.T) {
	projectworkRoot := t.TempDir()
	projectName := "demo"
	if err := os.MkdirAll(filepath.Join(projectworkRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, "src", "app.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, skipped, blocked, diffs, err := workModeApplyFileOpsToTmpWork(projectworkRoot, projectName, []builderFileOp{
		{Path: "index.html", Action: "create", Content: "<h1>Draft</h1>"},
		{Path: "src/app.js", Action: "overwrite", Content: "draft"},
		{Path: "tmp-work/nested.js", Action: "create", Content: "bad"},
	}, nil, ProjectLimits{MaxFiles: 10, MaxFileSizeKB: 64, MaxPayloadKB: 256})
	if err != nil {
		t.Fatalf("workModeApplyFileOpsToTmpWork failed: %v", err)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, "src", "app.js"))); got != "old" {
		t.Fatalf("real project file changed to %q, want old", got)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js"))); got != "draft" {
		t.Fatalf("tmp-work overwrite = %q, want draft", got)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, workModeTmpWorkDirName, "index.html"))); got != "<h1>Draft</h1>" {
		t.Fatalf("tmp-work create = %q", got)
	}
	if _, err := os.Stat(filepath.Join(projectworkRoot, workModeTmpWorkDirName, workModeTmpWorkDirName, "nested.js")); !os.IsNotExist(err) {
		t.Fatalf("nested tmp-work file exists or stat failed unexpectedly: %v", err)
	}
	if !containsTestString(changed, "tmp-work/index.html") || !containsTestString(changed, "tmp-work/src/app.js") {
		t.Fatalf("changed paths = %#v, want tmp-work/index.html and tmp-work/src/app.js", changed)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "tmp-work/nested.js") {
		t.Fatalf("skipped = %#v, want nested tmp-work rejection", skipped)
	}
	if len(blocked) != 1 || blocked[0].Path != "tmp-work/nested.js" {
		t.Fatalf("blocked = %#v, want nested tmp-work blocked output", blocked)
	}
	if len(diffs) == 0 {
		t.Fatalf("expected diffs for draft writes")
	}
}

func TestMergeTmpWorkFileOverwritesTargetAndRemovesDraft(t *testing.T) {
	workRoot := t.TempDir()
	projectworkRoot := filepath.Join(workRoot, "projects", "demo", "projectwork")
	if err := os.MkdirAll(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectworkRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, "src", "app.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{cfg: AppConfig{WorkRoot: workRoot}}
	sourcePath, targetPath, err := app.mergeTmpWorkFile("demo", "src/app.js")
	if err != nil {
		t.Fatalf("mergeTmpWorkFile failed: %v", err)
	}
	if sourcePath != "projects/demo/projectwork/tmp-work/src/app.js" {
		t.Fatalf("sourcePath = %q", sourcePath)
	}
	if targetPath != "projects/demo/projectwork/src/app.js" {
		t.Fatalf("targetPath = %q", targetPath)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, "src", "app.js"))); got != "draft" {
		t.Fatalf("merged target = %q, want draft", got)
	}
	if _, err := os.Stat(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js")); !os.IsNotExist(err) {
		t.Fatalf("tmp-work draft still exists or stat failed unexpectedly: %v", err)
	}
}

func mustReadFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func containsTestString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
