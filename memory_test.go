package serper_test

import (
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// Sizes and bounds of the memory suite. The heap bounds are deliberately
// loose next to the measured values quoted in each test, so that they fail
// only on a real regression, such as a body sized from a declared length, an
// extra body copy or a body kept past its call.
const (
	memKiB = 1 << 10
	memMiB = 1 << 20

	// memReadChunk is the most the SDK may read past a limit: io.ReadAll's
	// smallest read. The SDK actually reads one byte past it.
	memReadChunk = 512

	// memErrorBodyBytes is the documented bound of an error-body discard.
	memErrorBodyBytes = 4 * memKiB

	// memSlack covers a call's own small allocations (request, headers,
	// metadata, error) and measurement noise.
	memSlack = 64 * memKiB

	// memPreallocCeiling bounds the heap growth of a call whose body
	// declares a large length and then stalls: the SDK sizes its buffer
	// from a declared length only up to 1 MiB.
	memPreallocCeiling = 2 * memMiB
)

// memRetained runs f and returns what it returns and the live heap that
// value keeps reachable: the heap in use after a full collection, compared
// with the heap in use after one before f ran. It can be slightly negative.
func memRetained[T any](f func() T) (T, int64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	v := f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(v)
	return v, int64(after.HeapAlloc) - int64(before.HeapAlloc) //nolint:gosec // heap sizes fit an int64
}

// memStallingBody returns a body that sends the start of a JSON object and
// then answers (0, nil) forever, which the SDK fails as stalled.
func memStallingBody() io.ReadCloser {
	b := testutil.NewBody(`{"text":"`)
	b.Stall = true
	return b
}

func TestMemoryLargeDeclaredLengthIsNotPreallocated(t *testing.T) {
	testutil.SkipUnderRace(t)
	// Measured with Go 1.26 on darwin/arm64: about 4.4 KiB of heap growth for
	// every declared length over 1 MiB, and 1.01 MiB for exactly 1 MiB, the
	// largest buffer the SDK sizes from a declared length.
	const limit = serper.MaxResponseBytes
	for _, tt := range []struct {
		name     string
		declared int64
	}{
		{name: "1MiB", declared: memMiB},
		{name: "1MiB+1", declared: memMiB + 1},
		{name: "60MiB", declared: 60 * memMiB},
		{name: "64MiB", declared: limit},
	} {
		for _, o := range opCases() {
			t.Run(tt.name+"/Stalled/"+o.name, func(t *testing.T) {
				c := newTransportClient(t, testutil.Transport(http.StatusOK, tt.declared, memStallingBody),
					serper.WithMaxResponseBytes(limit))
				var err error
				growth := testutil.HeapGrowth(func() { _, err = directCall(t.Context(), c, o) })
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: serper.FailureTransport, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
					Class: "stalled",
				}))
				t.Logf("heap growth %d bytes for a declared %d", growth, tt.declared)
				assert.Less(t, growth, uint64(memPreallocCeiling), "no buffer sized from the declared length")
			})
			t.Run(tt.name+"/BlockedUntilTheTimeout/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c := newTransportClient(t, testutil.Transport(http.StatusOK, tt.declared, func() io.ReadCloser {
						return testutil.NewBlockingBody(`{"text":"`)
					}), serper.WithMaxResponseBytes(limit))
					var err error
					growth := testutil.HeapGrowth(func() { _, err = directCall(t.Context(), c, o) })
					ce := assertCallError(t, err, o.want(wantCall{
						Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
					}))
					assert.Equal(t, serper.DefaultTimeout, ce.Meta.Elapsed, "the fake clock ran to the deadline")
					t.Logf("heap growth %d bytes for a declared %d", growth, tt.declared)
					assert.Less(t, growth, uint64(memPreallocCeiling), "no buffer sized from the declared length")
					synctest.Wait()
				})
			})
		}
	}
}

func TestMemoryOversizedBodyStopsNearTheLimit(t *testing.T) {
	for _, tt := range []struct {
		name  string
		limit int64
		opts  []serper.Option
	}{
		{name: "1KiB", limit: memKiB, opts: []serper.Option{serper.WithMaxResponseBytes(memKiB)}},
		{name: "Default8MiB", limit: serper.DefaultMaxResponseBytes},
	} {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				body := &testutil.EndlessBody{}
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, -1, body), tt.opts...)
				got := o.call(t.Context(), c, serper.WithRawCapture(true))
				_ = assertCallError(t, got.err, o.want(wantCall{
					Kind: serper.FailureResponseTooLarge, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
					RawCapture: serper.RawCaptureFailed,
				}))
				assert.Greater(t, body.BytesRead(), tt.limit, "one byte past the limit shows the body is too large")
				assert.LessOrEqual(t, body.BytesRead(), tt.limit+memReadChunk, "reading stops near the limit")
				assert.Equal(t, 1, body.Closes(), "closed exactly once")
			})
		}
	}
}

func TestMemoryOversizedBodyRetainsNothing(t *testing.T) {
	testutil.SkipUnderRace(t)
	// Measured with Go 1.26 on darwin/arm64: 5.7 KiB of heap growth at the
	// 1 KiB limit and 16.3 MiB (2.03 times the limit, as io.ReadAll grows
	// its buffer) at the 8 MiB default; nothing is retained after either.
	for _, tt := range []struct {
		name      string
		opts      []serper.Option
		maxGrowth uint64
	}{
		{
			name: "1KiB", opts: []serper.Option{serper.WithMaxResponseBytes(memKiB)},
			maxGrowth: 3*memKiB + memSlack,
		},
		{name: "Default8MiB", maxGrowth: 3*serper.DefaultMaxResponseBytes + memSlack},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := opCaseNamed("Search")
			c := newTransportClient(t, testutil.Transport(http.StatusOK, -1, func() io.ReadCloser {
				return &testutil.EndlessBody{}
			}), append(tt.opts, serper.WithDefaultRawCapture())...)
			var err error
			growth := testutil.HeapGrowth(func() { _, err = directCall(t.Context(), c, o) })
			require.ErrorIs(t, err, serper.ErrResponseTooLarge)
			t.Logf("heap growth %d bytes", growth)
			assert.LessOrEqual(t, growth, tt.maxGrowth, "the read buffer grows only up to about the limit")

			failed, retained := memRetained(func() error {
				_, err := directCall(t.Context(), c, o)
				return err
			})
			require.ErrorIs(t, failed, serper.ErrResponseTooLarge)
			t.Logf("retained %d bytes", retained)
			assert.Less(t, retained, int64(memSlack), "the failed call keeps no part of the body")
		})
	}
}

func TestMemoryErrorBodyIsBoundedAndNotRead(t *testing.T) {
	content := strings.Repeat("e", memMiB)
	for _, code := range []int{400, 401, 403, 404, 429, 500, 503} {
		for _, tt := range []struct {
			name     string
			declared int64
			skipped  bool
		}{
			{name: "UnknownLength", declared: -1},
			{name: "DeclaredAtTheBound", declared: memErrorBodyBytes},
			{name: "DeclaredJustOver", declared: memErrorBodyBytes + 1, skipped: true},
			{name: "Declared1MiB", declared: memMiB, skipped: true},
		} {
			for _, o := range opCases() {
				t.Run(strconv.Itoa(code)+"/"+tt.name+"/"+o.name, func(t *testing.T) {
					b := testutil.NewBody(content)
					c := newTransportClient(t, testutil.TransportWith(code, nil, tt.declared, b))
					got := o.call(t.Context(), c, serper.WithRawCapture(true))
					ce := assertCallError(t, got.err, o.want(wantCall{
						Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code), Status: code, Attempts: 1,
						RawCapture: serper.RawCaptureFailed,
					}))
					raw, ok := ce.RawJSON()
					assertCapture(t, "", raw, ok, "an error body is never kept")
					assert.LessOrEqual(t, b.BytesRead(), int64(memErrorBodyBytes), "the discard reads at most 4 KiB")
					if tt.skipped {
						assert.Zero(t, b.Reads(), "a body declared longer than 4 KiB is never read")
					} else {
						assert.Positive(t, b.BytesRead(), "a short error body is discarded for reuse")
					}
					assert.Equal(t, 1, b.Closes(), "closed exactly once")
				})
			}
		}
	}
}

func TestMemoryErrorBodyRetainsNothing(t *testing.T) {
	testutil.SkipUnderRace(t)
	// Measured with Go 1.26 on darwin/arm64: 3.7 KiB of heap growth per call
	// with a 1 MiB error body, with capture on, and nothing retained.
	content := strings.Repeat("e", memMiB)
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		for _, o := range opCases() {
			t.Run(strconv.Itoa(code)+"/"+o.name, func(t *testing.T) {
				c := newTransportClient(t, testutil.Transport(code, -1, func() io.ReadCloser {
					return testutil.NewBody(content)
				}), serper.WithDefaultRawCapture())
				var err error
				growth := testutil.HeapGrowth(func() { _, err = directCall(t.Context(), c, o) })
				require.Error(t, err)
				t.Logf("heap growth %d bytes", growth)
				assert.Less(t, growth, uint64(memSlack), "the discard copies nothing of the body")

				failed, retained := memRetained(func() error {
					_, err := directCall(t.Context(), c, o)
					return err
				})
				require.Error(t, failed)
				t.Logf("retained %d bytes", retained)
				assert.Less(t, retained, int64(memSlack), "the error keeps no part of the body")
			})
		}
	}
}

// memMiBBody returns a successful answer of o of exactly 1 MiB and how many
// body-sized allocations decoding it makes with capture off: a Scrape whose
// text fills the body (the read buffer and the decoded Text), or a Search or
// Images answer padded with spaces (the read buffer only).
func memMiBBody(tb testing.TB, o opCase) (string, uint64) {
	tb.Helper()
	if o.scrape {
		body := string(testutil.ScrapeBody(memMiB-len(testutil.ScrapeBody(0, false)), false))
		require.Len(tb, body, memMiB)
		return body, 2
	}
	return padded(o.ok, memMiB), 1
}

func TestMemoryRawCaptureCost(t *testing.T) {
	testutil.SkipUnderRace(t)
	// Measured with Go 1.26 on darwin/arm64, capture off and on alike, as the
	// read buffer is kept without a copy (sized exactly from a declared
	// length; io.ReadAll returns a fitted slice): the 1 MiB Scrape 2.01 MiB
	// declared and 3.13 MiB unknown (the read buffer and the decoded Text),
	// Search and Images 1.01 MiB declared and 2.13 MiB unknown.
	for _, o := range opCases() {
		body, copies := memMiBBody(t, o)
		for _, declared := range []bool{true, false} {
			t.Run(o.name+"/declared="+strconv.FormatBool(declared), func(t *testing.T) {
				length, bound := int64(-1), (copies+2)*memMiB+memSlack // io.ReadAll grows as bytes arrive
				if declared {
					length, bound = int64(len(body)), copies*memMiB+memSlack
				}
				c := newTransportClient(t, testutil.Transport(http.StatusOK, length, func() io.ReadCloser {
					return io.NopCloser(strings.NewReader(body))
				}))
				var r response
				var err error
				off := testutil.HeapGrowth(func() { r, err = directCall(t.Context(), c, o, serper.WithRawCapture(false)) })
				require.NoError(t, err)
				_, ok := r.RawJSON()
				require.False(t, ok, "nothing captured with capture off")
				on := testutil.HeapGrowth(func() { r, err = directCall(t.Context(), c, o, serper.WithRawCapture(true)) })
				require.NoError(t, err)
				raw, ok := r.RawJSON()
				require.True(t, ok)
				require.Len(t, raw, memMiB)
				t.Logf("heap growth: capture off %d bytes, on %d bytes", off, on)
				assert.LessOrEqual(t, off, bound, "capture off makes no extra body copy")
				assert.LessOrEqual(t, on, off+memMiB+memSlack, "capture keeps at most one more body copy")
			})
		}
	}
}

func TestMemoryRawJSONCopiesOncePerCall(t *testing.T) {
	testutil.SkipUnderRace(t)
	for _, o := range opCases() {
		t.Run(o.name+"/Small", func(t *testing.T) {
			c := newTransportClient(t, testutil.StaticTransport(http.StatusOK, o.ok))
			r, err := directCall(t.Context(), c, o, serper.WithRawCapture(true))
			require.NoError(t, err)
			memAssertOneCopy(t, r.RawJSON, o.ok)
		})
		t.Run(o.name+"/1MiB", func(t *testing.T) {
			body, _ := memMiBBody(t, o)
			c := newTransportClient(t, testutil.StaticTransport(http.StatusOK, body))
			r, err := directCall(t.Context(), c, o, serper.WithRawCapture(true))
			require.NoError(t, err)
			memAssertOneCopy(t, r.RawJSON, body)
		})
		t.Run(o.name+"/RefusedBodyOnTheError", func(t *testing.T) {
			// Valid JSON, but no searchParameters, and no text for Scrape.
			body := padded(`{"credits":1}`, memMiB)
			c := newTransportClient(t, testutil.StaticTransport(http.StatusOK, body))
			_, err := directCall(t.Context(), c, o, serper.WithRawCapture(true))
			ce := callError(t, err)
			require.Equal(t, serper.RawCaptureComplete, ce.Meta.RawCapture)
			memAssertOneCopy(t, ce.RawJSON, body)
		})
	}
}

// memAssertOneCopy asserts that each call of rawJSON returns a new copy of
// the capture want, made with exactly one allocation of about its size.
func memAssertOneCopy(tb testing.TB, rawJSON func() ([]byte, bool), want string) {
	tb.Helper()
	var raw []byte
	allocs := testing.AllocsPerRun(100, func() { raw, _ = rawJSON() })
	assert.Equal(tb, 1, int(allocs), "one allocation per RawJSON call")
	growth := testutil.HeapGrowth(func() { raw, _ = rawJSON() })
	size := uint64(len(want))
	assert.GreaterOrEqual(tb, growth, size, "the copy is the whole body")
	assert.LessOrEqual(tb, growth, size+8*memKiB, "and no more than one copy, rounded to a page")
	assertBody(tb, want, raw)

	first, ok := rawJSON()
	require.True(tb, ok)
	second, ok := rawJSON()
	require.True(tb, ok)
	require.Equal(tb, first, second)
	first[0] ^= 0xff
	assert.NotEqual(tb, first[0], second[0], "every call returns its own copy")
	again, _ := rawJSON()
	assert.Equal(tb, second, again, "changing a copy never changes the capture")
}

func TestMemoryAllocationCeilings(t *testing.T) {
	testutil.SkipUnderRace(t)
	// Measured with Go 1.26 on darwin/arm64, allocations per run; each Max is
	// about 1.5 times the measured value, so the guard catches a regression
	// such as an extra copy per member or a per-call buffer, not noise. A
	// live call includes the in-memory transport's own 9 allocations
	// (response, header, reader).
	//
	//	Search 52, Images 52, Scrape 48 (capture on: 53, 53, 49);
	//	DecodeSearchResponse full 149, empty 15; DecodeImagesResponse full 33,
	//	empty 13; DecodeScrapeResponse text 12, markdown 14;
	//	NewClient default transport 18, injected client and every option 12.
	ctx := t.Context()
	ceilings := make([]testutil.Ceiling, 0, 16)
	for _, tt := range []struct {
		name     string
		max, cap float64 // capture off, on
	}{
		{name: "Search", max: 78, cap: 80},
		{name: "Images", max: 78, cap: 80},
		{name: "Scrape", max: 72, cap: 74},
	} {
		o := opCaseNamed(tt.name)
		c := newTransportClient(t, testutil.StaticTransport(http.StatusOK, o.ok))
		ceilings = append(ceilings,
			testutil.Ceiling{Name: tt.name, Max: tt.max, Run: func() { _, _ = directCall(ctx, c, o) }},
			testutil.Ceiling{Name: tt.name + "Captured", Max: tt.cap, Run: func() {
				_, _ = directCall(ctx, c, o, serper.WithRawCapture(true))
			}},
		)
	}
	for _, tt := range []struct {
		op      string
		fixture string
		max     float64
	}{
		{op: "Search", fixture: "search/full.json", max: 225},
		{op: "Search", fixture: "search/empty.json", max: 25},
		{op: "Images", fixture: "images/full.json", max: 50},
		{op: "Images", fixture: "images/empty.json", max: 20},
		{op: "Scrape", fixture: "scrape/text.json", max: 20},
		{op: "Scrape", fixture: "scrape/markdown.json", max: 22},
	} {
		data := fixture(t, tt.fixture)
		o := opCaseNamed(tt.op)
		require.NoError(t, o.decode(data).err, "%s decodes", tt.fixture)
		ceilings = append(ceilings, testutil.Ceiling{
			Name: "Decode/" + tt.fixture, Max: tt.max, Run: func() { _ = o.decode(data) },
		})
	}
	hc := &http.Client{}
	ceilings = append(ceilings,
		testutil.Ceiling{Name: "NewClient/Default", Max: 28, Run: func() { _, _ = serper.NewClient(testKey) }},
		testutil.Ceiling{Name: "NewClient/AllOptions", Max: 18, Run: func() {
			_, _ = serper.NewClient(testKey,
				serper.WithHTTPClient(hc), serper.WithSearchBaseURL(searchOrigin), serper.WithScrapeBaseURL(scrapeOrigin),
				serper.WithTimeout(serper.DefaultTimeout), serper.WithScrapeTimeout(serper.MaxTimeout),
				serper.WithMaxRequestBytes(memKiB), serper.WithMaxResponseBytes(memMiB),
				serper.WithUserAgent("example-agent/1.0"), serper.WithDefaultRawCapture())
		}},
	)
	testutil.CheckCeilings(t, ceilings)
}
