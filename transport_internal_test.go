package serper

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

// Planted standard-library-like errors.
var (
	errBadCertificate = errors.New("tls: bad certificate")
	errTLSLocal       = errors.New("tls: internal error")
	errX509           = errors.New("x509: certificate signed by unknown authority")
	errHTTPS          = errors.New("http: server gave HTTP response to HTTPS client")
	errMarker         = errors.New("transportmarker-203.0.113.9")
)

func TestNewTransportSettings(t *testing.T) {
	tr := newTransport()
	assert.Nil(t, tr.Proxy, "proxy environment variables are never read")
	require.NotNil(t, tr.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
	assert.Equal(t, []string{"http/1.1"}, tr.TLSClientConfig.NextProtos)
	assert.NotNil(t, tr.TLSNextProto)
	assert.Empty(t, tr.TLSNextProto, "an empty map disables HTTP/2")
	require.NotNil(t, tr.Protocols)
	assert.True(t, tr.Protocols.HTTP1())
	assert.False(t, tr.Protocols.HTTP2())
	assert.False(t, tr.Protocols.UnencryptedHTTP2())
	assert.Equal(t, 4*time.Second, tr.IdleConnTimeout)
	assert.Equal(t, 8, tr.MaxIdleConnsPerHost)
	assert.Equal(t, 16, tr.MaxIdleConns)
	assert.Equal(t, 10*time.Second, tr.TLSHandshakeTimeout)
	assert.Equal(t, int64(64<<10), tr.MaxResponseHeaderBytes)
	assert.Zero(t, tr.ResponseHeaderTimeout, "the call's deadline is the one clock")
	assert.False(t, tr.ForceAttemptHTTP2)
	assert.NotNil(t, tr.DialContext)
}

func TestOptionsClientCopy(t *testing.T) {
	o := defaultOptions()
	hc, closeIdle := o.client()
	require.NotNil(t, closeIdle, "the SDK owns its own transport")
	guard, ok := hc.Transport.(redirectGuard)
	require.True(t, ok)
	_, ok = guard.next.(*http.Transport)
	assert.True(t, ok)
	assert.Nil(t, hc.Jar)
	assert.Zero(t, hc.Timeout)
	require.NotNil(t, hc.CheckRedirect)
	assert.Equal(t, http.ErrUseLastResponse, hc.CheckRedirect(nil, nil))

	injected := &http.Client{Timeout: time.Minute}
	o.httpClient, o.httpClientSet = injected, true
	hc, closeIdle = o.client()
	assert.Nil(t, closeIdle, "an injected transport is the caller's")
	guard, ok = hc.Transport.(redirectGuard)
	require.True(t, ok)
	assert.Equal(t, http.DefaultTransport, guard.next, "a nil transport means http.DefaultTransport")
	assert.Zero(t, hc.Timeout, "the copy's Timeout is folded into the call deadline")
	assert.Equal(t, time.Minute, injected.Timeout, "the caller's client is unchanged")
	assert.Nil(t, injected.Transport)
}

func TestCallTimeoutFoldsTheInjectedTimeout(t *testing.T) {
	tests := []struct {
		name                  string
		opts                  []Option
		search, images, scrap time.Duration
	}{
		{name: "defaults", search: DefaultTimeout, images: DefaultTimeout, scrap: DefaultTimeout},
		{
			name: "scrape budget", opts: []Option{WithTimeout(time.Second), WithScrapeTimeout(time.Minute)},
			search: time.Second, images: time.Second, scrap: time.Minute,
		},
		{
			name: "injected timeout lower", opts: []Option{
				WithHTTPClient(&http.Client{Timeout: 2 * time.Second}), WithScrapeTimeout(time.Minute),
			},
			search: 2 * time.Second, images: 2 * time.Second, scrap: 2 * time.Second,
		},
		{
			name: "injected timeout higher", opts: []Option{
				WithHTTPClient(&http.Client{Timeout: time.Hour}), WithTimeout(time.Second),
			},
			search: time.Second, images: time.Second, scrap: time.Second,
		},
		{
			name: "injected without timeout", opts: []Option{WithHTTPClient(&http.Client{})},
			search: DefaultTimeout, images: DefaultTimeout, scrap: DefaultTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newInternalClient(t, tt.opts...)
			assert.Equal(t, tt.search, c.search.timeout)
			assert.Equal(t, tt.images, c.images.timeout)
			assert.Equal(t, tt.scrap, c.scrape.timeout)
		})
	}
}

func TestEndpoints(t *testing.T) {
	c := newInternalClient(t, WithScrapeBaseURL("https://127.0.0.1:9443/"))
	assert.Equal(t, endpoint{
		op: OpSearch, host: "google.serper.dev", origin: "https://google.serper.dev", path: "/search",
		timeout: DefaultTimeout,
	}, c.search)
	assert.Equal(t, endpoint{
		op: OpImages, host: "google.serper.dev", origin: "https://google.serper.dev", path: "/images",
		timeout: DefaultTimeout,
	}, c.images)
	assert.Equal(t, endpoint{
		op: OpScrape, host: "127.0.0.1:9443", origin: "https://127.0.0.1:9443", path: "/", timeout: DefaultTimeout,
	}, c.scrape)
}

func TestNewRequest(t *testing.T) {
	ep := &endpoint{op: OpScrape, host: "127.0.0.1:9443", origin: "https://127.0.0.1:9443", path: "/"}
	body := []byte(`{"url":"https://www.example.com/"}`)
	sent := newSentBody(body)
	req := newRequest(t.Context(), ep, sent, internalKey, "ua")
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "https://127.0.0.1:9443/", req.URL.String())
	assert.Equal(t, "127.0.0.1:9443", req.Host)
	assert.Equal(t, int64(len(body)), req.ContentLength)
	assert.Nil(t, req.GetBody, "never replayable")
	assert.NotEqual(t, http.NoBody, req.Body)
	assert.Equal(t, http.Header{
		"X-Api-Key": {internalKey}, "Content-Type": {"application/json"}, "Accept": {"application/json"},
		"User-Agent": {"ua"},
	}, req.Header)
	assert.Equal(t, t.Context(), req.Context())
	assert.False(t, sent.wasRead(), "nothing read yet")
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	assert.True(t, sent.wasRead(), "the transport read the body")
	require.NoError(t, req.Body.Close())

	// Each request gets its own header memory.
	other := newRequest(t.Context(), ep, newSentBody(body), internalKey, "ua")
	other.Header["Accept"][0] = "changed"
	other.Header["Accept"] = append(other.Header["Accept"], "more")
	assert.Equal(t, []string{"application/json"}, req.Header["Accept"])
	assert.Equal(t, []string{"application/json"}, other.Header["Content-Type"], "an append never spills over")
}

func TestRedirectGuard(t *testing.T) {
	for status := 100; status < 600; status += 50 {
		for _, code := range []int{status, 300, 301, 302, 303, 307, 308, 399} {
			kept := guardedLocation(t, code)
			assert.Equal(t, code < 300 || code > 399, kept, "status %d", code)
		}
	}
	errRT := testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1/", nil)
	resp, err := redirectGuard{next: errRT}.RoundTrip(req) //nolint:bodyclose // there is no response
	assert.Nil(t, resp)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, http.ErrUseLastResponse, refuseRedirect(nil, nil))
}

// guardedLocation passes a response of status code with a Location through
// the redirect guard and reports whether the Location survived.
func guardedLocation(t *testing.T, code int) bool {
	t.Helper()
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := testutil.Response(r, code, io.NopCloser(strings.NewReader("")), 0)
		resp.Header.Set("Location", "https://elsewhere.example.com/")
		return resp, nil
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1/", nil)
	resp, err := redirectGuard{next: rt}.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.Header.Get("Location") != ""
}

// searchAnswer answers every request with a minimal successful search.
func searchAnswer(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"searchParameters":{}}`)
}

func TestDefaultTransportSpeaksHTTP1AgainstAnH2Server(t *testing.T) {
	srv := testutil.NewServerProto(t, testutil.HTTP2, searchAnswer)
	c := sdkClient(t, srv.URL(), srv.RootCAs())
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
	require.NoError(t, err)
	assert.Equal(t, "HTTP/1.1", testutil.OnlyRequest(t, srv).Proto)
}

func TestDefaultTransportReusesConnections(t *testing.T) {
	var n atomic.Int32
	srv := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"Synthetic: slow down."}`)
			return
		}
		_, _ = io.WriteString(w, `{"searchParameters":{}}`)
	})
	c := sdkClient(t, srv.URL(), srv.RootCAs())
	for i := range 6 {
		_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
		if i%2 == 0 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrRateLimited)
		}
	}
	assert.Equal(t, 1, srv.Conns(), "one connection, reused after a success and after a small error body")

	c.CloseIdleConnections()
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
	require.NoError(t, err)
	assert.Equal(t, 2, srv.Conns(), "CloseIdleConnections closed the pooled connection")
}

func TestDefaultTransportIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	assert.Nil(t, newTransport().Proxy)
	srv := testutil.NewServer(t, searchAnswer)
	c := sdkClient(t, srv.URL(), srv.RootCAs())
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
	require.NoError(t, err, "a proxy at 127.0.0.1:1 would refuse the connection")
}

func TestDefaultTransportFailureIsBeforeConnection(t *testing.T) {
	for _, tt := range []struct {
		name  string
		class string
		// origin returns an origin the SDK's transport cannot reach.
		origin func(t *testing.T) string
	}{
		{name: "untrusted certificate", class: "tls", origin: func(t *testing.T) string {
			t.Helper()
			return testutil.NewServer(t, func(http.ResponseWriter, *http.Request) {}).URL()
		}},
		{name: "nothing listening", class: "dial", origin: func(t *testing.T) string {
			t.Helper()
			ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			require.NoError(t, err)
			addr := ln.Addr().String()
			require.NoError(t, ln.Close())
			return "https://" + addr
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := sdkClient(t, tt.origin(t), nil) // trusting only the system roots
			_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
			ce := testutil.RequireErrorAs[*CallError](t, err)
			assert.Equal(t, FailureTransport, ce.Kind)
			assert.Equal(t, "serper: search: transport: "+tt.class, ce.Error())
			assert.False(t, ce.OutcomeUnknown, "no request reached the server")
			assert.Equal(t, 1, ce.Meta.Attempts)
		})
	}
}

func TestClassOf(t *testing.T) {
	urlErr := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://google.serper.dev/search", Err: err}
	}
	tests := []struct {
		name string
		err  error
		want transportClass
	}{
		{name: "stalled", err: errTransportStalled, want: classStalled},
		{name: "dns", err: urlErr(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x"}}), want: classDNS},
		{name: "proxy", err: urlErr(&net.OpError{Op: "proxyconnect", Err: syscall.ECONNREFUSED}), want: classProxy},
		{name: "tls alert received", err: urlErr(&net.OpError{Op: "remote error", Err: errBadCertificate}), want: classTLS},
		{name: "tls alert sent", err: &net.OpError{Op: "local error", Err: errTLSLocal}, want: classTLS},
		{name: "certificate", err: urlErr(&tls.CertificateVerificationError{Err: errX509}), want: classCertificate},
		{name: "record header", err: urlErr(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), want: classTLS},
		{name: "alert", err: fmt.Errorf("w: %w", tls.AlertError(40)), want: classTLS},
		{name: "reset", err: urlErr(&net.OpError{Op: "read", Err: syscall.ECONNRESET}), want: classReset},
		{name: "aborted", err: urlErr(syscall.ECONNABORTED), want: classReset},
		{name: "broken pipe", err: urlErr(&net.OpError{Op: "write", Err: syscall.EPIPE}), want: classReset},
		{name: "dial refused", err: urlErr(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), want: classDial},
		{name: "eof", err: urlErr(io.EOF), want: classClosed},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: classClosed},
		{name: "net closed", err: urlErr(net.ErrClosed), want: classClosed},
		{name: "other", err: urlErr(errHTTPS), want: classOther},
		{name: "context", err: urlErr(context.Canceled), want: classOther},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, classOf(tt.err), tt.name)
	}
}

func TestTransportClassNames(t *testing.T) {
	tests := []struct {
		class  transportClass
		name   string
		cause  error
		before bool
	}{
		{classNone, "", errTransportOther, false},
		{classDNS, "dns", errTransportDNS, true},
		{classDial, "dial", errTransportDial, true},
		{classTLS, "tls", errTransportTLS, false},
		{classCertificate, "tls", errTransportTLS, true},
		{classProxy, "proxy", errTransportProxy, true},
		{classReset, "reset", errTransportReset, false},
		{classClosed, "closed", errTransportClosed, false},
		{classStalled, "stalled", errTransportStalled, false},
		{classOther, "other", errTransportOther, false},
		{transportClass(99), "", errTransportOther, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.name, tt.class.String(), "class %d", tt.class)
		assert.Equal(t, tt.cause, tt.class.cause(), "class %d", tt.class)
		assert.Equal(t, tt.before, tt.class.beforeConnection(), "class %d", tt.class)
	}
}

func TestIsNetTimeout(t *testing.T) {
	assert.False(t, isNetTimeout(nil))
	assert.True(t, isNetTimeout(testutil.TimeoutError{}))
	assert.True(t, isNetTimeout(&url.Error{Op: "Post", URL: "u", Err: testutil.TimeoutError{}}))
	assert.True(t, isNetTimeout(fmt.Errorf("a: %w", fmt.Errorf("b: %w", testutil.TimeoutError{}))))
	assert.True(t, isNetTimeout(errors.Join(io.EOF, fmt.Errorf("c: %w", testutil.TimeoutError{}))))
	assert.False(t, isNetTimeout(errors.Join(io.EOF, net.ErrClosed)))
	assert.False(t, isNetTimeout(io.EOF))
	assert.False(t, isNetTimeout(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}))
}

func TestClassify(t *testing.T) {
	meta := Metadata{Op: OpSearch}
	live := t.Context()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ce := classify(canceled, canceled, io.EOF, meta)
	assert.Equal(t, FailureCanceled, ce.Kind)
	assert.Equal(t, context.Canceled, ce.Unwrap())

	expired, cancel2 := context.WithTimeout(t.Context(), 0)
	defer cancel2()
	ce = classify(expired, expired, io.EOF, meta)
	assert.Equal(t, FailureDeadlineExceeded, ce.Kind)
	assert.Equal(t, context.DeadlineExceeded, ce.Unwrap())

	sdk, cancel3 := context.WithTimeoutCause(t.Context(), 0, errSDKTimeout)
	defer cancel3()
	ce = classify(live, sdk, io.EOF, meta)
	assert.Equal(t, FailureTimeout, ce.Kind)
	assert.Equal(t, context.DeadlineExceeded, ce.Unwrap())

	ce = classify(live, live, &net.OpError{Op: "dial", Err: testutil.TimeoutError{}}, meta)
	assert.Equal(t, FailureTimeout, ce.Kind)
	assert.True(t, ce.detail().class.beforeConnection(), "a dial timeout made no connection")

	ce = classify(live, live, fmt.Errorf("x: %w", context.DeadlineExceeded), meta)
	assert.Equal(t, FailureTimeout, ce.Kind)

	ce = classify(live, live, errResponseTooLarge, meta)
	assert.Equal(t, FailureResponseTooLarge, ce.Kind)

	ce = classify(live, live, &net.OpError{Op: "read", Err: syscall.ECONNRESET}, meta)
	assert.Equal(t, FailureTransport, ce.Kind)
	assert.Equal(t, errTransportReset, ce.Unwrap())
	assert.Equal(t, "serper: search: transport: reset", ce.Error())
}

func TestSendErrorsKeepNoText(t *testing.T) {
	marker := errMarker.Error()
	rt := testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: errMarker}
	})
	c := newInternalClient(t, WithHTTPClient(&http.Client{Transport: rt}))
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
	require.Error(t, err)
	testutil.AssertNeverPrints(t, err, marker, internalKey, "google.serper.dev/search")
	_, isURLErr := errors.AsType[*url.Error](err)
	assert.False(t, isURLErr)
}

// failingTransport fails every round trip with err, after reading the request
// body when read is set.
func failingTransport(err error, read bool) testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if read {
			testutil.Drain(r)
		}
		return nil, err
	}
}

func TestOutcomeFollowsTheBodyRead(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	cert := &tls.CertificateVerificationError{Err: errX509}
	alert := &net.OpError{Op: "remote error", Err: errBadCertificate}
	tests := []struct {
		name    string
		err     error
		read    bool
		unknown bool
	}{
		{name: "dial before reading", err: dial, unknown: false},
		{name: "dial after reading", err: dial, read: true, unknown: true},
		{name: "certificate before reading", err: cert, unknown: false},
		{name: "certificate after reading", err: cert, read: true, unknown: true},
		{name: "dns before reading", err: &net.DNSError{Err: "no such host", Name: "x"}, unknown: false},
		{name: "tls alert before reading", err: alert, unknown: true},
		{name: "reset before reading", err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}, unknown: true},
		{name: "other after reading", err: io.EOF, read: true, unknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newInternalClient(t, WithHTTPClient(&http.Client{Transport: failingTransport(tt.err, tt.read)}))
			_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
			ce := testutil.RequireErrorAs[*CallError](t, err)
			assert.Equal(t, FailureTransport, ce.Kind)
			assert.Equal(t, tt.unknown, ce.OutcomeUnknown)
		})
	}
}

// TestTLSFailureAfterTheRequestWasProcessed has a server read the whole
// request, as if it processed it, and then break the TLS stream with a
// plaintext record: the failure is TLS, and the outcome is unknown.
func TestTLSFailureAfterTheRequestWasProcessed(t *testing.T) {
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer certSrv.Close()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	var processed atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = raw.Close() }()
		tc := tls.Server(raw, &tls.Config{Certificates: certSrv.TLS.Certificates, NextProtos: []string{"http/1.1"}})
		if tc.HandshakeContext(t.Context()) != nil {
			return
		}
		req, rerr := http.ReadRequest(bufio.NewReader(tc))
		if rerr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		processed.Store(true)
		_, _ = raw.Write([]byte("\x17\x03\x03\x00\x20" + "0123456789abcdef0123456789abcdef"))
	}()
	roots := x509.NewCertPool()
	roots.AddCert(certSrv.Certificate())
	c := sdkClient(t, "https://"+ln.Addr().String(), roots)
	_, err = c.Search(t.Context(), SearchRequest{Query: "q"})
	// Closing the listener ends an Accept still waiting, should the call
	// have failed before it connected, so the wait cannot hang.
	_ = ln.Close()
	<-done
	ce := testutil.RequireErrorAs[*CallError](t, err)
	require.True(t, processed.Load(), "the server read the whole request")
	assert.Equal(t, FailureTransport, ce.Kind)
	assert.True(t, ce.OutcomeUnknown, "the request was processed: never report its outcome as known")
}

func TestRedirectGuardCopiesTheResponse(t *testing.T) {
	shared := http.Header{"Location": {"https://elsewhere.example.com/"}, "location": {"x"}, "X-Kept": {"1"}}
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := testutil.Response(r, http.StatusFound, io.NopCloser(strings.NewReader("")), 0)
		resp.Header = shared
		return resp, nil
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1/", nil)
	resp, err := redirectGuard{next: rt}.RoundTrip(req)
	require.NoError(t, err)
	assert.Equal(t, http.Header{"X-Kept": {"1"}}, resp.Header, "every spelling of Location is removed")
	assert.Len(t, shared, 3, "the transport's own header is never changed")
	require.NoError(t, resp.Body.Close())

	body := testutil.NewBody("")
	both := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.Response(r, http.StatusOK, body, 0), io.ErrUnexpectedEOF
	})
	resp, err = redirectGuard{next: both}.RoundTrip(req) //nolint:bodyclose // there is no response
	assert.Nil(t, resp, "a response that comes with an error is dropped")
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, 1, body.Closes(), "and its body closed")
}

func TestCloseIdleConnectionsReachesTheSDKTransport(t *testing.T) {
	srv := testutil.NewServer(t, searchAnswer)
	o := defaultOptions()
	hc, closeIdle := o.client()
	guard, ok := hc.Transport.(redirectGuard)
	require.True(t, ok)
	tr, ok := guard.next.(*http.Transport)
	require.True(t, ok)
	tr.TLSClientConfig.RootCAs = srv.RootCAs()
	testutil.GuardTransport(t, tr)
	defer closeIdle()
	get := func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL(), nil)
		require.NoError(t, err)
		resp, err := hc.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
	}
	get()
	get()
	assert.Equal(t, 1, srv.Conns(), "the pooled connection is reused")
	closeIdle()
	get()
	assert.Equal(t, 2, srv.Conns(), "closeIdle closed the SDK's own pooled connection")
}
