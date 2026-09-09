package main

import (
	"os"
	"strings"
	"testing"
)

func TestR451OutfitNameModalResolvesFromEveryUserExit(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	required := []string{
		`document.getElementById('closeOutfitNameModalBtn').addEventListener('click', () => closeOutfitNameModal(null));`,
		`document.getElementById('outfitNameCancelBtn').addEventListener('click', () => closeOutfitNameModal(null));`,
		`document.getElementById('outfitNameConfirmBtn').addEventListener('click', () => {`,
		`closeOutfitNameModal(name);`,
		`if (e.target.id === 'outfitNameModal') closeOutfitNameModal(null);`,
		`document.getElementById('outfitNameInput').addEventListener('keydown', e => {`,
		`document.getElementById('outfitNameConfirmBtn').click();`,
		`if (e.key === 'Escape') closeOutfitNameModal(null);`,
	}
	for _, want := range required {
		if !strings.Contains(source, want) {
			t.Fatalf("r451 Outfit name modal wiring missing %q", want)
		}
	}
}

func TestR451OutfitCreationAllowsEmptyDraftWithClearFeedback(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	required := []string{
		`const outfit = collectCurrentOutfitState();`,
		`JSON.stringify({ name, outfit })`,
		`const hasPrompt = Object.values(outfit.wavePrompts || {}).some`,
		`const hasSelectedAI = Array.isArray(outfit.models) && outfit.models.length > 0;`,
		`if (outfit.executionMode === 'standard' && !hasPrompt && !hasSelectedAI) {`,
		`Outfit created as an empty draft. Configure its prompt and AI selections when you are ready.`,
	}
	for _, want := range required {
		if !strings.Contains(source, want) {
			t.Fatalf("r451 empty Outfit draft behavior missing %q", want)
		}
	}
	forbidden := []string{
		`if (!hasPrompt || !hasSelectedAI) return`,
		`if (!hasPrompt && !hasSelectedAI) return`,
	}
	for _, bad := range forbidden {
		if strings.Contains(source, bad) {
			t.Fatalf("r451 must allow empty standard Outfit drafts; found blocking logic %q", bad)
		}
	}
}

func TestR451OutfitNameRequiresVisibleNameFeedback(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, `showToast('Enter an Outfit name before saving.', 'warning', 2600);`) {
		t.Fatal("r451 blank Outfit names must show a visible warning instead of silently closing")
	}
}
