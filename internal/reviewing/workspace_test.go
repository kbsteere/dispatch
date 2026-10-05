package reviewing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prOrigin builds an origin with main plus a PR ref like GitHub's pull/7/head.
func prOrigin(t *testing.T) (origin, head string) {
	t.Helper()
	origin = newOrigin(t)
	gitT(t, origin, "checkout", "-q", "-b", "feature")
	require.NoError(
		t,
		os.WriteFile(filepath.Join(origin, "kev.go"), []byte("package kev\n"), 0o644),
	)
	gitT(t, origin, "add", ".")
	gitT(t, origin, "commit", "-q", "-m", "feature")
	head = strings.TrimSpace(gitT(t, origin, "rev-parse", "HEAD"))
	gitT(t, origin, "update-ref", "refs/pull/7/head", head)
	gitT(t, origin, "checkout", "-q", "main")
	return origin, head
}

func TestFetchPRAndPool(t *testing.T) {
	ctx := context.Background()
	origin, head := prOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	gitT(t, filepath.Dir(clone), "clone", "-q", origin, clone)
	repo, err := ResolveLocalRepo(clone)
	require.NoError(t, err)

	k := PRKey{"chainguard-dev", "mono", 7}
	require.NoError(t, FetchPR(ctx, repo, k, "main"))
	assert.Equal(t, head, strings.TrimSpace(gitT(t, clone, "rev-parse", "refs/dispatch/7/head")))
	base := strings.TrimSpace(gitT(t, clone, "rev-parse", "refs/dispatch/7/base"))
	mb, err := MergeBase(ctx, repo, head, base)
	require.NoError(t, err)
	assert.Equal(t, base, mb)

	userBranch := strings.TrimSpace(gitT(t, clone, "rev-parse", "--abbrev-ref", "HEAD"))

	pool := NewPool(repo, filepath.Join(t.TempDir(), "slots"), 1)
	slot, release, err := pool.Acquire(ctx, head)
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(slot, "kev.go"))
	require.NoError(t, err)
	assert.Equal(t, "package kev\n", string(b))

	_, _, err = pool.Acquire(ctx, head)
	assert.ErrorIs(t, err, ErrPoolBusy)

	require.NoError(t, os.WriteFile(filepath.Join(slot, "stray.txt"), []byte("x"), 0o644))
	release()

	slot2, release2, err := pool.Acquire(ctx, base)
	require.NoError(t, err)
	assert.Equal(t, slot, slot2, "slots are reused")
	_, err = os.Stat(filepath.Join(slot2, "kev.go"))
	assert.True(t, os.IsNotExist(err), "slot moved to the base commit")
	_, err = os.Stat(filepath.Join(slot2, "stray.txt"))
	assert.True(t, os.IsNotExist(err), "untracked files are cleaned between uses")
	release2()

	assert.Equal(
		t,
		userBranch,
		strings.TrimSpace(gitT(t, clone, "rev-parse", "--abbrev-ref", "HEAD")),
		"user checkout untouched",
	)

	require.NoError(t, pool.Prune(ctx))
	_, err = os.Stat(slot)
	assert.True(t, os.IsNotExist(err))
	assert.NotContains(t, gitT(t, clone, "worktree", "list"), "slot-0")
}

func TestFetchPRIntoBareRepo(t *testing.T) {
	origin, head := prOrigin(t)
	bare := filepath.Join(t.TempDir(), "mono")
	gitT(t, filepath.Dir(bare), "clone", "-q", "--bare", origin, bare)
	repo, err := ResolveLocalRepo(bare)
	require.NoError(t, err)
	require.NoError(
		t,
		FetchPR(context.Background(), repo, PRKey{"chainguard-dev", "mono", 7}, "main"),
	)
	assert.Equal(t, head, strings.TrimSpace(gitT(t, bare, "rev-parse", "refs/dispatch/7/head")))
}

func TestFetchPRRejectsBadBaseRef(t *testing.T) {
	origin, _ := prOrigin(t)
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	for _, ref := range []string{"--upload-pack=touch /tmp/x", "main..evil", "a b", ""} {
		assert.Error(t, FetchPR(context.Background(), repo, PRKey{"o", "r", 7}, ref), ref)
	}
}

func TestPoolRejectsBadSHA(t *testing.T) {
	origin, _ := prOrigin(t)
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	pool := NewPool(repo, t.TempDir(), 1)
	_, _, err = pool.Acquire(context.Background(), "--upload-pack=touch /tmp/x")
	assert.Error(t, err)
	slot, release, err := pool.Acquire(
		context.Background(),
		strings.TrimSpace(gitT(t, origin, "rev-parse", "main")),
	)
	require.NoError(t, err, "a failed acquire releases its slot")
	assert.NotEmpty(t, slot)
	release()
}

func TestShowFileAtBase(t *testing.T) {
	origin, _ := prOrigin(t)
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	mainSHA := strings.TrimSpace(gitT(t, origin, "rev-parse", "main"))
	b, err := ShowFile(context.Background(), repo, mainSHA, "README.md")
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(b))
	_, err = ShowFile(context.Background(), repo, mainSHA, "kev.go")
	assert.Error(t, err, "file only exists on the PR branch")
	_, err = ShowFile(context.Background(), repo, "HEAD", "README.md")
	assert.Error(t, err, "only full SHAs")
}

func TestWriteReviewFiles(t *testing.T) {
	ctx := context.Background()
	origin, head := prOrigin(t)
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	base := strings.TrimSpace(gitT(t, origin, "rev-parse", "main"))
	dir := t.TempDir()

	files, err := WriteReviewFiles(ctx, repo, dir, base, head, base)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "pr.diff"), files.Diff)
	diff, err := os.ReadFile(files.Diff)
	require.NoError(t, err)
	assert.Contains(t, string(diff), "+package kev")
	commits, err := os.ReadFile(files.Commits)
	require.NoError(t, err)
	assert.Contains(t, string(commits), "feature")
	since, err := os.ReadFile(files.SinceLast)
	require.NoError(t, err)
	assert.Contains(t, string(since), "+package kev")
	info, err := os.Stat(files.Diff)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestWriteReviewFilesWithoutUsableLastReview(t *testing.T) {
	origin, head := prOrigin(t)
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	base := strings.TrimSpace(gitT(t, origin, "rev-parse", "main"))

	files, err := WriteReviewFiles(context.Background(), repo, t.TempDir(), base, head, "")
	require.NoError(t, err)
	assert.Empty(t, files.SinceLast, "first review has no incremental diff")

	files, err = WriteReviewFiles(
		context.Background(),
		repo,
		t.TempDir(),
		base,
		head,
		strings.Repeat("f", 40),
	)
	require.NoError(t, err)
	assert.Empty(t, files.SinceLast, "a force-pushed-away commit yields no incremental diff")

	_, err = WriteReviewFiles(context.Background(), repo, t.TempDir(), "--output=/tmp/x", head, "")
	assert.Error(t, err)
}

func TestWriteReviewFilesIgnoresExternalDiff(t *testing.T) {
	origin, head := prOrigin(t)
	canary := filepath.Join(t.TempDir(), "ran")
	gitT(t, origin, "config", "diff.external", "touch "+canary+" #")
	repo, err := ResolveLocalRepo(origin)
	require.NoError(t, err)
	base := strings.TrimSpace(gitT(t, origin, "rev-parse", "main"))
	_, err = WriteReviewFiles(context.Background(), repo, t.TempDir(), base, head, "")
	require.NoError(t, err)
	_, statErr := os.Stat(canary)
	assert.True(t, os.IsNotExist(statErr), "external diff drivers never run")
}

func TestEnsureLocalRepoPrefersRepoPaths(t *testing.T) {
	origin, _ := prOrigin(t)
	repo, err := EnsureLocalRepo(
		context.Background(),
		t.TempDir(),
		PRKey{"chainguard-dev", "mono", 7},
		func(name string) (string, bool) { return origin, name == "chainguard-dev/mono" },
	)
	require.NoError(t, err)
	assert.Equal(t, origin, repo.GitDir)

	_, err = EnsureLocalRepo(
		context.Background(),
		t.TempDir(),
		PRKey{"..", "mono", 7},
		func(string) (string, bool) { return "", false },
	)
	assert.Error(t, err)
}
