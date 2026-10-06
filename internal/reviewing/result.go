package reviewing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
)

// OutFinding is one problem the reviewer reports, anchored to a line of the
// new version of a file.
type OutFinding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Blocking  bool   `json:"blocking"`
	Body      string `json:"body"`
	Category  string `json:"category,omitempty"`
}

// LedgerUpdate is the reviewer's verdict on one earlier finding.
type LedgerUpdate struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reply  string `json:"reply,omitempty"`
}

// ReviewOutput is the structured output the reviewer prompt must return.
type ReviewOutput struct {
	Findings      []OutFinding   `json:"findings"`
	Ledger        []LedgerUpdate `json:"ledger"`
	AgreedThreads []string       `json:"agreed_threads"`
	DesignRefs    string         `json:"design_refs"`
	Note          string         `json:"note"`
	Examined      Examined       `json:"examined"`
	// Verdict is the reviewer's call: approve, comment, or request_changes.
	// dispatch never approves unless it is approve.
	Verdict       string `json:"verdict"`
	VerdictReason string `json:"verdict_reason"`
}

// Examined is the reviewer's private account of what it read. It is never
// posted; dispatch checks it against the files the session really opened.
type Examined struct {
	Docs   []string `json:"docs"`
	Code   []string `json:"code"`
	Usages []string `json:"usages"`
	Tests  []string `json:"tests"`
}

// RunResult is the final result event of a `claude -p --output-format json` run.
type RunResult struct {
	SessionID  string
	Subtype    string
	Text       string
	IsError    bool
	Turns      int
	CostUSD    float64
	Structured json.RawMessage
	// Reads are the files the session opened with Read, excluding reads
	// that failed.
	Reads []string
	// Searches counts the session's Grep and Glob calls.
	Searches int
}

// streamEvent is one event of claude's output. Assistant events carry tool
// calls; user events carry their results.
type streamEvent struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	Result     string          `json:"result"`
	SessionID  string          `json:"session_id"`
	IsError    bool            `json:"is_error"`
	NumTurns   int             `json:"num_turns"`
	TotalCost  float64         `json:"total_cost_usd"`
	Structured json.RawMessage `json:"structured_output"`
	Message    struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Input     struct {
		FilePath string `json:"file_path"`
	} `json:"input"`
}

// decodeEvents reads claude's output: a JSON array of events, or one event
// per line (stream-json).
func decodeEvents(out []byte) ([]streamEvent, error) {
	out = bytes.TrimSpace(out)
	if len(out) > 0 && out[0] == '[' {
		var events []streamEvent
		return events, json.Unmarshal(out, &events)
	}
	var events []streamEvent
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var ev streamEvent
		if err := dec.Decode(&ev); err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// ParseResult extracts the final result and the session's file reads from
// claude's output.
func ParseResult(out []byte) (RunResult, error) {
	events, err := decodeEvents(out)
	if err != nil {
		return RunResult{}, fmt.Errorf("parsing claude output: %w", err)
	}
	reads := map[string]string{} // tool_use id -> path
	failed := map[string]struct{}{}
	var res RunResult
	found := false
	for _, ev := range events {
		for _, c := range ev.Message.Content {
			switch {
			case c.Type == "tool_use" && c.Name == "Read" && c.Input.FilePath != "":
				reads[c.ID] = filepath.Clean(c.Input.FilePath)
			case c.Type == "tool_use" && (c.Name == "Grep" || c.Name == "Glob"):
				res.Searches++
			case c.Type == "tool_result" && c.IsError:
				failed[c.ToolUseID] = struct{}{}
			}
		}
		if ev.Type != "result" {
			continue
		}
		found = true
		res.SessionID, res.Subtype, res.Text = ev.SessionID, ev.Subtype, ev.Result
		res.IsError, res.Turns, res.CostUSD = ev.IsError, ev.NumTurns, ev.TotalCost
		res.Structured = ev.Structured
		if bytes.Equal(res.Structured, []byte("null")) {
			res.Structured = nil
		}
	}
	if !found {
		return RunResult{}, errors.New("claude output has no result event")
	}
	seen := map[string]struct{}{}
	for id, p := range reads {
		if _, bad := failed[id]; bad {
			continue
		}
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			res.Reads = append(res.Reads, p)
		}
	}
	slices.Sort(res.Reads)
	return res, nil
}

func (r RunResult) Err() error {
	if r.IsError || r.Subtype != "success" {
		return fmt.Errorf("claude run ended with %s: %s", r.Subtype, r.Text)
	}
	return nil
}

// Review decodes the structured review output.
func (r RunResult) Review() (ReviewOutput, error) {
	var out ReviewOutput
	if len(r.Structured) == 0 {
		return out, errors.New("claude run returned no structured output")
	}
	if err := json.Unmarshal(r.Structured, &out); err != nil {
		return out, fmt.Errorf("decoding review output: %w", err)
	}
	return out, nil
}

// VerdictApprove is the only reviewer verdict that allows an approval.
const VerdictApprove = "approve"

// CategorySecurity marks a security finding, which is always blocking.
const CategorySecurity = "security"
