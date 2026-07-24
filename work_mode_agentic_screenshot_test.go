package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgenticScreenshotCapabilityIsConditional(t *testing.T) {
	without := workModeAgenticJSONSchema()
	withoutProps := without["properties"].(map[string]any)
	withoutStatuses := withoutProps["agentic_status"].(map[string]any)["enum"].([]string)
	for _, status := range withoutStatuses {
		if status == workModeAgenticStatusScreenshot {
			t.Fatal("screenshot status was exposed without an approved source")
		}
	}
	with := workModeAgenticJSONSchema(true)
	withProps := with["properties"].(map[string]any)
	withStatuses := withProps["agentic_status"].(map[string]any)["enum"].([]string)
	found := false
	for _, status := range withStatuses {
		if status == workModeAgenticStatusScreenshot {
			found = true
		}
	}
	if !found {
		t.Fatal("screenshot status was not exposed with an approved source")
	}

	base := "BASE WORK MODE"
	withoutPrompt := buildWorkModeAgenticInstructions(base, workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50}, nil)
	if strings.Contains(withoutPrompt, "SCREENSHOT CAPABILITY") || strings.Contains(withoutPrompt, "run_command | screenshot") {
		t.Fatalf("screenshot capability leaked into disabled prompt:\n%s", withoutPrompt)
	}
	withPrompt := buildWorkModeAgenticInstructions(base, workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50, ScreenshotEnabled: true}, nil)
	for _, required := range []string{
		"SCREENSHOT CAPABILITY\n- If required, request a screenshot of the user's viewport by setting \"agentic_status\" to \"screenshot\" in the JSON object.",
		"run_command, screenshot, complete, or needs_user_input",
		`"agentic_status": "run_command | screenshot | complete | needs_user_input"`,
	} {
		if !strings.Contains(withPrompt, required) {
			t.Fatalf("screenshot-enabled prompt missing %q:\n%s", required, withPrompt)
		}
	}
}

func TestAgenticScreenshotResponseRequiresCapabilityAndEmptyCommand(t *testing.T) {
	raw := agenticEnvelope(workModeAgenticStatusScreenshot, emptyAgenticCommand(), "Need visual verification.", "")
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(raw, true); err != nil || len(validation) != 0 {
		t.Fatalf("enabled screenshot validation=%v err=%v", validation, err)
	}
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(raw); err != nil || len(validation) == 0 || !strings.Contains(strings.Join(validation, " "), "no screenshot source") {
		t.Fatalf("disabled screenshot validation=%v err=%v", validation, err)
	}
	bad := agenticEnvelope(workModeAgenticStatusScreenshot, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "echo", Args: []string{"bad"}, WorkingDirectory: ".", Purpose: "Must not execute",
	}, "Need visual verification.", "")
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(bad, true); err != nil || len(validation) == 0 || !strings.Contains(strings.Join(validation, " "), "command must be empty") {
		t.Fatalf("screenshot command validation=%v err=%v", validation, err)
	}
}

func TestHandleWorkModeSendAgenticScreenshotHandoff(t *testing.T) {
	var receivedInstructions string
	responseText := agenticEnvelope(workModeAgenticStatusScreenshot, emptyAgenticCommand(), "Code changed; inspect the running application.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = payload["instructions"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{
		"modelId": "1", "prompt": "Inspect the running app after changing it.",
		"agentic": map[string]any{"enabled": true, "mode": "manual", "maxRuns": 50, "screenshotEnabled": true},
	})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Agentic == nil || response.Agentic.Status != workModeAgenticStatusScreenshot || response.Agentic.Paused || !response.Agentic.AutoContinue {
		t.Fatalf("agentic screenshot handoff=%+v", response.Agentic)
	}
	if response.Agentic.Command != nil {
		t.Fatalf("screenshot unexpectedly returned executable command: %+v", response.Agentic.Command)
	}
	if response.Agentic.Workspace == nil || response.Agentic.Workspace.PendingCommand != nil || response.Agentic.Workspace.Status != agenticWorkspaceStatusActive {
		t.Fatalf("screenshot workspace=%+v", response.Agentic.Workspace)
	}
	if !strings.Contains(receivedInstructions, "SCREENSHOT CAPABILITY") || !strings.Contains(receivedInstructions, `"agentic_status" to "screenshot"`) {
		t.Fatalf("provider instructions missing screenshot protocol:\n%s", receivedInstructions)
	}
}

func TestHandleWorkModeSendRejectsScreenshotWithoutCapability(t *testing.T) {
	responseText := agenticEnvelope(workModeAgenticStatusScreenshot, emptyAgenticCommand(), "Need a screenshot.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()
	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{
		"modelId": "1", "prompt": "Continue.",
		"agentic": map[string]any{"enabled": true, "mode": "manual", "maxRuns": 50},
	})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Agentic == nil || response.Agentic.Status != workModeAgenticStatusProtocolError || !response.Agentic.Paused {
		t.Fatalf("agentic result=%+v", response.Agentic)
	}
	if !strings.Contains(strings.Join(response.Agentic.ValidationErrors, " "), "no screenshot source") {
		t.Fatalf("validation errors=%v", response.Agentic.ValidationErrors)
	}
}

func TestWorkModeAgenticScreenshotTemplate(t *testing.T) {
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
		`id="workModeTerminalCaptureBtn"`,
		`id="workModeTerminalCaptureLabel"`,
		`Allow AI to request screenshots from an app or screen.`,
		`navigator.mediaDevices.getDisplayMedia({ video:true, audio:false })`,
		`function isWorkModeAgenticCaptureAvailable()`,
		`track.addEventListener('ended', workModeAgenticCaptureEndedHandler, { once:true })`,
		`function releaseWorkModeAgenticCaptureSource()`,
		`return prepareWorkModeImageAttachment(file)`,
		`screenshotEnabled:isWorkModeAgenticCaptureAvailable()`,
		`agentic:{ enabled:true, mode, maxRuns, taskId:String(tab.agenticTaskId || ''), continuation:true, screenshotEnabled:true }`,
		`const workModeAgenticScreenshotLimit = 20`,
		`AI requested over 20 screenshots in a row and AgentGO stopped the session.`,
		`AgentGO ended the Terminal session because it can no longer access your approved screenshot source.`,
		`AgentGO fulfilled your screenshot request. The attached image is the current screenshot from the user-approved source. Inspect it, continue the task, and record any relevant visual findings in the workspace for later turns.`,
		`if (data.agentic?.status === 'screenshot')`,
		`continueWorkModeAgenticScreenshot`,
		`resetWorkModeTerminalSafetyAcknowledgement()`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("agentic screenshot template missing %q", required)
		}
	}
	captureRow := strings.Index(source, `id="workModeTerminalCapturePermission"`)
	ack := strings.Index(source, `id="workModeTerminalSafetyCheck"`)
	if captureRow < 0 || ack < 0 || captureRow > ack {
		t.Fatal("agentic screenshot selector must appear above the Terminal acknowledgment")
	}
	captureControls := source[captureRow:ack]
	if strings.Contains(captureControls, ">Change</button>") || strings.Contains(captureControls, ">Remove</button>") {
		t.Fatal("agentic screenshot permission unexpectedly includes Change or Remove controls")
	}
}

func TestWorkModeAgenticCaptureJavaScriptFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function isWorkModeAgenticCaptureAvailable()")
	end := strings.Index(source[start:], "function workModeScreenCaptureFileName")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate Agentic capture JavaScript")
	}
	captureJS := source[start : start+end]
	testJS := `
const assert = require('node:assert/strict');
let workModeAgenticCaptureStream = null;
let workModeAgenticCaptureTrack = null;
let workModeAgenticCaptureVideo = null;
let workModeAgenticCaptureEndedHandler = null;
let workModeAgenticCaptureLossHandling = false;
let workModeAgenticScreenshotStreak = 0;
let workModeTerminalSafetyAccepted = false;
const workModeAgenticScreenshotLimit = 20;
const workModeAgenticScreenshotContinuationPrompt = 'prompt';
const workModeAgenticScreenshotLimitNote = 'limit';
let stopped = 0;
let paused = 0;
let endedHandler = null;
let preparedFile = null;
let drawArgs = null;
let interruptCalls = 0;
let sendCalls = 0;
let stopCalls = 0;
let lastSendBody = null;
let owner = { id:'tab1', agenticTaskId:'task1', agenticWorkspace:{maxRuns:50}, sending:false };
const track = {
  readyState:'live',
  stop(){ stopped++; this.readyState='ended'; },
  getSettings(){ return {width:900,height:500}; },
  addEventListener(name, fn){ if(name==='ended') endedHandler=fn; },
  removeEventListener(name, fn){ if(name==='ended' && endedHandler===fn) endedHandler=null; }
};
const stream = { getVideoTracks(){ return [track]; }, getTracks(){ return [track]; } };
const video = { muted:false, playsInline:false, srcObject:null, videoWidth:900, videoHeight:500, play(){ return Promise.resolve(); }, pause(){ paused++; } };
const canvas = { width:0, height:0, getContext(){ return {drawImage(...args){drawArgs=args;}}; } };
const button = { disabled:false, attrs:{}, title:'', setAttribute(k,v){this.attrs[k]=v;}, getAttribute(k){return this.attrs[k] || null;}, removeAttribute(k){delete this.attrs[k];} };
const wrap = { selected:false, classList:{toggle(name,value){ if(name==='is-selected') wrap.selected=value; }} };
const label = { textContent:'' };
Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{async getDisplayMedia(opts){assert.deepEqual(opts,{video:true,audio:false});return stream;}}}});
globalThis.document = { createElement(kind){ if(kind==='video') return video; if(kind==='canvas') return canvas; throw new Error(kind); }, getElementById(id){ return id==='workModeTerminalCaptureBtn'?button:(id==='workModeTerminalCapturePermission'?wrap:(id==='workModeTerminalCaptureLabel'?label:null)); } };
globalThis.requestAnimationFrame = cb => cb();
globalThis.setTimeout = () => 0;
globalThis.File = class MockFile { constructor(parts,name,options){this.parts=parts;this.name=name;this.type=options.type;this.size=parts[0].size;} };
function stopWorkModeDisplayCapture(value){ if(value) value.getTracks().forEach(item=>item.stop()); }
function workModeScreenCaptureWasCanceled(){ return false; }
function appendWorkModeTerminalLine(){}
function showToast(){}
function canvasToPNGBlob(){ return Promise.resolve({size:123,type:'image/png'}); }
function workModeScreenCaptureFileName(){ return 'capture.png'; }
async function prepareWorkModeImageAttachment(file){ preparedFile=file; return {kind:'image',name:file.name,mime_type:file.type}; }
function getWorkModeTerminalOwnerTab(){ return workModeTerminalOwnerTabId === owner.id ? owner : null; }
function interruptWorkModeAgenticTask(){ interruptCalls++; return Promise.resolve(); }
function stopWorkModeAgenticAuditPolling(){}
function resetWorkModeTerminalSafetyAcknowledgement(){}
function renderWorkModeTerminalUI(){}
function updateWorkModeObserverControls(){}
function runWithWorkModeTab(){}
function appendWorkModeMessage(){}
function setWorkModeStatus(){}
function toggleWorkModeTerminalPanel(){}
function getWorkModeTab(id){ return !id || id === owner.id ? owner : null; }
async function api(path, options){
  if(path==='/api/work-mode/send'){
    sendCalls++;
    lastSendBody=JSON.parse(options.body);
    return {reply:'done',agentic:{enabled:true,status:'complete'}};
  }
  if(path==='/api/work-mode/agentic-work/interrupt'){
    stopCalls++;
    return {taskId:'task1',status:'interrupted',summary:'limit',auditRecords:[]};
  }
  throw new Error('unexpected api '+path);
}
function mergeWorkModeAgenticAuditRecords(){}
function renderWorkModeManualApproval(){}
function renderWorkModeAgenticWorkspaceReview(){}
function renderWorkModeAgenticTokenUsage(){}
function getActiveBuilderModelId(){ return 'builder1'; }
function getActiveBuilderLabel(){ return 'Builder'; }
function normalizeWorkModeTerminalMaxRuns(){ return 50; }
function serializeWorkModeAttachmentsForRequest(items){ return items.map(item=>({name:item.name,mime_type:item.mime_type})); }
function snapshotWorkModeAttachmentForMessage(item){ return item; }
function renderWorkModeConversationTabs(){}
function renderWorkModeAgenticDryRunResult(){}
function applyWorkModeResponseMemory(){}
function refreshLogs(){ return Promise.resolve(); }
function loadWorkModeFileTree(){ return Promise.resolve(); }
function isAnyWorkModeTabSending(){ return false; }
function updateWorkModeScrollButton(){}
function runWorkModeFullAutomaticCommand(){ return Promise.resolve({}); }
function runWorkModeSemiAutomaticCommand(){ return Promise.resolve({}); }
function continueWorkModeFullAgentic(){ return Promise.resolve(); }
function continueWorkModeSemiAgentic(){ return Promise.resolve(); }
let workModeTerminalOwnerTabId='';
let workModeTerminalPanelOpen=false;
let workModeTerminalMode='manual';
let workModeTerminalMaxRuns=50;
let workModeSending=false;
let workModeSendingTabId='';
` + captureJS + `
(async()=>{
  await selectWorkModeAgenticCaptureSource();
  assert.equal(isWorkModeAgenticCaptureAvailable(), true);
  assert.equal(stopped, 0, 'selected stream must remain active');
  assert.equal(typeof endedHandler, 'function');
  assert.equal(button.disabled, true);
  assert.equal(wrap.selected, true);
  assert.match(label.textContent,/selected/i);
  const attachment = await captureWorkModeAgenticScreenshotAttachment();
  assert.equal(attachment.kind,'image');
  assert.equal(preparedFile.type,'image/png');
  assert.equal(canvas.width,900);
  assert.equal(canvas.height,500);
  assert.equal(drawArgs[0],video);

  workModeTerminalOwnerTabId='tab1';
  workModeTerminalSafetyAccepted=true;
  await continueWorkModeAgenticScreenshot('tab1');
  assert.equal(sendCalls,1,'screenshot must automatically continue in every terminal mode');
  assert.equal(lastSendBody.prompt,workModeAgenticScreenshotContinuationPrompt);
  assert.equal(lastSendBody.agentic.screenshotEnabled,true);
  assert.equal(lastSendBody.temporaryAttachments.length,1);
  assert.equal(workModeAgenticScreenshotStreak,0,'non-screenshot response resets the streak');

  workModeAgenticScreenshotStreak=20;
  await continueWorkModeAgenticScreenshot('tab1');
  assert.equal(stopCalls,1,'the twenty-first consecutive request must stop automation');
  assert.equal(sendCalls,1,'the twenty-first request must not capture or call the Builder');

  await endWorkModeTerminalForLostCaptureSource();
  assert.equal(interruptCalls,1);
  assert.equal(workModeTerminalOwnerTabId,'');
  assert.equal(workModeTerminalSafetyAccepted,false);
  assert.equal(stopped,1);
  assert.equal(paused,1);
  assert.equal(isWorkModeAgenticCaptureAvailable(),false);
  assert.equal(endedHandler,null);
})().catch(error=>{console.error(error);process.exit(1);});
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Agentic capture JavaScript flow failed: %v\n%s", err, output)
	}
}
