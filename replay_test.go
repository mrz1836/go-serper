package serper_test

import (
	"go/version"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// rplClient returns a client whose calls of every operation go through hc to
// base.
func rplClient(tb testing.TB, hc *http.Client, base string) *serper.Client {
	tb.Helper()
	return newClient(tb, serper.WithHTTPClient(hc), serper.WithSearchBaseURL(base), serper.WithScrapeBaseURL(base))
}

// rplClass returns the transport class that err's text names, failing tb
// unless err is a transport CallError whose class is one of allowed.
func rplClass(tb testing.TB, err error, allowed ...string) string {
	tb.Helper()
	ce := callError(tb, err)
	prefix := "serper: " + string(ce.Meta.Op) + ": " + string(serper.FailureTransport) + ": "
	class, ok := strings.CutPrefix(ce.Error(), prefix)
	require.True(tb, ok, "want a transport failure, got %q", ce.Error())
	require.Contains(tb, allowed, class)
	return class
}

// rplH2Transport is one way to build an HTTP/2-capable transport that trusts
// an H2Server.
type rplH2Transport struct {
	name  string
	build func(tb testing.TB, srv *testutil.H2Server) *http.Transport
}

// rplH2Transports returns the server's own HTTP/2 transport and a clone of
// http.DefaultTransport, the standard library's default HTTP/2-capable
// transport, made to trust the server. Both dial loopback only.
func rplH2Transports() []rplH2Transport {
	return []rplH2Transport{
		{name: "server transport", build: func(tb testing.TB, srv *testutil.H2Server) *http.Transport {
			tb.Helper()
			return testutil.GuardTransport(tb, srv.Transport())
		}},
		{name: "default transport", build: rplDefaultTransport},
	}
}

// rplDefaultTransport returns a clone of http.DefaultTransport that trusts
// srv, reads no proxy settings and dials loopback only. The clone keeps
// ForceAttemptHTTP2, so it negotiates h2 with srv.
func rplDefaultTransport(tb testing.TB, srv *testutil.H2Server) *http.Transport {
	tb.Helper()
	dt, ok := http.DefaultTransport.(*http.Transport)
	require.True(tb, ok, "http.DefaultTransport is an *http.Transport")
	tr := dt.Clone()
	require.True(tb, tr.ForceAttemptHTTP2)
	tr.Proxy = nil
	tr.TLSClientConfig = srv.Transport().TLSClientConfig
	return testutil.GuardTransport(tb, tr)
}

// rplH2Client returns an http.Client over a new transport tc makes for srv,
// whose idle connections close when the test ends.
func rplH2Client(tb testing.TB, tc rplH2Transport, srv *testutil.H2Server) *http.Client {
	tb.Helper()
	tr := tc.build(tb, srv)
	tb.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// rplResendsAfterProtocolError reports whether this Go version's HTTP/2
// client resends a rewindable request after a PROTOCOL_ERROR stream reset:
// releases before Go 1.27 do; Go 1.27 and later, and development toolchains,
// do not (see testutil.H2Fault).
func rplResendsAfterProtocolError() bool {
	v, _, _ := strings.Cut(runtime.Version(), " ") // drop a GOEXPERIMENT suffix, such as " X:nojsonv2"
	return version.IsValid(v) && version.Compare(v, "go1.27") < 0
}

// TestReplayHTTP1DroppedRequestIsSentOnce drops the second call of every
// operation on a reused keep-alive connection after the server read the
// whole request, through the server's own client injected with
// WithHTTPClient. The call is sent exactly once (the server reads one more
// request and accepts no new connection) and fails as a transport failure
// whose outcome is unknown, since the server may have processed it.
func TestReplayHTTP1DroppedRequestIsSentOnce(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			srv := testutil.NewDropServer(t, testutil.Step{Body: o.ok}, testutil.DropNth(2))
			c := rplClient(t, srv.Client(), srv.URL())

			require.NoError(t, o.call(t.Context(), c).err, "the first call warms up the connection")
			require.Equal(t, 1, srv.Hits())
			require.Equal(t, 1, srv.Conns())

			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			class := rplClass(t, got.err, "closed", "reset")
			_ = assertCallError(t, got.err, o.wantAt(srv.URL(), wantCall{
				Kind: serper.FailureTransport, OutcomeUnknown: true, Attempts: 1,
				RawCapture: serper.RawCaptureNoResponse, Class: class,
			}))
			assert.Equal(t, 2, srv.Hits(), "the dropped call was read once and never resent")
			assert.Equal(t, 1, srv.Conns(), "no new connection was dialed for a resend")
		})
	}
}

// TestReplayH2FaultsAreSentOnce faults the first request on an H2Server with
// each fault, after which the standard library's HTTP/2 client resends a
// request whose body it can rewind (see
// TestReplayHarnessSeesStandardLibraryResends). Every operation, through the
// server's own HTTP/2 transport and through a clone of http.DefaultTransport,
// sends exactly one HEADERS frame and fails: its request has no GetBody, so
// the HTTP/2 client refuses to resend it ("cannot retry ... after
// Request.Body was written", which wraps nothing, so the class is "other"),
// with an unknown outcome. A later call is answered
// (HTTP 200 with an empty body, which no operation can decode), so a resend
// would have been answered and counted.
func TestReplayH2FaultsAreSentOnce(t *testing.T) {
	for _, fault := range testutil.H2Faults() {
		for _, tc := range rplH2Transports() {
			for _, o := range opCases() {
				t.Run(string(fault)+"/"+tc.name+"/"+o.name, func(t *testing.T) {
					srv := testutil.NewH2Server(t, fault)
					c := rplClient(t, rplH2Client(t, tc, srv), srv.URL())

					got := o.call(t.Context(), c, serper.WithRawCapture(true))
					assert.Equal(t, 1, srv.Headers(), "sent exactly once")
					_ = assertCallError(t, got.err, o.wantAt(srv.URL(), wantCall{
						Kind: serper.FailureTransport, OutcomeUnknown: true, Attempts: 1,
						RawCapture: serper.RawCaptureNoResponse, Class: "other",
					}))

					got = o.call(t.Context(), c, serper.WithRawCapture(true))
					assert.Equal(t, 2, srv.Headers(), "the next call is a request of its own")
					_ = assertCallError(t, got.err, o.wantAt(srv.URL(), wantCall{
						Kind: serper.FailureMalformedResponse, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
						RawCapture: serper.RawCaptureFailed,
					}))
				})
			}
		}
	}
}

// TestReplayHarnessSeesStandardLibraryResends proves that the servers above
// count the standard library's own resends on the Go version running them,
// with control requests that do not go through the SDK: a POST built with
// http.NewRequestWithContext over a bytes.Reader, so that GetBody is set.
func TestReplayHarnessSeesStandardLibraryResends(t *testing.T) {
	// HTTP/1 resends a request on a reused connection that fails before the
	// first response byte only when Request.isReplayable holds: GetBody set
	// (or no body) and an idempotent method, or an Idempotency-Key header. A
	// POST with GetBody alone is therefore not resent after the drop, on
	// every supported Go version: the server reads it once and the client
	// sees the failure.
	t.Run("http1 POST with GetBody", func(t *testing.T) {
		srv := testutil.NewDropServer(t, testutil.Step{Body: "{}"}, testutil.DropNth(2))
		hc := srv.Client()
		r, err := testutil.Post(t, hc, srv.URL(), controlPayload, nil)
		require.NoError(t, err, "warm-up")
		assert.Equal(t, testutil.PostReply{Status: http.StatusOK, Proto: "HTTP/1.1"}, r)

		_, err = testutil.Post(t, hc, srv.URL(), controlPayload, nil)
		require.Error(t, err, "not replayable, so never resent")
		assert.Equal(t, 2, srv.Hits(), "warm-up and the dropped POST")
		assert.Equal(t, 1, srv.Conns())
	})

	// With an Idempotency-Key, the same POST is replayable: the standard
	// library rewinds it with GetBody and resends it on a new connection, and
	// the server sees the extra request. The SDK sends neither GetBody nor an
	// Idempotency-Key.
	t.Run("http1 POST with GetBody and Idempotency-Key", func(t *testing.T) {
		srv := testutil.NewDropServer(t, testutil.Step{Body: "{}"}, testutil.DropNth(2))
		hc := srv.Client()
		header := http.Header{"Idempotency-Key": {"rpl-key-1"}}
		_, err := testutil.Post(t, hc, srv.URL(), controlPayload, header)
		require.NoError(t, err, "warm-up")

		r, err := testutil.Post(t, hc, srv.URL(), controlPayload, header)
		require.NoError(t, err, "the dropped POST is resent and then answered")
		assert.Equal(t, testutil.PostReply{Status: http.StatusOK, Proto: "HTTP/1.1"}, r)
		assert.Equal(t, 3, srv.Hits(), "warm-up, the dropped POST and its resend")
		assert.Equal(t, 2, srv.Conns(), "the resend dialed a new connection")
	})

	// HTTP/2 resends a request with GetBody after REFUSED_STREAM and GOAWAY,
	// whatever its method, and before Go 1.27 after PROTOCOL_ERROR too: the
	// server reads a second HEADERS frame for one request.
	for _, fault := range testutil.H2Faults() {
		for _, tc := range rplH2Transports() {
			t.Run("http2 POST with GetBody/"+string(fault)+"/"+tc.name, func(t *testing.T) {
				srv := testutil.NewH2Server(t, fault)
				r, err := testutil.Post(t, rplH2Client(t, tc, srv), srv.URL()+pathSearch, controlPayload, nil)
				if fault != testutil.H2ProtocolError || rplResendsAfterProtocolError() {
					require.NoError(t, err, "resent, then answered")
					assert.Equal(t, testutil.PostReply{Status: http.StatusOK, Proto: "HTTP/2.0"}, r)
					assert.Equal(t, 2, srv.Headers(), "the faulted request and its resend")
					return
				}
				require.Error(t, err, "Go 1.27 and later no longer resend after PROTOCOL_ERROR")
				assert.Equal(t, 1, srv.Headers())
			})
		}
	}
}
