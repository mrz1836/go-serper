package serper

import (
	"net/http"
	"time"
)

// Option configures NewClient. Options only record their argument: NewClient
// validates the result. A nil Option is skipped, and when an option is given
// more than once the last one wins. An invalid value, an explicit zero or
// empty value included, makes NewClient return a *ValidationError matching
// ErrInvalidConfig whose Field names the option, such as "WithTimeout".
type Option func(*options)

// options is the configuration record that Options fill in.
type options struct {
	httpClient        *http.Client
	httpClientSet     bool
	timeout           time.Duration
	scrapeTimeout     time.Duration
	scrapeTimeoutSet  bool
	maxRequestBytes   int64
	maxResponseBytes  int64
	userAgent         string
	searchBaseURL     string
	scrapeBaseURL     string
	defaultRawCapture bool
}

// defaultOptions returns the configuration before any Option.
func defaultOptions() options {
	return options{
		timeout:          DefaultTimeout,
		maxRequestBytes:  DefaultMaxRequestBytes,
		maxResponseBytes: DefaultMaxResponseBytes,
		userAgent:        DefaultUserAgent,
		searchBaseURL:    DefaultSearchBaseURL,
		scrapeBaseURL:    DefaultScrapeBaseURL,
	}
}

// WithHTTPClient makes calls through a copy of hc. The copy refuses redirects
// (its CheckRedirect returns http.ErrUseLastResponse), has no cookie jar, and
// wraps hc's transport, without modifying it, so that every redirect comes
// back as a plain response with its Location removed and is never followed. A
// nil transport means http.DefaultTransport. A positive hc.Timeout is folded
// into each call's deadline (the lower of it and the operation's timeout
// applies), and the copy's own Timeout is 0. hc itself is never modified or
// closed, and CloseIdleConnections never reaches its transport.
//
// The transport is trusted: it sees the API key in the X-API-KEY header and
// every request body, and it may retry on its own, which the SDK can neither
// prevent nor count (Metadata.Attempts and CallError.OutcomeUnknown describe
// the SDK's one request only). A nil transport means http.DefaultTransport,
// which, unlike the SDK's own, honors the proxy environment variables, speaks
// HTTP/2 and bounds response headers only at net/http's 10 MiB default, not
// the SDK's 64 KiB. The call's deadline relies on the transport: a response
// body must stop a blocked Read when Close is called from another goroutine,
// and a response must declare its exact ContentLength or -1; a transport that
// blocks a Read through Close holds the call past its deadline. A nil hc is
// rejected.
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) {
		o.httpClient = hc
		o.httpClientSet = true
	}
}

// WithTimeout bounds each call, including reading the whole response body.
// The default is DefaultTimeout (30 seconds); d must be greater than 0 and at
// most MaxTimeout (10 minutes). It applies to Scrape too, unless
// WithScrapeTimeout sets a budget of its own. The caller's context deadline
// applies as well: whichever comes first ends the call.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithScrapeTimeout bounds each Scrape call, including reading the whole
// response body. It defaults to the WithTimeout value; d must be greater than
// 0 and at most MaxTimeout (10 minutes).
func WithScrapeTimeout(d time.Duration) Option {
	return func(o *options) {
		o.scrapeTimeout = d
		o.scrapeTimeoutSet = true
	}
}

// WithMaxRequestBytes bounds the encoded JSON body of every request. The
// default is DefaultMaxRequestBytes (64 KiB); n must be from 1 KiB to 1 MiB.
// A larger request fails locally with ErrInvalidRequest and is never sent.
func WithMaxRequestBytes(n int64) Option {
	return func(o *options) { o.maxRequestBytes = n }
}

// WithMaxResponseBytes bounds every HTTP 200 response body, counted after any
// transport decompression, and so every raw capture. The default is
// DefaultMaxResponseBytes (8 MiB); n must be from 1 KiB to MaxResponseBytes
// (64 MiB). A larger body fails with ErrResponseTooLarge, after the provider
// may already have processed, and billed, the call.
func WithMaxResponseBytes(n int64) Option {
	return func(o *options) { o.maxResponseBytes = n }
}

// WithUserAgent sets the User-Agent header. The default is DefaultUserAgent;
// ua must be 1 to 256 bytes of visible ASCII and spaces, without a leading or
// trailing space.
func WithUserAgent(ua string) Option {
	return func(o *options) { o.userAgent = ua }
}

// WithSearchBaseURL sends Search and Images to another origin instead of
// DefaultSearchBaseURL, such as a local test server built with
// httptest.NewTLSServer, together with WithHTTPClient(srv.Client()). The SDK
// appends /search or /images.
//
// The origin receives the API key: choose it deliberately. rawURL must be an
// absolute https URL with a host and an optional port from 1 to 65535, and no
// userinfo, query (not even a bare "?"), fragment, backslash, percent-encoding
// or path other than "/". The host must be an ASCII host name (letters,
// digits, hyphens and dots: give an internationalized name in its "xn--"
// form) or a bracketed IPv6 address without a zone, so that the origin the
// Client shows is the one it dials. Errors never quote it. There is no
// plain-HTTP option.
func WithSearchBaseURL(rawURL string) Option {
	return func(o *options) { o.searchBaseURL = rawURL }
}

// WithScrapeBaseURL sends Scrape to another origin instead of
// DefaultScrapeBaseURL, under the same rules as WithSearchBaseURL. The SDK
// posts to its root path, "/".
//
// The origin receives the API key: choose it deliberately.
func WithScrapeBaseURL(rawURL string) Option {
	return func(o *options) { o.scrapeBaseURL = rawURL }
}

// WithDefaultRawCapture turns raw capture on for every call that does not
// set it with WithRawCapture. See WithRawCapture for what is kept.
func WithDefaultRawCapture() Option {
	return func(o *options) { o.defaultRawCapture = true }
}

// CallOption configures one call. It affects that call only, never the
// Client. A nil CallOption is skipped, and the last one wins.
type CallOption func(*callOptions)

// callOptions is the per-call record that CallOptions fill in.
type callOptions struct {
	rawCapture bool
}

// WithRawCapture turns raw capture on or off for one call, overriding
// WithDefaultRawCapture. With capture on, a successful response keeps the
// exact HTTP 200 body bytes the decoder read (after any transport
// decompression, never the request body or headers), reachable as a new copy
// through the response's RawJSON method, for replay storage. A complete,
// valid JSON HTTP 200 body that decoding refused is kept on the *CallError
// instead (see CallError.RawJSON). Metadata.RawCapture reports what happened.
//
// The body is bounded by WithMaxResponseBytes, so a capture is always whole,
// never a prefix. It is provider data and may echo the query.
func WithRawCapture(enabled bool) CallOption {
	return func(o *callOptions) { o.rawCapture = enabled }
}

// validate checks the options in a fixed order and returns the two endpoints'
// hosts (host[:port]).
func (o *options) validate() (searchHost, scrapeHost string, err error) {
	switch {
	case o.httpClientSet && o.httpClient == nil:
		return "", "", invalidConfig("", "WithHTTPClient", reasonNil)
	case !validTimeout(o.timeout):
		return "", "", invalidConfig("", "WithTimeout", reasonOutOfRange)
	case o.scrapeTimeoutSet && !validTimeout(o.scrapeTimeout):
		return "", "", invalidConfig("", "WithScrapeTimeout", reasonOutOfRange)
	case o.maxRequestBytes < minRequestBytesLimit || o.maxRequestBytes > maxRequestBytesLimit:
		return "", "", invalidConfig("", "WithMaxRequestBytes", reasonOutOfRange)
	case o.maxResponseBytes < minResponseBytesLimit || o.maxResponseBytes > MaxResponseBytes:
		return "", "", invalidConfig("", "WithMaxResponseBytes", reasonOutOfRange)
	}
	if reason := userAgentProblem(o.userAgent); reason != "" {
		return "", "", invalidConfig("", "WithUserAgent", reason)
	}
	searchHost, reason := originHost(o.searchBaseURL)
	if reason != "" {
		return "", "", invalidConfig("", "WithSearchBaseURL", reason)
	}
	scrapeHost, reason = originHost(o.scrapeBaseURL)
	if reason != "" {
		return "", "", invalidConfig("", "WithScrapeBaseURL", reason)
	}
	return searchHost, scrapeHost, nil
}

// callTimeout returns the timeout of a call whose operation timeout is d: the
// lower of d and an injected http.Client's positive Timeout. The SDK's copy of
// that client has no Timeout of its own, so net/http adds no timer of its own.
func (o *options) callTimeout(d time.Duration) time.Duration {
	if o.httpClientSet && o.httpClient.Timeout > 0 {
		return min(d, o.httpClient.Timeout)
	}
	return d
}

// validTimeout reports whether d is in (0, MaxTimeout].
func validTimeout(d time.Duration) bool { return d > 0 && d <= MaxTimeout }

// rawCapture returns the capture setting of one call: the last WithRawCapture
// among opts, else the Client's default.
func rawCapture(def bool, opts []CallOption) bool {
	co := callOptions{rawCapture: def}
	for _, opt := range opts {
		if opt != nil {
			opt(&co)
		}
	}
	return co.rawCapture
}
