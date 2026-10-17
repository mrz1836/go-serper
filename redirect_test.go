package serper_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// rdrMarker is planted in every Location the redirect tests send: no error,
// formatted value or metadata may ever quote it.
const rdrMarker = "locationmarker-91d3e7"

// rdrBody is the body of every 3xx answer, and the target's answer: every
// operation would decode it as a success. A 3xx must fail by its status
// alone, and a followed redirect would turn into a success.
const rdrBody = `{"searchParameters":{"q":"example","type":"search","engine":"google"},` +
	`"organic":[],"images":[],"text":"Example page text."}`

// rdrMemoryTarget is the other origin of the in-memory tests, which never
// dial it.
const rdrMemoryTarget = "https://www.example.org"

// rdrStatuses returns the 3xx statuses every case runs with: those the
// standard library follows (301, 302, 303, 307 and 308), 300, which it never
// follows, and 304, which is not a redirect at all.
func rdrStatuses() []int { return []int{300, 301, 302, 303, 304, 307, 308} }

// rdrReply is how an rdrServer answers every request.
type rdrReply struct {
	status   int
	location []string // the Location values, none when empty
	body     string
}

// rdrServer is a recording loopback TLS server (see testutil.Server) that
// answers with its current reply.
type rdrServer struct {
	*testutil.Server

	mu    sync.Mutex
	reply rdrReply
}

// rdrNewServer starts an rdrServer answering with reply. It is closed when
// the test ends.
func rdrNewServer(tb testing.TB, reply rdrReply) *rdrServer {
	tb.Helper()
	s := &rdrServer{reply: reply}
	s.Server = testutil.NewServer(tb, s.serve)
	return s
}

// serve answers with the current reply.
func (s *rdrServer) serve(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	reply := s.reply
	s.mu.Unlock()
	h := w.Header()
	h.Set("Content-Type", mediaJSON)
	if len(reply.location) > 0 {
		h["Location"] = slices.Clone(reply.location)
	}
	w.WriteHeader(reply.status)
	_, _ = io.WriteString(w, reply.body)
}

// answer makes the server answer with reply from now on, and forgets the
// requests and connections so far.
func (s *rdrServer) answer(reply rdrReply) {
	s.mu.Lock()
	s.reply = reply
	s.mu.Unlock()
	s.Reset()
}

// rdrRig is the two origins, and a third server, the target, at which
// cross-origin Locations point, with a transport that trusts all three and
// dials loopback only. The target answers every request with rdrBody.
type rdrRig struct {
	search, scrape, target *rdrServer
	trustAll               *http.Transport
	redirects              atomic.Int32 // calls of the permissive CheckRedirect
}

// rdrNewRig starts a rig. Everything is closed when the test ends.
// httptest servers share one certificate, so trusting the search server's
// trusts all three.
func rdrNewRig(tb testing.TB) *rdrRig {
	tb.Helper()
	ok := rdrReply{status: http.StatusOK, body: rdrBody}
	r := &rdrRig{search: rdrNewServer(tb, ok), scrape: rdrNewServer(tb, ok), target: rdrNewServer(tb, ok)}
	r.trustAll = testutil.GuardTransport(tb, &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: r.search.RootCAs(), MinVersion: tls.VersionTLS12},
	})
	tb.Cleanup(r.trustAll.CloseIdleConnections)
	return r
}

// servers returns the rig's three servers.
func (r *rdrRig) servers() []*rdrServer { return []*rdrServer{r.search, r.scrape, r.target} }

// of returns the origin o goes to.
func (r *rdrRig) of(o opCase) *rdrServer {
	if o.scrape {
		return r.scrape
	}
	return r.search
}

// allowRedirect is a permissive CheckRedirect that counts its calls.
func (r *rdrRig) allowRedirect(*http.Request, []*http.Request) error {
	r.redirects.Add(1)
	return nil
}

// rdrClient is an http.Client a caller might inject; counted reports whether
// its CheckRedirect is the rig's counting one.
type rdrClient struct {
	name    string
	hc      *http.Client
	counted bool
}

// clients returns the injected clients the wire tests run through, each of
// which follows redirects on its own: a permissive CheckRedirect, a nil one,
// both over a transport that trusts all three servers, and the search
// server's own client (httptest servers share a certificate, so it trusts all
// three too).
func (r *rdrRig) clients() []rdrClient {
	return []rdrClient{
		{name: "permissive CheckRedirect", hc: &http.Client{Transport: r.trustAll, CheckRedirect: r.allowRedirect}, counted: true},
		{name: "nil CheckRedirect", hc: &http.Client{Transport: r.trustAll}},
		{name: "server client", hc: r.search.Client()},
	}
}

// client returns an SDK client whose calls go through hc to the rig's
// origins.
func (r *rdrRig) client(tb testing.TB, hc *http.Client) *serper.Client {
	tb.Helper()
	return newClient(tb, serper.WithHTTPClient(hc),
		serper.WithSearchBaseURL(r.search.URL()), serper.WithScrapeBaseURL(r.scrape.URL()))
}

// reset makes o's origin answer with reply and every other server with
// rdrBody, and forgets every request, connection and CheckRedirect call so
// far.
func (r *rdrRig) reset(o opCase, reply rdrReply) {
	for _, s := range r.servers() {
		s.answer(rdrReply{status: http.StatusOK, body: rdrBody})
	}
	r.of(o).answer(reply)
	r.redirects.Store(0)
}

// assertOnlyOrigin asserts that since the last reset exactly one request
// reached o's origin, the SDK's own (with the key in X-API-KEY only and no
// Referer), and that nothing, not even a connection, reached the other
// origin or the target, nor the caller's CheckRedirect.
func (r *rdrRig) assertOnlyOrigin(t *testing.T, o opCase) {
	t.Helper()
	origin := r.of(o)
	assertWireRequest(t, testutil.OnlyRequest(t, origin), o.path)
	for _, s := range r.servers() {
		if s == origin {
			continue
		}
		assert.Empty(t, s.Requests(), "no request reached %s", s.URL())
		assert.Zero(t, s.Conns(), "no connection reached %s", s.URL())
	}
	assert.Zero(t, r.redirects.Load(), "the caller's CheckRedirect is never called")
}

// rdrLocation is a Location a 3xx answer carries, built from the origin of
// the call, its path and the other origin.
type rdrLocation struct {
	name   string
	values func(origin, path, other string) []string
}

// rdrOne is a Location of one value.
func rdrOne(name string, value func(origin, path, other string) string) rdrLocation {
	return rdrLocation{name: name, values: func(origin, path, other string) []string {
		return []string{value(origin, path, other)}
	}}
}

// rdrLocations returns the Locations that a server can send intact: absolute
// ones at the same origin and path or another path, at the other origin over
// https and plain http, relative ones, and ones the standard library cannot
// parse (its client would fail with an error quoting them). Each holds
// rdrMarker.
func rdrLocations() []rdrLocation {
	return []rdrLocation{
		rdrOne("same origin and path", func(origin, path, _ string) string { return origin + path + "?from=" + rdrMarker }),
		rdrOne("same origin other path", func(origin, _, _ string) string { return origin + "/moved/" + rdrMarker }),
		rdrOne("other origin", func(_, path, other string) string { return other + path + "?to=" + rdrMarker }),
		rdrOne("other origin over http", func(_, _, other string) string {
			return "http://" + originHost(other) + "/" + rdrMarker
		}),
		rdrOne("scheme relative other origin", func(_, _, other string) string {
			return "//" + originHost(other) + "/" + rdrMarker
		}),
		rdrOne("absolute path", func(_, _, _ string) string { return "/" + rdrMarker }),
		rdrOne("relative path", func(_, _, _ string) string { return rdrMarker + "/next" }),
		rdrOne("query only", func(_, _, _ string) string { return "?" + rdrMarker }),
		{name: "two values", values: func(origin, _, other string) []string {
			return []string{other + "/first-" + rdrMarker, origin + "/second-" + rdrMarker}
		}},
		rdrOne("unparsable host", func(_, _, _ string) string { return "http://[::1:bad/" + rdrMarker }),
		rdrOne("unparsable escape", func(_, _, _ string) string { return "/%zz" + rdrMarker }),
		rdrOne("unparsable space in host", func(_, _, _ string) string { return "https://www.exa mple.org/" + rdrMarker }),
	}
}

// rdrControlLocations returns Locations holding control characters. A Go
// server writes them as they are, but the standard transport refuses to read
// such a header line, with an error that quotes it.
func rdrControlLocations() []rdrLocation {
	return []rdrLocation{
		rdrOne("NUL", func(_, _, _ string) string { return "https://www.example.org/\x00" + rdrMarker }),
		rdrOne("DEL", func(_, _, _ string) string { return "/\x7f" + rdrMarker }),
		rdrOne("ESC", func(_, _, _ string) string { return "\x1b[31m" + rdrMarker }),
	}
}

// rdrMemoryLocations returns every Location an in-memory transport can hand
// over: the others, plus a header value with CR LF, which no server can send.
func rdrMemoryLocations() []rdrLocation {
	all := append(rdrLocations(), rdrControlLocations()...)
	return append(all, rdrOne("CR LF", func(_, _, _ string) string {
		return "https://www.example.org/\r\n" + rdrMarker + "\r\nSet-Cookie: k=v"
	}))
}

// rdrName names a case for t.Run.
func rdrName(parts ...string) string { return strings.Join(parts, "/") }

// rdrWant is the CallError of a 3xx answer with code, with capture on, but
// for its Op, Origin and Path: unexpected_response, one attempt, the
// documented outcome of the status (known, but unknown for a 303 See Other,
// the usual answer to a POST that was processed) and a failed capture.
func rdrWant(code int) wantCall {
	return wantCall{
		Kind: serper.FailureUnexpectedResponse, OutcomeUnknown: statusOutcomeUnknown(code), Status: code,
		Attempts: 1, RawCapture: serper.RawCaptureFailed,
	}
}

// rdrAssertError asserts that err is the CallError want describes, holding no
// captured bytes, and that neither it nor its metadata, however printed,
// walked or encoded as JSON, quotes rdrMarker or any of the Location values.
func rdrAssertError(t *testing.T, err error, want wantCall, location []string) {
	t.Helper()
	secrets := append([]string{rdrMarker}, location...)
	ce := assertCallError(t, err, want, secrets...)
	raw, ok := ce.RawJSON()
	assertCapture(t, "", raw, ok, "a 3xx body is never captured")
	testutil.AssertRedacted(t, jsonOf(t, ce.Meta), secrets, "metadata JSON")
	testutil.AssertNeverPrints(t, ce.Meta, secrets...)
}

// TestRedirectsAreNeverFollowedOverTLS answers every operation with every
// 3xx and every Location a server can send, through injected clients that
// would follow it on their own. A Location sent intact fails the call by its
// status. One holding control characters is refused by the standard
// transport before the SDK sees the response, with an error that quotes the
// header line: the call fails as a transport failure of class "other" with
// an unknown outcome (the request was sent). Either way the error quotes
// nothing of the Location, exactly one request reaches the call's origin,
// and nothing is resent or sent elsewhere.
func TestRedirectsAreNeverFollowedOverTLS(t *testing.T) {
	rig := rdrNewRig(t)
	for _, set := range []struct {
		locations []rdrLocation
		want      func(code int) wantCall // without Op, Origin and Path
	}{
		{locations: rdrLocations(), want: rdrWant},
		{locations: rdrControlLocations(), want: func(int) wantCall {
			return wantCall{
				Kind: serper.FailureTransport, OutcomeUnknown: true, Attempts: 1,
				RawCapture: serper.RawCaptureNoResponse, Class: "other",
			}
		}},
	} {
		for _, cl := range rig.clients() {
			c := rig.client(t, cl.hc)
			for _, o := range opCases() {
				for _, code := range rdrStatuses() {
					for _, loc := range set.locations {
						t.Run(rdrName(cl.name, o.name, strconv.Itoa(code), loc.name), func(t *testing.T) {
							origin := rig.of(o)
							location := loc.values(origin.URL(), o.path, rig.target.URL())
							rig.reset(o, rdrReply{status: code, location: location, body: rdrBody})

							got := o.call(t.Context(), c, serper.WithRawCapture(true))
							rdrAssertError(t, got.err, o.wantAt(origin.URL(), set.want(code)), location)
							rig.assertOnlyOrigin(t, o)
						})
					}
				}
			}
		}
	}
}

// TestRedirectsAreNeverFollowedInMemory answers every operation with every
// 3xx and every Location, control characters and CR LF included, through an
// in-memory transport whose client has a permissive CheckRedirect. The call
// fails by its status, quoting nothing of the Location, after exactly one
// round trip, which the standard library could not resend, and the caller's
// CheckRedirect is never called.
func TestRedirectsAreNeverFollowedInMemory(t *testing.T) {
	for _, o := range opCases() {
		for _, code := range rdrStatuses() {
			for _, loc := range rdrMemoryLocations() {
				t.Run(rdrName(o.name, strconv.Itoa(code), loc.name), func(t *testing.T) {
					location := loc.values(o.origin(), o.path, rdrMemoryTarget)
					s := testutil.NewScript(testutil.Step{
						Status: code, Header: http.Header{"Content-Type": {mediaJSON}, "Location": location}, Body: rdrBody,
					})
					var redirects atomic.Int32
					hc := &http.Client{Transport: s, CheckRedirect: func(*http.Request, []*http.Request) error {
						redirects.Add(1)
						return nil
					}}
					c := newHTTPClientClient(t, testKey, hc)

					got := o.call(t.Context(), c, serper.WithRawCapture(true))
					rdrAssertError(t, got.err, o.want(rdrWant(code)), location)
					req := testutil.OnlyRequest(t, s)
					assert.Equal(t, o.origin()+o.path, req.URL)
					assertWireRequest(t, req, o.path)
					assertNoReplay(t, req)
					assert.Zero(t, redirects.Load(), "the caller's CheckRedirect is never called")
				})
			}
		}
	}
}

// rdrPost sends a control request through hc, outside the SDK (see
// testutil.Post): a POST to url with the test key in X-Api-Key, whose body a
// 307 or 308 can resend. It returns the final status.
func rdrPost(t *testing.T, hc *http.Client, url string) (int, error) {
	t.Helper()
	r, err := testutil.Post(t, hc, url, controlPayload, http.Header{"X-Api-Key": {testKey}, "Content-Type": {mediaJSON}})
	return r.Status, err
}

// TestRedirectHarnessSeesAFollow proves the tests above would see a followed
// redirect. Outside the SDK, through the same clients, a POST that a 3xx
// sends to the target is followed: the target counts it and sees the key and
// a Referer. One sent to http:// shows up as a connection at the target, and
// a Script records a followed request.
func TestRedirectHarnessSeesAFollow(t *testing.T) {
	rig := rdrNewRig(t)
	search := opCaseNamed("Search")
	for _, cl := range rig.clients() {
		for _, code := range []int{301, 302, 303, 307, 308} {
			t.Run(rdrName(cl.name, strconv.Itoa(code)), func(t *testing.T) {
				rig.reset(search, rdrReply{status: code, location: []string{rig.target.URL() + "/landing"}, body: rdrBody})
				status, err := rdrPost(t, cl.hc, rig.search.URL()+pathSearch)
				require.NoError(t, err)
				assert.Equal(t, http.StatusOK, status, "the target answered")

				reqs := rig.target.Requests()
				require.Len(t, reqs, 1, "the target counted the followed request")
				method, body := http.MethodGet, ""
				if code == http.StatusTemporaryRedirect || code == http.StatusPermanentRedirect {
					method, body = http.MethodPost, controlPayload
				}
				assert.Equal(t, method, reqs[0].Method)
				assert.Equal(t, "/landing", reqs[0].Path)
				assert.Equal(t, body, string(reqs[0].Body))
				assert.Equal(t, []string{testKey}, reqs[0].Header.Values("X-Api-Key"), "a follow resends the key")
				assert.Equal(t, rig.search.URL()+pathSearch, reqs[0].Header.Get("Referer"))
				if cl.counted {
					assert.Equal(t, int32(1), rig.redirects.Load(), "the permissive CheckRedirect allowed it")
				}
			})
		}
	}

	t.Run("plain http target", func(t *testing.T) {
		rig.reset(search, rdrReply{status: http.StatusFound, location: []string{
			"http://" + originHost(rig.target.URL()) + "/" + rdrMarker,
		}})
		status, err := rdrPost(t, &http.Client{Transport: rig.trustAll}, rig.search.URL()+pathSearch)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, status, "a TLS server refuses a plain-HTTP request")
		assert.Empty(t, rig.target.Requests(), "it never reaches the handler")
		assert.Equal(t, 1, rig.target.Conns(), "but it is counted as a connection")
	})

	t.Run("in-memory", func(t *testing.T) {
		s := testutil.NewScript(testutil.Step{
			Status: http.StatusFound, Header: http.Header{"Location": {rdrMemoryTarget + "/landing"}},
		})
		_, err := rdrPost(t, &http.Client{Transport: s}, searchOrigin+pathSearch)
		require.ErrorIs(t, err, testutil.ErrScriptExhausted, "the followed request found no step left")
		reqs := s.Requests()
		require.Len(t, reqs, 2, "the Script recorded the followed request")
		assert.Equal(t, rdrMemoryTarget+"/landing", reqs[1].URL)
		assert.Equal(t, []string{testKey}, reqs[1].Header.Values("X-Api-Key"))
		assert.Equal(t, searchOrigin+pathSearch, reqs[1].Header.Get("Referer"))
	})
}
