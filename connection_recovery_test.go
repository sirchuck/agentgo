package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentGOConnectionRecoveryTemplateHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`const AGENTGO_RECOVERY_BACKOFF_STEPS_MS = [5000, 10000, 30000, 60000];`,
		`const AGENTGO_RECONNECT_HEADER = 'X-AgentGO-Reconnect-Attempt';`,
		`function scheduleAgentGORecoveryProbe`,
		`async function runAgentGORecoveryProbe`,
		`nativeAgentGOFetch('/api/session/active'`,
		`payload.match === true && payload.status === 'matched'`,
		`completeAgentGOConnectionRecovery('/api/session/active')`,
		`resetAgentGOPollingLanesAfterRecovery();`,
		`if (agentGOConnectionState.disconnected && !headers.has(AGENTGO_RECONNECT_HEADER))`,
		`Work Mode send blocked locally because another tab or request is still marked as sending.`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("connection recovery template missing %q", required)
		}
	}
}

func TestAgentGOConnectionRecoveryJavaScriptFlow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "const POLL_REQUEST_TIMEOUT_MS = 15000;")
	endMarker := "\nfunction getOutfitById(id) {"
	endRelative := strings.Index(source[start:], endMarker)
	if start < 0 || endRelative < 0 {
		t.Fatal("could not isolate AgentGO connection recovery JavaScript")
	}
	connectionJS := source[start : start+endRelative]

	testJS := `
const assert = require('node:assert/strict');
let fetchQueue = [];
let fetchCalls = [];
let timerTop = 0;
const timers = new Map();
globalThis.setTimeout = (fn, delay) => { const id = ++timerTop; timers.set(id, {fn, delay}); return id; };
globalThis.clearTimeout = id => timers.delete(id);
function response(payload, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 409 ? 'Conflict' : 'OK',
    async json(){ return payload; },
    async text(){ return JSON.stringify(payload); },
    clone(){ return response(payload, status); }
  };
}
const elements = new Map();
function element(){ return {textContent:'', classes:new Set(), classList:{add(v){this.owner.classes.add(v)},remove(v){this.owner.classes.delete(v)}},setAttribute(){},focus(){}}; }
for (const id of ['agentGOConnectionModal','agentGOConnectionTitle','agentGOConnectionCopy','agentGOConnectionStatus','agentGOConnectionDismissBtn']) {
  const el = element(); el.classList.owner = el; elements.set(id, el);
}
globalThis.window = {
  location: {href:'http://agentgo.local/', origin:'http://agentgo.local', pathname:'/'},
  fetch: async (url, options = {}) => {
    fetchCalls.push({url, options});
    if (!fetchQueue.length) throw new Error('no queued response');
    const next = fetchQueue.shift();
    if (next instanceof Error) throw next;
    return next;
  }
};
globalThis.document = {
  visibilityState:'visible',
  addEventListener(){},
  getElementById(id){ return elements.get(id) || null; }
};
globalThis.requestAnimationFrame = fn => fn();
let agentGOActiveSessionId = 'matching-session';
let agentGOSessionClaimState = {stale:false,promptOpen:false,dismissed:false,activating:false};
let workModeURLReviewState = null;
let observerStarts = 0;
let observerStops = 0;
let takeoverPrompts = 0;
function stopWorkModeObserverSessionPolling(){observerStops++;}
function clearWorkModeWaitingNotice(){}
function startWorkModeObserverSessionPolling(){observerStarts++;}
function isAnyWorkModeTabSending(){return false;}
function isWorkModeObserverReviewActive(){return false;}
async function promptAgentGOSessionTakeover(){takeoverPrompts++;}
` + connectionJS + `
(async()=>{
  getPollingLaneState('logs').nextAt = 9999999999999;
  showAgentGOConnectionLost('unreachable');
  assert.equal(agentGOConnectionState.disconnected, true);
  assert.ok(agentGOConnectionState.recoveryTimer, 'disconnect should schedule a recovery probe');

  fetchQueue.push(response({active:true,match:true,status:'matched'}));
  const recovered = await runAgentGORecoveryProbe();
  assert.equal(recovered, true);
  assert.equal(agentGOConnectionState.disconnected, false);
  assert.equal(agentGOConnectionState.recoveryTimer, null);
  assert.equal(getPollingLaneState('logs').nextAt, 0, 'recovery should release polling lanes');

  let pollRuns = 0;
  await runPollingLane('logs', async()=>{pollRuns++; return 'ok';});
  assert.equal(pollRuns, 1, 'polling should run after recovery');

  showAgentGOConnectionLost('unreachable');
  fetchQueue.push(response({active:true,match:false,status:'mismatch',session:{active_project:'Demo'}}));
  const mismatched = await runAgentGORecoveryProbe();
  await Promise.resolve();
  assert.equal(mismatched, false);
  assert.equal(agentGOConnectionState.disconnected, true);
  assert.equal(agentGOSessionClaimState.stale, true);
  assert.equal(takeoverPrompts, 1);
  assert.ok(agentGOConnectionState.recoveryTimer, 'mismatch should keep quiet probes scheduled');

  fetchQueue.push(response({error:'session_mismatch',message:'stale'}, 409));
  const conflictResponse = await agentGOFetch('/api/work-mode/send', {method:'POST'});
  assert.equal(conflictResponse.status, 409);
  assert.equal(agentGOConnectionState.disconnected, true, '409 mismatch must not falsely mark the session recovered');
  const conflictHeaders = fetchCalls.at(-1).options.headers;
  assert.equal(conflictHeaders.get('X-AgentGO-Session-ID'), 'matching-session');
  assert.equal(conflictHeaders.get('X-AgentGO-Reconnect-Attempt'), '1');

  fetchQueue.push(response({reply:'ok'}));
  const successResponse = await agentGOFetch('/api/work-mode/send', {method:'POST'});
  assert.equal(successResponse.status, 200);
  assert.equal(agentGOConnectionState.disconnected, false, 'successful matched prompt should recover the browser state');
  assert.equal(agentGOConnectionState.recoveryTimer, null);
})().catch(error=>{console.error(error);process.exit(1);});
`
	cmd := exec.Command("node", "-e", testJS)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("connection recovery JavaScript flow failed: %v\n%s", err, output)
	}
}
