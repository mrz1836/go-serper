package serper

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Transport settings of the SDK's own transport.
const (
	// dialTimeout and tlsHandshakeTimeout bound connection setup, 20 seconds
	// together, which DefaultTimeout leaves room for.
	dialTimeout         = 10 * time.Second
	dialKeepAlive       = 30 * time.Second
	tlsHandshakeTimeout = 10 * time.Second

	// idleConnTimeout is short, so that a pooled connection the server is
	// about to close is rarely picked for the next call: such a call fails
	// with a transport error, and the SDK never resends it.
	idleConnTimeout     = 4 * time.Second
	maxIdleConnsPerHost = 8

	// maxIdleConns covers both origins.
	maxIdleConns = 2 * maxIdleConnsPerHost

	// maxResponseHeaderBytes bounds the response headers on the SDK's own
	// transport.
	maxResponseHeaderBytes = 64 << 10
)

// sender makes the one attempt of a call: it builds the request for body on
// ep and sends it. Everything it needs, the API key included, is captured
// inside the function: fmt prints a func value only as an address, and
// reflection-based dumpers cannot see what a closure captures. An error is the
// http.Client's own, which the caller classifies and drops.
type sender func(ctx context.Context, ep *endpoint, body *sentBody) (*http.Response, error)

// newSender returns the sender for a validated configuration.
func newSender(hc *http.Client, apiKey, userAgent string) sender {
	return func(ctx context.Context, ep *endpoint, body *sentBody) (*http.Response, error) {
		return hc.Do(newRequest(ctx, ep, body, apiKey, userAgent))
	}
}

// newTransport returns the SDK's own transport, built from scratch rather
// than cloned from http.DefaultTransport, whose settings would bring HTTP/2
// and the proxy environment variables back in:
//   - HTTP/1.1 only, so that the HTTP/2 client's own resends (after a refused
//     stream, a GOAWAY or a protocol error) never apply;
//   - TLS 1.2 or later;
//   - no proxy: the proxy environment variables are never read (inject an
//     http.Client to use a proxy);
//   - its own small connection pool with a short idle timeout;
//   - no response-header timeout: the call's context deadline is the one
//     clock.
func newTransport() *http.Transport {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}
	return &http.Transport{
		Proxy:       nil,
		DialContext: dialer.DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
		TLSNextProto:           map[string]func(string, *tls.Conn) http.RoundTripper{},
		Protocols:              &protocols,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		MaxIdleConns:           maxIdleConns,
		MaxIdleConnsPerHost:    maxIdleConnsPerHost,
		IdleConnTimeout:        idleConnTimeout,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
}

// refuseRedirect stops the client at the first response, so the API key is
// never sent to another URL.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// redirectGuard removes the Location header from every 3xx response before the
// http.Client sees it. The client then returns the response as it is: it
// neither follows the redirect, which would resend the API key header and the
// body to the new URL, with a Referer quoting the old one, nor fails on an
// unparsable Location with an error that quotes it.
//
// It has no CloseIdleConnections method, so that the copy of an injected
// http.Client never reaches the caller's transport that way.
type redirectGuard struct {
	next http.RoundTripper
}

// RoundTrip passes r to the wrapped transport and strips Location, under any
// spelling of the header name, from a 3xx. It changes a copy of the response
// and of its header, never the transport's own, which a fake transport may
// share between responses. A transport that returns both a response and an
// error breaks the RoundTripper contract: its response body is closed and only
// the error is returned.
func (g redirectGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := g.next.RoundTrip(r)
	switch {
	case resp == nil:
		return nil, err
	case err != nil:
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	case resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest:
		return resp, nil
	}
	stripped := *resp
	stripped.Header = make(http.Header, len(resp.Header))
	for k, vs := range resp.Header {
		if !strings.EqualFold(k, "Location") {
			stripped.Header[k] = vs
		}
	}
	return &stripped, nil
}

// transportClass is the fixed class a transport error is reduced to.
type transportClass uint8

// Transport classes.
const (
	classNone transportClass = iota
	classDNS
	classDial
	classTLS
	classCertificate // a TLS certificate that failed verification, in the handshake
	classProxy
	classReset
	classClosed
	classStalled
	classOther
)

// String returns the class's short name, such as "reset".
func (c transportClass) String() string {
	switch c {
	case classDNS:
		return "dns"
	case classDial:
		return "dial"
	case classTLS, classCertificate:
		return "tls"
	case classProxy:
		return "proxy"
	case classReset:
		return "reset"
	case classClosed:
		return "closed"
	case classStalled:
		return "stalled"
	case classOther:
		return "other"
	case classNone:
	}
	return ""
}

// cause returns the fixed error of the class.
func (c transportClass) cause() error {
	switch c {
	case classDNS:
		return errTransportDNS
	case classDial:
		return errTransportDial
	case classTLS, classCertificate:
		return errTransportTLS
	case classProxy:
		return errTransportProxy
	case classReset:
		return errTransportReset
	case classClosed:
		return errTransportClosed
	case classStalled:
		return errTransportStalled
	case classNone, classOther:
	}
	return errTransportOther
}

// beforeConnection reports whether the class shows that no connection to the
// server was made, so the request cannot have been processed: a DNS or dial
// failure, a failure to connect to a proxy (net/http's "proxyconnect"), or a
// certificate refused in the TLS handshake. A proxy that refuses the tunnel
// returns a plain error, classed as other. Any other TLS failure, such as a
// bad record or an alert, can come after the request was written.
func (c transportClass) beforeConnection() bool {
	return c == classDNS || c == classDial || c == classCertificate || c == classProxy
}

// classOf reduces a transport error to one of a few fixed classes.
func classOf(err error) transportClass {
	if errors.Is(err, errTransportStalled) {
		return classStalled
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return classDNS
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return classCertificate
	}
	var op string
	if oe, ok := errors.AsType[*net.OpError](err); ok {
		op = oe.Op
	}
	switch {
	case op == "proxyconnect":
		return classProxy
	case isTLSError(err):
		return classTLS
	case errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE):
		return classReset
	case op == "dial":
		return classDial
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed):
		return classClosed
	}
	return classOther
}

// isTLSError reports whether err comes from the TLS handshake or record layer.
// Over TCP, crypto/tls reports an alert as a *net.OpError whose Op is
// "remote error" (received) or "local error" (sent), wrapping an unexported
// type.
func isTLSError(err error) bool {
	if oe, ok := errors.AsType[*net.OpError](err); ok && (oe.Op == "remote error" || oe.Op == "local error") {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	_, ok := errors.AsType[tls.AlertError](err)
	return ok
}

// isNetTimeout reports whether any error in err's tree reports a timeout,
// such as a dial or TLS handshake timeout. It walks the whole tree, because
// the *url.Error that http.Client adds reports a timeout only when its direct
// cause does, and a transport may wrap a timeout further down.
func isNetTimeout(err error) bool {
	if err == nil {
		return false
	}
	if t, ok := err.(interface{ Timeout() bool }); ok && t.Timeout() {
		return true
	}
	switch u := err.(type) { //nolint:errorlint // walking the tree by hand
	case interface{ Unwrap() error }:
		return isNetTimeout(u.Unwrap())
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), isNetTimeout)
	}
	return false
}
