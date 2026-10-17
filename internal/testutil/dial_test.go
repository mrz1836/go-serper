package testutil_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:443": true,
		"127.9.8.7:1":   true,
		"[::1]:8443":    true,
		"127.0.0.1":     false,
		"10.0.0.1:443":  false,
		"localhost:443": false,
		"example.com:1": false,
		"[::2]:443":     false,
		"":              false,
	} {
		assert.Equal(t, want, testutil.IsLoopbackAddr(addr), addr)
	}
}

func TestLoopbackDialer(t *testing.T) {
	srv := testutil.NewServer(t, testutil.Serve(testutil.Step{Body: `{}`}))

	t.Run("loopback", func(t *testing.T) {
		hc := srv.Client()
		tr, ok := hc.Transport.(*http.Transport)
		require.True(t, ok)
		hc.Transport = testutil.GuardTransport(t, tr.Clone())
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL(), nil)
		require.NoError(t, err)
		resp, err := hc.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("refused", func(t *testing.T) {
		rec := &recorder{}
		dial := testutil.LoopbackDialer(rec)
		conn, err := dial(t.Context(), "tcp", "example.com:443")
		assert.Nil(t, conn)
		require.ErrorIs(t, err, testutil.ErrNotLoopback)
		assert.True(t, rec.failed, "the test fails too")
	})
}
