package prview

import (
	"os"
	"path/filepath"
	"testing"
)

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
