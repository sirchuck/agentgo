package kokoroipc

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	want := Message{Type: "ready", Ref: "k-1", Protocol: ProtocolVersion, HelperVersion: "phase2", Voices: []string{"af_heart", "am_adam"}, Capabilities: []string{"ping", "list_voices"}}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.Ref != want.Ref || got.Protocol != ProtocolVersion || len(got.Voices) != 2 {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestReadFrameRejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	var header [5]byte
	header[0] = FrameJSON
	binary.BigEndian.PutUint32(header[1:], uint32(MaxFrameBytes+1))
	buf.Write(header[:])
	_, _, err := ReadFrame(&buf)
	if err == nil || !strings.Contains(err.Error(), "invalid Kokoro IPC frame size") {
		t.Fatalf("error = %v", err)
	}
}

func TestAudioFrameRoundTrip(t *testing.T) {
	want := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	payload, err := EncodeAudioFrame("synth-9", want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAudioFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "synth-9" || !bytes.Equal(got.Data, want) {
		t.Fatalf("audio round trip = ref %q data %v", got.Ref, got.Data)
	}
}

func TestTimingSpanRoundTrip(t *testing.T) {
	want := Message{
		Type:       "chunk_timing",
		Ref:        "timed-1",
		ChunkIndex: 2,
		Timings: []TimingSpan{{
			Kind:        "word",
			CharStart:   4,
			CharEnd:     7,
			StartSample: 120,
			EndSample:   480,
		}},
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "chunk_timing" || got.ChunkIndex != 2 || len(got.Timings) != 1 {
		t.Fatalf("timing round trip = %#v", got)
	}
	span := got.Timings[0]
	if span.Kind != "word" || span.CharStart != 4 || span.CharEnd != 7 || span.StartSample != 120 || span.EndSample != 480 {
		t.Fatalf("timing span = %#v", span)
	}
}
