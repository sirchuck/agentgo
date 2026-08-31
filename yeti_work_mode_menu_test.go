package main

import (
	"os"
	"strings"
	"testing"
)

func TestRevision444WorkModeYetiOpensSharedMenuWithDesktopToggle(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)

	required := []string{
		`.risk-indicator.is-work-mode-menu{z-index:2190}`,
		`const workModeButtonLabel = workModeOpen ? 'DESKTOP' : 'Work-Mode';`,
		`>${workModeButtonLabel}</button>`,
		`bubble.classList.toggle('is-work-mode-menu', showWorkModeMenu);`,
		`if (workModeYeti) workModeYeti.setAttribute('aria-expanded', showWorkModeMenu ? 'true' : 'false');`,
		`function handleYetiWorkModeButton() {`,
		`if (workModeOpen) {`,
		`hideWorkModeOverlay();`,
		`handleYetiWorkModeButton();`,
		`workModeYeti.addEventListener('click', toggleYetiMenu);`,
		`workModeYeti.addEventListener('keydown', e => {`,
		`(workModeYeti && workModeYeti.contains(event.target))`,
		`id="workModeYeti" src="/assets/fc_agentgo_yetti.png" tabindex="0" title="Open Yeti menu"`,
	}
	for _, want := range required {
		if !strings.Contains(source, want) {
			t.Fatalf("revision 444 Work-Mode Yeti menu behavior missing %q", want)
		}
	}

	if strings.Contains(source, `workModeYeti.addEventListener('click', hideWorkModeOverlay)`) {
		t.Fatal("Work-Mode Yeti still exits directly instead of opening the shared Yeti menu")
	}
}
