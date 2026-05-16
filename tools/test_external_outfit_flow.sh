#!/usr/bin/env bash
set -euo pipefail

# Lightweight smoke harness for AgentGO external Outfit trigger flows.
# Usage:
#   AGENTGO_URL=http://localhost:5226 \
#   OUTFIT_ROUTE=i1_my_outfit \
#   OUTFIT_KEY=your-key \
#   OBJECTIVE='Make a tiny safe change' \
#   tools/test_external_outfit_flow.sh

AGENTGO_URL="${AGENTGO_URL:-http://localhost:5226}"
OUTFIT_ROUTE="${OUTFIT_ROUTE:-}"
OUTFIT_KEY="${OUTFIT_KEY:-}"
OBJECTIVE="${OBJECTIVE:-External Outfit smoke test. Make the smallest safe patch possible.}"

if [[ -z "$OUTFIT_ROUTE" || -z "$OUTFIT_KEY" ]]; then
  echo "OUTFIT_ROUTE and OUTFIT_KEY are required." >&2
  exit 2
fi

trigger_url="${AGENTGO_URL%/}/outfits/${OUTFIT_ROUTE}/run"

echo "Triggering: $trigger_url"
accepted_json="$(curl -fsS -X POST "$trigger_url" \
  -H "Content-Type: application/json" \
  -H "X-AgentGO-Outfit-Key: $OUTFIT_KEY" \
  -d "$(printf '{"objective":%s,"diagnostics":%s}' "$(jq -Rn --arg v "$OBJECTIVE" '$v')" "$(jq -Rn --arg v "" '$v')")")"

echo "$accepted_json" | jq .
run_id="$(echo "$accepted_json" | jq -r '.run_id // .runId // empty')"
if [[ -z "$run_id" ]]; then
  echo "Could not find run_id in response." >&2
  exit 1
fi

echo "Accepted run: $run_id"
echo "Poll/list runs:"
curl -fsS -H "X-AgentGO-Outfit-Key: $OUTFIT_KEY" "${AGENTGO_URL%/}/outfits/${OUTFIT_ROUTE}/runs?limit=5" | jq .

echo
cat <<MSG
When the run completes, try:
  curl -H 'X-AgentGO-Outfit-Key: $OUTFIT_KEY' '${AGENTGO_URL%/}/outfits/${OUTFIT_ROUTE}/runs/${run_id}/pull/changed_files_manifest'
  curl -H 'X-AgentGO-Outfit-Key: $OUTFIT_KEY' -o changed_files.zip '${AGENTGO_URL%/}/outfits/${OUTFIT_ROUTE}/runs/${run_id}/pull/changed_files_zip'
  curl -H 'X-AgentGO-Outfit-Key: $OUTFIT_KEY' -o projectwork.zip '${AGENTGO_URL%/}/outfits/${OUTFIT_ROUTE}/runs/${run_id}/pull/projectwork_zip'
MSG
