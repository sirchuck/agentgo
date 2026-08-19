package main

import (
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkModeManualScreenCaptureTemplate(t *testing.T) {
	path := filepath.Join("templates", "index.html")
	if _, err := template.ParseFiles(path); err != nil {
		t.Fatalf("parse Work Mode template: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)

	for _, required := range []string{
		`id="workModeScreenCaptureBtn"`,
		`aria-label="Capture a screen or application window"`,
		`class="work-mode-attach-source-separator" aria-hidden="true">•</span>`,
		`navigator.mediaDevices.getDisplayMedia({ video: true, audio: false })`,
		`function stopWorkModeDisplayCapture(stream)`,
		`stream.getTracks().forEach(track =>`,
		`track.stop()`,
		`video.srcObject = null`,
		`canvas.toBlob(resolve, 'image/png')`,
		`new File([blob], name, { type: 'image/png'`,
		`await addWorkModeTemporaryFile(file)`,
		`await prepareWorkModeImageAttachment(file)`,
		`if (workModeSettings.autoCompressImages !== false)`,
		`name === 'AbortError' || name === 'NotAllowedError'`,
		`button.setAttribute('aria-busy', 'true')`,
		`showToast('Temporary screenshot added.')`,
		`const workModeTemporaryAttachmentLimit = 8`,
		`ensureWorkModeTemporaryAttachmentCapacity()`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("manual screen capture template missing %q", required)
		}
	}

	popupStart := strings.Index(source, `id="workModeAttachPopup"`)
	captureButton := strings.Index(source, `id="workModeScreenCaptureBtn"`)
	fileInput := strings.Index(source, `id="workModeAttachFileInput"`)
	pasteTarget := strings.Index(source, `id="workModePasteTarget"`)
	if popupStart < 0 || captureButton < popupStart || fileInput < captureButton || pasteTarget < fileInput {
		t.Fatalf("manual capture control is not ordered camera, file input, paste target inside the Temporary Attachments popup")
	}

	if strings.Contains(source, `getDisplayMedia({ video: true, audio: true })`) {
		t.Fatal("manual screenshot capture must not request audio")
	}
}

func TestWorkModeManualScreenCaptureJavaScriptFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function stopWorkModeDisplayCapture(stream)")
	end := strings.Index(source[start:], "function toggleWorkModeAttachPopup(force)")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate manual screen capture JavaScript")
	}
	captureJS := source[start : start+end]
	testJS := `
const assert = require('node:assert/strict');
let stopped = 0;
let paused = 0;
let drawArgs = null;
let blobType = '';
let displayOptions = null;
let attachedFile = null;
let toastArgs = null;
const track = { readyState:'live', stop(){ stopped++; }, getSettings(){ return { width:1280, height:720 }; } };
const stream = { getVideoTracks(){ return [track]; }, getTracks(){ return [track]; } };
const video = { muted:false, playsInline:false, srcObject:null, videoWidth:1280, videoHeight:720, play(){ return Promise.resolve(); }, pause(){ paused++; } };
const canvas = { width:0, height:0, getContext(){ return { drawImage(...args){ drawArgs=args; } }; }, toBlob(cb, type){ blobType=type; cb({ size:321, type }); } };
Object.defineProperty(globalThis, 'navigator', { configurable:true, value:{ mediaDevices:{ async getDisplayMedia(options){ displayOptions=options; return stream; } } } });
globalThis.document = { createElement(kind){ if (kind === 'video') return video; if (kind === 'canvas') return canvas; throw new Error('unexpected element '+kind); }, getElementById(){ return button; } };
globalThis.requestAnimationFrame = callback => callback();
globalThis.setTimeout = () => 0;
globalThis.File = class MockFile { constructor(parts, name, options){ this.parts=parts; this.name=name; this.type=options.type; this.lastModified=options.lastModified; this.size=parts[0].size; } };
const button = { disabled:false, attrs:{}, setAttribute(name,value){ this.attrs[name]=value; }, removeAttribute(name){ delete this.attrs[name]; } };
function ensureWorkModeTemporaryAttachmentCapacity() {}
async function addWorkModeTemporaryFile(file){ attachedFile=file; }
function showToast(...args){ toastArgs=args; }
` + captureJS + `
(async () => {
  const file = await captureWorkModeDisplaySurfaceFile();
  assert.deepEqual(displayOptions, { video:true, audio:false });
  assert.equal(file.type, 'image/png');
  assert.match(file.name, /^AgentGO-screenshot-\d{8}-\d{6}\.png$/);
  assert.equal(canvas.width, 1280);
  assert.equal(canvas.height, 720);
  assert.equal(drawArgs[0], video);
  assert.equal(blobType, 'image/png');
  assert.equal(stopped, 1);
  assert.equal(paused, 1);
  assert.equal(video.srcObject, null);

  stopped = 0;
  paused = 0;
  await captureWorkModeScreenScreenshot();
  assert.equal(attachedFile.type, 'image/png');
  assert.deepEqual(toastArgs, ['Temporary screenshot added.']);
  assert.equal(button.disabled, false);
  assert.equal(button.attrs['aria-busy'], undefined);
  assert.equal(stopped, 1);
  assert.equal(paused, 1);

  attachedFile = null;
  toastArgs = null;
  navigator.mediaDevices.getDisplayMedia = async () => { const error = new Error('denied'); error.name='NotAllowedError'; throw error; };
  await captureWorkModeScreenScreenshot();
  assert.equal(attachedFile, null);
  assert.deepEqual(toastArgs, ['Screen capture was canceled or not permitted.', 'warning', 3000]);
  assert.equal(button.disabled, false);
  assert.equal(workModeScreenCaptureWasCanceled({name:'AbortError'}), true);
  assert.equal(workModeScreenCaptureWasCanceled({name:'NotAllowedError'}), true);
  assert.equal(workModeScreenCaptureWasCanceled({name:'NotReadableError'}), false);
})().catch(error => { console.error(error); process.exit(1); });
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("manual screen capture JavaScript flow failed: %v\n%s", err, output)
	}
}

func TestWorkModeImageCompressionPreservesTransparency(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function workModeCanvasHasTransparency(ctx, width, height)")
	if start < 0 {
		t.Fatal("transparent-image detection helper is missing")
	}
	endRel := strings.Index(source[start:], "async function prepareWorkModeImageAttachment")
	if endRel < 0 {
		t.Fatal("could not isolate Work Mode image compression JavaScript")
	}
	compressionJS := source[start : start+endRel]
	testJS := `
const assert = require('node:assert/strict');
let alpha = 0;
let encodedTypes = [];
const ctx = {
  drawImage() {},
  getImageData() { return { data: new Uint8ClampedArray([10, 20, 30, alpha, 40, 50, 60, 255]) }; }
};
const canvas = {
  width: 0,
  height: 0,
  getContext() { return ctx; },
  toBlob(callback, mimeType) {
    encodedTypes.push(mimeType);
    callback({ size: 120000, type: mimeType });
  }
};
global.document = { createElement() { return canvas; } };
async function imageElementFromDataURL() { return { naturalWidth: 2048, naturalHeight: 1208 }; }
async function readFileAsDataURL(blob) { return 'data:' + blob.type + ';base64,QQ=='; }
function workModeAttachmentBase64() { return 'QQ=='; }
` + compressionJS + `
(async () => {
  alpha = 0;
  encodedTypes = [];
  const transparent = await compressWorkModeImageDataURL('data:image/png;base64,AA==', 'rafi.png', 'transparent');
  assert.equal(transparent.mime_type, 'image/png');
  assert.equal(transparent.name, 'rafi.png');
  assert.deepEqual(encodedTypes, ['image/png']);

  alpha = 255;
  encodedTypes = [];
  const opaque = await compressWorkModeImageDataURL('data:image/png;base64,AA==', 'opaque.png', 'opaque');
  assert.equal(opaque.mime_type, 'image/jpeg');
  assert.equal(opaque.name, 'opaque.jpg');
  assert.deepEqual(encodedTypes, ['image/jpeg']);
})().catch(err => { console.error(err); process.exit(1); });
`
	path := filepath.Join(t.TempDir(), "work-mode-image-compression-test.js")
	if err := os.WriteFile(path, []byte(testJS), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("node", path).CombinedOutput()
	if err != nil {
		t.Fatalf("transparent Work Mode image compression test failed: %v\n%s", err, string(output))
	}
}
