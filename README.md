# dispatch

A rich terminal UI for GitHub pull requests, issues, and notifications — with AI-assisted code review built in.

dispatch is a fork of the excellent [gh-dash](https://github.com/dlvhdr/gh-dash) by Dolev Hadar ([@dlvhdr](https://github.com/dlvhdr)) and the [gh-dash community](https://github.com/dlvhdr/gh-dash/graphs/contributors). All credit for the core dashboard belongs upstream; dispatch adds features aimed at high-volume review workflows.

## What dispatch adds

- **AI code review** — press `b` on a PR to run a read-only Claude review in the background. Results render in a Review tab in the preview pane, a status icon in the PR table shows running/finished/failed per PR, and `B` drops you into the interactive Claude session to interrogate the review or ask for fixes.
- **Reviewing for others** — configure a list of authors and repos, and dispatch reviews their PRs autonomously while it runs: it posts findings, resolves its own threads once they are fixed, and approves once it has reviewed the code at the current commit, nothing blocking is outstanding, and required CI is green. Reviews report only problems (bugs, test gaps, departures from the code's documented intent), never a narration of what was checked. See [Reviewing for others](#reviewing-for-others).
- **Plain-text icon mode** — `iconStyle: text` renders every icon as plain Unicode, so no Nerd Font or patched terminal font is required.
- **Inline diff tab** — the PR preview has a Diff tab with a colorized unified diff, fetched lazily and scrollable in place (`Ctrl+d`/`Ctrl+u`).
- **Honest fetch states** — sections show a loading spinner during any refresh and render fetch errors (e.g. rate limits) in the table instead of a misleading "no results" message.

## Install

```bash
go build -o dispatch .
```

Requires Go (see `.go-version`), the [gh CLI](https://cli.github.com) authenticated, and [Claude Code](https://claude.com/claude-code) on your PATH for the AI review features.

## Configuration

Config lives at `~/.config/dispatch/config.yml` (an existing `~/.config/gh-dash/config.yml` is picked up automatically as a fallback). Sections are GitHub search filters:

```yaml
prSections:
  - title: My Pull Requests
    filters: is:open author:@me org:your-org
iconStyle: text        # plain Unicode icons, no Nerd Font needed
repoPaths:
  your-org/repo: ~/code/repo      # exact repo -> local checkout (used by AI review + checkout)
  your-org/*: ~/code/*            # wildcard fallback
```

The `repoPaths` mapping tells dispatch where repos live locally; AI reviews and interactive sessions run in the mapped directory so Claude has the source tree for context.

## Key bindings (additions)

| Key | Action |
| --- | --- |
| `b` | Run a Claude review of the selected PR |
| `B` | Open the finished review's Claude session interactively |
| `d` | Full-screen diff via your configured pager (e.g. delta) |
| `[` / `]` | Cycle preview tabs (Overview, Activity, Commits, Checks, Files, Diff, Review) |
| `f` | Add the selected PR to the review watch, or remove a PR you added |
| `F` | Add a PR to the review watch by `owner/repo#N` or URL |
| `S` | Send a held review (press twice) |
| `N` | Review the selected watched PR now |
| `Z` | Pause or resume the review watch |

All upstream gh-dash keybindings and configuration continue to work; see the [upstream docs](https://gh-dash.dev) for the full reference.

## Reviewing for others

> **It acts as you.** In live mode dispatch posts reviews and approvals on GitHub as your `gh` user. Start every watch with `dryRun: true` and read its drafts before switching it to live.

```yaml
reviewingFor:
  limits:                       # defaults shown; each watch may override any of them
    maxBudgetUsd: 10            # per claude run
    maxTurns: 100               # per claude run
    maxTurnsLedgerOnly: 15      # per run when only comments changed
    runTimeout: 15m             # per claude run
    dailyBudgetUsd: 100         # per watch
    maxReviewsPerPrPerDay: 6
    maxInFlight: 3
    pollInterval: 3m            # minimum 1m
    maxGithubPointsPerHour: 300
  watches:
    - name: sustaining-automation
      authors: [tcnghia, powersj, dnegreira]
      scopes: [repo:chainguard-dev/mono, repo:chainguard-dev/stereo]
      dryRun: true
      focus: []                 # optional rules every review checks
      designRefs: []            # optional designs PRs must conform to
      ignoreLogins: []          # optional: activity from these never triggers a review
```

Watches have their own **Reviewing** view (cycle views with `s`, or set `defaults.view: reviewing`). Each watch is a tab with a summary line (mode, spend against the daily budget, reviews running, GitHub quota) and a table of its PRs, searched and hand-added together, built from local state so it costs no API quota: status, PR, author, open findings (`2b 1n` = 2 blocking, 1 non-blocking), required CI, last run (turns and cost), and last update. PRs needing your attention sort first. Selecting a row opens the PR sidebar on the Review tab, with outstanding findings, the private note, the session, and any held review. Watched PRs also show their status icon in the Claude column wherever they appear in the PRs view.

How it behaves:

- **Only while dispatch runs.** Quitting stops every review session.
- **New activity only.** On first start, open PRs are recorded as a baseline and not reviewed. A PR is reviewed when it gets a push (after it settles for 150s), a comment or resolution from someone other than you, or leaves draft. Drafts are never reviewed.
- **Bounded sessions.** Every review is a fresh `claude -p` session with a fixed session id, turn and dollar caps, and a timeout that kills the whole process group. Sessions are never resumed by the loop, so context cannot grow across a PR's lifetime. A new push kills a review of the old head.
- **No shell for agents.** Claude gets only Read/Grep/Glob over a worktree of the PR head and your other `repoPaths` checkouts. dispatch runs git itself (with external diff, textconv, and fsmonitor disabled) and hands the agent the PR diff, commit log, and diff since its last review as files. The agent cannot post; dispatch validates its output and does the posting. Your user Claude settings and hooks are not loaded into review sessions.
- **Fail closed.** A finding dispatch cannot place on a changed line holds the review for you instead of being dropped, every changed file is fetched (not just the first 100), and a failed required-checks lookup counts as "not passed".
- **Tone check.** Every comment is checked for bot narration (commit SHAs, "verified at", "LGTM", praise, summaries). A review that fails is held instead of posted; edit the draft file shown in the Review tab and press `S` twice to send it.
- **Approvals** are given only when dispatch reviewed the code at the current commit (a pass that only reconciled comments never counts), the PR is open, ready, mergeable, and not yours, required CI passed, and nothing blocking that dispatch raised or agreed with is outstanding. With no findings the approval has an empty body; non-blocking findings are posted with it, each marked `non-blocking:` or `nit:`. Activity on a baseline PR, or pressing `N`, runs a full review. In dry run the status is marked `(dry)` in the table, since nothing was posted.
- **Spend and quota.** Daily spend per watch and GitHub GraphQL quota are tracked; the footer shows `paused: budget` or `paused: github quota` when a limit is reached.

State lives in `$XDG_STATE_HOME/dispatch/reviewing/` (default `~/.local/state/dispatch/reviewing/`): one JSON file per PR, daily spend, dry-run and held drafts, worktree slots, and session directories.

## Under the hood

dispatch uses [bubbletea](https://github.com/charmbracelet/bubbletea), [lipgloss](https://github.com/charmbracelet/lipgloss), [glamour](https://github.com/charmbracelet/glamour), [cobra](https://github.com/spf13/cobra), [gh](https://github.com/cli/cli), [delta](https://github.com/dandavison/delta), and [Claude Code](https://claude.com/claude-code).

## License

MIT, same as upstream gh-dash — see [LICENSE.txt](./LICENSE.txt). Original work copyright Dolev Hadar ([@dlvhdr](https://github.com/dlvhdr)).
