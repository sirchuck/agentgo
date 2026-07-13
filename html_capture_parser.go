package main

import (
	"errors"
	stdhtml "html"
	"strings"
)

// parseHTMLCaptureDocument uses a forgiving HTML parser intended for real-world
// browser markup. It accepts boolean attributes, mismatched end tags, raw script
// text, unquoted values, and other constructs that encoding/xml rejects.
func parseHTMLCaptureDocument(data []byte, baseURL, contentType string) (htmlCaptureDocument, error) {
	if len(data) == 0 {
		return htmlCaptureDocument{}, errors.New("webpage HTML was empty")
	}
	root := &htmlCaptureNode{Tag: "#document", Attrs: map[string]string{}}
	parser := tolerantHTMLCaptureParser{source: string(data)}
	parser.parseInto(root)
	if len(root.Children) == 0 && strings.TrimSpace(root.Text.String()) == "" {
		return htmlCaptureDocument{}, errors.New("webpage HTML contained no readable structure")
	}
	metadata := extractHTMLCaptureMetadata(root, baseURL)
	metadata.ContentType = contentType
	return htmlCaptureDocument{Root: root, Metadata: metadata}, nil
}

type tolerantHTMLCaptureParser struct {
	source string
	pos    int
}

func (p *tolerantHTMLCaptureParser) parseInto(root *htmlCaptureNode) {
	stack := []*htmlCaptureNode{root}
	for p.pos < len(p.source) {
		if p.source[p.pos] != '<' {
			p.consumeText(stack[len(stack)-1])
			continue
		}
		if p.consumeCommentOrDeclaration(stack[len(stack)-1]) {
			continue
		}
		if p.pos+1 < len(p.source) && p.source[p.pos+1] == '/' {
			p.consumeEndTag(&stack)
			continue
		}
		if p.pos+1 < len(p.source) && isHTMLTagNameByte(p.source[p.pos+1]) {
			p.consumeStartTag(&stack)
			continue
		}
		stack[len(stack)-1].Text.WriteByte('<')
		p.pos++
	}
}

func (p *tolerantHTMLCaptureParser) consumeText(node *htmlCaptureNode) {
	start := p.pos
	if next := strings.IndexByte(p.source[start:], '<'); next >= 0 {
		p.pos = start + next
	} else {
		p.pos = len(p.source)
	}
	if p.pos > start {
		node.Text.WriteString(stdhtml.UnescapeString(p.source[start:p.pos]))
	}
}

func (p *tolerantHTMLCaptureParser) consumeCommentOrDeclaration(node *htmlCaptureNode) bool {
	rest := p.source[p.pos:]
	switch {
	case strings.HasPrefix(rest, "<!--"):
		if end := strings.Index(rest[4:], "-->"); end >= 0 {
			p.pos += 4 + end + 3
		} else {
			p.pos = len(p.source)
		}
		return true
	case strings.HasPrefix(strings.ToUpper(rest), "<![CDATA["):
		start := p.pos + len("<![CDATA[")
		if end := strings.Index(p.source[start:], "]]>"); end >= 0 {
			node.Text.WriteString(p.source[start : start+end])
			p.pos = start + end + 3
		} else {
			node.Text.WriteString(p.source[start:])
			p.pos = len(p.source)
		}
		return true
	case strings.HasPrefix(rest, "<!") || strings.HasPrefix(rest, "<?"):
		p.pos = scanHTMLTagEnd(p.source, p.pos+2)
		return true
	default:
		return false
	}
}

func (p *tolerantHTMLCaptureParser) consumeEndTag(stack *[]*htmlCaptureNode) {
	p.pos += 2
	p.skipSpace()
	start := p.pos
	for p.pos < len(p.source) && isHTMLTagNameByte(p.source[p.pos]) {
		p.pos++
	}
	tag := strings.ToLower(strings.TrimSpace(p.source[start:p.pos]))
	p.pos = scanHTMLTagEnd(p.source, p.pos)
	if tag == "" {
		return
	}
	for i := len(*stack) - 1; i > 0; i-- {
		if (*stack)[i].Tag == tag {
			*stack = (*stack)[:i]
			return
		}
	}
}

func (p *tolerantHTMLCaptureParser) consumeStartTag(stack *[]*htmlCaptureNode) {
	p.pos++
	start := p.pos
	for p.pos < len(p.source) && isHTMLTagNameByte(p.source[p.pos]) {
		p.pos++
	}
	tag := strings.ToLower(p.source[start:p.pos])
	attrs := map[string]string{}
	selfClosing := false
	for p.pos < len(p.source) {
		p.skipSpace()
		if p.pos >= len(p.source) {
			break
		}
		if p.source[p.pos] == '>' {
			p.pos++
			break
		}
		if p.source[p.pos] == '/' && p.pos+1 < len(p.source) && p.source[p.pos+1] == '>' {
			selfClosing = true
			p.pos += 2
			break
		}
		nameStart := p.pos
		for p.pos < len(p.source) && isHTMLAttributeNameByte(p.source[p.pos]) {
			p.pos++
		}
		if nameStart == p.pos {
			p.pos++
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p.source[nameStart:p.pos]))
		p.skipSpace()
		value := ""
		if p.pos < len(p.source) && p.source[p.pos] == '=' {
			p.pos++
			p.skipSpace()
			value = p.consumeAttributeValue()
		}
		if name != "" {
			if _, exists := attrs[name]; !exists {
				attrs[name] = stdhtml.UnescapeString(value)
			}
		}
	}

	autoCloseHTMLCaptureStack(stack, tag)
	parent := (*stack)[len(*stack)-1]
	node := &htmlCaptureNode{Tag: tag, Attrs: attrs, Parent: parent}
	parent.Children = append(parent.Children, node)
	if selfClosing || isVoidHTMLCaptureTag(tag) {
		return
	}
	*stack = append(*stack, node)
	if isRawTextHTMLCaptureTag(tag) {
		p.consumeRawText(node, stack, tag)
	}
}

func (p *tolerantHTMLCaptureParser) consumeAttributeValue() string {
	if p.pos >= len(p.source) {
		return ""
	}
	quote := p.source[p.pos]
	if quote == '\'' || quote == '"' {
		p.pos++
		start := p.pos
		for p.pos < len(p.source) && p.source[p.pos] != quote {
			p.pos++
		}
		value := p.source[start:p.pos]
		if p.pos < len(p.source) {
			p.pos++
		}
		return value
	}
	start := p.pos
	for p.pos < len(p.source) {
		ch := p.source[p.pos]
		if isHTMLSpace(ch) || ch == '>' || (ch == '/' && p.pos+1 < len(p.source) && p.source[p.pos+1] == '>') {
			break
		}
		p.pos++
	}
	return p.source[start:p.pos]
}

func (p *tolerantHTMLCaptureParser) consumeRawText(node *htmlCaptureNode, stack *[]*htmlCaptureNode, tag string) {
	lower := strings.ToLower(p.source[p.pos:])
	needle := "</" + tag
	index := strings.Index(lower, needle)
	if index < 0 {
		node.Text.WriteString(p.source[p.pos:])
		p.pos = len(p.source)
		*stack = (*stack)[:len(*stack)-1]
		return
	}
	if index > 0 {
		raw := p.source[p.pos : p.pos+index]
		if tag == "title" || tag == "textarea" {
			raw = stdhtml.UnescapeString(raw)
		}
		node.Text.WriteString(raw)
	}
	p.pos += index
	p.consumeEndTag(stack)
}

func (p *tolerantHTMLCaptureParser) skipSpace() {
	for p.pos < len(p.source) && isHTMLSpace(p.source[p.pos]) {
		p.pos++
	}
}

func scanHTMLTagEnd(source string, pos int) int {
	quote := byte(0)
	for pos < len(source) {
		ch := source[pos]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			pos++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			pos++
			continue
		}
		pos++
		if ch == '>' {
			return pos
		}
	}
	return len(source)
}

func autoCloseHTMLCaptureStack(stack *[]*htmlCaptureNode, incoming string) {
	if len(*stack) <= 1 {
		return
	}
	current := (*stack)[len(*stack)-1].Tag
	shouldClose := false
	switch incoming {
	case "li":
		shouldClose = current == "li"
	case "dt", "dd":
		shouldClose = current == "dt" || current == "dd"
	case "p":
		shouldClose = current == "p"
	case "tr":
		shouldClose = current == "tr"
	case "td", "th":
		shouldClose = current == "td" || current == "th"
	case "option":
		shouldClose = current == "option"
	}
	if shouldClose {
		*stack = (*stack)[:len(*stack)-1]
	}
}

func isVoidHTMLCaptureTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func isRawTextHTMLCaptureTag(tag string) bool {
	switch tag {
	case "script", "style", "textarea", "title", "xmp", "iframe", "noembed", "noframes", "plaintext":
		return true
	default:
		return false
	}
}

func isHTMLTagNameByte(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == ':' || ch == '-' || ch == '_'
}

func isHTMLAttributeNameByte(ch byte) bool {
	return !isHTMLSpace(ch) && ch != '=' && ch != '>' && ch != '/' && ch != '<' && ch != '\'' && ch != '"'
}

func isHTMLSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}
