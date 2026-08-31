package main

// This file intentionally remains as an inert compatibility placeholder.
//
// AgentGO revisions before the Phase 5 pure-Go Kokoro helper used this path
// for a fallback newKokoroEngine implementation. Overlay-style patch installs
// cannot delete stale files, so keeping this file (without any declarations)
// ensures that copying r440 over an older tree replaces the legacy constructor
// instead of allowing it to collide with engine_onnx.go.
