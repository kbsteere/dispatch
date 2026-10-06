## Scope: the whole codebase (required)

Every step below is required. dispatch records each file you open and will not approve a review that skipped one.

1. Read the PR diff file.
2. Read every changed code file in full at head, not only its hunks. For config, data, and lock files the diff is enough; open them when the surrounding lines matter.
3. Documentation and design. For each touched component, read its README, its docs/ directory, design documents or ADRs beside it or referenced by the PR or its linked issues, the pinned design references, and every CLAUDE.md or AGENTS.md from the component up to the repository root. Open the diagrams too: Mermaid blocks in Markdown, .puml, .drawio, .mmd, and .svg or .png files under docs directories (you can view images).
4. Usages. For each changed or added function, method, type, constant, config key, flag, or environment variable, Grep the worktree and every context repo for where it is used, and read those call sites. Read the callees whose behaviour the change depends on.
5. Tests. Read the existing unit and integration tests for each changed package: test files beside the code, testdata fixtures, *_integration_test.go files, build-tagged tests, and e2e/ directories.
6. Related repositories. When the PR, its docs, or its code refer to code in a context repo, read that code as well.
