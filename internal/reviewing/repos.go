package reviewing

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LocalRepo is a local copy of a GitHub repository. GitDir is the directory
// git commands run against; Browse is a checked-out tree for reading, empty
// when a bare repo has no main worktree.
type LocalRepo struct {
	GitDir string
	Browse string
	Bare   bool
}

// ResolveLocalRepo expands a repoPaths entry (~ and globs) and classifies it
// as a normal checkout or a bare repo.
func ResolveLocalRepo(path string) (LocalRepo, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return LocalRepo{}, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if strings.ContainsAny(path, "*?[") {
		matches, err := filepath.Glob(path)
		if err != nil || len(matches) == 0 {
			return LocalRepo{}, fmt.Errorf("no match for %s", path)
		}
		path = matches[0]
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return LocalRepo{}, fmt.Errorf("%s is not a directory", path)
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--is-bare-repository", "--show-toplevel").
		Output()
	if err != nil {
		// rev-parse fails on --show-toplevel inside a bare repo; ask again
		// for bareness alone.
		bare, berr := exec.Command("git", "-C", path, "rev-parse", "--is-bare-repository").Output()
		if berr != nil || strings.TrimSpace(string(bare)) != "true" {
			return LocalRepo{}, fmt.Errorf("%s is not a git repository", path)
		}
		return LocalRepo{GitDir: path, Browse: mainWorktree(path), Bare: true}, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "true" {
		return LocalRepo{GitDir: path, Browse: mainWorktree(path), Bare: true}, nil
	}
	if len(lines) != 2 || !samePath(lines[1], path) {
		return LocalRepo{}, fmt.Errorf("%s is not the top of a git repository", path)
	}
	return LocalRepo{GitDir: path, Browse: path}, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// mainWorktree returns the worktree of a bare repo that has main (or master)
// checked out, or "" when there is none.
func mainWorktree(gitDir string) string {
	out, err := exec.Command("git", "-C", gitDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	var wt string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			wt = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/main", line == "branch refs/heads/master":
			return wt
		}
	}
	return ""
}
