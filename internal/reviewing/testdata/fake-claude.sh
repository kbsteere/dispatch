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
prompt=$(cat)
printf '%s' "$prompt" > "${FAKE_CLAUDE_STDIN:-/dev/null}"
wt=$(printf '%s\n' "$prompt" | sed -n 's/^worktree: //p' | head -n 1)
diff=$(printf '%s\n' "$prompt" | sed -n 's/^pr diff: //p' | head -n 1)

# Events mimic claude's stream-json output: tool calls, then the result.
n=0
read_file() {
  n=$((n + 1))
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"r%s","name":"Read","input":{"file_path":"%s"}}]}}\n' "$n" "$1"
  printf '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"r%s"}]}}\n' "$n"
}
# explore reads the diff, then FAKE_CLAUDE_READS (worktree-relative; by
# default the changed file, a doc, and a test), then searches once.
explore() {
  if [ -n "$diff" ]; then read_file "$diff"; fi
  if [ -z "$wt" ]; then return 0; fi
  for f in ${FAKE_CLAUDE_READS-kev.go README.md kev_test.go}; do read_file "$wt/$f"; done
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"Grep","input":{"pattern":"Sync","path":"%s"}}]}}\n' "$wt"
}
result() {
  printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$sid"
  explore
  printf '{"type":"result","subtype":"success","is_error":false,"num_turns":%s,"total_cost_usd":%s,"session_id":"%s","result":"","structured_output":%s}\n' "$1" "$2" "$sid" "$3"
}
case "${FAKE_CLAUDE_MODE:-success}" in
  success)
    result 7 1.25 '{"findings":[],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"clean","examined":{"docs":["README.md"],"code":["kev.go"],"usages":[],"tests":["kev_test.go"]},"verdict":"approve","verdict_reason":"nothing blocking"}'
    ;;
  finding)
    result 9 2 '{"findings":[{"path":"kev.go","line":1,"blocking":true,"body":"`package kev` has no doc comment, and the repo CLAUDE.md requires one for every package."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"one blocking","verdict":"request_changes","verdict_reason":"blocking finding"}'
    ;;
  hesitant)
    result 6 1 '{"findings":[{"path":"kev.go","line":1,"blocking":false,"body":"nit: `Sync` could log the feed URL it failed on."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"","examined":{"docs":["README.md"],"code":["kev.go"],"usages":[],"tests":["kev_test.go"]},"verdict":"comment","verdict_reason":"needs the author to confirm the retry budget"}'
    ;;
  security)
    result 8 1.5 '{"findings":[{"path":"kev.go","line":1,"blocking":false,"category":"security","body":"`Sync` passes the feed URL from the advisory to `exec.Command(\"curl\", url)`, so a URL starting with `-o` writes an arbitrary file. Validate the scheme and pass `--` before it."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"","examined":{"docs":["README.md"],"code":["kev.go"],"usages":[],"tests":["kev_test.go"]},"verdict":"approve","verdict_reason":"only a hardening note"}'
    ;;
  phantom)
    result 7 1.25 '{"findings":[],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"clean","examined":{"docs":["docs/design.md"],"code":["kev.go"],"usages":[],"tests":["kev_test.go"]},"verdict":"approve","verdict_reason":"nothing blocking"}'
    ;;
  nit)
    result 6 1 '{"findings":[{"path":"kev.go","line":1,"blocking":false,"body":"`Sync` could return early when the feed is empty, which skips one allocation."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"one nit","verdict":"approve","verdict_reason":"nothing blocking"}'
    ;;
  botspeak)
    result 9 2 '{"findings":[{"path":"kev.go","line":1,"blocking":true,"body":"Verified at abc1234def: missing doc comment."}],"ledger":[],"agreed_threads":[],"design_refs":"none","note":"","verdict":"request_changes","verdict_reason":"blocking finding"}'
    ;;
  resolve)
    result 4 1 '{"findings":[],"ledger":[{"id":"f1","status":"addressed"}],"agreed_threads":[],"design_refs":"none","note":"fixed","verdict":"approve","verdict_reason":"nothing blocking"}'
    ;;
  pushback)
    result 4 1 '{"findings":[],"ledger":[{"id":"f1","status":"outstanding","reply":"`run` still ignores `ctx.Done()`, so cancelling the watch leaks the goroutine."}],"agreed_threads":[],"design_refs":"none","note":"","verdict":"request_changes","verdict_reason":"blocking finding"}'
    ;;
  badpath)
    result 4 1 '{"findings":[{"path":"not-in-pr.go","line":3,"blocking":true,"body":"Something is wrong here."}],"ledger":[{"id":"ghost","status":"addressed"}],"agreed_threads":[],"design_refs":"none","note":"","verdict":"request_changes","verdict_reason":"blocking finding"}'
    ;;
  crash)
    # The session dies with no result, as one cut off by sleep does.
    exit 1
    ;;
  max_turns)
    printf '{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":100,"total_cost_usd":3.5,"session_id":"%s","result":""}\n' "$sid"
    ;;
  hang)
    # Spawn a child that would outlive us if only the parent were killed.
    sleep 300 &
    echo $! > "${FAKE_CLAUDE_CHILD:-/dev/null}"
    wait
    ;;
esac
