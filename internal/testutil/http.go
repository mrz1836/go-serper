package testutil

import (
	"bytes"
	"crypto/x509"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip returns f(r).
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// CountingTransport counts the round trips it passes on to Next.
type CountingTransport struct {
	Next     http.RoundTripper
	attempts atomic.Int64
}

// RoundTrip counts the attempt and delegates to Next.
func (c *CountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.attempts.Add(1)
	return c.Next.RoundTrip(r)
}

// Attempts returns the number of round trips so far.
func (c *CountingTransport) Attempts() int { return int(c.attempts.Load()) }

// Drain reads and closes the request body, as a real transport would, and
// returns its length.
func Drain(r *http.Request) int64 {
	if r.Body == nil {
		return 0
	}
	n, _ := io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	return n
}

// BlockUntilDone is an in-memory round trip that never answers: it drains the
// request, waits for the request's context to end and returns its error.
func BlockUntilDone(r *http.Request) (*http.Response, error) {
	Drain(r)
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// Request is one request as a test saw it.
type Request struct {
	Method string
	Proto  string
	// URL is the request's URL as received: the path and query on a server,
	// the full URL in a transport.
	URL  string
	Path string
	// Host is the request's host: the Host header on a server (which net/http
	// moves out of Header), the URL's or Request.Host in a transport.
	Host    string
	Header  http.Header
	Body    []byte
	GetBody bool
	// NoBody reports a body that was exactly http.NoBody.
	NoBody bool
	// ContentLength is the declared request length (-1 for unknown).
	ContentLength int64
}

// Record reads and closes r's body and returns a copy of the request.
func Record(r *http.Request) Request {
	var body []byte
	noBody := r.Body == http.NoBody
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	return Request{
		Method:        r.Method,
		Proto:         r.Proto,
		URL:           r.URL.String(),
		Path:          r.URL.Path,
		Host:          requestHost(r),
		Header:        r.Header.Clone(),
		Body:          body,
		GetBody:       r.GetBody != nil,
		NoBody:        noBody,
		ContentLength: r.ContentLength,
	}
}

// requestHost returns r.Host, or the URL's host when it is empty.
func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

// Recorder is a fake that records the requests it answers: a Script or a
// Server.
type Recorder interface {
	Requests() []Request
}

// OnlyRequest returns the one request rec recorded, failing tb unless it
// recorded exactly one.
func OnlyRequest(tb testing.TB, rec Recorder) Request {
	tb.Helper()
	reqs := rec.Requests()
	require.Len(tb, reqs, 1, "exactly one request")
	return reqs[0]
}

// Response returns a response to r with the given status, body and declared
// length (-1 for unknown), as an in-memory transport would.
func Response(r *http.Request, status int, body io.ReadCloser, contentLength int64) *http.Response {
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          body,
		ContentLength: contentLength,
		Request:       r,
	}
}

// Transport answers every request in memory with status and the body newBody
// returns, declared with contentLength (-1 for unknown), after draining the
// request body.
func Transport(status int, contentLength int64, newBody func() io.ReadCloser) RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		Drain(r)
		return Response(r, status, newBody(), contentLength), nil
	}
}

// StaticTransport answers every request in memory with the same status and
// body, after draining the request body.
func StaticTransport(status int, body string) RoundTripFunc {
	return Transport(status, int64(len(body)), func() io.ReadCloser { return io.NopCloser(strings.NewReader(body)) })
}

// TransportWith answers every request in memory with status, header and body,
// declared with contentLength (-1 for unknown), after draining the request
// body. A nil header keeps Response's Content-Type application/json. Every
// answer shares body, so it suits a single call.
func TransportWith(status int, header http.Header, contentLength int64, body io.ReadCloser) RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		Drain(r)
		resp := Response(r, status, body, contentLength)
		if header != nil {
			resp.Header = header.Clone()
		}
		return resp, nil
	}
}

// Serve returns a handler that answers as a Script answers with step: Status
// (200 when zero), Header (Content-Type application/json when nil) and Body.
// Respond and Err mean nothing to a server and are ignored.
func Serve(step Step) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		if step.Header == nil {
			h.Set("Content-Type", "application/json")
		}
		for k, v := range step.Header {
			h[k] = slices.Clone(v)
		}
		status := step.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, step.Body)
	}
}

// Protocol selects the HTTP version a Server and its Client speak.
type Protocol string

// Protocols.
const (
	HTTP1 Protocol = "HTTP/1.1"
	HTTP2 Protocol = "HTTP/2.0"
)

// Name names p for subtests: "http1" or "http2". Unlike p itself, it has no
// slash, which -run would read as a subtest separator.
func (p Protocol) Name() string {
	if p == HTTP2 {
		return "http2"
	}
	return "http1"
}

// Protocols returns both protocols, for suites that run over each.
func Protocols() []Protocol { return []Protocol{HTTP1, HTTP2} }

// Server is a local TLS server that records every request before handing it
// to its handler, and counts the connections it accepts. Handlers run on
// server goroutines, so they should only answer; tests assert on Requests
// afterward.
type Server struct {
	srv   *httptest.Server
	conns atomic.Int32
	mu    sync.Mutex
	reqs  []Request
}

// NewServer starts an HTTP/1.1 Server; see NewServerProto.
func NewServer(tb testing.TB, h http.HandlerFunc) *Server {
	tb.Helper()
	return NewServerProto(tb, HTTP1, h)
}

// NewServerProto starts a Server speaking proto that records each request and
// then calls h, which can still read the request body. It is closed when the
// test ends. Its error log is discarded: a server closing while a client
// finishes a spare TLS handshake logs a harmless error.
func NewServerProto(tb testing.TB, proto Protocol, h http.HandlerFunc) *Server {
	tb.Helper()
	s := &Server{}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := Record(r)
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(rec.Body))
		h(w, r)
	}))
	s.srv.EnableHTTP2 = proto == HTTP2
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			s.conns.Add(1)
		}
	}
	s.srv.StartTLS()
	tb.Cleanup(s.srv.Close)
	return s
}

// URL returns the server's base URL, such as https://127.0.0.1:port.
func (s *Server) URL() string { return s.srv.URL }

// Client returns an HTTP client that trusts the server's certificate and
// speaks the server's protocol.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// RootCAs returns a pool that trusts the server's certificate, which every
// httptest server shares.
func (s *Server) RootCAs() *x509.CertPool { return certPool(s.srv) }

// Requests returns a copy of the recorded requests, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// Hits returns the number of requests received.
func (s *Server) Hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// Conns returns the number of connections accepted, counting one that never
// completes a TLS handshake, such as a plain-HTTP request.
func (s *Server) Conns() int { return int(s.conns.Load()) }

// Reset forgets the requests and connections so far.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = nil
	s.conns.Store(0)
}

// certPool returns a pool that trusts srv's certificate.
func certPool(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

// PostReply is the answer to a control request, its body read and closed.
type PostReply struct {
	Status int
	Proto  string
}

// Post sends one control request through hc, outside the SDK: a POST of body
// to url with header added, built with http.NewRequestWithContext over a
// strings.Reader, so that GetBody is set and the standard library may rewind
// the body to resend it, or to follow a 307 or 308 with it. It returns the
// final answer, or the error of hc.Do.
func Post(tb testing.TB, hc *http.Client, url, body string, header http.Header) (PostReply, error) {
	tb.Helper()
	req, err := http.NewRequestWithContext(tb.Context(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(tb, err)
	require.NotNil(tb, req.GetBody, "a strings.Reader body is rewindable")
	maps.Copy(req.Header, header)
	resp, err := hc.Do(req)
	if err != nil {
		return PostReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(tb, err)
	return PostReply{Status: resp.StatusCode, Proto: resp.Proto}, nil
}

// bodyCounters counts the reads of a response body, the bytes they returned
// and its closes.
type bodyCounters struct {
	reads  atomic.Int32
	read   atomic.Int64
	closes atomic.Int32
}

// Close counts the close.
func (c *bodyCounters) Close() error {
	c.closes.Add(1)
	return nil
}

// Reads returns the number of Read calls.
func (c *bodyCounters) Reads() int { return int(c.reads.Load()) }

// Closes returns the number of Close calls.
func (c *bodyCounters) Closes() int { return int(c.closes.Load()) }

// BytesRead returns the number of bytes read.
func (c *bodyCounters) BytesRead() int64 { return c.read.Load() }

// Body is a response body over fixed content that counts its reads, the
// bytes read from it and its closes. By default a Read returns as much of the
// content as fits, and io.EOF after it. The exported fields, set before the
// first Read, script a harder body:
//   - Step bounds the bytes a Read returns (0 for no bound);
//   - Empties reads that return (0, nil) come before each read of content;
//   - Gap is waited before each read of content;
//   - after the content, a Read returns End (io.EOF when nil), or blocks
//     until Close when Block is set, or returns (0, nil) when Stall is set.
//
// Like a real response body, a Read after Close, or one that Close
// interrupts, fails with net.ErrClosed. Create it with NewBody, inside the
// synctest bubble that uses it, if any. Read is for one goroutine; Close may
// come from any.
type Body struct {
	bodyCounters

	Step    int
	Empties int
	Gap     time.Duration
	End     error
	Block   bool
	Stall   bool

	content string
	off     int
	pending int
	done    chan struct{}
	once    sync.Once
}

// NewBody returns a Body over content.
func NewBody(content string) *Body { return &Body{content: content, done: make(chan struct{})} }

// NewBlockingBody returns a Body over content that blocks after it until it
// is closed, as a body whose server stops sending does.
func NewBlockingBody(content string) *Body {
	b := NewBody(content)
	b.Block = true
	return b
}

// Read serves the next part of the body.
func (b *Body) Read(p []byte) (int, error) {
	b.reads.Add(1)
	select {
	case <-b.done:
		return 0, net.ErrClosed
	default:
	}
	if b.off >= len(b.content) {
		return b.tail()
	}
	if b.pending < b.Empties {
		b.pending++
		return 0, nil
	}
	b.pending = 0
	if b.Gap > 0 {
		select {
		case <-b.done:
			return 0, net.ErrClosed
		case <-time.After(b.Gap):
		}
	}
	if b.Step > 0 && len(p) > b.Step {
		p = p[:b.Step]
	}
	n := copy(p, b.content[b.off:])
	b.off += n
	b.read.Add(int64(n))
	return n, nil
}

// Close ends the body; it counts every call.
func (b *Body) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.done) })
	return nil
}

// tail is a Read after the content.
func (b *Body) tail() (int, error) {
	switch {
	case b.Stall:
		return 0, nil
	case b.Block:
		<-b.done
		return 0, net.ErrClosed
	case b.End != nil:
		return 0, b.End
	}
	return 0, io.EOF
}

// EndlessBody streams 'x' forever, counting the bytes read and the closes.
type EndlessBody struct {
	bodyCounters
}

// Read fills p with 'x'.
func (b *EndlessBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	for i := range p {
		p[i] = 'x'
	}
	b.read.Add(int64(len(p)))
	return len(p), nil
}
