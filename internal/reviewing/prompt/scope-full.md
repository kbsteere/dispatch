## Scope: the whole codebase (required)

Every step below is required. dispatch records each file you open and will not approve a review that skipped one.

1. Read the PR diff file.
2. Read every changed code file in full at head, not only its hunks. For config, data, and lock files the diff is enough; open them when the surrounding lines matter.
3. Documentation and design. For each touched component, read its README, its docs/ directory, design documents or ADRs beside it or referenced by the PR or its linked issues, the pinned design references, and every CLAUDE.md or AGENTS.md from the component up to the repository root. Open the diagrams too: Mermaid blocks in Markdown, .puml, .drawio, .mmd, and .svg or .png files under docs directories (you can view images).
4. Usages. For each changed or added function, method, type, constant, config key, flag, or environment variable, Grep the worktree and every context repo for where it is used, and read those call sites. Read the callees whose behaviour the change depends on.
5. Tests. Read the existing unit and integration tests for each changed package: test files beside the code, testdata fixtures, *_integration_test.go files, build-tagged tests, and e2e/ directories.
6. Related repositories. When the PR, its docs, or its code refer to code in a context repo, read that code as well.
7. Security. Run a security pass over the diff and the code paths it touches, and report every vulnerability you can show as a finding with category `security`. If a security review guide is given below, follow its methodology and read the reference files that match the change. Otherwise check at least: command, SQL, and path injection; untrusted input reaching exec or git; requests to variable hosts (SSRF); missing authorization checks; secrets in code or logs; disabled TLS verification; security checks that fail open on errors or missing data; untrusted text written to terminals, logs, Markdown, Slack, or GitHub comments without sanitising; GitHub Actions injection (`pull_request_target`, `${{ }}` inside `run:`); unpinned images, actions, and dependencies; over-broad IAM or Terraform permissions; and crypto misuse.
