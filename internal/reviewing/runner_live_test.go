package reviewing

import (
	"context"
	"os"
	"path/filepath"
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
