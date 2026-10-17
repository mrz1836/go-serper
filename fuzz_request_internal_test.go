package serper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

// Synthetic values of the request and error-text fuzz targets. The marker is
// planted in front of fuzz input in response bodies: no error rendering may
// ever contain it.
const (
	fzrKey          = "test-fzr-keymarker-41c0" // gitleaks:allow (synthetic test marker)
	fzrQuery        = "fzr querymarker 77a1"
	fzrTarget       = "https://www.example.com/fzr?token=fzrtargetmarker"
	fzrMarker       = "fzrmarker-5e83c1a7"
	fzrSearchOrigin = "https://127.0.0.1:8443"
	fzrScrapeOrigin = "https://127.0.0.1:9443"
	fzrWindow       = 8 // the shortest input byte sequence that counts as a leak
	fzrRequestLimit = 1 << 20
)

// fzrScrapeReasons are the only reasons a scrape target is refused for.
func fzrScrapeReasons() []string {
	return []string{"", "required", "invalid_utf8", "invalid_url", "unsupported_scheme", "has_userinfo", "missing_host"}
}

// fzrBaseReasons are the only reasons a base URL is refused for.
func fzrBaseReasons() []string {
	return []string{
		"", "required", "invalid_url", "has_query", "has_fragment", "unsupported_scheme", "has_userinfo",
		"missing_host", "has_path", "invalid_host", "invalid_port",
	}
}

// fzrSplitHost splits the host[:port] of a parsed URL into its hostname, an
// IPv6 literal without its brackets, and its port, and reports whether a
// port separator was present, independently of url.URL's own helpers.
func fzrSplitHost(hostport string) (hostname, port string, hasPort bool) {
	if strings.HasPrefix(hostport, "[") {
		end := strings.LastIndexByte(hostport, ']')
		if end < 0 {
			return hostport, "", false
		}
		rest := hostport[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return hostport[1:end], "", false
		}
		return hostport[1:end], rest[1:], true
	}
	i := strings.LastIndexByte(hostport, ':')
	if i < 0 {
		return hostport, "", false
	}
	return hostport[:i], hostport[i+1:], true
}

// fzrPortOK is the port rule of the contract: no separator at all, or a
// canonical decimal from 1 to 65535 (no empty port, no leading zero, no 0).
func fzrPortOK(port string, hasPort bool) bool {
	if !hasPort {
		return true
	}
	if port == "" || len(port) > 5 || port[0] == '0' {
		return false
	}
	n := 0
	for _, c := range []byte(port) {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n <= 65535
}

// fzrWantScrapeReason is the scrape target oracle, from the contract: "" is
// required; then valid UTF-8; then a url.Parse result that is not opaque;
// then an http or https scheme in any case; then no userinfo; then a
// non-empty hostname.
func fzrWantScrapeReason(s string) string {
	switch {
	case s == "":
		return "required"
	case !utf8.ValidString(s):
		return "invalid_utf8"
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Opaque != "":
		return "invalid_url"
	case !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https"):
		return "unsupported_scheme"
	case u.User != nil:
		return "has_userinfo"
	}
	if hostname, _, _ := fzrSplitHost(u.Host); hostname == "" {
		return "missing_host"
	}
	return ""
}

// fzrWantOrigin is the base URL oracle, from the contract, in its order:
// required; no backslash, '?' or '#' anywhere; a url.Parse result that is
// not opaque; scheme https; no userinfo; a non-empty hostname; a path of ""
// or "/" with no RawPath; a valid port. It returns the host[:port] kept.
func fzrWantOrigin(raw string) (string, string) {
	switch {
	case raw == "":
		return "", "required"
	case strings.Contains(raw, `\`), strings.Contains(raw, "%"):
		return "", "invalid_url"
	case strings.Contains(raw, "?"):
		return "", "has_query"
	case strings.Contains(raw, "#"):
		return "", "has_fragment"
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Opaque != "":
		return "", "invalid_url"
	case !strings.EqualFold(u.Scheme, "https"):
		return "", "unsupported_scheme"
	case u.User != nil:
		return "", "has_userinfo"
	}
	hostname, port, hasPort := fzrSplitHost(u.Host)
	switch {
	case hostname == "":
		return "", "missing_host"
	case (u.Path != "" && u.Path != "/") || u.RawPath != "":
		return "", "has_path"
	case !fzrHostOK(u.Host, hostname):
		return "", "invalid_host"
	case !fzrPortOK(port, hasPort):
		return "", "invalid_port"
	}
	return u.Host, ""
}

// fzrHostOK is an independent statement of the host rule: a bracketed IPv6
// address without a zone, or an ASCII host name of at most 253 bytes whose
// dot-separated labels are 1 to 63 bytes of [A-Za-z0-9-], neither starting
// nor ending with a hyphen.
func fzrHostOK(hostport, hostname string) bool {
	if strings.HasPrefix(hostport, "[") {
		ip, err := netip.ParseAddr(hostname)
		return err == nil && ip.Is6() && ip.Zone() == ""
	}
	if len(hostname) > 253 {
		return false
	}
	return regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`).
		MatchString(hostname)
}

// fzrScrapeBody is the exact JSON body of a scrape of target: the target as a
// JSON string with HTML left bare, then includeMarkdown only when set.
func fzrScrapeBody(target string, markdown bool) string {
	b := `{"url":` + testutil.JSONQuoteNoHTML(target)
	if markdown {
		b += `,"includeMarkdown":true`
	}
	return b + "}"
}

func FuzzScrapeURL(f *testing.F) {
	for _, s := range []string{
		"", fzrTarget, "http://example.com", "HTTP://EXAMPLE.COM", "hTtPs://example.org/a b", "https://example.net/",
		"ftp://example.com", "mailto:user@example.com", "http:example.com", "//example.com", "example.com",
		"http://", "http:///path", "http://:80", "http://user@example.com", "http://user:pass@example.com",
		"http://@example.com", "http://[::1]:8080/x", "http://[fe80::1%25en0]/", "http://[::1", "http://exa mple.com",
		"http://example.com/\x7f", "\xff", "http://example.com/\xff", "https://example.com/<b>&  ",
		"javascript:alert(1)", "http://example.com#frag", "http://%41.example.com", "http://a:b:c",
		"  http://example.com", "http://example.com:99999", "https://example.com/?q=\"quoted\"&x=\\",
	} {
		f.Add(s, false)
	}
	f.Add(fzrTarget, true)
	f.Fuzz(func(t *testing.T, s string, markdown bool) {
		got := scrapeURLProblem(s)
		want := fzrWantScrapeReason(s)
		assert.Equal(t, want, got, "scrapeURLProblem(%q)", s)
		assert.Contains(t, fzrScrapeReasons(), got)

		w, err := ScrapeRequest{URL: s, IncludeMarkdown: markdown}.wire()
		if want != "" {
			assert.Nil(t, w)
			ve := testutil.RequireErrorAs[*ValidationError](t, err)
			assert.Equal(t, ValidationError{Err: ErrInvalidRequest, Op: OpScrape, Field: "URL", Reason: want}, *ve)
			assert.Equal(t, "serper: invalid request: scrape: URL: "+want, err.Error())
			return
		}
		require.NoError(t, err)
		require.NotNil(t, w)
		assert.Equal(t, s, w.URL, "the target is sent exactly as given")
		body, err := encodeBody(OpScrape, w, fzrRequestLimit)
		wantBody := fzrScrapeBody(s, markdown)
		if len(wantBody) > fzrRequestLimit {
			ve := testutil.RequireErrorAs[*ValidationError](t, err)
			assert.Equal(t, "too_large", ve.Reason)
			return
		}
		require.NoError(t, err)
		assert.Equal(t, []byte(wantBody), body, "the exact scrape body")
	})
}

func FuzzBaseURL(f *testing.F) {
	for _, s := range []string{
		"", DefaultSearchBaseURL, DefaultScrapeBaseURL, fzrSearchOrigin, "https://example.com/",
		"https://example.com:443", "https://example.com:", "https://example.com:/", "https://example.com:0",
		"https://example.com:1", "https://example.com:65535", "https://example.com:65536", "https://example.com:0443",
		"https://example.com:99999999999999999999", "https://example.com/path", "https://example.com//",
		"https://example.com/%2F", "https://example.com/%61", "https://example.com?", "https://example.com/?a=1",
		"https://example.com#", "https://example.com\\", "http://example.com", "HTTPS://EXAMPLE.COM",
		"hTTps://example.org", "https://user@example.com", "https://user:pw@example.com", "https://:443",
		"https://", "https:///", "https:example.com", "https://[::1]:8443", "https://[::1]", "https://[::1]:",
		"https://[::1]:0", "https://[::ffff:127.0.0.1]", "https://[1.2.3.4]", "https://%FF", "https://%C3%A9x.example",
		"https://a:1:2", "ftp://example.com", " https://example.com", "https://exa mple.com", "https://example.com/\x00",
		"https://[fe80::1%25en0]:8443", "https://google.serper.dev\u3002evil.com", "https://xn--bcher-kva.example",
		"https://a_b.example.com", "https://-a.example", "https://a-.example", "https://a..b", "https://a.",
		"https://Example-1.COM:8443", "https://b\u00fccher.example", "https://[::1%25lo0]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		host, reason := originHost(raw)
		wantHost, wantReason := fzrWantOrigin(raw)
		assert.Equal(t, wantReason, reason, "originHost(%q) reason", raw)
		assert.Equal(t, wantHost, host, "originHost(%q) host", raw)
		assert.Contains(t, fzrBaseReasons(), reason)
		if reason == "" {
			again, againReason := originHost("https://" + host)
			assert.Empty(t, againReason, "the accepted host %q re-validates", host)
			assert.Equal(t, host, again, "the accepted host %q re-validates as itself", host)
		} else {
			assert.Empty(t, host, "a refused base URL keeps no host")
		}
		for _, opt := range []struct {
			name string
			with func(string) Option
		}{{"WithSearchBaseURL", WithSearchBaseURL}, {"WithScrapeBaseURL", WithScrapeBaseURL}} {
			c, err := NewClient(fzrKey, WithHTTPClient(&http.Client{}), opt.with(raw))
			if wantReason != "" {
				assert.Nil(t, c)
				ve := testutil.RequireErrorAs[*ValidationError](t, err)
				assert.Equal(t, ValidationError{Err: ErrInvalidConfig, Field: opt.name, Reason: wantReason}, *ve)
				continue
			}
			require.NoError(t, err, "%s(%q)", opt.name, raw)
			eps := []endpoint{c.search, c.images}
			if opt.name == "WithScrapeBaseURL" {
				eps = []endpoint{c.scrape}
			}
			for _, ep := range eps {
				assert.Equal(t, wantHost, ep.host)
				assert.Equal(t, "https://"+wantHost, ep.origin)
			}
		}
	})
}

// fzrWantError renders the CallError Error grammar, "serper: <op>: <kind>
// [ (HTTP <n>)][: <field>]" for an error without a transport class, with the
// documented fallbacks for an empty op ("serper: <kind>") and kind
// ("call_failed").
func fzrWantError(op, kind string, status int, field string) string {
	s := "serper: "
	if op != "" {
		s += op + ": "
	}
	if kind == "" {
		kind = "call_failed"
	}
	s += kind
	if status != 0 {
		s += " (HTTP " + strconv.Itoa(status) + ")"
	}
	if field != "" {
		s += ": " + field
	}
	return s
}

// fzrCreditsName is the printed name of a credits status.
func fzrCreditsName(s CreditsStatus) string {
	switch s {
	case CreditsNotReported:
		return "not_reported"
	case CreditsReported:
		return "reported"
	case CreditsInvalid:
		return "invalid"
	default:
		return "CreditsStatus(" + strconv.Itoa(int(s)) + ")"
	}
}

// fzrCaptureName is the printed name of a raw capture state.
func fzrCaptureName(s RawCaptureState) string {
	switch s {
	case RawCaptureOff:
		return "off"
	case RawCaptureComplete:
		return "complete"
	case RawCaptureNoResponse:
		return "no_response"
	case RawCaptureFailed:
		return "failed"
	default:
		return "RawCaptureState(" + strconv.Itoa(int(s)) + ")"
	}
}

// fzrWantGoString renders the GoString of ce from its exported fields only.
func fzrWantGoString(ce *CallError) string {
	m := ce.Meta
	used := "nil"
	if m.Credits.Used != nil {
		used = strconv.FormatInt(*m.Credits.Used, 10)
	}
	return "&serper.CallError{Kind:" + strconv.Quote(string(ce.Kind)) + ", Field:" + strconv.Quote(ce.Field) +
		", OutcomeUnknown:" + strconv.FormatBool(ce.OutcomeUnknown) +
		", Meta:serper.Metadata{Op:" + strconv.Quote(string(m.Op)) + ", Origin:" + strconv.Quote(m.Origin) +
		", Path:" + strconv.Quote(m.Path) + ", StatusCode:" + strconv.Itoa(m.StatusCode) +
		", Attempts:" + strconv.Itoa(m.Attempts) + ", Elapsed:" + strconv.FormatInt(int64(m.Elapsed), 10) +
		", Credits:serper.Credits{Status:" + fzrCreditsName(m.Credits.Status) + ", Used:" + used + "}" +
		", RawCapture:" + fzrCaptureName(m.RawCapture) + "}}"
}

// fzrWantValidation renders the ValidationError text: the sentinel's message
// (or a fixed fallback) and each non-empty part after ": ".
func fzrWantValidation(sentinel error, op, field, reason string) string {
	var s strings.Builder
	if sentinel != nil {
		s.WriteString(sentinel.Error())
	} else {
		s.WriteString("serper: validation failed")
	}
	for _, part := range []string{op, field, reason} {
		if part != "" {
			s.WriteString(": ")
			s.WriteString(part)
		}
	}
	return s.String()
}

// fzrMatches is the documented errors.Is mapping of a CallError built from
// its exported fields, written out independently of the package's.
func fzrMatches(kind FailureKind, outcomeUnknown bool) []error {
	var want []error
	switch kind {
	case FailureUnauthorized:
		want = append(want, ErrUnauthorized)
	case FailureForbidden:
		want = append(want, ErrForbidden)
	case FailureRateLimited:
		want = append(want, ErrRateLimited)
	case FailureRejected:
		want = append(want, ErrRejected)
	case FailureUpstream:
		want = append(want, ErrUpstream)
	case FailureUnexpectedResponse:
		want = append(want, ErrUnexpectedResponse)
	case FailureMalformedResponse:
		want = append(want, ErrMalformedResponse)
	case FailureResponseTooLarge:
		want = append(want, ErrResponseTooLarge)
	case FailureTransport:
		want = append(want, ErrTransport)
	case FailureTimeout:
		want = append(want, ErrTimeout, context.DeadlineExceeded)
	case FailureCanceled:
		want = append(want, context.Canceled)
	case FailureDeadlineExceeded:
		want = append(want, context.DeadlineExceeded)
	}
	if outcomeUnknown {
		want = append(want, ErrOutcomeUnknown)
	}
	return want
}

// fzrTargets are every error a CallError may be asked about with errors.Is.
func fzrTargets() []error {
	return []error{
		ErrInvalidConfig, ErrInvalidRequest, ErrUnauthorized, ErrForbidden, ErrRateLimited, ErrRejected,
		ErrUpstream, ErrUnexpectedResponse, ErrMalformedResponse, ErrResponseTooLarge, ErrTransport, ErrTimeout,
		ErrOutcomeUnknown, context.Canceled, context.DeadlineExceeded,
	}
}

// fzrNoPanic formats v with every verb and fails t when fmt reports a panic
// in one of v's methods.
func fzrNoPanic(t *testing.T, v any) {
	t.Helper()
	for _, verb := range testutil.FormatVerbs() {
		assert.NotContains(t, fmt.Sprintf(verb, v), "PANIC=", "verb %s on %T", verb, v)
	}
}

// fzrBuilt is a CallError and a ValidationError built from fuzzed exported
// fields.
type fzrBuilt struct {
	kind, field, op, origin, path string
	status, attempts              int
	elapsed, used                 int64
	credits, capture, flags       uint8
}

// fzrCheckBuilt checks the text of errors built from arbitrary exported
// fields: Error follows the grammar, GoString prints exactly the exported
// fields, no verb panics, and errors.Is follows the documented mapping.
func fzrCheckBuilt(t *testing.T, in fzrBuilt) {
	t.Helper()
	ce := &CallError{
		Kind: FailureKind(in.kind), Field: in.field, OutcomeUnknown: in.flags&1 != 0,
		Meta: Metadata{
			Op: Operation(in.op), Origin: in.origin, Path: in.path, StatusCode: in.status, Attempts: in.attempts,
			Elapsed: time.Duration(in.elapsed), Credits: Credits{Status: CreditsStatus(in.credits)},
			RawCapture: RawCaptureState(in.capture),
		},
	}
	if in.flags&2 != 0 {
		ce.Meta.Credits.Used = &in.used
	}
	assert.Equal(t, fzrWantError(in.op, in.kind, in.status, in.field), ce.Error())
	assert.Equal(t, fzrWantGoString(ce), ce.GoString())
	assert.Equal(t, ce.GoString(), fmt.Sprintf("%#v", ce))
	fzrNoPanic(t, ce)
	require.NoError(t, ce.Unwrap(), "a CallError built from its fields has no cause")
	raw, ok := ce.RawJSON()
	assert.False(t, ok)
	assert.Nil(t, raw)
	want := fzrMatches(ce.Kind, ce.OutcomeUnknown)
	for _, target := range fzrTargets() {
		assert.Equal(t, slices.Contains(want, target), errors.Is(ce, target), "kind %q vs %v", in.kind, target)
	}

	sentinel := []error{nil, ErrInvalidConfig, ErrInvalidRequest}[int(in.flags>>2)%3]
	ve := &ValidationError{Err: sentinel, Op: Operation(in.op), Field: in.field, Reason: in.kind}
	assert.Equal(t, fzrWantValidation(sentinel, in.op, in.field, in.kind), ve.Error())
	require.ErrorIs(t, ve.Unwrap(), sentinel)
	fzrNoPanic(t, ve)
}

// fzrOp is one operation the pipeline check calls with a minimal request.
type fzrOp struct {
	op      Operation
	origin  string
	path    string
	fieldRE *regexp.Regexp                                     // the operation's Field oracle
	call    func(ctx context.Context, c *Client) (bool, error) // whether a result came back, and the error
}

// fzrOps returns the three operations, with their Field oracles compiled.
func fzrOps() []fzrOp {
	return []fzrOp{
		{
			OpSearch, fzrSearchOrigin, pathSearch, fzdSchemaOf(OpSearch).fieldRE,
			func(ctx context.Context, c *Client) (bool, error) {
				res, err := c.Search(ctx, SearchRequest{Query: fzrQuery})
				return res != nil, err
			},
		},
		{
			OpImages, fzrSearchOrigin, pathImages, fzdSchemaOf(OpImages).fieldRE,
			func(ctx context.Context, c *Client) (bool, error) {
				res, err := c.Images(ctx, ImagesRequest{Query: fzrQuery})
				return res != nil, err
			},
		},
		{
			OpScrape, fzrScrapeOrigin, pathScrape, fzdSchemaOf(OpScrape).fieldRE,
			func(ctx context.Context, c *Client) (bool, error) {
				res, err := c.Scrape(ctx, ScrapeRequest{URL: fzrTarget})
				return res != nil, err
			},
		},
	}
}

// fzrClient returns a client, with raw capture on by default, whose
// in-memory transport answers every request with HTTP 200, Content-Type
// application/json and the bytes *body points to, with their exact length.
func fzrClient(tb testing.TB, body *[]byte) *Client {
	tb.Helper()
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		testutil.Drain(r)
		b := *body
		return testutil.Response(r, http.StatusOK, io.NopCloser(bytes.NewReader(b)), int64(len(b))), nil
	})
	c, err := NewClient(fzrKey, WithHTTPClient(&http.Client{Transport: rt}), WithSearchBaseURL(fzrSearchOrigin),
		WithScrapeBaseURL(fzrScrapeOrigin), WithDefaultRawCapture())
	require.NoError(tb, err)
	return c
}

// fzrMarked returns bodies that plant the marker, followed by input, where a
// decoder could be tempted to quote it: a whole body, wrong-typed values,
// member names, attributes, credits, list items and broken JSON.
func fzrMarked(input []byte) [][]byte {
	m := fzrMarker + string(input)
	q := testutil.JSONQuote(m)
	esc := q[1 : len(q)-1]
	return [][]byte{
		[]byte(m),
		[]byte(`{"searchParameters":"` + esc + `","text":7}`),
		[]byte(`{"` + esc + `":1,"organic":[null],"images":[null],"text":null}`),
		[]byte(`{"searchParameters":{"q":"` + esc + `","num":"` + esc + `"},"text":["` + esc + `"]}`),
		[]byte(`{"knowledgeGraph":{"attributes":{"` + esc + `":7}},"organic":[{"attributes":"` + esc +
			`"}],"images":[{"title":["` + esc + `"]}],"markdown":{"` + esc + `":1},"searchParameters":{}}`),
		[]byte(`{"credits":"` + esc + `","` + esc + `":"` + esc + `"}`),
		[]byte(`{"searchParameters":{},"organic":[{"sitelinks":[{"title":"` + esc + `"},null]}],"text":"` + esc +
			`","markdown":7}`),
		[]byte(`{"searchParameters":` + m + `}`),
		[]byte(`{"text":"` + m + `"}`),
	}
}

// fzrLeaks returns every fzrWindow-byte sequence of out that also occurs in
// body but not in vocab, the text the rendering is allowed to hold. A
// rendering wholly inside vocab holds nothing else.
func fzrLeaks(out string, body []byte, vocab string) []string {
	if strings.Contains(vocab, out) {
		return nil
	}
	var leaks []string
	for i := 0; i+fzrWindow <= len(out); i++ {
		w := out[i : i+fzrWindow]
		if !strings.Contains(vocab, w) && bytes.Contains(body, []byte(w)) {
			leaks = append(leaks, w)
		}
	}
	return leaks
}

// fzrRenderings returns err as a caller might print or walk it: every fmt
// verb on it, %v, %+v and %#v on it wrapped with %w and joined with
// errors.Join, and every string reflection reaches.
func fzrRenderings(err error) string {
	var b strings.Builder
	for _, verb := range testutil.FormatVerbs() {
		_, _ = fmt.Fprintf(&b, verb, err)
		b.WriteByte('\n')
	}
	for _, e := range []error{fmt.Errorf("wrapped: %w", err), errors.Join(err, context.Canceled)} {
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			_, _ = fmt.Fprintf(&b, verb, e)
			b.WriteByte('\n')
		}
	}
	b.WriteString(strings.Join(testutil.ReflectStrings(err), "\n"))
	return b.String()
}

// fzrSeen is what one pipeline CallError shows, reduced so that one
// comparison checks it against the contract.
type fzrSeen struct {
	DecodingKind   bool // malformed_response or unexpected_response
	KnownField     bool // "" or built only from known member names
	OutcomeUnknown bool
	Op             Operation
	Origin         string
	Path           string
	Status         int
	Attempts       int
	Measured       bool // Elapsed > 0
	Credits        Credits
	RawCapture     RawCaptureState
	Captured       bool
	Raw            []byte
	Error          string
	GoString       string
}

// fzrCheckPipeline calls every operation against a server that answers
// body, and checks each CallError: the documented kinds, metadata and
// capture of an HTTP 200 body, an Error that follows the grammar with a
// Field of known member names, a GoString of exported fields only, and no
// rendering that holds input bytes beyond that fixed vocabulary or, when
// marked, the marker in any form.
func fzrCheckPipeline(t *testing.T, ops []fzrOp, c *Client, current *[]byte, body []byte, marked bool) {
	t.Helper()
	*current = body
	wellFormed := utf8.Valid(body) && json.Valid(body)
	object := wellFormed && fzdFirstByte(body) == '{'
	var members []fzdMember
	if object {
		members = fzdMembersOf(t, body)
	}
	credits := fzdCreditsFrom(members, object)
	for _, o := range ops {
		got, err := o.call(t.Context(), c)
		require.NotEqual(t, got, err != nil, "%s: a result or an error, never both or neither", o.op)
		if err == nil {
			continue
		}
		ce := testutil.RequireErrorAs[*CallError](t, err)
		m := ce.Meta
		raw, captured := ce.RawJSON()
		seen := fzrSeen{
			DecodingKind: ce.Kind == FailureMalformedResponse || ce.Kind == FailureUnexpectedResponse,
			KnownField:   ce.Field == "" || o.fieldRE.MatchString(ce.Field), OutcomeUnknown: ce.OutcomeUnknown,
			Op: m.Op, Origin: m.Origin, Path: m.Path, Status: m.StatusCode, Attempts: m.Attempts,
			Measured: m.Elapsed > 0, Credits: m.Credits, RawCapture: m.RawCapture, Captured: captured, Raw: raw,
			Error: ce.Error(), GoString: ce.GoString(),
		}
		wantErr := fzrWantError(string(o.op), string(ce.Kind), http.StatusOK, ce.Field)
		want := fzrSeen{
			DecodingKind: true, KnownField: true, OutcomeUnknown: true,
			Op: o.op, Origin: o.origin, Path: o.path, Status: http.StatusOK, Attempts: 1, Measured: true,
			Credits: credits, RawCapture: RawCaptureFailed, Error: wantErr, GoString: fzrWantGoString(ce),
		}
		if wellFormed {
			want.RawCapture, want.Captured, want.Raw = RawCaptureComplete, true, body
		}
		assert.Equal(t, want, seen, "%s on %q", o.op, body)

		vocab := strings.Join([]string{wantErr, strconv.Quote(wantErr), fzrWantGoString(ce)}, "\n")
		for _, out := range []string{
			ce.Error(), ce.GoString(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%s", err),
			fmt.Sprintf("%q", err), fmt.Sprintf("%#v", err),
		} {
			assert.Empty(t, fzrLeaks(out, body, vocab), "%s: input bytes in %q", o.op, out)
		}
		if marked {
			testutil.AssertRedacted(t, fzrRenderings(err), []string{fzrMarker}, "%s: the marker is printed", o.op)
		}
	}
}

func FuzzErrorText(f *testing.F) {
	f.Add("malformed_response", "organic[3]", "search", fzrSearchOrigin, "/search", 200, 1, int64(1500), int64(7),
		uint8(1), uint8(1), uint8(3), []byte(`{"searchParameters":{},"organic":[null],"credits":7}`))
	f.Add("", "", "", "", "", 0, 0, int64(0), int64(0), uint8(0), uint8(0), uint8(0), []byte(""))
	f.Add("timeout", "text", "scrape", "https://scrape.serper.dev", "/", -1, 2, int64(-5), int64(-1),
		uint8(9), uint8(200), uint8(6), []byte(`{"text":null,"credits":12345678901234}`))
	f.Add("transport", "\x00\n\"`", "images", "\xff", "\\", 999, 0, int64(1)<<62, int64(1)<<62,
		uint8(2), uint8(3), uint8(255), []byte("\xff{"))
	f.Add("unknown_kind", "a.b[0]", "op", "o", "p", 600, 1, int64(1), int64(0), uint8(3), uint8(4), uint8(1),
		[]byte(`{"searchParameters":{"num":"x"},"text":5,"images":[{},null]}`))
	for _, name := range []string{"search/full.json", "images/full.json", "scrape/markdown.json"} {
		f.Add("rejected", "", "search", "", "", 400, 1, int64(1), int64(0), uint8(0), uint8(0), uint8(0),
			testutil.Fixture(f, "synthetic/"+name))
	}
	ops := fzrOps()
	f.Fuzz(func(t *testing.T, kind, field, op, origin, path string, status, attempts int, elapsed, used int64,
		credits, capture, flags uint8, body []byte,
	) {
		fzrCheckBuilt(t, fzrBuilt{
			kind: kind, field: field, op: op, origin: origin, path: path, status: status, attempts: attempts,
			elapsed: elapsed, used: used, credits: credits, capture: capture, flags: flags,
		})
		var current []byte
		c := fzrClient(t, &current)
		fzrCheckPipeline(t, ops, c, &current, body, false)
		for _, b := range fzrMarked(body) {
			fzrCheckPipeline(t, ops, c, &current, b, true)
		}
	})
}
