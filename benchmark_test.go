package serper_test

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// bnchMode is one raw capture setting a benchmark runs with.
type bnchMode struct {
	name string
	on   bool
}

// bnchModes are the two capture settings, as sub-benchmark names.
func bnchModes() []bnchMode {
	return []bnchMode{{name: "capture=off", on: false}, {name: "capture=on", on: true}}
}

// bnchRunCall benchmarks a call of o against an in-memory transport answering
// body, with capture off and on, on one Client built before the loops. A
// first, unmeasured call checks that the call succeeds with the expected
// capture.
func bnchRunCall(b *testing.B, body []byte, o opCase) {
	b.Helper()
	c := newTransportClient(b, testutil.StaticTransport(http.StatusOK, string(body)))
	for _, mode := range bnchModes() {
		b.Run(mode.name, func(b *testing.B) {
			ctx := b.Context()
			opts := []serper.CallOption{serper.WithRawCapture(mode.on)}
			res, err := directCall(ctx, c, o, opts...)
			require.NoError(b, err)
			require.Equal(b, captureState(mode.on, serper.RawCaptureComplete), metaOf(res).RawCapture)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := directCall(ctx, c, o, opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSearch(b *testing.B) {
	for _, n := range []int{10, 100} {
		body := testutil.SearchBody(n)
		b.Run("results="+strconv.Itoa(n), func(b *testing.B) { bnchRunCall(b, body, opCaseNamed("Search")) })
	}
}

func BenchmarkImages(b *testing.B) {
	body := testutil.ImagesBody(100)
	b.Run("results=100", func(b *testing.B) { bnchRunCall(b, body, opCaseNamed("Images")) })
}

func BenchmarkScrape(b *testing.B) {
	for _, tc := range []struct {
		name     string
		markdown bool
	}{{name: "text=1MiB", markdown: false}, {name: "markdown=1MiB", markdown: true}} {
		body := testutil.ScrapeBody(1<<20, tc.markdown)
		b.Run(tc.name, func(b *testing.B) { bnchRunCall(b, body, opCaseNamed("Scrape")) })
	}
}

// BenchmarkRawJSON measures one RawJSON copy of a captured 100-result search
// (capture=on), against the (nil, false) of a response without a capture
// (capture=off).
func BenchmarkRawJSON(b *testing.B) {
	body := testutil.SearchBody(100)
	c := newTransportClient(b, testutil.StaticTransport(http.StatusOK, string(body)))
	for _, mode := range bnchModes() {
		b.Run(mode.name, func(b *testing.B) {
			res, err := c.Search(b.Context(), serper.SearchRequest{Query: testQuery}, serper.WithRawCapture(mode.on))
			require.NoError(b, err)
			raw, ok := res.RawJSON()
			require.Equal(b, mode.on, ok)
			if mode.on {
				require.Equal(b, body, raw)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				if _, ok := res.RawJSON(); ok != mode.on {
					b.Fatalf("RawJSON reported %t with capture %t", ok, mode.on)
				}
			}
		})
	}
}

// bnchRunDecode benchmarks a Decode helper on body.
func bnchRunDecode(b *testing.B, body []byte, decode func([]byte) error) {
	b.Helper()
	require.NoError(b, decode(body))
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if err := decode(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeSearch(b *testing.B) {
	body := testutil.SearchBody(100)
	b.Run("results=100", func(b *testing.B) {
		bnchRunDecode(b, body, func(d []byte) error {
			_, err := serper.DecodeSearchResponse(d)
			return err
		})
	})
}

func BenchmarkDecodeImages(b *testing.B) {
	body := testutil.ImagesBody(100)
	b.Run("results=100", func(b *testing.B) {
		bnchRunDecode(b, body, func(d []byte) error {
			_, err := serper.DecodeImagesResponse(d)
			return err
		})
	})
}

func BenchmarkDecodeScrape(b *testing.B) {
	body := testutil.ScrapeBody(1<<20, false)
	b.Run("text=1MiB", func(b *testing.B) {
		bnchRunDecode(b, body, func(d []byte) error {
			_, err := serper.DecodeScrapeResponse(d)
			return err
		})
	})
}

// BenchmarkNewClient measures NewClient with its own default transport, with
// the default raw capture off and on.
func BenchmarkNewClient(b *testing.B) {
	for _, mode := range bnchModes() {
		b.Run(mode.name, func(b *testing.B) {
			var opts []serper.Option
			if mode.on {
				opts = append(opts, serper.WithDefaultRawCapture())
			}
			_ = newClient(b, opts...)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := serper.NewClient(testKey, opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRequestEncode measures a Search with every request field set,
// against a transport that drains the request and answers the smallest
// success; the bytes are the encoded request body's.
func BenchmarkRequestEncode(b *testing.B) {
	req := serper.SearchRequest{
		Query: testQuery, Location: "Example City, Example State", GL: "us", HL: "en", Num: 100,
		Autocorrect: new(false), TBS: "qdr:w", Page: 3,
	}
	var sent int64
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = testutil.Drain(r)
		return testutil.Response(r, http.StatusOK, io.NopCloser(bytes.NewReader([]byte(searchOK))), int64(len(searchOK))), nil
	})
	c := newTransportClient(b, rt)
	for _, mode := range bnchModes() {
		b.Run(mode.name, func(b *testing.B) {
			ctx := b.Context()
			res, err := c.Search(ctx, req, serper.WithRawCapture(mode.on))
			require.NoError(b, err)
			require.Equal(b, captureState(mode.on, serper.RawCaptureComplete), res.Meta.RawCapture)
			require.Positive(b, sent, "the transport drained the request body")
			b.ReportAllocs()
			b.SetBytes(sent)
			for b.Loop() {
				if _, err := c.Search(ctx, req, serper.WithRawCapture(mode.on)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
