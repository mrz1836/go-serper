package serper

import (
	"crypto/x509"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

// The helpers that the internal test files share.

// internalKey is the synthetic key of the internal tests.
const internalKey = "test-internal-keymarker"

// newInternalClient returns a Client built with internalKey and opts, failing
// tb when NewClient fails.
func newInternalClient(tb testing.TB, opts ...Option) *Client {
	tb.Helper()
	c, err := NewClient(internalKey, opts...)
	require.NoError(tb, err)
	return c
}

// sdkTransport returns the SDK's own transport, made to trust roots (the
// system roots when nil) and to dial loopback only, whose idle connections
// close when the test ends.
func sdkTransport(tb testing.TB, roots *x509.CertPool) *http.Transport {
	tb.Helper()
	tr := testutil.GuardTransport(tb, newTransport())
	tr.TLSClientConfig.RootCAs = roots
	tb.Cleanup(tr.CloseIdleConnections)
	return tr
}

// sdkHTTPClient returns the http.Client that NewClient builds around its own
// transport tr: through the redirect guard, with refuseRedirect.
func sdkHTTPClient(tr *http.Transport) *http.Client {
	return &http.Client{Transport: redirectGuard{next: tr}, CheckRedirect: refuseRedirect}
}

// sdkClient returns a Client whose calls of every operation go to origin
// through the SDK's own transport (see sdkTransport), wrapped as NewClient
// wraps it, with CloseIdleConnections reaching it.
func sdkClient(tb testing.TB, origin string, roots *x509.CertPool) *Client {
	tb.Helper()
	c := newInternalClient(tb, WithSearchBaseURL(origin), WithScrapeBaseURL(origin))
	tr := sdkTransport(tb, roots)
	c.send = newSender(sdkHTTPClient(tr), internalKey, DefaultUserAgent)
	c.closeIdle = tr.CloseIdleConnections
	return c
}
