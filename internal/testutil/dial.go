package testutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ErrNotLoopback is the dial error LoopbackDialer returns for an address
// that is not a loopback IP literal.
var ErrNotLoopback = errors.New("testutil: refused to dial a non-loopback address")

// LoopbackDialer returns a DialContext function that dials only loopback IP
// literals (127.0.0.0/8 and ::1), with a port. Anything else, a host name
// included, since dialing one would need a DNS lookup, fails tb and the dial
// with ErrNotLoopback, so a test that reaches it can never touch the network.
func LoopbackDialer(tb testing.TB) func(ctx context.Context, network, addr string) (net.Conn, error) {
	tb.Helper()
	var d net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !IsLoopbackAddr(addr) {
			assert.Fail(tb, "dial refused: not a loopback IP literal")
			return nil, ErrNotLoopback
		}
		return d.DialContext(ctx, network, addr)
	}
}

// IsLoopbackAddr reports whether addr is "ip:port" for a loopback IP literal.
func IsLoopbackAddr(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	return err == nil && ap.Addr().IsLoopback()
}

// GuardTransport makes t dial only loopback addresses (see LoopbackDialer)
// and returns it.
func GuardTransport(tb testing.TB, t *http.Transport) *http.Transport {
	tb.Helper()
	t.DialContext = LoopbackDialer(tb)
	return t
}
