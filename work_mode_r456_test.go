package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR456TranscriptExportContainsWideContent(t *testing.T) {
	html := buildWorkModeTranscriptIndex(workModeTranscriptExportRequest{
		BodyHTML: `<article class="transcript-message ai"><div class="work-mode-message-body"><pre>very-wide-line</pre><table><tr><td>wide-cell</td></tr></table></div></article>`,
	})
	for _, want := range []string{
		`.transcript-messages{display:grid;gap:16px;min-width:0;max-width:100%}`,
		`.transcript-message{min-width:0;max-width:100%`,
		`.transcript-message table{display:block;width:100%;max-width:100%;min-width:0;overflow-x:auto`,
		`pre{width:100%;max-width:100%;min-width:0;overflow-x:auto`,
		`.work-mode-message-body,.work-mode-ai-warnings,.work-mode-file-outputs,.work-mode-agent-details,.work-mode-repair-details,.transcript-review-section{min-width:0;max-width:100%}`,
		`.work-mode-file-output-head{display:flex;justify-content:space-between;gap:12px;min-width:0;max-width:100%;flex-wrap:wrap`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("r456 transcript containment CSS missing %q", want)
		}
	}
}

func TestR456WorkModeDropZoneIsDragOnly(t *testing.T) {
	source, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		`id="workModeFileDropZone"`,
		`Drop files to add to project`,
		`.work-mode-sidebar.is-file-dragging .work-mode-file-dropzone`,
		`workModeDropOverlay.addEventListener('dragenter'`,
		`workModeDropOverlay.addEventListener('dragover'`,
		`workModeDropOverlay.addEventListener('drop'`,
		`importWorkModeDroppedFiles(transfer)`,
		`form.append('overwrite', overwrite ? 'true' : 'false')`,
		`title:'File Already Exists'`,
		`confirmLabel:'Replace'`,
		`cancelLabel:'Skip'`,
		`Drag-and-drop currently supports text/code files only. Use Import for ZIP or other file types.`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("r456 Work Mode drag/drop UI missing %q", want)
		}
	}
}

func r456MultipartUploadRequest(t *testing.T, filename, content, overwrite string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if overwrite != "" {
		if err := writer.WriteField("overwrite", overwrite); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.WriteField("includeGitMetadata", "false"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/project/import/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestR456ProjectImportUploadCanRejectAndThenReplaceConflict(t *testing.T) {
	app := newActiveSessionTestApp(t)
	projectRoot, err := app.projectWorkRoot("Demo")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projectRoot, "notes.md")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	reject := httptest.NewRecorder()
	app.handleProjectImportUpload(reject, r456MultipartUploadRequest(t, "notes.md", "replacement", "false"))
	if reject.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", reject.Code, reject.Body.String())
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "original" {
		t.Fatalf("conflict changed file: content=%q err=%v", got, err)
	}

	replace := httptest.NewRecorder()
	app.handleProjectImportUpload(replace, r456MultipartUploadRequest(t, "notes.md", "replacement", "true"))
	if replace.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body = %s", replace.Code, replace.Body.String())
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "replacement" {
		t.Fatalf("replace did not update file: content=%q err=%v", got, err)
	}
}
