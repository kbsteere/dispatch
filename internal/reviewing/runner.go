package reviewing

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// gitReadCmds are the git subcommands the reviewer may run, each pinned to a
// directory with `git -C <dir>` because the agent cannot cd.
var gitReadCmds = []string{"show", "log", "diff", "grep", "blame", "ls-tree"}

var (
	// safeDirRe limits directories that are embedded in permission rules.
	safeDirRe   = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// GitAllowRules returns the Bash permission rules that let the agent run
// read-only git commands in the given directories and nothing else.
func GitAllowRules(dirs []string) []string {
	var rules []string
	for _, d := range dirs {
		for _, c := range gitReadCmds {
			rules = append(rules, fmt.Sprintf("Bash(git -C %s %s:*)", d, c))
		}
	}
	return rules
}

// RunSpec describes one bounded claude session.
type RunSpec struct {
	Binary       string
	SessionID    string
	Prompt       string
	Schema       string
	CWD          string
	AddDirs      []string
	MaxTurns     int
	MaxBudgetUSD float64
	Timeout      time.Duration
}

// BuildArgs returns the claude arguments for s. The prompt is not among
// them; it is written to stdin.
func BuildArgs(s RunSpec) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--session-id", s.SessionID,
		"--max-turns", strconv.Itoa(s.MaxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", s.MaxBudgetUSD),
		// User settings can carry PreToolUse hooks that approve Bash calls
		// and override --allowedTools; load none of them.
		"--setting-sources", "local",
		"--tools", "Read,Grep,Glob,Bash",
		"--permission-mode", "dontAsk",
	}
	if rules := GitAllowRules(s.AddDirs); len(rules) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, rules...)
	}
	for _, d := range s.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if s.Schema != "" {
		args = append(args, "--json-schema", s.Schema)
	}
	return args
}

// NewSessionID returns a random version 4 UUID.
func NewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidSessionID reports whether s is a UUID and therefore safe in a path.
func ValidSessionID(s string) bool { return sessionIDRe.MatchString(s) }

// Run is one claude process running in its own process group.
type Run struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdout bytes.Buffer
	stderr bytes.Buffer
	once   sync.Once
}

// Start launches a claude session in its own process group. The session is
// killed, with every child, when Timeout elapses or ctx is cancelled.
func Start(ctx context.Context, s RunSpec) (*Run, error) {
	for _, d := range s.AddDirs {
		if !safeDirRe.MatchString(d) || strings.Contains(d, "..") {
			return nil, fmt.Errorf("directory %q cannot be used in a permission rule", d)
		}
	}
	if !ValidSessionID(s.SessionID) {
		return nil, fmt.Errorf("invalid session id %q", s.SessionID)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	bin := s.Binary
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, BuildArgs(s)...)
	cmd.Dir = s.CWD
	cmd.Stdin = strings.NewReader(s.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	r := &Run{cmd: cmd, cancel: cancel}
	cmd.Stdout = &r.stdout
	cmd.Stderr = &r.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return r, nil
}

func (r *Run) PID() int { return r.cmd.Process.Pid }

// Kill stops the whole process group.
func (r *Run) Kill() {
	r.once.Do(func() {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		r.cancel()
	})
}

// Wait returns the parsed result. A run that hit a cap still returns its
// result (with its spend) and a nil error; check RunResult.Err.
func (r *Run) Wait() (RunResult, error) {
	err := r.cmd.Wait()
	r.cancel()
	if res, perr := ParseResult(r.stdout.Bytes()); perr == nil {
		return res, nil
	}
	if err == nil {
		err = errors.New("claude produced no result")
	}
	return RunResult{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(r.stderr.String()))
}

// KillOrphan kills the process group of pid when pid is a claude process
// left behind by an earlier dispatch.
func KillOrphan(pid int) bool {
	if pid <= 0 {
		return false
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil || strings.TrimSpace(string(comm)) != "claude" {
		return false
	}
	return syscall.Kill(-pid, syscall.SIGKILL) == nil
}
