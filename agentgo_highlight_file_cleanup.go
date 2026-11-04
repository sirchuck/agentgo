package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode/utf8"
)

// stripAgentGOCodeChangeSpans removes AgentGO's reply-only display wrappers from
// AI-generated file content. It preserves the wrapped source and all unrelated
// spans byte-for-byte. An incomplete marker rejects the file rather than making
// a potentially destructive guess about which closing tag belongs to it.
func stripAgentGOCodeChangeSpans(content string) (string, int, error) {
	if !strings.Contains(content, "agentgo-code-change") {
		return content, 0, nil
	}
	type spanFrame struct {
		start, end int
		marker     bool
	}
	type removal struct{ start, end int }
	stack := []spanFrame{}
	removals := []removal{}
	markers := 0

	for cursor := 0; cursor < len(content); {
		lt := strings.IndexByte(content[cursor:], '<')
		if lt < 0 {
			break
		}
		lt += cursor
		end, closing, selfClosing, isSpan, marker, incomplete := agentGOScanSpanTag(content, lt)
		if incomplete {
			return "", 0, errors.New("AgentGO highlight span has an incomplete opening tag")
		}
		if !isSpan {
			cursor = lt + 1
			continue
		}
		cursor = end
		if closing {
			if len(stack) == 0 {
				continue // Other HTML may have unmatched spans; do not change them.
			}
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if frame.marker {
				removals = append(removals, removal{frame.start, frame.end}, removal{lt, end})
			}
			continue
		}
		if marker {
			markers++
			if selfClosing {
				return "", 0, errors.New("AgentGO highlight span is self-closing")
			}
		}
		if !selfClosing {
			stack = append(stack, spanFrame{start: lt, end: end, marker: marker})
		}
	}
	for _, frame := range stack {
		if frame.marker {
			return "", 0, errors.New("AgentGO highlight span has no matching closing </span>")
		}
	}
	if markers == 0 {
		return content, 0, nil
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].start < removals[j].start })
	var out strings.Builder
	out.Grow(len(content))
	cursor := 0
	for _, span := range removals {
		if span.start < cursor {
			return "", 0, errors.New("AgentGO highlight span markers overlap")
		}
		out.WriteString(content[cursor:span.start])
		cursor = span.end
	}
	out.WriteString(content[cursor:])
	return out.String(), markers, nil
}

// agentGOScanSpanTag recognizes only actual span elements, including attributes
// quoted with either kind of quote and tags with '>' inside an attribute value.
// It does not parse or rewrite any of the file's other source text.
func agentGOScanSpanTag(content string, start int) (end int, closing, selfClosing, isSpan, marker, incomplete bool) {
	pos := start + 1
	if pos >= len(content) {
		return
	}
	if content[pos] == '/' {
		closing = true
		pos++
	}
	if pos+4 > len(content) || !strings.EqualFold(content[pos:pos+4], "span") {
		return
	}
	pos += 4
	if pos < len(content) && !agentGOHTMLSpace(content[pos]) && content[pos] != '>' && content[pos] != '/' {
		return // e.g. <spanner>, not <span>
	}
	isSpan = true
	quote := byte(0)
	for end = pos; end < len(content); end++ {
		switch content[end] {
		case '\'', '"':
			if quote == 0 {
				quote = content[end]
			} else if quote == content[end] {
				quote = 0
			}
		case '>':
			if quote == 0 {
				end++
				if !closing {
					p := end - 2
					for p >= pos && agentGOHTMLSpace(content[p]) {
						p--
					}
					selfClosing = p >= pos && content[p] == '/'
					marker = agentGOHasHighlightClass(content[pos : end-1])
				}
				return
			}
		}
	}
	// A malformed non-marker span is not ours to fix. A malformed marker is
	// dangerous, so fail rather than leave a partial display tag in source.
	incomplete = !closing && strings.Contains(content[pos:], "agentgo-code-change")
	isSpan = false
	return
}

func agentGOHTMLSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	}
	return false
}

func agentGOHasHighlightClass(attrs string) bool {
	for i := 0; i < len(attrs); {
		for i < len(attrs) && agentGOHTMLSpace(attrs[i]) {
			i++
		}
		if i == len(attrs) || attrs[i] == '/' {
			break
		}
		start := i
		for i < len(attrs) && !agentGOHTMLSpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := attrs[start:i]
		for i < len(attrs) && agentGOHTMLSpace(attrs[i]) {
			i++
		}
		if i == len(attrs) || attrs[i] != '=' {
			continue
		}
		i++
		for i < len(attrs) && agentGOHTMLSpace(attrs[i]) {
			i++
		}
		valueStart := i
		if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
			quote := attrs[i]
			i++
			valueStart = i
			for i < len(attrs) && attrs[i] != quote {
				i++
			}
		} else {
			for i < len(attrs) && !agentGOHTMLSpace(attrs[i]) && attrs[i] != '/' {
				i++
			}
		}
		value := attrs[valueStart:i]
		if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
			i++
		}
		if strings.EqualFold(name, "class") {
			for _, className := range strings.Fields(value) {
				if className == "agentgo-code-change" {
					return true
				}
			}
		}
	}
	return false
}

// sanitizeAIFileHighlightMarkup is for AI-produced file payloads only. Never
// run it over an existing project file, a user editor submission, or a reply.
func sanitizeAIFileHighlightMarkup(data []byte, filePath string) ([]byte, error) {
	if !bytes.Contains(data, []byte("agentgo-code-change")) || !utf8.Valid(data) {
		return data, nil
	}
	clean, count, err := stripAgentGOCodeChangeSpans(string(data))
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", filePath, err)
	}
	if count == 0 {
		return data, nil
	}
	log.Printf("AgentGO removed %d reply-only highlight span(s) from AI-generated file %q", count, filePath)
	return []byte(clean), nil
}
