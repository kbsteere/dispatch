package prview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualReviewArgs(t *testing.T) {
	args := manualReviewArgs()
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-p --output-format json", "--setting-sources local", "--tools Read,Grep,Glob,Bash", "--permission-mode dontAsk",
		"Bash(git diff:*)", "Bash(gh pr view:*)", "Bash(gh pr diff:*)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	// --allowedTools is variadic and would swallow a positional prompt, so
	// the prompt goes over stdin.
	if strings.Contains(joined, "github.com") {
		t.Errorf("the prompt must not be passed as an argument: %s", joined)
	}
	if strings.Contains(joined, "/review ") {
		t.Error("manual reviews use the reviewer instructions, not /review")
	}
	if strings.Contains(joined, "gh pr review") || strings.Contains(joined, "gh api") {
		t.Error("the manual review must not be able to post")
	}
}

func TestResolveReviewDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := filepath.Join(home, "code", "repos", "stereo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repoDir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}

	// ~/Dev/cg/omnibump layout: a bare repo whose main worktree lives inside it.
	bareDir := filepath.Join(home, "code", "repos", "omnibump")
	for _, args := range [][]string{
		{"init", "-q", "--bare", bareDir},
		{"-C", bareDir, "worktree", "add", "-q", "--orphan", "-b", "main", filepath.Join(bareDir, "main")},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}

	t.Run("bare repo resolves to its main worktree", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/omnibump", map[string]string{
			"chainguard-dev/*": filepath.Join(home, "code", "repos", "*"),
		})
		if want := filepath.Join(bareDir, "main"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to home for a directory that is not a repo", func(t *testing.T) {
		plain := filepath.Join(home, "code", "repos", "plain")
		if err := os.MkdirAll(plain, 0o755); err != nil {
			t.Fatal(err)
		}
		got := resolveReviewDir("chainguard-dev/plain", map[string]string{"chainguard-dev/plain": plain})
		if got != home {
			t.Errorf("got %q, want home %q", got, home)
		}
	})

	t.Run("exact mapping", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/stereo", map[string]string{
			"chainguard-dev/stereo": repoDir,
		})
		if got != repoDir {
			t.Errorf("got %q, want %q", got, repoDir)
		}
	})

	t.Run("expands home-relative paths", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/stereo", map[string]string{
			"chainguard-dev/stereo": "~/code/repos/stereo",
		})
		if got != repoDir {
			t.Errorf("got %q, want %q", got, repoDir)
		}
	})

	t.Run("resolves owner wildcard mappings", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/stereo", map[string]string{
			"chainguard-dev/*": filepath.Join(home, "code", "repos", "*"),
		})
		if got != repoDir {
			t.Errorf("got %q, want %q", got, repoDir)
		}
	})

	t.Run("resolves glob values in exact mappings", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/stereo", map[string]string{
			"chainguard-dev/stereo": "~/code/repos/st*",
		})
		if got != repoDir {
			t.Errorf("got %q, want %q", got, repoDir)
		}
	})

	t.Run("falls back to home when unmapped", func(t *testing.T) {
		if got := resolveReviewDir("someone/else", map[string]string{}); got != home {
			t.Errorf("got %q, want home %q", got, home)
		}
	})

	t.Run("falls back to home when the path doesn't exist", func(t *testing.T) {
		got := resolveReviewDir("chainguard-dev/stereo", map[string]string{
			"chainguard-dev/stereo": filepath.Join(home, "missing"),
		})
		if got != home {
			t.Errorf("got %q, want home %q", got, home)
		}
	})
}
