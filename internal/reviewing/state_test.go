package reviewing

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var monoKey = PRKey{"chainguard-dev", "mono", 62436}

func TestStoreRoundTrip(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "sustaining-automation")
	require.NoError(t, err)

	st, err := s.LoadPR(monoKey)
	require.NoError(t, err)
	assert.Equal(t, PRState{Key: monoKey}, st, "missing file yields a fresh state")

	st.Status = StatusWaitingOnCI
	st.Ledger = []Finding{
		{
			ID:       "f1",
			ThreadID: "PRRT_1",
			Path:     "bots/kev/main.go",
			Line:     12,
			Blocking: true,
			Summary:  "nil deref",
			Status:   FindingOutstanding,
		},
	}
	st.ReviewsByDay = map[string]int{"2026-10-05": 2}
	st.Claim = &Claim{
		SessionID: "s1",
		PID:       42,
		Since:     time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Head:      "h",
	}
	require.NoError(t, s.SavePR(st))

	got, err := s.LoadPR(monoKey)
	require.NoError(t, err)
	assert.Equal(t, st, got)

	info, err := os.Stat(filepath.Join(string(s), "prs", monoKey.FileName()+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	all, err := s.AllPRs()
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestStoreCorruptFileMovedAside(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "w")
	require.NoError(t, err)
	p := filepath.Join(string(s), "prs", monoKey.FileName()+".json")
	require.NoError(t, os.WriteFile(p, []byte("{not json"), 0o600))

	st, err := s.LoadPR(monoKey)
	require.ErrorIs(t, err, ErrCorrupt)
	assert.Equal(t, monoKey, st.Key)
	matches, _ := filepath.Glob(p + ".corrupt-*")
	assert.Len(t, matches, 1)
	_, err = os.Stat(p)
	assert.True(t, os.IsNotExist(err))
}

func TestOpenStoreRejectsUnsafeWatch(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", "", "UPPER"} {
		_, err := OpenStore(t.TempDir(), name)
		assert.Error(t, err, name)
	}
}

func TestSpend(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "w")
	require.NoError(t, err)
	total, err := s.AddSpend("2026-10-05", 2.5)
	require.NoError(t, err)
	assert.Equal(t, 2.5, total)
	total, err = s.AddSpend("2026-10-05", 1.25)
	require.NoError(t, err)
	assert.Equal(t, 3.75, total)
	assert.Equal(t, 3.75, s.Spend("2026-10-05"))
	assert.Equal(t, 0.0, s.Spend("2026-10-06"))
}

func TestInitializedMarker(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "w")
	require.NoError(t, err)
	assert.False(t, s.Initialized())
	require.NoError(t, s.MarkInitialized())
	assert.True(t, s.Initialized())
}

func TestLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	s1, err := OpenStore(root, "w")
	require.NoError(t, err)
	unlock, err := s1.Lock()
	require.NoError(t, err)

	s2, err := OpenStore(root, "w")
	require.NoError(t, err)
	_, err = s2.Lock()
	require.True(t, errors.Is(err, ErrLocked), "got %v", err)
	assert.Contains(t, err.Error(), "pid ")

	unlock()
	unlock2, err := s2.Lock()
	require.NoError(t, err)
	unlock2()
}

func TestOutstandingCounts(t *testing.T) {
	st := PRState{Ledger: []Finding{
		{Blocking: true, Status: FindingOutstanding},
		{Blocking: false, Status: FindingOutstanding},
		{Blocking: true, Status: FindingAddressed},
		{Blocking: false, Status: FindingAnswered},
	}}
	b, nb := st.OutstandingCounts()
	assert.Equal(t, 1, b)
	assert.Equal(t, 1, nb)
}

func TestDraftPath(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "w")
	require.NoError(t, err)
	p := s.DraftPath(monoKey, time.Unix(1791219000, 0))
	assert.Equal(
		t,
		filepath.Join(string(s), "drafts", "chainguard-dev__mono__62436__1791219000.json"),
		p,
	)
}

func TestDay(t *testing.T) {
	assert.Equal(t, "2026-10-05", Day(time.Date(2026, 10, 5, 23, 0, 0, 0, time.Local)))
}
