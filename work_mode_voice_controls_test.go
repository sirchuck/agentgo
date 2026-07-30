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

func TestVoiceCommandConfigDefaultsValidationAndPersistence(t *testing.T) {
	cfg := AppConfig{}
	applyConfigDefaults(&cfg)
	if cfg.VoiceStartCommand != defaultVoiceStartCommand || cfg.VoiceSendCommand != defaultVoiceSendCommand || cfg.VoiceRepeatCommand != defaultVoiceRepeatCommand || cfg.VoiceReadCommand != defaultVoiceReadCommand {
		t.Fatalf("voice defaults = %q / %q / %q / %q", cfg.VoiceStartCommand, cfg.VoiceSendCommand, cfg.VoiceRepeatCommand, cfg.VoiceReadCommand)
	}
	if got := normalizeVoiceCommandForMatch(" AgentGO   Start! "); got != "agentgostart" {
		t.Fatalf("normalized voice command = %q", got)
	}
	if _, _, _, _, err := validateVoiceCommands("AgentGO Start", "agentgo-start", "AgentGO Repeat", "AgentGO Read"); err == nil {
		t.Fatal("equivalent normalized commands were accepted")
	}
	if _, _, _, _, err := validateVoiceCommands("Go", "Send", "Repeat", "Read"); err == nil {
		t.Fatal("voice command shorter than 4 characters was accepted")
	}
	if start, send, repeat, read, err := validateVoiceCommands("  Start  ", " Send ", " Repeat ", " Read "); err != nil || start != "Start" || send != "Send" || repeat != "Repeat" || read != "Read" {
		t.Fatalf("trimmed voice commands = %q / %q / %q / %q, err = %v", start, send, repeat, read, err)
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	initial := `{"agentgo_file":"config","file_version":1,"bind_host":"0.0.0.0","http_port":5226,"https_port":5227,"tls_cert_file":"","tls_key_file":"","work_root":"work","tts_voice":"Saved Voice","max_response_history":50,"risk_mode_max_iterations":10,"outfit_run_retention":50,"auto_merge_single_builder_waves":true,"prompt_version":1,"wiretap":{"max_wiretap_entries":200,"default_runtime_slice_entries":75,"max_runtime_slice_entries":150}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	appCfg := AppConfig{WorkRoot: "work", TTSVoice: "Saved Voice"}
	applyConfigDefaults(&appCfg)
	app := &App{cfg: appCfg, configPath: configPath}

	req := httptest.NewRequest(http.MethodPost, "/api/config/voice-commands", strings.NewReader(`{"start_command":"Computer Listen","send_command":"Computer Send","repeat_command":"Computer Repeat","read_command":"Computer Read"}`))
	rr := httptest.NewRecorder()
	app.handleVoiceCommandConfig(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["start_command"] != "Computer Listen" || response["send_command"] != "Computer Send" || response["repeat_command"] != "Computer Repeat" || response["read_command"] != "Computer Read" {
		t.Fatalf("save response = %#v", response)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["voice_start_command"] != "Computer Listen" || saved["voice_send_command"] != "Computer Send" || saved["voice_repeat_command"] != "Computer Repeat" || saved["voice_read_command"] != "Computer Read" {
		t.Fatalf("saved commands = %#v / %#v / %#v / %#v", saved["voice_start_command"], saved["voice_send_command"], saved["voice_repeat_command"], saved["voice_read_command"])
	}
	if saved["tts_voice"] != "Saved Voice" {
		t.Fatalf("unrelated tts_voice changed to %#v", saved["tts_voice"])
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/config/voice-commands", strings.NewReader(`{"start_command":"Same Word","send_command":"same-word","repeat_command":"Computer Repeat","read_command":"Computer Read"}`))
	badRR := httptest.NewRecorder()
	app.handleVoiceCommandConfig(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("equivalent command status = %d, want 400", badRR.Code)
	}

	shortReq := httptest.NewRequest(http.MethodPost, "/api/config/voice-commands", strings.NewReader(`{"start_command":"Go","send_command":"Computer Send","repeat_command":"Computer Repeat","read_command":"Computer Read"}`))
	shortRR := httptest.NewRecorder()
	app.handleVoiceCommandConfig(shortRR, shortReq)
	if shortRR.Code != http.StatusBadRequest {
		t.Fatalf("short command status = %d, want 400", shortRR.Code)
	}
	if app.cfg.VoiceStartCommand != "Computer Listen" {
		t.Fatalf("short command overwrote the last valid command: %q", app.cfg.VoiceStartCommand)
	}
}

func TestWorkModeVoiceControlsTemplate(t *testing.T) {
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
		`workModeVoiceStartCommand = String({{.VoiceStartCommandJSON}}`,
		`workModeVoiceSendCommand = String({{.VoiceSendCommandJSON}}`,
		`workModeVoiceRepeatCommand = String({{.VoiceRepeatCommandJSON}}`,
		`workModeVoiceReadCommand = String({{.VoiceReadCommandJSON}}`,
		`Start Recording Prompt`,
		`Send Recording Prompt`,
		`Repeat Response Prompt`,
		`Read Lines Prompt`,
		`id="workModeVoiceReadCommandExample"`,
		`Example Say: “${escapeHtml(workModeVoiceReadCommand)} 4 to 9”`,
		`function updateWorkModeVoiceReadCommandExample(value)`,
		"example.textContent = `Example Say: “${command} 4 to 9”`;",
		`Array.from(command).length < 4`,
		`Voice commands must be at least 4 characters.`,
		`id="workModeVoiceStartCommandInput"`,
		`id="workModeVoiceSendCommandInput"`,
		`id="workModeVoiceRepeatCommandInput"`,
		`id="workModeVoiceReadCommandInput"`,
		`/api/config/voice-commands`,
		`input.addEventListener('blur'`,
		`readCommandInput.addEventListener('input'`,
		`event.key !== 'Enter'`,
		`workModeMicBtn.addEventListener('contextmenu', toggleWorkModeHandsFreeSpeechInput)`,
		`workModeAutoReadBtn.addEventListener('contextmenu'`,
		`btn.addEventListener('contextmenu', toggleSpeaker)`,
		`recognition.continuous = true`,
		`recognition.interimResults = false`,
		`workModeSpeechMode === 'dictation') stopWorkModeSpeechInput()`,
		`classList.toggle('is-hands-free'`,
		`.work-mode-icon-btn.is-listening{background:rgba(255,62,78,.40)`,
		`.work-mode-icon-btn.is-hands-free{background:rgba(74,163,255,.22)`,
		`findWorkModeVoiceCommand(workModeHandsFreeBuffer, workModeVoiceStartCommand)`,
		`findWorkModeVoiceCommand(workModeHandsFreeBuffer, workModeVoiceSendCommand)`,
		`findWorkModeVoiceCommand(value, workModeVoiceRepeatCommand)`,
		`findWorkModeVoiceCommand(value, workModeVoiceReadCommand)`,
		`AgentGO couldn't understand your request.`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("voice controls template missing %q", required)
		}
	}
	if strings.Contains(source, "Listening... speak now.") {
		t.Fatal("speech input still overwrites the Work Mode status area")
	}
	memoryDefault := strings.Index(source, `AgentGO Memory Default`)
	voiceCommands := strings.Index(source, `work-mode-voice-commands-title">Voice Commands`)
	if memoryDefault < 0 || voiceCommands < memoryDefault {
		t.Fatal("Voice Commands settings are not below AgentGO Memory Default")
	}
	speechStart := strings.Index(source, "function setWorkModePromptText")
	speechEnd := strings.Index(source[speechStart:], "function autosizeWorkModePrompt")
	if speechStart < 0 || speechEnd < 0 {
		t.Fatal("could not isolate Work Mode speech functions")
	}
	if strings.Contains(source[speechStart:speechStart+speechEnd], "setWorkModeStatus(") {
		t.Fatal("microphone speech functions still overwrite the Work Mode status area")
	}
}

func TestWorkModeHandsFreeVoiceJavaScriptFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function setWorkModePromptText")
	end := strings.Index(source[start:], "function autosizeWorkModePrompt")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate Work Mode voice JavaScript")
	}
	voiceJS := source[start : start+end]
	testJS := `
const assert = require('node:assert/strict');
let workModeRecognition = null;
let workModeSpeechActive = false;
let workModeSpeechMode = 'off';
let workModeSpeechStopRequested = false;
let workModeHandsFreeState = 'waiting';
let workModeHandsFreeBuffer = '';
let workModeHandsFreePromptBase = '';
let workModePromptDraft = '';
let workModeVoiceStartCommand = 'AgentGO Start';
let workModeVoiceSendCommand = 'AgentGO Send';
let workModeVoiceRepeatCommand = 'AgentGO Repeat';
let workModeVoiceReadCommand = 'AgentGO Read';
let workModeOpen = true;
let sendCalls = 0;
let toastCalls = [];
let speakCalls = [];
let workModeMessages = [
  {role:'ai',text:'# Heading\n- First line\nSecond **line**\nThird line'},
  {role:'user',text:'later prompt'}
];
const input = {value:'Existing text', focus(){}, style:{}, scrollHeight:40};
const mic = {classes:{}, classList:{toggle(name,value){mic.classes[name]=value;}}, setAttribute(){}, title:''};
const autoRead = {disabled:false,classList:{toggle(){}},setAttribute(){},title:''};
globalThis.document = {getElementById(id){if(id==='workModePromptInput')return input;if(id==='workModeMicBtn')return mic;if(id==='workModeAutoReadBtn')return autoRead;return null;}};
Object.defineProperty(globalThis,'navigator',{configurable:true,value:{language:'en-US'}});
function autosizeWorkModePrompt(){}
function updateWorkModeSpeechUI(){}
function showToast(...args){toastCalls.push(args);}
function showErrorToast(message){throw new Error(message);}
function workModeHTMLToPlainText(value){return String(value || '');}
function speakWorkModeReplyText(index,text,options){speakCalls.push({index,text,options});return true;}
async function sendWorkModePrompt(){sendCalls++;}
function getWorkModeSpeechRecognitionCtor(){return MockRecognition;}
class MockRecognition {
  constructor(){this.started=0;this.stopped=0;}
  start(){this.started++;if(this.onstart)this.onstart();}
  stop(){this.stopped++;if(this.onend)this.onend();}
}
` + voiceJS + `
(async()=>{
  assert.equal(normalizeWorkModeVoiceCommand(' AgentGO   Start! '),'agentgostart');
  assert.deepEqual(findWorkModeVoiceCommand('noise AgentGO    Start now','agentgo start'),{start:6,end:22});
  assert.deepEqual(parseWorkModeVoiceLineRange('lines 1 until 6'),{start:1,end:6});
  assert.equal(parseWorkModeVoiceLineRange('A to B'),null);
  assert.equal(parseWorkModeVoiceLineRange('10 through 1'),null);
  assert.deepEqual(workModeReplyLogicalLines(workModeMessages[0].text),['Heading','First line','Second line','Third line']);
  appendWorkModeTranscript('more words');
  assert.equal(input.value,'Existing text more words');

  processWorkModeHandsFreeTranscript('AgentGO Repeat');
  assert.equal(speakCalls.length,1);
  assert.equal(speakCalls[0].text,'Heading\nFirst line\nSecond line\nThird line');
  processWorkModeHandsFreeTranscript('AgentGO Read lines 2 until 3');
  assert.equal(speakCalls.length,2);
  assert.equal(speakCalls[1].text,'First line\nSecond line');
  processWorkModeHandsFreeTranscript('AgentGO Read A to B');
  assert.deepEqual(toastCalls.at(-1),["AgentGO couldn't understand your request.",'warning',3600]);
  assert.equal(speakCalls.at(-1).text,"AgentGO couldn't understand your request.");

  input.value='Existing text';
  workModePromptDraft=input.value;
  processWorkModeHandsFreeTranscript('unrelated AgentGO');
  assert.equal(workModeHandsFreeState,'waiting');
  processWorkModeHandsFreeTranscript('Start move the train');
  assert.equal(workModeHandsFreeState,'recording');
  assert.equal(input.value,'Existing text move the train');
  processWorkModeHandsFreeTranscript('closer AgentGO');
  assert.equal(input.value,'Existing text move the train closer AgentGO');
  processWorkModeHandsFreeTranscript('Read 1 to 2');
  assert.equal(input.value,'Existing text move the train closer');
  assert.equal(workModeHandsFreeState,'recording');
  processWorkModeHandsFreeTranscript('AgentGO Send');
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(sendCalls,1);
  assert.equal(input.value,'Existing text move the train closer');
  assert.equal(workModeHandsFreeState,'waiting');
  assert.equal(workModeHandsFreeBuffer,'');

  const finalOnly=workModeRecognitionFinalTranscripts({resultIndex:1,results:[{isFinal:true,0:{transcript:'old'}},{isFinal:true,0:{transcript:'new'}},{isFinal:false,0:{transcript:'interim'}}]});
  assert.deepEqual(finalOnly,['new']);

  input.value='Dictated';
  processWorkModeDictationTranscript('AgentGO Read 3 to 4');
  assert.equal(input.value,'Dictated');
  assert.equal(speakCalls.at(-1).text,'Second line\nThird line');

  startWorkModeSpeechMode('dictation');
  assert.equal(workModeSpeechMode,'dictation');
  assert.equal(workModeSpeechActive,true);
  assert.equal(workModeRecognition.continuous,true);
  assert.equal(workModeRecognition.interimResults,false);
  startWorkModeSpeechMode('handsfree');
  assert.equal(workModeSpeechMode,'off','clicking while active must toggle the microphone off');
  assert.equal(workModeSpeechActive,false);
})().catch(error=>{console.error(error);process.exit(1);});
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hands-free voice JavaScript flow failed: %v\n%s", err, output)
	}
}

func TestWorkModeStatusDismissAndReadExampleJavaScriptFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	statusStart := strings.Index(source, "function renderWorkModeStatusMessage")
	statusEnd := strings.Index(source[statusStart:], "function restoreWorkModeReplyScrolling")
	readStart := strings.Index(source, "function updateWorkModeVoiceReadCommandExample")
	readEnd := strings.Index(source[readStart:], "async function saveWorkModeVoiceCommands")
	if statusStart < 0 || statusEnd < 0 || readStart < 0 || readEnd < 0 {
		t.Fatal("could not isolate Work Mode status or read-example JavaScript")
	}
	statusJS := source[statusStart : statusStart+statusEnd]
	readJS := source[readStart : readStart+readEnd]
	testJS := `
const assert = require('node:assert/strict');
const tab = {statusText:'old', statusHTML:''};
const status = {innerHTML:''};
const example = {textContent:''};
let settingsUpdates = 0;
let workModeBackgroundTabMutation = false;
let workModeVoiceReadCommand = 'AgentGO Read';
function getWorkModeTab(){return tab;}
function escapeHtml(value){return String(value || '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;').replaceAll("'",'&#39;');}
function updateWorkModeSettingsStatus(){settingsUpdates++;status.innerHTML='OPTIONS';}
globalThis.document = {getElementById(id){if(id==='workModePromptStatus')return status;if(id==='workModeVoiceReadCommandExample')return example;return null;}};
` + statusJS + readJS + `
setWorkModeStatus('<waiting>');
assert.match(status.innerHTML,/data-work-status-dismiss/);
assert.match(status.innerHTML,/&lt;waiting&gt;/);
assert.ok(status.innerHTML.indexOf('data-work-status-dismiss') < status.innerHTML.indexOf('work-mode-status-copy'));
assert.equal(tab.statusText,'');
assert.equal(tab.statusHTML,status.innerHTML);
dismissWorkModeStatus();
assert.equal(settingsUpdates,1);
assert.equal(status.innerHTML,'OPTIONS');
setWorkModeStatusHTML('<b>options</b>',{dismissible:false});
assert.equal(status.innerHTML,'<b>options</b>');
assert.doesNotMatch(status.innerHTML,/data-work-status-dismiss/);
updateWorkModeVoiceReadCommandExample(' Custom Read ');
assert.equal(example.textContent,'Example Say: “Custom Read 4 to 9”');
updateWorkModeVoiceReadCommandExample('');
assert.equal(example.textContent,'Example Say: “AgentGO Read 4 to 9”');
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Work Mode status/read-example JavaScript flow failed: %v\n%s", err, output)
	}
}
