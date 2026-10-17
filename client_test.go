package serper_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// defaultClientString is what String prints for a Client with every default.
const defaultClientString = "serper.Client{searchOrigin: https://google.serper.dev, " +
	"scrapeOrigin: https://scrape.serper.dev, timeout: 30s, scrapeTimeout: 30s, maxRequestBytes: 65536, " +
	"maxResponseBytes: 8388608, userAgent: \"go-serper\", defaultRawCapture: false, httpClient: default}"

func TestNewClientDefaults(t *testing.T) {
	c := newClient(t)
	assert.Equal(t, defaultClientString, c.String())
	assert.Equal(t, defaultClientString, c.GoString())
	assert.Equal(t, defaultClientString, fmt.Sprintf("%v", c))
	assert.Equal(t, defaultClientString, fmt.Sprintf("%#v", c))
}

func TestDefaultConstants(t *testing.T) {
	assert.Equal(t, "https://google.serper.dev", serper.DefaultSearchBaseURL)
	assert.Equal(t, "https://scrape.serper.dev", serper.DefaultScrapeBaseURL)
	assert.Equal(t, 30*time.Second, serper.DefaultTimeout)
	assert.Equal(t, 10*time.Minute, serper.MaxTimeout)
	assert.Equal(t, 64<<10, serper.DefaultMaxRequestBytes)
	assert.Equal(t, 8<<20, serper.DefaultMaxResponseBytes)
	assert.Equal(t, 64<<20, serper.MaxResponseBytes)
	assert.Equal(t, "go-serper", serper.DefaultUserAgent)
}

func TestNewClientKey(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		reason string
	}{
		{name: "empty", key: "", reason: reasonRequired},
		{name: "space", key: "abc def", reason: reasonInvalidFormat},
		{name: "leading space", key: " abc", reason: reasonInvalidFormat},
		{name: "trailing newline", key: "abc\n", reason: reasonInvalidFormat},
		{name: "tab", key: "abc\tdef", reason: reasonInvalidFormat},
		{name: "DEL", key: "abc\x7f", reason: reasonInvalidFormat},
		{name: "non-ASCII", key: "abcé", reason: reasonInvalidFormat},
		{name: "too long", key: strings.Repeat("k", 257), reason: reasonInvalidFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := serper.NewClient(tt.key)
			assert.Nil(t, c)
			assertValidation(t, err, serper.ErrInvalidConfig, "", "apiKey", tt.reason)
		})
	}
	for _, key := range []string{"k", strings.Repeat("k", 256), "!~#$%&'*+-.^_`|0123456789"} {
		_, err := serper.NewClient(key)
		require.NoError(t, err, "a key of %d bytes", len(key))
	}
}

func TestNewClientOptionBounds(t *testing.T) {
	tests := []struct {
		name   string
		opt    serper.Option
		field  string
		reason string
	}{
		{name: "nil http client", opt: serper.WithHTTPClient(nil), field: "WithHTTPClient", reason: reasonNil},
		{name: "zero timeout", opt: serper.WithTimeout(0), field: "WithTimeout", reason: reasonOutOfRange},
		{name: "negative timeout", opt: serper.WithTimeout(-time.Second), field: "WithTimeout", reason: reasonOutOfRange},
		{
			name: "timeout over max", opt: serper.WithTimeout(serper.MaxTimeout + 1),
			field: "WithTimeout", reason: reasonOutOfRange,
		},
		{name: "zero scrape timeout", opt: serper.WithScrapeTimeout(0), field: "WithScrapeTimeout", reason: reasonOutOfRange},
		{
			name: "scrape timeout over max", opt: serper.WithScrapeTimeout(serper.MaxTimeout + 1),
			field: "WithScrapeTimeout", reason: reasonOutOfRange,
		},
		{name: "zero request bytes", opt: serper.WithMaxRequestBytes(0), field: "WithMaxRequestBytes", reason: reasonOutOfRange},
		{
			name: "request bytes under min", opt: serper.WithMaxRequestBytes(1<<10 - 1),
			field: "WithMaxRequestBytes", reason: reasonOutOfRange,
		},
		{
			name: "request bytes over max", opt: serper.WithMaxRequestBytes(1<<20 + 1),
			field: "WithMaxRequestBytes", reason: reasonOutOfRange,
		},
		{name: "zero response bytes", opt: serper.WithMaxResponseBytes(0), field: "WithMaxResponseBytes", reason: reasonOutOfRange},
		{
			name: "response bytes under min", opt: serper.WithMaxResponseBytes(1<<10 - 1),
			field: "WithMaxResponseBytes", reason: reasonOutOfRange,
		},
		{
			name: "response bytes over max", opt: serper.WithMaxResponseBytes(serper.MaxResponseBytes + 1),
			field: "WithMaxResponseBytes", reason: reasonOutOfRange,
		},
		{name: "empty user agent", opt: serper.WithUserAgent(""), field: "WithUserAgent", reason: reasonRequired},
		{name: "user agent leading space", opt: serper.WithUserAgent(" ua"), field: "WithUserAgent", reason: reasonInvalidFormat},
		{name: "user agent trailing space", opt: serper.WithUserAgent("ua "), field: "WithUserAgent", reason: reasonInvalidFormat},
		{name: "user agent newline", opt: serper.WithUserAgent("ua\r\nX: y"), field: "WithUserAgent", reason: reasonInvalidFormat},
		{name: "user agent non-ASCII", opt: serper.WithUserAgent("uä"), field: "WithUserAgent", reason: reasonInvalidFormat},
		{
			name: "user agent too long", opt: serper.WithUserAgent(strings.Repeat("u", 257)),
			field: "WithUserAgent", reason: reasonInvalidFormat,
		},
		{name: "empty search base", opt: serper.WithSearchBaseURL(""), field: "WithSearchBaseURL", reason: reasonRequired},
		{name: "empty scrape base", opt: serper.WithScrapeBaseURL(""), field: "WithScrapeBaseURL", reason: reasonRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := serper.NewClient(testKey, tt.opt)
			assert.Nil(t, c)
			assertValidation(t, err, serper.ErrInvalidConfig, "", tt.field, tt.reason)
		})
	}
}

func TestNewClientOptionLimitsAccepted(t *testing.T) {
	for _, opt := range []serper.Option{
		serper.WithTimeout(time.Nanosecond), serper.WithTimeout(serper.MaxTimeout),
		serper.WithScrapeTimeout(time.Nanosecond), serper.WithScrapeTimeout(serper.MaxTimeout),
		serper.WithMaxRequestBytes(1 << 10), serper.WithMaxRequestBytes(1 << 20),
		serper.WithMaxResponseBytes(1 << 10), serper.WithMaxResponseBytes(serper.MaxResponseBytes),
		serper.WithUserAgent("u"), serper.WithUserAgent(strings.Repeat("u", 256)),
		serper.WithUserAgent("my-app/1.2 (contact: ops@example.com) go-serper"),
	} {
		_, err := serper.NewClient(testKey, opt)
		require.NoError(t, err)
	}
}

func TestNewClientBaseURLs(t *testing.T) {
	tests := []struct {
		raw    string
		reason string
	}{
		{raw: "http://example.com", reason: reasonUnsupportedScheme},
		{raw: "ftp://example.com", reason: reasonUnsupportedScheme},
		{raw: "example.com", reason: reasonUnsupportedScheme},
		{raw: "//example.com", reason: reasonUnsupportedScheme},
		{raw: "https:example.com", reason: reasonInvalidURL},
		{raw: "https://user@example.com", reason: reasonHasUserinfo},
		{raw: "https://user:pass@example.com", reason: reasonHasUserinfo}, //nolint:gosec // a synthetic userinfo to refuse
		{raw: "https://example.com?x=1", reason: reasonHasQuery},
		{raw: "https://example.com?", reason: reasonHasQuery},
		{raw: "https://example.com/#", reason: reasonHasFragment},
		{raw: "https://example.com#frag", reason: reasonHasFragment},
		{raw: "https://example.com\\evil", reason: reasonInvalidURL},
		{raw: "https://example.com/v1", reason: reasonHasPath},
		{raw: "https://example.com//", reason: reasonHasPath},
		{raw: "https://example.com/%2F", reason: reasonInvalidURL},
		{raw: "https://google.serper.dev%E3%80%82evil.com", reason: reasonInvalidURL},
		{raw: "https://google.serper.dev\u3002evil.com", reason: reasonInvalidHost},
		{raw: "https://google.serper.dev\uff0eevil.com", reason: reasonInvalidHost},
		{raw: "https://b\u00fccher.example", reason: reasonInvalidHost},
		{raw: "https://a.com;b.com", reason: reasonInvalidHost},
		{raw: "https://a.com<b>", reason: reasonInvalidHost},
		{raw: "https://a.com\"x", reason: reasonInvalidHost},
		{raw: "https://a_b.example.com", reason: reasonInvalidHost},
		{raw: "https://a..example.com", reason: reasonInvalidHost},
		{raw: "https://.example.com", reason: reasonInvalidHost},
		{raw: "https://example.com.", reason: reasonInvalidHost},
		{raw: "https://-a.example.com", reason: reasonInvalidHost},
		{raw: "https://a-.example.com", reason: reasonInvalidHost},
		{raw: "https://[fe80::1%25en0]", reason: reasonInvalidURL},
		{raw: "https://[127.0.0.1]", reason: reasonInvalidURL},
		{raw: "https://" + strings.Repeat("a", 64) + ".example.com", reason: reasonInvalidHost},
		{raw: "https://" + strings.Repeat("abcdefgh.", 28) + "example.com", reason: reasonInvalidHost},
		{raw: "https://", reason: reasonMissingHost},
		{raw: "https://:443", reason: reasonMissingHost},
		{raw: "https://example.com:", reason: reasonInvalidPort},
		{raw: "https://example.com:0", reason: reasonInvalidPort},
		{raw: "https://example.com:65536", reason: reasonInvalidPort},
		{raw: "https://example.com:0443", reason: reasonInvalidPort},
		{raw: "https://exa mple.com", reason: reasonInvalidURL},
		{raw: "https://example.com\n", reason: reasonInvalidURL},
		{raw: " https://example.com", reason: reasonInvalidURL},
		{raw: "https://[::1", reason: reasonInvalidURL},
	}
	for i, tt := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", tt.raw)
			for _, o := range []struct {
				field string
				opt   func(string) serper.Option
			}{
				{"WithSearchBaseURL", serper.WithSearchBaseURL},
				{"WithScrapeBaseURL", serper.WithScrapeBaseURL},
			} {
				c, err := serper.NewClient(testKey, o.opt(tt.raw))
				assert.Nil(t, c)
				assertValidation(t, err, serper.ErrInvalidConfig, "", o.field, tt.reason)
				if strings.TrimSpace(tt.raw) != "" {
					assert.NotContains(t, err.Error(), strings.TrimSpace(tt.raw), "the URL is never quoted")
				}
			}
		})
	}
}

func TestNewClientBaseURLsAccepted(t *testing.T) {
	tests := map[string]string{
		"https://example.com":                                 "https://example.com",
		"https://example.com/":                                "https://example.com",
		"HTTPS://example.com":                                 "https://example.com",
		"https://example.com:8443":                            "https://example.com:8443",
		"https://127.0.0.1:1":                                 "https://127.0.0.1:1",
		"https://[::1]:65535":                                 "https://[::1]:65535",
		"https://[::ffff:127.0.0.1]:8443":                     "https://[::ffff:127.0.0.1]:8443",
		"https://Search-1.Example.ORG":                        "https://Search-1.Example.ORG",
		"https://" + strings.Repeat("a", 63) + ".example.com": "https://" + strings.Repeat("a", 63) + ".example.com",
		"https://search.example.org":                          "https://search.example.org",
		"https://xn--bcher-kva.example/":                      "https://xn--bcher-kva.example",
	}
	for raw, origin := range tests {
		c := newClient(t, serper.WithSearchBaseURL(raw), serper.WithScrapeBaseURL(raw))
		assert.Contains(t, c.String(), "searchOrigin: "+origin+",", raw)
		assert.Contains(t, c.String(), "scrapeOrigin: "+origin+",", raw)
	}
}

func TestNewClientOptionsLastWinsAndNilSkipped(t *testing.T) {
	c := newClient(t,
		nil,
		serper.WithTimeout(time.Minute),
		serper.WithTimeout(2*time.Second),
		serper.WithMaxRequestBytes(2048),
		serper.WithMaxResponseBytes(4096),
		serper.WithUserAgent("first"),
		serper.WithUserAgent("app/1.0"),
		serper.WithSearchBaseURL("https://a.example.com"),
		serper.WithSearchBaseURL("https://b.example.com"),
		serper.WithScrapeBaseURL("https://c.example.com"),
		serper.WithDefaultRawCapture(),
		serper.WithHTTPClient(&http.Client{}),
		nil,
	)
	assert.Equal(t, "serper.Client{searchOrigin: https://b.example.com, scrapeOrigin: https://c.example.com, "+
		"timeout: 2s, scrapeTimeout: 2s, maxRequestBytes: 2048, maxResponseBytes: 4096, userAgent: \"app/1.0\", "+
		"defaultRawCapture: true, httpClient: injected}", c.String())

	c = newClient(t, serper.WithScrapeTimeout(time.Minute), serper.WithTimeout(time.Second))
	assert.Contains(t, c.String(), "timeout: 1s, scrapeTimeout: 1m0s,", "the scrape timeout is its own")

	c = newClient(t, serper.WithHTTPClient(&http.Client{Timeout: 2 * time.Second}), serper.WithScrapeTimeout(time.Minute))
	assert.Contains(t, c.String(), "timeout: 2s, scrapeTimeout: 2s,", "the effective deadlines")

	// A later valid value does not rescue an earlier invalid one of another
	// option, and an invalid value given last wins over a valid one.
	_, err := serper.NewClient(testKey, serper.WithTimeout(time.Second), serper.WithTimeout(0))
	assertValidation(t, err, serper.ErrInvalidConfig, "", "WithTimeout", reasonOutOfRange)
}

func TestNewClientValidationOrder(t *testing.T) {
	_, err := serper.NewClient("", serper.WithTimeout(0))
	assertValidation(t, err, serper.ErrInvalidConfig, "", "apiKey", reasonRequired)

	_, err = serper.NewClient(testKey, serper.WithScrapeBaseURL(""), serper.WithSearchBaseURL(""),
		serper.WithUserAgent(""), serper.WithMaxResponseBytes(0), serper.WithMaxRequestBytes(0),
		serper.WithScrapeTimeout(0), serper.WithTimeout(0), serper.WithHTTPClient(nil))
	assertValidation(t, err, serper.ErrInvalidConfig, "", "WithHTTPClient", reasonNil)

	steps := []struct {
		field string
		fix   serper.Option
	}{
		{"WithTimeout", serper.WithHTTPClient(&http.Client{})},
		{"WithScrapeTimeout", serper.WithTimeout(time.Second)},
		{"WithMaxRequestBytes", serper.WithScrapeTimeout(time.Second)},
		{"WithMaxResponseBytes", serper.WithMaxRequestBytes(2048)},
		{"WithUserAgent", serper.WithMaxResponseBytes(2048)},
		{"WithSearchBaseURL", serper.WithUserAgent("ua")},
		{"WithScrapeBaseURL", serper.WithSearchBaseURL("https://example.com")},
	}
	opts := make([]serper.Option, 0, 8+len(steps))
	opts = append(opts,
		serper.WithHTTPClient(nil), serper.WithTimeout(0), serper.WithScrapeTimeout(0),
		serper.WithMaxRequestBytes(0), serper.WithMaxResponseBytes(0), serper.WithUserAgent(""),
		serper.WithSearchBaseURL(""), serper.WithScrapeBaseURL(""),
	)
	for _, s := range steps {
		opts = append(opts, s.fix)
		_, err = serper.NewClient(testKey, opts...)
		require.Error(t, err)
		assert.Equal(t, s.field, validationError(t, err).Field, "options are checked in a fixed order")
	}
}

func TestNilAndZeroClient(t *testing.T) {
	var nilClient *serper.Client
	assert.Equal(t, "serper.Client(nil)", nilClient.String())
	assert.Equal(t, "serper.Client(nil)", nilClient.GoString())
	assert.Equal(t, "serper.Client(not initialized)", (&serper.Client{}).String())
	assert.NotPanics(t, nilClient.CloseIdleConnections)
	assert.NotPanics(t, (&serper.Client{}).CloseIdleConnections)

	for _, c := range []*serper.Client{nil, {}} {
		for _, o := range opCases() {
			got := o.call(t.Context(), c)
			assertValidation(t, got.err, serper.ErrInvalidConfig, o.op, "Client", reasonNotInitialized)
		}
	}
}

func TestNilContext(t *testing.T) {
	c := newOfflineClient(t)
	var nilCtx context.Context
	for _, o := range opCases() {
		got := o.call(nilCtx, c)
		assertValidation(t, got.err, serper.ErrInvalidRequest, o.op, "ctx", reasonNil)

		// A nil context is checked before the Client.
		got = o.call(nilCtx, nil)
		assertValidation(t, got.err, serper.ErrInvalidRequest, o.op, "ctx", reasonNil)
	}
}

func TestWithHTTPClientIsNeverModified(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	check := func(*http.Request, []*http.Request) error { return nil }
	rt := testutil.NewScript(testutil.Step{
		Body:   searchOK,
		Header: http.Header{"Content-Type": {mediaJSON}, "Set-Cookie": {"session=cookiemarker; Path=/"}},
	})
	hc := &http.Client{Transport: rt, CheckRedirect: check, Jar: jar, Timeout: time.Minute}
	before := *hc

	c := newHTTPClientClient(t, testKey, hc)
	got := opCaseNamed("Search").call(t.Context(), c)
	require.NoError(t, got.err)
	c.CloseIdleConnections()

	assert.Same(t, before.Transport, hc.Transport)
	assert.Same(t, before.Jar, hc.Jar)
	assert.Equal(t, before.Timeout, hc.Timeout)
	assert.Equal(t, fmt.Sprintf("%p", before.CheckRedirect), fmt.Sprintf("%p", hc.CheckRedirect))

	u := testutil.OnlyRequest(t, rt)
	assert.Empty(t, u.Header.Values("Cookie"))
	cookies := jar.Cookies(mustURL(t, searchOrigin))
	assert.Empty(t, cookies, "Set-Cookie never reaches the caller's jar")
}

func TestCloseIdleConnectionsNeverReachesAnInjectedTransport(t *testing.T) {
	ct := &idleCounter{RoundTripper: testutil.StaticTransport(http.StatusOK, searchOK)}
	c := newTransportClient(t, ct)
	got := opCaseNamed("Search").call(t.Context(), c)
	require.NoError(t, got.err)
	c.CloseIdleConnections()
	assert.Zero(t, ct.closes.Load(), "the caller owns the injected transport")
}

func TestCloseIdleConnectionsOnTheDefaultTransport(t *testing.T) {
	c := newClient(t)
	assert.NotPanics(t, c.CloseIdleConnections)
	assert.NotPanics(t, c.CloseIdleConnections, "it may be called again")
}

func TestClientPrintsNoKey(t *testing.T) {
	c := newClient(t, serper.WithHTTPClient(&http.Client{}))
	testutil.AssertShapesNeverPrint(t, c, testKey)
	testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(c), "\n"), []string{testKey}, "reflection")
}
