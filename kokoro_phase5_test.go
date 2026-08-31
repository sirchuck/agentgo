package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKokoroPhase5UsesSeparatePureGoHelperModule(t *testing.T) {
	rootMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootMod), "onnxruntime_go") || strings.Contains(string(rootMod), "onnxer") {
		t.Fatalf("main AgentGO module must not carry native-helper dependencies:\n%s", rootMod)
	}

	helperMod, err := os.ReadFile(filepath.Join("cmd", "kokoro-helper", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"module agentgo/kokoro-helper", "go 1.25", "github.com/benedoc-inc/onnxer v0.7.1-0.20260807134843-4a9e532648b2", "replace agentgo => ../.."} {
		if !strings.Contains(string(helperMod), want) {
			t.Fatalf("Kokoro helper go.mod missing %q:\n%s", want, helperMod)
		}
	}

	engine, err := os.ReadFile(filepath.Join("cmd", "kokoro-helper", "engine_onnx.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(engine)
	if strings.Contains(source, "import \"C\"") || strings.Contains(source, "github.com/yalue/onnxruntime_go") || strings.Contains(source, "//go:build kokoro_onnx") {
		t.Fatalf("Phase 5 helper still contains the old CGO/tagged ONNX path")
	}
	for _, want := range []string{
		"github.com/benedoc-inc/onnxer/onnxruntime",
		"ort.NewRuntime",
		"ortRuntime.NewSession",
		"DisableTelemetry",
		"ort.NewTensorValue",
		"ort.WithOutputNames",
		"GetTensorElementType",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Phase 5 pure-Go engine missing %q", want)
		}
	}
	stub, err := os.ReadFile(filepath.Join("cmd", "kokoro-helper", "engine_stub.go"))
	if err != nil {
		t.Fatalf("Phase 5 compatibility placeholder must be present for overlay upgrades: %v", err)
	}
	stubSource := string(stub)
	if strings.Contains(stubSource, "func newKokoroEngine") || strings.Contains(stubSource, "//go:build") {
		t.Fatalf("legacy engine_stub.go must be inert and must not define or select a Kokoro engine:\n%s", stubSource)
	}
	if !strings.Contains(stubSource, "inert compatibility placeholder") {
		t.Fatalf("engine_stub.go does not document its overlay-upgrade purpose:\n%s", stubSource)
	}
}

func TestKokoroPhase5AllPlatformBuilderPackagesNativeRuntime(t *testing.T) {
	data, err := os.ReadFile("build_agentgo_all_platforms.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"onnxruntime-win-x64-${ONNXRUNTIME_VERSION}.zip",
		"onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}.tgz",
		"onnxruntime-linux-aarch64-${ONNXRUNTIME_VERSION}.tgz",
		"onnxruntime-osx-arm64-${ONNXRUNTIME_VERSION}.tgz",
		"onnxruntime-osx-x86_64-${ONNXRUNTIME_VERSION}.tgz",
		"windows-amd64",
		"linux-amd64",
		"linux-arm64",
		"darwin-arm64",
		"darwin-amd64",
		"CGO_ENABLED=0 GOOS=\"$goos\" GOARCH=\"$goarch\"",
		"mod tidy",
		"mod verify",
		"-mod=readonly",
		"Validating Kokoro helper cross-builds",
		"GOTOOLCHAIN",
		"AGENTGO_KOKORO_RUNTIME_CACHE",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("all-platform builder missing Kokoro runtime contract %q", want)
		}
	}
	for _, forbidden := range []string{"huggingface.co", "Kokoro-82M", "model.onnx -o", "voices.zip", "HIGH_SIERRA_GO_BIN", "macos-highsierra", "High Sierra"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("builder must not download Kokoro model/voice assets; found %q", forbidden)
		}
	}
}

func TestKokoroPhase5DocumentationRemovesManualCGOInstall(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", "kokoro", "Kokoro.md"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"./build_agentgo_all_platforms.sh",
		"does **not** download a Kokoro model or voices",
		"No Python, Docker, C compiler, CGO setup, manual helper compilation",
		"dist/assets/kokoro/model.onnx",
		"dist/assets/kokoro/voices/",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Kokoro.md missing Phase 5 install guidance %q", want)
		}
	}
	for _, forbidden := range []string{"CGO_ENABLED=1", "-tags kokoro_onnx", "github.com/yalue/onnxruntime_go", "High Sierra"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Kokoro.md still contains obsolete manual runtime instruction %q", forbidden)
		}
	}
}
