package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentgo/internal/kokoroipc"
)

const (
	phoneticsRelativePath = "system_prompts/phonetics.md"
	maximumPhoneticsBytes = 256 << 10
)

type phoneticRule struct {
	Written string
	Spoken  string
	order   int
}

type phoneticMappedRune struct {
	TransformedStart int
	TransformedEnd   int
	SourceStart      int
	SourceEnd        int
}

type phoneticTransform struct {
	Text              string
	SourceUTF16Length int
	MappedRunes       []phoneticMappedRune
}

type phoneticsResponse struct {
	OK           bool   `json:"ok"`
	Path         string `json:"path"`
	Content      string `json:"content"`
	RuleCount    int    `json:"rule_count"`
	IgnoredLines int    `json:"ignored_lines"`
}

type phoneticsRequest struct {
	Content string `json:"content"`
}

func parsePhoneticRules(content string) ([]phoneticRule, int) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	rules := make([]phoneticRule, 0, len(lines))
	ignored := 0
	for lineIndex, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=>", 2)
		if len(parts) != 2 {
			ignored++
			continue
		}
		written := strings.TrimSpace(parts[0])
		spoken := strings.TrimSpace(parts[1])
		if written == "" || spoken == "" {
			ignored++
			continue
		}
		rules = append(rules, phoneticRule{Written: written, Spoken: spoken, order: lineIndex})
	}
	// Longer entries win first so a phrase or product name cannot be partially
	// consumed by a shorter rule that happens to share its prefix.
	sort.SliceStable(rules, func(i, j int) bool {
		left := utf8.RuneCountInString(rules[i].Written)
		right := utf8.RuneCountInString(rules[j].Written)
		if left == right {
			return rules[i].order < rules[j].order
		}
		return left > right
	})
	return rules, ignored
}

func isPhoneticWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func phoneticRuleMatchAt(text string, byteStart int, written string) (int, bool) {
	if written == "" || byteStart < 0 || byteStart+len(written) > len(text) {
		return 0, false
	}
	candidate := text[byteStart : byteStart+len(written)]
	if !strings.EqualFold(candidate, written) {
		return 0, false
	}
	first, _ := utf8.DecodeRuneInString(written)
	last, _ := utf8.DecodeLastRuneInString(written)
	if isPhoneticWordRune(first) && byteStart > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:byteStart])
		if isPhoneticWordRune(previous) {
			return 0, false
		}
	}
	byteEnd := byteStart + len(written)
	if isPhoneticWordRune(last) && byteEnd < len(text) {
		next, _ := utf8.DecodeRuneInString(text[byteEnd:])
		if isPhoneticWordRune(next) {
			return 0, false
		}
	}
	return byteEnd, true
}

func utf16StringUnits(value string) int {
	total := 0
	for _, r := range value {
		total += utf16RuneUnits(r)
	}
	return total
}

func utf16RuneUnits(r rune) int {
	if r > 0xffff {
		return 2
	}
	return 1
}

func applyPhoneticRules(text string, rules []phoneticRule) phoneticTransform {
	var out strings.Builder
	out.Grow(len(text))
	mapped := make([]phoneticMappedRune, 0, utf8.RuneCountInString(text))
	sourcePos := 0
	transformedPos := 0
	for bytePos := 0; bytePos < len(text); {
		matched := false
		for _, rule := range rules {
			byteEnd, ok := phoneticRuleMatchAt(text, bytePos, rule.Written)
			if !ok {
				continue
			}
			sourceEnd := sourcePos + utf16StringUnits(text[bytePos:byteEnd])
			for _, r := range rule.Spoken {
				units := utf16RuneUnits(r)
				mapped = append(mapped, phoneticMappedRune{
					TransformedStart: transformedPos,
					TransformedEnd:   transformedPos + units,
					SourceStart:      sourcePos,
					SourceEnd:        sourceEnd,
				})
				out.WriteRune(r)
				transformedPos += units
			}
			sourcePos = sourceEnd
			bytePos = byteEnd
			matched = true
			break
		}
		if matched {
			continue
		}
		r, size := utf8.DecodeRuneInString(text[bytePos:])
		units := utf16RuneUnits(r)
		mapped = append(mapped, phoneticMappedRune{
			TransformedStart: transformedPos,
			TransformedEnd:   transformedPos + units,
			SourceStart:      sourcePos,
			SourceEnd:        sourcePos + units,
		})
		out.WriteRune(r)
		transformedPos += units
		sourcePos += units
		bytePos += size
	}
	return phoneticTransform{Text: out.String(), SourceUTF16Length: sourcePos, MappedRunes: mapped}
}

func (transform phoneticTransform) remapRange(start, end int) (int, int) {
	if end < start {
		end = start
	}
	mappedStart := transform.SourceUTF16Length
	mappedEnd := 0
	found := false
	for _, item := range transform.MappedRunes {
		if item.TransformedEnd <= start || item.TransformedStart >= end {
			continue
		}
		if !found || item.SourceStart < mappedStart {
			mappedStart = item.SourceStart
		}
		if !found || item.SourceEnd > mappedEnd {
			mappedEnd = item.SourceEnd
		}
		found = true
	}
	if found {
		return mappedStart, mappedEnd
	}
	if start <= 0 {
		return 0, 0
	}
	if len(transform.MappedRunes) == 0 {
		if start > transform.SourceUTF16Length {
			start = transform.SourceUTF16Length
		}
		if end > transform.SourceUTF16Length {
			end = transform.SourceUTF16Length
		}
		return start, end
	}
	last := transform.MappedRunes[len(transform.MappedRunes)-1]
	if start >= last.TransformedEnd {
		return transform.SourceUTF16Length, transform.SourceUTF16Length
	}
	for _, item := range transform.MappedRunes {
		if start <= item.TransformedStart {
			return item.SourceStart, item.SourceStart
		}
	}
	return transform.SourceUTF16Length, transform.SourceUTF16Length
}

func (transform phoneticTransform) remapMessage(message *kokoroipc.Message) {
	if message == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(message.Type)) {
	case "chunk_start":
		message.CharStart, message.CharEnd = transform.remapRange(message.CharStart, message.CharEnd)
	case "chunk_timing":
		for i := range message.Timings {
			message.Timings[i].CharStart, message.Timings[i].CharEnd = transform.remapRange(message.Timings[i].CharStart, message.Timings[i].CharEnd)
		}
	}
}

func (a *App) phoneticsPath() string {
	root := "."
	if a != nil && strings.TrimSpace(a.appRoot) != "" {
		root = a.appRoot
	}
	return filepath.Join(root, filepath.FromSlash(phoneticsRelativePath))
}

func (a *App) readPhonetics() (string, error) {
	path := a.phoneticsPath()
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > maximumPhoneticsBytes {
			return "", errors.New("phonetics.md exceeds the AgentGO size limit")
		}
		return string(data), nil
	}
	if os.IsNotExist(err) {
		// The shipped system_prompts/phonetics.md file owns the starter library.
		// If a user removes it, speech simply falls back to the source text until
		// they save a new library from the editor.
		return "", nil
	}
	return "", err
}

func normalizePhoneticsFileContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
}

func (a *App) handleTTSPhonetics(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		content, err := a.readPhonetics()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rules, ignored := parsePhoneticRules(content)
		writeJSON(w, http.StatusOK, phoneticsResponse{OK: true, Path: phoneticsRelativePath, Content: content, RuleCount: len(rules), IgnoredLines: ignored})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maximumPhoneticsBytes*4)
		var req phoneticsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid phonetics request", http.StatusBadRequest)
			return
		}
		content := normalizePhoneticsFileContent(req.Content)
		if len([]byte(content)) > maximumPhoneticsBytes {
			http.Error(w, "phonetics.md exceeds the AgentGO size limit", http.StatusRequestEntityTooLarge)
			return
		}
		if err := os.MkdirAll(filepath.Dir(a.phoneticsPath()), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := atomicWriteFile(a.phoneticsPath(), []byte(content), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rules, ignored := parsePhoneticRules(content)
		writeJSON(w, http.StatusOK, phoneticsResponse{OK: true, Path: phoneticsRelativePath, Content: content, RuleCount: len(rules), IgnoredLines: ignored})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
