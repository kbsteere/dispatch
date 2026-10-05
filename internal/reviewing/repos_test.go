package reviewing

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// newOrigin creates a normal repo with one commit on main.
func newOrigin(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "origin")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitT(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestResolveLocalRepoNormalClone(t *testing.T) {
	origin := newOrigin(t)
	got, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	assert.Equal(t, LocalRepo{GitDir: origin, Browse: origin, Bare: false}, got)
}

func TestResolveLocalRepoBareWithMainWorktree(t *testing.T) {
	// Mirrors ~/Dev/cg/mono: a bare repo with a "main" worktree inside it.
	origin := newOrigin(t)
	bare := filepath.Join(t.TempDir(), "mono")
	gitT(t, filepath.Dir(bare), "clone", "-q", "--bare", origin, bare)
	gitT(t, bare, "worktree", "add", "-q", filepath.Join(bare, "main"), "main")

	got, err := ResolveLocalRepo(bare)
	require.NoError(t, err)
	assert.True(t, got.Bare)
	assert.Equal(t, bare, got.GitDir)
	assert.Equal(t, filepath.Join(bare, "main"), got.Browse)
}

func TestResolveLocalRepoBareWithoutMainWorktree(t *testing.T) {
	origin := newOrigin(t)
	bare := filepath.Join(t.TempDir(), "omnibump")
	gitT(t, filepath.Dir(bare), "clone", "-q", "--bare", origin, bare)

	got, err := ResolveLocalRepo(bare)
	require.NoError(t, err)
	assert.Equal(t, LocalRepo{GitDir: bare, Bare: true}, got)
}

func TestResolveLocalRepoWorktreePath(t *testing.T) {
	// repoPaths maps chainguard-dev/mono to ~/Dev/cg/mono/main (a worktree).
	origin := newOrigin(t)
	bare := filepath.Join(t.TempDir(), "mono")
	gitT(t, filepath.Dir(bare), "clone", "-q", "--bare", origin, bare)
	wt := filepath.Join(bare, "main")
	gitT(t, bare, "worktree", "add", "-q", wt, "main")

	got, err := ResolveLocalRepo(wt)
	require.NoError(t, err)
	assert.Equal(t, LocalRepo{GitDir: wt, Browse: wt}, got)
}

func TestResolveLocalRepoGlobHomeAndMissing(t *testing.T) {
	origin := newOrigin(t)
	got, err := ResolveLocalRepo(filepath.Join(filepath.Dir(origin), "orig*"))
	require.NoError(t, err)
	assert.Equal(t, origin, got.GitDir)

	t.Setenv("HOME", filepath.Dir(origin))
	got, err = ResolveLocalRepo("~/origin")
	require.NoError(t, err)
	assert.Equal(t, origin, got.GitDir)

	_, err = ResolveLocalRepo(filepath.Join(t.TempDir(), "nope"))
	assert.Error(t, err)

	plain := t.TempDir()
	_, err = ResolveLocalRepo(plain)
	assert.Error(t, err, "a directory that is not a git repo")

	sub := filepath.Join(origin, "docs")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	_, err = ResolveLocalRepo(sub)
	assert.Error(t, err, "a subdirectory of a repo is not the repo")
}
