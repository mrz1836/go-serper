package serper_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// cctLimit is the response limit of the in-memory clients, so that an
// oversized body stays small.
const cctLimit = 1024

// cctSlowTimeout is the timeout of the client whose calls the servers hold
// until it gives up: short enough to wait out. The calls before and after
// such a call go through a client with the default timeout on the same
// transport, so that a new connection's dial and TLS handshake, slow under
// the race detector on a loaded machine, never race it.
const cctSlowTimeout = 250 * time.Millisecond

// cctOptions returns the options of the in-memory clients: a 1 KiB response
// limit and a minute's timeout, which a synctest bubble waits out at once.
func cctOptions() []serper.Option {
	return []serper.Option{serper.WithMaxResponseBytes(cctLimit), serper.WithTimeout(time.Minute)}
}

// cctAssertFailure asserts that got failed with a CallError of kind that
// matches its sentinel and reports attempts attempts, with an elapsed time
// exactly when there was one.
func cctAssertFailure(tb testing.TB, got outcome, kind serper.FailureKind, attempts int) {
	tb.Helper()
	ce := callError(tb, got.err)
	assert.Equal(tb, kind, ce.Kind)
	require.ErrorIs(tb, got.err, kindSentinels()[kind])
	assert.Equal(tb, attempts, ce.Meta.Attempts, "Meta.Attempts")
	assert.Equal(tb, attempts == 1, ce.Meta.Elapsed > 0, "Elapsed is positive exactly when a request was made")
}

// cctCallMode calls o on c with the value that makes cctServe answer in
// mode: the query "cct <mode>", or a scrape target ending in "/<mode>".
func cctCallMode(ctx context.Context, c *serper.Client, o opCase, mode string) outcome {
	switch o.name {
	case "Search":
		return outcomeOf(c.Search(ctx, serper.SearchRequest{Query: "cct " + mode}))
	case "Images":
		return outcomeOf(c.Images(ctx, serper.ImagesRequest{Query: "cct " + mode}))
	}
	return outcomeOf(c.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/cct/" + mode}))
}

// cctModeOf returns the mode a request body names (see cctCallMode).
func cctModeOf(body []byte) (string, bool) {
	var in struct {
		Q   string `json:"q"`
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &in) != nil {
		return "", false
	}
	if mode, found := strings.CutPrefix(in.Q, "cct "); found {
		return mode, true
	}
	i := strings.LastIndexByte(in.URL, '/')
	return in.URL[i+1:], i >= 0
}

// cctBig returns answer, a JSON object, padded past cctLimit.
func cctBig(answer string) string { return padded(answer, len(answer)+2*cctLimit) }

// cctServe answers a request in the mode its value names: ok,
// status-<code> (a 3xx with a Location back to the same path), encoding (an
// encoding the transport does not decode), type (not JSON), large-declared,
// large-read (chunked), truncated (shorter than declared), invalid-json,
// invalid-utf8, rule (no success member), drop (the connection closed after
// the request was read), block-headers or block-body (held until the client
// gives up).
func cctServe(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	mode, ok := cctModeOf(data)
	if err != nil || !ok {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	answer := okBody(r.URL.Path)
	h := w.Header()
	h.Set("Content-Type", mediaJSON)
	code := http.StatusOK
	switch mode {
	case "encoding":
		h.Set("Content-Encoding", "br")
	case "type":
		h.Set("Content-Type", "text/plain; charset=utf-8")
	case "large-declared":
		answer = cctBig(answer)
		h.Set("Content-Length", strconv.Itoa(len(answer)))
	case "large-read":
		big := cctBig(answer)
		_, _ = io.WriteString(w, big[:512])
		_ = http.NewResponseController(w).Flush() // sent chunked, without a Content-Length
		_, _ = io.WriteString(w, big[512:])
		return
	case "truncated":
		h.Set("Content-Length", strconv.Itoa(len(answer)+10))
	case "invalid-json":
		answer = answer[:len(answer)-1]
	case "invalid-utf8":
		answer = answer[:len(answer)-1] + `,"x":"` + "\xff" + `"}`
	case "rule":
		answer = `{}`
	case "drop":
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	case "block-headers":
		<-r.Context().Done()
		return
	case "block-body":
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, answer[:5])
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		return
	default:
		if n, found := strings.CutPrefix(mode, "status-"); found {
			code, _ = strconv.Atoi(n)
		}
	}
	if code >= 300 && code < 400 {
		h.Set("Location", r.URL.Path)
	}
	w.WriteHeader(code)
	_, _ = io.WriteString(w, answer)
}

// cctServerMode is one way the servers answer a call.
type cctServerMode struct {
	mode string             // the mode cctServe reads
	kind serper.FailureKind // the failure, "" for a success
	slow bool               // the server holds the call until the client gives up
}

// cctServerModes returns every server failure mode of o: each final status,
// each HTTP 200 failure a server can send, a dropped connection, and a
// timeout before the headers and during the body.
func cctServerModes(o opCase) []cctServerMode {
	rule := serper.FailureUnexpectedResponse
	if o.scrape {
		rule = serper.FailureMalformedResponse
	}
	modes := make([]cctServerMode, 0, len(statusCodes())+12)
	for _, code := range statusCodes() {
		if code < 200 {
			continue // a server cannot end a response with a 1xx
		}
		modes = append(modes, cctServerMode{mode: "status-" + strconv.Itoa(code), kind: statusKind(code)})
	}
	return append(modes,
		cctServerMode{mode: "encoding", kind: serper.FailureUnexpectedResponse},
		cctServerMode{mode: "type", kind: serper.FailureUnexpectedResponse},
		cctServerMode{mode: "large-declared", kind: serper.FailureResponseTooLarge},
		cctServerMode{mode: "large-read", kind: serper.FailureResponseTooLarge},
		cctServerMode{mode: "truncated", kind: serper.FailureTransport},
		cctServerMode{mode: "invalid-json", kind: serper.FailureMalformedResponse},
		cctServerMode{mode: "invalid-utf8", kind: serper.FailureMalformedResponse},
		cctServerMode{mode: "rule", kind: rule},
		cctServerMode{mode: "drop", kind: serper.FailureTransport},
		cctServerMode{mode: "block-headers", kind: serper.FailureTimeout, slow: true},
		cctServerMode{mode: "block-body", kind: serper.FailureTimeout, slow: true},
	)
}

// cctServers starts two recording TLS servers running cctServe, one per
// origin, and returns them with a client of theirs, built with opts, and the
// transport that counts its round trips. httptest servers share one
// certificate, so the search server's transport trusts both.
func cctServers(tb testing.TB, opts ...serper.Option) (servers, *testutil.CountingTransport, *serper.Client) {
	tb.Helper()
	s := servers{search: testutil.NewServer(tb, cctServe), scrape: testutil.NewServer(tb, cctServe)}
	ct := &testutil.CountingTransport{Next: s.search.Client().Transport}
	return s, ct, cctServerClient(tb, s, ct, opts...)
}

// cctServerClient returns a client of s built with opts, with a 1 KiB
// response limit, whose round trips go through ct.
func cctServerClient(tb testing.TB, s servers, ct *testutil.CountingTransport, opts ...serper.Option) *serper.Client {
	tb.Helper()
	all := append([]serper.Option{
		serper.WithHTTPClient(&http.Client{Transport: ct}),
		serper.WithSearchBaseURL(s.search.URL()),
		serper.WithScrapeBaseURL(s.scrape.URL()),
		serper.WithMaxResponseBytes(cctLimit),
	}, opts...)
	return newClient(tb, all...)
}

// cctAssertAtServer calls o on c in mode and asserts that it failed with
// kind, or succeeded when kind is "", after exactly one round trip and one
// request at o's server, and none at the other.
func cctAssertAtServer(t *testing.T, s servers, ct *testutil.CountingTransport, c *serper.Client, o opCase,
	mode string, kind serper.FailureKind,
) {
	t.Helper()
	target, other := s.of(o), s.other(o)
	hits, otherHits, sent := target.Hits(), other.Hits(), ct.Attempts()
	got := cctCallMode(t.Context(), c, o, mode)
	if kind == "" {
		require.NoError(t, got.err, mode)
		assert.Equal(t, 1, got.meta.Attempts)
	} else {
		cctAssertFailure(t, got, kind, 1)
	}
	assert.Equal(t, hits+1, target.Hits(), "%s: exactly one request reached the server", mode)
	assert.Equal(t, otherHits, other.Hits(), "%s: nothing reached the other origin", mode)
	assert.Equal(t, sent+1, ct.Attempts(), "%s: exactly one round trip", mode)
}

func TestCallCountOneAttemptAtTheServer(t *testing.T) {
	s, ct, c := cctServers(t)
	slow := cctServerClient(t, s, ct, serper.WithTimeout(cctSlowTimeout))
	for _, o := range opCases() {
		for _, m := range cctServerModes(o) {
			t.Run(o.name+"/"+m.mode, func(t *testing.T) {
				client := c
				if m.slow {
					// A first call leaves a pooled connection, so that the
					// timed call only waits for the server.
					cctAssertAtServer(t, s, ct, c, o, "ok", "")
					client = slow
				}
				cctAssertAtServer(t, s, ct, client, o, m.mode, m.kind)
				// The transport works after the failure, with one more
				// request; for a timed call, through c, which shares it.
				cctAssertAtServer(t, s, ct, c, o, "ok", "")
			})
		}
	}
}

func TestCallCountDroppedRequestsAreNeverResent(t *testing.T) {
	// One answer that every operation accepts.
	const answer = `{"searchParameters":{"q":"example"},"organic":[],"images":[],"text":"Example page text."}`
	d := testutil.NewDropServer(t, testutil.Step{Body: answer}, func(n int) bool { return n%2 == 0 })
	ct := &testutil.CountingTransport{Next: d.Client().Transport}
	c := newClient(t, serper.WithHTTPClient(&http.Client{Transport: ct}),
		serper.WithSearchBaseURL(d.URL()), serper.WithScrapeBaseURL(d.URL()))
	calls := 0
	for round := range 4 {
		for _, o := range opCases() {
			calls++
			got := o.call(t.Context(), c)
			if calls%2 == 0 {
				// Dropped on the connection the previous call left pooled:
				// the standard library would resend a replayable request.
				cctAssertFailure(t, got, serper.FailureTransport, 1)
				assert.True(t, callError(t, got.err).OutcomeUnknown, "the server read the request")
			} else {
				require.NoError(t, got.err, "round %d %s", round, o.name)
			}
			assert.Equal(t, calls, d.Hits(), "round %d %s: one request per call", round, o.name)
			assert.Equal(t, calls, ct.Attempts(), "round %d %s: one round trip per call", round, o.name)
		}
	}
}

// cctAssertNothingSent asserts that no round trip was made and no request
// reached either server.
func cctAssertNothingSent(tb testing.TB, s servers, ct *testutil.CountingTransport) {
	tb.Helper()
	assert.Zero(tb, ct.Attempts(), "round trips")
	assert.Zero(tb, s.search.Hits(), "requests at the search server")
	assert.Zero(tb, s.scrape.Hits(), "requests at the scrape server")
}

func TestCallCountZeroAttempts(t *testing.T) {
	s, ct, c := cctServers(t, serper.WithDefaultRawCapture())
	t.Run("validation", func(t *testing.T) {
		for _, v := range invalidRequests() {
			t.Run(v.name, func(t *testing.T) {
				assertValidation(t, v.call(t.Context(), c), serper.ErrInvalidRequest, v.op, v.field, v.reason)
				cctAssertNothingSent(t, s, ct)
			})
		}
	})
	t.Run("nil context", func(t *testing.T) {
		var nilCtx context.Context
		for _, o := range opCases() {
			assertValidation(t, o.call(nilCtx, c).err, serper.ErrInvalidRequest, o.op, "ctx", reasonNil)
		}
		cctAssertNothingSent(t, s, ct)
	})
	t.Run("nil and zero client", func(t *testing.T) {
		for _, bad := range []*serper.Client{nil, {}} {
			for _, o := range opCases() {
				got := o.call(t.Context(), bad, serper.WithRawCapture(true))
				assertValidation(t, got.err, serper.ErrInvalidConfig, o.op, "Client", reasonNotInitialized)
			}
		}
		cctAssertNothingSent(t, s, ct)
	})
	t.Run("done context", func(t *testing.T) {
		for _, done := range doneContexts() {
			for _, o := range opCases() {
				ctx, cancel := done.ctx(t.Context())
				got := o.call(ctx, c)
				cancel()
				_ = assertCallError(t, got.err, o.wantAt(s.of(o).URL(), wantCall{
					Kind: done.kind, RawCapture: serper.RawCaptureNoResponse,
				}), prvSecrets()...)
			}
		}
		cctAssertNothingSent(t, s, ct)
	})
}

func TestCallCountSequential(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10} {
		t.Run(strconv.Itoa(n)+" calls", func(t *testing.T) {
			for _, o := range opCases() {
				c, script := newScriptClient(t, slices.Repeat([]testutil.Step{{Body: o.ok}}, n)...)
				for i := range n {
					got := o.call(t.Context(), c)
					require.NoError(t, got.err)
					assert.Equal(t, 1, got.meta.Attempts)
					assert.Equal(t, i+1, script.Calls(), "%s: one request per call", o.name)
				}
			}

			// At the servers, the same request each time, all three
			// operations interleaved on one client.
			s, ct, c := cctServers(t)
			for range n {
				for _, o := range opCases() {
					cctAssertAtServer(t, s, ct, c, o, "ok", "")
				}
			}
			assert.Equal(t, 2*n, s.search.Hits())
			assert.Equal(t, n, s.scrape.Hits())
			assert.Equal(t, 3*n, ct.Attempts())
		})
	}
}

func TestCallCountClientReusableAfterEveryFailure(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Each call is answered by the transport stored before it.
				var next atomic.Pointer[http.RoundTripper]
				ct := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return (*next.Load()).RoundTrip(r)
				})}
				c := newTransportClient(t, ct, cctOptions()...)
				sent := 0
				for _, rc := range rawCases(o) {
					ctx, cancel := rawContext(t.Context(), rc)
					rt := rc.answer(t, cancel)
					next.Store(&rt)
					got := o.call(ctx, c)
					cancel()
					attempts := 1
					if rc.fail != nil {
						attempts = rc.fail.Attempts
						cctAssertFailure(t, got, rc.fail.Kind, attempts)
					} else {
						require.NoError(t, got.err, rc.name)
					}
					sent += attempts
					assert.Equal(t, sent, ct.Attempts(), "%s: one round trip, or none for a done context", rc.name)
					cctFailWithoutSending(t, c, o)
					assert.Equal(t, sent, ct.Attempts(), "%s: failures that send nothing", rc.name)

					ok := http.RoundTripper(testutil.NewScript(testutil.Step{Body: o.ok}))
					next.Store(&ok)
					got = o.call(t.Context(), c)
					require.NoError(t, got.err, "the client works after %s", rc.name)
					assert.Equal(t, 1, got.meta.Attempts)
					sent++
					assert.Equal(t, sent, ct.Attempts(), "%s: then one more round trip", rc.name)
				}
			})
		})
	}
}

// cctFailWithoutSending makes the calls of o on c that fail before sending
// anything: an invalid request, a nil context and done contexts.
func cctFailWithoutSending(t *testing.T, c *serper.Client, o opCase) {
	t.Helper()
	require.ErrorIs(t, invalidCall(t.Context(), c, o), serper.ErrInvalidRequest)
	var nilCtx context.Context
	require.ErrorIs(t, o.call(nilCtx, c).err, serper.ErrInvalidRequest)
	for _, done := range doneContexts() {
		ctx, cancel := done.ctx(t.Context())
		cctAssertFailure(t, o.call(ctx, c), done.kind, 0)
		cancel()
	}
}
