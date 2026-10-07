package reviewing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveClaudeSandbox runs the real claude CLI with the arguments the
// runner builds. It costs a few cents, so it only runs with
// DISPATCH_LIVE_CLAUDE=1.
func TestLiveClaudeSandbox(t *testing.T) {
	if os.Getenv("DISPATCH_LIVE_CLAUDE") != "1" {
		t.Skip("set DISPATCH_LIVE_CLAUDE=1 to run against the real claude CLI")
	}
	origin, head := prOrigin(t)
	cwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "pr.diff"), []byte("+package kev\n"), 0o600))
	canary := filepath.Join(t.TempDir(), "pwned")
	gitCanary := filepath.Join(t.TempDir(), "git-pwned")
	schema := `{"type":"object","additionalProperties":false,"required":["diff","touched"],"properties":{"diff":{"type":"string"},"touched":{"type":"boolean"}}}`
	prompt := "1. Read " + filepath.Join(cwd, "pr.diff") + ".\n" +
		"2. Run: touch " + canary + "\n" +
		"3. Run: git -C " + origin + " show -s --format=tformat:x " + head + " --output=" + gitCanary + "\n" +
		"Return diff = the content from step 1, touched = whether step 2 or 3 ran."

	r, err := Start(context.Background(), RunSpec{
		SessionID: NewSessionID(), Prompt: prompt, Schema: schema, CWD: cwd,
		AddDirs: []string{origin}, MaxTurns: 8, MaxBudgetUSD: 1, Timeout: 3 * time.Minute,
	})
	require.NoError(t, err)
	res, err := r.Wait()
	require.NoError(t, err)
	require.NoError(t, res.Err())
	assert.Contains(t, string(res.Structured), "package kev", "provided files are readable")
	for _, c := range []string{canary, gitCanary} {
		_, statErr := os.Stat(c)
		assert.True(t, os.IsNotExist(statErr), "no shell: %s must not exist", c)
	}
}

func TestLiveClaudeResume(t *testing.T) {
	if os.Getenv("DISPATCH_LIVE_CLAUDE") != "1" {
		t.Skip("set DISPATCH_LIVE_CLAUDE=1 to run against the real claude CLI")
	}
	cwd := t.TempDir()
	first, second := filepath.Join(cwd, "a.txt"), filepath.Join(cwd, "b.txt")
	require.NoError(t, os.WriteFile(first, []byte("alpha\n"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("bravo\n"), 0o600))
	schema := `{"type":"object","additionalProperties":false,"required":["words"],"properties":{"words":{"type":"array","items":{"type":"string"}}}}`
	spec := RunSpec{
		SessionID: NewSessionID(), Schema: schema, CWD: cwd, MaxTurns: 6, MaxBudgetUSD: 1,
		Timeout: 3 * time.Minute, EventsPath: filepath.Join(cwd, "events.jsonl"),
		Prompt: "Read " + first + " and return the words in it.",
	}
	r, err := Start(context.Background(), spec)
	require.NoError(t, err)
	res, err := r.Wait()
	require.NoError(t, err)
	require.NoError(t, res.Err())

	spec.Resume = true
	spec.Prompt = "You skipped a file. Read " + second + " too, then return every word from both files."
	r, err = Start(context.Background(), spec)
	require.NoError(t, err)
	more, err := r.Wait()
	require.NoError(t, err)
	require.NoError(t, more.Err())
	t.Logf("first: turns=%d cost=%.4f; resumed: turns=%d cost=%.4f session=%s",
		res.Turns, res.CostUSD, more.Turns, more.CostUSD, more.SessionID)

	assert.Equal(t, spec.SessionID, more.SessionID, "the same session continued")
	assert.GreaterOrEqual(t, more.CostUSD, res.CostUSD,
		"a resumed run reports the session's total cost, which dispatch relies on")
	assert.Contains(t, string(more.Structured), "alpha", "it remembers the first run")
	assert.Contains(t, string(more.Structured), "bravo")
	assert.Equal(t, []string{second}, more.Reads, "reads are the continuation's own")
	events, err := os.ReadFile(spec.EventsPath)
	require.NoError(t, err)
	assert.Equal(
		t,
		2,
		strings.Count(string(events), `"type":"result"`),
		"both runs' events are kept",
	)
}
