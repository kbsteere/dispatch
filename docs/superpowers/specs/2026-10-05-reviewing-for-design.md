# Reviewing For: autonomous PR review for a list of authors

Date: 2026-10-05
Status: approved design, pending implementation plan

## Goal

dispatch watches open PRs from a configured list of authors in a configured set
of repos and reviews them autonomously while the TUI is running: it posts
findings, resolves its own threads once fixed, and approves when nothing is
outstanding and required CI is green. It replaces the Python `autorev` skill
(`../autorev`) with a Go implementation inside dispatch and changes how reviews
read: they report only problems (bugs, gaps, regressions, divergence from the
code's documented intent) and always check for missing unit and integration
tests. They never narrate what was checked.

## Non-goals

- Running headless or outside the TUI. Closing dispatch stops reviewing.
- Webhooks. Creating repo/org webhooks on chainguard-dev needs admin rights and
  a public endpoint; polling is cheap enough (see Polling budget).
- Topic, path, or CODEOWNERS filtering. Every non-draft PR from a listed author
  in a listed repo is in scope.
- Review depth levels. Every review runs the same checks, test gaps included.
- Agents executing PR code or holding GitHub write access.

## Configuration

A top-level `reviewingFor` block in `~/.config/dispatch/config.yml`, validated
with the rest of the config.

```yaml
reviewingFor:
  limits:                       # defaults for every watch; a watch may override any
    maxBudgetUsd: 10            # per claude run
    maxTurns: 100               # per claude run
    maxTurnsLedgerOnly: 15      # per run when only comments changed
    runTimeout: 15m             # per claude run, kills the process group
    dailyBudgetUsd: 100         # per watch, sum of total_cost_usd
    maxReviewsPerPrPerDay: 6
    maxInFlight: 3
    pollInterval: 3m            # minimum 1m
    maxGithubPointsPerHour: 300
  watches:
    - name: sustaining-automation
      authors: [tcnghia, powersj, dnegreira, AdamIsrael, ca-scribner, philroche, anushkamittal20, vishal-chdhry]
      scopes: [repo:chainguard-dev/mono, repo:chainguard-dev/stereo, repo:chainguard-dev/omnibump]
      dryRun: true
      focus: []                 # optional checkable rules applied to every review
      designRefs: []            # optional: "owner/repo#N (note)", repo path, or Linear key
      ignoreLogins: []          # optional: activity from these logins never triggers a pass
```

Validation: `name` unique and filesystem-safe, `authors` non-empty, every scope
of the form `repo:owner/name` or `org:name`, `pollInterval >= 1m`, all limits
positive.

## Session lifecycle and cost control

Each review is one short `claude -p` session that is never resumed by the loop.

- Go generates a UUID and writes it, with the PID and start time, to the PR's
  state file before launch.
- Invocation:
  `claude -p --session-id <uuid> --output-format json --max-turns <n>
  --max-budget-usd <x> --tools Read,Grep,Glob,Bash --allowedTools <git read-only
  Bash patterns> --json-schema <schema> --add-dir <worktree> --add-dir <context
  repos...>`, with cwd set to an empty directory under the state root.
- The next pass on the same PR starts a new session. Continuity comes from the
  compact ledger Go passes in (thread ids, paths, one-line summaries), never
  from conversation history, so context cannot grow across a PR's lifetime.

Hard stops, enforced in Go:

- Per run: `--max-turns`, `--max-budget-usd`, and a `runTimeout` context that
  kills the whole process group (`Setpgid`), including Bash children.
- Superseded runs: when the poller sees a new head, or the PR returns to draft,
  while a run is in flight, that session is killed and nothing is posted.
- TUI exit: every in-flight session is killed and its claim released.
- Startup: state files whose PID is alive and is a `claude` process are killed;
  their claims are released.
- Daily budget: `total_cost_usd` from each result is added to
  `spend/<YYYY-MM-DD>.json`. At `dailyBudgetUsd` the watch pauses and the
  footer shows `paused: budget`.
- A result with subtype `error_max_turns`, `error_max_budget`, or any other
  error never posts. The PR backs off 30m, then 2h, then 1d.

Loop guards:

- A pass triggers only on a new head, a draft becoming ready, or activity by
  someone other than the viewer and `ignoreLogins`. dispatch's own posts never
  trigger a pass.
- At most one full or incremental pass per head. A comments-only pass uses
  `maxTurnsLedgerOnly` and re-reads code only to check a claimed fix.
- `maxReviewsPerPrPerDay`; beyond it the PR is `throttled` until the next day or
  a manual re-review.
- `maxInFlight` concurrent runs.

Each PR's state records the last run's session id, turns, cost, and outcome.

## Polling

Search, every `pollInterval`, at most 3 pages:

```
is:pr is:open draft:false archived:false <scopes> author:<a> author:<b> ...
```

GitHub ORs repeated `author:` and `repo:` qualifiers. A detail query runs only
for PRs whose `updatedAt` moved since the cached value.

Ported from autorev, with its regression cases as tests:

- Activity fingerprint: hash of the newest comment, review, or thread reply by
  someone other than the viewer and `ignoreLogins`, plus the resolved-thread
  count. Hashing only the newest event avoids the autorev bug where its own
  approval evicted an older item from the fetch window and woke an empty pass.
- Head settle: a new head must be seen for about 150s before a pass is claimed,
  so review bots that post on each push land first.
- CI wake: a PR in `waiting-on-ci` is re-reviewed only when required checks
  settle on a different terminal result, held for two consecutive polls, at most
  once per 10 minutes.
- Required checks come from branch protection and rulesets, cached 15 minutes.
  Approval-gate checks (e.g. `doc-approval`) and checks that only report another
  reviewer's findings do not block approval.

Drafts are excluded by the search. A PR converted to draft drops out of the
results; an in-flight run for it is killed. A draft marked ready appears as new
and gets its first review.

First start of a watch: every PR in the results is recorded as a baseline
(current head and fingerprint) without a review. Reviews begin on activity after
that. The re-review key forces a pass on any PR.

### Manually added PRs

Any PR can be added to a watch by hand, including PRs from other authors or
repos outside the watch's scopes:

- From any PR section: a key adds the selected PR.
- By reference: a prompt accepts a PR URL or `owner/repo#N`.
- With more than one watch configured, a fuzzy picker chooses the watch; with
  one, it is used directly.

An added PR is stored in the watch state with `pinned: true` and is reviewed
by the autonomous loop exactly like a searched PR (same limits, triggers, gate,
and tone rules), with these differences:

- It is not baselined: its first review is claimed on the next poll.
- Pinned PRs are refreshed with one batched `nodes(ids: [...])` detail query per
  poll, not the search, so they cost one extra GraphQL request per poll in total.
- The draft rule still applies: a pinned draft waits until it is marked ready.
- If the PR is the viewer's own, it is reviewed with comments only and never
  approved.
- It is unpinned automatically when merged or closed, or by hand with the same
  key (which toggles).

Pinned PRs appear in the watch's `Reviewing: <watch>` section even when the
search would not return them.

### Polling budget

Measured 2026-10-05: the watch search costs 1 GraphQL point and returned about
45 non-draft PRs. Estimated steady state at 3m: about 20 points/hour for search
plus about 90 for detail queries, roughly 3% of the 5,000/hour GraphQL quota.
Reviews cost no GitHub quota.

Guards:

- Every GraphQL query also selects `rateLimit { remaining resetAt }`. Below 1,500
  remaining, polling slows to 15 minutes; below 500 it pauses until `resetAt`
  and the footer shows `paused: github quota`.
- `maxGithubPointsPerHour` caps the watch's own spend regardless of quota.
- REST calls for required checks send `If-None-Match`; 304 responses do not
  count against the core limit.

## State

Root: `$XDG_STATE_HOME/dispatch/reviewing/` (default `~/.local/state/...`, the
same root `internal/data/bookmarks.go` uses).

```
<watch>/
  lock                          # held by the dispatch process running the watch
  prs/<owner>__<repo>__<n>.json # one file per PR
  spend/<YYYY-MM-DD>.json
  drafts/                       # dry-run and held (needs-attention) reviews
worktrees/<owner>__<repo>/slot-<i>/
sessions/                       # empty cwd for claude runs
```

PR state holds: status, baseline flag, last reviewed head and signature, head
first-seen time, CI observation, ledger (finding id, thread id, path, line,
blocking, summary, status), thread ids dispatch opened, design refs found,
in-flight claim (session id, PID, start), last run (session id, turns, cost,
outcome), per-day review counts, backoff.

Writes are atomic (temp file and rename). A second dispatch process that cannot
take a watch's lock shows that watch read-only as "running in pid N".

A corrupt PR state file is moved aside and the ledger is rebuilt from GitHub
threads carrying the `<!-- dispatch -->` marker, so findings are not reposted.

## Local code access

Repos are resolved from dispatch's `repoPaths` (exact match, then glob, `~`
expanded), then classified:

- normal clone: used directly;
- bare repo (e.g. `~/Dev/cg/mono`, `~/Dev/cg/omnibump`): the git source, with
  its `main` worktree (from `git worktree list`) as the browsable copy;
- no entry: a blobless bare clone under the state root.

`resolveReviewDir` in `prview/review.go` gets the same bare-repo handling so the
manual review stops using a bare directory as its cwd.

PR code under review:

- `git fetch origin pull/<n>/head` into `refs/dispatch/<n>/head` and the base
  branch into `refs/dispatch/<n>/base`.
- A worktree pool per repo with `maxInFlight` slots, created with
  `git worktree add --detach`. Each pass checks out the head in a free slot with
  `git checkout --detach <head>`, which is incremental after the first use. The
  user's working trees and branches are never touched. Stale slots are pruned at
  startup.

Context repos: every resolved `repoPaths` repo is passed with `--add-dir` so the
agent can read related code across repos. The prompt states these are context
copies, not necessarily current main, and that `git show origin/main:<path>` is
authoritative.

Tools: `Read`, `Grep`, `Glob`, and Bash limited to `git show`, `git log`,
`git diff`, `git grep`, `git blame`, `git ls-tree`. No edit, write, or network
tools.

Prompt injection through `CLAUDE.md`: the claude cwd is an empty directory, and
the PR worktree comes in through `--add-dir`. Go reads the repo's `CLAUDE.md`
and `AGENTS.md` at the base commit and includes them in the prompt; changes the
PR makes to those files are reviewed as part of the diff. Implementation must
verify early that `--add-dir` does not auto-load `CLAUDE.md` on the installed
CLI; if it does, those files are removed from the slot checkout before each run.

## The reviewer

The prompt (`internal/reviewing/prompt/reviewer.md`, embedded) is a rewrite of
autorev's `reviewer.md` as a read-and-return-JSON contract.

Inputs assembled by Go: PR metadata and description, linked issues, changed
files, literal head/base/merge-base SHAs, worktree path, pass type (full,
incremental, comments-only), the ledger, other reviewers' open threads, watch
focus rules and design refs, and base-commit `CLAUDE.md`/`AGENTS.md`.

Reviewer steps:

1. Establish the original intent of the code being changed: repo instruction
   files and review-relevant repo skills, the touched components' READMEs and
   `docs/`, package doc comments, comments in and around the changed code,
   design docs (pinned, PR-referenced, or alongside the code), and linked Linear
   issues. This is the yardstick for whether the PR is a sound addition.
2. Review the diff for bugs, regressions, security issues, broken invariants,
   divergence from documented intent or design, and violations of a cited repo
   rule.
3. Check test gaps on every pass: a unit test for every new or changed branch of
   behaviour, a regression test for every fix, and an integration test only when
   the change crosses a process, network, database, or external API boundary or
   the repo already keeps integration tests for that component
   (`*_integration_test.go`, build tags, `e2e/`). Report each gap as the
   specific untested case.
4. Keep only findings backed by a concrete failure scenario or a cited rule.
5. Reconcile the ledger: each earlier finding is addressed, answered, or
   outstanding. Other reviewers' findings it agrees with are listed in
   `agreed_threads`; they hold the approval and get no reply.

Comment style:

- Only problems. Each inline comment states the problem, its consequence, and
  the fix, with a `suggestion` block for small mechanical fixes.
- Non-blocking items start with `nit:` or `non-blocking:`. Nothing else carries
  a label.
- The review body is empty unless a finding cannot be anchored to a diff line.
- Approvals have an empty body.
- Fixed threads dispatch opened are resolved without a reply. It replies only
  when the author pushed back and it still disagrees, once, with evidence.
- Never: commit SHAs, "verified", what was checked, skills applied,
  "conforms to", "no design doc found", praise, or summaries.

Output (`--json-schema`):

```json
{
  "findings": [{"path": "string", "line": 1, "start_line": 1, "blocking": true, "body": "string"}],
  "ledger": [{"id": "string", "status": "addressed|answered|outstanding", "reply": "string"}],
  "agreed_threads": ["string"],
  "design_refs": "string",
  "note": "string"
}
```

`note` is shown in the TUI only.

### Tone lint

Every `body` and `reply` passes `lintBody` before posting. It rejects text
containing a 7 to 40 character hex token that looks like a commit SHA, or
phrases such as `verified`, `confirmed at`, `I checked`, `LGTM`, `looks good`,
`no issues`, `conforms to`, `design doc`, `skills applied`, and praise words.
On any rejection nothing is posted for that pass; the PR becomes
`needs-attention` and the draft is held for the user to edit and post.

## Decisions made in Go

- Approve with an empty body and `commit_id` set to the reviewed head only when:
  open, not draft, not the viewer's PR, not conflicting, required checks
  `SUCCESS`, no outstanding ledger items, and not already approved at this head.
- `REQUEST_CHANGES` only to withdraw an earlier dispatch approval. Everything
  else is `COMMENT`.
- Before every post the head is re-read; if it moved, the pass is discarded and
  the PR re-queued.
- Inline anchors rejected with 422 are folded into the body, re-linted, and
  posted.
- dispatch resolves only threads it opened, tracked by thread id in state plus
  an invisible `<!-- dispatch -->` marker on every post.
- Dry run writes the would-be payload to `drafts/` and never touches GitHub.

PR statuses: `reviewing`, `approved`, `changes-requested`, `waiting-on-author`,
`waiting-on-ci`, `waiting-on-others`, `needs-attention`, `throttled`, `failed`,
`baseline`.

## TUI

- A `Reviewing: <watch>` PR section is added automatically when `reviewingFor`
  is configured, using the watch's search filter.
- The existing Claude status column shows the watch status per PR (glyph in
  nerd mode, short word with `iconStyle: text`).
- The Review tab for a watched PR shows outstanding findings, the private note,
  last run turns, cost, and session id, and any held draft. `B` opens the last
  session.
- Keys (final bindings chosen after checking existing ones): add or remove the
  selected PR from a watch, add a PR by URL or `owner/repo#N`, post held draft
  (with confirmation), re-review now (budget still applies), pause or resume the
  watch.
- Footer: `<watch> · N reviewing · $X today · live|dry run|paused: budget|paused: github quota|paused`.
- Polling runs on `tea.Tick`; all GitHub and claude work returns as messages.
- Quit kills in-flight sessions and releases claims.

The manual `b` review uses the same prompt and tone with posting disabled.
`parseClaudeResult` is fixed to read the event array that
`claude -p --output-format json` emits on CLI 2.1.280.

## Code layout

`internal/config`: `ReviewingForConfig`, defaults, validation.

`internal/reviewing/` (no TUI imports):

| File | Responsibility |
|---|---|
| `state.go` | PR state, spend, atomic writes, lock |
| `poll.go` | search and detail queries; pure `signature()` and `needsReview()` |
| `quota.go` | rate-limit tracking and backoff decisions |
| `gate.go` | pure approval-gate decision |
| `lint.go` | pure `lintBody()` |
| `workspace.go` | repo resolution, worktree pool, ref fetch |
| `runner.go` | claude command construction, process group, timeout, result parsing |
| `github.go` | post review, resolve, reply behind an interface |
| `engine.go` | poll, claim, run, gate, lint, post, record; emits events |
| `prompt/reviewer.md`, `prompt/schema.json` | embedded |

`internal/tui`: a bridge that schedules ticks, runs engine commands, and routes
engine events to the section, status column, Review tab, and footer.

## Error handling

- GitHub rate limit or 5xx: retry with backoff, show in footer, claims kept.
- claude cap or failure: no post, PR backoff.
- 422 inline anchor: fold into body, re-lint, post.
- Head moved: discard pass, re-queue.
- Corrupt state: move aside, rebuild ledger from marked threads.
- Watch lock held elsewhere: read-only view.

## Testing

- Table-driven unit tests with real data:
  - `needsReview`/`signature`: autorev's regression cases (window eviction as in
    mono#54695, head settle, CI debounce and cooldown).
  - `gate`: every approval condition.
  - `lintBody`: autorev's real bodies ("LGTM. Verified at ...", "Fixed at ...",
    "Seconding: confirmed at ...") must fail; real human review comments must
    pass.
  - config validation, state atomic write and lock, quota backoff thresholds,
    parsing a captured real `claude` event-array result.
- Workspace: real git repos in `t.TempDir()` (bare with worktrees, normal
  clone, glob `repoPaths`), pool checkout and reuse, stale slot cleanup.
- Runner: a fake `claude` bash script on `PATH` that emits canned results, sleeps
  past the timeout, or spawns a child, proving process-group kill, timeout, cap
  subtypes, and session id propagation.
- GitHub writes: `httptest` server for posting, 422 fallback, resolve, and the
  refusal to resolve threads dispatch did not open.
- Engine integration: fake GitHub plus fake `claude` covering dry run, live
  comment, approve, approval withdrawal, lint hold, baseline on first start,
  draft exclusion, pinned PR first review and auto-unpin on merge.
- PR reference parsing (`owner/repo#N`, PR URLs, malformed input).
- Before going live: run the real watch in `dryRun` and review drafts; run
  `/go-security-review` before opening the PR.
