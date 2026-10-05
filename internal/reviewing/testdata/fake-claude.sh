#!/usr/bin/env bash
# Stand-in for the claude CLI. Behaviour is chosen by FAKE_CLAUDE_MODE.
set -euo pipefail
args="$*"
sid=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) sid="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$args" > "${FAKE_CLAUDE_ARGS:-/dev/null}"
cat > "${FAKE_CLAUDE_STDIN:-/dev/null}"
result() {
  printf '[{"type":"system","subtype":"init","session_id":"%s"},{"type":"result","subtype":"success","is_error":false,"num_turns":%s,"total_cost_usd":%s,"session_id":"%s","result":"","structured_output":%s}]\n' "$sid" "$1" "$2" "$sid" "$3"
}
case "${FAKE_CLAUDE_MODE:-success}" in
  success)
    result 7 1.25 '{"findings":[],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"clean"}'
    ;;
  finding)
    result 9 2 '{"findings":[{"path":"kev.go","line":1,"blocking":true,"body":"`package kev` has no doc comment, and the repo CLAUDE.md requires one for every package."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"one blocking"}'
    ;;
  nit)
    result 6 1 '{"findings":[{"path":"kev.go","line":1,"blocking":false,"body":"`Sync` could return early when the feed is empty, which skips one allocation."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"one nit"}'
    ;;
  botspeak)
    result 9 2 '{"findings":[{"path":"kev.go","line":1,"blocking":true,"body":"Verified at abc1234def: missing doc comment."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":""}'
    ;;
  resolve)
    result 4 1 '{"findings":[],"ledger":[{"id":"f1","status":"addressed"}],"agreed_threads":[],"design_refs":"none","note":"fixed"}'
    ;;
  pushback)
    result 4 1 '{"findings":[],"ledger":[{"id":"f1","status":"outstanding","reply":"`run` still ignores `ctx.Done()`, so cancelling the watch leaks the goroutine."}],"agreed_threads":[],"design_refs":"none","note":""}'
    ;;
  badpath)
    result 4 1 '{"findings":[{"path":"not-in-pr.go","line":3,"blocking":true,"body":"Something is wrong here."}],"ledger":[{"id":"ghost","status":"addressed"}],"agreed_threads":[],"design_refs":"none","note":""}'
    ;;
  max_turns)
    printf '[{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":100,"total_cost_usd":3.5,"session_id":"%s","result":""}]\n' "$sid"
    ;;
  hang)
    # Spawn a child that would outlive us if only the parent were killed.
    sleep 300 &
    echo $! > "${FAKE_CLAUDE_CHILD:-/dev/null}"
    wait
    ;;
esac
