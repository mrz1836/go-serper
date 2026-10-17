package serper_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// The concurrency suite's calls: cncUnique calls with a value of their own,
// and cncDuplicates calls that repeat the operation and value of an earlier
// one, with another capture choice.
const (
	cncUnique     = 72
	cncDuplicates = 24
)

// Per-call capture choices of a job.
const (
	cncCaptureDefault = iota // no WithRawCapture: the Client's default
	cncCaptureOn             // WithRawCapture(true)
	cncCaptureOff            // WithRawCapture(false)
)

// cncJob is one call of the concurrency suite.
type cncJob struct {
	o       opCase
	value   string // the query or scrape target, which the answer echoes
	num     int    // the Num of a Search or Images request
	capture int    // a cncCapture choice
}

// cncJobs returns the suite's calls: Search, Images and Scrape in turn, each
// capture choice for each operation, and duplicates.
func cncJobs() []cncJob {
	ops := opCases()
	jobs := make([]cncJob, 0, cncUnique+cncDuplicates)
	for n := range cncUnique {
		o := ops[n%len(ops)]
		jobs = append(jobs, cncJob{o: o, value: cncValue(o, n), num: n%9 + 1, capture: n / len(ops) % 3})
	}
	for i := range cncDuplicates {
		dup := jobs[i]
		dup.capture = (dup.capture + 1) % 3
		jobs = append(jobs, dup)
	}
	return jobs
}

// cncValue returns the unique value of call n of o: a query, with HTML
// characters, or a scrape target. Its last three characters are n.
func cncValue(o opCase, n int) string {
	if o.scrape {
		return fmt.Sprintf("https://www.example.com/cnc/page?n=%03d", n)
	}
	return fmt.Sprintf("cnc %s <&> %03d", strings.ToLower(o.name), n)
}

// cncNumber returns the number a value ends with, or -1.
func cncNumber(value string) int {
	if len(value) < 3 {
		return -1
	}
	n, err := strconv.Atoi(value[len(value)-3:])
	if err != nil {
		return -1
	}
	return n
}

// cncAnswer returns the answer to a call at path with value and num: the
// value echoed in its typed members and in an untyped one, num echoed in
// searchParameters, and the value's number as the credits.
func cncAnswer(path, value string, num int) string {
	v := testutil.JSONQuote(value)
	credits := strconv.Itoa(cncNumber(value))
	params := func(typ string) string {
		return `{"q":` + v + `,"type":"` + typ + `","num":` + strconv.Itoa(num) + `}`
	}
	switch path {
	case pathScrape:
		return `{"text":` + v + `,"echo":` + v + `,"credits":` + credits + `}`
	case pathImages:
		return `{"searchParameters":` + params("images") + `,"images":[{"title":` + v +
			`,"position":1}],"echo":` + v + `,"credits":` + credits + `}`
	}
	return `{"searchParameters":` + params("search") + `,"organic":[{"title":` + v +
		`,"position":1}],"echo":` + v + `,"credits":` + credits + `}`
}

// cncWire returns the exact request body of j.
func cncWire(j cncJob) string {
	if j.o.scrape {
		return `{"url":` + testutil.JSONQuoteNoHTML(j.value) + `}`
	}
	return `{"q":` + testutil.JSONQuoteNoHTML(j.value) + `,"num":` + strconv.Itoa(j.num) + `}`
}

// cncRequest returns the value and Num of a request body.
func cncRequest(body []byte) (string, int, bool) {
	var in struct {
		Q   string `json:"q"`
		URL string `json:"url"`
		Num int    `json:"num"`
	}
	if json.Unmarshal(body, &in) != nil {
		return "", 0, false
	}
	return in.Q + in.URL, in.Num, true
}

// cncHandler answers each request with cncAnswer for its path and its own
// value.
func cncHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		value, num, ok := cncRequest(body)
		if err != nil || !ok {
			http.Error(w, "unreadable request", http.StatusBadRequest)
			return
		}
		testutil.Serve(testutil.Step{Body: cncAnswer(r.URL.Path, value, num)})(w, r)
	}
}

// cncMemory returns an in-memory transport that answers as cncHandler does,
// and a function that returns, by origin host, the path and body of each
// request it received.
func cncMemory() (testutil.RoundTripFunc, func() map[string][]string) {
	var mu sync.Mutex
	seen := map[string][]string{}
	rt := func(r *http.Request) (*http.Response, error) {
		req := testutil.Record(r)
		mu.Lock()
		seen[r.URL.Host] = append(seen[r.URL.Host], req.Path+" "+string(req.Body))
		mu.Unlock()
		value, num, ok := cncRequest(req.Body)
		if !ok {
			return nil, errUnrelated
		}
		answer := cncAnswer(req.Path, value, num)
		return testutil.Response(r, http.StatusOK, io.NopCloser(strings.NewReader(answer)), int64(len(answer))), nil
	}
	return rt, func() map[string][]string {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(seen)
	}
}

// cncCall makes the call of j on c.
func cncCall(ctx context.Context, c *serper.Client, j cncJob) outcome {
	var opts []serper.CallOption
	switch j.capture {
	case cncCaptureOn:
		opts = append(opts, serper.WithRawCapture(true))
	case cncCaptureOff:
		opts = append(opts, serper.WithRawCapture(false))
	}
	switch j.o.name {
	case "Search":
		return outcomeOf(c.Search(ctx, serper.SearchRequest{Query: j.value, Num: j.num}, opts...))
	case "Images":
		return outcomeOf(c.Images(ctx, serper.ImagesRequest{Query: j.value, Num: j.num}, opts...))
	}
	return outcomeOf(c.Scrape(ctx, serper.ScrapeRequest{URL: j.value}, opts...))
}

// cncCaptured reports whether j is captured on a Client whose default is def.
func cncCaptured(j cncJob, def bool) bool {
	switch j.capture {
	case cncCaptureOn:
		return true
	case cncCaptureOff:
		return false
	}
	return def
}

// cncRun issues every job at once on c, each from a goroutine of its own
// that also closes c's idle connections just before and after its call, and
// returns the outcomes in job order.
func cncRun(ctx context.Context, c *serper.Client, jobs []cncJob) []outcome {
	out := make([]outcome, len(jobs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			<-start
			c.CloseIdleConnections()
			out[i] = cncCall(ctx, c, j)
			c.CloseIdleConnections()
		})
	}
	close(start)
	wg.Wait()
	return out
}

// cncCheck asserts that got is the answer to j, and to no other call, at
// origin: its metadata, its typed echo and, when captured is set, its raw
// capture all carry j's own operation and value.
func cncCheck(tb testing.TB, j cncJob, got outcome, origin string, captured bool) {
	tb.Helper()
	require.NoError(tb, got.err, "%s %q", j.o.name, j.value)
	m := got.meta
	assert.Equal(tb, j.o.op, m.Op)
	assert.Equal(tb, origin, m.Origin)
	assert.Equal(tb, j.o.path, m.Path)
	assert.Equal(tb, http.StatusOK, m.StatusCode)
	assert.Equal(tb, 1, m.Attempts)
	assert.Positive(tb, m.Elapsed)
	assert.Equal(tb, reported(int64(cncNumber(j.value))), m.Credits, "the credits echo the call's number")
	switch r := got.value.(type) {
	case *serper.SearchResponse:
		assert.Equal(tb, "Search", j.o.name)
		require.NotNil(tb, r.SearchParameters)
		assert.Equal(tb, serper.SearchParameters{Query: j.value, Type: "search", Num: j.num}, *r.SearchParameters)
		require.Len(tb, r.Organic, 1)
		assert.Equal(tb, j.value, r.Organic[0].Title)
	case *serper.ImagesResponse:
		assert.Equal(tb, "Images", j.o.name)
		require.NotNil(tb, r.SearchParameters)
		assert.Equal(tb, serper.SearchParameters{Query: j.value, Type: "images", Num: j.num}, *r.SearchParameters)
		require.Len(tb, r.Images, 1)
		assert.Equal(tb, j.value, r.Images[0].Title)
	case *serper.ScrapeResponse:
		assert.Equal(tb, "Scrape", j.o.name)
		assert.Equal(tb, j.value, r.Text)
	default:
		require.Failf(tb, "not a response", "%T", got.value)
	}
	assert.Equal(tb, captureState(captured, serper.RawCaptureComplete), m.RawCapture)
	assertCapture(tb, capturedBody(captured, cncAnswer(j.o.path, j.value, j.num)), got.raw, got.rawOK)
}

// cncWantWire returns, by origin host, the path and body of every request
// jobs send: duplicates appear once per call. host returns the host of an
// operation's origin.
func cncWantWire(jobs []cncJob, host func(opCase) string) map[string][]string {
	want := map[string][]string{}
	for _, j := range jobs {
		h := host(j.o)
		want[h] = append(want[h], j.o.path+" "+cncWire(j))
	}
	return want
}

// cncAssertWire asserts that the requests got, by origin host, are exactly
// those of want, as multisets: every call was sent once, duplicates included,
// to its own origin and path.
func cncAssertWire(tb testing.TB, want, got map[string][]string) {
	tb.Helper()
	require.Len(tb, got, len(want), "requests reached exactly the expected origins")
	for host, reqs := range want {
		assert.ElementsMatch(tb, reqs, got[host], "requests at %s", host)
	}
}

// cncServerWire returns the path and body of each request s received.
func cncServerWire(s *testutil.Server) []string {
	reqs := s.Requests()
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Path + " " + string(r.Body)
	}
	return out
}

// cncClientOptions returns the options of a Client whose capture default is
// def.
func cncClientOptions(def bool) []serper.Option {
	if def {
		return []serper.Option{serper.WithDefaultRawCapture()}
	}
	return nil
}

func TestConcurrencyMixedOperationsNoCrossTalk(t *testing.T) {
	jobs := cncJobs()
	require.GreaterOrEqual(t, len(jobs), 64)
	for _, def := range []bool{false, true} {
		name := "default capture " + strconv.FormatBool(def)
		for _, proto := range testutil.Protocols() {
			t.Run("tls "+proto.Name()+"/"+name, func(t *testing.T) {
				c, s := newServerClientProto(t, proto, cncHandler(), cncHandler(), cncClientOptions(def)...)
				out := cncRun(t.Context(), c, jobs)
				for i, j := range jobs {
					cncCheck(t, j, out[i], s.of(j.o).URL(), cncCaptured(j, def))
				}
				want := cncWantWire(jobs, func(o opCase) string { return originHost(s.of(o).URL()) })
				cncAssertWire(t, want, map[string][]string{
					originHost(s.search.URL()): cncServerWire(s.search),
					originHost(s.scrape.URL()): cncServerWire(s.scrape),
				})
				assert.Equal(t, len(want[originHost(s.search.URL())]), s.search.Hits(), "one hit per call")
				assert.Equal(t, len(want[originHost(s.scrape.URL())]), s.scrape.Hits(), "one hit per call")
			})
		}
		t.Run("memory/"+name, func(t *testing.T) {
			rt, seen := cncMemory()
			ct := &testutil.CountingTransport{Next: rt}
			c := newTransportClient(t, ct, cncClientOptions(def)...)
			out := cncRun(t.Context(), c, jobs)
			for i, j := range jobs {
				cncCheck(t, j, out[i], j.o.origin(), cncCaptured(j, def))
			}
			cncAssertWire(t, cncWantWire(jobs, func(o opCase) string { return originHost(o.origin()) }), seen())
			assert.Equal(t, len(jobs), ct.Attempts(), "one round trip per call")
		})
	}
}

func TestConcurrencyDuplicateQueriesAreDistinctCalls(t *testing.T) {
	const copies = 64
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			c, s := newServerClient(t, cncHandler(), cncHandler())
			j := cncJob{o: o, value: cncValue(o, 7), num: 3, capture: cncCaptureOn}
			out := cncRun(t.Context(), c, slices.Repeat([]cncJob{j}, copies))
			for _, got := range out {
				cncCheck(t, j, got, s.of(o).URL(), true)
			}
			assert.Equal(t, copies, s.of(o).Hits(), "no call is merged with an identical one")
			for i, got := range out[1:] {
				assert.NotSame(t, out[0].value, got.value, "call %d returns a response of its own", i+1)
			}
		})
	}
}

// cncCloseIdleLoop calls c.CloseIdleConnections once, and then over and over
// from another goroutine until the returned function is called, which
// returns the number of calls.
func cncCloseIdleLoop(c *serper.Client) func() int {
	done := make(chan struct{})
	calls := make(chan int)
	c.CloseIdleConnections()
	go func() {
		n := 1
		for {
			select {
			case <-done:
				calls <- n
				return
			default:
				c.CloseIdleConnections()
				n++
				runtime.Gosched()
			}
		}
	}()
	return func() int {
		close(done)
		return <-calls
	}
}

func TestConcurrencyCloseIdleConnectionsWhileCallsRun(t *testing.T) {
	jobs := cncJobs()
	t.Run("injected transport", func(t *testing.T) {
		s := servers{search: testutil.NewServer(t, cncHandler()), scrape: testutil.NewServer(t, cncHandler())}
		idle := &idleCounter{RoundTripper: s.search.Client().Transport}
		c := newClient(t, serper.WithHTTPClient(&http.Client{Transport: idle}),
			serper.WithSearchBaseURL(s.search.URL()), serper.WithScrapeBaseURL(s.scrape.URL()))
		stop := cncCloseIdleLoop(c)
		out := cncRun(t.Context(), c, jobs)
		assert.Positive(t, stop())
		for i, j := range jobs {
			cncCheck(t, j, out[i], s.of(j.o).URL(), cncCaptured(j, false))
		}
		assert.Zero(t, idle.closes.Load(), "CloseIdleConnections never reaches an injected transport")
	})

	t.Run("sdk transport", func(t *testing.T) {
		// The SDK's own transport does not trust the test certificate, so
		// every call fails its TLS handshake with the loopback server, while
		// CloseIdleConnections runs on that very transport.
		srv := testutil.NewServer(t, cncHandler())
		c := newClient(t, serper.WithSearchBaseURL(srv.URL()), serper.WithScrapeBaseURL(srv.URL()))
		stop := cncCloseIdleLoop(c)
		out := cncRun(t.Context(), c, jobs)
		assert.Positive(t, stop())
		for i, j := range jobs {
			_ = assertCallError(t, out[i].err, j.o.wantAt(srv.URL(), wantCall{
				Kind: serper.FailureTransport, Attempts: 1, Class: "tls",
				RawCapture: captureState(cncCaptured(j, false), serper.RawCaptureNoResponse),
			}))
		}
		assert.Zero(t, srv.Hits(), "no request got past the handshake")
	})
}

func TestConcurrencySharedResponseRawJSON(t *testing.T) {
	c, _ := newServerClient(t, cncHandler(), cncHandler())
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			j := cncJob{o: o, value: cncValue(o, 42), num: 5, capture: cncCaptureOn}
			got := cncCall(t.Context(), c, j)
			require.NoError(t, got.err)
			r, ok := got.value.(response)
			require.True(t, ok)
			want := []byte(cncAnswer(o.path, j.value, j.num))
			cncHammer(t, r.RawJSON, want, func() bool {
				return metaOf(got.value).RawCapture == serper.RawCaptureComplete &&
					!strings.Contains(fmt.Sprintf("%+v", got.value), "\"echo\"")
			})
		})
	}
	t.Run("CallError", func(t *testing.T) {
		refused := `{"credits":1,"echo":"shared"}`
		sc, _ := newScriptClient(t, testutil.Step{Body: refused})
		got := opCaseNamed("Search").call(t.Context(), sc, serper.WithRawCapture(true))
		ce := callError(t, got.err)
		cncHammer(t, ce.RawJSON, []byte(refused), func() bool {
			return ce.Error() == "serper: search: unexpected_response (HTTP 200): searchParameters" &&
				ce.Meta.RawCapture == serper.RawCaptureComplete
		})
	})
}

// cncHammer calls get from many goroutines at once, many times each, and
// asserts that every call returned want, though each goroutine overwrites
// every copy it gets, and that read, another read of the shared value run
// alongside, held each time. A copy that shared memory would also be a race.
func cncHammer(tb testing.TB, get func() ([]byte, bool), want []byte, read func() bool) {
	tb.Helper()
	const goroutines, rounds = 64, 16
	var bad atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			<-start
			for range rounds {
				cp, ok := get()
				if !ok || !bytes.Equal(want, cp) || !read() {
					bad.Add(1)
				}
				for i := range cp {
					cp[i] ^= 0xff
				}
			}
		})
	}
	close(start)
	wg.Wait()
	assert.Zero(tb, bad.Load(), "calls that saw another call's writes, or a wrong value")
	final, ok := get()
	require.True(tb, ok)
	assert.Equal(tb, want, final)
}
