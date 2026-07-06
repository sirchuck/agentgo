#!/usr/bin/env bash
set -euo pipefail

# AgentGO multi-platform build script for Linux/macOS terminals.
# Run this from the AgentGO source folder.

APP_NAME="agentgo"
DIST_DIR="dist"

echo
echo "========================================"
echo "Building AgentGO binaries"
echo "========================================"
echo

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go is not installed or is not on PATH."
  echo "Install Go, then run this script again."
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

build_target() {
  local goos="$1"
  local goarch="$2"
  local output="$3"
  local label="$4"

  echo "Building $label..."
  GOOS="$goos" GOARCH="$goarch" go build -o "$DIST_DIR/$output"
}

build_target "windows" "amd64" "${APP_NAME}-windows-amd64.exe" "Windows 64-bit"
build_target "linux"   "amd64" "${APP_NAME}-linux-amd64"       "Linux 64-bit Intel / AMD"
build_target "linux"   "arm64" "${APP_NAME}-linux-arm64"       "Linux ARM64"
build_target "darwin"  "arm64" "${APP_NAME}-macos-arm64"       "macOS Apple Silicon"
build_target "darwin"  "amd64" "${APP_NAME}-macos-amd64"       "macOS Intel"

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
