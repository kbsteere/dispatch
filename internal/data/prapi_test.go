package data

import (
	"io"
	"net/http"
	"strings"
	"testing"

	gh "github.com/cli/go-gh/v2/pkg/api"
	"github.com/stretchr/testify/require"
)

func TestClearEnrichmentCache(t *testing.T) {
	// Save original state
	originalCachedClient := cachedClient
	defer func() {
		cachedClient = originalCachedClient
	}()

	t.Run("clears nil cache without panic", func(t *testing.T) {
		cachedClient = nil
		require.True(t, IsEnrichmentCacheCleared(), "cache should be cleared initially")

		ClearEnrichmentCache()
		require.True(t, IsEnrichmentCacheCleared(), "cache should remain cleared")
	})

	t.Run("clears non-nil cache", func(t *testing.T) {
		// Simulate having a cached client (we use an empty struct pointer
		// since we can't create a real GraphQL client without credentials)
		cachedClient = &gh.GraphQLClient{}
		require.False(
			t,
			IsEnrichmentCacheCleared(),
			"cache should not be cleared when client is set",
		)

		ClearEnrichmentCache()
		require.True(
			t,
			IsEnrichmentCacheCleared(),
			"cache should be cleared after ClearEnrichmentCache",
		)
	})
}

func TestIsEnrichmentCacheCleared(t *testing.T) {
	// Save original state
	originalCachedClient := cachedClient
	defer func() {
		cachedClient = originalCachedClient
	}()

	t.Run("returns true when cache is nil", func(t *testing.T) {
		cachedClient = nil
		require.True(t, IsEnrichmentCacheCleared())
	})

	t.Run("returns false when cache is set", func(t *testing.T) {
		cachedClient = &gh.GraphQLClient{}
		require.False(t, IsEnrichmentCacheCleared())
	})
}

func TestSetClient(t *testing.T) {
	// Save original state
	originalClient := client
	originalCachedClient := cachedClient
	defer func() {
		client = originalClient
		cachedClient = originalCachedClient
	}()

	t.Run("sets both client and cachedClient", func(t *testing.T) {
		client = nil
		cachedClient = nil

		// SetClient with nil should set both to nil
		SetClient(nil)
		require.Nil(t, client)
		require.True(t, IsEnrichmentCacheCleared())
	})
}

// queryRecorder captures each GraphQL request body and answers with an
// empty result.
type queryRecorder struct{ bodies []string }

func (r *queryRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	r.bodies = append(r.bodies, string(b))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":{}}`)),
		Request:    req,
	}, nil
}

func TestMergeStateStatusOnlyFetchedForSinglePR(t *testing.T) {
	original := client
	t.Cleanup(func() { client = original })
	rec := &queryRecorder{}
	c, err := gh.NewGraphQLClient(
		gh.ClientOptions{Host: "github.com", AuthToken: "fake-token", Transport: rec},
	)
	require.NoError(t, err)
	client = c

	_, err = FetchPullRequests("is:open author:@me", 30, nil)
	require.NoError(t, err)
	_, err = FetchPullRequest("https://github.com/chainguard-dev/mono/pull/1")
	require.NoError(t, err)

	require.Len(t, rec.bodies, 2)
	require.NotContains(t, rec.bodies[0], "mergeStateStatus",
		"searches must not ask GitHub to compute merge state for every result")
	require.Contains(t, rec.bodies[1], "mergeStateStatus")
}
