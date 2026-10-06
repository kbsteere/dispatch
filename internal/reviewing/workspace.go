package reviewing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
		return out, fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return out, nil
}

// EnsureLocalRepo returns the configured local copy of k's repo, or a
// blobless bare clone kept under stateRoot when none is configured.
func EnsureLocalRepo(
	ctx context.Context,
	stateRoot string,
	k PRKey,
	lookup func(string) (string, bool),
) (LocalRepo, error) {
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
		cmd := exec.CommandContext(
			ctx,
			"git",
			slices.Concat(
				ghAuthArgs,
				[]string{"clone", "--bare", "--filter=blob:none", "--", url, dir},
			)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return LocalRepo{}, fmt.Errorf("cloning %s: %w: %s", url, err, out)
		}
	}
	return LocalRepo{GitDir: dir, Bare: true}, nil
}

// ErrKeyTouch reports an SSH fetch that needed a security key touch that
// never came.
var ErrKeyTouch = errors.New("security key touch needed")

// ghAuthArgs make git authenticate HTTPS requests with gh's login and
// nothing else, so no other credential helper can prompt.
var ghAuthArgs = []string{
	"-c",
	"credential.helper=",
	"-c",
	"credential.helper=!gh auth git-credential",
}

// remoteURLs returns the canonical repository's HTTPS and SSH addresses.
// PR refs (refs/pull/N/head) exist only there, not on a fork that a local
// clone's origin may point at. It is a variable so tests can use local repos.
var remoteURLs = func(k PRKey) (https, ssh string) {
	return fmt.Sprintf("https://github.com/%s/%s.git", k.Owner, k.Repo),
		fmt.Sprintf("git@github.com:%s/%s.git", k.Owner, k.Repo)
}

// sshNeedsTouch reports whether the SSH agent holds a security key
// identity, whose signatures need a physical touch.
var sshNeedsTouch = func() bool {
	out, err := exec.Command("ssh-add", "-L").Output()
	return err == nil && strings.Contains(string(out), "sk-")
}

// isTouchError recognises an SSH failure caused by an untouched security key.
func isTouchError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "agent refused operation") ||
		(strings.Contains(s, "signing failed") && strings.Contains(s, "-SK"))
}

// FetchPR fetches the PR head to refs/dispatch/N/head and its base branch to
// refs/dispatch/N/base from the canonical repository, without touching any
// working tree or remote. It uses HTTPS with gh's login, and falls back to
// SSH; onTouch, when set, is called before an SSH fetch that may need a
// security key touch.
func FetchPR(ctx context.Context, repo LocalRepo, k PRKey, baseRef string, onTouch func()) error {
	if baseRef == "" || !branchRe.MatchString(baseRef) || strings.HasPrefix(baseRef, "-") ||
		strings.Contains(baseRef, "..") {
		return fmt.Errorf("invalid base ref %q", baseRef)
	}
	if k.Number <= 0 {
		return fmt.Errorf("invalid PR number %d", k.Number)
	}
	specs := []string{
		fmt.Sprintf("+refs/pull/%d/head:refs/dispatch/%d/head", k.Number, k.Number),
		fmt.Sprintf("+refs/heads/%s:refs/dispatch/%d/base", baseRef, k.Number),
	}
	fetch := []string{"fetch", "--quiet", "--no-tags", "--force"}
	httpsURL, sshURL := remoteURLs(k)

	args := slices.Concat(ghAuthArgs, fetch, []string{httpsURL}, specs)
	_, httpsErr := gitCmd(ctx, repo.GitDir, args...)
	if httpsErr == nil {
		return nil
	}
	if onTouch != nil && sshNeedsTouch() {
		onTouch()
	}
	_, sshErr := gitCmd(ctx, repo.GitDir, slices.Concat(fetch, []string{sshURL}, specs)...)
	switch {
	case sshErr == nil:
		return nil
	case isTouchError(sshErr):
		return fmt.Errorf("%w: %v", ErrKeyTouch, sshErr)
	default:
		return fmt.Errorf("fetching %s: https: %v; ssh: %w", k, httpsErr, sshErr)
	}
}

// MergeBase returns the merge base of two commits.
func MergeBase(ctx context.Context, repo LocalRepo, head, base string) (string, error) {
	if !ValidSHA(head) || !ValidSHA(base) {
		return "", errors.New("invalid sha")
	}
	out, err := gitCmd(ctx, repo.GitDir, "merge-base", head, base)
	return strings.TrimSpace(string(out)), err
}

// ReviewFiles are git outputs dispatch prepares for a review session, so
// the agent never needs to run git itself.
type ReviewFiles struct {
	Diff      string
	Commits   string
	SinceLast string // empty when there is no usable earlier review
	// SinceLastFiles are the files changed since the last review; set only
	// with SinceLast.
	SinceLastFiles []string
}

// safeGitArgs keep git from running external programs configured in the
// repo or user config while it produces review files.
var safeGitArgs = []string{"-c", "core.fsmonitor=", "-c", "diff.external=", "-c", "core.pager=cat"}

// WriteReviewFiles writes the PR diff, its commit log, and the diff since
// the last reviewed head into dir.
func WriteReviewFiles(
	ctx context.Context,
	repo LocalRepo,
	dir, mergeBase, head, lastReviewed string,
) (ReviewFiles, error) {
	if !ValidSHA(mergeBase) || !ValidSHA(head) {
		return ReviewFiles{}, errors.New("invalid sha")
	}
	write := func(name string, args ...string) (string, error) {
		out, err := gitCmd(ctx, repo.GitDir, append(slices.Clone(safeGitArgs), args...)...)
		if err != nil {
			return "", err
		}
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, out, 0o600)
	}
	var files ReviewFiles
	var err error
	if files.Diff, err = write(
		"pr.diff",
		"diff",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		mergeBase,
		head,
	); err != nil {
		return files, err
	}
	if files.Commits, err = write(
		"commits.txt",
		"log",
		"--no-color",
		"--format=%H %an%n%s%n%n%b",
		mergeBase+".."+head,
	); err != nil {
		return files, err
	}
	if ValidSHA(lastReviewed) && lastReviewed != head {
		// A force push can drop the last reviewed commit; then there is no
		// incremental diff and the agent reviews the full diff.
		if p, err := write(
			"since-last-review.diff",
			"diff",
			"--no-color",
			"--no-ext-diff",
			"--no-textconv",
			lastReviewed,
			head,
		); err == nil {
			files.SinceLast = p
			if out, err := gitCmd(ctx, repo.GitDir, append(slices.Clone(safeGitArgs),
				"diff", "--name-only", "--no-ext-diff", lastReviewed, head)...); err == nil {
				files.SinceLastFiles = strings.FieldsFunc(
					string(out),
					func(r rune) bool { return r == '\n' },
				)
			}
		}
	}
	return files, nil
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
			_, err = gitCmd(
				ctx,
				p.repo.GitDir,
				"worktree",
				"add",
				"--quiet",
				"--detach",
				"--force",
				slot,
				head,
			)
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
