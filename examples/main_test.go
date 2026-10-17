package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
)

// demoOutput is everything the demo prints when every call succeeds.
const demoOutput = `Search (spends credits)
  1. Lighthouse tours <https://www.example.com/tours> 3 days ago
  2. Visiting hours <https://www.example.org/hours>
  knowledge graph: Example Lighthouse (opened 1901)
  credits used: 1

Images (spends credits)
  image https://images.example.net/dusk.jpg
  on page https://photos.example.com/dusk
  source "Example Photos" (photos.example.com)
  credits used: not reported (unknown, not zero)

Scrape (spends credits; Serper fetches the page)
  text: 59 bytes
  markdown: "# Example Domain"
  credits used: 2

Raw capture and offline replay
  captured 451 bytes (complete): op=search path=/search status=200 attempts=1
  replayed offline: 2 results, untyped answerBox kept: true

A changed response shape
  serper: search: malformed_response (HTTP 200): organic
  outcome unknown (may be billed): true; body kept: true (71 bytes)

Concurrent calls on one client
  3 separate calls (no cache), results [2 2 2]
`

func TestRunPrintsTheDemo(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, fakeSerper{}))
	assert.Equal(t, demoOutput, out.String())
	assert.NotContains(t, out.String(), demoKey, "the key is never printed")
}

func TestRunDescribesACanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := run(ctx, &out, fakeSerper{})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "Search (spends credits)\nstopped by the caller\n", out.String())
}

// failingSerper plays the fake, except that it answers path on origin with
// status.
type failingSerper struct {
	fakeSerper

	scrapeOrigin bool
	path         string
	status       int
	body         string
}

func (f failingSerper) search() http.Handler {
	if f.scrapeOrigin {
		return f.fakeSerper.search()
	}
	return f.wrap(f.fakeSerper.search())
}

func (f failingSerper) scrape() http.Handler {
	if !f.scrapeOrigin {
		return f.fakeSerper.scrape()
	}
	return f.wrap(f.fakeSerper.scrape())
}

// wrap answers f.path with f.status and passes every other request to next.
func (f failingSerper) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != f.path {
			next.ServeHTTP(w, r)
			return
		}
		body := f.body
		if body == "" {
			body = `{"message":"Synthetic failure."}`
		}
		reply(w, f.status, body)
	})
}

// TestRunDescribesEachFailedStep fails each step in turn and checks that run
// stops there, describes the failure, and returns it.
func TestRunDescribesEachFailedStep(t *testing.T) {
	tests := []struct {
		name string
		fake failingSerper
		want error
		last string // the line before describe's
		says string // describe's line
	}{
		{
			name: "search unauthorized", fake: failingSerper{path: "/search", status: http.StatusUnauthorized},
			want: serper.ErrUnauthorized, last: "Search (spends credits)", says: "check the API key and the account",
		},
		{
			name: "images forbidden", fake: failingSerper{path: "/images", status: http.StatusForbidden},
			want: serper.ErrForbidden, last: "Images (spends credits)", says: "check the API key and the account",
		},
		{
			name: "scrape rate limited",
			fake: failingSerper{scrapeOrigin: true, path: "/", status: http.StatusTooManyRequests},
			want: serper.ErrRateLimited, last: "Scrape (spends credits; Serper fetches the page)",
			says: "rate limited: the SDK never retries; try again later",
		},
		{
			name: "scrape upstream", fake: failingSerper{scrapeOrigin: true, path: "/", status: http.StatusBadGateway},
			want: serper.ErrUpstream, last: "Scrape (spends credits; Serper fetches the page)",
			says: "failed: serper: scrape: upstream (HTTP 502)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), &out, tt.fake)
			require.ErrorIs(t, err, tt.want)
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			require.GreaterOrEqual(t, len(lines), 2)
			assert.Equal(t, tt.last, lines[len(lines)-2])
			assert.Equal(t, tt.says, lines[len(lines)-1])
		})
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		err  error
		says string
	}{
		{
			err:  &serper.ValidationError{Err: serper.ErrInvalidRequest, Op: serper.OpSearch, Field: "Query", Reason: "required"},
			says: "refused locally, nothing sent: serper: invalid request: search: Query: required\n",
		},
		{
			err:  &serper.CallError{Kind: serper.FailureTimeout, OutcomeUnknown: true},
			says: "the SDK's timeout expired; it may be billed: true\n",
		},
		{err: errNoCapture, says: "failed: examples: the capture is missing\n"},
	}
	for i, tt := range tests {
		var out bytes.Buffer
		require.ErrorIs(t, describe(&out, tt.err), tt.err, "row %d", i)
		assert.Equal(t, tt.says, out.String(), "row %d", i)
	}
}

func TestFakeRefusesAnUnknownKey(t *testing.T) {
	for _, h := range []http.Handler{fakeSerper{}.search(), fakeSerper{}.scrape()} {
		rec := serve(t, h, http.MethodPost, "/search", "wrong-key", `{"q":"x"}`)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	}
	assert.Equal(t, http.StatusBadRequest, serve(t, fakeSerper{}.search(), http.MethodGet, "/search", demoKey, "").Code)
	assert.Equal(t, http.StatusNotFound, serve(t, fakeSerper{}.search(), http.MethodPost, "/news", demoKey, `{"q":"x"}`).Code)
	assert.Equal(t, http.StatusNotFound, serve(t, fakeSerper{}.scrape(), http.MethodPost, "/x", demoKey, `{}`).Code)
}

func TestDriftNeedsACallError(t *testing.T) {
	var out bytes.Buffer
	err := drift(t.Context(), &out, nil) // a nil Client fails validation: not a CallError
	require.ErrorIs(t, err, serper.ErrInvalidConfig)
	assert.Empty(t, strings.TrimPrefix(out.String(), "\nA changed response shape\n"))
}

// serve answers one in-memory request to h.
func serve(t *testing.T, h http.Handler, method, target, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.Header.Set("X-Api-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
