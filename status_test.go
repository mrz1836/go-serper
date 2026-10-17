package serper_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// bodyMarker is planted in error bodies, which no error may ever quote.
const bodyMarker = "bodymarker-4c2e9a"

func TestStatusTable(t *testing.T) {
	bodies := map[string]string{
		"empty":    "",
		"json":     `{"message":"Synthetic: ` + testQuery + ` ` + testKey + ` ` + bodyMarker + `"}`,
		"non-json": "<html>" + testTarget + bodyMarker + "</html>",
	}
	for _, o := range opCases() {
		for _, code := range statusCodes() {
			for name, body := range bodies {
				t.Run(o.name+"/"+strconv.Itoa(code)+"/"+name, func(t *testing.T) {
					for _, capture := range []bool{false, true} {
						c, s := newScriptClient(t, testutil.Step{Status: code, Body: body})
						got := o.call(t.Context(), c, serper.WithRawCapture(capture))
						ce := assertCallError(t, got.err, o.want(wantCall{
							Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code),
							Status: code, Attempts: 1, RawCapture: captureState(capture, serper.RawCaptureFailed),
						}), bodyMarker)
						raw, ok := ce.RawJSON()
						assertCapture(t, "", raw, ok, "an error body is never captured")
						assert.Equal(t, 1, s.Calls(), "exactly one attempt")
					}
				})
			}
		}
	}
}

func TestStatusErrorBodyIsDiscardedAndClosed(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		declared int64
		size     int
		wantRead int64
	}{
		{name: "small body read for reuse", status: 429, declared: 100, size: 100, wantRead: 100},
		{name: "unknown length bounded", status: 500, declared: -1, size: 64 << 10, wantRead: 4 << 10},
		{name: "declared too long skipped", status: 404, declared: 5000, size: 5000, wantRead: 0},
		{name: "redirect body", status: 302, declared: 10, size: 10, wantRead: 10},
		{name: "1xx never read", status: 101, declared: 10, size: 10, wantRead: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := testutil.NewBody(strings.Repeat("e", tt.size))
			c := newTransportClient(t, testutil.TransportWith(tt.status, nil, tt.declared, body))
			got := opCaseNamed("Search").call(t.Context(), c)
			require.Error(t, got.err)
			assert.Equal(t, tt.wantRead, body.BytesRead())
			assert.Equal(t, 1, body.Closes(), "closed exactly once")
		})
	}
}

func TestStatusContentEncoding(t *testing.T) {
	for _, enc := range []string{"gzip", "br", "deflate", "zstd", "identity, gzip", "x-custom"} {
		t.Run(enc, func(t *testing.T) {
			for _, o := range opCases() {
				body := testutil.NewBody(o.ok)
				h := http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {enc}}
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, h, int64(len(o.ok)), body))
				got := o.call(t.Context(), c, serper.WithRawCapture(true))
				_ = assertCallError(t, got.err, o.want(wantCall{
					Kind: serper.FailureUnexpectedResponse, OutcomeUnknown: true, Status: 200, Attempts: 1,
					RawCapture: serper.RawCaptureFailed,
				}))
				assert.Zero(t, body.BytesRead(), "an undecoded body is never read")
				assert.Equal(t, 1, body.Closes())
			}
		})
	}
	for _, enc := range []string{"identity", "", " , identity", "IDENTITY"} {
		c := newTransportClient(t, testutil.TransportWith(http.StatusOK,
			http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {enc}}, -1,
			io.NopCloser(strings.NewReader(searchOK))))
		require.NoError(t, opCaseNamed("Search").call(t.Context(), c).err, "%q", enc)
	}
	// A non-canonical spelling an injected transport may set counts too.
	c := newTransportClient(t, testutil.TransportWith(http.StatusOK,
		http.Header{"content-encoding": {"gzip"}}, -1, io.NopCloser(strings.NewReader(searchOK))))
	require.ErrorIs(t, opCaseNamed("Search").call(t.Context(), c).err, serper.ErrUnexpectedResponse)
}

func TestStatusContentType(t *testing.T) {
	accepted := []http.Header{
		{},
		{"Content-Type": {"application/json"}},
		{"Content-Type": {"application/json; charset=utf-8"}},
		{"Content-Type": {"APPLICATION/JSON"}},
		{"Content-Type": {"application/problem+json"}},
		{"Content-Type": {"application/vnd.example+json; v=1"}},
	}
	for _, h := range accepted {
		c := newTransportClient(t, testutil.TransportWith(http.StatusOK, h, -1, io.NopCloser(strings.NewReader(searchOK))))
		require.NoError(t, opCaseNamed("Search").call(t.Context(), c).err, "%v", h)
	}
	refused := []http.Header{
		{"Content-Type": {"text/html"}},
		{"Content-Type": {"text/plain; charset=utf-8"}},
		{"Content-Type": {"application/json", "application/json"}},
		{"Content-Type": {"application/json, text/html"}},
		{"Content-Type": {""}},
		{"Content-Type": {"application/"}},
		{"Content-Type": {"application/jsonx"}},
		{"Content-Type": {"application/json"}, "content-type": {"text/html"}},
	}
	for _, h := range refused {
		for _, o := range opCases() {
			body := testutil.NewBody(o.ok)
			c := newTransportClient(t, testutil.TransportWith(http.StatusOK, h, int64(len(o.ok)), body))
			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			_ = assertCallError(t, got.err, o.want(wantCall{
				Kind: serper.FailureUnexpectedResponse, OutcomeUnknown: true, Status: 200, Attempts: 1,
				RawCapture: serper.RawCaptureFailed,
			}))
			assert.Zero(t, body.BytesRead(), "%v: a body of another type is never read", h)
		}
	}
}

func TestStatusResponseLimit(t *testing.T) {
	const limit = 4096
	for _, o := range opCases() {
		for _, tt := range []struct {
			name     string
			size     int
			declared bool
			ok       bool
		}{
			{name: "limit-1 declared", size: limit - 1, declared: true, ok: true},
			{name: "limit declared", size: limit, declared: true, ok: true},
			{name: "limit+1 declared", size: limit + 1, declared: true},
			{name: "limit-1 unknown", size: limit - 1, ok: true},
			{name: "limit unknown", size: limit, ok: true},
			{name: "limit+1 unknown", size: limit + 1},
			{name: "far over unknown", size: 64 * limit},
		} {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				body := padded(o.ok, tt.size)
				declared := int64(-1)
				if tt.declared {
					declared = int64(len(body))
				}
				b := testutil.NewBody(body)
				b.Step = 512
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, declared, b),
					serper.WithMaxResponseBytes(limit))
				got := o.call(t.Context(), c, serper.WithRawCapture(true))
				assert.Equal(t, 1, b.Closes())
				if tt.ok {
					require.NoError(t, got.err)
					assert.Equal(t, []byte(body), got.raw, "the capture is the whole body")
					return
				}
				_ = assertCallError(t, got.err, o.want(wantCall{
					Kind: serper.FailureResponseTooLarge, OutcomeUnknown: true, Status: 200, Attempts: 1,
					RawCapture: serper.RawCaptureFailed,
				}))
				assert.LessOrEqual(t, b.BytesRead(), int64(limit+512), "reading stops at the limit")
				if tt.declared {
					assert.Zero(t, b.BytesRead(), "a declared length over the limit is never read")
				}
			})
		}
	}
}

func TestStatusGzipCountsDecompressedBytes(t *testing.T) {
	const limit = 4096
	var small, bomb bytes.Buffer
	for buf, body := range map[*bytes.Buffer]string{
		&small: searchOK,
		&bomb:  padded(searchOK, 1<<20),
	} {
		zw := gzip.NewWriter(buf)
		_, err := io.WriteString(zw, body)
		require.NoError(t, err)
		require.NoError(t, zw.Close())
	}
	require.Less(t, bomb.Len(), limit, "the compressed bomb is small on the wire")

	gz := func(data []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", mediaJSON)
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(data)
		}
	}
	c, _ := newServerClient(t, gz(small.Bytes()), okScrapeHandler(), serper.WithMaxResponseBytes(limit))
	got := opCaseNamed("Search").call(t.Context(), c, serper.WithRawCapture(true))
	require.NoError(t, got.err, "the transport decodes gzip itself")
	assertBody(t, searchOK, got.raw) // the capture is the decoded body

	search := opCaseNamed("Search")
	c, srv := newServerClient(t, gz(bomb.Bytes()), okScrapeHandler(), serper.WithMaxResponseBytes(limit))
	got = search.call(t.Context(), c)
	_ = assertCallError(t, got.err, search.wantAt(srv.search.URL(), wantCall{
		Kind: serper.FailureResponseTooLarge, OutcomeUnknown: true, Status: 200, Attempts: 1,
	}))
}

func TestStatusEmptiesBeforeContentAreTolerated(t *testing.T) {
	b := testutil.NewBody(searchOK)
	b.Empties = 50
	b.Step = 7
	c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, int64(len(searchOK)), b))
	require.NoError(t, opCaseNamed("Search").call(t.Context(), c).err, "fewer than 100 empty reads in a row")
}

func TestStatusLateAnswers(t *testing.T) {
	for _, code := range []int{200, 204, 301, 404, 429, 500} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			o := opCaseNamed("Search")
			// The transport ignores the deadline: it answers only once the
			// call's context has ended.
			late := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				<-r.Context().Done()
				return testutil.Response(r, code, io.NopCloser(strings.NewReader(o.ok)), int64(len(o.ok))), nil
			})
			c := newTransportClient(t, late, serper.WithTimeout(time.Millisecond))
			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			if code == http.StatusOK {
				// An HTTP 200 answer needs its body, which the deadline covers.
				_ = assertCallError(t, got.err, o.want(wantCall{
					Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: code, Attempts: 1,
					RawCapture: serper.RawCaptureFailed,
				}))
				return
			}
			// Any other status decides the outcome alone, however late.
			_ = assertCallError(t, got.err, o.want(wantCall{
				Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code), Status: code, Attempts: 1,
				RawCapture: serper.RawCaptureFailed,
			}))
		})
	}
}
