package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed data/cmudict.dict
var embeddedCMUDict string

var (
	cmuOnce       sync.Once
	cmuPronounce  map[string][]string
	numberPattern = regexp.MustCompile(`\$?\b\d+(?:\.\d+)?%?`)
)

var kokoroVocab = map[rune]int64{
	';': 1, ':': 2, ',': 3, '.': 4, '!': 5, '?': 6, '—': 9, '…': 10, '"': 11, '(': 12, ')': 13,
	'“': 14, '”': 15, ' ': 16, 'A': 24, 'I': 25, 'O': 31, 'Q': 33, 'S': 35, 'T': 36, 'W': 39,
	'Y': 41, 'ᵊ': 42, 'a': 43, 'b': 44, 'c': 45, 'd': 46, 'e': 47, 'f': 48, 'h': 50, 'i': 51,
	'j': 52, 'k': 53, 'l': 54, 'm': 55, 'n': 56, 'o': 57, 'p': 58, 'q': 59, 'r': 60, 's': 61, 't': 62,
	'u': 63, 'v': 64, 'w': 65, 'x': 66, 'y': 67, 'z': 68, 'ɑ': 69, 'ɐ': 70, 'ɒ': 71, 'æ': 72,
	'β': 75, 'ɔ': 76, 'ɕ': 77, 'ç': 78, 'ɖ': 80, 'ð': 81, 'ʤ': 82, 'ə': 83, 'ɚ': 85, 'ɛ': 86,
	'ɜ': 87, 'ɟ': 90, 'ɡ': 92, 'ɥ': 99, 'ɨ': 101, 'ɪ': 102, 'ʝ': 103, 'ɯ': 110, 'ɰ': 111,
	'ŋ': 112, 'ɳ': 113, 'ɲ': 114, 'ɴ': 115, 'ø': 116, 'ɸ': 118, 'θ': 119, 'œ': 120, 'ɹ': 123,
	'ɾ': 125, 'ɻ': 126, 'ʁ': 128, 'ɽ': 129, 'ʂ': 130, 'ʃ': 131, 'ʈ': 132, 'ʧ': 133, 'ʊ': 135,
	'ʋ': 136, 'ʌ': 138, 'ɣ': 139, 'ɤ': 140, 'χ': 142, 'ʎ': 143, 'ʒ': 147, 'ʔ': 148, 'ˈ': 156,
	'ˌ': 157, 'ː': 158, 'ʰ': 162, 'ʲ': 164, 'ᵻ': 177,
}

var arpabetConsonants = map[string]string{
	"B": "b", "CH": "ʧ", "D": "d", "DH": "ð", "F": "f", "G": "ɡ", "HH": "h", "JH": "ʤ",
	"K": "k", "L": "l", "M": "m", "N": "n", "NG": "ŋ", "P": "p", "R": "ɹ", "S": "s", "SH": "ʃ",
	"T": "t", "TH": "θ", "V": "v", "W": "w", "Y": "j", "Z": "z", "ZH": "ʒ",
}

var letterNames = map[rune]string{
	'a': "AY1", 'b': "B IY1", 'c': "S IY1", 'd': "D IY1", 'e': "IY1", 'f': "EH1 F", 'g': "JH IY1",
	'h': "EY1 CH", 'i': "AY1", 'j': "JH EY1", 'k': "K EY1", 'l': "EH1 L", 'm': "EH1 M", 'n': "EH1 N",
	'o': "OW1", 'p': "P IY1", 'q': "K Y UW1", 'r': "AA1 R", 's': "EH1 S", 't': "T IY1", 'u': "Y UW1",
	'v': "V IY1", 'w': "D AH1 B AH0 L Y UW0", 'x': "EH1 K S", 'y': "W AY1", 'z': "Z IY1",
}

func loadCMUDict() map[string][]string {
	cmuOnce.Do(func() {
		cmuPronounce = make(map[string][]string, 130000)
		scanner := bufio.NewScanner(strings.NewReader(embeddedCMUDict))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ";;;") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			word := strings.ToLower(fields[0])
			if i := strings.Index(word, "("); i > 0 && strings.HasSuffix(word, ")") {
				word = word[:i]
			}
			if _, exists := cmuPronounce[word]; exists {
				continue
			}
			cmuPronounce[word] = append([]string(nil), fields[1:]...)
		}
	})
	return cmuPronounce
}

type mappedSpeechRune struct {
	Value     rune
	CharStart int
	CharEnd   int
}

type phonemeSourceGroup struct {
	CharStart  int
	CharEnd    int
	TokenStart int
	TokenEnd   int
}

// unknownWordPronouncer is intentionally tiny so Kokoro remains fully usable
// without an optional G2P model. The Moonshine implementation satisfies this
// interface when its local assets are present; otherwise the existing
// deterministic letter-name fallback remains in place.
type unknownWordPronouncer interface {
	Pronounce(word string) (string, bool)
}

var speechTextReplacements = []struct {
	From string
	To   string
}{
	{"Mrs.", "Missus"},
	{"Mr.", "Mister"},
	{"Dr.", "Doctor"},
	{"St.", "Saint"},
	{"e.g.", "for example"},
	{"i.e.", "that is"},
	{"etc.", "etcetera"},
	{"’", "'"},
	{"‘", "'"},
	{"–", "—"},
	{"\u00a0", " "},
}

func appendMappedReplacement(out []mappedSpeechRune, value string, start, end int) []mappedSpeechRune {
	for _, r := range value {
		out = append(out, mappedSpeechRune{Value: r, CharStart: start, CharEnd: end})
	}
	return out
}

func normalizeEnglishWithSource(text string) []mappedSpeechRune {
	if text == "" {
		return nil
	}
	numberMatches := numberPattern.FindAllStringIndex(text, -1)
	numberByStart := make(map[int][2]int, len(numberMatches))
	for _, match := range numberMatches {
		if len(match) == 2 {
			numberByStart[match[0]] = [2]int{match[0], match[1]}
		}
	}
	out := make([]mappedSpeechRune, 0, len(text))
	utf16Pos := 0
	for i := 0; i < len(text); {
		if match, ok := numberByStart[i]; ok {
			raw := text[match[0]:match[1]]
			end := utf16Pos + utf16Units(raw)
			out = appendMappedReplacement(out, spokenNumberExpression(raw), utf16Pos, end)
			utf16Pos = end
			i = match[1]
			continue
		}
		replaced := false
		for _, replacement := range speechTextReplacements {
			if !strings.HasPrefix(text[i:], replacement.From) {
				continue
			}
			end := utf16Pos + utf16Units(replacement.From)
			out = appendMappedReplacement(out, replacement.To, utf16Pos, end)
			utf16Pos = end
			i += len(replacement.From)
			replaced = true
			break
		}
		if replaced {
			continue
		}
		r, size := utf8DecodeRune(text[i:])
		units := 1
		if r > 0xffff {
			units = 2
		}
		out = append(out, mappedSpeechRune{Value: r, CharStart: utf16Pos, CharEnd: utf16Pos + units})
		utf16Pos += units
		i += size
	}

	// Match strings.Join(strings.Fields(...), " "): trim leading/trailing
	// whitespace and collapse every interior run to one source-mapped space.
	collapsed := make([]mappedSpeechRune, 0, len(out))
	spacePending := false
	spaceStart := 0
	spaceEnd := 0
	for _, item := range out {
		if unicode.IsSpace(item.Value) {
			if len(collapsed) > 0 {
				if !spacePending {
					spaceStart = item.CharStart
				}
				spacePending = true
				spaceEnd = item.CharEnd
			}
			continue
		}
		if spacePending {
			collapsed = append(collapsed, mappedSpeechRune{Value: ' ', CharStart: spaceStart, CharEnd: spaceEnd})
			spacePending = false
		}
		collapsed = append(collapsed, item)
	}
	return collapsed
}

func normalizeEnglishForSpeech(text string) string {
	mapped := normalizeEnglishWithSource(text)
	var out strings.Builder
	for _, item := range mapped {
		out.WriteRune(item.Value)
	}
	return out.String()
}

func spokenNumberExpression(raw string) string {
	value := strings.TrimSpace(raw)
	currency := strings.HasPrefix(value, "$")
	percent := strings.HasSuffix(value, "%")
	value = strings.TrimPrefix(strings.TrimSuffix(value, "%"), "$")
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > 999999999999 {
		return raw
	}
	spoken := integerWords(whole)
	if len(parts) == 2 && parts[1] != "" {
		digits := make([]string, 0, len(parts[1]))
		for _, r := range parts[1] {
			if r < '0' || r > '9' {
				return raw
			}
			digits = append(digits, integerWords(int64(r-'0')))
		}
		spoken += " point " + strings.Join(digits, " ")
	}
	if currency {
		if len(parts) == 1 && whole == 1 {
			spoken += " dollar"
		} else {
			spoken += " dollars"
		}
	}
	if percent {
		spoken += " percent"
	}
	return spoken
}

func integerWords(value int64) string {
	if value == 0 {
		return "zero"
	}
	ones := []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	underThousand := func(n int64) string {
		var out []string
		if n >= 100 {
			out = append(out, ones[n/100], "hundred")
			n %= 100
		}
		if n >= 20 {
			out = append(out, tens[n/10])
			if n%10 != 0 {
				out = append(out, ones[n%10])
			}
		} else if n > 0 {
			out = append(out, ones[n])
		}
		return strings.Join(out, " ")
	}
	type scale struct {
		value int64
		name  string
	}
	scales := []scale{{1000000000, "billion"}, {1000000, "million"}, {1000, "thousand"}}
	var out []string
	for _, s := range scales {
		if value >= s.value {
			out = append(out, underThousand(value/s.value), s.name)
			value %= s.value
		}
	}
	if value > 0 {
		out = append(out, underThousand(value))
	}
	return strings.Join(out, " ")
}

func phonemizeEnglish(text string) (string, []int64, error) {
	phonemes, tokens, _, err := phonemizeEnglishWithSource(text)
	return phonemes, tokens, err
}

func phonemizeEnglishWithSource(text string) (string, []int64, []phonemeSourceGroup, error) {
	return phonemizeEnglishWithSourceUsing(text, nil)
}

func phonemizeEnglishWithSourceUsing(text string, oov unknownWordPronouncer) (string, []int64, []phonemeSourceGroup, error) {
	mapped := normalizeEnglishWithSource(text)
	if len(mapped) == 0 {
		return "", nil, nil, fmt.Errorf("text is empty after normalization")
	}
	dict := loadCMUDict()
	var phonemes strings.Builder
	tokens := make([]int64, 0, len(mapped)*2)
	groups := make([]phonemeSourceGroup, 0, len(mapped)/4)
	needSpace := false
	appendTokenRune := func(r rune) bool {
		id, ok := kokoroVocab[r]
		if !ok {
			return false
		}
		if r == ' ' {
			if len(tokens) == 0 || tokens[len(tokens)-1] == 16 {
				return false
			}
		}
		tokens = append(tokens, id)
		phonemes.WriteRune(r)
		return true
	}
	appendIPA := func(ipa string) bool {
		ipa = normalizeMoonshineIPA(ipa)
		if strings.TrimSpace(ipa) == "" {
			return false
		}
		for _, pr := range ipa {
			if unicode.IsSpace(pr) {
				continue
			}
			// A Moonshine pronunciation should already use Kokoro-compatible
			// English IPA. Reject rather than silently delete an unknown symbol;
			// the caller can then fall back to AgentGO's deterministic spelling.
			if _, ok := kokoroVocab[pr]; !ok {
				return false
			}
		}
		start := len(tokens)
		for _, pr := range ipa {
			if unicode.IsSpace(pr) {
				continue
			}
			appendTokenRune(pr)
		}
		return len(tokens) > start
	}
	for i := 0; i < len(mapped); {
		r := mapped[i].Value
		if unicode.IsLetter(r) || r == '\'' {
			start := i
			var word strings.Builder
			for i < len(mapped) {
				r2 := mapped[i].Value
				if !unicode.IsLetter(r2) && r2 != '\'' {
					break
				}
				word.WriteRune(r2)
				i++
			}
			rawWord := word.String()
			phones := cmuPronunciationForWord(dict, rawWord)
			if needSpace && len(tokens) > 0 {
				appendTokenRune(' ')
			}
			tokenStart := len(tokens)
			pronounced := false
			if len(phones) > 0 {
				for _, pr := range arpabetToKokoroIPA(phones) {
					appendTokenRune(pr)
				}
				pronounced = len(tokens) > tokenStart
			} else if oov != nil && shouldUseAutomaticG2P(rawWord) {
				if ipa, ok := oov.Pronounce(strings.ToLower(strings.Trim(rawWord, "'"))); ok {
					pronounced = appendIPA(ipa)
				}
			}
			if !pronounced {
				for _, pr := range arpabetToKokoroIPA(letterNamePronunciation(rawWord)) {
					appendTokenRune(pr)
				}
			}
			tokenEnd := len(tokens)
			if tokenEnd > tokenStart {
				groups = append(groups, phonemeSourceGroup{
					CharStart:  mapped[start].CharStart,
					CharEnd:    mapped[i-1].CharEnd,
					TokenStart: tokenStart,
					TokenEnd:   tokenEnd,
				})
			}
			needSpace = true
			continue
		}
		i++
		if unicode.IsSpace(r) || r == '-' || r == '_' || r == '/' || r == '\\' {
			needSpace = true
			continue
		}
		if _, ok := kokoroVocab[r]; ok {
			appendTokenRune(r)
			needSpace = true
		}
	}
	for len(tokens) > 0 && tokens[len(tokens)-1] == 16 {
		tokens = tokens[:len(tokens)-1]
	}
	phonemeText := strings.TrimSpace(phonemes.String())
	if len(tokens) == 0 {
		return phonemeText, nil, nil, fmt.Errorf("text produced no Kokoro tokens")
	}
	return phonemeText, tokens, groups, nil
}

func cmuPronunciationForWord(dict map[string][]string, raw string) []string {
	word := strings.ToLower(strings.Trim(raw, "'"))
	if word == "" {
		return nil
	}
	if phones := dict[word]; len(phones) > 0 {
		return phones
	}
	if strings.HasSuffix(word, "'s") {
		if phones := dict[strings.TrimSuffix(word, "'s")]; len(phones) > 0 {
			return append(append([]string(nil), phones...), "Z")
		}
	}
	return nil
}

func letterNamePronunciation(raw string) []string {
	word := strings.ToLower(strings.Trim(raw, "'"))
	var phones []string
	for _, r := range word {
		if names, ok := letterNames[r]; ok {
			phones = append(phones, strings.Fields(names)...)
		}
	}
	return phones
}

func pronunciationForWord(dict map[string][]string, raw string) []string {
	if phones := cmuPronunciationForWord(dict, raw); len(phones) > 0 {
		return phones
	}
	return letterNamePronunciation(raw)
}

func shouldUseAutomaticG2P(raw string) bool {
	word := strings.Trim(raw, "'")
	if word == "" {
		return false
	}
	letterCount := 0
	upperCount := 0
	for _, r := range word {
		if r == '\'' {
			continue
		}
		if !unicode.IsLetter(r) {
			return false
		}
		letterCount++
		if unicode.IsUpper(r) {
			upperCount++
		}
	}
	if letterCount == 0 {
		return false
	}
	// Preserve the useful historical behavior for obvious developer acronyms
	// (HTML, CPU, API). Mixed/title/lowercase unknown words go through G2P.
	return !(letterCount >= 2 && upperCount == letterCount)
}

func normalizeMoonshineIPA(value string) string {
	value = strings.TrimSpace(value)
	// Moonshine's CMU-derived English IPA uses ɝ for stressed ER while the
	// Kokoro vocabulary represents that sound as ɜ + ɹ.
	value = strings.ReplaceAll(value, "ɝ", "ɜɹ")
	return value
}

func arpabetToKokoroIPA(phones []string) string {
	var out strings.Builder
	for _, raw := range phones {
		phone := strings.ToUpper(strings.TrimSpace(raw))
		if phone == "" {
			continue
		}
		stress := byte(0)
		last := phone[len(phone)-1]
		if last >= '0' && last <= '2' {
			stress = last
			phone = phone[:len(phone)-1]
		}
		if stress == '1' {
			out.WriteRune('ˈ')
		} else if stress == '2' {
			out.WriteRune('ˌ')
		}
		switch phone {
		case "AA":
			out.WriteString("ɑ")
		case "AE":
			out.WriteString("æ")
		case "AH":
			if stress == '0' {
				out.WriteString("ə")
			} else {
				out.WriteString("ʌ")
			}
		case "AO":
			out.WriteString("ɔ")
		case "AW":
			out.WriteString("aʊ")
		case "AY":
			out.WriteString("aɪ")
		case "EH":
			out.WriteString("ɛ")
		case "ER":
			if stress == '0' {
				out.WriteString("ɚ")
			} else {
				out.WriteString("ɜɹ")
			}
		case "EY":
			out.WriteString("eɪ")
		case "IH":
			out.WriteString("ɪ")
		case "IY":
			out.WriteString("i")
		case "OW":
			out.WriteString("oʊ")
		case "OY":
			out.WriteString("ɔɪ")
		case "UH":
			out.WriteString("ʊ")
		case "UW":
			out.WriteString("u")
		default:
			if mapped, ok := arpabetConsonants[phone]; ok {
				out.WriteString(mapped)
			}
		}
	}
	return out.String()
}

// Kept local so phonemizer.go does not need to import unicode/utf8 just for one
// operation in multiple hot loops.
func utf8DecodeRune(value string) (rune, int) {
	for i, r := range value {
		if i == 0 {
			return r, len(string(r))
		}
	}
	return unicode.ReplacementChar, 1
}
