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

func TestParseResultStreamRecordsReads(t *testing.T) {
	// Captured from claude 2.1.280 with --output-format stream-json --verbose.
	out, err := os.ReadFile("testdata/claude-stream.jsonl")
	require.NoError(t, err)
	res, err := ParseResult(out)
	require.NoError(t, err)
	require.NoError(t, res.Err())
	assert.Equal(t, 5, res.Turns)
	assert.JSONEq(t, `{"ok":"done"}`, string(res.Structured))
	assert.Equal(t, []string{"/tmp/sj/a.txt"}, res.Reads)
	assert.Equal(t, 1, res.Searches, "one Grep")
}

func TestParseResultIgnoresFailedReads(t *testing.T) {
	out := `{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/kev.go"}},` +
		`{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/w/missing_test.go"}},` +
		`{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/w/./kev.go"}}]}}
{"type":"user","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"t1"},` +
		`{"type":"tool_result","tool_use_id":"t2","is_error":true}]}}
{"type":"result","subtype":"success","num_turns":2,"session_id":"s1"}
`
	res, err := ParseResult([]byte(out))
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"/w/kev.go"},
		res.Reads,
		"failed reads drop out; paths are cleaned and deduplicated",
	)
}

func TestParseResultRecordsReadRanges(t *testing.T) {
	// The read pattern of mono#58771's review: the diff's first 950 lines,
	// a whole file, a slice of a caller, then the diff's tail.
	out := `{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/s/pr.diff","limit":950}},` +
		`{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/w/evasion.go"}},` +
		`{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/w/reconciler.go","offset":2060,"limit":140}},` +
		`{"type":"tool_use","id":"t4","name":"Read","input":{"file_path":"/s/pr.diff","offset":"3643"}},` +
		`{"type":"tool_use","id":"t5","name":"Read","input":{"file_path":"/w/gone.go","offset":1}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t5","is_error":true}]}}
{"type":"result","subtype":"success","num_turns":5,"session_id":"s1"}
`
	res, err := ParseResult([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/pr.diff", "/w/evasion.go", "/w/reconciler.go"}, res.Reads)
	assert.ElementsMatch(t, []LineRange{{1, 950}, {3643, 5642}}, res.Ranges["/s/pr.diff"],
		"a numeric string offset still counts")
	assert.Equal(
		t,
		[]LineRange{{1, 2000}},
		res.Ranges["/w/evasion.go"],
		"no limit reads 2,000 lines",
	)
	assert.Equal(t, []LineRange{{2060, 2199}}, res.Ranges["/w/reconciler.go"])
	assert.NotContains(t, res.Ranges, "/w/gone.go", "failed reads cover nothing")
}

func TestParseResultToleratesOddReadInputs(t *testing.T) {
	out := `{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/a.go","offset":null,"limit":"all"}}]}}
{"type":"result","subtype":"success","num_turns":1,"session_id":"s1"}
`
	res, err := ParseResult([]byte(out))
	require.NoError(t, err, "an unexpected offset or limit never loses the result")
	assert.Equal(t, []LineRange{{1, 2000}}, res.Ranges["/w/a.go"])
}
