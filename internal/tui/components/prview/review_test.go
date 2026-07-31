package prview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseClaudeResult(t *testing.T) {
	t.Run("parses a successful result", func(t *testing.T) {
		out := []byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":184052,"num_turns":12,"result":"## Code Review\n\nThe change looks correct.","session_id":"5c1e2a4f-9d1b-4a3e-8f2c-0b7d6e5a4c3b","total_cost_usd":0.42}`)

		res, err := parseClaudeResult(out)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.SessionId != "5c1e2a4f-9d1b-4a3e-8f2c-0b7d6e5a4c3b" {
			t.Errorf("got session id %q", res.SessionId)
		}
		if res.Result != "## Code Review\n\nThe change looks correct." {
			t.Errorf("got result %q", res.Result)
		}
	})

	t.Run("surfaces claude errors", func(t *testing.T) {
		out := []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"credit balance too low","session_id":"x"}`)

		if _, err := parseClaudeResult(out); err == nil {
			t.Fatal("expected an error for is_error=true")
		}
	})

	t.Run("rejects non-JSON output", func(t *testing.T) {
		if _, err := parseClaudeResult([]byte("not json")); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestResolveReviewDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := filepath.Join(home, "code", "repos", "stereo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

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
