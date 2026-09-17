package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeR455MoonshineAsset(t *testing.T, appRoot, relative, content string) {
	t.Helper()
	path := filepath.Join(appRoot, filepath.FromSlash(kokoroAssetsRelativeDir), filepath.FromSlash(kokoroMoonshineG2PDir), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKokoroG2PR455StatusFallsBackWhenMoonshineMissing(t *testing.T) {
	root := t.TempDir()
	status := inspectKokoroAssets(root)
	if status.G2PProvider != "standard" || status.G2PAvailable || status.G2PAssetsPresent || status.G2PState != "missing" {
		t.Fatalf("missing Moonshine status=%#v", status)
	}
	if !strings.Contains(status.G2PMessage, "standard pronunciation fallback") {
		t.Fatalf("missing Moonshine message=%q", status.G2PMessage)
	}
}

func TestKokoroG2PR455DetectsCompleteOptionalAssetSet(t *testing.T) {
	root := t.TempDir()
	for relative, content := range map[string]string{
		"dict_filtered_heteronyms.tsv": "glyphs\tɡlɪfs\n",
		"g2p-config.json":              "{}\n",
		"oov/model.onnx":               "placeholder-model",
		"oov/onnx-config.json":         "{}\n",
	} {
		writeR455MoonshineAsset(t, root, relative, content)
	}
	status := inspectKokoroAssets(root)
	if !status.G2PAssetsPresent || status.G2PState != "installed" {
		t.Fatalf("complete Moonshine status=%#v", status)
	}
	if status.G2PAvailable {
		t.Fatal("filesystem discovery must not claim Moonshine is active before helper handshake")
	}
}

func TestKokoroG2PR455ReportsIncompleteOptionalAssetsWithoutBreakingKokoroStatus(t *testing.T) {
	root := t.TempDir()
	writeR455MoonshineAsset(t, root, "dict_filtered_heteronyms.tsv", "glyphs\tɡlɪfs\n")
	status := inspectKokoroAssets(root)
	if status.G2PState != "incomplete" || status.G2PAssetsPresent || status.G2PAvailable {
		t.Fatalf("incomplete Moonshine status=%#v", status)
	}
	if !strings.Contains(status.G2PMessage, "assets are incomplete") {
		t.Fatalf("incomplete Moonshine message=%q", status.G2PMessage)
	}
}
