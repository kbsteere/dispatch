package reviewing

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

//go:embed prompt/reviewer.md
var reviewerInstructions string

// ReviewSchema is the JSON schema the reviewer's structured output must match.
//
//go:embed prompt/schema.json
var ReviewSchema string

// Pass is the kind of review pass.
type Pass string

const (
	PassFull        Pass = "full"
	PassIncremental Pass = "incremental"
	PassLedger      Pass = "comments-only"
)

// StandardFile is a repository instruction file read at the base commit.
type StandardFile struct {
	Path    string
	Content string
}

// PromptInput is everything a review prompt is built from.
type PromptInput struct {
	Watch        config.ReviewWatch
	Detail       PRDetail
	Pass         Pass
	Head         string
	Base         string
	MergeBase    string
	LastReviewed string
	Worktree     string
	ContextDirs  []string
	Ledger       []Finding
	Viewer       string
	Standards    []StandardFile
}

func untrusted(s string) string {
	s = strings.ReplaceAll(s, "</untrusted>", "&lt;/untrusted>")
	return "<untrusted>\n" + s + "\n</untrusted>"
}

// BuildPrompt renders the reviewer instructions followed by this pass's
// context. Text written by others is fenced in <untrusted> blocks.
func BuildPrompt(in PromptInput) string {
	var b strings.Builder
	b.WriteString(reviewerInstructions)
	fmt.Fprintf(&b, "\n\n## This pass\n\npr: %s\npass: %s\nauthor: %s\nreviewer (you act for): %s\n", in.Detail.Key, in.Pass, in.Detail.Author, in.Viewer)
	fmt.Fprintf(&b, "worktree: %s\nhead: %s\nbase: %s\nmerge_base: %s\n", in.Worktree, in.Head, in.Base, in.MergeBase)
	fmt.Fprintf(&b, "full diff: git -C %s diff %s %s\n", in.Worktree, in.MergeBase, in.Head)
	if in.Pass == PassIncremental && in.LastReviewed != "" {
		fmt.Fprintf(&b, "since last review: git -C %s diff %s %s (if this fails, the branch was force-pushed: review the full diff instead)\n",
			in.Worktree, in.LastReviewed, in.Head)
	}
	if len(in.ContextDirs) > 0 {
		b.WriteString("context repos:\n")
		for _, d := range in.ContextDirs {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	b.WriteString("\nchanged files:\n")
	for _, f := range in.Detail.Files {
		fmt.Fprintf(&b, "- %s\n", f)
	}

	if len(in.Standards) > 0 {
		b.WriteString("\n## Repository instructions (from the base branch)\n")
		for _, s := range in.Standards {
			fmt.Fprintf(&b, "\n### %s\n%s\n", s.Path, s.Content)
		}
	}
	if len(in.Watch.Focus) > 0 {
		b.WriteString("\n## Watch focus rules\n")
		for _, f := range in.Watch.Focus {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if len(in.Watch.DesignRefs) > 0 {
		b.WriteString("\n## Pinned design references\n")
		for _, d := range in.Watch.DesignRefs {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}

	b.WriteString("\n## Earlier findings (yours)\n")
	if len(in.Ledger) == 0 {
		b.WriteString("none\n")
	}
	for _, f := range in.Ledger {
		fmt.Fprintf(&b, "- id=%s thread=%s %s:%d blocking=%t status=%s: %s\n", f.ID, f.ThreadID, f.Path, f.Line, f.Blocking, f.Status, f.Summary)
	}

	b.WriteString("\n## Pull request\n\ntitle:\n")
	b.WriteString(untrusted(in.Detail.Title))
	b.WriteString("\n\ndescription:\n")
	b.WriteString(untrusted(in.Detail.Body))

	b.WriteString("\n\n## Review threads\n")
	for _, t := range in.Detail.Threads {
		state := "open"
		if t.IsResolved {
			state = "resolved"
		}
		fmt.Fprintf(&b, "\nthread %s (%s) %s:%d\n", t.ID, state, t.Path, t.Line)
		var tb strings.Builder
		for _, c := range t.Comments {
			fmt.Fprintf(&tb, "%s: %s\n", c.Author, c.Body)
		}
		b.WriteString(untrusted(tb.String()))
		b.WriteString("\n")
	}

	b.WriteString("\n## Conversation\n")
	var cb strings.Builder
	for _, c := range in.Detail.Comments {
		fmt.Fprintf(&cb, "%s: %s\n", c.Author, c.Body)
	}
	b.WriteString(untrusted(cb.String()))
	b.WriteString("\n")
	return b.String()
}

// LoadStandards reads CLAUDE.md and AGENTS.md at the base commit from the
// repo root and from every ancestor directory of the changed files. An
// AGENTS.md identical to its sibling CLAUDE.md is skipped.
func LoadStandards(ctx context.Context, repo LocalRepo, baseOID string, files []string) []StandardFile {
	dirs := []string{""}
	seen := map[string]bool{"": true}
	for _, f := range files {
		for d := path.Dir(f); d != "." && d != "/"; d = path.Dir(d) {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	depth := func(d string) int {
		if d == "" {
			return 0
		}
		return strings.Count(d, "/") + 1
	}
	slices.SortFunc(dirs, func(a, b string) int {
		if c := cmp.Compare(depth(a), depth(b)); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})

	var out []StandardFile
	for _, d := range dirs {
		var claude []byte
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			p := path.Join(d, name)
			content, err := ShowFile(ctx, repo, baseOID, p)
			if err != nil {
				continue
			}
			if name == "AGENTS.md" && claude != nil && bytes.Equal(content, claude) {
				continue
			}
			if name == "CLAUDE.md" {
				claude = content
			}
			out = append(out, StandardFile{Path: p, Content: string(content)})
		}
	}
	return out
}

// ManualPrompt is the prompt for an on-demand review that is shown in the
// TUI and never posted.
func ManualPrompt(url string) string {
	return "Review the pull request " + url + " using the instructions below, but return your findings as Markdown for a human to read, not JSON. " +
		"Nothing will be posted. You are running inside the repository checkout: run git commands without -C, and use gh pr view / gh pr diff to read the PR.\n\n" +
		reviewerInstructions
}
