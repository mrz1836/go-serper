package serper

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

// rpiOp is one operation, called through an internal Client.
type rpiOp struct {
	name string
	op   Operation
	path string
	ok   string // its smallest successful answer
	call func(ctx context.Context, c *Client, opts ...CallOption) error
}

// rpiOps returns every operation, called with a minimal valid request.
func rpiOps() []rpiOp {
	return []rpiOp{
		{
			name: "Search", op: OpSearch, path: pathSearch,
			ok: `{"searchParameters":{"q":"example","type":"search","engine":"google"},"organic":[]}`,
			call: func(ctx context.Context, c *Client, opts ...CallOption) error {
				_, err := c.Search(ctx, SearchRequest{Query: "rpi query"}, opts...)
				return err
			},
		},
		{
			name: "Images", op: OpImages, path: pathImages,
			ok: `{"searchParameters":{"q":"example","type":"images","engine":"google"},"images":[]}`,
			call: func(ctx context.Context, c *Client, opts ...CallOption) error {
				_, err := c.Images(ctx, ImagesRequest{Query: "rpi query"}, opts...)
				return err
			},
		},
		{
			name: "Scrape", op: OpScrape, path: pathScrape, ok: `{"text":"Example page text."}`,
			call: func(ctx context.Context, c *Client, opts ...CallOption) error {
				_, err := c.Scrape(ctx, ScrapeRequest{URL: "https://www.example.com/rpi"}, opts...)
				return err
			},
		},
	}
}

// TestReplayDefaultTransportDroppedRequestIsSentOnce drops the second call of
// every operation on the reused keep-alive connection of the SDK's own
// transport, after the server read the whole request. The call is sent
// exactly once, with no new connection, and fails as a closed or reset
// transport failure whose outcome is unknown.
func TestReplayDefaultTransportDroppedRequestIsSentOnce(t *testing.T) {
	for _, o := range rpiOps() {
		t.Run(o.name, func(t *testing.T) {
			srv := testutil.NewDropServer(t, testutil.Step{Body: o.ok}, testutil.DropNth(2))
			c := sdkClient(t, srv.URL(), srv.RootCAs())

			require.NoError(t, o.call(t.Context(), c), "the first call warms up the connection")
			require.Equal(t, 1, srv.Hits())
			require.Equal(t, 1, srv.Conns())

			err := o.call(t.Context(), c, WithRawCapture(true))
			ce := testutil.RequireErrorAs[*CallError](t, err)
			class := ce.detail().class
			require.Contains(t, []transportClass{classClosed, classReset}, class)
			assert.Equal(t, FailureTransport, ce.Kind)
			assert.Equal(t, "serper: "+string(o.op)+": transport: "+class.String(), ce.Error())
			assert.Equal(t, class.cause(), ce.Unwrap())
			assert.True(t, ce.OutcomeUnknown)
			require.ErrorIs(t, err, ErrOutcomeUnknown)
			assert.Equal(t, Metadata{
				Op: o.op, Origin: srv.URL(), Path: o.path, Attempts: 1, Elapsed: ce.Meta.Elapsed,
				RawCapture: RawCaptureNoResponse,
			}, ce.Meta)
			assert.Positive(t, ce.Meta.Elapsed)
			testutil.AssertNeverPrints(t, err, internalKey, "rpi query", "www.example.com/rpi")

			assert.Equal(t, 2, srv.Hits(), "the dropped call was read once and never resent")
			assert.Equal(t, 1, srv.Conns(), "no new connection was dialed for a resend")
		})
	}
}

// TestReplayDefaultTransportResendsAReplayableRequest is the control of the
// test above: the SDK's own transport, against the same server, does resend
// a POST that the standard library counts as replayable (GetBody set and an
// Idempotency-Key header), and the server sees the extra request. So the
// SDK's single attempt comes from the shape of its request, which has
// neither, not from the transport or the server.
func TestReplayDefaultTransportResendsAReplayableRequest(t *testing.T) {
	srv := testutil.NewDropServer(t, testutil.Step{Body: "{}"}, testutil.DropNth(2))
	hc := sdkHTTPClient(sdkTransport(t, srv.RootCAs()))
	for i := range 2 {
		r, err := testutil.Post(t, hc, srv.URL()+pathSearch, `{"q":"example"}`,
			http.Header{"Idempotency-Key": {"rpi-key-1"}})
		require.NoError(t, err, "request %d: the dropped one is resent and then answered", i+1)
		assert.Equal(t, testutil.PostReply{Status: http.StatusOK, Proto: "HTTP/1.1"}, r)
	}
	assert.Equal(t, 3, srv.Hits(), "warm-up, the dropped POST and its resend")
	assert.Equal(t, 2, srv.Conns(), "the resend dialed a new connection")
}
