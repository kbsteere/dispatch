package reviewing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	ErrPoolBusy = errors.New("no free worktree slot")
	branchRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

func gitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// EnsureLocalRepo returns the configured local copy of k's repo, or a
// blobless bare clone kept under stateRoot when none is configured.
func EnsureLocalRepo(ctx context.Context, stateRoot string, k PRKey, lookup func(string) (string, bool)) (LocalRepo, error) {
	if !ValidName(k.Owner) || !ValidName(k.Repo) {
		return LocalRepo{}, fmt.Errorf("invalid repo %s", k.NameWithOwner())
	}
	if p, ok := lookup(k.NameWithOwner()); ok {
		if repo, err := ResolveLocalRepo(p); err == nil {
			return repo, nil
		}
	}
	dir := filepath.Join(stateRoot, "clones", fmt.Sprintf("%s__%s.git", k.Owner, k.Repo))
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return LocalRepo{}, err
		}
		url := fmt.Sprintf("https://github.com/%s/%s.git", k.Owner, k.Repo)
		cmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--filter=blob:none", "--", url, dir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return LocalRepo{}, fmt.Errorf("cloning %s: %w: %s", url, err, out)
		}
	}
	return LocalRepo{GitDir: dir, Bare: true}, nil
}

// FetchPR fetches the PR head to refs/dispatch/N/head and its base branch to
// refs/dispatch/N/base without touching any working tree.
func FetchPR(ctx context.Context, repo LocalRepo, k PRKey, baseRef string) error {
	if baseRef == "" || !branchRe.MatchString(baseRef) || strings.HasPrefix(baseRef, "-") || strings.Contains(baseRef, "..") {
		return fmt.Errorf("invalid base ref %q", baseRef)
	}
	if k.Number <= 0 {
		return fmt.Errorf("invalid PR number %d", k.Number)
	}
	_, err := gitCmd(ctx, repo.GitDir, "fetch", "--quiet", "--no-tags", "--force", "origin",
		fmt.Sprintf("+refs/pull/%d/head:refs/dispatch/%d/head", k.Number, k.Number),
		fmt.Sprintf("+refs/heads/%s:refs/dispatch/%d/base", baseRef, k.Number))
	return err
}

// MergeBase returns the merge base of two commits.
func MergeBase(ctx context.Context, repo LocalRepo, head, base string) (string, error) {
	if !ValidSHA(head) || !ValidSHA(base) {
		return "", errors.New("invalid sha")
	}
	out, err := gitCmd(ctx, repo.GitDir, "merge-base", head, base)
	return strings.TrimSpace(string(out)), err
}

// ShowFile returns a file's content at a commit.
func ShowFile(ctx context.Context, repo LocalRepo, sha, path string) ([]byte, error) {
	if !ValidSHA(sha) || path == "" || strings.HasPrefix(path, "-") {
		return nil, errors.New("invalid sha or path")
	}
	return exec.CommandContext(ctx, "git", "-C", repo.GitDir, "show", sha+":"+path).Output()
}

// Pool hands out detached worktrees of one repo, at most size at a time.
// Slots are reused across reviews, so checkouts after the first are
// incremental.
type Pool struct {
	repo LocalRepo
	dir  string
	mu   sync.Mutex
	busy []bool
}

func NewPool(repo LocalRepo, dir string, size int) *Pool {
	return &Pool{repo: repo, dir: dir, busy: make([]bool, size)}
}

// Acquire checks out head in a free slot. The returned function frees it.
func (p *Pool) Acquire(ctx context.Context, head string) (string, func(), error) {
	if !ValidSHA(head) {
		return "", nil, fmt.Errorf("invalid head %q", head)
	}
	p.mu.Lock()
	idx := -1
	for i, b := range p.busy {
		if !b {
			idx = i
			p.busy[i] = true
			break
		}
	}
	p.mu.Unlock()
	if idx < 0 {
		return "", nil, ErrPoolBusy
	}
	release := func() {
		p.mu.Lock()
		p.busy[idx] = false
		p.mu.Unlock()
	}
	slot := filepath.Join(p.dir, fmt.Sprintf("slot-%d", idx))
	var err error
	if _, statErr := os.Stat(filepath.Join(slot, ".git")); statErr == nil {
		_, err = gitCmd(ctx, slot, "checkout", "--quiet", "--detach", "--force", head)
		if err == nil {
			_, err = gitCmd(ctx, slot, "clean", "-ffdxq")
		}
	} else {
		if err = os.MkdirAll(p.dir, 0o700); err == nil {
			_, err = gitCmd(ctx, p.repo.GitDir, "worktree", "add", "--quiet", "--detach", "--force", slot, head)
		}
	}
	if err != nil {
		release()
		return "", nil, err
	}
	return slot, release, nil
}

// Prune removes every slot and git's records of them.
func (p *Pool) Prune(ctx context.Context) error {
	slots, _ := filepath.Glob(filepath.Join(p.dir, "slot-*"))
	for _, slot := range slots {
		_, _ = gitCmd(ctx, p.repo.GitDir, "worktree", "remove", "--force", slot)
		_ = os.RemoveAll(slot)
	}
	_, err := gitCmd(ctx, p.repo.GitDir, "worktree", "prune")
	return err
}
