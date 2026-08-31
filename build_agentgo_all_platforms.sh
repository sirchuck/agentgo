#!/usr/bin/env bash
set -euo pipefail

# AgentGO multi-platform build script for Linux/macOS terminals.
# Run this from the AgentGO source folder.
#
# Output: a flat, ready-to-run dist/ folder containing all platform
# executables plus clean public runtime files. The script intentionally
# generates clean config.json and models.json instead of copying local files.
#
# Kokoro Phase 5:
#   - AgentGO itself remains a normal CGO-free Go 1.20 module.
#   - The Kokoro helper is a separate pure-Go module that dynamically loads
#     the platform ONNX Runtime shared library; no C compiler/CGO is needed.
#   - This build downloads/caches ONNX Runtime 1.23.2 CPU runtime libraries and
#     packages the correct helper/runtime for every supported release target.
#   - Kokoro model.onnx and voices are NEVER downloaded here. If you placed
#     them under assets/kokoro before the build, the normal asset copy includes
#     them; otherwise the release still works with legacy browser/system TTS.

APP_NAME="agentgo"
DIST_DIR="dist"
GO_BIN="${GO_BIN:-go}"
KOKORO_HELPER_GO_BIN="${KOKORO_HELPER_GO_BIN:-$GO_BIN}"
KOKORO_HELPER_DIR="cmd/kokoro-helper"
KOKORO_HELPER_BUILD_DIR=""
ONNXRUNTIME_VERSION="1.23.2"
ONNXRUNTIME_BASE_URL="${AGENTGO_ONNXRUNTIME_BASE_URL:-https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}}"
ONNXRUNTIME_CACHE_DIR="${AGENTGO_KOKORO_RUNTIME_CACHE:-${XDG_CACHE_HOME:-${HOME:-.}/.cache}/agentgo/onnxruntime/${ONNXRUNTIME_VERSION}}"

REQUIRED_RUNTIME_DIRS=("templates" "assets" "system_prompts")
REQUIRED_RUNTIME_FILES=("version.json" "model_names.json" "README.md")
GENERATED_RUNTIME_FILES=("config.json" "models.json")
REQUIRED_RELEASE_BINARIES=(
  "${APP_NAME}-windows-amd64.exe"
  "${APP_NAME}-linux-amd64"
  "${APP_NAME}-linux-arm64"
  "${APP_NAME}-macos-arm64"
  "${APP_NAME}-macos-amd64"
)

# goos|goarch|runtime-directory|ORT archive|ORT library filename|helper filename
KOKORO_RUNTIME_TARGETS=(
  "windows|amd64|windows-amd64|onnxruntime-win-x64-${ONNXRUNTIME_VERSION}.zip|onnxruntime.dll|kokoro-helper.exe"
  "linux|amd64|linux-amd64|onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}.tgz|libonnxruntime.so.${ONNXRUNTIME_VERSION}|kokoro-helper"
  "linux|arm64|linux-arm64|onnxruntime-linux-aarch64-${ONNXRUNTIME_VERSION}.tgz|libonnxruntime.so.${ONNXRUNTIME_VERSION}|kokoro-helper"
  "darwin|arm64|darwin-arm64|onnxruntime-osx-arm64-${ONNXRUNTIME_VERSION}.tgz|libonnxruntime.${ONNXRUNTIME_VERSION}.dylib|kokoro-helper"
  "darwin|amd64|darwin-amd64|onnxruntime-osx-x86_64-${ONNXRUNTIME_VERSION}.tgz|libonnxruntime.${ONNXRUNTIME_VERSION}.dylib|kokoro-helper"
)

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

if [[ ! -f "$KOKORO_HELPER_DIR/go.mod" ]]; then
  echo "ERROR: Kokoro helper module is missing: $KOKORO_HELPER_DIR/go.mod"
  exit 1
fi

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# AgentGO's main application remains CGO-free for straightforward cross-builds.
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

build_target "windows" "amd64" "${APP_NAME}-windows-amd64.exe" "Windows 64-bit"
build_target "linux"   "amd64" "${APP_NAME}-linux-amd64"       "Linux 64-bit Intel / AMD"
build_target "linux"   "arm64" "${APP_NAME}-linux-arm64"       "Linux ARM64"
build_target "darwin"  "arm64" "${APP_NAME}-macos-arm64"       "macOS Apple Silicon"
build_target "darwin"  "amd64" "${APP_NAME}-macos-amd64"       "macOS Intel"

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

version_at_least() {
  local version="$1"
  local need_major="$2"
  local need_minor="$3"
  version="${version#go}"
  local major="${version%%.*}"
  local rest="${version#*.}"
  local minor="${rest%%.*}"
  major="${major%%[^0-9]*}"
  minor="${minor%%[^0-9]*}"
  [[ -n "$major" && -n "$minor" ]] || return 1
  (( major > need_major || (major == need_major && minor >= need_minor) ))
}

prepare_kokoro_helper_module() {
  if ! command -v "$KOKORO_HELPER_GO_BIN" >/dev/null 2>&1; then
    echo "ERROR: Kokoro helper Go command is unavailable: $KOKORO_HELPER_GO_BIN"
    echo "Set KOKORO_HELPER_GO_BIN to a Go 1.25+ toolchain, or use Go 1.21+ so GOTOOLCHAIN=auto can select it."
    exit 1
  fi

  local helper_go_version
  helper_go_version="$("$KOKORO_HELPER_GO_BIN" env GOVERSION 2>/dev/null || true)"
  if version_at_least "$helper_go_version" 1 25; then
    export AGENTGO_HELPER_GOTOOLCHAIN="local"
  elif version_at_least "$helper_go_version" 1 21; then
    export AGENTGO_HELPER_GOTOOLCHAIN="auto"
  else
    echo "ERROR: Kokoro helper requires Go 1.25+."
    echo "Your selected helper command reports ${helper_go_version:-an unknown version}."
    echo "Use Go 1.21+ (automatic toolchain selection) or set KOKORO_HELPER_GO_BIN to a Go 1.25+ executable."
    exit 1
  fi

  echo
  echo "Preparing native Kokoro helper module (pure Go; CGO is not required)..."

  # Work in a throw-away sibling directory so dependency preparation never
  # rewrites the checked-in helper go.mod/go.sum. Keeping the workspace under
  # cmd/ preserves the helper module's replace agentgo => ../.. relationship.
  KOKORO_HELPER_BUILD_DIR="$(mktemp -d "cmd/.kokoro-helper-build.XXXXXX")"
  cp -R "$KOKORO_HELPER_DIR/." "$KOKORO_HELPER_BUILD_DIR/"
  trap 'if [[ -n "${KOKORO_HELPER_BUILD_DIR:-}" ]]; then rm -rf "$KOKORO_HELPER_BUILD_DIR"; fi' EXIT
  (
    cd "$KOKORO_HELPER_BUILD_DIR"
    # Resolve the exact pinned helper dependency set and populate go.sum.
    # Do not run `go mod verify` here: this helper module intentionally uses
    # `replace agentgo => ../..`, and Go cannot verify a local replacement as
    # a downloaded module-cache ZIP. The readonly cross-builds below are the
    # release validation for the complete dependency graph.
    GOTOOLCHAIN="$AGENTGO_HELPER_GOTOOLCHAIN" "$KOKORO_HELPER_GO_BIN" mod tidy
  )
}

download_file() {
  local url="$1"
  local output="$2"
  if [[ -s "$output" ]]; then
    return 0
  fi
  mkdir -p "$(dirname "$output")"
  echo "Downloading $(basename "$output")..."
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --retry-delay 2 -o "$output.tmp" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$output.tmp" "$url"
  else
    echo "ERROR: curl or wget is required to obtain ONNX Runtime build assets."
    exit 1
  fi
  mv "$output.tmp" "$output"
}

extract_runtime_library() {
  local archive="$1"
  local expected_name="$2"
  local destination="$3"
  local temp_dir
  temp_dir="$(mktemp -d)"
  if [[ "$archive" == *.zip ]]; then
    if ! command -v unzip >/dev/null 2>&1; then
      echo "ERROR: unzip is required to extract $(basename "$archive")."
      rm -rf "$temp_dir"
      exit 1
    fi
    unzip -q "$archive" -d "$temp_dir"
  else
    tar -xzf "$archive" -C "$temp_dir"
  fi

  local found
  found="$(find "$temp_dir" -type f -name "$expected_name" -print -quit)"
  if [[ -z "$found" ]]; then
    echo "ERROR: $expected_name was not found inside $(basename "$archive")."
    rm -rf "$temp_dir"
    exit 1
  fi
  cp "$found" "$destination"
  rm -rf "$temp_dir"
}

build_kokoro_helper_target() {
  local goos="$1"
  local goarch="$2"
  local platform="$3"
  local helper_name="$4"
  local runtime_dir="$DIST_DIR/assets/kokoro/runtime/$platform"

  mkdir -p "$runtime_dir"
  echo "Building Kokoro helper for $platform..."
  (
    cd "$KOKORO_HELPER_BUILD_DIR"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOTOOLCHAIN="$AGENTGO_HELPER_GOTOOLCHAIN" \
      "$KOKORO_HELPER_GO_BIN" build -mod=readonly -trimpath -o "../../$runtime_dir/$helper_name" .
  )
}

package_kokoro_runtime_target() {
  local platform="$1"
  local archive_name="$2"
  local library_name="$3"
  local runtime_dir="$DIST_DIR/assets/kokoro/runtime/$platform"
  local archive_path="$ONNXRUNTIME_CACHE_DIR/$archive_name"

  download_file "$ONNXRUNTIME_BASE_URL/$archive_name" "$archive_path"
  echo "Packaging ONNX Runtime $ONNXRUNTIME_VERSION for $platform..."
  extract_runtime_library "$archive_path" "$library_name" "$runtime_dir/$library_name"
}

prepare_kokoro_helper_module

echo
echo "========================================"
echo "Validating Kokoro helper cross-builds"
echo "========================================"
echo

# Compile every supported helper target before downloading/packaging runtimes.
# If an upstream dependency regresses one platform (as v0.7.0 did on Windows),
# the release build fails immediately instead of producing a partial dist tree.
for target in "${KOKORO_RUNTIME_TARGETS[@]}"; do
  IFS='|' read -r goos goarch platform _archive_name _library_name helper_name <<< "$target"
  build_kokoro_helper_target "$goos" "$goarch" "$platform" "$helper_name"
done

echo
echo "========================================"
echo "Packaging Kokoro native runtimes"
echo "========================================"
echo

for target in "${KOKORO_RUNTIME_TARGETS[@]}"; do
  IFS='|' read -r _goos _goarch platform archive_name library_name _helper_name <<< "$target"
  package_kokoro_runtime_target "$platform" "$archive_name" "$library_name"
done

rm -rf "$KOKORO_HELPER_BUILD_DIR"
KOKORO_HELPER_BUILD_DIR=""
trap - EXIT

echo
echo "Auditing release contents..."
for dir in "${REQUIRED_RUNTIME_DIRS[@]}"; do
  if [[ ! -d "$DIST_DIR/$dir" ]]; then
    echo "ERROR: Release audit failed; runtime folder missing from dist: $dir"
    exit 1
  fi
done
for file in "${REQUIRED_RUNTIME_FILES[@]}" "${GENERATED_RUNTIME_FILES[@]}"; do
  if [[ ! -s "$DIST_DIR/$file" ]]; then
    echo "ERROR: Release audit failed; runtime file missing or empty in dist: $file"
    exit 1
  fi
done
for binary in "${REQUIRED_RELEASE_BINARIES[@]}"; do
  if [[ ! -s "$DIST_DIR/$binary" ]]; then
    echo "ERROR: Release audit failed; required binary missing or empty in dist: $binary"
    exit 1
  fi
done
for target in "${KOKORO_RUNTIME_TARGETS[@]}"; do
  IFS='|' read -r _goos _goarch platform _archive_name library_name helper_name <<< "$target"
  if [[ ! -s "$DIST_DIR/assets/kokoro/runtime/$platform/$helper_name" ]]; then
    echo "ERROR: Release audit failed; Kokoro helper missing for $platform."
    exit 1
  fi
  if [[ ! -s "$DIST_DIR/assets/kokoro/runtime/$platform/$library_name" ]]; then
    echo "ERROR: Release audit failed; ONNX Runtime library missing for $platform."
    exit 1
  fi
done
echo "Release audit passed."

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
echo "  README.md          in-app README / pumpkin notes content"
echo "  templates/"
echo "  assets/"
echo "  system_prompts/"
echo "  assets/kokoro/runtime/<platform>/kokoro-helper[.exe]"
echo "  assets/kokoro/runtime/<platform>/ONNX Runtime $ONNXRUNTIME_VERSION"
echo
if [[ -s "$DIST_DIR/assets/kokoro/model.onnx" ]] && find "$DIST_DIR/assets/kokoro/voices" -type f -name '*.bin' -print -quit 2>/dev/null | grep -q .; then
  echo "Kokoro model + voice assets were present in the source assets and are included in this release."
else
  echo "Kokoro model/voice assets were not supplied; they were not downloaded."
  echo "AgentGO remains usable with browser/system speech. See assets/kokoro/Kokoro.md."
fi
echo
echo "Private/runtime data intentionally not included:"
echo "  work/, projects, outfits, mastermind data, video_jobs, mesh_jobs, logs, local models/config secrets"
echo
