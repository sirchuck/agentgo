package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const kokoroPinnedOnnxerVersion = "v0.7.1-0.20260807134843-4a9e532648b2"

func TestKokoroPhase5StabilizationRevisionAndDependencyLock(t *testing.T) {
	version, err := os.ReadFile("version.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(version), `"revision": "r448"`) {
		t.Fatalf("Phase 5 overlay-upgrade stabilization must preserve the stabilized Phase 5 dependency lock through r448:\n%s", version)
	}

	helperMod, err := os.ReadFile(filepath.Join("cmd", "kokoro-helper", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	modText := string(helperMod)
	if !strings.Contains(modText, "github.com/benedoc-inc/onnxer "+kokoroPinnedOnnxerVersion) {
		t.Fatalf("Kokoro helper is not pinned to the Windows-fixed onnxer revision:\n%s", modText)
	}
	if !strings.Contains(modText, "github.com/ebitengine/purego v0.9.0 // indirect") {
		t.Fatalf("Kokoro helper does not lock the transitive PureGo dependency:\n%s", modText)
	}
	if strings.Contains(modText, "github.com/benedoc-inc/onnxer v0.7.0") {
		t.Fatal("Kokoro helper still pins the Windows-broken onnxer v0.7.0 release")
	}

	helperSum, err := os.ReadFile(filepath.Join("cmd", "kokoro-helper", "go.sum"))
	if err != nil {
		t.Fatalf("Kokoro helper must ship go.sum: %v", err)
	}
	for _, want := range []string{
		"github.com/benedoc-inc/onnxer " + kokoroPinnedOnnxerVersion + "/go.mod h1:wALlP3CtDgGF1KMaqxuVCw8XOK8G2T95xg3ZW4f87Vw=",
		"github.com/ebitengine/purego v0.9.0 h1:",
		"github.com/ebitengine/purego v0.9.0/go.mod h1:",
	} {
		if !strings.Contains(string(helperSum), want) {
			t.Fatalf("Kokoro helper go.sum missing %q", want)
		}
	}
}

func TestKokoroPhase5StabilizationBuilderIsCleanAndCrossPlatform(t *testing.T) {
	data, err := os.ReadFile("build_agentgo_all_platforms.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`mktemp -d "cmd/.kokoro-helper-build.XXXXXX"`,
		`mod tidy`,
		`build -mod=readonly -trimpath`,
		`Validating Kokoro helper cross-builds`,
		`windows|amd64|windows-amd64`,
		`linux|amd64|linux-amd64`,
		`linux|arm64|linux-arm64`,
		`darwin|arm64|darwin-arm64`,
		`darwin|amd64|darwin-amd64`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("stabilized release builder missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`"$KOKORO_HELPER_GO_BIN" mod verify`,
		"HIGH_SIERRA_GO_BIN",
		"build_high_sierra_target",
		"macos-highsierra",
		"High Sierra",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("forbidden release-builder handling remains: %q", forbidden)
		}
	}
}

func TestKokoroPhase5StabilizationDocsTrackPinnedDependency(t *testing.T) {
	for _, name := range []string{
		filepath.Join("assets", "kokoro", "Kokoro.md"),
		filepath.Join("assets", "kokoro", "licenses", "THIRD_PARTY_NOTICES.md"),
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), kokoroPinnedOnnxerVersion) {
			t.Fatalf("%s does not document the pinned onnxer revision", name)
		}
		if strings.Contains(string(data), "onnxer v0.7.0") {
			t.Fatalf("%s still documents the broken onnxer v0.7.0 pin", name)
		}
	}
}
