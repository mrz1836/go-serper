package serper_test

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// rawMarker is planted only in members the SDK does not type: a capture may
// hold it, but no error, formatted value or JSON encoding may ever print it.
const rawMarker = "rawmarker-6b1d93"

// rawDecoded is the text that rawEscaped spells: what the typed members of
// every exotic body decode to.
const rawDecoded = "caf\u00e9 \u00e9 " + rawUTF8 + " \U0001F642 \u2028\u2029 / <&> \"q\""

// rawUTF8 is text that bodies carry as raw UTF-8, never escaped.
const rawUTF8 = "\u00e9\u4e16\u754c\U0001F642"

// rawLargePad is the padding of a large exotic body: past the 1 MiB up to
// which the SDK sizes its buffer from the declared length.
const rawLargePad = 1<<20 + 512

// rawEscaped returns rawDecoded as the inside of a JSON string, mixing JSON
// escapes (lower and upper case hex, a surrogate pair, U+2028 and U+2029, an
// escaped solidus and quotes) with raw UTF-8 and bare HTML characters.
func rawEscaped() string {
	return testutil.Esc(`caf~u00e9 ~u00E9 `) + rawUTF8 +
		testutil.Esc(` ~ud83d~ude42 ~u2028~u2029 ~/ <&> ~"q~"`)
}

// rawExoticBody returns a successful answer for o that any re-encoding would
// change: CRLF and other insignificant whitespace before, inside and after
// the object, members out of order, duplicate member names (the last one
// wins, as in encoding/json), nested unknown members, numbers beyond the
// range and precision of float64 in untyped members, JSON escapes next to
// raw UTF-8, and pad bytes of padding in an untyped member. Its typed text
// members hold rawDecoded, and it reports 1 credit.
func rawExoticBody(o opCase, pad int) string {
	text := `"` + rawEscaped() + `"`
	var core string
	switch o.name {
	case "Search":
		core = `"organic" : [ {"position":1,"title":` + text + `,"rating":4.50000000000000000001,` +
			`"x":{"y":{"z":[]}}} ],` + "\r\n" +
			`  "searchParameters" : { "q":"first", "type":"search" , "q":` + text + ` }`
	case "Images":
		core = `"images":[{"title":` + text + `,"imageWidth":1280,"position":1,"x":[1e400]}],` + "\r\n" +
			`  "searchParameters":{"q":"first","type":"images","q":` + text + `}`
	default:
		core = `"metadata":{"title":"x","n":{"m":1e400}},"markdown" : null ,` + "\r\n" +
			`  "text":"first",` + "\r\n" + `  "text" : ` + text
	}
	return "\r\n\t {\r\n  \"zz\" : 0 ,\r\n  " + core + ",\r\n  " +
		`"answerBox":{"deep":[{"a":[1.000000000000000000001,1e400,-1e400,1E-400,-0.0,0e0,null,true,{},[]]}],` +
		`"dup":1,"dup":2},` + "\r\n" +
		`  "precise" : 1.000000000000000000001, "huge":1e400, "huge":-1e400,` + "\r\n" +
		`  "dup":"a","dup":"b", "escaped":"` + testutil.Esc(`~u0041~u00e9~ud83d~ude42~/~b~f~n~r~t~"~\~u0000`) +
		`", "utf8":"` + rawUTF8 + `",` + "\r\n" +
		`  "pad":"` + strings.Repeat(" ", pad) + `",` + "\r\n" +
		`  "credits" : 1` + "\r\n}\r\n  "
}

// rawTypedTexts returns the typed text members of v, a successful response:
// for Search and Images the searchParameters query and the first result's
// title, for Scrape the text.
func rawTypedTexts(tb testing.TB, v any) []string {
	tb.Helper()
	switch r := v.(type) {
	case *serper.SearchResponse:
		require.NotNil(tb, r.SearchParameters)
		require.Len(tb, r.Organic, 1)
		return []string{r.SearchParameters.Query, r.Organic[0].Title}
	case *serper.ImagesResponse:
		require.NotNil(tb, r.SearchParameters)
		require.Len(tb, r.Images, 1)
		return []string{r.SearchParameters.Query, r.Images[0].Title}
	case *serper.ScrapeResponse:
		assert.Nil(tb, r.Markdown, "a null markdown is absent")
		return []string{r.Text}
	}
	require.Failf(tb, "not a response", "%T", v)
	return nil
}

// rawMissing returns the failure of an HTTP 200 answer of o that is a valid
// JSON object without o's success member, but for its Op, Origin and Path:
// unexpected_response at searchParameters for Search and Images,
// malformed_response at text for Scrape, with the body kept when capture is
// on.
func rawMissing(o opCase, credits serper.Credits) wantCall {
	w := wantCall{
		Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", OutcomeUnknown: true, Status: 200,
		Attempts: 1, Credits: credits, RawCapture: serper.RawCaptureComplete,
	}
	if o.scrape {
		w.Kind, w.Field = serper.FailureMalformedResponse, "text"
	}
	return w
}

// rawRefusing returns the operation whose exotic body o refuses: Scrape's,
// which has no searchParameters, for Search and Images, and Search's, which
// has no text, for Scrape.
func rawRefusing(o opCase) opCase {
	if o.scrape {
		return opCaseNamed("Search")
	}
	return opCaseNamed("Scrape")
}

// rawRoute is one way an answer reaches the decoder.
type rawRoute struct {
	name string
	// bytewise routes read one byte at a time: too slow for a large body.
	bytewise bool
	// client returns a client whose calls of o are answered with body, and
	// the origin those calls go to.
	client func(tb testing.TB, o opCase, body string) (*serper.Client, string)
}

// rawRoutes returns the routes of the byte identity test: in memory, with
// the length declared or not and with short and empty reads, and over both
// TLS servers in each protocol, plain and gzip-encoded.
func rawRoutes() []rawRoute {
	routes := make([]rawRoute, 0, 3+2*len(testutil.Protocols()))
	routes = append(routes,
		rawRoute{name: "memory declared", client: func(tb testing.TB, o opCase, body string) (*serper.Client, string) {
			tb.Helper()
			c, _ := newScriptClient(tb, testutil.Step{Body: body})
			return c, o.origin()
		}},
		rawRoute{name: "memory unknown length bytewise", bytewise: true, client: func(tb testing.TB, o opCase, body string,
		) (*serper.Client, string) {
			tb.Helper()
			b := testutil.NewBody(body)
			b.Step = 1
			return newTransportClient(tb, testutil.TransportWith(http.StatusOK, nil, -1, b)), o.origin()
		}},
		rawRoute{name: "memory empty reads", client: func(tb testing.TB, o opCase, body string) (*serper.Client, string) {
			tb.Helper()
			b := testutil.NewBody(body)
			b.Step, b.Empties = 4093, 7
			return newTransportClient(tb, testutil.TransportWith(http.StatusOK, nil, int64(len(body)), b)), o.origin()
		}},
	)
	for _, proto := range testutil.Protocols() {
		routes = append(routes,
			rawRoute{name: "tls " + proto.Name(), client: rawServerRoute(proto, func(body string) http.HandlerFunc {
				return testutil.Serve(testutil.Step{Body: body})
			})},
			rawRoute{name: "tls " + proto.Name() + " gzip", client: rawServerRoute(proto, rawGzip)},
		)
	}
	return routes
}

// rawServerRoute returns a route over two TLS servers speaking proto, each
// running the handler that serve returns for the body.
func rawServerRoute(proto testutil.Protocol, serve func(body string) http.HandlerFunc,
) func(tb testing.TB, o opCase, body string) (*serper.Client, string) {
	return func(tb testing.TB, o opCase, body string) (*serper.Client, string) {
		tb.Helper()
		c, s := newServerClientProto(tb, proto, serve(body), serve(body))
		return c, s.of(o).URL()
	}
}

// rawGzip returns a handler that sends body gzip-encoded, with
// Content-Encoding: gzip, which the client's transport decodes itself.
func rawGzip(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", mediaJSON)
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, body)
		_ = zw.Close()
	}
}

func TestRawCaptureByteIdentity(t *testing.T) {
	for _, o := range opCases() {
		for _, route := range rawRoutes() {
			for _, size := range []struct {
				name string
				pad  int
			}{{name: "small"}, {name: "large", pad: rawLargePad}} {
				if size.pad > 0 && route.bytewise {
					continue
				}
				t.Run(o.name+"/"+route.name+"/"+size.name, func(t *testing.T) {
					rawCheckIdentity(t, o, route, size.pad)
				})
			}
		}
	}
}

// rawCheckIdentity checks, over route, that a capture of o's exotic body is
// that body exactly, and that a capture of the exotic body o refuses is that
// body exactly too, kept on the CallError.
func rawCheckIdentity(t *testing.T, o opCase, route rawRoute, pad int) {
	t.Helper()
	body := rawExoticBody(o, pad)
	c, _ := route.client(t, o, body)
	got := o.call(t.Context(), c, serper.WithRawCapture(true))
	require.NoError(t, got.err)
	assertCapture(t, body, got.raw, got.rawOK)
	assert.Equal(t, serper.RawCaptureComplete, got.meta.RawCapture)
	assert.Equal(t, reported(1), got.meta.Credits)
	for _, text := range rawTypedTexts(t, got.value) {
		assert.Equal(t, rawDecoded, text, "escapes and raw UTF-8 decode")
	}

	refused := rawExoticBody(rawRefusing(o), pad)
	c, origin := route.client(t, o, refused)
	got = o.call(t.Context(), c, serper.WithRawCapture(true))
	ce := assertCallError(t, got.err, o.wantAt(origin, rawMissing(o, reported(1))))
	raw, ok := ce.RawJSON()
	assertCapture(t, refused, raw, ok, "a complete, valid JSON body that was refused is kept")
}

func TestRawCapturePrecedence(t *testing.T) {
	on, off := serper.WithRawCapture(true), serper.WithRawCapture(false)
	def := serper.WithDefaultRawCapture()
	tests := []struct {
		name   string
		client []serper.Option
		call   []serper.CallOption
		want   bool
	}{
		{name: "off by default"},
		{name: "call on", call: []serper.CallOption{on}, want: true},
		{name: "call off", call: []serper.CallOption{off}},
		{name: "client default on", client: []serper.Option{def}, want: true},
		{name: "client default twice", client: []serper.Option{def, def}, want: true},
		{name: "client default among nil options", client: []serper.Option{nil, def, nil}, want: true},
		{name: "client default then call off", client: []serper.Option{def}, call: []serper.CallOption{off}},
		{name: "client default then call on", client: []serper.Option{def}, call: []serper.CallOption{on}, want: true},
		{name: "last wins on off", call: []serper.CallOption{on, off}},
		{name: "last wins off on", call: []serper.CallOption{off, on}, want: true},
		{name: "last wins on off on", call: []serper.CallOption{on, off, on}, want: true},
		{name: "last wins over default", client: []serper.Option{def}, call: []serper.CallOption{off, on, off}},
		{name: "nil after on", call: []serper.CallOption{on, nil}, want: true},
		{name: "nil before on", call: []serper.CallOption{nil, on}, want: true},
		{name: "nil alone", call: []serper.CallOption{nil}},
		{name: "nil alone keeps default", client: []serper.Option{def}, call: []serper.CallOption{nil, nil}, want: true},
		{name: "nil after off keeps off", client: []serper.Option{def}, call: []serper.CallOption{off, nil}},
	}
	for _, tt := range tests {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				state := captureState(tt.want, serper.RawCaptureComplete)
				c := newTransportClient(t, testutil.NewScript(testutil.Step{Body: o.ok}, testutil.Step{Body: `{"credits":1}`}),
					tt.client...)

				got := o.call(t.Context(), c, tt.call...)
				require.NoError(t, got.err)
				assert.Equal(t, state, got.meta.RawCapture, "success")
				assertCapture(t, capturedBody(tt.want, o.ok), got.raw, got.rawOK, "success")

				got = o.call(t.Context(), c, tt.call...)
				ce := callError(t, got.err)
				assert.Equal(t, state, ce.Meta.RawCapture, "a refused body")
				raw, ok := ce.RawJSON()
				assertCapture(t, capturedBody(tt.want, `{"credits":1}`), raw, ok, "a refused body")
			})
		}
	}
}

// rawOKTransport answers each operation's path with its smallest successful
// answer, in memory, every time.
func rawOKTransport() testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return testutil.Reply(r, testutil.Step{Body: okBody(r.URL.Path)})
	}
}

func TestRawCaptureCallOptionsNeverChangeTheClient(t *testing.T) {
	for _, def := range []bool{false, true} {
		t.Run("default "+strconv.FormatBool(def), func(t *testing.T) {
			var opts []serper.Option
			if def {
				opts = append(opts, serper.WithDefaultRawCapture())
			}
			c := newTransportClient(t, rawOKTransport(), opts...)
			before := c.String()
			assert.Contains(t, before, "defaultRawCapture: "+strconv.FormatBool(def))
			for round := range 3 {
				for _, o := range opCases() {
					rawCheckDefaultKept(t, c, o, def, round)
				}
			}
			assert.Equal(t, before, c.String(), "per-call options never touch the Client")
		})
	}
}

// rawCheckDefaultKept calls o on c, whose default capture is def, once with
// the opposite per-call option and then without one, and checks that the
// later calls fall back to the default.
func rawCheckDefaultKept(t *testing.T, c *serper.Client, o opCase, def bool, round int) {
	t.Helper()
	got := o.call(t.Context(), c, serper.WithRawCapture(!def))
	require.NoError(t, got.err)
	assert.Equal(t, !def, got.rawOK, "round %d: the call's own option", round)
	want := captureState(def, serper.RawCaptureComplete)
	for _, callOpts := range [][]serper.CallOption{nil, {nil}} {
		got = o.call(t.Context(), c, callOpts...)
		require.NoError(t, got.err)
		assert.Equal(t, def, got.rawOK, "round %d: a later call falls back to the default", round)
		assert.Equal(t, want, got.meta.RawCapture)
	}
}

func TestRawJSONReturnsNewCopy(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			body := rawExoticBody(o, 0)
			refused := rawExoticBody(rawRefusing(o), 0)
			c, _ := newScriptClient(t, testutil.Step{Body: body}, testutil.Step{Body: refused})

			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			require.NoError(t, got.err)
			r, ok := got.value.(response)
			require.True(t, ok)
			for i := range got.raw {
				got.raw[i] = 'X' // the copy outcomeOf took
			}
			rawAssertFreshCopies(t, r.RawJSON, body)
			for _, text := range rawTypedTexts(t, got.value) {
				assert.Equal(t, rawDecoded, text, "mutating a copy never changes the response")
			}

			got = o.call(t.Context(), c, serper.WithRawCapture(true))
			ce := callError(t, got.err)
			rawAssertFreshCopies(t, ce.RawJSON, refused)
		})
	}
}

// rawAssertFreshCopies asserts that every call of get returns a new copy of
// want: two calls never share a backing array, and mutating or appending to
// one copy never changes another, nor a later one.
func rawAssertFreshCopies(tb testing.TB, get func() ([]byte, bool), want string) {
	tb.Helper()
	a, ok := get()
	require.True(tb, ok)
	assertBody(tb, want, a)
	b, ok := get()
	require.True(tb, ok)
	require.NotEmpty(tb, b)
	assert.NotSame(tb, &a[0], &b[0], "each call returns a new backing array")

	for i := range a {
		a[i] = 'X'
	}
	a = append(a[:1], strings.Repeat("Y", 2*len(want))...)
	assertBody(tb, want, b)
	later, ok := get()
	require.True(tb, ok)
	assertBody(tb, want, later)
	assert.NotSame(tb, &a[0], &later[0])

	b[len(b)-1] = 'Z'
	later, _ = get()
	assertBody(tb, want, later)
}

func TestRawJSONConcurrentCopiesAreIsolated(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			body := rawExoticBody(o, 0)
			refused := rawExoticBody(rawRefusing(o), 0)
			c, _ := newScriptClient(t, testutil.Step{Body: body}, testutil.Step{Body: refused})
			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			require.NoError(t, got.err)
			r, ok := got.value.(response)
			require.True(t, ok)
			rawAssertIsolatedCopies(t, r.RawJSON, body)

			got = o.call(t.Context(), c, serper.WithRawCapture(true))
			rawAssertIsolatedCopies(t, callError(t, got.err).RawJSON, refused)
		})
	}
}

// rawAssertIsolatedCopies takes copies with get from many goroutines at once,
// each of which overwrites its own copy, and asserts that every goroutine got
// want in a backing array of its own, that its writes stayed in it, and that
// a later copy is still want.
func rawAssertIsolatedCopies(tb testing.TB, get func() ([]byte, bool), want string) {
	tb.Helper()
	const goroutines = 32
	copies := make([][]byte, goroutines)
	intact := make([]bool, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			<-start
			cp, ok := get()
			intact[g] = ok && string(cp) == want
			fill := rawLetters[g%len(rawLetters)]
			for i := range cp {
				cp[i] = fill
			}
			copies[g] = cp
		})
	}
	close(start)
	wg.Wait()

	seen := make(map[*byte]int, goroutines)
	for g, cp := range copies {
		assert.True(tb, intact[g], "goroutine %d got the body", g)
		require.Len(tb, cp, len(want))
		fill := rawLetters[g%len(rawLetters)]
		assert.Equal(tb, strings.Repeat(string(fill), len(want)), string(cp), "goroutine %d's writes stayed its own", g)
		if prev, dup := seen[&cp[0]]; dup {
			assert.Failf(tb, "shared backing array", "goroutines %d and %d", prev, g)
		}
		seen[&cp[0]] = g
	}
	later, ok := get()
	require.True(tb, ok)
	assertBody(tb, want, later)
}

// rawLetters are the bytes goroutines fill their copies with.
const rawLetters = "abcdefghijklmnopqrstuvwxyz"

// rawCase is one row of the raw capture state table, for one operation: how
// the call is answered and what it must return.
type rawCase struct {
	name string
	// answer returns a new transport for the call; cancel cancels the
	// caller's context.
	answer func(tb testing.TB, cancel context.CancelFunc) http.RoundTripper
	// ctx derives the caller's context from parent; nil means a cancelable
	// child.
	ctx func(parent context.Context) (context.Context, context.CancelFunc)
	// opts are client options besides the capture's.
	opts []serper.Option
	// bubble marks a call that waits for a deadline, which then runs in a
	// synctest bubble, where waiting takes no time.
	bubble bool
	// fail is the failure, without Op, Origin, Path and RawCapture; nil for
	// a success.
	fail *wantCall
	// on is the capture state when capture is on.
	on serper.RawCaptureState
	// keep is the body a capture keeps, "" for none.
	keep string
}

// rawStep returns an answer that is step.
func rawStep(step testutil.Step) func(testing.TB, context.CancelFunc) http.RoundTripper {
	return func(testing.TB, context.CancelFunc) http.RoundTripper { return testutil.NewScript(step) }
}

// rawBody returns an answer of HTTP 200 with header h (Content-Type
// application/json when nil) and the body newBody returns, declared with
// declared (-1 for unknown).
func rawBody(h http.Header, declared int64, newBody func() io.ReadCloser,
) func(testing.TB, context.CancelFunc) http.RoundTripper {
	return func(testing.TB, context.CancelFunc) http.RoundTripper {
		return testutil.TransportWith(http.StatusOK, h, declared, newBody())
	}
}

// rawSuccess is the row of a success: the body is kept, exactly.
func rawSuccess(body string) rawCase {
	return rawCase{
		name: "success", answer: rawStep(testutil.Step{Body: body}), on: serper.RawCaptureComplete, keep: body,
	}
}

// rawRefused is the row of a complete, valid JSON HTTP 200 body that decoding
// or the success rule refuses: the body is kept on the CallError.
func rawRefused(name, body string, kind serper.FailureKind, field string, credits serper.Credits) rawCase {
	return rawCase{
		name: "refused " + name, answer: rawStep(testutil.Step{Body: body}),
		fail: &wantCall{
			Kind: kind, Field: field, OutcomeUnknown: true, Status: 200, Attempts: 1, Credits: credits,
		},
		on: serper.RawCaptureComplete, keep: body,
	}
}

// rawFailed is the row of a response whose body is never kept.
func rawFailed(name string, answer func(testing.TB, context.CancelFunc) http.RoundTripper, fail wantCall) rawCase {
	return rawCase{name: name, answer: answer, fail: &fail, on: serper.RawCaptureFailed}
}

// rawNoResponse is the row of a call that received no response.
func rawNoResponse(name string, answer func(testing.TB, context.CancelFunc) http.RoundTripper, fail wantCall) rawCase {
	return rawCase{name: name, answer: answer, fail: &fail, on: serper.RawCaptureNoResponse}
}

// rawCases returns the state table for o.
func rawCases(o opCase) []rawCase {
	cases := []rawCase{rawSuccess(" " + o.ok + "\r\n")}
	cases = append(cases, rawRefusedCases(o)...)
	cases = append(cases, rawFailed200Cases(o)...)
	for _, code := range statusCodes() {
		// An error body may even be a valid answer with credits: it is
		// never kept, and its credits are never read.
		body := o.ok[:len(o.ok)-1] + `,"credits":5}`
		cases = append(cases, rawFailed("status "+strconv.Itoa(code), rawStep(testutil.Step{Status: code, Body: body}),
			wantCall{
				Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code), Status: code, Attempts: 1,
			}))
	}
	return append(cases, rawNoResponseCases()...)
}

// rawRefusedCases returns the rows of valid JSON bodies that o refuses.
func rawRefusedCases(o opCase) []rawCase {
	malformed := serper.FailureMalformedResponse
	empty := rawMissing(o, serper.Credits{})
	cases := []rawCase{
		rawRefused("empty object", `{}`, empty.Kind, empty.Field, serper.Credits{}),
		rawRefused("empty array", `[]`, malformed, "", serper.Credits{}),
		rawRefused("null", `null`, malformed, "", serper.Credits{}),
		rawRefused("spaced array", " [ 1 ,\r\n 2 ]\r\n", malformed, "", serper.Credits{}),
		rawRefused("array of answers", `[{"searchParameters":{},"text":"","credits":1}]`, malformed, "", serper.Credits{}),
		rawRefused("string", `"text"`, malformed, "", serper.Credits{}),
		rawRefused("number", `1e400`, malformed, "", serper.Credits{}),
		rawRefused("boolean", `true`, malformed, "", serper.Credits{}),
	}
	switch o.name {
	case "Search":
		unexpected := serper.FailureUnexpectedResponse
		return append(cases,
			rawRefused("wrong type", `{"searchParameters":{"q":1},"credits":3}`, malformed, "searchParameters.q", reported(3)),
			rawRefused("wrong list type", `{"searchParameters":{},"organic":{}}`, malformed, "organic", serper.Credits{}),
			rawRefused("wrong item type", `{"searchParameters":{},"organic":[{},{"position":"2"}],"credits":0}`,
				malformed, "organic.position", reported(0)),
			rawRefused("null item", `{"searchParameters":{},"organic":[{},null]}`, malformed, "organic[1]", serper.Credits{}),
			rawRefused("null nested item", `{"searchParameters":{},"organic":[{"sitelinks":[{},null]}]}`,
				malformed, "organic[0].sitelinks[1]", serper.Credits{}),
			rawRefused("bad attributes", `{"searchParameters":{},"organic":[{"attributes":{"a":1}}]}`,
				malformed, "organic[0].attributes", serper.Credits{}),
			rawRefused("bad knowledge graph attributes", `{"searchParameters":{},"knowledgeGraph":{"attributes":[]}}`,
				malformed, "knowledgeGraph.attributes", serper.Credits{}),
			rawRefused("searchParameters not an object", `{"searchParameters":[]}`, malformed, "searchParameters",
				serper.Credits{}),
			rawRefused("missing searchParameters", "{\r\n  \"organic\" : [ ] ,\r\n  \"credits\" : 2\r\n}\r\n",
				unexpected, "searchParameters", reported(2)),
			rawRefused("null searchParameters", `{"searchParameters":null,"credits":1.0}`,
				unexpected, "searchParameters", invalidCredits()),
		)
	case "Images":
		unexpected := serper.FailureUnexpectedResponse
		return append(cases,
			rawRefused("wrong type", `{"searchParameters":{},"images":[{"imageWidth":"1"}],"credits":3}`,
				malformed, "images.imageWidth", reported(3)),
			rawRefused("wrong list type", `{"searchParameters":{},"images":"x"}`, malformed, "images", serper.Credits{}),
			rawRefused("null item", `{"searchParameters":{},"images":[null]}`, malformed, "images[0]", serper.Credits{}),
			rawRefused("wrong searchParameters member", `{"searchParameters":{"num":"1"}}`,
				malformed, "searchParameters.num", serper.Credits{}),
			rawRefused("missing searchParameters", `{ "images" : [] , "credits" : 2 }`,
				unexpected, "searchParameters", reported(2)),
			rawRefused("null searchParameters", `{"searchParameters":null,"images":[],"credits":"1"}`,
				unexpected, "searchParameters", invalidCredits()),
		)
	}
	return append(cases,
		rawRefused("wrong type", `{"text":1,"credits":3}`, malformed, "text", reported(3)),
		rawRefused("wrong markdown type", `{"text":"","markdown":[]}`, malformed, "markdown", serper.Credits{}),
		rawRefused("missing text", "{\r\n  \"markdown\" : \"m\" ,\r\n  \"credits\" : 2\r\n}", malformed, "text", reported(2)),
		rawRefused("null text", `{"text":null,"credits":-1}`, malformed, "text", invalidCredits()),
	)
}

// rawFailed200Cases returns the rows of HTTP 200 answers whose body is never
// kept: too large, cut short, stalled, of the wrong encoding or type, not
// valid UTF-8 or JSON, or ended by a deadline or a cancellation.
func rawFailed200Cases(o opCase) []rawCase {
	ok := o.ok
	failed := func(kind serper.FailureKind, class string) wantCall {
		return wantCall{Kind: kind, OutcomeUnknown: true, Status: 200, Attempts: 1, Class: class}
	}
	transport, malformed := serper.FailureTransport, serper.FailureMalformedResponse
	unexpected, tooLarge := serper.FailureUnexpectedResponse, serper.FailureResponseTooLarge
	big := padded(ok, len(ok)+2048)
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	stalled := func() io.ReadCloser {
		b := testutil.NewBody(ok[:5])
		b.Stall = true
		return b
	}
	blocking := func() io.ReadCloser { return testutil.NewBlockingBody(ok[:5]) }

	return []rawCase{
		rawLimited(rawFailed("too large declared", rawBody(nil, int64(len(big)), rawReader(big)), failed(tooLarge, ""))),
		rawLimited(rawFailed("too large as read", rawBody(nil, -1, rawReader(big)), failed(tooLarge, ""))),
		rawFailed("truncated", rawBody(nil, int64(len(ok)+10), rawReader(ok)), failed(transport, "closed")),
		rawFailed("cut by an unexpected EOF", rawBody(nil, int64(len(ok)+10), func() io.ReadCloser {
			return endingWith(ok[:4], io.ErrUnexpectedEOF)
		}), failed(transport, "closed")),
		rawFailed("longer than declared", rawBody(nil, int64(len(ok)-1), rawReader(ok)), failed(transport, "closed")),
		rawFailed("reset during the body", rawBody(nil, -1, func() io.ReadCloser { return endingWith(ok[:4], reset) }),
			failed(transport, "reset")),
		rawFailed("stalled", rawBody(nil, -1, stalled), failed(transport, "stalled")),
		rawFailed("undecoded content encoding", rawBody(http.Header{
			"Content-Type": {mediaJSON}, "Content-Encoding": {"gzip"},
		}, int64(len(ok)), rawReader(ok)), failed(unexpected, "")),
		rawFailed("content type not JSON", rawBody(http.Header{"Content-Type": {"text/html"}}, int64(len(ok)), rawReader(ok)),
			failed(unexpected, "")),
		rawFailed("two content types", rawBody(http.Header{"Content-Type": {mediaJSON, mediaJSON}}, int64(len(ok)), rawReader(ok)),
			failed(unexpected, "")),
		rawFailed("invalid UTF-8", rawStep(testutil.Step{Body: ok[:len(ok)-1] + `,"x":"` + "\xff" + `"}`}),
			failed(malformed, "")),
		rawFailed("invalid JSON", rawStep(testutil.Step{Body: ok[:len(ok)-1]}), failed(malformed, "")),
		rawFailed("trailing data", rawStep(testutil.Step{Body: ok + " {}"}), failed(malformed, "")),
		rawFailed("trailing garbage", rawStep(testutil.Step{Body: ok + "x"}), failed(malformed, "")),
		rawFailed("empty", rawStep(testutil.Step{Body: ""}), failed(malformed, "")),
		rawFailed("whitespace only", rawStep(testutil.Step{Body: " \r\n"}), failed(malformed, "")),
		rawFailed("canceled during the body", func(_ testing.TB, cancel context.CancelFunc) http.RoundTripper {
			return testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				cancel()
				return testutil.Response(r, http.StatusOK, blocking(), -1), nil
			})
		}, failed(serper.FailureCanceled, "")),
		rawInBubble(rawFailed("timeout during the body", rawBody(nil, -1, blocking), failed(serper.FailureTimeout, "")), nil),
		rawInBubble(rawFailed("caller deadline during the body", rawBody(nil, -1, blocking),
			failed(serper.FailureDeadlineExceeded, "")), rawShortDeadline),
	}
}

// rawLimited returns rc with a response limit of 1 KiB.
func rawLimited(rc rawCase) rawCase {
	rc.opts = []serper.Option{serper.WithMaxResponseBytes(1024)}
	return rc
}

// rawInBubble returns rc run in a synctest bubble, with an SDK timeout of a
// minute and the caller's context from ctx, if not nil.
func rawInBubble(rc rawCase, ctx func(parent context.Context) (context.Context, context.CancelFunc)) rawCase {
	rc.bubble, rc.opts, rc.ctx = true, []serper.Option{serper.WithTimeout(time.Minute)}, ctx
	return rc
}

// rawReader returns a function that returns a new reader over s.
func rawReader(s string) func() io.ReadCloser {
	return func() io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
}

// rawShortDeadline returns a child of parent with a deadline a second away,
// before the SDK's own.
func rawShortDeadline(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Second)
}

// rawNoResponseCases returns the rows of calls that received no response:
// transport failures of every class before the headers, a deadline or a
// cancellation before the headers, and a context already done (see
// rawDoneCases).
func rawNoResponseCases() []rawCase {
	// A transport that fails without reading the request body, as a real one
	// does before it has a connection: only then can a failure leave the
	// outcome known.
	err := func(e error) func(testing.TB, context.CancelFunc) http.RoundTripper {
		return func(testing.TB, context.CancelFunc) http.RoundTripper {
			return testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, e })
		}
	}
	sent := func(class string, outcomeUnknown bool) wantCall {
		return wantCall{Kind: serper.FailureTransport, OutcomeUnknown: outcomeUnknown, Attempts: 1, Class: class}
	}
	refused := syscall.ECONNREFUSED
	timeout := wantCall{Kind: serper.FailureTimeout, OutcomeUnknown: true, Attempts: 1}
	blocking := func(testing.TB, context.CancelFunc) http.RoundTripper {
		return testutil.RoundTripFunc(testutil.BlockUntilDone)
	}
	return append([]rawCase{
		rawNoResponse("closed", err(io.ErrUnexpectedEOF), sent("closed", true)),
		rawNoResponse("EOF", err(io.EOF), sent("closed", true)),
		rawNoResponse("net closed", err(net.ErrClosed), sent("closed", true)),
		rawNoResponse("reset", err(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), sent("reset", true)),
		rawNoResponse("aborted", err(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNABORTED}), sent("reset", true)),
		rawNoResponse("broken pipe", err(&net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}), sent("reset", true)),
		rawNoResponse("dial", err(&net.OpError{Op: "dial", Net: "tcp", Err: refused}), sent("dial", false)),
		rawNoResponse("dns", err(&net.DNSError{Err: "no such host", Name: "www.example.invalid", IsNotFound: true}),
			sent("dns", false)),
		// A bad TLS record can come after the request was written.
		rawNoResponse("tls record", err(tls.RecordHeaderError{Msg: "not a TLS handshake"}), sent("tls", true)),
		rawNoResponse("tls certificate", err(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}),
			sent("tls", false)),
		rawNoResponse("proxy", err(&net.OpError{Op: "proxyconnect", Net: "tcp", Err: refused}), sent("proxy", false)),
		rawNoResponse("other", err(errUnrelated), sent("other", true)),
		rawNoResponse("net timeout", err(testutil.TimeoutError{}), timeout),
		rawNoResponse("dial timeout", err(&net.OpError{Op: "dial", Net: "tcp", Err: testutil.TimeoutError{}}),
			wantCall{Kind: serper.FailureTimeout, Attempts: 1}),
		rawNoResponse("canceled before the headers", func(_ testing.TB, cancel context.CancelFunc) http.RoundTripper {
			return testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
		}, wantCall{Kind: serper.FailureCanceled, OutcomeUnknown: true, Attempts: 1}),
		rawInBubble(rawNoResponse("timeout before the headers", blocking, timeout), nil),
		rawInBubble(rawNoResponse("caller deadline before the headers", blocking,
			wantCall{Kind: serper.FailureDeadlineExceeded, OutcomeUnknown: true, Attempts: 1}), rawShortDeadline),
	}, rawDoneCases()...)
}

// rawDoneCases returns the rows of a context already done, which sends
// nothing.
func rawDoneCases() []rawCase {
	offline := func(tb testing.TB, _ context.CancelFunc) http.RoundTripper {
		tb.Helper()
		return offlineTransport(tb)
	}
	dones := doneContexts()
	cases := make([]rawCase, 0, len(dones))
	for _, done := range dones {
		rc := rawNoResponse("context already "+done.name, offline, wantCall{Kind: done.kind})
		rc.ctx = done.ctx
		cases = append(cases, rc)
	}
	return cases
}

// rawContext returns the caller's context of a call of row rc, a cancelable
// child of parent unless the row makes its own.
func rawContext(parent context.Context, rc rawCase) (context.Context, context.CancelFunc) {
	if rc.ctx != nil {
		return rc.ctx(parent)
	}
	return context.WithCancel(parent)
}

func TestRawCaptureStateTable(t *testing.T) {
	for _, o := range opCases() {
		for _, rc := range rawCases(o) {
			for _, m := range captureModes() {
				t.Run(o.name+"/"+rc.name+"/"+m.name, func(t *testing.T) {
					if rc.bubble {
						synctest.Test(t, func(t *testing.T) { rawRunCase(t, o, rc, m) })
						return
					}
					rawRunCase(t, o, rc, m)
				})
			}
		}
	}
}

// rawRunCase makes the call of row rc for o, with capture as m asks, and
// checks the result, the capture state and the kept bytes.
func rawRunCase(t *testing.T, o opCase, rc rawCase, m captureMode) {
	t.Helper()
	ctx, cancel := rawContext(t.Context(), rc)
	defer cancel()
	ct := &testutil.CountingTransport{Next: rc.answer(t, cancel)}
	c := newTransportClient(t, ct, append(slices.Clone(rc.opts), m.client...)...)
	got := o.call(ctx, c, m.call...)
	state := captureState(m.on, rc.on)
	if rc.fail == nil {
		require.NoError(t, got.err)
		assert.Equal(t, state, got.meta.RawCapture)
		assertCapture(t, capturedBody(m.on, rc.keep), got.raw, got.rawOK)
		assert.Equal(t, 1, ct.Attempts())
		return
	}
	want := *rc.fail
	want.RawCapture = state
	ce := assertCallError(t, got.err, o.want(want))
	raw, ok := ce.RawJSON()
	assertCapture(t, capturedBody(m.on, rc.keep), raw, ok, "the refused body is kept exactly when capture is on")
	assert.Equal(t, want.Attempts, ct.Attempts(), "round trips the transport saw")
}

func TestRawCaptureValidationErrorHasNoMetadata(t *testing.T) {
	meta := reflect.TypeFor[serper.Metadata]()
	for f := range reflect.TypeFor[serper.ValidationError]().Fields() {
		assert.NotEqual(t, meta, f.Type, "field %s", f.Name)
	}
	_, hasRaw := any(&serper.ValidationError{}).(interface{ RawJSON() ([]byte, bool) })
	assert.False(t, hasRaw, "a ValidationError has no capture to return")

	c := newOfflineClient(t, serper.WithDefaultRawCapture())
	var nilCtx context.Context
	for _, o := range opCases() {
		field := "Query"
		if o.scrape {
			field = "URL"
		}
		for _, opts := range [][]serper.CallOption{nil, {serper.WithRawCapture(true)}} {
			err := invalidCall(t.Context(), c, o, opts...)
			assertValidation(t, err, serper.ErrInvalidRequest, o.op, field, reasonRequired)
			got := o.call(nilCtx, c, opts...)
			assertValidation(t, got.err, serper.ErrInvalidRequest, o.op, "ctx", reasonNil)
			got = o.call(t.Context(), nil, opts...)
			assertValidation(t, got.err, serper.ErrInvalidConfig, o.op, "Client", reasonNotInitialized)
		}
	}
}

func TestRawCaptureNeverInDecodeHelpers(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			c, _ := newScriptClient(t, testutil.Step{Body: rawExoticBody(o, 0)})
			live := o.call(t.Context(), c, serper.WithRawCapture(true))
			require.NoError(t, live.err)
			require.True(t, live.rawOK)

			got := o.decode(live.raw)
			require.NoError(t, got.err, "a captured body decodes offline")
			assertCapture(t, "", got.raw, got.rawOK, "a Decode result never holds a capture")
			assert.Equal(t, serper.Metadata{Op: o.op, Credits: reported(1)}, got.meta)

			for _, refused := range []string{`{"credits":1}`, `[]`, rawExoticBody(rawRefusing(o), 0), `{`} {
				got = o.decode([]byte(refused))
				ce := callError(t, got.err)
				raw, ok := ce.RawJSON()
				assertCapture(t, "", raw, ok, "a Decode failure never holds a capture")
				assert.Equal(t, serper.RawCaptureOff, ce.Meta.RawCapture)
				assert.False(t, ce.OutcomeUnknown)
			}
		})
	}
}

// rawWithMarker returns body, a JSON object, with rawMarker planted in members
// the SDK does not type, as a member name and as values.
func rawWithMarker(body string) string {
	return body[:len(body)-1] + `,"answerBox":{"snippet":"` + rawMarker + `"},"` + rawMarker + `":["` +
		rawMarker + `",1e400]}`
}

func TestRawCaptureNeverPrinted(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			refused := rawWithMarker(`{"credits":1}`)
			c, _ := newScriptClient(t, testutil.Step{Body: rawWithMarker(o.ok)}, testutil.Step{Body: refused})

			got := o.call(t.Context(), c, serper.WithRawCapture(true))
			require.NoError(t, got.err)
			require.Contains(t, string(got.raw), rawMarker, "the marker was captured")
			switch r := got.value.(type) {
			case *serper.SearchResponse:
				rawAssertClean(t, r)
			case *serper.ImagesResponse:
				rawAssertClean(t, r)
			case *serper.ScrapeResponse:
				rawAssertClean(t, r)
			default:
				require.Failf(t, "not a response", "%T", got.value)
			}
			rawAssertClean(t, &got.meta)

			got = o.call(t.Context(), c, serper.WithRawCapture(true))
			ce := assertCallError(t, got.err, o.want(rawMissing(o, reported(1))), rawMarker)
			raw, ok := ce.RawJSON()
			require.True(t, ok)
			require.Contains(t, string(raw), rawMarker, "the marker was captured")
			rawAssertClean(t, ce)
			rawAssertClean(t, &ce.Meta)
		})
	}
}

// rawAssertClean asserts that rawMarker appears nowhere in p however a caller
// might hold, print, walk or encode it: every shape with every fmt verb but
// %p, a reflection walk, and json.Marshal.
func rawAssertClean[T any](tb testing.TB, p *T) {
	tb.Helper()
	secrets := []string{rawMarker}
	for _, sh := range testutil.Shapes(p) {
		for _, verb := range verbsButP() {
			testutil.AssertRedacted(tb, fmt.Sprintf(verb, sh.Value), secrets, "%T %s %s", p, sh.Name, verb)
		}
	}
	testutil.AssertRedacted(tb, strings.Join(testutil.ReflectStrings(p), "\n"), secrets, "%T reflection walk", p)
	data, err := json.Marshal(p)
	require.NoError(tb, err)
	testutil.AssertRedacted(tb, string(data), secrets, "%T JSON", p)
}
