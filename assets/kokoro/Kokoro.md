# Kokoro Local Voice Assets

Kokoro is AgentGO's supported local neural voice companion. AgentGO itself does not download Kokoro models or voices.

AgentGO's all-platform build supplies the native `kokoro-helper` and the matching ONNX Runtime library for each supported platform. A developer installing Kokoro from a GitHub/source build only needs to add a compatible `model.onnx` and the Kokoro voice `.bin` files, then restart AgentGO. If Kokoro is missing or cannot initialize, AgentGO remains usable and falls back to its legacy browser/system speech path.

## Expected AgentGO layout

The stable layout is:

- `assets/kokoro/model.onnx` - the Kokoro ONNX model AgentGO should attempt to load.
- `assets/kokoro/voices/*.bin` - Kokoro voice embeddings. AgentGO discovers installed voice names at runtime.
- `assets/kokoro/runtime/<os>-<arch>/kokoro-helper[.exe]` - AgentGO's platform-specific private helper, supplied by the AgentGO build.
- `assets/kokoro/runtime/<os>-<arch>/onnxruntime.dll` on Windows, or the matching ONNX Runtime 1.23.2 shared library on Linux/macOS, supplied by the AgentGO build.
- `assets/kokoro/licenses/` - third-party notices/licenses distributed with the Kokoro runtime path.
- `assets/kokoro/g2p/moonshine/en_us/` - optional Moonshine English G2P assets for better pronunciation of words missing from AgentGO's bundled CMU dictionary.

AgentGO creates the Kokoro directory structure and this document if they are missing. After adding or replacing Kokoro model/voice files, restart AgentGO.

## Installing Kokoro in a GitHub/source build

1. Run AgentGO's normal all-platform build from the repository root:

   ```bash
   ./build_agentgo_all_platforms.sh
   ```

   The build creates the platform helpers and packages ONNX Runtime automatically. It may download the official ONNX Runtime 1.23.2 CPU archives into a reusable build cache. It does **not** download a Kokoro model or voices.

2. Copy your Kokoro model to:

   `dist/assets/kokoro/model.onnx`

3. Copy the Kokoro `.bin` voice files into:

   `dist/assets/kokoro/voices/`

4. Start/restart the AgentGO binary from `dist/`.

You can instead place `model.onnx` and `voices/*.bin` under the source `assets/kokoro/` directory **before** running the builder; the normal AgentGO asset copy will carry them into `dist/`.

No Python, Docker, C compiler, CGO setup, manual helper compilation, or manually installed ONNX Runtime is required for the normal AgentGO build path.


## Optional Moonshine English G2P

AgentGO can use Moonshine's small English grapheme-to-phoneme model as an optional pronunciation fallback. Moonshine is **not required** for Kokoro: if these files are absent, incomplete, or incompatible, AgentGO keeps Kokoro available and uses its standard pronunciation fallback. Nothing is downloaded automatically.

Expected layout:

```text
assets/kokoro/g2p/moonshine/en_us/
  dict_filtered_heteronyms.tsv
  g2p-config.json
  oov/
    model.onnx              # model.ort is also accepted
    onnx-config.json        # config_onnx.json is also accepted
```

The Moonshine OOV model reuses the same ONNX Runtime already loaded by the Kokoro helper, so users do not install Python, Apache, another runtime, or a separate service. A packaged AgentGO release may include these files; a GitHub/source user can add them manually to the path above and restart AgentGO.

Pronunciation priority is:

1. AgentGO **Custom Phonetics** (`system_prompts/phonetics.md`)
2. AgentGO's bundled CMU dictionary
3. Moonshine English G2P, when the complete asset set above loads successfully
4. AgentGO's existing deterministic letter-name fallback

AgentGO keeps obvious all-uppercase developer acronyms such as `HTML`, `CPU`, and `API` on the letter-name path rather than sending them through automatic G2P. Voice status reports whether Moonshine G2P is active or the standard fallback is in use.

If Moonshine data/model files are redistributed with an AgentGO package, preserve and review the license/provenance terms published with those upstream assets.

## AgentGO full-quality model target

The full-quality model selected for the official AgentGO package is the timestamp-enabled Kokoro 82M v1.0 FP32 ONNX model (including the `pred_dur` duration output used when available):

- Repository: `onnx-community/Kokoro-82M-v1.0-ONNX-timestamped`
- Upstream file: `onnx/model.onnx`
- Install filename: `assets/kokoro/model.onnx`
- Precision: FP32
- Published size: approximately 326 MB
- SHA-256: `651ea8291843a92276a4a003581a215cb07d15e47dde6fcfb1b768f9a1682054`
- Source: https://huggingface.co/onnx-community/Kokoro-82M-v1.0-ONNX-timestamped/blob/main/onnx/model.onnx

The SHA-256 is informational so a developer can verify the exact AgentGO-tested FP32 file. AgentGO does **not** enforce that checksum at runtime. Compatible Kokoro precision/model variants may be substituted as `assets/kokoro/model.onnx`.

## Build/runtime details

The Kokoro helper is a separate pure-Go module under `cmd/kokoro-helper/`. It dynamically loads ONNX Runtime and therefore does not require CGO. The main AgentGO application remains on its existing Go 1.20 module baseline.

The helper dependency currently requires a Go 1.25 toolchain. `build_agentgo_all_platforms.sh` uses an installed Go 1.25+ toolchain directly, or when the selected Go command is 1.21+, uses Go's automatic toolchain selection. You can override the helper compiler with `KOKORO_HELPER_GO_BIN=/path/to/go`.

The pure-Go ONNX binding is pinned to `github.com/benedoc-inc/onnxer` commit `4a9e532648b28fa800dbc991bf8e5e95da80333c` (`v0.7.1-0.20260807134843-4a9e532648b2`). That post-v0.7.0 revision includes the upstream Windows loader/path fixes needed for cross-platform helper builds. The builder prepares dependencies in a temporary helper workspace, verifies the module set, and compiles every supported helper target before packaging any ONNX Runtime archive.

The build packages these Kokoro runtime targets:

- Windows AMD64
- Linux AMD64
- Linux ARM64
- macOS Apple Silicon (ARM64)
- macOS Intel (AMD64)


The compiled helper uses private framed stdin/stdout IPC and opens no network port. The ONNX model stays loaded in the helper for the AgentGO session.

## Voice discovery and fallback

Voice availability is discovered from installed `.bin` voice files through the local helper at runtime. If a previously saved voice is no longer present, AgentGO selects the first voice reported by the helper and persists that replacement.

AgentGO only marks Kokoro as available after the helper completes its private IPC handshake, reports at least one voice, loads the model through ONNX Runtime, and reports synthesis capability. If any check fails, AgentGO keeps the browser/system speech fallback active.

During speech, AgentGO targets about 180 runes for the first browser synthesis request, then finishes at a safe whitespace boundary so a word is never intentionally split. Later requests also reach at least 180 runes, then prefer the next sentence-ending period through about 300 runes; if no suitable period appears by then, AgentGO finishes at the next safe whitespace boundary. Kokoro receives the punctuation/newlines inside each request and determines its own speech cadence. AgentGO receives 24 kHz mono float32 PCM frames and streams them to the browser for Web Audio playback. When the loaded model exposes duration timing, AgentGO maps predicted timing back to original source spans. If exact timing is unavailable or cannot be mapped, AgentGO falls back to chunk/sentence highlighting rather than show an incorrect word.

See `assets/kokoro/licenses/THIRD_PARTY_NOTICES.md` for third-party components and license files used by the Kokoro speech path.

## Local release acceptance check

Before calling a packaged Kokoro runtime fully validated, test it on a machine that has the actual model/voice assets:

1. Confirm the Kokoro voice list appears in **AGENTGO VOICE** and the selected voice survives an AgentGO restart.
2. Read a short sentence and a long multi-sentence response; playback should begin before the long response is fully synthesized.
3. Read `AgentGO charged $42, e.g. for the test.` and confirm source highlighting stays synchronized, or safely falls back to chunk highlighting.
4. Change Speech Speed and confirm speech/highlighting remain synchronized.
5. Stop speech mid-response and confirm queued audio/highlights stop immediately.
6. With Moonshine G2P installed, read a sentence containing uncommon ordinary words such as `glyphs` and `descenders`; confirm they are pronounced as words rather than spelled letter by letter.
7. Temporarily remove/rename the Moonshine G2P asset directory and confirm Kokoro still works, with voice status reporting the AgentGO standard pronunciation fallback.
8. Temporarily remove/rename `model.onnx`, a selected voice, or the helper and confirm AgentGO remains usable through legacy browser/system TTS with a clear Kokoro-unavailable message.
