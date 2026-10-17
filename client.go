package serper

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Defaults and limits. They are SDK choices, not provider guarantees.
const (
	// DefaultSearchBaseURL is the origin of Search and Images.
	DefaultSearchBaseURL = "https://google.serper.dev"

	// DefaultScrapeBaseURL is the origin of Scrape, a separate service.
	DefaultScrapeBaseURL = "https://scrape.serper.dev"

	// DefaultTimeout bounds each call, including reading the whole response
	// body, for every operation (Scrape too, unless WithScrapeTimeout sets
	// its own budget).
	DefaultTimeout = 30 * time.Second

	// MaxTimeout is the largest WithTimeout and WithScrapeTimeout accept.
	MaxTimeout = 10 * time.Minute

	// DefaultMaxRequestBytes bounds the encoded JSON body of every request.
	// WithMaxRequestBytes accepts 1 KiB to 1 MiB.
	DefaultMaxRequestBytes = 64 << 10

	// DefaultMaxResponseBytes bounds every HTTP 200 response body, counted
	// after transport decompression, and so every raw capture.
	DefaultMaxResponseBytes = 8 << 20

	// MaxResponseBytes is the largest WithMaxResponseBytes accepts.
	MaxResponseBytes = 64 << 20

	// DefaultUserAgent is the User-Agent header of every call unless
	// WithUserAgent sets another.
	DefaultUserAgent = "go-serper"
)

// Internal bounds.
const (
	minRequestBytesLimit  = 1 << 10
	maxRequestBytesLimit  = 1 << 20
	minResponseBytesLimit = 1 << 10
	maxKeyBytes           = 256
	maxUserAgentBytes     = 256
	maxHostBytes          = 253
	maxLabelBytes         = 63
)

// Request paths.
const (
	pathSearch = "/search"
	pathImages = "/images"
	pathScrape = "/"
)

// Client calls Serper's Search, Images and Scrape operations. Create it with
// NewClient; the zero Client and a nil *Client return ErrInvalidConfig
// without any I/O. A Client is immutable and safe for concurrent use.
//
// Its fields are unexported and the API key is held only inside a function
// value, so formatting a Client, or a struct holding one, with any fmt verb or
// a reflection-based dumper never prints it.
type Client struct {
	send      sender
	closeIdle func() // nil when the http.Client was injected

	search endpoint
	images endpoint
	scrape endpoint

	maxRequestBytes   int64
	maxResponseBytes  int64
	userAgent         string
	defaultRawCapture bool
}

// endpoint is where one operation goes: its origin, its fixed path and the
// deadline of each call.
type endpoint struct {
	op      Operation
	host    string // host[:port]
	origin  string // "https://" + host
	path    string
	timeout time.Duration
}

// NewClient returns a Client that authenticates with a Serper API key.
//
// Parameters:
//   - apiKey: the key from the Serper dashboard, sent as the X-API-KEY header
//     on every request, never in a URL or body
//   - opts: optional settings; see the With functions
//
// The key must be 1 to 256 bytes of visible ASCII (0x21 to 0x7E); it is never
// trimmed. The application decides where the key comes from: NewClient reads
// no environment variables or files and performs no I/O.
//
// Errors are *ValidationError values matching ErrInvalidConfig. They name the
// argument or option, never its value.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if err := checkKey(apiKey); err != nil {
		return nil, err
	}
	o := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	searchHost, scrapeHost, err := o.validate()
	if err != nil {
		return nil, err
	}
	hc, closeIdle := o.client()
	scrapeTimeout := o.timeout
	if o.scrapeTimeoutSet {
		scrapeTimeout = o.scrapeTimeout
	}
	return &Client{
		send:              newSender(hc, apiKey, o.userAgent),
		closeIdle:         closeIdle,
		search:            newEndpoint(OpSearch, searchHost, pathSearch, o.callTimeout(o.timeout)),
		images:            newEndpoint(OpImages, searchHost, pathImages, o.callTimeout(o.timeout)),
		scrape:            newEndpoint(OpScrape, scrapeHost, pathScrape, o.callTimeout(scrapeTimeout)),
		maxRequestBytes:   o.maxRequestBytes,
		maxResponseBytes:  o.maxResponseBytes,
		userAgent:         o.userAgent,
		defaultRawCapture: o.defaultRawCapture,
	}, nil
}

// newEndpoint returns the endpoint of op on host.
func newEndpoint(op Operation, host, path string, timeout time.Duration) endpoint {
	return endpoint{op: op, host: host, origin: "https://" + host, path: path, timeout: timeout}
}

// CloseIdleConnections closes the idle connections of the transport the SDK
// built itself. It does nothing for a Client built with WithHTTPClient: the
// caller owns that transport. It is safe on a nil or zero Client.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.closeIdle != nil {
		c.closeIdle()
	}
}

// String describes the Client's settings and origins, such as
// "serper.Client{searchOrigin: https://google.serper.dev, ...}". Its timeout
// and scrapeTimeout are the effective deadlines of each call: the lower of
// the option and an injected http.Client's Timeout. It never includes the API
// key.
func (c *Client) String() string {
	if c == nil {
		return "serper.Client(nil)"
	}
	if c.send == nil {
		return "serper.Client(not initialized)"
	}
	httpClient := "default"
	if c.closeIdle == nil {
		httpClient = "injected"
	}
	return describe("serper.Client",
		field{"searchOrigin", c.search.origin},
		field{"scrapeOrigin", c.scrape.origin},
		field{"timeout", c.search.timeout.String()},
		field{"scrapeTimeout", c.scrape.timeout.String()},
		field{"maxRequestBytes", strconv.FormatInt(c.maxRequestBytes, 10)},
		field{"maxResponseBytes", strconv.FormatInt(c.maxResponseBytes, 10)},
		field{"userAgent", strconv.Quote(c.userAgent)},
		field{"defaultRawCapture", strconv.FormatBool(c.defaultRawCapture)},
		field{"httpClient", httpClient},
	)
}

// GoString returns the same text as String, so that %#v prints settings only.
func (c *Client) GoString() string { return c.String() }

// field is one "name: value" pair of a description.
type field struct{ name, value string }

// describe prints a value's description as "typ{name: value, ...}".
func describe(typ string, fields ...field) string {
	n := len(typ) + 2
	for _, f := range fields {
		n += len(f.name) + len(f.value) + 4
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteString(typ)
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(f.name)
		b.WriteString(": ")
		b.WriteString(f.value)
	}
	b.WriteByte('}')
	return b.String()
}

// client returns the HTTP client calls use, and the function that closes the
// idle connections of the transport the SDK built (nil for an injected
// client): a copy of the injected client, or a new one on a fresh transport,
// that never follows a redirect, keeps no cookies and has no Timeout of its
// own.
func (o *options) client() (*http.Client, func()) {
	var c http.Client
	var closeIdle func()
	if o.httpClientSet {
		c = *o.httpClient
	} else {
		t := newTransport()
		c.Transport = t
		closeIdle = t.CloseIdleConnections
	}
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = redirectGuard{next: next}
	c.CheckRedirect = refuseRedirect
	c.Jar = nil
	c.Timeout = 0
	return &c, closeIdle
}
