package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkModeSpeechCodeBlocksR452TemplateHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`.work-mode-code-block.is-speech-cue`,
		`items.push({ text:'See code block.', codeBlockIndex:markerIndex });`,
		`items.push({ text:'See code block.', codeBlockIndex:nextBlockIndex++ });`,
		`function workModeReplySpeechPlan(value)`,
		`function applyWorkModeSpeechCodeCueHighlight(state, start, end)`,
		`pre,.work-mode-code-block,.work-mode-code-toolbar`,
		`function workModeKokoroSplitText(value, forcedRanges = [])`,
		`prepareWorkModeReplySpeechHighlight(session.index, session.text, session, session.speechPlan);`,
		`prepareWorkModeReplySpeechHighlight(index, spokenText, utterance, options.speechPlan || null);`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("r452 speech code-block behavior missing %q", required)
		}
	}
}

func TestWorkModeSpeechCodeBlocksR452JavaScriptPlanAndKokoroSegmentation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)

	planStart := strings.Index(source, "function stripWorkModeSpeechLineFormatting")
	planEndRel := strings.Index(source[planStart:], "function latestWorkModeAIMessageIndex")
	if planStart < 0 || planEndRel < 0 {
		t.Fatal("could not isolate r452 speech-plan JavaScript")
	}
	planJS := source[planStart : planStart+planEndRel]

	splitStart := strings.Index(source, "function workModeKokoroSplitText")
	splitEndRel := strings.Index(source[splitStart:], "function createWorkModeKokoroSession")
	if splitStart < 0 || splitEndRel < 0 {
		t.Fatal("could not isolate r452 Kokoro splitter JavaScript")
	}
	splitJS := source[splitStart : splitStart+splitEndRel]

	testJS := `
const assert = require('node:assert/strict');
function workModeHTMLToPlainText(value){ return String(value || ''); }
` + planJS + `
const raw = 'Before the example.\n\x60\x60\x60js\nconst alpha = 42;\nconsole.log(alpha);\n\x60\x60\x60\nAfter the example.';
const plan = workModeReplySpeechPlan(raw);
assert.equal(plan.text, 'Before the example.\nSee code block.\nAfter the example.');
assert.equal(plan.codeCues.length, 1);
assert.equal(plan.text.slice(plan.codeCues[0].start, plan.codeCues[0].end), 'See code block.');
assert.equal(plan.codeCues[0].blockIndex, 0);
assert.equal(plan.text.includes('const alpha'), false);
assert.equal(plan.text.includes('console.log'), false);
const tailPlan = workModeSpeechPlanFromLineItems(plan.lineItems.slice(1));
assert.equal(tailPlan.text, 'See code block.\nAfter the example.');
assert.deepEqual(tailPlan.codeCues.map(cue => cue.blockIndex), [0]);

const multiple = workModeReplySpeechPlan('Start\n\x60\x60\x60\none\n\x60\x60\x60\nMiddle\n~~~txt\ntwo\n~~~\nEnd');
assert.equal(multiple.text, 'Start\nSee code block.\nMiddle\nSee code block.\nEnd');
assert.deepEqual(multiple.codeCues.map(cue => cue.blockIndex), [0,1]);

const WORK_MODE_KOKORO_FIRST_REQUEST_TARGET_RUNES = 100;
const WORK_MODE_KOKORO_REQUEST_TARGET_RUNES = 180;
` + splitJS + `
const segments = workModeKokoroSplitText(plan.text, plan.codeCues);
const cueSegment = segments.find(segment => segment.codeBlockIndex === 0);
assert.ok(cueSegment, 'code cue should be an isolated Kokoro segment');
assert.equal(cueSegment.text, 'See code block.');
assert.equal(cueSegment.start, plan.codeCues[0].start);
assert.equal(cueSegment.end, plan.codeCues[0].end);
assert.equal(segments.some(segment => segment.text.includes('const alpha')), false);
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("r452 speech-plan JavaScript failed: %v\n%s", err, output)
	}
}

func TestWorkModeSpeechCodeBlocksR452JavaScriptHighlightFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "function workModeSpeechTokenCharacter")
	endRel := strings.Index(source[start:], "function workModeKokoroIsEngaged")
	if start < 0 || endRel < 0 {
		t.Fatal("could not isolate r452 speech-highlighter JavaScript")
	}
	highlightJS := source[start : start+endRel]

	testJS := `
const assert = require('node:assert/strict');
let workModeReplySpeechHighlightState = null;
let workModeReplySpeechHighlightSpans = [];
let agentGOTTSHighlightEnabled = true;
const WORK_MODE_SPEECH_HIGHLIGHT_NAME = 'agentgo-spoken-word';
const spoken = 'Before words.\nSee code block.\nAfter words.';
const cueStart = spoken.indexOf('See code block.');
const speechPlan = {text:spoken, codeCues:[{start:cueStart,end:cueStart+'See code block.'.length,blockIndex:0}]};
const classes = new Set();
const codeBlock = {
  nodeType:1,
  childNodes:[],
  isConnected:true,
  classList:{add(name){classes.add(name);},remove(name){classes.delete(name);},contains(name){return classes.has(name);}}
};
const beforeParent = {closest(){return null;}};
const afterParent = {closest(){return null;}};
const codeParent = {closest(selector){return selector.includes('pre') || selector.includes('.work-mode-code-block') ? codeBlock : null;}};
const beforeNode = {nodeType:3,textContent:'Before words.',parentElement:beforeParent,isConnected:true};
const codeNode = {nodeType:3,textContent:'const alpha = 42;',parentElement:codeParent,isConnected:true};
const afterNode = {nodeType:3,textContent:'After words.',parentElement:afterParent,isConnected:true};
codeBlock.childNodes = [codeNode];
const body = {nodeType:1,childNodes:[beforeNode,codeBlock,afterNode],isConnected:true};
const registry = new Map();
globalThis.document = {
  querySelector(selector){
    if (selector === '.work-mode-message-body[data-work-message-body="4"]') return body;
    if (selector === '.work-mode-code-block[data-work-code-message="4"][data-work-code-index="0"]') return codeBlock;
    return null;
  },
  querySelectorAll(selector){
    if (selector === '.work-mode-code-block.is-speech-cue' && classes.has('is-speech-cue')) return [codeBlock];
    return [];
  },
  createRange(){return {setStart(node,offset){this.startNode=node;this.startOffset=offset;},setEnd(node,offset){this.endNode=node;this.endOffset=offset;}};},
  createElement(){throw new Error('CSS Highlight API should be used');}
};
globalThis.CSS = {highlights:{set(name,value){registry.set(name,value);},delete(name){return registry.delete(name);}}};
globalThis.Highlight = class Highlight {constructor(...ranges){this.ranges=ranges;}};
` + highlightJS + `
const utterance = {};
assert.ok(prepareWorkModeReplySpeechHighlight(4, spoken, utterance, speechPlan));
assert.equal(workModeReplySpeechHighlightState.bodyTokens.some(token => token.node === codeNode), false, 'code text must not participate in word alignment');

assert.equal(updateWorkModeReplySpeechHighlight({name:'word',charIndex:spoken.indexOf('Before'),charLength:6},utterance),true);
assert.ok(registry.has(WORK_MODE_SPEECH_HIGHLIGHT_NAME));
assert.equal(classes.has('is-speech-cue'),false);

assert.equal(updateWorkModeReplySpeechHighlight({name:'word',charIndex:cueStart,charLength:3},utterance),true);
assert.equal(registry.has(WORK_MODE_SPEECH_HIGHLIGHT_NAME),false, 'word highlight should clear during the code cue');
assert.equal(classes.has('is-speech-cue'),true, 'the matching code frame should highlight during the code cue');

assert.equal(updateWorkModeReplySpeechHighlight({name:'word',charIndex:spoken.indexOf('After'),charLength:5},utterance),true);
assert.equal(classes.has('is-speech-cue'),false, 'code-frame highlight should clear when prose resumes');
assert.ok(registry.has(WORK_MODE_SPEECH_HIGHLIGHT_NAME), 'word highlighting should resume after the code block');
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("r452 code-cue highlight JavaScript failed: %v\n%s", err, output)
	}
}
