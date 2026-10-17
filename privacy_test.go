package serper_test

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
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

// prvLocationURL is the redirect target planted in Location headers: a page
// under example.com, which a test server's client routes to the server
// itself, so that a followed redirect would show up there.
const prvLocationURL = "https://www.example.com/" + prvMarkLocation + "?key=" + prvKey

// prvGoAwayError mimics the HTTP/2 transport's GOAWAY error, whose text
// quotes the debug data the server sent.
type prvGoAwayError struct {
	LastStreamID uint32
	ErrCode      uint32
	DebugData    string
}

func (e prvGoAwayError) Error() string {
	return fmt.Sprintf("http2: server sent GOAWAY and closed the connection; LastStreamID=%v, ErrCode=%v, debug=%q",
		e.LastStreamID, e.ErrCode, e.DebugData)
}

// prvAddr is a net.Addr whose text is a marker.
type prvAddr string

func (a prvAddr) Network() string { return "tcp" }
func (a prvAddr) String() string  { return string(a) }

// prvHostLog records the Host of every request a handler receives.
type prvHostLog struct {
	mu    sync.Mutex
	hosts []string
}

// wrap returns h, recording the Host of each request first.
func (l *prvHostLog) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.hosts = append(l.hosts, r.Host)
		l.mu.Unlock()
		h(w, r)
	}
}

// list returns a copy of the recorded hosts.
func (l *prvHostLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.hosts)
}

// prvFill replaces every $ in s with marker.
func prvFill(s, marker string) string { return strings.ReplaceAll(s, "$", marker) }

// prvCases returns opCases calling each operation with the marker requests.
func prvCases() []opCase {
	cases := opCases()
	for i := range cases {
		cases[i].call = prvCallOf(cases[i].op)
	}
	return cases
}

// prvCallOf returns the call of op with its marker request.
func prvCallOf(op serper.Operation) func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
	switch op {
	case serper.OpSearch:
		return func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
			return outcomeOf(c.Search(ctx, prvSearchRequest(), opts...))
		}
	case serper.OpImages:
		return func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
			return outcomeOf(c.Images(ctx, serper.ImagesRequest(prvSearchRequest()), opts...))
		}
	case serper.OpScrape:
		return func(ctx context.Context, c *serper.Client, opts ...serper.CallOption) outcome {
			return outcomeOf(c.Scrape(ctx, prvScrapeRequest(), opts...))
		}
	}
	panic("no marker call for operation " + strconv.Quote(string(op)))
}

// prvAssertCall asserts that err is the CallError want describes, as
// assertCallError does with every privacy marker as a secret, then runs
// prvAssertExtras. It returns the CallError.
func prvAssertCall(tb testing.TB, err error, want wantCall) *serper.CallError {
	tb.Helper()
	ce := assertCallError(tb, err, want, prvSecrets()...)
	prvAssertExtras(tb, err)
	return ce
}

// prvAssertValidationClean asserts that the validation error err never
// shows a marker or any of values, the rejected inputs.
func prvAssertValidationClean(tb testing.TB, err error, values ...string) {
	tb.Helper()
	assertErrorClean(tb, err, append(prvSecrets(), values...)...)
	prvAssertExtras(tb, err, values...)
}

// prvAssertExtras checks what assertErrorClean does not: the chain holds no
// standard-library or planted error and nothing but the SDK's own errors and
// context errors, and the JSON of err, and for a CallError its GoString, its
// Meta formatted with every verb and Meta's JSON, show no marker or any of
// values.
func prvAssertExtras(tb testing.TB, err error, values ...string) {
	tb.Helper()
	secrets := slices.Concat(leakMarkers(), prvSecrets(), values)
	for _, target := range []any{
		new(*url.Error), new(*net.OpError), new(*net.DNSError), new(*json.SyntaxError),
		new(*json.UnmarshalTypeError), new(tls.RecordHeaderError), new(*tls.CertificateVerificationError),
		new(*os.SyscallError), new(prvTextError), new(prvGoAwayError), new(testutil.TimeoutError),
	} {
		assert.NotErrorAs(tb, err, target, "the chain holds no %T", target)
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		own := strings.HasPrefix(e.Error(), "serper: ") ||
			slices.Contains([]error{context.Canceled, context.DeadlineExceeded}, e)
		assert.True(tb, own, "the chain holds only the SDK's errors and context errors, not a %T", e)
	}
	testutil.AssertRedacted(tb, jsonOf(tb, err), secrets, "JSON of the error")
	ce, ok := errors.AsType[*serper.CallError](err)
	if !ok {
		return
	}
	testutil.AssertRedacted(tb, ce.GoString(), secrets, "GoString")
	testutil.AssertNeverPrints(tb, ce.Meta, secrets...)
	testutil.AssertRedacted(tb, jsonOf(tb, ce.Meta), secrets, "JSON of Meta")
}

// prvShaped is a request type, which prints its shape.
type prvShaped interface {
	fmt.Stringer
	fmt.GoStringer
}

// prvAddressShapes names the Shapes that the p verb prints as an address.
func prvAddressShapes() []string {
	return []string{
		"pointer", "pointer to pointer", "slice of pointers", "slice of values", "map of pointers", "map of values",
	}
}

// prvAssertRequestShapes asserts that the request *p prints only its shape
// in every Shape fmt can call its methods in (all but unexported fields, a
// documented limit) with every verb but p, and with p too where p prints an
// address.
func prvAssertRequestShapes[T prvShaped](tb testing.TB, p *T) {
	tb.Helper()
	secrets := prvSecrets()
	for _, sh := range testutil.Shapes(p) {
		if strings.Contains(sh.Name, "unexported") {
			continue // fmt cannot call Format there: a documented limit
		}
		assertPrintsShapeOnly(tb, sh.Value, secrets...)
		if slices.Contains(prvAddressShapes(), sh.Name) {
			testutil.AssertRedacted(tb, fmt.Sprintf("%p", sh.Value), secrets, "the p verb on the %s", sh.Name)
		}
	}
	shape := (*p).String()
	testutil.AssertRedacted(tb, shape, secrets, "String")
	assert.Equal(tb, shape, (*p).GoString())
	assert.Contains(tb, shape, " bytes", "the shape gives byte counts")
}

// prvAssertResponseClean asserts that the response v never shows a marker:
// every Shape with every verb, reflection walks, JSON, the JSON of its Meta,
// and a testify diff against other, a response of the same type, when other
// is one.
func prvAssertResponseClean(tb testing.TB, v, other any) {
	tb.Helper()
	switch r := v.(type) {
	case *serper.SearchResponse:
		prvAssertValueClean(tb, r, other)
	case *serper.ImagesResponse:
		prvAssertValueClean(tb, r, other)
	case *serper.ScrapeResponse:
		prvAssertValueClean(tb, r, other)
	default:
		require.Failf(tb, "not a response", "%T", v)
	}
}

// prvAssertValueClean is prvAssertResponseClean for a *T.
func prvAssertValueClean[T any](tb testing.TB, p *T, other any) {
	tb.Helper()
	secrets := append(leakMarkers(), prvSecrets()...)
	testutil.AssertShapesNeverPrint(tb, p, secrets...)
	for _, sh := range testutil.Shapes(p) {
		testutil.AssertRedacted(tb, strings.Join(testutil.ReflectStrings(sh.Value), "\n"), secrets,
			"reflection walk of the %s", sh.Name)
		testutil.AssertRedacted(tb, jsonOf(tb, sh.Value), secrets, "JSON of the %s", sh.Name)
	}
	testutil.AssertRedacted(tb, jsonOf(tb, metaOf(p)), secrets, "JSON of Meta")
	if o, ok := other.(*T); ok {
		testutil.AssertDiffRedacted(tb, p, o, secrets...)
	}
}

// prvCheckSuccess asserts that got is a success whose capture follows
// capture and holds exactly body, and whose response never shows a marker,
// compared against decoded when it is a response.
func prvCheckSuccess(tb testing.TB, got outcome, body string, capture bool, decoded any) {
	tb.Helper()
	require.NoError(tb, got.err)
	assert.Equal(tb, captureState(capture, serper.RawCaptureComplete), got.meta.RawCapture)
	assertCapture(tb, capturedBody(capture, body), got.raw, got.rawOK, "the capture is the exact body")
	prvAssertResponseClean(tb, got.value, decoded)
}

// prvRefusal is an HTTP 200 body that decoding refuses, and what the refusal
// reports.
type prvRefusal struct {
	name    string
	body    string // $ stands for the marker
	kind    serper.FailureKind
	field   string
	invalid bool // not valid UTF-8 or not one JSON value, so never captured
	credits serper.Credits
}

// prvSearchRefusals are the Search bodies decoding refuses, with markers in
// strings and in member names, unknown and known.
func prvSearchRefusals() []prvRefusal {
	const unexpected = serper.FailureUnexpectedResponse
	return []prvRefusal{
		{
			name: "organic is a string", body: `{"searchParameters":{"q":"$"},"organic":"$","credits":4}`,
			field: "organic", credits: reported(4),
		},
		{
			name: "marker names and a null result", body: `{"$":1,"searchParameters":{"$":"$"},"organic":[null]}`,
			field: "organic[0]",
		},
		{
			name: "query is an object", body: `{"searchParameters":{"q":{"$":"$"}},"credits":4}`,
			field: "searchParameters.q", credits: reported(4),
		},
		{
			name: "position is a string", body: `{"searchParameters":{},"organic":[{"title":"$","position":"$"}]}`,
			field: "organic.position",
		},
		{
			name: "attribute is not a string", body: `{"searchParameters":{},"organic":[{"attributes":{"$":["$"]}}]}`,
			field: "organic[0].attributes",
		},
		{
			name:  "knowledge graph attributes are a string",
			body:  `{"searchParameters":{},"knowledgeGraph":{"title":"$","attributes":"$"}}`,
			field: "knowledgeGraph.attributes",
		},
		{
			name: "null sitelink", body: `{"searchParameters":{},"organic":[{"sitelinks":[{"title":"$"},null]}]}`,
			field: "organic[0].sitelinks[1]",
		},
		{
			name: "null related search", body: `{"searchParameters":{},"relatedSearches":[{"query":"$"},null]}`,
			field: "relatedSearches[1]",
		},
		{name: "question is a string", body: `{"searchParameters":{},"peopleAlsoAsk":["$"]}`, field: "peopleAlsoAsk"},
		{name: "searchParameters is a string", body: `{"searchParameters":"$"}`, field: "searchParameters"},
		{name: "member matched ignoring case", body: `{"SEARCHPARAMETERS":["$"]}`, field: "searchParameters"},
		{
			name: "invalid credits", body: `{"organic":"$","credits":"$"}`, field: "organic",
			credits: invalidCredits(),
		},
		{
			name: "no searchParameters", body: `{"$":"$","organic":[{"title":"$"}],"credits":4}`,
			kind: unexpected, field: "searchParameters", credits: reported(4),
		},
		{
			name: "null searchParameters", body: `{"searchParameters":null,"answerBox":{"answer":"$"}}`,
			kind: unexpected, field: "searchParameters",
		},
	}
}

// prvImagesRefusals are the Images bodies decoding refuses.
func prvImagesRefusals() []prvRefusal {
	const unexpected = serper.FailureUnexpectedResponse
	return []prvRefusal{
		{
			name: "images is a string", body: `{"searchParameters":{"q":"$"},"images":"$","credits":4}`,
			field: "images", credits: reported(4),
		},
		{name: "marker names and a null image", body: `{"$":1,"images":[{"title":"$"},null]}`, field: "images[1]"},
		{
			name: "image URL is an array", body: `{"searchParameters":{},"images":[{"imageUrl":["$"]}]}`,
			field: "images.imageUrl",
		},
		{name: "num is a string", body: `{"searchParameters":{"num":"$"}}`, field: "searchParameters.num"},
		{
			name: "no searchParameters", body: `{"images":[{"title":"$"}],"$":{}}`,
			kind: unexpected, field: "searchParameters",
		},
		{
			name: "null searchParameters", body: `{"searchParameters":null,"$":"$"}`,
			kind: unexpected, field: "searchParameters",
		},
	}
}

// prvScrapeRefusals are the Scrape bodies decoding refuses.
func prvScrapeRefusals() []prvRefusal {
	return []prvRefusal{
		{name: "text is an array", body: `{"text":["$"],"credits":4}`, field: "text", credits: reported(4)},
		{name: "no text", body: `{"$":"$","metadata":{"title":"$"}}`, field: "text"},
		{name: "null text", body: `{"text":null,"$":true}`, field: "text"},
		{name: "markdown is an object", body: `{"text":"$","markdown":{"$":"$"}}`, field: "markdown"},
		{name: "member matched ignoring case", body: `{"TeXt":{"$":1}}`, field: "text"},
		{
			name: "invalid credits", body: `{"text":"ok","markdown":["$"],"credits":"$"}`, field: "markdown",
			credits: invalidCredits(),
		},
	}
}

// prvCommonRefusals are the bodies every operation refuses before looking at
// a member: not valid UTF-8, not one JSON value, or not an object.
func prvCommonRefusals() []prvRefusal {
	return []prvRefusal{
		{name: "invalid UTF-8 in a value", body: "{\"$\":\"$\xff\"}", invalid: true},
		{name: "invalid UTF-8 in a member name", body: "{\"$\xfe\":1}", invalid: true},
		{name: "two values", body: `{"$":1} {"$":2}`, invalid: true},
		{name: "trailing text", body: `{"$":1}$`, invalid: true},
		{name: "syntax error", body: `{"$" "$"}`, invalid: true},
		{name: "cut short", body: `{"$":"$`, invalid: true},
		{name: "not JSON", body: `$`, invalid: true},
		{name: "an array", body: `["$",{"$":1}]`},
		{name: "a string", body: `"$"`},
	}
}

// prvRefusals returns the refused bodies of op, filled with the body marker;
// a refusal without a kind is malformed_response.
func prvRefusals(op serper.Operation) []prvRefusal {
	var own []prvRefusal
	switch op {
	case serper.OpSearch:
		own = prvSearchRefusals()
	case serper.OpImages:
		own = prvImagesRefusals()
	case serper.OpScrape:
		own = prvScrapeRefusals()
	default:
		panic("no refusals for operation " + strconv.Quote(string(op)))
	}
	all := slices.Concat(own, prvCommonRefusals())
	for i := range all {
		all[i].body = prvFill(all[i].body, prvMarkBody)
		if all[i].kind == "" {
			all[i].kind = serper.FailureMalformedResponse
		}
	}
	return all
}

func TestPrivacyRefused200Bodies(t *testing.T) {
	for _, o := range prvCases() {
		for _, r := range prvRefusals(o.op) {
			t.Run(o.name+"/"+r.name, func(t *testing.T) {
				prvCheckRefusal(t, o, r)
			})
		}
	}
}

// prvCheckRefusal runs o against the refused body r, live with capture off
// and on and through o's Decode helper.
func prvCheckRefusal(t *testing.T, o opCase, r prvRefusal) {
	t.Helper()
	captured := serper.RawCaptureComplete
	if r.invalid {
		captured = serper.RawCaptureFailed
	}
	for _, capture := range []bool{false, true} {
		c := newHTTPClientClient(t, prvKey, &http.Client{Transport: testutil.NewScript(testutil.Step{Body: r.body})})
		got := o.call(t.Context(), c, serper.WithRawCapture(capture))
		ce := prvAssertCall(t, got.err, o.want(wantCall{
			Kind: r.kind, Field: r.field, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
			Credits: r.credits, RawCapture: captureState(capture, captured),
		}))
		raw, ok := ce.RawJSON()
		assertCapture(t, capturedBody(capture && !r.invalid, r.body), raw, ok,
			"the refused body is kept exactly when it is valid JSON and capture is on")
	}
	got := o.decode([]byte(r.body))
	_ = prvAssertCall(t, got.err, wantCall{Kind: r.kind, Field: r.field, Op: o.op, Credits: r.credits})
}

// prvAnswer is a non-200 answer with markers in its headers and body.
type prvAnswer struct {
	name   string
	header http.Header
	body   string
}

// prvErrorAnswers returns error answers with markers in the body, in a JSON
// message, in a Location (valid or not), a cookie, a Content-Type parameter,
// header names and other header values.
func prvErrorAnswers() []prvAnswer {
	return []prvAnswer{
		{
			name: "JSON message with a location and a cookie",
			header: http.Header{
				"Content-Type": {"application/json; " + prvMarkHeader + "=1"},
				"Location":     {prvLocationURL},
				"Set-Cookie":   {"prvsession=" + prvMarkHeader + "; Path=/; Secure"},
				"X-Prv-Header": {prvMarkHeader},
			},
			body: `{"message":` + testutil.JSONQuote(prvMarkBody+": invalid key "+prvKey+" for "+prvQuery) + `}`,
		},
		{
			name: "HTML with an unparsable location",
			header: http.Header{
				"Content-Type": {"text/html; charset=" + prvMarkHeader},
				"Location":     {"https://[" + prvMarkLocation},
			},
			body: `<html><a href="` + prvTarget + `">` + prvMarkBody + `</a></html>`,
		},
		{
			name: "text with marker header names",
			header: http.Header{
				"Location":           {"://" + prvMarkLocation},
				"Prvheadermarker-Id": {prvMarkHeader},
				"Content-Encoding":   {prvMarkHeader},
				"Retry-After":        {prvMarkHeader},
				"Www-Authenticate":   {`Bearer error="` + prvMarkHeader + `"`},
			},
			body: prvMarkBody + "\n" + prvKey + "\n" + prvTarget,
		},
	}
}

func TestPrivacyErrorStatuses(t *testing.T) {
	for _, code := range statusCodes() {
		for _, a := range prvErrorAnswers() {
			t.Run(strconv.Itoa(code)+"/"+a.name, func(t *testing.T) {
				for _, o := range prvCases() {
					for _, capture := range []bool{false, true} {
						ct := &testutil.CountingTransport{
							Next: testutil.NewScript(testutil.Step{Status: code, Header: a.header, Body: a.body}),
						}
						c := newHTTPClientClient(t, prvKey, &http.Client{Transport: ct})
						got := o.call(t.Context(), c, serper.WithRawCapture(capture))
						// Had the http.Client seen an unparsable Location on a
						// 3xx, the call would fail as a transport error.
						ce := prvAssertCall(t, got.err, o.want(wantCall{
							Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code), Status: code,
							Attempts: 1, RawCapture: captureState(capture, serper.RawCaptureFailed),
						}))
						raw, ok := ce.RawJSON()
						assertCapture(t, "", raw, ok, "an error body is never captured")
						assert.Equal(t, 1, ct.Attempts(), "a redirect is never followed")
					}
				}
			})
		}
	}
}

// prvRawBody returns a successful answer of op whose marker is only in
// members the SDK does not type, so that no typed field can show it.
func prvRawBody(op serper.Operation) string {
	var body string
	switch op {
	case serper.OpSearch:
		body = `{"searchParameters":{"q":"example","type":"search","engine":"google"},` +
			`"organic":[{"title":"Example","link":"https://www.example.org/","position":1}],` +
			`"answerBox":{"answer":"$","$":true},"$":["$"],"credits":1}`
	case serper.OpImages:
		body = `{"searchParameters":{"q":"example","type":"images","engine":"google"},` +
			`"images":[{"title":"Example image","imageUrl":"https://images.example.net/1.jpg","position":1}],` +
			`"topStories":[{"title":"$"}],"$":"$","credits":1}`
	case serper.OpScrape:
		body = `{"text":"Example page text.","metadata":{"title":"$","$":"$"},"credits":2}`
	default:
		panic("no raw body for operation " + strconv.Quote(string(op)))
	}
	return prvFill(body, prvMarkRaw)
}

// prvHeaderCase is a set of HTTP 200 headers with markers, and whether the
// SDK refuses the answer for them.
type prvHeaderCase struct {
	name    string
	header  http.Header
	refused bool
}

// prvOKHeaders returns HTTP 200 headers with markers: in Content-Type
// parameters and types, Content-Encoding values, cookies, a Location, header
// names and other values.
func prvOKHeaders() []prvHeaderCase {
	return []prvHeaderCase{
		{name: "JSON with marker parameters", header: http.Header{
			"Content-Type": {"application/json; " + prvMarkHeader + "=" + prvMarkHeader + "; charset=utf-8"},
			"Set-Cookie":   {"prvsession=" + prvMarkHeader + "; Path=/; Secure; HttpOnly"},
		}},
		{name: "+json type with a marker", header: http.Header{
			"Content-Type":       {"application/" + prvMarkHeader + "+json"},
			"X-Prv-Header":       {prvMarkHeader},
			"Prvheadermarker-Id": {"1"},
		}},
		{name: "no content type", header: http.Header{
			"Location":         {prvLocationURL},
			"Content-Encoding": {"identity"},
			"Www-Authenticate": {`Bearer realm="` + prvMarkHeader + `"`},
		}},
		{name: "non-JSON type", refused: true, header: http.Header{"Content-Type": {"text/" + prvMarkHeader}}},
		{
			name: "broken parameter", refused: true,
			header: http.Header{"Content-Type": {`application/json; charset="` + prvMarkHeader}},
		},
		{name: "type is a marker", refused: true, header: http.Header{"Content-Type": {prvMarkHeader}}},
		{
			name: "two types", refused: true,
			header: http.Header{"Content-Type": {mediaJSON, "application/" + prvMarkHeader + "+json"}},
		},
		{
			name: "encoding is a marker", refused: true,
			header: http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {prvMarkHeader}},
		},
		{name: "encoding list with a marker", refused: true, header: http.Header{
			"Content-Encoding": {"identity, " + prvMarkHeader},
			"Set-Cookie":       {"prvsession=" + prvMarkHeader},
		}},
		{name: "encoding header in lower case", refused: true, header: http.Header{"content-encoding": {prvMarkHeader}}},
	}
}

func TestPrivacyResponseHeaders(t *testing.T) {
	for _, hc := range prvOKHeaders() {
		t.Run(hc.name, func(t *testing.T) {
			for _, o := range prvCases() {
				body := prvRawBody(o.op)
				for _, capture := range []bool{false, true} {
					b := testutil.NewBody(body)
					rt := testutil.TransportWith(http.StatusOK, hc.header, int64(len(body)), b)
					c := newHTTPClientClient(t, prvKey, &http.Client{Transport: rt})
					got := o.call(t.Context(), c, serper.WithRawCapture(capture))
					if hc.refused {
						_ = prvAssertCall(t, got.err, o.want(wantCall{
							Kind: serper.FailureUnexpectedResponse, OutcomeUnknown: true, Status: http.StatusOK,
							Attempts: 1, RawCapture: captureState(capture, serper.RawCaptureFailed),
						}))
						assert.Zero(t, b.BytesRead(), "a refused body is never read")
					} else {
						prvCheckSuccess(t, got, body, capture, nil)
					}
					assert.Equal(t, 1, b.Closes(), "the body is closed exactly once")
				}
			}
		})
	}
}

// prvTransportCase is an error an injected transport returns, and how the
// SDK must report it.
type prvTransportCase struct {
	name  string
	err   error
	kind  serper.FailureKind // FailureTransport when empty
	class string             // the transport class, "" for a timeout

	// unknown is OutcomeUnknown when the transport fails without reading the
	// request body. A transport that read it may have sent it, so then the
	// outcome is always unknown.
	unknown bool
}

// prvDeep returns inner wrapped several levels deep with %w and errors.Join,
// next to sibling errors, every level quoting markers.
func prvDeep(inner error) error {
	err := fmt.Errorf("%s level 3: %w", prvMarkJoin, inner)
	err = errors.Join(prvTextError(prvMarkJoin+" sibling"), err)
	err = fmt.Errorf("%s level 2: %w", prvMarkJoin, err)
	err = errors.Join(err, prvTextError(prvMarkJoin+" "+prvKey))
	return fmt.Errorf("%s level 1 %s: %w", prvMarkJoin, prvTarget, err)
}

// prvTransportCases returns transport errors quoting markers in every field
// they print.
func prvTransportCases() []prvTransportCase {
	const timeout = serper.FailureTimeout
	urlErr := &url.Error{
		Op: "Post " + prvMarkURLErr, URL: "https://" + prvMarkURLErr + ".example.com/search?key=" + prvKey,
		Err: prvTextError(prvMarkURLErr + " " + prvQuery),
	}
	dns := &net.DNSError{
		Err: prvMarkDNS + ": no such host", Name: prvMarkDNS + ".example.com", Server: prvMarkDNS + ".example.net:53",
		IsNotFound: true,
	}
	dial := &net.OpError{
		Op: "dial", Net: "tcp", Source: prvAddr(prvMarkNet + "-source"), Addr: prvAddr(prvMarkNet + "-addr"),
		Err: prvTextError(prvMarkNet + ": connection refused"),
	}
	cases := []prvTransportCase{
		{name: "url error", err: urlErr, class: "other", unknown: true},
		{name: "url error around a dial error", err: &url.Error{Op: "Post", URL: prvTarget, Err: dial}, class: "dial"},
		{name: "dial error", err: dial, class: "dial"},
		{name: "read error", class: "other", unknown: true, err: &net.OpError{
			Op: "read", Net: "tcp", Addr: prvAddr(prvMarkNet), Err: prvTextError(prvMarkNet + " " + prvMarkBody),
		}},
		{name: "connection reset", class: "reset", unknown: true, err: &net.OpError{
			Op: "write", Net: "tcp", Addr: prvAddr(prvMarkNet), Err: os.NewSyscallError("write "+prvMarkNet, syscall.ECONNRESET),
		}},
		{name: "proxy error", class: "proxy", err: &net.OpError{
			Op: "proxyconnect", Net: "tcp", Addr: prvAddr(prvMarkNet + ".example.net:3128"), Err: urlErr,
		}},
		// A TLS alert or a bad record can come after the request was written.
		{name: "TLS alert", class: "tls", unknown: true, err: &net.OpError{Op: "remote error", Err: prvTextError("tls: " + prvMarkTLS)}},
		{name: "DNS error", err: dns, class: "dns"},
		{name: "dial error around a DNS error", err: &net.OpError{Op: "dial", Net: "tcp", Err: dns}, class: "dns"},
		{name: "TLS record header", class: "tls", unknown: true, err: tls.RecordHeaderError{
			Msg: prvMarkTLS + ": first record does not look like a TLS handshake",
		}},
		{name: "certificate verification", class: "tls", err: &tls.CertificateVerificationError{
			Err: prvTextError("x509: " + prvMarkTLS),
		}},
		{name: "HTTP/2 GOAWAY", class: "other", unknown: true, err: prvGoAwayError{
			LastStreamID: 1, ErrCode: 2, DebugData: prvMarkGoAway + " " + prvKey,
		}},
		{
			name: "wrapped HTTP/2 GOAWAY", class: "other", unknown: true,
			err: fmt.Errorf("http2: %s: %w", prvMarkGoAway, prvGoAwayError{DebugData: prvMarkGoAway}),
		},
		{name: "DNS error deep down", class: "dns", err: prvDeep(&net.OpError{
			Op: "dial", Net: "tcp", Addr: prvAddr(prvMarkJoin), Err: dns,
		})},
		{
			name: "reset deep down", class: "reset", unknown: true,
			err: prvDeep(os.NewSyscallError(prvMarkJoin, syscall.ECONNRESET)),
		},
		{name: "unexpected EOF deep down", err: prvDeep(io.ErrUnexpectedEOF), class: "closed", unknown: true},
		{name: "net timeout", kind: timeout, unknown: true, err: testutil.TimeoutError{Msg: prvMarkTimeout + ": i/o timeout"}},
		{name: "dial timeout", kind: timeout, err: &net.OpError{
			Op: "dial", Net: "tcp", Addr: prvAddr(prvMarkTimeout), Err: testutil.TimeoutError{Msg: prvMarkTimeout},
		}},
		{name: "DNS timeout", kind: timeout, err: &net.DNSError{Err: prvMarkTimeout, Name: prvMarkDNS, IsTimeout: true}},
		{name: "timeout deep down", kind: timeout, unknown: true, err: prvDeep(testutil.TimeoutError{Msg: prvMarkTimeout})},
		{
			name: "wrapped deadline", kind: timeout, unknown: true,
			err: fmt.Errorf("%s: %w", prvMarkTimeout, context.DeadlineExceeded),
		},
	}
	for i := range cases {
		if cases[i].kind == "" {
			cases[i].kind = serper.FailureTransport
		}
	}
	return cases
}

func TestPrivacyTransportErrors(t *testing.T) {
	for _, tc := range prvTransportCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, o := range prvCases() {
				for _, capture := range []bool{false, true} {
					// The Script reads the request body before it fails.
					c := newHTTPClientClient(t, prvKey, &http.Client{Transport: testutil.NewScript(testutil.Step{Err: tc.err})})
					got := o.call(t.Context(), c, serper.WithRawCapture(capture))
					_ = prvAssertCall(t, got.err, o.want(wantCall{
						Kind: tc.kind, Class: tc.class, OutcomeUnknown: true, Attempts: 1,
						RawCapture: captureState(capture, serper.RawCaptureNoResponse),
					}))
					unread := testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })
					c = newHTTPClientClient(t, prvKey, &http.Client{Transport: unread})
					got = o.call(t.Context(), c, serper.WithRawCapture(capture))
					_ = prvAssertCall(t, got.err, o.want(wantCall{
						Kind: tc.kind, Class: tc.class, OutcomeUnknown: tc.unknown, Attempts: 1,
						RawCapture: captureState(capture, serper.RawCaptureNoResponse),
					}))
				}
			}
		})
	}
}

// prvReadCase is a response whose body read fails, or is refused, and how
// the SDK must report it. An HTTP 200 failure without a kind is a transport
// failure; any other status fails by the status alone.
type prvReadCase struct {
	name     string
	status   int    // 0 for 200
	content  string // "" for prvPartialBody
	header   http.Header
	end      error // what a read after the content returns, io.EOF when nil
	stall    bool
	declared func(n int64) int64 // the declared length from the content's, nil for none
	limit    int64               // WithMaxResponseBytes, 0 for the default
	unread   bool                // the body must never be read
	kind     serper.FailureKind
	class    string
}

// want returns the CallError of tc for o, with capture on or off.
func (tc prvReadCase) want(o opCase, capture bool) wantCall {
	status, kind, unknown := cmp.Or(tc.status, http.StatusOK), cmp.Or(tc.kind, serper.FailureTransport), true
	if status != http.StatusOK {
		kind, unknown = statusKind(status), statusOutcomeUnknown(status)
	}
	return o.want(wantCall{
		Kind: kind, Class: tc.class, OutcomeUnknown: unknown, Status: status, Attempts: 1,
		RawCapture: captureState(capture, serper.RawCaptureFailed),
	})
}

// prvPartialBody returns the start of a body, cut short, holding markers.
func prvPartialBody() string {
	return prvFill(`{"$":"$","q":`, prvMarkBody) + testutil.JSONQuote(prvQuery+" "+prvKey+" "+prvTarget)
}

// prvReadCases returns body reads failing with errors that quote markers,
// and bodies refused by their length.
func prvReadCases() []prvReadCase {
	large := strings.Repeat(prvMarkBody+" ", 200)
	exact := func(n int64) int64 { return n }
	readErr := prvTextError(prvMarkRead + " " + prvMarkBody)
	return []prvReadCase{
		{name: "read error", end: readErr, class: "other"},
		{name: "connection reset", class: "reset", end: &net.OpError{
			Op: "read", Net: "tcp", Addr: prvAddr(prvMarkRead), Err: os.NewSyscallError("read "+prvMarkRead, syscall.ECONNRESET),
		}},
		{name: "unexpected EOF", end: fmt.Errorf("%s: %w", prvMarkRead, io.ErrUnexpectedEOF), declared: exact, class: "closed"},
		{name: "closed connection", end: fmt.Errorf("%s: %w", prvMarkRead, net.ErrClosed), class: "closed"},
		{name: "timeout", end: testutil.TimeoutError{Msg: prvMarkRead + " " + prvMarkTimeout}, kind: serper.FailureTimeout},
		{name: "HTTP/2 GOAWAY", end: prvGoAwayError{LastStreamID: 3, ErrCode: 2, DebugData: prvMarkGoAway}, class: "other"},
		{name: "TLS record", end: tls.RecordHeaderError{Msg: prvMarkTLS + " " + prvMarkRead}, class: "tls"},
		{
			name: "joined DNS error", class: "dns",
			end: errors.Join(prvTextError(prvMarkRead), &net.DNSError{Name: prvMarkDNS, Err: prvMarkRead}),
		},
		{name: "stalled", stall: true, class: "stalled"},
		{name: "shorter than declared", declared: func(n int64) int64 { return n + 10 }, class: "closed"},
		{name: "longer than declared", declared: func(n int64) int64 { return n - 5 }, class: "closed"},
		{name: "too large as read", content: large, limit: 1 << 10, kind: serper.FailureResponseTooLarge},
		{
			name: "too large as declared", content: large, declared: exact, limit: 1 << 10, unread: true,
			kind: serper.FailureResponseTooLarge,
		},
		{name: "upstream error body", status: http.StatusInternalServerError, end: readErr},
		{name: "rejected error body", status: http.StatusNotFound, end: readErr},
		{
			name: "redirect body", status: http.StatusFound, end: readErr,
			header: http.Header{"Location": {prvLocationURL}, "Content-Type": {"text/html; " + prvMarkHeader + "=1"}},
		},
	}
}

func TestPrivacyBodyReadErrors(t *testing.T) {
	for _, tc := range prvReadCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, o := range prvCases() {
				for _, capture := range []bool{false, true} {
					prvCheckRead(t, tc, o, capture)
				}
			}
		})
	}
}

// prvCheckRead calls o, with capture on or off, against the answer of tc.
func prvCheckRead(t *testing.T, tc prvReadCase, o opCase, capture bool) {
	t.Helper()
	content := cmp.Or(tc.content, prvPartialBody())
	declared := int64(-1)
	if tc.declared != nil {
		declared = tc.declared(int64(len(content)))
	}
	var opts []serper.Option
	if tc.limit > 0 {
		opts = append(opts, serper.WithMaxResponseBytes(tc.limit))
	}
	b := testutil.NewBody(content)
	b.End, b.Stall = tc.end, tc.stall
	rt := testutil.TransportWith(cmp.Or(tc.status, http.StatusOK), tc.header, declared, b)
	c := newHTTPClientClient(t, prvKey, &http.Client{Transport: rt}, opts...)
	got := o.call(t.Context(), c, serper.WithRawCapture(capture))
	_ = prvAssertCall(t, got.err, tc.want(o, capture))
	assert.Equal(t, 1, b.Closes(), "the body is closed exactly once")
	if tc.unread {
		assert.Zero(t, b.BytesRead(), "a body declared too large is never read")
	}
}

func TestPrivacyDoneContext(t *testing.T) {
	for _, done := range doneContexts() {
		t.Run(done.name, func(t *testing.T) {
			ctx, cancel := done.ctx(t.Context())
			defer cancel()
			prvCheckDoneBeforeSend(ctx, t, done.kind)
		})
	}
}

// prvCheckDoneBeforeSend runs every operation with ctx, already done with a
// marker cause, and checks the CallError of kind, with nothing sent.
func prvCheckDoneBeforeSend(ctx context.Context, t *testing.T, kind serper.FailureKind) {
	t.Helper()
	// A done context sends nothing: the offline transport fails the test on
	// any round trip.
	c := newHTTPClientClient(t, prvKey, &http.Client{Transport: offlineTransport(t)})
	for _, o := range prvCases() {
		for _, capture := range []bool{false, true} {
			got := o.call(ctx, c, serper.WithRawCapture(capture))
			_ = prvAssertCall(t, got.err, o.want(wantCall{
				Kind: kind, RawCapture: captureState(capture, serper.RawCaptureNoResponse),
			}))
		}
	}
}

// prvWaitThenFail is a transport that waits for the call to end and fails
// with an error quoting markers and wrapping the context's cause.
func prvWaitThenFail() testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		testutil.Drain(r)
		<-r.Context().Done()
		return nil, fmt.Errorf("%s %s: %w", prvMarkCause, prvTarget, context.Cause(r.Context()))
	}
}

// prvCauseCtx returns a child of parent that times out after a second with a
// cause quoting markers.
func prvCauseCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(parent, time.Second, prvTextError(prvMarkCause+" "+prvKey))
}

func TestPrivacyEndedInFlight(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  wantCall               // without Op, Origin, Path and RawCapture
		state serper.RawCaptureState // the capture state when capture is on
		// call calls o with opt, in a synctest bubble.
		call func(t *testing.T, o opCase, opt serper.CallOption) outcome
	}{
		{
			name: "canceled", state: serper.RawCaptureNoResponse,
			want: wantCall{Kind: serper.FailureCanceled, OutcomeUnknown: true, Attempts: 1},
			call: func(t *testing.T, o opCase, opt serper.CallOption) outcome {
				t.Helper()
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					testutil.Drain(r)
					cancel(prvTextError(prvMarkCause + " " + prvKey))
					return nil, &net.OpError{Op: "read", Net: "tcp", Addr: prvAddr(prvMarkNet), Err: prvTextError(prvMarkCause)}
				})
				return o.call(ctx, newHTTPClientClient(t, prvKey, &http.Client{Transport: rt}), opt)
			},
		},
		{
			name: "caller deadline", state: serper.RawCaptureNoResponse,
			want: wantCall{Kind: serper.FailureDeadlineExceeded, OutcomeUnknown: true, Attempts: 1},
			call: func(t *testing.T, o opCase, opt serper.CallOption) outcome {
				t.Helper()
				ctx, cancel := prvCauseCtx(t.Context())
				defer cancel()
				return o.call(ctx, newHTTPClientClient(t, prvKey, &http.Client{Transport: prvWaitThenFail()}), opt)
			},
		},
		{
			name: "SDK timeout", state: serper.RawCaptureNoResponse,
			want: wantCall{Kind: serper.FailureTimeout, OutcomeUnknown: true, Attempts: 1},
			call: func(t *testing.T, o opCase, opt serper.CallOption) outcome {
				t.Helper()
				c := newHTTPClientClient(t, prvKey, &http.Client{Transport: prvWaitThenFail()}, serper.WithTimeout(time.Second))
				return o.call(t.Context(), c, opt)
			},
		},
		{
			name: "caller deadline during the body read", state: serper.RawCaptureFailed,
			want: wantCall{Kind: serper.FailureDeadlineExceeded, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1},
			call: func(t *testing.T, o opCase, opt serper.CallOption) outcome {
				t.Helper()
				ctx, cancel := prvCauseCtx(t.Context())
				defer cancel()
				b := testutil.NewBlockingBody(prvPartialBody())
				c := newHTTPClientClient(t, prvKey, &http.Client{Transport: testutil.TransportWith(http.StatusOK, nil, -1, b)})
				got := o.call(ctx, c, opt)
				assert.Equal(t, 1, b.Closes(), "the body is closed exactly once")
				return got
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for _, o := range prvCases() {
					for _, capture := range []bool{false, true} {
						got := tc.call(t, o, serper.WithRawCapture(capture))
						want := tc.want
						want.RawCapture = captureState(capture, tc.state)
						_ = prvAssertCall(t, got.err, o.want(want))
					}
				}
			})
		})
	}
}

func TestPrivacyValidationKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "space", key: prvKey + " " + prvMarkBody},
		{name: "leading space", key: " " + prvKey},
		{name: "tab", key: prvKey + "\t"},
		{name: "header injection", key: prvKey + "\r\nX-Injected: " + prvMarkHeader},
		{name: "DEL", key: prvKey + "\x7f"},
		{name: "NUL", key: prvKey + "\x00"},
		{name: "non-ASCII", key: prvKey + "ü"},
		{name: "invalid UTF-8", key: prvKey + "\xff"},
		{name: "too long", key: prvKey + strings.Repeat("k", 257-len(prvKey))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := serper.NewClient(tc.key)
			assert.Nil(t, c)
			assertValidation(t, err, serper.ErrInvalidConfig, "", "apiKey", reasonInvalidFormat)
			prvAssertValidationClean(t, err, tc.key)
		})
	}

	// A valid key with an invalid option: the error names the option only.
	c, err := serper.NewClient(prvKey, serper.WithTimeout(-time.Second))
	assert.Nil(t, c)
	assertValidation(t, err, serper.ErrInvalidConfig, "", "WithTimeout", reasonOutOfRange)
	prvAssertValidationClean(t, err)
}

// prvInvalidBaseURL is a refused base URL holding markers, and the reason.
type prvInvalidBaseURL struct {
	raw    string
	reason string
}

// prvInvalidBaseURLs returns base URLs refused for every reason, each
// holding the base URL marker.
func prvInvalidBaseURLs() []prvInvalidBaseURL {
	const m = prvMarkBaseURL
	return []prvInvalidBaseURL{
		{raw: "http://" + m + ".example.com", reason: reasonUnsupportedScheme},
		{raw: "ftp://" + m + ".example.com", reason: reasonUnsupportedScheme},
		{raw: m + ".example.com", reason: reasonUnsupportedScheme},
		{raw: "//" + m + ".example.com", reason: reasonUnsupportedScheme},
		{raw: "https:" + m, reason: reasonInvalidURL},
		{raw: "https://" + m + "@example.com", reason: reasonHasUserinfo},
		{raw: "https://" + m + ":" + prvKey + "@example.com", reason: reasonHasUserinfo},
		{raw: "https://example.com?" + m, reason: reasonHasQuery},
		{raw: "https://example.com/#" + m, reason: reasonHasFragment},
		{raw: "https://example.com\\" + m, reason: reasonInvalidURL},
		{raw: "https://example.com/" + m, reason: reasonHasPath},
		{raw: "https:///" + m, reason: reasonMissingHost},
		{raw: "https://" + m + ".example.com:", reason: reasonInvalidPort},
		{raw: "https://" + m + ".example.com:0", reason: reasonInvalidPort},
		{raw: "https://" + m + ".example.com:65536", reason: reasonInvalidPort},
		{raw: "https://example.com:" + m, reason: reasonInvalidURL},
		{raw: "https://" + m + " .example.com", reason: reasonInvalidURL},
		{raw: "https://" + m + ".example.com\n", reason: reasonInvalidURL},
		{raw: " https://" + m + ".example.com", reason: reasonInvalidURL},
		{raw: "https://[" + m, reason: reasonInvalidURL},
		{raw: "https://%" + m, reason: reasonInvalidURL},
	}
}

func TestPrivacyValidationOptions(t *testing.T) {
	for _, u := range prvInvalidBaseURLs() {
		for _, opt := range []struct {
			field string
			with  func(string) serper.Option
		}{
			{field: "WithSearchBaseURL", with: serper.WithSearchBaseURL},
			{field: "WithScrapeBaseURL", with: serper.WithScrapeBaseURL},
		} {
			c, err := serper.NewClient(prvKey, opt.with(u.raw))
			assert.Nil(t, c)
			assertValidation(t, err, serper.ErrInvalidConfig, "", opt.field, u.reason)
			prvAssertValidationClean(t, err, u.raw)
		}
	}
	for _, ua := range []string{
		prvMarkAgent + "\r\nX-Injected: " + prvMarkHeader, " " + prvMarkAgent, prvMarkAgent + " ",
		prvMarkAgent + "\x00", prvMarkAgent + "ü", prvMarkAgent + "\xff", strings.Repeat(prvMarkAgent, 20),
	} {
		c, err := serper.NewClient(prvKey, serper.WithUserAgent(ua))
		assert.Nil(t, c)
		assertValidation(t, err, serper.ErrInvalidConfig, "", "WithUserAgent", reasonInvalidFormat)
		prvAssertValidationClean(t, err, ua)
	}
}

func TestPrivacyValidationRequests(t *testing.T) {
	for _, tc := range invalidRequests() {
		t.Run(tc.name, func(t *testing.T) {
			// Nothing is sent: the offline transport fails the test on any
			// round trip.
			c := newHTTPClientClient(t, prvKey, &http.Client{Transport: offlineTransport(t)})
			err := tc.call(t.Context(), c)
			assertValidation(t, err, serper.ErrInvalidRequest, tc.op, tc.field, tc.reason)
			prvAssertValidationClean(t, err)
		})
	}

	c := newHTTPClientClient(t, prvKey, &http.Client{Transport: offlineTransport(t)})
	var nilCtx context.Context
	for _, o := range prvCases() {
		got := o.call(nilCtx, c)
		assertValidation(t, got.err, serper.ErrInvalidRequest, o.op, "ctx", reasonNil)
		prvAssertValidationClean(t, got.err)
		for _, unusable := range []*serper.Client{nil, {}} {
			got = o.call(t.Context(), unusable)
			assertValidation(t, got.err, serper.ErrInvalidConfig, o.op, "Client", reasonNotInitialized)
			prvAssertValidationClean(t, got.err)
		}
	}
}

func TestPrivacyClientNeverPrintsKey(t *testing.T) {
	s := testutil.NewScript(testutil.Step{Body: searchOK})
	injected := newKeyedClient(t, prvKey,
		serper.WithHTTPClient(&http.Client{Transport: s, Timeout: time.Minute}),
		serper.WithTimeout(20*time.Second), serper.WithScrapeTimeout(time.Minute),
		serper.WithMaxRequestBytes(2<<10), serper.WithMaxResponseBytes(4<<10),
		serper.WithUserAgent("example-app/1.0 (+https://www.example.org)"),
		serper.WithSearchBaseURL(searchOrigin), serper.WithScrapeBaseURL(scrapeOrigin+"/"),
		serper.WithDefaultRawCapture(),
	)
	// After a call, the injected transport holds the key in a recorded
	// header, beyond the Client's reach.
	require.NoError(t, opCaseNamed("Search").call(t.Context(), injected).err)
	assert.Equal(t, []string{prvKey}, testutil.OnlyRequest(t, s).Header.Values("X-Api-Key"))

	clients := []struct {
		name           string
		c              *serper.Client
		search, scrape string
	}{
		{name: "defaults", c: newKeyedClient(t, prvKey), search: serper.DefaultSearchBaseURL, scrape: serper.DefaultScrapeBaseURL},
		{name: "every option", c: injected, search: searchOrigin, scrape: scrapeOrigin},
	}
	secrets := append(leakMarkers(), prvSecrets()...)
	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			testutil.AssertShapesNeverPrint(t, tc.c, secrets...)
			for _, sh := range testutil.Shapes(tc.c) {
				testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(sh.Value), "\n"), secrets,
					"reflection walk of the %s", sh.Name)
			}
			str := tc.c.String()
			assert.Contains(t, str, "searchOrigin: "+tc.search+",")
			assert.Contains(t, str, "scrapeOrigin: "+tc.scrape+",")
			testutil.AssertRedacted(t, str, secrets, "String")
			assert.Equal(t, str, tc.c.GoString())
			assert.Equal(t, str, fmt.Sprint(tc.c))
			assert.Equal(t, str, fmt.Sprintf("%#v", tc.c))
			assert.JSONEq(t, "{}", jsonOf(t, tc.c), "a Client has no exported fields")
		})
	}
	testutil.AssertDiffRedacted(t, clients[0].c, clients[1].c, secrets...)
}

func TestPrivacyRequestsPrintShapeOnly(t *testing.T) {
	sreq := prvSearchRequest()
	ireq := serper.ImagesRequest(sreq)
	creq := prvScrapeRequest()
	prvAssertRequestShapes(t, &sreq)
	prvAssertRequestShapes(t, &ireq)
	prvAssertRequestShapes(t, &creq)
}

// TestPrivacyRequestDocumentedLimits documents what the request types cannot
// hide: encoding/json writes every field, and fmt prints the fields when it
// cannot call Format, for the p verb on a value and inside an unexported
// field.
func TestPrivacyRequestDocumentedLimits(t *testing.T) {
	sreq := prvSearchRequest()
	data := jsonOf(t, sreq)
	for _, v := range []string{prvQuery, prvReqLocation, prvGL, prvHL, prvTBS} {
		assert.Contains(t, data, v, "json.Marshal writes every field of a request: log its String instead")
	}
	assert.Contains(t, jsonOf(t, serper.ImagesRequest(sreq)), prvQuery)
	assert.Contains(t, jsonOf(t, prvScrapeRequest()), prvTarget)

	assert.Contains(t, fmt.Sprintf("%p", sreq), prvQuery, "the p verb on a value bypasses Format")
	held := struct{ req serper.SearchRequest }{req: sreq}
	assert.Contains(t, fmt.Sprintf("%v", held), prvQuery, "fmt cannot call Format on an unexported field")
}

func TestPrivacyRawCaptureResponse(t *testing.T) {
	for _, m := range captureModes() {
		t.Run(m.name, func(t *testing.T) {
			for _, o := range prvCases() {
				body := prvRawBody(o.op)
				c := newHTTPClientClient(t, prvKey, &http.Client{Transport: testutil.NewScript(testutil.Step{Body: body})}, m.client...)
				got := o.call(t.Context(), c, m.call...)
				decoded := o.decode([]byte(body))
				require.NoError(t, decoded.err)
				prvCheckSuccess(t, got, body, m.on, decoded.value)
				if m.on {
					assert.Contains(t, string(got.raw), prvMarkRaw, "the capture holds what no typed field can")
				}
			}
		})
	}
}

// prvRawRefusals returns HTTP 200 bodies of op that decoding refuses, with
// the raw marker only in members the SDK does not type.
func prvRawRefusals(op serper.Operation) []prvRefusal {
	const malformed, unexpected = serper.FailureMalformedResponse, serper.FailureUnexpectedResponse
	var out []prvRefusal
	switch op {
	case serper.OpSearch:
		out = []prvRefusal{
			{
				name: "null result", kind: malformed, field: "organic[0]",
				body: `{"searchParameters":{"q":"example"},"organic":[null],"answerBox":{"answer":"$"}}`,
			},
			{
				name: "no searchParameters", kind: unexpected, field: "searchParameters", credits: reported(3),
				body: `{"organic":[],"$":{"$":"$"},"credits":3}`,
			},
		}
	case serper.OpImages:
		out = []prvRefusal{
			{
				name: "null image", kind: malformed, field: "images[1]",
				body: `{"searchParameters":{"q":"example"},"images":[{"title":"Example"},null],"$":1}`,
			},
			{
				name: "no searchParameters", kind: unexpected, field: "searchParameters",
				body: `{"images":[],"topStories":[{"title":"$"}]}`,
			},
		}
	case serper.OpScrape:
		out = []prvRefusal{
			{
				name: "no text", kind: malformed, field: "text", credits: reported(2),
				body: `{"metadata":{"title":"$"},"credits":2}`,
			},
			{name: "not an object", kind: malformed, body: `["$"]`},
		}
	default:
		panic("no raw refusals for operation " + strconv.Quote(string(op)))
	}
	for i := range out {
		out[i].body = prvFill(out[i].body, prvMarkRaw)
	}
	return out
}

func TestPrivacyRawCaptureCallError(t *testing.T) {
	for _, o := range prvCases() {
		for _, r := range prvRawRefusals(o.op) {
			t.Run(o.name+"/"+r.name, func(t *testing.T) {
				s := testutil.NewScript(testutil.Step{Body: r.body})
				c := newHTTPClientClient(t, prvKey, &http.Client{Transport: s}, serper.WithDefaultRawCapture())
				got := o.call(t.Context(), c)
				ce := prvAssertCall(t, got.err, o.want(wantCall{
					Kind: r.kind, Field: r.field, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
					Credits: r.credits, RawCapture: serper.RawCaptureComplete,
				}))
				raw, ok := ce.RawJSON()
				require.True(t, assertCapture(t, r.body, raw, ok))
				assert.Contains(t, string(raw), prvMarkRaw, "ce.RawJSON returns what nothing else shows")
				raw[0] = 'X'
				again, ok := ce.RawJSON()
				assertCapture(t, r.body, again, ok, "each RawJSON is a new copy")
			})
		}
	}
}

// prvWireHandler answers each operation's path with its smallest success, or
// with status and a Location to a page under example.com when status is not
// 200, setting a cookie and a marker header either way.
func prvWireHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, known := map[string]string{pathSearch: searchOK, pathImages: imagesOK, pathScrape: scrapeOK}[r.URL.Path]
		if r.Method != http.MethodPost || !known {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Set-Cookie", "prvsession="+prvMarkHeader+"; Path=/; Secure")
		h.Set("X-Prv-Header", prvMarkHeader)
		if status != http.StatusOK {
			h.Set("Location", prvLocationURL)
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `<a href="`+prvLocationURL+`">`+prvMarkBody+`</a>`)
			return
		}
		h.Set("Content-Type", mediaJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

func TestPrivacyWire(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, status := range []int{
			http.StatusOK, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
		} {
			t.Run(proto.Name()+"/"+strconv.Itoa(status), func(t *testing.T) {
				prvCheckWire(t, proto, status)
			})
		}
	}
}

// prvCheckWire calls every operation twice against two TLS servers speaking
// proto and answering with status, through a caller's client that has a
// cookie jar, and checks what reached the servers.
func prvCheckWire(t *testing.T, proto testutil.Protocol, status int) {
	t.Helper()
	searchLog, scrapeLog := &prvHostLog{}, &prvHostLog{}
	_, s := newServerClientProto(t, proto, searchLog.wrap(prvWireHandler(status)), scrapeLog.wrap(prvWireHandler(status)))
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hc := *s.search.Client()
	hc.Jar = jar
	c := newKeyedClient(t, prvKey, serper.WithHTTPClient(&hc),
		serper.WithSearchBaseURL(s.search.URL()), serper.WithScrapeBaseURL(s.scrape.URL()))

	for _, o := range prvCases() {
		for range 2 { // the second call would carry a cookie the first one stored
			got := o.call(t.Context(), c)
			if status == http.StatusOK {
				require.NoError(t, got.err)
				continue
			}
			_ = prvAssertCall(t, got.err, o.wantAt(s.of(o).URL(), wantCall{
				Kind: serper.FailureUnexpectedResponse, OutcomeUnknown: statusOutcomeUnknown(status), Status: status,
				Attempts: 1,
			}))
		}
	}

	// No redirect was followed and the scrape target was never fetched: the
	// servers' clients route example.com to the search server, so either
	// would show up as a request whose Host is under example.com.
	assert.Equal(t, 4, s.search.Hits(), "two calls each of Search and Images")
	assert.Equal(t, 2, s.scrape.Hits(), "two calls of Scrape")
	for _, side := range []struct {
		srv *testutil.Server
		log *prvHostLog
	}{{s.search, searchLog}, {s.scrape, scrapeLog}} {
		u := mustURL(t, side.srv.URL())
		hosts := side.log.list()
		assert.Len(t, hosts, side.srv.Hits())
		for _, h := range hosts {
			assert.Equal(t, u.Host, h, "every request went to the server's own origin")
			assert.NotContains(t, h, "example.com")
		}
		for _, r := range side.srv.Requests() {
			prvAssertWireRequest(t, r)
		}
		assert.Empty(t, jar.Cookies(u), "the caller's jar never sees a cookie")
	}

	// Control: the servers' client does route a page under example.com to the
	// search server, so the checks above would have seen a fetch of it.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, prvTarget, nil)
	require.NoError(t, err)
	resp, err := s.search.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	hosts := searchLog.list()
	require.NotEmpty(t, hosts)
	assert.Equal(t, "www.example.com", hosts[len(hosts)-1], "a fetch of the target reaches the search server")
}

// prvAssertWireRequest asserts that the key appears in r only as its one
// X-Api-Key value, and the request values only in its body: never in the URL
// or another header, and that r carries no credential, cookie or referer
// header.
func prvAssertWireRequest(tb testing.TB, r testutil.Request) {
	tb.Helper()
	secrets := append(leakMarkers(), prvSecrets()...)
	assert.Equal(tb, http.MethodPost, r.Method)
	assert.Contains(tb, []string{pathSearch, pathImages, pathScrape}, r.Path)
	assert.Equal(tb, []string{prvKey}, r.Header.Values("X-Api-Key"), "the key goes in X-Api-Key, once")
	for name, values := range r.Header {
		if http.CanonicalHeaderKey(name) == "X-Api-Key" {
			continue
		}
		testutil.AssertRedacted(tb, name+": "+strings.Join(values, ", "), secrets, "header %s", name)
	}
	for _, h := range bannedHeaders() {
		assert.Empty(tb, r.Header.Values(h), "header %s must never be sent", h)
	}
	testutil.AssertRedacted(tb, r.URL, secrets, "URL")
	testutil.AssertRedacted(tb, string(r.Body), []string{prvKey, "prvkeymarker"}, "body")
	if r.Path == pathScrape {
		assert.Contains(tb, string(r.Body), prvTarget, "the target travels in the body only")
	} else {
		assert.Contains(tb, string(r.Body), prvQuery, "the query travels in the body only")
	}
}
