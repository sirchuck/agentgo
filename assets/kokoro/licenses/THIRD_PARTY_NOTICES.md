# Kokoro Speech Third-Party Notices

AgentGO's local Kokoro speech path is designed to redistribute or use the following third-party components when they are included in an AgentGO package. Include the corresponding license files with any distributed runtime/model assets.

- **Kokoro 82M v1.0 model and voice assets** — Apache License 2.0. See `Kokoro-Apache-2.0-LICENSE.txt`.
- **ONNX Runtime 1.23.2** — MIT License, Microsoft Corporation. See `ONNXRuntime-LICENSE.txt`.
- **onnxer v0.7.1-0.20260807134843-4a9e532648b2** — MIT License, copyright Shota Sugiura. AgentGO uses its pure-Go ONNX Runtime binding in the Kokoro helper. See `onnxer-LICENSE.txt`.
- **Ebitengine PureGo v0.9.0** — Apache License 2.0. `onnxer` uses PureGo for dynamic native-library calls without CGO. See `purego-Apache-2.0-LICENSE.txt`.
- **Go runtime-derived code included by PureGo** — BSD 3-Clause license. See `Go-BSD-3-LICENSE.txt`.
- **CMU Pronouncing Dictionary data** — used by AgentGO's English text-to-phoneme preparation. See `CMUdict-LICENSE.txt`.

Only the components actually included in a distributed AgentGO package need to be redistributed with that package. If the packaged Kokoro/runtime dependencies change, review and update these notices before release.
