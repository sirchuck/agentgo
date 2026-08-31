package main

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	kokoroAssetsRelativeDir   = "assets/kokoro"
	kokoroModelFilename       = "model.onnx"
	kokoroVoicesDirname       = "voices"
	kokoroRuntimeDirname      = "runtime"
	kokoroLicensesDirname     = "licenses"
	kokoroReadmeFilename      = "Kokoro.md"
	kokoroKnownModelSHA256    = "651ea8291843a92276a4a003581a215cb07d15e47dde6fcfb1b768f9a1682054"
	kokoroKnownModelRepo      = "onnx-community/Kokoro-82M-v1.0-ONNX-timestamped"
	kokoroKnownModelSourceURL = "https://huggingface.co/onnx-community/Kokoro-82M-v1.0-ONNX-timestamped/blob/main/onnx/model.onnx"
)

//go:embed assets/kokoro/Kokoro.md
var kokoroReadmeContents string

type kokoroAssetStatus struct {
	Available       bool     `json:"available"`
	AssetsPresent   bool     `json:"assets_present"`
	ModelPresent    bool     `json:"model_present"`
	VoicesPresent   bool     `json:"voices_present"`
	HelperPresent   bool     `json:"helper_present"`
	HelperRunning   bool     `json:"helper_running"`
	HandshakeReady  bool     `json:"handshake_ready"`
	ModelLoaded     bool     `json:"model_loaded"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	HelperVersion   string   `json:"helper_version,omitempty"`
	State           string   `json:"state"`
	Message         string   `json:"message"`
	AssetsDir       string   `json:"assets_dir"`
	ModelPath       string   `json:"model_path"`
	VoicesDir       string   `json:"voices_dir"`
	RuntimeDir      string   `json:"runtime_dir"`
	ReadmePath      string   `json:"readme_path"`
	HelperPath      string   `json:"helper_path,omitempty"`
	Voices          []string `json:"voices"`
	KnownModelName  string   `json:"known_model_name"`
}

func kokoroAssetsDir(appRoot string) string {
	root := strings.TrimSpace(appRoot)
	if root == "" {
		root = "."
	}
	return filepath.Join(root, filepath.FromSlash(kokoroAssetsRelativeDir))
}

func kokoroRuntimePlatformDirname() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func kokoroHelperFilename() string {
	if runtime.GOOS == "windows" {
		return "kokoro-helper.exe"
	}
	return "kokoro-helper"
}

func kokoroDefaultPaths(appRoot string) (assetsDir, modelPath, voicesDir, runtimeDir, readmePath, helperPath string) {
	assetsDir = kokoroAssetsDir(appRoot)
	modelPath = filepath.Join(assetsDir, kokoroModelFilename)
	voicesDir = filepath.Join(assetsDir, kokoroVoicesDirname)
	runtimeDir = filepath.Join(assetsDir, kokoroRuntimeDirname)
	readmePath = filepath.Join(assetsDir, kokoroReadmeFilename)
	helperPath = filepath.Join(runtimeDir, kokoroRuntimePlatformDirname(), kokoroHelperFilename())
	return
}

func kokoroHelperCandidates(appRoot string) []string {
	_, _, _, runtimeDir, _, preferred := kokoroDefaultPaths(appRoot)
	generic := filepath.Join(runtimeDir, kokoroHelperFilename())
	if filepath.Clean(preferred) == filepath.Clean(generic) {
		return []string{preferred}
	}
	return []string{preferred, generic}
}

func findKokoroHelper(appRoot string) (string, bool) {
	candidates := kokoroHelperCandidates(appRoot)
	for _, candidate := range candidates {
		if regularFileExists(candidate) {
			return candidate, true
		}
	}
	if len(candidates) > 0 {
		return candidates[0], false
	}
	return "", false
}

func ensureKokoroAssetLayout(appRoot string) error {
	assetsDir, _, voicesDir, runtimeDir, readmePath, _ := kokoroDefaultPaths(appRoot)
	for _, dir := range []string{
		assetsDir,
		voicesDir,
		runtimeDir,
		filepath.Join(runtimeDir, kokoroRuntimePlatformDirname()),
		filepath.Join(assetsDir, kokoroLicensesDirname),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if _, err := os.Stat(readmePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(readmePath, []byte(kokoroReadmeContents), 0o644)
}

func regularFileExists(filename string) bool {
	info, err := os.Stat(filename)
	return err == nil && info.Mode().IsRegular()
}

func kokoroVoiceFiles(voicesDir string) []string {
	entries, err := os.ReadDir(voicesDir)
	if err != nil {
		return nil
	}
	voices := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".bin" && ext != ".npy" && ext != ".npz" {
			continue
		}
		voices = append(voices, strings.TrimSuffix(name, filepath.Ext(name)))
	}
	sort.Strings(voices)
	return voices
}

func inspectKokoroAssets(appRoot string) kokoroAssetStatus {
	_, modelPath, voicesDir, _, _, _ := kokoroDefaultPaths(appRoot)
	localVoices := kokoroVoiceFiles(voicesDir)
	helperPath, helperPresent := findKokoroHelper(appRoot)
	status := kokoroAssetStatus{
		Available:      false,
		ModelPresent:   regularFileExists(modelPath),
		VoicesPresent:  len(localVoices) > 0,
		HelperPresent:  helperPresent,
		AssetsDir:      filepath.ToSlash(filepath.Join(kokoroAssetsRelativeDir)),
		ModelPath:      filepath.ToSlash(filepath.Join(kokoroAssetsRelativeDir, kokoroModelFilename)),
		VoicesDir:      filepath.ToSlash(filepath.Join(kokoroAssetsRelativeDir, kokoroVoicesDirname)),
		RuntimeDir:     filepath.ToSlash(filepath.Join(kokoroAssetsRelativeDir, kokoroRuntimeDirname)),
		ReadmePath:     filepath.ToSlash(filepath.Join(kokoroAssetsRelativeDir, kokoroReadmeFilename)),
		HelperPath:     filepath.ToSlash(helperPath),
		Voices:         []string{}, // authoritative names arrive from the helper handshake (A-032).
		KnownModelName: "Kokoro 82M v1.0 timestamped FP32",
	}
	status.AssetsPresent = status.ModelPresent && status.VoicesPresent
	switch {
	case !status.ModelPresent && !status.VoicesPresent:
		status.State = "missing"
		status.Message = "Local Kokoro voices are not installed. Using system speech."
	case !status.ModelPresent:
		status.State = "incomplete"
		status.Message = "Kokoro voice files were found, but model.onnx is missing. Using system speech."
	case !status.VoicesPresent:
		status.State = "incomplete"
		status.Message = "Kokoro model.onnx was found, but no voice files were found. Using system speech."
	case !status.HelperPresent:
		status.State = "helper-missing"
		status.Message = "Kokoro assets were found, but the AgentGO Kokoro helper is unavailable. Using system speech."
	default:
		status.State = "initializing"
		status.Message = "Kokoro assets and helper were found. Waiting for the local helper initialization handshake."
	}
	return status
}
