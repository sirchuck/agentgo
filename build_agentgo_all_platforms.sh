#!/usr/bin/env bash
set -euo pipefail

# AgentGO multi-platform build script for Linux/macOS terminals.
# Run this from the AgentGO source folder.

APP_NAME="agentgo"
DIST_DIR="dist"
GO_BIN="${GO_BIN:-go}"
HIGH_SIERRA_GO_BIN="${HIGH_SIERRA_GO_BIN:-go1.20}"

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
echo "========================================"
echo "Build complete."
echo "Binaries are in the \"$DIST_DIR\" folder."
echo "========================================"
echo

ls -lh "$DIST_DIR"/${APP_NAME}-*

echo
echo "Package each binary with:"
echo "  config.json"
echo "  models.json"
echo "  version.json"
echo "  templates/"
echo "  assets/"
echo "  system_prompts/"
echo
