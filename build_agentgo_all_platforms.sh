#!/usr/bin/env bash
set -euo pipefail

# AgentGO multi-platform build script for Linux/macOS terminals.
# Run this from the AgentGO source folder.
#
# Output: a flat, ready-to-run dist/ folder containing all platform
# executables plus clean public runtime files. The script intentionally
# generates clean config.json and models.json instead of copying local files.

APP_NAME="agentgo"
DIST_DIR="dist"
GO_BIN="${GO_BIN:-go}"
HIGH_SIERRA_GO_BIN="${HIGH_SIERRA_GO_BIN:-go1.20}"

REQUIRED_RUNTIME_DIRS=("templates" "assets" "system_prompts")
REQUIRED_RUNTIME_FILES=("version.json" "model_names.json")

echo
echo "========================================"
echo "Building AgentGO binaries"
echo "========================================"
echo

if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "ERROR: Go is not installed or is not on PATH."
  echo "Install Go, or set GO_BIN=/path/to/go, then run this script again."
  exit 1
fi

if ! ls *.go >/dev/null 2>&1; then
  echo "ERROR: No Go source files were found."
  echo "Run this script from the AgentGO source folder."
  exit 1
fi

for dir in "${REQUIRED_RUNTIME_DIRS[@]}"; do
  if [[ ! -d "$dir" ]]; then
    echo "ERROR: Required runtime folder missing: $dir"
    exit 1
  fi
done

for file in "${REQUIRED_RUNTIME_FILES[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "ERROR: Required runtime file missing: $file"
    exit 1
  fi
done

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# Use CGO_ENABLED=0 for easier cross-compilation.
# If AgentGO ever adds CGO dependencies, remove or change this.
export CGO_ENABLED=0

build_target_with_go() {
  local go_bin="$1"
  local goos="$2"
  local goarch="$3"
  local output="$4"
  local label="$5"

  echo "Building $label..."
  GOOS="$goos" GOARCH="$goarch" "$go_bin" build -o "$DIST_DIR/$output"
}

build_target() {
  build_target_with_go "$GO_BIN" "$@"
}

build_high_sierra_target() {
  local high_sierra_go="$HIGH_SIERRA_GO_BIN"
  if ! command -v "$high_sierra_go" >/dev/null 2>&1; then
    if "$GO_BIN" version | grep -Eq 'go1\.20(\.| |$)'; then
      high_sierra_go="$GO_BIN"
    else
      echo "Skipping macOS High Sierra Intel: Go 1.20 is required."
      echo "  Install a Go 1.20 toolchain named go1.20, or set HIGH_SIERRA_GO_BIN=/path/to/go1.20."
      return 0
    fi
  fi
  build_target_with_go "$high_sierra_go" "darwin" "amd64" "${APP_NAME}-macos-highsierra-amd64" "macOS High Sierra Intel (Go 1.20)"
}

build_target "windows" "amd64" "${APP_NAME}-windows-amd64.exe" "Windows 64-bit"
build_target "linux"   "amd64" "${APP_NAME}-linux-amd64"       "Linux 64-bit Intel / AMD"
build_target "linux"   "arm64" "${APP_NAME}-linux-arm64"       "Linux ARM64"
build_target "darwin"  "arm64" "${APP_NAME}-macos-arm64"       "macOS Apple Silicon"
build_target "darwin"  "amd64" "${APP_NAME}-macos-amd64"       "macOS Intel"
build_high_sierra_target

echo
echo "Copying public runtime assets..."
for dir in "${REQUIRED_RUNTIME_DIRS[@]}"; do
  cp -R "$dir" "$DIST_DIR/"
done
for file in "${REQUIRED_RUNTIME_FILES[@]}"; do
  cp "$file" "$DIST_DIR/$file"
done

echo "Writing clean public config.json..."
cat > "$DIST_DIR/config.json" <<'JSON'
{
  "agentgo_file": "config",
  "file_version": 1,
  "bind_host": "127.0.0.1",
  "http_port": 5226,
  "https_port": 0,
  "tls_cert_file": "",
  "tls_key_file": "",
  "work_root": "work",
  "max_response_history": 50,
  "risk_mode_max_iterations": 10,
  "outfit_run_retention": 50,
  "auto_merge_single_builder_waves": true,
  "prompt_version": 1,
  "wiretap": {
    "max_wiretap_entries": 200,
    "default_runtime_slice_entries": 75,
    "max_runtime_slice_entries": 150
  }
}
JSON

echo "Writing clean public models.json..."
cat > "$DIST_DIR/models.json" <<'JSON'
{
  "agentgo_file": "models",
  "file_version": 1,
  "schema_version": 1,
  "top_id": 0,
  "models": []
}
JSON

echo
echo "========================================"
echo "Build complete."
echo "Ready-to-run release files are in the \"$DIST_DIR\" folder."
echo "Zip the whole \"$DIST_DIR\" folder when you are ready to publish."
echo "========================================"
echo

ls -lh "$DIST_DIR"/${APP_NAME}-* 2>/dev/null || true

echo
echo "Included runtime files:"
echo "  config.json        clean public default"
echo "  models.json        clean empty model list"
echo "  version.json"
echo "  model_names.json"
echo "  templates/"
echo "  assets/"
echo "  system_prompts/"
echo
echo "Private/runtime data intentionally not included:"
echo "  work/, projects, outfits, mastermind data, video_jobs, mesh_jobs, logs, local models/config secrets"
echo
