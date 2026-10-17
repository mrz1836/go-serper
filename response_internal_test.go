package serper

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestReadBody(t *testing.T) {
	const limit = 64
	tests := []struct {
		name     string
		body     string
		declared int64
		step     int
		want     string
		err      error
	}{
		{name: "declared exact", body: "hello", declared: 5, want: "hello"},
		{name: "declared exact in steps", body: "hello world", declared: 11, step: 2, want: "hello world"},
		{name: "declared longer than sent", body: "hi", declared: 5, want: "hi"},
		{name: "sent longer than declared", body: "hello!", declared: 5, want: "hello!"},
		{name: "unknown", body: "hello", declared: -1, want: "hello"},
		{name: "empty", body: "", declared: 0, want: ""},
		{name: "at limit unknown", body: strings.Repeat("a", limit), declared: -1, want: strings.Repeat("a", limit)},
		{name: "over limit unknown", body: strings.Repeat("a", limit+1), declared: -1, err: errResponseTooLarge},
		{name: "over limit declared at limit", body: strings.Repeat("a", limit+1), declared: limit, err: errResponseTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testutil.NewBody(tt.body)
			b.Step = tt.step
			got, err := readBody(b, tt.declared, limit)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				assert.LessOrEqual(t, len(got), limit, "never more than the limit")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestReadBodyPreallocation(t *testing.T) {
	got, err := readBody(strings.NewReader("abc"), 3, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, 4, cap(got), "a declared length sizes the buffer exactly, with one spare byte")

	big := strings.Repeat("b", maxPrealloc+10)
	got, err = readBody(strings.NewReader(big), int64(len(big)), 4<<20)
	require.NoError(t, err)
	assert.Len(t, got, len(big), "a large declared length grows as bytes arrive")
}

func TestReadBodyErrors(t *testing.T) {
	b := testutil.NewBody("abc")
	b.End = errTransportReset
	_, err := readBody(b, 10, 64)
	require.ErrorIs(t, err, errTransportReset)

	b = testutil.NewBody("abc")
	b.Stall = true
	_, err = readBody(b, -1, 64)
	require.ErrorIs(t, err, errTransportStalled)
}

func TestCapReader(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		over bool
	}{
		{name: "over the limit", body: "abcdef", over: true},
		// Over by one byte: sticky, even though the reader itself is at EOF.
		{name: "one byte over", body: "abcde", over: true},
		{name: "exactly the limit", body: "abcd"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &capReader{r: strings.NewReader(tt.body), limit: 4}
			if !tt.over {
				data, err := io.ReadAll(c)
				require.NoError(t, err, "a body of exactly the limit ends cleanly")
				assert.Equal(t, tt.body, string(data))
				return
			}
			p := make([]byte, 10)
			n, err := c.Read(p)
			assert.Equal(t, 4, n, "the bytes within the limit")
			require.ErrorIs(t, err, errResponseTooLarge)
			n, err = c.Read(p)
			assert.Zero(t, n)
			require.ErrorIs(t, err, errResponseTooLarge, "and every read after it")
		})
	}
}

func TestProgressReader(t *testing.T) {
	p := &progressReader{r: zeroReader{}}
	buf := make([]byte, 4)
	for range maxEmptyReads - 1 {
		n, err := p.Read(buf)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	_, err := p.Read(buf)
	require.ErrorIs(t, err, errTransportStalled)

	p = &progressReader{r: zeroReader{}}
	for range 3 * maxEmptyReads {
		n, err := p.Read(nil)
		require.NoError(t, err, "an empty buffer is no stall")
		assert.Zero(t, n)
	}
}

// zeroReader returns (0, nil) forever.
type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func TestDiscard(t *testing.T) {
	b := testutil.NewBody(strings.Repeat("x", 10))
	discard(b, -1)
	assert.Equal(t, int64(10), b.BytesRead())

	b = testutil.NewBody(strings.Repeat("x", 10<<10))
	discard(b, -1)
	assert.Equal(t, int64(maxErrorBodyBytes), b.BytesRead(), "bounded")

	b = testutil.NewBody("x")
	discard(b, maxErrorBodyBytes+1)
	assert.Zero(t, b.BytesRead(), "a body declared too long is skipped")

	discard(zeroReader{}, -1) // a stalled body ends after maxEmptyReads
}

func TestEncodedAndJSONContentType(t *testing.T) {
	assert.False(t, encoded(http.Header{}))
	assert.False(t, encoded(http.Header{"Content-Encoding": {"identity", " , "}}))
	assert.True(t, encoded(http.Header{"Content-Encoding": {"identity", "gzip"}}))
	assert.True(t, encoded(http.Header{"CONTENT-ENCODING": {"br"}}))

	assert.True(t, jsonContentType(http.Header{}))
	assert.True(t, jsonContentType(http.Header{"content-type": {"application/json"}}))
	assert.False(t, jsonContentType(http.Header{"Content-Type": {}, "content-type": {"a/b", "c/d"}}))
	assert.False(t, jsonContentType(http.Header{"Content-Type": {"application/json;;"}}))
}

func TestParseAttributes(t *testing.T) {
	for _, in := range []string{``, `{`, `{"a"`, `{"a":`, `{"a":"b"`, `{"a":"b",}`, `{1:"b"}`, `[`, `}`} {
		list, ok := parseAttributes([]byte(in))
		assert.False(t, ok, "%q", in)
		assert.Nil(t, list)
	}
	list, ok := parseAttributes([]byte(` {"a" : "1" , "a":"2"} `))
	require.True(t, ok)
	assert.Equal(t, Attributes{{"a", "1"}, {"a", "2"}}, list)

	var m attributesMirror
	require.NoError(t, m.UnmarshalJSON([]byte(`{"k":"v"}`)))
	assert.False(t, m.bad)
	assert.Equal(t, Attributes{{"k", "v"}}, m.list)
	require.NoError(t, m.UnmarshalJSON([]byte(`{"k":"w"}`)))
	assert.Equal(t, Attributes{{"k", "w"}}, m.list, "a repeated member keeps the last copy")
	require.NoError(t, m.UnmarshalJSON([]byte(`7`)))
	assert.True(t, m.bad)
	require.NoError(t, m.UnmarshalJSON([]byte(`{"k":"v"}`)))
	assert.True(t, m.bad, "a bad copy stays bad, as encoding/json keeps the first type error")

	for _, in := range []string{`{"a":"b"} x`, `{"a":"b"}{"c":"d"}`, `null null`, `{} []`} {
		_, ok := parseAttributes([]byte(in))
		assert.False(t, ok, "%q: nothing may follow the object", in)
		var a Attributes
		require.Error(t, a.UnmarshalJSON([]byte(in)), "%q", in)
	}
}

func TestCreditsOf(t *testing.T) {
	assert.Equal(t, Credits{}, creditsOf(nil))
	assert.Equal(t, Credits{}, creditsOf([]byte("null")))
	assert.Equal(t, Credits{Status: CreditsInvalid}, creditsOf([]byte(`"7"`)))
}

func TestRawBody(t *testing.T) {
	var none rawBody
	b, ok := none.copyOut()
	assert.False(t, ok)
	assert.Nil(t, b)

	src := make([]byte, 3, 100)
	copy(src, "abc")
	r := newRawBody(src)
	assert.Equal(t, 3, cap(r()), "spare capacity is never pinned")
	src[0] = 'X'
	assert.Equal(t, "abc", string(r()), "a body that had spare capacity was copied")

	oneSpare := append(make([]byte, 0, 4), "abc"...)
	r = newRawBody(oneSpare)
	assert.Equal(t, 3, cap(r()), "one spare byte, as a read to the declared length leaves, is clipped")
	oneSpare[0] = 'Y'
	assert.Equal(t, "Ybc", string(r()), "without a copy")
	r = newRawBody([]byte("abc"))

	out, ok := r.copyOut()
	require.True(t, ok)
	out[0] = 'Z'
	again, _ := r.copyOut()
	assert.Equal(t, "abc", string(again), "every copy is independent")

	for _, raw := range [][]byte{nil, {}} {
		out := cloneBytes(raw)
		assert.NotNil(t, out, "a copy is never nil, so a capture is never (nil, true)")
		assert.Empty(t, out)
	}
}

func TestResponseRawJSONNilReceivers(t *testing.T) {
	var s *SearchResponse
	var i *ImagesResponse
	var p *ScrapeResponse
	for _, f := range []func() ([]byte, bool){s.RawJSON, i.RawJSON, p.RawJSON} {
		b, ok := f()
		assert.False(t, ok)
		assert.Nil(t, b)
	}
}

func TestKindSentinelUnknown(t *testing.T) {
	require.NoError(t, kindSentinel("unknown_kind"))
	assert.False(t, (&CallError{Kind: "unknown_kind"}).Is(ErrTransport))
	assert.NotErrorIs(t, &CallError{Kind: FailureCanceled}, ErrTransport)
}

// overReader returns more bytes than it was given room for.
type overReader struct{ n int }

func (o overReader) Read(p []byte) (int, error) { return len(p) + o.n, nil }

func TestProgressReaderRefusesInvalidCounts(t *testing.T) {
	for _, n := range []int{1, 100} {
		p := &progressReader{r: overReader{n: n}}
		got, err := p.Read(make([]byte, 4))
		assert.Zero(t, got)
		require.ErrorIs(t, err, errInvalidRead)
	}
	p := &progressReader{r: overReader{n: -10}}
	_, err := p.Read(make([]byte, 4))
	require.ErrorIs(t, err, errInvalidRead, "a negative count")
	assert.Equal(t, classOther, classOf(errInvalidRead))

	_, err = readBody(overReader{n: 3}, -1, 1<<10)
	require.ErrorIs(t, err, errInvalidRead, "never a panic")
}

func TestJSONContentTypeNeedsATypeAndSubtype(t *testing.T) {
	for media, ok := range map[string]bool{
		"application/json":              true,
		"application/problem+json":      true,
		"text/vnd.example+json; v=1":    true,
		"application/+json":             false,
		"+json":                         false,
		"x+json":                        false,
		"/+json":                        false,
		"application/json+xml":          false,
		"application/jsonx":             false,
		"application/x-ndjson":          false,
		"application/problem+json, a/b": false,
	} {
		assert.Equal(t, ok, jsonContentType(http.Header{"Content-Type": {media}}), media)
	}
}

func TestCanonicalField(t *testing.T) {
	search := reflect.TypeFor[*searchMirror]()
	tests := map[string]string{
		"":                            "",
		"organic":                     "organic",
		"organic.position":            "organic.position",
		"organic.1.position":          "organic.position",
		"ORGANIC.0.POSITION":          "organic.position",
		"organic.0.sitelinks.1.linK":  "organic.sitelinks.link",
		"organic.0.ſitelinks":         "organic.sitelinks",
		"searchParameters.num":        "searchParameters.num",
		"searchparameters.Q":          "searchParameters.q",
		"knowledgeGraph.imageurl":     "knowledgeGraph.imageUrl",
		"organic.unknown":             "",
		"nothing":                     "",
		"organic.position.deeper":     "",
		"organic..position":           "",
		"credits":                     "credits",
		"peopleAlsoAsk.3.question":    "peopleAlsoAsk.question",
		"relatedSearches.2.QUERY":     "relatedSearches.query",
		"organic.attributes":          "organic.attributes",
		"bodymarker-4c2e9a.position":  "",
		"organic.0.bodymarker-4c2e9a": "",
	}
	for in, want := range tests {
		assert.Equal(t, want, canonicalField(search, in), "%q", in)
	}
	assert.Equal(t, "images.imageWidth", canonicalField(reflect.TypeFor[*imagesMirror](), "IMAGES.7.IMAGEWIDTH"))
	assert.Equal(t, "markdown", canonicalField(reflect.TypeFor[*scrapeMirror](), "Markdown"))
}

func TestRefusedBodyIsKeptAtExactSize(t *testing.T) {
	body := `{"searchParameters":{},"organic":[null]}` + strings.Repeat(" ", 3000)
	rt := testutil.TransportWith(http.StatusOK, nil, -1, io.NopCloser(strings.NewReader(body)))
	c := newInternalClient(t, WithHTTPClient(&http.Client{Transport: rt}))
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"}, WithRawCapture(true))
	raw := testutil.RequireErrorAs[*CallError](t, err).detail().raw
	assert.Len(t, raw, len(body))
	assert.Equal(t, len(raw), cap(raw), "a body of unknown length never pins its spare capacity")
}

func TestEncodeBodyChecksTheTextFirst(t *testing.T) {
	huge := strings.Repeat("\x01", 1<<20) // 6 MiB of JSON once escaped
	_, err := encodeBody(OpSearch, &queryWire{Q: huge}, 1<<10)
	ve := testutil.RequireErrorAs[*ValidationError](t, err)
	assert.Equal(t, "too_large", ve.Reason)
	if !testutil.RaceEnabled {
		grew := testutil.HeapGrowth(func() { _, _ = encodeBody(OpSearch, &queryWire{Q: huge}, 1<<10) })
		assert.Less(t, grew, uint64(64<<10), "nothing is encoded")
	}
	b, err := encodeBody(OpScrape, &scrapeWire{URL: "https://example.com/"}, 1<<10)
	require.NoError(t, err)
	want := `{"url":"https://example.com/"}` // member order and escaping count: no JSONEq
	assert.Equal(t, want, string(b), "byte for byte")
}
