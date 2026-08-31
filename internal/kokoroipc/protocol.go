package kokoroipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 64 << 20

	FrameJSON  byte = 1
	FrameAudio byte = 2
)

type TimingSpan struct {
	Kind        string `json:"kind,omitempty"`
	CharStart   int    `json:"char_start,omitempty"`
	CharEnd     int    `json:"char_end,omitempty"`
	StartSample int    `json:"start_sample,omitempty"`
	EndSample   int    `json:"end_sample,omitempty"`
}

type Message struct {
	Type           string   `json:"type"`
	ID             string   `json:"id,omitempty"`
	Ref            string   `json:"ref,omitempty"`
	Protocol       int      `json:"protocol,omitempty"`
	HelperVersion  string   `json:"helper_version,omitempty"`
	RuntimeVersion string   `json:"runtime_version,omitempty"`
	InstanceID     string   `json:"instance_id,omitempty"`
	ModelName      string   `json:"model_name,omitempty"`
	ModelLoaded    bool     `json:"model_loaded,omitempty"`
	Voices         []string `json:"voices,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	Error          string   `json:"error,omitempty"`
	Message        string   `json:"message,omitempty"`

	Text         string       `json:"text,omitempty"`
	Voice        string       `json:"voice,omitempty"`
	Speed        float32      `json:"speed,omitempty"`
	SampleRate   int          `json:"sample_rate,omitempty"`
	Channels     int          `json:"channels,omitempty"`
	Encoding     string       `json:"encoding,omitempty"`
	ChunkIndex   int          `json:"chunk_index,omitempty"`
	CharStart    int          `json:"char_start,omitempty"`
	CharEnd      int          `json:"char_end,omitempty"`
	TotalSamples int          `json:"total_samples,omitempty"`
	Timings      []TimingSpan `json:"timings,omitempty"`
}

type AudioFrame struct {
	Ref  string
	Data []byte
}

func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	if w == nil {
		return io.ErrClosedPipe
	}
	if kind != FrameJSON && kind != FrameAudio {
		return fmt.Errorf("unsupported Kokoro IPC frame kind %d", kind)
	}
	if len(payload) <= 0 || len(payload) > MaxFrameBytes {
		return fmt.Errorf("invalid Kokoro IPC frame size %d", len(payload))
	}
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	kind := header[0]
	if kind != FrameJSON && kind != FrameAudio {
		return 0, nil, fmt.Errorf("unsupported Kokoro IPC frame kind %d", kind)
	}
	size := int(binary.BigEndian.Uint32(header[1:]))
	if size <= 0 || size > MaxFrameBytes {
		return 0, nil, fmt.Errorf("invalid Kokoro IPC frame size %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return kind, payload, nil
}

func EncodeMessage(msg Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if len(data) <= 0 || len(data) > MaxFrameBytes {
		return nil, fmt.Errorf("invalid Kokoro JSON payload size %d", len(data))
	}
	return data, nil
}

func DecodeMessage(payload []byte) (Message, error) {
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return Message{}, fmt.Errorf("decode Kokoro IPC message: %w", err)
	}
	return msg, nil
}

func WriteMessage(w io.Writer, msg Message) error {
	data, err := EncodeMessage(msg)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameJSON, data)
}

func ReadMessage(r io.Reader) (Message, error) {
	kind, payload, err := ReadFrame(r)
	if err != nil {
		return Message{}, err
	}
	if kind != FrameJSON {
		return Message{}, fmt.Errorf("expected Kokoro JSON frame, got kind %d", kind)
	}
	return DecodeMessage(payload)
}

// EncodeAudioFrame prefixes raw little-endian float32 PCM with the synthesis
// request id. This lets a single helper stdout stream carry interleaved
// synthesis requests without base64-encoding audio.
func EncodeAudioFrame(ref string, pcm []byte) ([]byte, error) {
	refBytes := []byte(ref)
	if len(refBytes) == 0 || len(refBytes) > math.MaxUint16 {
		return nil, fmt.Errorf("invalid Kokoro audio reference length %d", len(refBytes))
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("Kokoro audio payload is empty")
	}
	payload := make([]byte, 2+len(refBytes)+len(pcm))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(refBytes)))
	copy(payload[2:2+len(refBytes)], refBytes)
	copy(payload[2+len(refBytes):], pcm)
	if len(payload) > MaxFrameBytes {
		return nil, fmt.Errorf("Kokoro audio payload exceeds %d bytes", MaxFrameBytes)
	}
	return payload, nil
}

func DecodeAudioFrame(payload []byte) (AudioFrame, error) {
	if len(payload) < 3 {
		return AudioFrame{}, fmt.Errorf("Kokoro audio frame is too short")
	}
	refLen := int(binary.BigEndian.Uint16(payload[:2]))
	if refLen <= 0 || len(payload) <= 2+refLen {
		return AudioFrame{}, fmt.Errorf("invalid Kokoro audio reference length %d", refLen)
	}
	return AudioFrame{
		Ref:  string(payload[2 : 2+refLen]),
		Data: append([]byte(nil), payload[2+refLen:]...),
	}, nil
}

func WriteAudioFrame(w io.Writer, ref string, pcm []byte) error {
	payload, err := EncodeAudioFrame(ref, pcm)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameAudio, payload)
}
