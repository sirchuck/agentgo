package main

import (
	"os"
	"strings"
	"testing"
)

func TestR460ReadmeNeedHelpPanelContent(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)

	heading := "# FrostCandy AgentGO"
	start := "<!-- agentgo-help-start -->"
	end := "<!-- agentgo-help-end -->"
	intro := "AgentGO is a local multi-model AI workspace"

	headingAt := strings.Index(readme, heading)
	startAt := strings.Index(readme, start)
	endAt := strings.Index(readme, end)
	introAt := strings.Index(readme, intro)
	if headingAt < 0 || startAt < 0 || endAt < 0 || introAt < 0 {
		t.Fatalf("README help panel anchors missing: heading=%d start=%d end=%d intro=%d", headingAt, startAt, endAt, introAt)
	}
	if !(headingAt < startAt && startAt < endAt && endAt < introAt) {
		t.Fatalf("README help panel should appear directly after the main heading and before the normal introduction")
	}

	required := []string{
		"## NEED HELP?",
		"Copy the line below into ChatGPT:",
		"ChatGPT, I need help using AgentGO. Please read the AgentGO GitHub repository at",
		"https://github.com/sirchuck/agentgo",
		"Troubleshooting your installed copy? Zip your full AgentGO project and attach it to ChatGPT instead",
		"**You can ask things like:**",
		"How do I add my Claude model?",
		"How do I enter Work Mode?",
		"How do I use the Work Mode memory feature?",
		"How do I use Kokoro voice?",
		"How do I use DeadDrop?",
		"How do I use an API key environment variable instead of entering the API key directly?",
		"How do I add or reset a password in my config file?",
	}
	for _, want := range required {
		if !strings.Contains(readme, want) {
			t.Fatalf("README help panel missing %q", want)
		}
	}
}

func TestR460KnowledgeRendererStylesHelpPanel(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	required := []string{
		".knowledge-help-card {",
		".knowledge-help-card blockquote {",
		"trimmed === '<!-- agentgo-help-start -->'",
		"trimmed === '<!-- agentgo-help-end -->'",
		"<section class=\"knowledge-help-card\">",
		"line.match(/^\\s*>\\s?(.*)$/)",
		"<blockquote>${renderKnowledgeInlineMarkdown(quoteLines.join(' '))}</blockquote>",
	}
	for _, want := range required {
		if !strings.Contains(html, want) {
			t.Fatalf("r460 knowledge help renderer missing %q", want)
		}
	}
}
