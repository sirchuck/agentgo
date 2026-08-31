package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFakeTGZ(t *testing.T, filename, innerName string) {
	t.Helper()
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	body := []byte("fake onnx runtime\n")
	if err := tw.WriteHeader(&tar.Header{Name: "pkg/lib/" + innerName, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
}

func writeFakeZIP(t *testing.T, filename, innerName string) {
	t.Helper()
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	w, err := zw.Create("pkg/bin/" + innerName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("fake onnx runtime\n")); err != nil {
		t.Fatal(err)
	}
}

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
	if err := os.MkdirAll(filepath.Join(root, "cmd", "kokoro-helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "kokoro-helper", "go.mod"), []byte("module agentgo/kokoro-helper\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "kokoro-helper", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(root, "ort-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeZIP(t, filepath.Join(cache, "onnxruntime-win-x64-1.23.2.zip"), "onnxruntime.dll")
	writeFakeTGZ(t, filepath.Join(cache, "onnxruntime-linux-x64-1.23.2.tgz"), "libonnxruntime.so.1.23.2")
	writeFakeTGZ(t, filepath.Join(cache, "onnxruntime-linux-aarch64-1.23.2.tgz"), "libonnxruntime.so.1.23.2")
	writeFakeTGZ(t, filepath.Join(cache, "onnxruntime-osx-arm64-1.23.2.tgz"), "libonnxruntime.1.23.2.dylib")
	writeFakeTGZ(t, filepath.Join(cache, "onnxruntime-osx-x86_64-1.23.2.tgz"), "libonnxruntime.1.23.2.dylib")

	// User-specific notes must not be distributed.
	if err := os.WriteFile(filepath.Join(root, "agentgo_notes.md"), []byte("private notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeGo := filepath.Join(root, "fake-go")
	fakeGoScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "version" ]]; then
  echo "go version go1.25.0 test/fake"
  exit 0
fi
if [[ "${1:-}" == "env" && "${2:-}" == "GOVERSION" ]]; then
  echo "go1.25.0"
  exit 0
fi
if [[ -n "${FAKE_GO_LOG:-}" ]]; then
  printf '%s|%s|%s|%s\n' "$PWD" "${GOOS:-}" "${GOARCH:-}" "$*" >> "$FAKE_GO_LOG"
fi
if [[ "${1:-}" == "mod" && ( "${2:-}" == "tidy" || "${2:-}" == "verify" ) ]]; then
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

	fakeGoLog := filepath.Join(root, "fake-go.log")
	cmd := exec.Command("bash", "build_agentgo_all_platforms.sh")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO_BIN="+fakeGo, "KOKORO_HELPER_GO_BIN="+fakeGo, "FAKE_GO_LOG="+fakeGoLog, "AGENTGO_KOKORO_RUNTIME_CACHE="+cache)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release build failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Release audit passed.") {
		t.Fatalf("release build did not report a passed audit:\n%s", output)
	}

	logData, err := os.ReadFile(fakeGoLog)
	if err != nil {
		t.Fatalf("read fake Go build log: %v", err)
	}
	logText := string(logData)
	for _, target := range []string{
		"|windows|amd64|build -mod=readonly",
		"|linux|amd64|build -mod=readonly",
		"|linux|arm64|build -mod=readonly",
		"|darwin|arm64|build -mod=readonly",
		"|darwin|amd64|build -mod=readonly",
	} {
		found := false
		for _, line := range strings.Split(logText, "\n") {
			if strings.Contains(line, "cmd/.kokoro-helper-build.") && strings.Contains(line, target) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("release builder did not cross-compile Kokoro helper target %q; log:\n%s", target, logText)
		}
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
	for _, item := range []struct{ platform, helper, library string }{
		{"windows-amd64", "kokoro-helper.exe", "onnxruntime.dll"},
		{"linux-amd64", "kokoro-helper", "libonnxruntime.so.1.23.2"},
		{"linux-arm64", "kokoro-helper", "libonnxruntime.so.1.23.2"},
		{"darwin-arm64", "kokoro-helper", "libonnxruntime.1.23.2.dylib"},
		{"darwin-amd64", "kokoro-helper", "libonnxruntime.1.23.2.dylib"},
	} {
		for _, name := range []string{item.helper, item.library} {
			path := filepath.Join(dist, "assets", "kokoro", "runtime", item.platform, name)
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				t.Fatalf("Kokoro runtime item missing %s: %v", path, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dist, "agentgo-macos-highsierra-amd64")); !os.IsNotExist(err) {
		t.Fatal("obsolete macOS High Sierra binary was unexpectedly included in the release")
	}
	if _, err := os.Stat(filepath.Join(dist, "agentgo_notes.md")); !os.IsNotExist(err) {
		t.Fatal("private agentgo_notes.md was unexpectedly included in the release")
	}
}
