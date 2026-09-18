package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestR458MixedHTMLMarkdownRendererGuards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		"function normalizeAgentGOReplyMarkup(value)",
		"function workModeReplyHasMarkdown(value)",
		"function renderWorkModeInlineMarkdown(value)",
		"function renderWorkModeMarkdownFragment(value)",
		"function protectWorkModeMarkdownCodeForHTMLParse(value)",
		"const raw = normalizeAgentGOReplyMarkup(value);",
		"template.innerHTML = protectWorkModeMarkdownCodeForHTMLParse(raw);",
		"const allowed = new Set(['h3','h4','p','ul','ol','li','strong','em','u','code','pre','br','img','span','a','blockquote','hr','table','thead','tbody','tfoot','tr','th','td']);",
		"const dropped = new Set(['script','style','iframe','object','embed','link','meta','base','form','input','select','textarea','button','canvas','svg','video','audio']);",
		"const href = sanitizeAgentGOHref(node.getAttribute('href') || '');",
		"el.setAttribute('rel', 'noopener noreferrer');",
		"const hasMarkdown = allowHTML && workModeReplyHasMarkdown(text);",
		"const reply = (workModeReplyHasAllowedHTML(reviewReply) || workModeReplyHasMarkdown(reviewReply)) ? sanitizeAgentGOReplyHTML(reviewReply) : formatWorkModeMessageText(normalizeAgentGOReplyMarkup(reviewReply));",
		"((hasHTML || hasMarkdown) ? sanitizeAgentGOReplyHTML(text) : formatWorkModeMessageText(normalizeAgentGOReplyMarkup(text)))",
		"let raw = normalizeAgentGOReplyMarkup(value);",
		"raw = raw.replace(/~~~[\\s\\S]*?~~~/g, ' ');",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("r458 mixed response renderer missing %q", required)
		}
	}
}

func TestR458VisualSVGArtifactCleanupGuards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		"function workModeRecentUserHasVisualInput()",
		"function stripWorkModePhantomSVGArtifactLines(value, visualInput)",
		"return !/^svg(?:\\s*svg)+$/i.test(trimmed);",
		"if (fence) return true;",
		"const source = role === 'ai' ? stripWorkModePhantomSVGArtifactLines(text, workModeRecentUserHasVisualInput()) : String(text || '');",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("r458 visual artifact cleanup missing %q", required)
		}
	}
}

func TestR458MixedMarkupPureHelpers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	slice := func(startMarker, endMarker string) string {
		start := strings.Index(source, startMarker)
		if start < 0 {
			t.Fatalf("missing JS start marker %q", startMarker)
		}
		endRel := strings.Index(source[start:], endMarker)
		if endRel < 0 {
			t.Fatalf("missing JS end marker %q after %q", endMarker, startMarker)
		}
		return source[start : start+endRel]
	}

	checks := strings.Join([]string{
		"function assert(condition, message) { if (!condition) { console.error(message); process.exit(1); } }",
		"const encoded = '&lt;p&gt;Hi &lt;strong&gt;Claude&lt;/strong&gt;&lt;/p&gt;';",
		"assert(normalizeAgentGOReplyMarkup(encoded) === '<p>Hi <strong>Claude</strong></p>', 'encoded HTML normalization failed');",
		`const escaped = '\\<h3 style="color:#2b6cb0">Title\\</h3>';`,
		`assert(normalizeAgentGOReplyMarkup(escaped) === '<h3 style="color:#2b6cb0">Title</h3>', 'backslash HTML normalization failed');`,
		"assert(workModeReplyHasAllowedHTML(encoded), 'encoded safe HTML was not detected');",
		"assert(workModeReplyHasMarkdown('**TEMPimage.jpg**'), 'markdown was not detected');",
		`const md = renderWorkModeMarkdownFragment('[image.jpg](http://host/path?id=abc&content=payload)\n\n**TEMPimage.jpg**');`,
		`assert(md.includes('href="http://host/path?id=abc&amp;content=payload"'), 'markdown link rendering failed');`,
		`assert(md.includes('<strong>TEMPimage.jpg</strong>'), 'markdown bold rendering failed');`,
		`const fence = String.fromCharCode(96, 96, 96);`,
		`const protectedCode = protectWorkModeMarkdownCodeForHTMLParse(fence + 'html\n<script>alert(1)</script>\n' + fence);`,
		`assert(protectedCode.includes('&lt;script&gt;alert(1)&lt;/script&gt;'), 'fenced HTML code was not protected before DOM parsing');`,
		`assert(stripWorkModePhantomSVGArtifactLines('before\nsvgsvg\nsvg svg\nafter', true) === 'before\nafter', 'phantom svg artifact lines were not removed');`,
		`assert(stripWorkModePhantomSVGArtifactLines(fence + '\nsvgsvg\n' + fence, true).includes('svgsvg'), 'svg text inside a code fence was removed');`,
		"console.log('PASS');",
	}, "\n")

	js := slice("function escapeHtml(value) {", "function renderKnowledgeInlineMarkdown(value) {") +
		slice("const AGENTGO_REPLY_NORMALIZABLE_TAGS", "function sanitizeAgentGOImageSrc(value) {") +
		slice("function renderWorkModeInlineMarkdown(value) {", "function sanitizeAgentGOReplyHTML(value) {") +
		slice("function stripWorkModePhantomSVGArtifactLines(value, visualInput) {", "function workModeMessageNeedsCollapseFallback(value) {") + checks

	path := filepath.Join(t.TempDir(), "r458-markup.js")
	if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("node", path).CombinedOutput(); err != nil {
		t.Fatalf("r458 markup helper regression: %v\n%s", err, output)
	}
}
