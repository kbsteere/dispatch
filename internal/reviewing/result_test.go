package reviewing

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResultEventArray(t *testing.T) {
	out, err := os.ReadFile("testdata/claude-success.json")
	require.NoError(t, err)
	res, err := ParseResult(out)
	require.NoError(t, err)
	assert.Equal(t, "success", res.Subtype)
	assert.NotEmpty(t, res.SessionID)
	assert.Greater(t, res.CostUSD, 0.0)
	require.NoError(t, res.Err())
	assert.JSONEq(t, `{"note":"hello"}`, string(res.Structured))
}

func TestParseResultMaxTurns(t *testing.T) {
	out, err := os.ReadFile("testdata/claude-max-turns.json")
	require.NoError(t, err)
	res, err := ParseResult(out)
	require.NoError(t, err)
	assert.Equal(t, "error_max_turns", res.Subtype)
	assert.Error(t, res.Err())
}

func TestParseResultSingleObject(t *testing.T) {
	res, err := ParseResult(
		[]byte(
			`{"type":"result","subtype":"success","result":"ok","session_id":"s1","num_turns":2,"total_cost_usd":0.5,"is_error":false}`,
		),
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		RunResult{SessionID: "s1", Subtype: "success", Text: "ok", Turns: 2, CostUSD: 0.5},
		res,
	)
}

func TestParseResultErrorDuringExecution(t *testing.T) {
	res, err := ParseResult(
		[]byte(
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"credit balance too low","session_id":"x"}`,
		),
	)
	require.NoError(t, err)
	assert.ErrorContains(t, res.Err(), "credit balance too low")
}

func TestParseResultNoResultEvent(t *testing.T) {
	_, err := ParseResult([]byte(`[{"type":"system","subtype":"init"}]`))
	assert.Error(t, err)
	_, err = ParseResult([]byte(`not json`))
	assert.Error(t, err)
}

func TestRunResultReview(t *testing.T) {
	res := RunResult{
		Subtype: "success",
		Structured: []byte(
			`{"findings":[{"path":"a.go","line":3,"blocking":true,"body":"x"}],"ledger":[],"agreed_threads":["T1"],"design_refs":"none","note":"n"}`,
		),
	}
	out, err := res.Review()
	require.NoError(t, err)
	assert.Equal(t, []OutFinding{{Path: "a.go", Line: 3, Blocking: true, Body: "x"}}, out.Findings)
	assert.Equal(t, []string{"T1"}, out.AgreedThreads)

	_, err = RunResult{Subtype: "success"}.Review()
	assert.Error(t, err, "missing structured output")
}
