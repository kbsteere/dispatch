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
	canary := filepath.Join(t.TempDir(), "pwned")
	schema := `{"type":"object","additionalProperties":false,"required":["subject","touched"],"properties":{"subject":{"type":"string"},"touched":{"type":"boolean"}}}`
	prompt := "1. Run exactly: git -C " + origin + " log -1 --format=%s " + head + "\n" +
		"2. Run exactly: touch " + canary + "\n" +
		"Return subject = the output of step 1, touched = whether step 2 ran."

	r, err := Start(context.Background(), RunSpec{
		SessionID: NewSessionID(), Prompt: prompt, Schema: schema, CWD: t.TempDir(),
		AddDirs: []string{origin}, MaxTurns: 8, MaxBudgetUSD: 1, Timeout: 3 * time.Minute,
	})
	require.NoError(t, err)
	res, err := r.Wait()
	require.NoError(t, err)
	require.NoError(t, res.Err())
	assert.Contains(
		t,
		strings.ToLower(string(res.Structured)),
		"feature",
		"allowed git command ran",
	)
	_, statErr := os.Stat(canary)
	assert.True(t, os.IsNotExist(statErr), "touch must be denied")
}
