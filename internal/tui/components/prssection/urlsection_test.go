package prssection

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/data"
)

func TestFetchUrlSectionUsesPerPRFetch(t *testing.T) {
	var fetched []string
	orig := fetchPullRequestByURL
	fetchPullRequestByURL = func(url string) (data.EnrichedPullRequestData, error) {
		fetched = append(fetched, url)
		return data.EnrichedPullRequestData{Url: url, Title: "omnibump: support gradle force", Number: 88}, nil
	}
	t.Cleanup(func() { fetchPullRequestByURL = orig })

	prs, err := fetchUrlSection([]string{
		"https://github.com/chainguard-dev/omnibump/pull/88",
		"https://github.com/chainguard-dev/mono/pull/62436",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://github.com/chainguard-dev/omnibump/pull/88",
		"https://github.com/chainguard-dev/mono/pull/62436",
	}, fetched)
	require.Len(t, prs, 2)
	assert.Equal(t, "omnibump: support gradle force", prs[0].Primary.Title)
	assert.True(t, prs[0].IsEnriched)
}

func TestFetchUrlSectionPropagatesErrors(t *testing.T) {
	orig := fetchPullRequestByURL
	fetchPullRequestByURL = func(string) (data.EnrichedPullRequestData, error) {
		return data.EnrichedPullRequestData{}, errors.New("API rate limit exceeded")
	}
	t.Cleanup(func() { fetchPullRequestByURL = orig })

	_, err := fetchUrlSection([]string{"https://github.com/chainguard-dev/omnibump/pull/88"})
	assert.ErrorContains(t, err, "rate limit")
}
