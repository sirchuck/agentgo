package main

import (
	"os"
	"strings"
	"testing"
)

func TestR457ToastUsesGlowInsteadOfSeverityBorder(t *testing.T) {
	source, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	for _, want := range []string{
		`.toast {`,
		`border: 0;`,
		`font-weight: 600;`,
		`line-height: 1.38;`,
		`--toast-glow: rgba(157,182,215,0.52);`,
		`box-shadow: 0 0 18px var(--toast-glow), 0 16px 36px rgba(0,0,0,0.42), inset 0 1px 0 rgba(255,255,255,0.08);`,
		`.toast[data-variant="warning"] {`,
		`--toast-glow: rgba(244,183,64,0.72);`,
		`.toast[data-variant="error"] {`,
		`--toast-glow: rgba(255,91,103,0.76);`,
		`.toast[data-variant="success"] {`,
		`--toast-glow: rgba(73,201,132,0.62);`,
		`.toast[data-variant="info"] {`,
		`--toast-glow: rgba(88,184,255,0.62);`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("r457 toast styling missing %q", want)
		}
	}

	for _, old := range []string{
		`border-top-width: 4px;`,
		`border-top-color: #9db6d7;`,
		`border-top-color: #f4b740;`,
		`border-top-color: #ff5b67;`,
	} {
		if strings.Contains(text, old) {
			t.Fatalf("r457 toast styling still contains retired top-border rule %q", old)
		}
	}
}

func TestR457ToastVariantsUseSharedToastRouter(t *testing.T) {
	source, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	for _, want := range []string{
		`showToast(message || 'Something went wrong.', 'error');`,
		`showToast('Copied code.', 'success', 1600);`,
		`showToast('Media generation outputs are saved directly into projectwork; no merge step is needed.', 'info', 3200);`,
		`if (variant && variant !== 'default') toast.dataset.variant = variant;`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("r457 toast variant routing missing %q", want)
		}
	}
}
