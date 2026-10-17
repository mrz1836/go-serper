package serper_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestWireDefaultOrigins(t *testing.T) {
	s := testutil.NewScript(
		testutil.Step{Body: searchOK}, testutil.Step{Body: imagesOK}, testutil.Step{Body: scrapeOK},
	)
	c := newClient(t, serper.WithHTTPClient(&http.Client{Transport: s}))
	for _, o := range opCases() {
		got := o.call(t.Context(), c)
		require.NoError(t, got.err, o.name)
	}
	reqs := s.Requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "https://google.serper.dev/search", reqs[0].URL)
	assert.Equal(t, "https://google.serper.dev/images", reqs[1].URL)
	assert.Equal(t, "https://scrape.serper.dev/", reqs[2].URL, "scrape posts to the root of its own origin")
	for i, o := range opCases() {
		assertWireRequest(t, reqs[i], o.path)
		assertNoReplay(t, reqs[i])
	}
}

func TestWireMetadataOfSuccess(t *testing.T) {
	c, _ := newScriptClient(t,
		testutil.Step{Body: searchOK}, testutil.Step{Body: imagesOK}, testutil.Step{Body: scrapeOK})
	for _, o := range opCases() {
		got := o.call(t.Context(), c)
		require.NoError(t, got.err)
		assert.Equal(t, o.op, got.meta.Op)
		assert.Equal(t, o.origin(), got.meta.Origin)
		assert.Equal(t, o.path, got.meta.Path)
		assert.Equal(t, http.StatusOK, got.meta.StatusCode)
		assert.Equal(t, 1, got.meta.Attempts)
		assert.Positive(t, got.meta.Elapsed)
		assert.Equal(t, serper.RawCaptureOff, got.meta.RawCapture)
		assert.Equal(t, serper.Credits{}, got.meta.Credits, "no credits member: not reported")
		assertCapture(t, "", got.raw, got.rawOK)
	}
}

func TestWireSeparateOriginsOverTLS(t *testing.T) {
	c, srv := newServerClient(t, okSearchHandler(), okScrapeHandler())
	for _, o := range opCases() {
		got := o.call(t.Context(), c)
		require.NoError(t, got.err, o.name)
	}
	for _, o := range opCases() {
		assert.Positive(t, srv.of(o).Hits(), "%s reached its own origin", o.name)
	}
	search, scrape := srv.search.Requests(), srv.scrape.Requests()
	require.Len(t, search, 2, "search and images hit the search origin only")
	require.Len(t, scrape, 1, "scrape hits the scrape origin only")
	assertWireRequest(t, search[0], pathSearch)
	assertWireRequest(t, search[1], pathImages)
	assertWireRequest(t, scrape[0], pathScrape)
	for _, r := range append(search, scrape...) {
		assert.Equal(t, "HTTP/1.1", r.Proto)
		assert.True(t, strings.HasPrefix(r.Host, "127.0.0.1:"), "only the test origins are contacted, never the scrape target: %s", r.Host)
	}
}

func TestWireExactBodies(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *serper.Client) error
		path string
		body string
	}{
		{
			name: "search minimal", path: pathSearch, body: `{"q":"golang"}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Search(ctx, serper.SearchRequest{Query: "golang"})
				return err
			},
		},
		{
			name: "search every field", path: pathSearch,
			body: `{"q":"golang","location":"Example City","gl":"us","hl":"en","num":10,"autocorrect":true,` +
				`"tbs":"qdr:w","page":1}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Search(ctx, serper.SearchRequest{
					Query: "golang", Location: "Example City", GL: "us", HL: "en", Num: 10,
					Autocorrect: new(true), TBS: "qdr:w", Page: 1,
				})
				return err
			},
		},
		{
			name: "search autocorrect false", path: pathSearch, body: `{"q":"golang","autocorrect":false}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Search(ctx, serper.SearchRequest{Query: "golang", Autocorrect: new(false)})
				return err
			},
		},
		{
			name: "images every field", path: pathImages,
			body: `{"q":"lighthouse","location":"Example City","gl":"de","hl":"de","num":100,"autocorrect":false,` +
				`"tbs":"isz:l","page":3}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Images(ctx, serper.ImagesRequest{
					Query: "lighthouse", Location: "Example City", GL: "de", HL: "de", Num: 100,
					Autocorrect: new(false), TBS: "isz:l", Page: 3,
				})
				return err
			},
		},
		{
			name: "images minimal", path: pathImages, body: `{"q":"lighthouse"}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Images(ctx, serper.ImagesRequest{Query: "lighthouse"})
				return err
			},
		},
		{
			name: "scrape", path: pathScrape, body: `{"url":"https://www.example.com/post?id=7&x=<y>"}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/post?id=7&x=<y>"})
				return err
			},
		},
		{
			name: "scrape with markdown", path: pathScrape,
			body: `{"url":"https://www.example.com/","includeMarkdown":true}`,
			call: func(ctx context.Context, c *serper.Client) error {
				_, err := c.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/", IncludeMarkdown: true})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: okBody(tt.path)})
			require.NoError(t, tt.call(t.Context(), c))
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, tt.path)
			assertNoReplay(t, r)
			assertBody(t, tt.body, r.Body)
		})
	}
}

func TestWireQueryFidelity(t *testing.T) {
	queries := map[string]string{
		"spaces":                 "  two  leading and trailing spaces  ",
		"quotes and operators":   `"exact phrase" -site:example.com OR intitle:go filetype:pdf`,
		"punctuation":            `a&b<c>d'e\f/g?h#i%j+k=l`,
		"unicode":                "caf\u00e9 \u6771\u4eac \u2713 \U0001F9ED",
		"control characters":     "tab\tnew\nline\x01",
		"line separators":        "a b c",
		"only spaces":            "   ",
		"json-looking":           `{"q":"nested"}`,
		"long":                   strings.Repeat("go ", 2000),
		"html escapes stay bare": "<script>&amp;</script>",
	}
	for name, q := range queries {
		t.Run(name, func(t *testing.T) {
			for _, o := range []string{"Search", "Images"} {
				c, s := newScriptClient(t, testutil.Step{Body: opCaseNamed(o).ok})
				var err error
				if o == "Search" {
					_, err = c.Search(t.Context(), serper.SearchRequest{Query: q})
				} else {
					_, err = c.Images(t.Context(), serper.ImagesRequest{Query: q})
				}
				require.NoError(t, err)
				assertBody(t, `{"q":`+testutil.JSONQuoteNoHTML(q)+`}`, testutil.OnlyRequest(t, s).Body)
			}
		})
	}
}

func TestWireRepeatedQueriesAreSeparateCalls(t *testing.T) {
	c, s := newScriptClient(t,
		testutil.Step{Body: `{"searchParameters":{"q":"same"},"organic":[{"title":"first","position":1}]}`},
		testutil.Step{Body: `{"searchParameters":{"q":"same"},"organic":[{"title":"second","position":1}]}`},
	)
	req := serper.SearchRequest{Query: "same"}
	a, err := c.Search(t.Context(), req)
	require.NoError(t, err)
	b, err := c.Search(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, 2, s.Calls(), "no cache: each call is sent")
	assert.Equal(t, "first", a.Organic[0].Title)
	assert.Equal(t, "second", b.Organic[0].Title)
}

func TestWireUserAgent(t *testing.T) {
	s := testutil.NewScript(testutil.Step{Body: searchOK})
	c := newTransportClient(t, s, serper.WithUserAgent("my-app/1.0 (+https://www.example.com)"))
	require.NoError(t, opCaseNamed("Search").call(t.Context(), c).err)
	assert.Equal(t, []string{"my-app/1.0 (+https://www.example.com)"}, testutil.OnlyRequest(t, s).Header.Values("User-Agent"))
}

func TestRequestValidation(t *testing.T) {
	for _, tt := range invalidRequests() {
		t.Run(tt.name, func(t *testing.T) {
			c := newOfflineClient(t)
			err := tt.call(t.Context(), c)
			assertValidation(t, err, serper.ErrInvalidRequest, tt.op, tt.field, tt.reason)
		})
	}
}

func TestRequestScrapeTargetsAccepted(t *testing.T) {
	for _, target := range []string{
		"http://example.com",
		"HTTPS://WWW.EXAMPLE.COM/A?B=C#frag",
		"https://example.com:8443/path;params?q=1",
		"https://[2001:db8::1]/",
		"https://127.0.0.1/",
		"https://xn--bcher-kva.example/ü",
		"https://example.com/a b",
	} {
		c, s := newScriptClient(t, testutil.Step{Body: scrapeOK})
		_, err := c.Scrape(t.Context(), serper.ScrapeRequest{URL: target})
		require.NoError(t, err, target)
		assertBody(t, `{"url":`+testutil.JSONQuoteNoHTML(target)+`}`, testutil.OnlyRequest(t, s).Body)
	}
}

func TestRequestSizeLimit(t *testing.T) {
	const limit = 2048
	overhead := len(`{"q":""}`)
	for _, tt := range []struct {
		name string
		size int
		ok   bool
	}{
		{name: "below", size: limit - 1, ok: true},
		{name: "at", size: limit, ok: true},
		{name: "above", size: limit + 1, ok: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(testutil.Step{Body: searchOK})
			c := newTransportClient(t, s, serper.WithMaxRequestBytes(limit))
			q := strings.Repeat("q", tt.size-overhead)
			_, err := c.Search(t.Context(), serper.SearchRequest{Query: q})
			if tt.ok {
				require.NoError(t, err)
				assert.Len(t, testutil.OnlyRequest(t, s).Body, tt.size)
				return
			}
			assertValidation(t, err, serper.ErrInvalidRequest, serper.OpSearch, "request", reasonTooLarge)
			assert.Zero(t, s.Calls(), "nothing was sent")
		})
	}

	t.Run("escapes count", func(t *testing.T) {
		c := newOfflineClient(t, serper.WithMaxRequestBytes(1024))
		// 600 quotes are 600 bytes of URL but 1200 bytes of JSON.
		_, err := c.Scrape(t.Context(), serper.ScrapeRequest{URL: "https://example.com/" + strings.Repeat(`"`, 600)})
		assertValidation(t, err, serper.ErrInvalidRequest, serper.OpScrape, "request", reasonTooLarge)
	})
}

func TestRequestDoneContextSendsNothing(t *testing.T) {
	for _, done := range doneContexts() {
		t.Run(done.name, func(t *testing.T) {
			for _, capture := range []bool{false, true} {
				c := newOfflineClient(t)
				for _, o := range opCases() {
					ctx, cancel := done.ctx(t.Context())
					got := o.call(ctx, c, serper.WithRawCapture(capture))
					cancel()
					_ = assertCallError(t, got.err, o.want(wantCall{
						Kind: done.kind, RawCapture: captureState(capture, serper.RawCaptureNoResponse),
					}))
				}
			}
		})
	}
}

func TestRequestValidationBeforeContext(t *testing.T) {
	c := newOfflineClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.Search(ctx, serper.SearchRequest{})
	assertValidation(t, err, serper.ErrInvalidRequest, serper.OpSearch, "Query", reasonRequired)
}

func TestRequestFormatting(t *testing.T) {
	tests := []struct {
		name  string
		value interface {
			fmt.Stringer
			fmt.GoStringer
		}
		want string
	}{
		{
			name:  "search empty",
			value: serper.SearchRequest{},
			want: "serper.SearchRequest{query: unset, location: unset, gl: unset, hl: unset, num: unset, " +
				"autocorrect: unset, tbs: unset, page: unset}",
		},
		{
			name: "search full",
			value: serper.SearchRequest{
				Query: testQuery, Location: "Example City", GL: "us", HL: "en", Num: 10,
				Autocorrect: new(false), TBS: "qdr:w", Page: 2,
			},
			want: fmt.Sprintf("serper.SearchRequest{query: %d bytes, location: set, gl: set, hl: set, num: 10, "+
				"autocorrect: false, tbs: set, page: 2}", len(testQuery)),
		},
		{
			name:  "images",
			value: serper.ImagesRequest{Query: testQuery, Autocorrect: new(true)},
			want: fmt.Sprintf("serper.ImagesRequest{query: %d bytes, location: unset, gl: unset, hl: unset, "+
				"num: unset, autocorrect: true, tbs: unset, page: unset}", len(testQuery)),
		},
		{
			name:  "scrape",
			value: serper.ScrapeRequest{URL: testTarget, IncludeMarkdown: true},
			want:  fmt.Sprintf("serper.ScrapeRequest{url: %d bytes, includeMarkdown: true}", len(testTarget)),
		},
		{
			name:  "scrape empty",
			value: serper.ScrapeRequest{},
			want:  "serper.ScrapeRequest{url: unset, includeMarkdown: false}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, verb := range verbsButP() {
				want := tt.want
				if verb == "%q" {
					want = fmt.Sprintf("%q", tt.want)
				}
				assert.Equal(t, want, fmt.Sprintf(verb, tt.value), verb)
			}
			assert.Equal(t, tt.want, tt.value.String())
			assert.Equal(t, tt.want, tt.value.GoString())
		})
	}

	req := serper.SearchRequest{Query: testQuery, Location: "locmarker", GL: "glmarker", HL: "hlmarker", TBS: "tbsmarker"}
	assertPrintsShapeOnly(t, req, testQuery, "locmarker", "glmarker", "hlmarker", "tbsmarker")
	assertPrintsShapeOnly(t, &req, testQuery, "locmarker", "glmarker", "hlmarker", "tbsmarker")
	img := serper.ImagesRequest{Query: testQuery}
	assertPrintsShapeOnly(t, img, testQuery)
	assertPrintsShapeOnly(t, &img, testQuery)
	sc := serper.ScrapeRequest{URL: testTarget}
	assertPrintsShapeOnly(t, sc, testTarget)
	assertPrintsShapeOnly(t, []serper.ScrapeRequest{sc}, testTarget)
	assertPrintsShapeOnly(t, map[string]*serper.ScrapeRequest{"k": &sc}, testTarget)
}
