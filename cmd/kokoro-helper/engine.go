package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	kokoroSampleRate       = 24000
	kokoroChannels         = 1
	kokoroAudioEncoding    = "f32le"
	kokoroMaxModelTokens   = 510
	kokoroTargetChunkRunes = 180
	kokoroPCMFrameSamples  = kokoroSampleRate / 2 // 0.5 seconds per streamed audio frame.
)

type sourceChunk struct {
	Text      string
	CharStart int
	CharEnd   int
}

type kokoroWordTiming struct {
	CharStart   int
	CharEnd     int
	StartSample int
	EndSample   int
}

type kokoroSynthesisChunk struct {
	Source  sourceChunk
	PCM     []float32
	Timings []kokoroWordTiming
}

type kokoroEngine interface {
	RuntimeVersion() string
	TimingSupported() bool
	G2PStatus() (bool, string)
	Synthesize(context.Context, string, string, float32, func(kokoroSynthesisChunk) error) error
	Close() error
}

func clampKokoroSpeed(speed float32) float32 {
	if math.IsNaN(float64(speed)) || math.IsInf(float64(speed), 0) || speed <= 0 {
		return 1
	}
	if speed < 0.75 {
		return 0.75
	}
	if speed > 1.5 {
		return 1.5
	}
	return speed
}

// splitNaturalSourceText keeps source offsets so the browser can highlight the
// source whose audio is actually playing. r455 deliberately does not choose
// sentence/punctuation boundaries: after reaching the target size it continues
// only until the next whitespace boundary so Kokoro, not AgentGO, decides the
// sentence cadence while words remain intact.
func splitNaturalSourceText(text string) []sourceChunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var chunks []sourceChunk
	start := 0
	runeCount := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		raw := text[start:end]
		leftTrim := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		right := strings.TrimRight(raw, " \t\r\n")
		if right == "" {
			start = end
			runeCount = 0
			return
		}
		chunkStart := start + leftTrim
		chunkEnd := start + len(right)
		chunks = append(chunks, sourceChunk{Text: text[chunkStart:chunkEnd], CharStart: utf16Units(text[:chunkStart]), CharEnd: utf16Units(text[:chunkEnd])})
		start = end
		runeCount = 0
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		next := i + size
		runeCount++
		if runeCount >= kokoroTargetChunkRunes && (r == ' ' || r == '\t' || r == '\r' || r == '\n') {
			flush(next)
		}
		i = next
	}
	flush(len(text))
	return chunks
}

func utf16Units(value string) int {
	count := 0
	for _, r := range value {
		if r > 0xffff {
			count += 2
		} else {
			count++
		}
	}
	return count
}

func float32PCMBytes(samples []float32) []byte {
	data := make([]byte, len(samples)*4)
	for i, sample := range samples {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(sample))
	}
	return data
}

func splitPCMFrames(samples []float32) [][]float32 {
	if len(samples) == 0 {
		return nil
	}
	frames := make([][]float32, 0, (len(samples)+kokoroPCMFrameSamples-1)/kokoroPCMFrameSamples)
	for start := 0; start < len(samples); start += kokoroPCMFrameSamples {
		end := start + kokoroPCMFrameSamples
		if end > len(samples) {
			end = len(samples)
		}
		frames = append(frames, samples[start:end])
	}
	return frames
}

func validateSynthesisRequest(text, voice string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("speech text is empty")
	}
	if len(text) > 200000 {
		return fmt.Errorf("speech text exceeds the AgentGO synthesis limit")
	}
	if strings.TrimSpace(voice) == "" {
		return fmt.Errorf("Kokoro voice is empty")
	}
	return nil
}

func buildKokoroWordTimings(chunk sourceChunk, groups []phonemeSourceGroup, tokens []int64, durations []float64, sampleCount int) []kokoroWordTiming {
	if len(groups) == 0 || len(tokens) == 0 || len(durations) < len(tokens)+2 || sampleCount <= 0 {
		return nil
	}
	for _, value := range durations {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil
		}
	}
	// The timestamp-enabled Kokoro graph reports predicted durations in model
	// frames. Upstream's timestamp reference maps one frame to 600 samples at
	// 24 kHz (40 frames/second), trims three frames from the initial boundary,
	// and gives a word half of a following whitespace duration. Preserve that
	// model clock here, then clamp to the waveform actually returned.
	const samplesPerDurationFrame = 600.0
	prefix := make([]float64, len(tokens)+1)
	for tokenIndex := range tokens {
		prefix[tokenIndex+1] = prefix[tokenIndex] + durations[1+tokenIndex]
	}
	boundaryFrames := math.Max(0, durations[0]-3)
	timings := make([]kokoroWordTiming, 0, len(groups))
	for _, group := range groups {
		if group.TokenStart < 0 || group.TokenEnd <= group.TokenStart || group.TokenEnd > len(tokens) {
			continue
		}
		startFrames := boundaryFrames + prefix[group.TokenStart]
		endFrames := boundaryFrames + prefix[group.TokenEnd]
		if group.TokenEnd < len(tokens) && tokens[group.TokenEnd] == 16 {
			endFrames += durations[1+group.TokenEnd] / 2
		}
		startSample := int(math.Round(startFrames * samplesPerDurationFrame))
		endSample := int(math.Round(endFrames * samplesPerDurationFrame))
		if startSample < 0 {
			startSample = 0
		}
		if startSample >= sampleCount {
			continue
		}
		if endSample > sampleCount {
			endSample = sampleCount
		}
		if endSample <= startSample {
			continue
		}
		timing := kokoroWordTiming{
			CharStart:   chunk.CharStart + group.CharStart,
			CharEnd:     chunk.CharStart + group.CharEnd,
			StartSample: startSample,
			EndSample:   endSample,
		}
		if len(timings) > 0 {
			last := &timings[len(timings)-1]
			// Normalization can expand one source span into several spoken words
			// (for example "$42" -> "forty two dollars"). Keep the original
			// source span highlighted for the entire normalized spoken interval.
			if last.CharStart == timing.CharStart && last.CharEnd == timing.CharEnd {
				if timing.EndSample > last.EndSample {
					last.EndSample = timing.EndSample
				}
				continue
			}
		}
		timings = append(timings, timing)
	}
	return timings
}
