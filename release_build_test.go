package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseBuildIncludesRequiredRuntimeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release shell-script test requires bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}

	root := t.TempDir()
	copyFile := func(src, dst string, mode os.FileMode) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, mode); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}

	copyFile("build_agentgo_all_platforms.sh", filepath.Join(root, "build_agentgo_all_platforms.sh"), 0o755)
	for _, name := range []string{"version.json", "model_names.json", "README.md"} {
		copyFile(name, filepath.Join(root, name), 0o644)
	}
	if err := os.WriteFile(filepath.Join(root, "dummy.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"templates", "assets", "system_prompts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "placeholder.txt"), []byte(dir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// User-specific notes must not be distributed.
	if err := os.WriteFile(filepath.Join(root, "agentgo_notes.md"), []byte("private notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeGo := filepath.Join(root, "fake-go")
	fakeGoScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "version" ]]; then
  echo "go version go1.23.0 test/fake"
  exit 0
fi
out=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "-o" ]]; then
    shift
    out="$1"
    break
  fi
  shift
done
if [[ -z "$out" ]]; then
  echo "missing -o" >&2
  exit 2
fi
mkdir -p "$(dirname "$out")"
printf 'fake binary\n' > "$out"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "build_agentgo_all_platforms.sh")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO_BIN="+fakeGo, "HIGH_SIERRA_GO_BIN=definitely-not-installed")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release build failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Release audit passed.") {
		t.Fatalf("release build did not report a passed audit:\n%s", output)
	}

	dist := filepath.Join(root, "dist")
	for _, name := range []string{
		"README.md", "version.json", "model_names.json", "config.json", "models.json",
		"agentgo-windows-amd64.exe", "agentgo-linux-amd64", "agentgo-linux-arm64", "agentgo-macos-arm64", "agentgo-macos-amd64",
	} {
		info, err := os.Stat(filepath.Join(dist, name))
		if err != nil {
			t.Fatalf("required release item %s missing: %v", name, err)
		}
		if info.IsDir() || info.Size() == 0 {
			t.Fatalf("required release file %s is empty or not a file", name)
		}
	}
	for _, dir := range []string{"templates", "assets", "system_prompts"} {
		info, err := os.Stat(filepath.Join(dist, dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("required release directory %s missing", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(dist, "agentgo_notes.md")); !os.IsNotExist(err) {
		t.Fatal("private agentgo_notes.md was unexpectedly included in the release")
	}
}
