package reviewing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// OutFinding is one problem the reviewer reports, anchored to a line of the
// new version of a file.
type OutFinding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Blocking  bool   `json:"blocking"`
	Body      string `json:"body"`
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
}

type resultEvent struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	Result     string          `json:"result"`
	SessionID  string          `json:"session_id"`
	IsError    bool            `json:"is_error"`
	NumTurns   int             `json:"num_turns"`
	TotalCost  float64         `json:"total_cost_usd"`
	Structured json.RawMessage `json:"structured_output"`
}

// ParseResult reads claude's JSON output, which is either a single result
// object or an array of events ending in a result event.
func ParseResult(out []byte) (RunResult, error) {
	out = bytes.TrimSpace(out)
	var events []resultEvent
	if len(out) > 0 && out[0] == '[' {
		if err := json.Unmarshal(out, &events); err != nil {
			return RunResult{}, fmt.Errorf("parsing claude output: %w", err)
		}
	} else {
		var ev resultEvent
		if err := json.Unmarshal(out, &ev); err != nil {
			return RunResult{}, fmt.Errorf("parsing claude output: %w", err)
		}
		events = []resultEvent{ev}
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != "result" {
			continue
		}
		structured := ev.Structured
		if bytes.Equal(structured, []byte("null")) {
			structured = nil
		}
		return RunResult{
			SessionID:  ev.SessionID,
			Subtype:    ev.Subtype,
			Text:       ev.Result,
			IsError:    ev.IsError,
			Turns:      ev.NumTurns,
			CostUSD:    ev.TotalCost,
			Structured: structured,
		}, nil
	}
	return RunResult{}, errors.New("claude output has no result event")
}

// Err reports a run that ended in an error or hit a cap.
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
