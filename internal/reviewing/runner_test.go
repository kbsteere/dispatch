package reviewing

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-claude.sh")
	require.NoError(t, err)
	return bin
}

func fakeSpec(t *testing.T) RunSpec {
	return RunSpec{
		Binary: fakeClaudeBin(
			t,
		),
		SessionID: NewSessionID(),
		Prompt:    "review this",
		Schema:    ReviewSchema,
		CWD:       t.TempDir(),
		AddDirs: []string{
			"/wt/slot-0",
			"/home/u/Dev/cg/stereo",
		},
		MaxTurns:     100,
		MaxBudgetUSD: 10,
		Timeout:      5 * time.Second,
	}
}

func TestBuildArgs(t *testing.T) {
	s := fakeSpec(t)
	args := strings.Join(BuildArgs(s), " ")
	for _, want := range []string{
		"-p", "--output-format stream-json --verbose", "--session-id " + s.SessionID, "--max-turns 100", "--max-budget-usd 10.00",
		"--setting-sources local", "--tools Read,Grep,Glob --permission-mode dontAsk",
		"--add-dir /wt/slot-0", "--add-dir /home/u/Dev/cg/stereo", "--json-schema",
	} {
		assert.Contains(t, args, want)
	}
	assert.NotContains(t, args, "dangerously")
	assert.NotContains(t, args, "Bash", "review sessions never get a shell")
	assert.NotContains(t, args, "--allowedTools")
	assert.NotContains(t, args, "review this", "the prompt goes over stdin, never argv")
	assert.NotContains(t, args, "--model", "unset keeps the claude CLI default")
	assert.NotContains(t, args, "--effort")
}

func TestBuildArgsModel(t *testing.T) {
	s := fakeSpec(t)
	s.Model = config.ReviewModel{Name: "claude-sonnet-5-5", Effort: "high"}
	args := strings.Join(BuildArgs(s), " ")
	assert.Contains(t, args, "--model claude-sonnet-5-5 --effort high")
	assert.Contains(t, args, "--tools Read,Grep,Glob --permission-mode dontAsk")

	s.Resume = true
	assert.Contains(
		t,
		strings.Join(BuildArgs(s), " "),
		"--model claude-sonnet-5-5 --effort high",
		"a continuation runs on the same model",
	)

	assert.Equal(t, []string{"--effort", "max"}, ModelArgs(config.ReviewModel{Effort: "max"}))
	assert.Equal(
		t,
		[]string{"--model", "opus[1m]"},
		ModelArgs(config.ReviewModel{Name: "opus[1m]"}),
	)
}

func TestStartRejectsUnsafeDirs(t *testing.T) {
	for _, d := range []string{"/tmp/a b", "relative/dir", "/tmp/x) Bash(rm:*", "/tmp/../etc"} {
		s := fakeSpec(t)
		s.AddDirs = []string{d}
		_, err := Start(context.Background(), s)
		assert.Error(t, err, d)
	}
}

func TestNewSessionID(t *testing.T) {
	id := NewSessionID()
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	assert.NotEqual(t, id, NewSessionID())
	assert.True(t, ValidSessionID(id))
	assert.False(t, ValidSessionID("../../etc"))
}

func TestRunSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_CLAUDE_MODE", "success")
	t.Setenv("FAKE_CLAUDE_ARGS", filepath.Join(dir, "args"))
	t.Setenv("FAKE_CLAUDE_STDIN", filepath.Join(dir, "stdin"))
	s := fakeSpec(t)
	r, err := Start(context.Background(), s)
	require.NoError(t, err)
	assert.Greater(t, r.PID(), 0)
	res, err := r.Wait()
	require.NoError(t, err)
	assert.Equal(t, s.SessionID, res.SessionID)
	assert.Equal(t, 7, res.Turns)
	assert.Equal(t, 1.25, res.CostUSD)
	out, err := res.Review()
	require.NoError(t, err)
	assert.Equal(t, "clean", out.Note)

	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	assert.Contains(t, string(args), "--session-id "+s.SessionID)
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	assert.Equal(t, "review this", string(stdin))
}

func TestRunMaxTurnsIsAResult(t *testing.T) {
	t.Setenv("FAKE_CLAUDE_MODE", "max_turns")
	r, err := Start(context.Background(), fakeSpec(t))
	require.NoError(t, err)
	res, err := r.Wait()
	require.NoError(t, err, "a cap is a parsed result, not a process failure")
	assert.Equal(t, "error_max_turns", res.Subtype)
	assert.Error(t, res.Err())
	assert.Equal(t, 3.5, res.CostUSD, "spend is still recorded")
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	childFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("FAKE_CLAUDE_MODE", "hang")
	t.Setenv("FAKE_CLAUDE_CHILD", childFile)
	s := fakeSpec(t)
	s.Timeout = 500 * time.Millisecond
	r, err := Start(context.Background(), s)
	require.NoError(t, err)
	_, err = r.Wait()
	require.Error(t, err)

	b, err := os.ReadFile(childFile)
	require.NoError(t, err)
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	require.Positive(t, child)
	require.Eventually(
		t,
		func() bool { return !alive(child) },
		2*time.Second,
		50*time.Millisecond,
		"child sleep must die with the group",
	)
}

func TestRunKill(t *testing.T) {
	childFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("FAKE_CLAUDE_MODE", "hang")
	t.Setenv("FAKE_CLAUDE_CHILD", childFile)
	r, err := Start(context.Background(), fakeSpec(t))
	require.NoError(t, err)
	require.Eventually(
		t,
		func() bool { b, _ := os.ReadFile(childFile); return len(b) > 0 },
		2*time.Second,
		20*time.Millisecond,
	)
	pid := r.PID()
	r.Kill()
	r.Kill()
	_, err = r.Wait()
	assert.Error(t, err)
	assert.False(t, alive(pid))
	b, _ := os.ReadFile(childFile)
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	require.Eventually(t, func() bool { return !alive(child) }, 2*time.Second, 50*time.Millisecond)
}

func TestKillOrphanIgnoresNonClaude(t *testing.T) {
	assert.False(t, KillOrphan(os.Getpid()), "never kills a process that is not claude")
	assert.False(t, KillOrphan(0))
	assert.False(t, KillOrphan(-1))
}

func TestBuildArgsResume(t *testing.T) {
	s := fakeSpec(t)
	s.Resume = true
	args := strings.Join(BuildArgs(s), " ")
	assert.Contains(t, args, "--resume "+s.SessionID)
	assert.NotContains(t, args, "--session-id", "a resumed session is not started again")
	for _, want := range []string{
		"--tools Read,Grep,Glob --permission-mode dontAsk", "--setting-sources local",
		"--add-dir /wt/slot-0", "--json-schema",
	} {
		assert.Contains(t, args, want, "a continuation keeps the first run's limits")
	}
}
