package reviewing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Status is where a PR stands in a watch.
type Status string

const (
	StatusBaseline         Status = "baseline"
	StatusReviewing        Status = "reviewing"
	StatusApproved         Status = "approved"
	StatusChangesRequested Status = "changes-requested"
	StatusWaitingOnAuthor  Status = "waiting-on-author"
	StatusWaitingOnCI      Status = "waiting-on-ci"
	StatusWaitingOnOthers  Status = "waiting-on-others"
	StatusNeedsAttention   Status = "needs-attention"
	StatusThrottled        Status = "throttled"
	StatusFailed           Status = "failed"
)

// Finding statuses.
const (
	FindingOutstanding = "outstanding"
	FindingAddressed   = "addressed"
	FindingAnswered    = "answered"
)

// Finding is a problem dispatch raised, or agreed with, on a PR.
type Finding struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id,omitempty"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Blocking bool   `json:"blocking"`
	Summary  string `json:"summary"`
	Status   string `json:"status"`
	// Replied records that dispatch already answered pushback on this
	// finding's thread; it never replies twice.
	Replied bool `json:"replied,omitempty"`
}

// Signature is the PR state a review pass last saw.
type Signature struct {
	Head   string `json:"head"`
	Draft  bool   `json:"draft"`
	Digest string `json:"digest"`
	CI     string `json:"ci"`
}

// Claim marks a PR as being reviewed by a running claude session.
type Claim struct {
	SessionID string    `json:"session_id"`
	PID       int       `json:"pid"`
	Since     time.Time `json:"since"`
	Head      string    `json:"head"`
}

// RunRecord describes the last claude session run for a PR.
type RunRecord struct {
	SessionID string    `json:"session_id"`
	Turns     int       `json:"turns"`
	CostUSD   float64   `json:"cost_usd"`
	Outcome   string    `json:"outcome"`
	At        time.Time `json:"at"`
}

type HeadSeen struct {
	OID string    `json:"oid"`
	At  time.Time `json:"at"`
}

type CIObs struct {
	State string `json:"state"`
	N     int    `json:"n"`
}

type Backoff struct {
	Until time.Time `json:"until"`
	Step  int       `json:"step"`
}

// PRState is everything dispatch remembers about one PR in one watch.
type PRState struct {
	Key    PRKey  `json:"key"`
	NodeID string `json:"node_id,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	CI     string `json:"ci,omitempty"` // required-check state at the last poll
	Pinned bool   `json:"pinned,omitempty"`
	// ReviewRequested marks a PR where you, or a team you belong to, are a
	// requested reviewer.
	ReviewRequested bool `json:"review_requested,omitempty"`
	// Inactive marks a PR the watch's search no longer returns (merged,
	// closed, back to draft, or out of scope). It is hidden until it returns.
	Inactive    bool       `json:"inactive,omitempty"`
	Status      Status     `json:"status,omitempty"`
	UpdatedAt   string     `json:"updated_at,omitempty"`
	ReviewedSig *Signature `json:"reviewed_sig,omitempty"`
	// CodeReviewedHead is the head whose code a full or incremental pass last
	// read. Baseline and comments-only passes never set it.
	CodeReviewedHead string `json:"code_reviewed_head,omitempty"`
	// DryRun marks a status recorded in dry run: nothing was posted.
	DryRun bool `json:"dry_run,omitempty"`
	// Examined is what the last pass said it read; ReviewGaps is what it
	// was required to read and did not.
	Examined   *Examined `json:"examined,omitempty"`
	ReviewGaps []string  `json:"review_gaps,omitempty"`
	// Verdict and VerdictReason are the last pass's approval call.
	Verdict       string         `json:"verdict,omitempty"`
	VerdictReason string         `json:"verdict_reason,omitempty"`
	HeadSeen      *HeadSeen      `json:"head_seen,omitempty"`
	CIObs         CIObs          `json:"ci_obs"`
	LastCIWake    time.Time      `json:"last_ci_wake"`
	Ledger        []Finding      `json:"ledger,omitempty"`
	OurThreads    []string       `json:"our_threads,omitempty"`
	DesignRefs    string         `json:"design_refs,omitempty"`
	ApprovedHead  string         `json:"approved_head,omitempty"`
	Claim         *Claim         `json:"claim,omitempty"`
	LastRun       *RunRecord     `json:"last_run,omitempty"`
	ReviewsByDay  map[string]int `json:"reviews_by_day,omitempty"`
	Backoff       Backoff        `json:"backoff"`
	Note          string         `json:"note,omitempty"`
	HeldDraft     string         `json:"held_draft,omitempty"`
	ForceReview   bool           `json:"force_review,omitempty"`
}

// OutstandingCounts counts outstanding findings by severity.
func (st PRState) OutstandingCounts() (blocking, nonBlocking int) {
	for _, f := range st.Ledger {
		if f.Status != FindingOutstanding {
			continue
		}
		if f.Blocking {
			blocking++
		} else {
			nonBlocking++
		}
	}
	return blocking, nonBlocking
}

var (
	ErrCorrupt = errors.New("corrupt PR state")
	ErrLocked  = errors.New("watch is locked by another dispatch process")
)

var storeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Day is the local calendar day used for daily budgets and review counts.
func Day(t time.Time) string { return t.Local().Format("2006-01-02") }

// DefaultStateRoot is $XDG_STATE_HOME/dispatch/reviewing.
func DefaultStateRoot() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dispatch", "reviewing")
}

// Store is a watch's state directory.
type Store string

// OpenStore creates (if needed) and returns the state directory for watch.
func OpenStore(root, watch string) (Store, error) {
	if !storeNameRe.MatchString(watch) {
		return "", fmt.Errorf("unsafe watch name %q", watch)
	}
	dir := filepath.Join(root, watch)
	for _, sub := range []string{"prs", "spend", "drafts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return "", err
		}
	}
	return Store(dir), nil
}

func (s Store) prPath(k PRKey) string {
	return filepath.Join(string(s), "prs", k.FileName()+".json")
}

// LoadPR returns the saved state for k, or a fresh state when none exists.
// A file that cannot be decoded is moved aside and ErrCorrupt is returned
// along with a fresh state.
func (s Store) LoadPR(k PRKey) (PRState, error) {
	fresh := PRState{Key: k}
	b, err := os.ReadFile(s.prPath(k))
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return fresh, err
	}
	var st PRState
	if err := json.Unmarshal(b, &st); err != nil {
		aside := fmt.Sprintf("%s.corrupt-%d", s.prPath(k), time.Now().UnixNano())
		_ = os.Rename(s.prPath(k), aside)
		return fresh, fmt.Errorf("%w: %s moved to %s", ErrCorrupt, k, aside)
	}
	return st, nil
}

func (s Store) SavePR(st PRState) error {
	return writeJSONAtomic(s.prPath(st.Key), st)
}

// AllPRs returns every readable PR state, sorted by file name.
func (s Store) AllPRs() ([]PRState, error) {
	paths, err := filepath.Glob(filepath.Join(string(s), "prs", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]PRState, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var st PRState
		if json.Unmarshal(b, &st) == nil {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s Store) spendPath(day string) string {
	return filepath.Join(string(s), "spend", day+".json")
}

// Spend is the total claude cost recorded for day.
func (s Store) Spend(day string) float64 {
	var v struct {
		USD float64 `json:"usd"`
	}
	b, err := os.ReadFile(s.spendPath(day))
	if err != nil || json.Unmarshal(b, &v) != nil {
		return 0
	}
	return v.USD
}

// AddSpend adds usd to day's total and returns the new total.
func (s Store) AddSpend(day string, usd float64) (float64, error) {
	total := s.Spend(day) + usd
	return total, writeJSONAtomic(s.spendPath(day), map[string]float64{"usd": total})
}

// Initialized reports whether the watch has recorded its first-start baseline.
func (s Store) Initialized() bool {
	_, err := os.Stat(filepath.Join(string(s), "initialized"))
	return err == nil
}

func (s Store) MarkInitialized() error {
	return os.WriteFile(
		filepath.Join(string(s), "initialized"),
		[]byte(time.Now().Format(time.RFC3339)),
		0o600,
	)
}

// DraftPath is where a dry-run or held review for k is written.
func (s Store) DraftPath(k PRKey, t time.Time) string {
	return filepath.Join(string(s), "drafts", fmt.Sprintf("%s__%d.json", k.FileName(), t.Unix()))
}

// LatestDraft returns the most recently written draft for k. Files that
// cannot be decoded are skipped.
func (s Store) LatestDraft(k PRKey) (Draft, bool) {
	if !ValidName(k.Owner) || !ValidName(k.Repo) || k.Number <= 0 {
		return Draft{}, false
	}
	prefix := k.FileName() + "__"
	paths, err := filepath.Glob(filepath.Join(string(s), "drafts", prefix+"*.json"))
	if err != nil {
		return Draft{}, false
	}
	type stamped struct {
		path string
		at   int64
	}
	found := make([]stamped, 0, len(paths))
	for _, p := range paths {
		ts := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), prefix), ".json")
		at, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		found = append(found, stamped{p, at})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at > found[j].at })
	for _, f := range found {
		if d, err := readDraft(f.path); err == nil {
			return d, true
		}
	}
	return Draft{}, false
}

// Lock takes an exclusive, non-blocking lock on the watch. The returned
// function releases it.
func (s Store) Lock() (func(), error) {
	p := filepath.Join(string(s), "lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(p)
		f.Close()
		return nil, fmt.Errorf("%w (pid %s)", ErrLocked, strings.TrimSpace(string(b)))
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
