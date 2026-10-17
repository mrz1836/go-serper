package serper_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// Synthetic test values. They double as leak markers: no error, formatted
// value, metadata or reflection walk may ever contain them. They are not real
// secrets.
const (
	testKey    = "test-keymarker-7f3a2c91" // gitleaks:allow (synthetic test marker)
	testQuery  = "querymarker 5d0c7e19 lighthouse"
	testTarget = "https://www.example.com/post?id=7&token=targetmarker0b6e2f"
)

// In-memory test origins: loopback addresses that an in-memory transport
// never dials, so a test that loses its fake still cannot reach the network.
const (
	searchOrigin = "https://127.0.0.1:8443"
	scrapeOrigin = "https://127.0.0.1:9443"
)

// The path of each operation.
const (
	pathSearch = "/search"
	pathImages = "/images"
	pathScrape = "/"
)

// mediaJSON is the media type the SDK sends and accepts.
const mediaJSON = "application/json"

// controlPayload is the body of the control requests, which do not go
// through the SDK.
const controlPayload = `{"q":"example"}`

// Smallest successful answers, synthetic SDK fixtures.
const (
	searchOK = `{"searchParameters":{"q":"example","type":"search","engine":"google"},"organic":[]}`
	imagesOK = `{"searchParameters":{"q":"example","type":"images","engine":"google"},"images":[]}`
	scrapeOK = `{"text":"Example page text."}`
)

// Validation reasons, as ValidationError.Reason reports them.
const (
	reasonRequired          = "required"
	reasonNil               = "nil"
	reasonNotInitialized    = "not_initialized"
	reasonOutOfRange        = "out_of_range"
	reasonInvalidFormat     = "invalid_format"
	reasonInvalidUTF8       = "invalid_utf8"
	reasonInvalidURL        = "invalid_url"
	reasonUnsupportedScheme = "unsupported_scheme"
	reasonMissingHost       = "missing_host"
	reasonHasUserinfo       = "has_userinfo"
	reasonHasQuery          = "has_query"
	reasonHasFragment       = "has_fragment"
	reasonHasPath           = "has_path"
	reasonInvalidHost       = "invalid_host"
	reasonInvalidPort       = "invalid_port"
	reasonTooLarge          = "too_large"
)

// errUnrelated is an error no CallError may ever match.
var errUnrelated = errors.New("unrelated test error")

// outcome is the result of one operation call, reduced to what every suite
// checks: the error, and for a success the metadata and the capture.
type outcome struct {
	value any
	meta  serper.Metadata
	raw   []byte
	rawOK bool
	err   error
}

// opCase is one public operation of Client, called with a minimal valid
// request. Every suite that runs all the operations shares it; a suite that
// needs more, such as a marker payload or an exact wire request, wraps it.
type opCase struct {
	name   string           // the method's name
	op     serper.Operation // the operation it calls
	path   string           // its path
	scrape bool             // whether it goes to the scrape origin
	ok     string           // its smallest successful answer
	call   func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome
	decode func(data []byte) outcome // its Decode helper
}

// origin returns the in-memory origin o goes to.
func (o opCase) origin() string {
	if o.scrape {
		return scrapeOrigin
	}
	return searchOrigin
}

// want completes w, the CallError of a failed in-memory call of o, with o's
// Op, Origin and Path.
func (o opCase) want(w wantCall) wantCall { return o.wantAt(o.origin(), w) }

// wantAt completes w, the CallError of a failed call of o to origin, with o's
// Op and Path and origin as its Origin.
func (o opCase) wantAt(origin string, w wantCall) wantCall {
	w.Op, w.Origin, w.Path = o.op, origin, o.path
	return w
}

// opCases returns the operation cases, in the order of the documentation.
func opCases() []opCase {
	return []opCase{
		{
			name: "Search", op: serper.OpSearch, path: pathSearch, ok: searchOK,
			call: func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
				return outcomeOf(c.Search(ctx, serper.SearchRequest{Query: testQuery}, opts...))
			},
			decode: func(data []byte) outcome { return outcomeOf(serper.DecodeSearchResponse(data)) },
		},
		{
			name: "Images", op: serper.OpImages, path: pathImages, ok: imagesOK,
			call: func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
				return outcomeOf(c.Images(ctx, serper.ImagesRequest{Query: testQuery}, opts...))
			},
			decode: func(data []byte) outcome { return outcomeOf(serper.DecodeImagesResponse(data)) },
		},
		{
			name: "Scrape", op: serper.OpScrape, path: pathScrape, scrape: true, ok: scrapeOK,
			call: func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
				return outcomeOf(c.Scrape(ctx, serper.ScrapeRequest{URL: testTarget}, opts...))
			},
			decode: func(data []byte) outcome { return outcomeOf(serper.DecodeScrapeResponse(data)) },
		},
	}
}

// opCaseNamed returns the case of the method called name. An unknown name is
// a bug in the test, so it panics with the name.
func opCaseNamed(name string) opCase {
	for _, o := range opCases() {
		if o.name == name {
			return o
		}
	}
	panic("no operation case named " + strconv.Quote(name))
}

// response is what every response type has.
type response interface {
	RawJSON() ([]byte, bool)
}

// outcomeOf reduces a method's results to an outcome. A method that fails
// must return a nil response; one that succeeds, a non-nil one.
func outcomeOf[T any, P interface {
	*T
	response
}](v P, err error,
) outcome {
	if err != nil {
		if v != nil {
			return outcome{err: errors.Join(err, errBrokenCall)}
		}
		return outcome{err: err}
	}
	if v == nil {
		return outcome{err: errBrokenCall}
	}
	raw, ok := v.RawJSON()
	return outcome{value: v, meta: metaOf(v), raw: raw, rawOK: ok}
}

// errBrokenCall reports a method that broke its own contract, such as
// returning a value alongside an error.
var errBrokenCall = errors.New("the call broke its contract")

// valueOf returns the response of got, a successful outcome, as a *T.
func valueOf[T any](tb testing.TB, got outcome) *T {
	tb.Helper()
	require.NoError(tb, got.err)
	res, ok := got.value.(*T)
	require.True(tb, ok, "want a %T, got %T", res, got.value)
	return res
}

// directCall calls o's method on c with its minimal request, without
// copying out a capture as opCase.call does, and returns the response, as a
// response, or the error.
func directCall(ctx context.Context, c *serper.Client, o opCase, opts ...serper.CallOption) (response, error) {
	switch o.op {
	case serper.OpSearch:
		return directResult(c.Search(ctx, serper.SearchRequest{Query: testQuery}, opts...))
	case serper.OpImages:
		return directResult(c.Images(ctx, serper.ImagesRequest{Query: testQuery}, opts...))
	case serper.OpScrape:
		return directResult(c.Scrape(ctx, serper.ScrapeRequest{URL: testTarget}, opts...))
	}
	panic("no method for operation " + strconv.Quote(string(o.op)))
}

// directResult returns a method's results with the response as a response,
// nil when the call failed.
func directResult[P response](r P, err error) (response, error) {
	if err != nil {
		return nil, err
	}
	return r, nil
}

// invalidCall calls o with a request that fails validation (an empty query
// or target) and returns the error.
func invalidCall(ctx context.Context, c *serper.Client, o opCase, opts ...serper.CallOption) error {
	var err error
	switch o.op {
	case serper.OpSearch:
		_, err = c.Search(ctx, serper.SearchRequest{}, opts...)
	case serper.OpImages:
		_, err = c.Images(ctx, serper.ImagesRequest{}, opts...)
	case serper.OpScrape:
		_, err = c.Scrape(ctx, serper.ScrapeRequest{}, opts...)
	default:
		panic("no method for operation " + strconv.Quote(string(o.op)))
	}
	return err
}

// metaOf returns the Meta of a response.
func metaOf(v any) serper.Metadata {
	switch r := v.(type) {
	case *serper.SearchResponse:
		return r.Meta
	case *serper.ImagesResponse:
		return r.Meta
	case *serper.ScrapeResponse:
		return r.Meta
	}
	panic(fmt.Sprintf("not a response: %T", v))
}

// newKeyedClient returns a client built with key and opts, failing tb when
// NewClient fails.
func newKeyedClient(tb testing.TB, key string, opts ...serper.Option) *serper.Client {
	tb.Helper()
	c, err := serper.NewClient(key, opts...)
	require.NoError(tb, err)
	return c
}

// newClient is newKeyedClient with the test key.
func newClient(tb testing.TB, opts ...serper.Option) *serper.Client {
	tb.Helper()
	return newKeyedClient(tb, testKey, opts...)
}

// newHTTPClientClient returns a client built with key whose calls go through
// hc to the loopback test origins, with opts applied after those options.
func newHTTPClientClient(tb testing.TB, key string, hc *http.Client, opts ...serper.Option) *serper.Client {
	tb.Helper()
	all := append([]serper.Option{
		serper.WithHTTPClient(hc),
		serper.WithSearchBaseURL(searchOrigin),
		serper.WithScrapeBaseURL(scrapeOrigin),
	}, opts...)
	return newKeyedClient(tb, key, all...)
}

// newTransportClient returns a client built with the test key whose calls go
// through rt, in memory, to the loopback test origins, with opts applied
// after the transport options.
func newTransportClient(tb testing.TB, rt http.RoundTripper, opts ...serper.Option) *serper.Client {
	tb.Helper()
	return newHTTPClientClient(tb, testKey, &http.Client{Transport: rt}, opts...)
}

// newScriptClient returns a client whose calls go, in memory, through a new
// Script answering with steps.
func newScriptClient(tb testing.TB, steps ...testutil.Step) (*serper.Client, *testutil.Script) {
	tb.Helper()
	s := testutil.NewScript(steps...)
	return newTransportClient(tb, s), s
}

// offlineTransport is a transport that fails tb if anything uses it.
func offlineTransport(tb testing.TB) testutil.RoundTripFunc {
	tb.Helper()
	return func(r *http.Request) (*http.Response, error) {
		assert.Fail(tb, "unexpected round trip to "+r.URL.Path+": this call must send nothing")
		return nil, errUnrelated
	}
}

// doneContext is a way to make a context that is already done before the
// call, and the failure kind a call with it gives.
type doneContext struct {
	name string
	kind serper.FailureKind
	ctx  func(parent context.Context) (context.Context, context.CancelFunc)
}

// doneContexts returns contexts already canceled and already expired, each
// with no cause and with a cause that quotes privacy markers, which no error
// may show.
func doneContexts() []doneContext {
	cause := prvTextError(prvMarkCause + " " + prvKey)
	past := func() time.Time { return time.Now().Add(-time.Minute) }
	return []doneContext{
		{name: "canceled", kind: serper.FailureCanceled, ctx: func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			return ctx, cancel
		}},
		{name: "canceled with a cause", kind: serper.FailureCanceled, ctx: func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancelCause(parent)
			cancel(cause)
			return ctx, func() { cancel(nil) }
		}},
		{name: "expired", kind: serper.FailureDeadlineExceeded, ctx: func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(parent, past())
		}},
		{name: "expired with a cause", kind: serper.FailureDeadlineExceeded, ctx: func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadlineCause(parent, past(), cause)
		}},
	}
}

// invalidRequest is a call that fails validation, and what it reports.
type invalidRequest struct {
	name   string
	op     serper.Operation
	field  string
	reason string
	call   func(ctx context.Context, c *serper.Client) error
}

// invalidRequests returns a call that fails validation for every field of
// every operation and every reason. The valid fields hold privacy markers, and
// so do the refused values where they can, so that the privacy suite checks
// the same rows. A request over the limit is over the default one, and so over
// any lower limit too.
func invalidRequests() []invalidRequest {
	var calls []invalidRequest
	for _, op := range []serper.Operation{serper.OpSearch, serper.OpImages} {
		add := func(name, field, reason string, edit func(*serper.SearchRequest)) {
			req := prvSearchRequest()
			edit(&req)
			calls = append(calls, invalidRequest{
				name: string(op) + " " + name, op: op, field: field, reason: reason, call: queryCall(op, req),
			})
		}
		add("empty query", "Query", reasonRequired, func(r *serper.SearchRequest) { r.Query = "" })
		add("invalid Query", "Query", reasonInvalidUTF8, func(r *serper.SearchRequest) { r.Query += "\xff" })
		add("invalid Location", "Location", reasonInvalidUTF8, func(r *serper.SearchRequest) { r.Location += "\xff" })
		add("invalid GL", "GL", reasonInvalidUTF8, func(r *serper.SearchRequest) { r.GL += "\xff" })
		add("invalid HL", "HL", reasonInvalidUTF8, func(r *serper.SearchRequest) { r.HL += "\xff" })
		add("invalid TBS", "TBS", reasonInvalidUTF8, func(r *serper.SearchRequest) { r.TBS += "\xff" })
		add("negative num", "Num", reasonOutOfRange, func(r *serper.SearchRequest) { r.Num = -1 })
		add("negative page", "Page", reasonOutOfRange, func(r *serper.SearchRequest) { r.Page = -3 })
		add("too large", "request", reasonTooLarge, func(r *serper.SearchRequest) {
			r.Query += strings.Repeat("q", serper.DefaultMaxRequestBytes)
		})
	}
	const path = "prvtargetpathmarker"
	for _, tt := range []struct{ name, url, reason string }{
		{name: "empty", url: "", reason: reasonRequired},
		{name: "invalid UTF-8", url: prvTarget + "\xff", reason: reasonInvalidUTF8},
		{name: "ftp", url: "ftp://www.example.com/" + path, reason: reasonUnsupportedScheme},
		{name: "file", url: "file:///" + path, reason: reasonUnsupportedScheme},
		{name: "no scheme", url: "www.example.com/" + path, reason: reasonUnsupportedScheme},
		{name: "relative", url: "/" + path + "?prvtargetquerymarker=1", reason: reasonUnsupportedScheme},
		{name: "userinfo", url: "https://" + prvMarkUser + "@www.example.com/" + path, reason: reasonHasUserinfo},
		{name: "userinfo with the key", url: "https://" + prvMarkUser + ":" + prvKey + "@www.example.com/", reason: reasonHasUserinfo},
		{name: "no host", url: "https:///" + path, reason: reasonMissingHost},
		{name: "port only", url: "http://:80/" + path, reason: reasonMissingHost},
		{name: "bad escape", url: "https://www.example.com/%zz" + path, reason: reasonInvalidURL},
		{name: "bad port", url: "https://www.example.com:" + path + "/", reason: reasonInvalidURL},
		{name: "space in the host", url: "https://www.example .com/" + path, reason: reasonInvalidURL},
		{name: "open bracket", url: "https://[" + path + "/", reason: reasonInvalidURL},
		{name: "opaque", url: "https:www.example.com/" + path, reason: reasonInvalidURL},
		{name: "opaque mailto", url: "mailto:" + path + "@example.com", reason: reasonInvalidURL},
		{name: "javascript", url: "javascript:alert('" + path + "')", reason: reasonInvalidURL},
		{name: "control character", url: "https://www.example.com/" + path + "\n", reason: reasonInvalidURL},
		{name: "too large", url: prvTarget + strings.Repeat("a", serper.DefaultMaxRequestBytes), reason: reasonTooLarge},
	} {
		field := "URL"
		if tt.reason == reasonTooLarge {
			field = "request"
		}
		calls = append(calls, invalidRequest{
			name: "scrape " + tt.name, op: serper.OpScrape, field: field, reason: tt.reason, call: scrapeCall(tt.url),
		})
	}
	return calls
}

// queryCall returns a call of op, Search or Images, with req.
func queryCall(op serper.Operation, req serper.SearchRequest) func(context.Context, *serper.Client) error {
	if op == serper.OpImages {
		return func(ctx context.Context, c *serper.Client) error {
			_, err := c.Images(ctx, serper.ImagesRequest(req))
			return err
		}
	}
	return func(ctx context.Context, c *serper.Client) error {
		_, err := c.Search(ctx, req)
		return err
	}
}

// scrapeCall returns a call of Scrape for target, with Markdown.
func scrapeCall(target string) func(context.Context, *serper.Client) error {
	return func(ctx context.Context, c *serper.Client) error {
		_, err := c.Scrape(ctx, serper.ScrapeRequest{URL: target, IncludeMarkdown: true})
		return err
	}
}

// newOfflineClient returns a client whose transport fails tb if it is ever
// called: for tests of calls that must send nothing.
func newOfflineClient(tb testing.TB, opts ...serper.Option) *serper.Client {
	tb.Helper()
	return newTransportClient(tb, offlineTransport(tb), opts...)
}

// servers is a pair of recording TLS servers playing the two origins.
type servers struct {
	search *testutil.Server
	scrape *testutil.Server
}

// of returns the server that o goes to.
func (s servers) of(o opCase) *testutil.Server {
	if o.scrape {
		return s.scrape
	}
	return s.search
}

// other returns the server that o does not go to.
func (s servers) other(o opCase) *testutil.Server {
	if o.scrape {
		return s.search
	}
	return s.scrape
}

// newServerClient starts two recording HTTP/1.1 TLS servers, one per origin,
// running searchH and scrapeH, and returns a client pointed at them.
// httptest servers share one certificate, so the search server's client
// trusts both.
func newServerClient(tb testing.TB, searchH, scrapeH http.HandlerFunc, opts ...serper.Option,
) (*serper.Client, servers) {
	tb.Helper()
	return newServerClientProto(tb, testutil.HTTP1, searchH, scrapeH, opts...)
}

// newServerClientProto is newServerClient for servers speaking proto.
func newServerClientProto(tb testing.TB, proto testutil.Protocol, searchH, scrapeH http.HandlerFunc,
	opts ...serper.Option,
) (*serper.Client, servers) {
	tb.Helper()
	s := servers{
		search: testutil.NewServerProto(tb, proto, searchH),
		scrape: testutil.NewServerProto(tb, proto, scrapeH),
	}
	all := append([]serper.Option{
		serper.WithHTTPClient(s.search.Client()),
		serper.WithSearchBaseURL(s.search.URL()),
		serper.WithScrapeBaseURL(s.scrape.URL()),
	}, opts...)
	return newClient(tb, all...), s
}

// okBody returns the smallest successful answer of the operation at path.
func okBody(path string) string {
	switch path {
	case pathSearch:
		return searchOK
	case pathImages:
		return imagesOK
	}
	return scrapeOK
}

// okSearchHandler answers each search path with its smallest successful
// answer, and anything else with HTTP 404.
func okSearchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != pathSearch && r.URL.Path != pathImages) {
			http.NotFound(w, r)
			return
		}
		testutil.Serve(testutil.Step{Body: okBody(r.URL.Path)})(w, r)
	}
}

// okScrapeHandler answers POST / with the smallest successful scrape, and
// anything else with HTTP 404.
func okScrapeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != pathScrape {
			http.NotFound(w, r)
			return
		}
		testutil.Serve(testutil.Step{Body: scrapeOK})(w, r)
	}
}

// callError returns the *serper.CallError in err's chain, failing tb when
// there is none.
func callError(tb testing.TB, err error) *serper.CallError {
	tb.Helper()
	return testutil.RequireErrorAs[*serper.CallError](tb, err)
}

// validationError returns the *serper.ValidationError in err's chain,
// failing tb when there is none.
func validationError(tb testing.TB, err error) *serper.ValidationError {
	tb.Helper()
	return testutil.RequireErrorAs[*serper.ValidationError](tb, err)
}

// assertValidation asserts that err is the *ValidationError for op, field and
// reason that matches sentinel, ErrInvalidConfig or ErrInvalidRequest, and not
// the other one, with the fixed error text, which never quotes a value. It is
// never a CallError.
func assertValidation(tb testing.TB, err, sentinel error, op serper.Operation, field, reason string) {
	tb.Helper()
	require.ErrorIs(tb, err, sentinel)
	ve := validationError(tb, err)
	assert.Equal(tb, serper.ValidationError{Err: sentinel, Op: op, Field: field, Reason: reason}, *ve)
	for _, other := range []error{serper.ErrInvalidConfig, serper.ErrInvalidRequest} {
		assert.Equal(tb, errors.Is(other, sentinel), errors.Is(err, other), "matches %v", other)
	}
	var want strings.Builder
	want.WriteString(sentinel.Error())
	for _, part := range []string{string(op), field, reason} {
		if part != "" {
			want.WriteString(": ")
			want.WriteString(part)
		}
	}
	assert.Equal(tb, want.String(), err.Error())
	assert.NotErrorAs(tb, err, new(*serper.CallError), "a validation failure is never a CallError")
	assertErrorClean(tb, err, leakMarkers()...)
}

// wantCall is the *CallError a test expects. Elapsed is checked only for
// being positive exactly when Attempts is 1.
type wantCall struct {
	Kind           serper.FailureKind
	Field          string
	OutcomeUnknown bool
	Op             serper.Operation
	Origin         string
	Path           string
	Status         int
	Attempts       int
	Credits        serper.Credits
	RawCapture     serper.RawCaptureState
	Class          string // the transport class in the Error text, "" for none
}

// assertCallError asserts that err is the *CallError want describes, field
// for field. The Error text the caller sees must be the documented one, err
// must match exactly the sentinels of its kind (and ErrOutcomeUnknown exactly
// when OutcomeUnknown is set), its cause must be a fixed one, and none of the
// leak markers or secrets may appear however err is printed or walked. It
// returns the CallError, for tests that need more of it.
func assertCallError(tb testing.TB, err error, want wantCall, secrets ...string) *serper.CallError {
	tb.Helper()
	ce := callError(tb, err)
	m := ce.Meta
	assert.Equal(tb, want, wantCall{
		Kind: ce.Kind, Field: ce.Field, OutcomeUnknown: ce.OutcomeUnknown,
		Op: m.Op, Origin: m.Origin, Path: m.Path, Status: m.StatusCode, Attempts: m.Attempts,
		Credits: m.Credits, RawCapture: m.RawCapture, Class: want.Class,
	})
	assert.Equal(tb, m.Attempts == 1, m.Elapsed > 0, "Elapsed is positive exactly when a request was made")
	assert.Equal(tb, errorText(want), err.Error(), "the error the caller sees")
	assertOnlySentinel(tb, err, want.Kind, want.OutcomeUnknown)
	assertFixedCause(tb, err)
	assertErrorClean(tb, err, append(leakMarkers(), secrets...)...)
	return ce
}

// errorText renders the documented Error grammar of a CallError,
// "serper: <op>: <kind>[: <class>][ (HTTP <n>)][: <field>]", independently
// of the library.
func errorText(w wantCall) string {
	s := "serper: " + string(w.Op) + ": " + string(w.Kind)
	if w.Class != "" {
		s += ": " + w.Class
	}
	if w.Status != 0 {
		s += " (HTTP " + strconv.Itoa(w.Status) + ")"
	}
	if w.Field != "" {
		s += ": " + w.Field
	}
	return s
}

// allSentinels returns every error a CallError could be asked about with
// errors.Is: the package's sentinels, both context errors, and an unrelated
// error.
func allSentinels() []error {
	return []error{
		serper.ErrInvalidConfig, serper.ErrInvalidRequest, serper.ErrUnauthorized, serper.ErrForbidden,
		serper.ErrRateLimited, serper.ErrRejected, serper.ErrUpstream, serper.ErrUnexpectedResponse,
		serper.ErrMalformedResponse, serper.ErrResponseTooLarge, serper.ErrTransport, serper.ErrTimeout,
		serper.ErrOutcomeUnknown, context.Canceled, context.DeadlineExceeded, errUnrelated,
	}
}

// kindSentinels is the documented mapping from each failure kind to the
// sentinel, or context error, that it matches with errors.Is, written out
// independently of the library's. FailureTimeout also matches
// context.DeadlineExceeded.
func kindSentinels() map[serper.FailureKind]error {
	return map[serper.FailureKind]error{
		serper.FailureUnauthorized:       serper.ErrUnauthorized,
		serper.FailureForbidden:          serper.ErrForbidden,
		serper.FailureRateLimited:        serper.ErrRateLimited,
		serper.FailureRejected:           serper.ErrRejected,
		serper.FailureUpstream:           serper.ErrUpstream,
		serper.FailureUnexpectedResponse: serper.ErrUnexpectedResponse,
		serper.FailureMalformedResponse:  serper.ErrMalformedResponse,
		serper.FailureResponseTooLarge:   serper.ErrResponseTooLarge,
		serper.FailureTransport:          serper.ErrTransport,
		serper.FailureTimeout:            serper.ErrTimeout,
		serper.FailureCanceled:           context.Canceled,
		serper.FailureDeadlineExceeded:   context.DeadlineExceeded,
	}
}

// wantMatches returns the targets a CallError of kind must match with
// errors.Is: its own sentinel, plus context.DeadlineExceeded for a timeout,
// plus ErrOutcomeUnknown when the outcome is unknown.
func wantMatches(kind serper.FailureKind, outcomeUnknown bool) []error {
	want := []error{kindSentinels()[kind]}
	if kind == serper.FailureTimeout {
		want = append(want, context.DeadlineExceeded)
	}
	if outcomeUnknown {
		want = append(want, serper.ErrOutcomeUnknown)
	}
	return want
}

// assertOnlySentinel asserts that err, a CallError of kind or anything
// wrapping one, matches exactly the targets documented for kind and no other
// sentinel or context error.
func assertOnlySentinel(tb testing.TB, err error, kind serper.FailureKind, outcomeUnknown bool) {
	tb.Helper()
	want := wantMatches(kind, outcomeUnknown)
	for _, target := range allSentinels() {
		assert.Equal(tb, slices.Contains(want, target), errors.Is(err, target), "kind %s vs %v", kind, target)
	}
}

// assertFixedCause asserts that the cause of err's *CallError is a context
// error or a fixed error of the package that wraps nothing: never a reader's
// or transport's own error.
func assertFixedCause(tb testing.TB, err error) {
	tb.Helper()
	cause := callError(tb, err).Unwrap()
	require.Error(tb, cause, "a CallError the SDK returns keeps a cause")
	if slices.Contains([]error{context.Canceled, context.DeadlineExceeded}, cause) {
		return
	}
	assert.True(tb, strings.HasPrefix(cause.Error(), "serper: "), "cause %q is a fixed package error", cause)
	require.NoError(tb, errors.Unwrap(cause), "a fixed cause wraps nothing")
}

// assertErrorClean fails tb when a secret appears however a caller might print
// or walk err: any fmt verb on each error of its Unwrap chain, or on err
// wrapped or joined, or any string reflection reaches. The chain must also hold
// no *url.Error, whose text quotes the URL.
func assertErrorClean(tb testing.TB, err error, secrets ...string) {
	tb.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		testutil.AssertNeverPrints(tb, e, secrets...)
	}
	testutil.AssertNeverPrints(tb, fmt.Errorf("wrapped: %w", err), secrets...)
	testutil.AssertNeverPrints(tb, errors.Join(err, context.Canceled), secrets...)
	testutil.AssertRedacted(tb, strings.Join(testutil.ReflectStrings(err), "\n"), secrets, "reflection walk")
	assert.NotErrorAs(tb, err, new(*url.Error), "the chain must never hold a *url.Error")
}

// leakMarkers returns every synthetic secret and request value the tests
// plant: none may ever appear in an error or in metadata.
func leakMarkers() []string {
	return []string{testKey, testQuery, "querymarker", testTarget, "targetmarker0b6e2f"}
}

// statusCodes are the statuses every operation is checked against, besides
// 200: every 3xx, and others of each class.
func statusCodes() []int {
	return []int{
		100, 101, 103, 201, 202, 204, 206, 299, 300, 301, 302, 303, 304, 305, 306, 307, 308, 399,
		400, 401, 402, 403, 404, 405, 407, 409, 413, 422, 429, 451, 499,
		500, 501, 502, 503, 504, 599, 600, 999,
	}
}

// statusKind is the documented failure kind of an HTTP status other than 200,
// written out independently of the library.
func statusKind(code int) serper.FailureKind {
	switch {
	case code == 401:
		return serper.FailureUnauthorized
	case code == 403:
		return serper.FailureForbidden
	case code == 429:
		return serper.FailureRateLimited
	case code >= 400 && code <= 499:
		return serper.FailureRejected
	case code >= 500 && code <= 599:
		return serper.FailureUpstream
	default:
		return serper.FailureUnexpectedResponse
	}
}

// statusOutcomeUnknown is the documented OutcomeUnknown of an HTTP status
// other than 200, written out independently of the library: unknown for a
// 5xx, a 2xx, a 1xx, a 303 and any status outside 100 to 599; known for any
// other 3xx and a 4xx.
func statusOutcomeUnknown(code int) bool {
	return code < 300 || code > 499 || code == 303
}

// reported returns the Credits of a reported value.
func reported(n int64) serper.Credits {
	return serper.Credits{Status: serper.CreditsReported, Used: &n}
}

// invalidCredits returns the Credits of an invalid value.
func invalidCredits() serper.Credits {
	return serper.Credits{Status: serper.CreditsInvalid}
}

// captureState returns state when capture is on, and RawCaptureOff otherwise.
func captureState(on bool, state serper.RawCaptureState) serper.RawCaptureState {
	if !on {
		return serper.RawCaptureOff
	}
	return state
}

// capturedBody returns body when capture is on, and "" otherwise: the capture
// that assertCapture expects.
func capturedBody(on bool, body string) string {
	if !on {
		return ""
	}
	return body
}

// captureMode is one way of asking, or not asking, for a capture.
type captureMode struct {
	name   string
	client []serper.Option
	call   []serper.CallOption
	on     bool
}

// captureModes returns capture off, on for the call, on by the Client's
// default, and the default turned off for the call.
func captureModes() []captureMode {
	def := []serper.Option{serper.WithDefaultRawCapture()}
	return []captureMode{
		{name: "off"},
		{name: "call on", call: []serper.CallOption{serper.WithRawCapture(true)}, on: true},
		{name: "default on", client: def, on: true},
		{name: "default turned off", client: def, call: []serper.CallOption{serper.WithRawCapture(false)}},
	}
}

// assertCapture asserts that raw and ok, what a RawJSON method returned, are
// the capture want, byte for byte, or (nil, false) when want is "": a capture
// is never empty. A mismatch reports the lengths and the first differing
// byte, not the bodies, which can be large. msgAndArgs annotate a failure. It
// reports whether the assertions passed.
func assertCapture(tb testing.TB, want string, raw []byte, ok bool, msgAndArgs ...any) bool {
	tb.Helper()
	if want == "" {
		return assert.False(tb, ok, msgAndArgs...) && assert.Nil(tb, raw, msgAndArgs...)
	}
	if !assert.True(tb, ok, msgAndArgs...) {
		return false
	}
	if string(raw) == want {
		return true
	}
	at := 0
	for at < len(want) && at < len(raw) && want[at] == raw[at] {
		at++
	}
	return assert.Failf(tb, "the capture is not the body", "want %d bytes, got %d; first difference at byte %d",
		len(want), len(raw), at)
}

// fixture returns the SDK fixture name, under testdata/synthetic.
func fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	return testutil.Fixture(tb, "synthetic/"+name)
}

// assertBody asserts that got is exactly the JSON text want, byte for byte:
// member order and escaping are part of the contract, so a semantic JSON
// comparison would be too lenient.
func assertBody(tb testing.TB, want string, got []byte) {
	tb.Helper()
	assert.Equal(tb, []byte(want), got, "JSON text\n got: %s\nwant: %s", got, want)
}

// jsonOf returns v marshaled with encoding/json, as a logger would write it.
// The JSON of a response leaves out its Meta and its capture.
func jsonOf(tb testing.TB, v any) string {
	tb.Helper()
	data, err := json.Marshal(v)
	require.NoError(tb, err)
	return string(data)
}

// assertRoundTrip marshals v, checks the JSON is exactly want, unmarshals it
// into a new T, and checks the copy equals v. It returns the copy.
func assertRoundTrip[T any](tb testing.TB, v *T, want string) *T {
	tb.Helper()
	data, err := json.Marshal(v)
	require.NoError(tb, err)
	assertBody(tb, want, data)
	back := new(T)
	require.NoError(tb, json.Unmarshal(data, back))
	assert.Equal(tb, v, back, "round trip changed the value")
	return back
}

// bannedHeaders are the request headers the SDK must never send: other
// credentials, cookies, and the idempotency keys with which the standard
// library may replay a request.
func bannedHeaders() []string {
	return []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Idempotency-Key", "X-Idempotency-Key",
		"Range", "Expect", "Referer",
	}
}

// assertWireRequest asserts the wire policy every operation shares, on a
// request recorded by a server or a transport: POST to path, the SDK's fixed
// headers with the key in X-API-KEY, none of the banned headers, an exact
// Content-Length, and the key nowhere in the URL or the body.
func assertWireRequest(tb testing.TB, r testutil.Request, path string) {
	tb.Helper()
	assert.Equal(tb, http.MethodPost, r.Method)
	assert.Equal(tb, path, r.Path)
	assert.Equal(tb, []string{testKey}, r.Header.Values("X-Api-Key"))
	assert.Equal(tb, []string{mediaJSON}, r.Header.Values("Content-Type"))
	assert.Equal(tb, []string{mediaJSON}, r.Header.Values("Accept"))
	assert.Equal(tb, []string{serper.DefaultUserAgent}, r.Header.Values("User-Agent"))
	for _, h := range bannedHeaders() {
		assert.Empty(tb, r.Header.Values(h), "header %s must never be sent", h)
	}
	assert.Equal(tb, int64(len(r.Body)), r.ContentLength, "an exact Content-Length")
	assert.NotContains(tb, r.URL, testKey, "the key is never in the URL")
	assert.NotContains(tb, string(r.Body), testKey, "the key is never in the body")
	assert.NotContains(tb, r.URL, "?", "the URL never has a query")
}

// assertNoReplay asserts, on a request recorded by a transport (a Script or a
// CountingTransport's Next, never a server), that the standard library could
// not resend it: its body is neither nil nor http.NoBody and GetBody is nil.
func assertNoReplay(tb testing.TB, r testutil.Request) {
	tb.Helper()
	assert.False(tb, r.GetBody, "GetBody must be nil, so nothing can be resent")
	assert.False(tb, r.NoBody, "the body is never http.NoBody")
	assert.NotEmpty(tb, r.Body, "the body is the JSON request")
}

// verbsButP returns every verb of testutil.FormatVerbs but %p, which fmt
// applies to a value without calling its methods.
func verbsButP() []string {
	return slices.DeleteFunc(testutil.FormatVerbs(), func(v string) bool { return v == "%p" })
}

// assertPrintsShapeOnly fails tb when formatting v, a request or a value
// holding requests, with any verb but %p prints a secret: requests print
// their shape only. %p on a value bypasses Format, a documented limit.
func assertPrintsShapeOnly(tb testing.TB, v any, secrets ...string) {
	tb.Helper()
	for _, verb := range verbsButP() {
		testutil.AssertRedacted(tb, fmt.Sprintf(verb, v), secrets, "verb %s on %T", verb, v)
	}
}

// padded returns body, a JSON object, with spaces before its closing brace,
// so that it is exactly n bytes long; n must be at least len(body).
func padded(body string, n int) string {
	return body[:len(body)-1] + strings.Repeat(" ", n-len(body)) + "}"
}

// endingWith returns a body over content whose read after it fails with end.
func endingWith(content string, end error) *testutil.Body {
	b := testutil.NewBody(content)
	b.End = end
	return b
}

// mustURL parses raw, failing tb when it does not parse.
func mustURL(tb testing.TB, raw string) *url.URL {
	tb.Helper()
	u, err := url.Parse(raw)
	require.NoError(tb, err)
	return u
}

// originHost returns the host[:port] of an https origin.
func originHost(origin string) string { return strings.TrimPrefix(origin, "https://") }

// idleCounter is a transport that counts the CloseIdleConnections calls that
// reach it.
type idleCounter struct {
	http.RoundTripper

	closes atomic.Int32
}

// CloseIdleConnections counts the call.
func (c *idleCounter) CloseIdleConnections() { c.closes.Add(1) }
