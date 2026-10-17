<div align="center">

# 🔎&nbsp;&nbsp;go-serper

**A small, typed, context-first Go SDK for Serper: web search, image search and page scrape.**

<br/>

<a href="https://github.com/mrz1836/go-serper/releases"><img src="https://img.shields.io/github/release-pre/mrz1836/go-serper?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://go.dev/"><img src="https://img.shields.io/github/go-mod/go-version/mrz1836/go-serper?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="https://github.com/mrz1836/go-serper/blob/master/LICENSE"><img src="https://img.shields.io/github/license/mrz1836/go-serper.svg?style=flat-square&color=blue" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-serper/actions"><img src="https://img.shields.io/github/actions/workflow/status/mrz1836/go-serper/fortress.yml?branch=master&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/mrz1836/go-serper/actions"><img src="https://img.shields.io/github/last-commit/mrz1836/go-serper?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://codecov.io/gh/mrz1836/go-serper"><img src="https://codecov.io/gh/mrz1836/go-serper/branch/master/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/mrz1836/go-serper"><img src="https://api.scorecard.dev/projects/github.com/mrz1836/go-serper/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-serper/graphs/contributors"><img src="https://img.shields.io/github/contributors/mrz1836/go-serper?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://mrz1818.com/"><img src="https://img.shields.io/badge/donate-bitcoin-ff9900?style=flat-square&logo=bitcoin" alt="Bitcoin"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="20%">
       🚀&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="20%">
       🏁&nbsp;<a href="#-quick-start"><code>Quick&nbsp;Start</code></a>
    </td>
    <td align="center" width="20%">
       💡&nbsp;<a href="#-usage"><code>Usage</code></a>
    </td>
    <td align="center" width="20%">
       ⚙️&nbsp;<a href="#%EF%B8%8F-behavior"><code>Behavior</code></a>
    </td>
    <td align="center" width="20%">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
    <td align="center">
      🔧&nbsp;<a href="#-development"><code>Development</code></a>
    </td>
    <td align="center">
      🛠️&nbsp;<a href="#%EF%B8%8F-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
      🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
  </tr>
</table>
<br/>

go-serper calls three [Serper](https://serper.dev) operations from Go: web search (`POST /search`) and
image search (`POST /images`) on `https://google.serper.dev`, and page scrape (`POST /` on the separate
`https://scrape.serper.dev` origin). It is an **independent, unofficial** SDK, not affiliated with
Serper or Google. Use it when a Go service needs search results or page text and has to stay in control
of every paid call.

- **One attempt per call.** No retries, redirects, replays, hedging, pagination, batching or caching: the
  SDK never repeats a request on its own, and the standard library's transports cannot resend one either.
- **Typed results that keep the provider's data as returned.** Lists keep their order, positions are
  never renumbered, dates are never parsed, and an absent list or object stays apart from an empty one.
- **Bounded.** Every call has a deadline that covers the whole body read, and every request and response
  has a size limit.
- **Safe to log.** Error text and `Metadata` never contain the API key, queries, scrape targets, headers,
  bodies or provider messages; requests print only their shape, and a body kept by raw capture is
  reachable only through `RawJSON`.
- **Replay-ready.** Opt-in, bounded, byte-exact raw capture, and offline `Decode…` helpers that apply the
  same decoder, success rule and credits reading to a stored body.
- **Zero runtime dependencies:** the standard library only.

It is a library, not an application: there is no binary, CLI, cache, queue, rate limiter or
configuration file. No official API reference was found: the request and response shapes come from the
provider's own playground and examples, and the SDK assumes nothing beyond them (see
[evidence and open questions](#evidence-and-open-questions)).

<br/>

## 📦 Installation

**go-serper** requires a [supported release of Go](https://go.dev/doc/devel/release#policy) (Go 1.26 or
later).

```bash
go get github.com/mrz1836/go-serper
```

<br/>

### Credentials

Every call needs a Serper API key, from the Serper dashboard. `NewClient` takes it as an argument and
reads no environment variable or file: the application decides where the key comes from. The key travels
only in the `X-API-KEY` header, to the configured origins, never in a URL or body.

<br/>

## 🏁 Quick Start

With the key in `SERPER_API_KEY`, this makes one real search, which spends credits:

```go
client, err := serper.NewClient(os.Getenv("SERPER_API_KEY"))
if err != nil {
	log.Fatal(err) // a *serper.ValidationError, such as a missing key; nothing was sent
}
res, err := client.Search(context.Background(), serper.SearchRequest{Query: "golang http client"})
if err != nil {
	log.Fatal(err) // never contains the key, the query or a body
}
for _, o := range res.Organic {
	fmt.Println(o.Position, o.Title, o.Link)
}
```

To see every operation without a key, a network or any cost, run the offline demo, which plays Serper
on two local TLS servers:

```bash
go run ./examples
```

Read the [behavior](#%EF%B8%8F-behavior) section before relying on an edge case.

<br/>

## 💡 Usage

The snippets below use a `client` built as in [set up once](#set-up-once) and the caller's `ctx`.

<br/>

### Set up once

Create one `Client` and share it: it is immutable and safe for concurrent use.

```go
client, err := serper.NewClient(apiKey,
	serper.WithTimeout(20*time.Second),
	serper.WithScrapeTimeout(45*time.Second), // an explicit, longer scrape budget
	serper.WithMaxResponseBytes(4<<20),
)
if err != nil {
	fmt.Println(err) // a *serper.ValidationError; nothing was sent
	return
}
fmt.Println(client) // settings and origins, never the key
```

| Option | Default | Accepted |
|---|---|---|
| `WithTimeout` | 30 seconds (`DefaultTimeout`), every operation | more than 0, at most 10 minutes (`MaxTimeout`) |
| `WithScrapeTimeout` | the `WithTimeout` value | more than 0, at most 10 minutes |
| `WithMaxRequestBytes` | 64 KiB (`DefaultMaxRequestBytes`) | 1 KiB to 1 MiB |
| `WithMaxResponseBytes` | 8 MiB (`DefaultMaxResponseBytes`) | 1 KiB to 64 MiB (`MaxResponseBytes`) |
| `WithUserAgent` | `go-serper` (`DefaultUserAgent`) | 1 to 256 bytes of visible ASCII and spaces, no leading or trailing space |
| `WithSearchBaseURL` | `https://google.serper.dev` (`DefaultSearchBaseURL`) | any `https` origin (see [origins](#origins-and-trust-boundaries)) |
| `WithScrapeBaseURL` | `https://scrape.serper.dev` (`DefaultScrapeBaseURL`) | any `https` origin |
| `WithHTTPClient` | the SDK's own transport | any non-nil `*http.Client`, copied |
| `WithDefaultRawCapture` | off | turns raw capture on for every call |

Options only record values: `NewClient` validates them all and returns a `*ValidationError` matching
`ErrInvalidConfig` that names the option, never its value. A nil option is skipped, the last value
wins, and an explicit zero or empty value is invalid. `NewClient` makes no call. A service that is done
with its client can call `client.CloseIdleConnections()` to close the idle connections of the SDK's own
transport; it does nothing for an injected `http.Client`, whose transport the caller owns.

<br/>

### Operations and billing

| Method | Request | Effect |
|---|---|---|
| `Search` | `POST https://google.serper.dev/search` | PAID: spends credits |
| `Images` | `POST https://google.serper.dev/images` | PAID: spends credits |
| `Scrape` | `POST https://scrape.serper.dev/` | PAID: spends credits; Serper fetches the target |
| `DecodeSearchResponse`, `DecodeImagesResponse`, `DecodeScrapeResponse` | none | decodes a body you kept, offline |

Nothing is paid unless you call a method, and a repeated request is a new call that may be billed again.
When a failure leaves open whether Serper processed, and billed, the call (after a 5xx, for example, or a
timeout once the request was sent), the error matches `ErrOutcomeUnknown`; the
[error reference](#error-reference) gives the rule for every failure. The SDK does no pricing
arithmetic: the playground's credit hints (such as more credits for 100 image results or for harder
scrapes) are the provider's to define.

<br/>

### Web search

Every `SearchRequest` field is sent byte for byte: the SDK never trims, normalizes or rewrites a value,
and never sends a default of its own (it never forces `gl=us` or `hl=en`).

```go
res, err := client.Search(ctx, serper.SearchRequest{Query: `"go http client" -site:example.com`, GL: "us", Num: 10})
if err != nil {
	fmt.Println(err)
	return
}
for _, o := range res.Organic {
	fmt.Println(o.Position, o.Title, o.Link, o.Date) // provider order, values as returned
}
if used, ok := res.Meta.Credits.Value(); ok {
	fmt.Println("credits used:", used)
} // otherwise unknown, not zero
```

| Field | Sent as | Rule |
|---|---|---|
| `Query` | `q` | required: 1 or more bytes of valid UTF-8; operators pass through |
| `Location` | `location` | omitted when `""`; opaque text |
| `GL`, `HL` | `gl`, `hl` | omitted when `""` |
| `Num` | `num` | omitted when 0; at least 1 when set; the provider may return fewer |
| `Autocorrect` | `autocorrect` | omitted when nil; `new(false)` sends `false` |
| `TBS` | `tbs` | omitted when `""`; opaque, such as `qdr:w` |
| `Page` | `page` | omitted when 0; at least 1 when set; one call is one page, and the SDK never fetches the next |

Text fields must be valid UTF-8, because `encoding/json` would silently replace invalid bytes.

<br/>

### Image search

`ImagesRequest` has the same fields and rules as `SearchRequest`, as a type of its own:

```go
imgs, err := client.Images(ctx, serper.ImagesRequest{Query: "lighthouse at dusk", Autocorrect: new(false)})
if err != nil {
	fmt.Println(err)
	return
}
for _, im := range imgs.Images {
	fmt.Println(im.ImageURL, im.Link, im.Source, im.Domain)
}
```

<br/>

### Scrape a page

The target is data sent to Serper, which fetches it: the SDK never contacts it. `URL` must be valid
UTF-8 that parses as an absolute `http` or `https` URL (the scheme in any case) with a host and no
userinfo, and is sent unchanged; deciding which targets are allowed is the caller's job.

```go
page, err := client.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/post?id=7", IncludeMarkdown: true})
if err != nil {
	fmt.Println(err) // a failed scrape is never an empty success
	return
}
fmt.Println(len(page.Text))
if page.Markdown != nil {
	fmt.Println(len(*page.Markdown))
}
```

`IncludeMarkdown` is sent only when true.

<br/>

### Results

Each method returns its own result type. Its `Meta` (a `Metadata`) describes the call: operation,
origin, path, HTTP status, attempts, elapsed time, credits and raw capture state (see
[metadata](#raw-capture-states)).

| Type | Fields |
|---|---|
| `SearchResponse` | `SearchParameters`, `KnowledgeGraph`, `Organic`, `PeopleAlsoAsk`, `RelatedSearches`, `Meta` |
| `ImagesResponse` | `SearchParameters`, `Images`, `Meta` |
| `ScrapeResponse` | `Text`, `Markdown`, `Meta` |
| `SearchParameters` | the provider's echo of the request: `Query` (`q`), `Type`, `Engine`, `GL`, `HL`, `Location`, `TBS`, `Num`, `Page`, `Autocorrect` |
| `KnowledgeGraph` | `Title`, `Type`, `Website`, `ImageURL`, `Description`, `DescriptionSource`, `DescriptionLink`, `Attributes` |
| `OrganicResult` | `Title`, `Link`, `Snippet`, `Date`, `Position`, `Sitelinks` (each a `Sitelink`: `Title`, `Link`), `Attributes` |
| `PeopleAlsoAsk` | `Question`, `Snippet`, `Title`, `Link` |
| `RelatedSearch` | `Query` |
| `ImageResult` | `Title`, `ImageURL`, `ImageWidth`, `ImageHeight`, `ThumbnailURL`, `ThumbnailWidth`, `ThumbnailHeight`, `Source`, `Domain`, `Link`, `GoogleURL`, `Position` |

Values are kept as returned:

- Lists keep the provider's order: nothing is sorted, deduplicated or filled in. `Position` is the
  provider's number, never renumbered (0 when it was not returned), and `Date` is the provider's text,
  such as `3 days ago`, never parsed.
- A nil list or pointer means the member was absent or `null`; an empty, non-nil list means `[]`. A
  text or number member that was absent reads as `""` or 0, like an empty one.
- `SearchParameters` is always present on success and may echo the query.
- In an `ImageResult`, `ImageURL` is the image file, `Link` the page it appears on and `Source` the
  provider's label for its source: none is ever filled in from another.
- `Text` is always present on a Scrape success, and `""` is a valid value. `Markdown` is nil unless the
  response holds it, and is never derived from `Text`.
- `Attributes` holds an attributes object as a list of `Attribute` values (`Name`, `Value`), in the
  provider's member order with duplicates kept. `Get` returns the value of the first attribute with the
  exact name. A nil `Attributes` means the member was absent or `null`.
- Members the SDK does not type, such as `answerBox`, `topStories` or Scrape's `metadata`, are reachable
  only through a [raw capture](#raw-capture-and-replay).

Every result round-trips through `encoding/json` under the provider's member names: marshaled and
unmarshaled again, its typed members are unchanged, nil and empty lists included. `Meta` and the capture
are never marshaled; to keep the exact bytes Serper sent, use raw capture.

<br/>

### Credits

When a response reports the credits it used, `Meta.Credits` holds them. The member is undocumented, so
the SDK reads a top-level `credits` member tolerantly and never fails a call over it:

| `credits` in the body | `Credits.Status` | `Value()` |
|---|---|---|
| absent or `null` | `not_reported` | `(0, false)`: unknown, not zero |
| plain digits that fit an `int64`, such as `0` or `2` | `reported` | `(n, true)` |
| anything else: `2.0`, `2e0`, `-1`, a string, an object | `invalid` | `(0, false)` |

The statuses are `not_reported` (`CreditsNotReported`), `reported` (`CreditsReported`) and `invalid`
(`CreditsInvalid`). `Credits.Used` holds the number, and is non-nil only when it was reported. Credits
are read from every HTTP 200 body that reaches the decoder and is valid UTF-8 and one JSON object, even
one the decoder then refused, so a `*CallError`'s `Meta.Credits` reports them too; every other failure
reports `not_reported`.

<br/>

### Raw capture and replay

Raw capture keeps the exact HTTP 200 body the decoder read, for replay storage. It is off by default;
`WithRawCapture(true)` turns it on for one call and `WithDefaultRawCapture()` for every call (a per-call
`WithRawCapture(false)` turns it off again). An application might store a capture like this, together
with the input it owns:

```go
type replayRecord struct {
	CorrelationID string           `json:"correlationId"`
	Op            serper.Operation `json:"op"`
	Query         string           `json:"query"`
	Body          []byte           `json:"body"` // stored as bytes, never re-encoded
	Meta          serper.Metadata  `json:"meta"`
}
```

```go
req := serper.SearchRequest{Query: "example query"}
res, err := client.Search(ctx, req, serper.WithRawCapture(true))
if err != nil {
	fmt.Println(err)
	return
}
raw, ok := res.RawJSON() // an owned copy: the exact bytes the decoder saw
if !ok {
	return
}
rec := replayRecord{CorrelationID: "req-0001", Op: res.Meta.Op, Query: req.Query, Body: raw, Meta: res.Meta}
fmt.Println(rec.Op, rec.Meta.RawCapture, len(rec.Body))

// Later, offline: the same decoder, success rule and credits reading.
again, err := serper.DecodeSearchResponse(rec.Body)
if err != nil {
	fmt.Println(err)
	return
}
fmt.Println(len(again.Organic), again.Organic[0].Title)
```

The capture is the body after the transport's transparent gzip decoding, bounded by
`WithMaxResponseBytes`, so it is always whole, never a prefix. `RawJSON` returns a new copy on every
call. Request bodies and headers are never captured, and a capture is never printed or marshaled. Pair
the bytes with the input you own (the request, a correlation ID): the body itself may echo the query.

A valid JSON response that the decoder refused is kept on the error instead, so that a changed shape can
be stored and studied:

```go
_, err := client.Search(ctx, serper.SearchRequest{Query: "example query"}, serper.WithRawCapture(true))
fmt.Println(err)
if ce, ok := errors.AsType[*serper.CallError](err); ok {
	if raw, ok := ce.RawJSON(); ok { // schema drift: keep the response for later replay
		fmt.Println(ce.Meta.RawCapture, len(raw))
	}
}
```

<br/>

### Errors

```go
_, err := client.Search(ctx, serper.SearchRequest{Query: "example query"})
switch {
case err == nil:
	return
case errors.Is(err, context.Canceled): // the caller canceled
case errors.Is(err, serper.ErrTimeout): // the SDK's timeout (also matches context.DeadlineExceeded)
case errors.Is(err, serper.ErrRateLimited): // HTTP 429: no automatic retry; the caller decides
	fmt.Println("rate limited; outcome unknown:", errors.Is(err, serper.ErrOutcomeUnknown))
case errors.Is(err, serper.ErrResponseTooLarge): // raise WithMaxResponseBytes deliberately
}
if ce, ok := errors.AsType[*serper.CallError](err); ok {
	fmt.Printf("serper %s: kind=%s status=%d attempts=%d outcomeUnknown=%t\n",
		ce.Meta.Op, ce.Kind, ce.Meta.StatusCode, ce.Meta.Attempts, ce.OutcomeUnknown)
}
```

A local failure is a `*ValidationError` matching `ErrInvalidConfig` or `ErrInvalidRequest`: nothing was
sent. Its `Op` is the operation (empty for `NewClient`), its `Reason` a fixed word from the
[reason table](#error-reference), and its `Field` names what was refused, never its value: `apiKey`, an
option such as `WithTimeout`, `ctx`, `Client`, a request field (`Query`, `Location`, `GL`, `HL`, `TBS`,
`Num`, `Page` or `URL`), or `request` for an encoded body over `WithMaxRequestBytes`. Its text reads like
`serper: invalid request: search: Query: required`.

Every other failure is a `*CallError` with a `Kind`, a `Field` for decoding failures (a JSON location,
such as `organic[3]`), `OutcomeUnknown`, and the call's `Meta`. Its text is
`serper: <op>: <kind>[: <transport class>][ (HTTP <status>)][: <field>]`, such as
`serper: search: rate_limited (HTTP 429)`; the transport class is `dns`, `dial`, `tls`, `proxy`,
`reset`, `closed`, `stalled` or `other`. `CallError.Unwrap` returns only a context error or a fixed error
of this package, never a `*url.Error`, a net error or a JSON error, which could quote a URL, a header or
response content. See the [error reference](#error-reference).

<br/>

### Concurrency

The caller owns the concurrency bound; per-call options never touch shared configuration:

```go
queries := []string{"alpha", "beta", "alpha"} // a duplicate stays a separate call
sem := make(chan struct{}, 4)
var wg sync.WaitGroup
out, errs := make([]*serper.SearchResponse, len(queries)), make([]error, len(queries))
for i, q := range queries {
	wg.Go(func() {
		sem <- struct{}{}
		defer func() { <-sem }()
		out[i], errs[i] = client.Search(ctx, serper.SearchRequest{Query: q}, serper.WithRawCapture(i == 0))
	})
}
wg.Wait() // results are keyed by index, never by query string
```

The SDK keeps no state shared between calls and starts no background goroutine: the only SDK code that
runs off the caller's goroutine is the request body's `Read`, called by the transport's own connection
goroutines, and the `context.AfterFunc` that closes a response body on timeout or cancellation.
The transport it builds pools connections, as every `http.Transport` does. Account budgets and rate
limits are the caller's to enforce.

<br/>

### Timeouts and HTTP handlers

Each call has one deadline, the earlier of the caller's context deadline and the operation's timeout,
and it covers the whole body read:

```go
handler := func(c *serper.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		res, err := c.Search(ctx, serper.SearchRequest{Query: r.FormValue("q")})
		if err != nil {
			http.Error(w, "search unavailable", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(res.Organic)
	}
}(client)
```

`ErrTimeout` means the SDK's own timeout expired (`WithTimeout`, `WithScrapeTimeout`, or an injected
`http.Client`'s Timeout, which is folded into the call's deadline), or the network reported a timeout,
such as the SDK transport's 10-second dial or TLS handshake timeout. It also matches
`context.DeadlineExceeded`, so check it first. The caller's own deadline is `deadline_exceeded`, and a
cancellation is `canceled`.

<br/>

### Testing your application

The SDK exports no interface: own a tiny one in your application, and fake it in your tests.

```go
type webSearcher interface {
	Search(ctx context.Context, req serper.SearchRequest, opts ...serper.CallOption) (*serper.SearchResponse, error)
}

// fakeSearcher is an application's test double for webSearcher.
type fakeSearcher struct{ titles []string }

func (f fakeSearcher) Search(context.Context, serper.SearchRequest, ...serper.CallOption) (*serper.SearchResponse, error) {
	res := &serper.SearchResponse{}
	for i, t := range f.titles {
		res.Organic = append(res.Organic, serper.OrganicResult{Title: t, Position: i + 1})
	}
	return res, nil
}
```

A fake may fail with a `CallError` built from its exported fields alone, such as
`&serper.CallError{Kind: serper.FailureRateLimited}`: it matches `errors.Is` like one the SDK returns,
with no cause and no capture.

To exercise the real client offline, point it at local TLS servers built with
`httptest.NewTLSServer`, with `WithHTTPClient(srv.Client())`, `WithSearchBaseURL(srv.URL)` and
`WithScrapeBaseURL(otherSrv.URL)`; httptest servers share one certificate, so one server's client trusts
both. There is no plain-HTTP option. `DecodeSearchResponse` and its siblings decode stored bodies without
any server.

<br/>

## ⚙️ Behavior

### Exactly one attempt

Every call is one HTTP request, or none when validation fails or the context is already done. The SDK
never retries, and it disables the standard library's own resends: every request body is a plain reader
with `GetBody` nil and no `Idempotency-Key`, so neither the HTTP/1 transport (on a reused connection that
fails) nor the HTTP/2 transport (after a refused stream, a GOAWAY or a protocol error) can resend it, and
a 307 or 308 cannot be followed. Redirects are never followed: the SDK's copy of the `http.Client`
refuses them, and a guard strips the `Location` header of every 3xx, so no URL is ever quoted in an
error. `Metadata.Attempts` counts the requests the SDK issued, 0 or 1; an
[injected transport](#origins-and-trust-boundaries) may still retry on its own.

<br/>

### Origins and trust boundaries

The API key goes only to the two configured origins, in the `X-API-KEY` header. `WithSearchBaseURL` and
`WithScrapeBaseURL` accept any `https` origin, such as a local test server or a gateway you run: such an
origin receives the key, so choose it deliberately. An origin is an absolute `https` URL with a host and
an optional port from 1 to 65535, and no userinfo, query (not even a bare `?`), fragment, backslash,
percent-encoding or path other than `/`. Its host is an ASCII host name (give an internationalized name
in its `xn--` form) or a bracketed IPv6 address without a zone, so that the origin `Client.String`
shows, and never the key, is the one the transport dials. On any transport, the SDK's own included, an
`httptrace.ClientTrace` in the caller's context sees every request header, the key included.

The SDK's own transport speaks **HTTP/1.1 only**, requires TLS 1.2 or later, **never reads the proxy
environment variables** (inject an `http.Client` to use a proxy), gives up on a dial after 10 seconds
(the DNS lookup included) and on a TLS handshake after 10 seconds, uses a 30-second TCP keep-alive,
keeps at most 8 idle connections per host and 16 in all, each for 4 seconds, and bounds response
headers at 64 KiB.

An injected `http.Client` is copied, never modified or closed, and its positive `Timeout` is folded into
each call's deadline. Its transport and any TLS-intercepting proxy see the key and every body, and the
transport may retry on its own, which the SDK can neither prevent nor count: `Attempts` and
`OutcomeUnknown` describe only the SDK's one request. `CloseIdleConnections` never reaches it. A nil
`Transport` means `http.DefaultTransport`, which honors the proxy environment variables, speaks HTTP/2
and bounds response headers only at net/http's 10 MiB default. The call's deadline relies on the
transport: its response bodies must stop a blocked read when closed from another goroutine, and must
declare their exact length or -1. The standard library itself may log stray bytes
that a server sends on an idle connection.

<br/>

### Limits

| What | Limit | On failure |
|---|---|---|
| API key | 1 to 256 bytes of visible ASCII | `ErrInvalidConfig` |
| Encoded request body | 64 KiB by default (`WithMaxRequestBytes`, 1 KiB to 1 MiB) | `ErrInvalidRequest`, nothing sent |
| HTTP 200 body, after decompression | 8 MiB by default (`WithMaxResponseBytes`, 1 KiB to 64 MiB) | `ErrResponseTooLarge` |
| Buffer sized from a declared length before any byte arrives | 1 MiB | grows as bytes arrive |
| Error body read for connection reuse | 4 KiB, never kept, within the call's deadline | skipped when declared longer |
| Reads in a row that return nothing | 100 empty reads in a row | `ErrTransport` (stalled) |
| Response headers (SDK-built transport) | 64 KiB | `ErrTransport` |
| Connection setup (SDK-built transport) | 10-second dial timeout (DNS lookup included), 10-second TLS handshake timeout | `ErrTimeout` |
| Idle connections (SDK-built transport) | 8 per host, 16 in all, closed after 4 seconds idle | a new connection |
| Operation timeout (`WithTimeout`, `WithScrapeTimeout`) | 30 seconds by default, more than 0, at most 10 minutes | `ErrTimeout` |

<br/>

### Error reference

| What happened | `Kind` | `errors.Is` matches | `OutcomeUnknown` |
|---|---|---|---|
| HTTP 401 | `unauthorized` | `ErrUnauthorized` | false |
| HTTP 403 | `forbidden` | `ErrForbidden` | false |
| HTTP 429 | `rate_limited` | `ErrRateLimited` | false |
| Any other HTTP 4xx | `rejected` | `ErrRejected` | false |
| HTTP 5xx | `upstream` | `ErrUpstream` | true |
| HTTP 3xx other than 303 (never followed) | `unexpected_response` | `ErrUnexpectedResponse` | false |
| HTTP 303 (never followed), 1xx, a 2xx other than 200, below 100, 600 and above | `unexpected_response` | `ErrUnexpectedResponse` | true |
| HTTP 200 with an undecoded Content-Encoding or a Content-Type that is not JSON | `unexpected_response` | `ErrUnexpectedResponse` | true |
| HTTP 200 JSON object without a `searchParameters` object (Search and Images) | `unexpected_response` | `ErrUnexpectedResponse` | true |
| HTTP 200 body that is not valid UTF-8, not one JSON value or not an object, or has a member of the wrong type, a null list item or no `text` string (Scrape) | `malformed_response` | `ErrMalformedResponse` | true |
| HTTP 200 body over the limit | `response_too_large` | `ErrResponseTooLarge` | true |
| A DNS or dial failure or a failure to connect to a proxy, other than a timeout, or a TLS certificate refused, before the transport read the request | `transport` | `ErrTransport` | false |
| Any other transport failure that is not a timeout: another TLS failure, a proxy that refused the tunnel, a reset, a body cut short or longer than declared, a stalled body | `transport` | `ErrTransport` | true |
| The SDK's timeout, or a timeout the network reported (a dial, TLS handshake or DNS timeout) | `timeout` | `ErrTimeout`, `context.DeadlineExceeded` | true once sent |
| The caller's deadline | `deadline_exceeded` | `context.DeadlineExceeded` | true once sent |
| The caller's cancellation | `canceled` | `context.Canceled` | true once sent |

"Once sent" means once the request was handed to the transport, unless the transport never read the
request body and the failure shows that no connection was made: a DNS or dial failure or a failure to
connect to a proxy (a timeout included), or a TLS certificate refused in the handshake. Any other
timeout, such as the TLS handshake timeout, leaves the outcome unknown, and so does a call that times
out, reaches the caller's deadline or is canceled while the transport is still connecting: net/http then
reports only the context's error, which cannot show whether a connection was made. The classification errs on the side of "may be billed". A context that is
already done sends nothing.

A 303 See Other is the usual response to a POST that was processed, so it leaves the outcome unknown
too. A status other than 200 decides the outcome on its own, even when it arrives after the call's
deadline. An HTTP 200 response whose body was not read in full before the call's deadline fails as
`timeout`, or as
`deadline_exceeded` or `canceled` when the caller's context ended first. `OutcomeUnknown` is the SDK's
classification, not a billing guarantee, and it drives `errors.Is(err, ErrOutcomeUnknown)`. The status
sentinels are generic HTTP classes: which status Serper sends for a bad key or exhausted credits is not
documented. The SDK never reads Retry-After or rate-limit headers, and never keeps provider messages.

The kinds are `unauthorized` (`FailureUnauthorized`), `forbidden` (`FailureForbidden`), `rate_limited`
(`FailureRateLimited`), `rejected` (`FailureRejected`), `upstream` (`FailureUpstream`),
`unexpected_response` (`FailureUnexpectedResponse`), `malformed_response` (`FailureMalformedResponse`),
`response_too_large` (`FailureResponseTooLarge`), `transport` (`FailureTransport`), `timeout`
(`FailureTimeout`), `canceled` (`FailureCanceled`) and `deadline_exceeded` (`FailureDeadlineExceeded`).
The operations are `search` (`OpSearch`), `images` (`OpImages`) and `scrape` (`OpScrape`).

| `Reason` | Meaning |
|---|---|
| `required` | the value is missing or empty |
| `nil` | a nil context or `*http.Client` |
| `not_initialized` | a nil Client, or one not built by `NewClient` |
| `out_of_range` | a number or duration outside its bounds |
| `invalid_format` | an API key or User-Agent over its length or with bytes outside its character set, or a User-Agent with a leading or trailing space |
| `invalid_utf8` | text that is not valid UTF-8 |
| `invalid_url` / `unsupported_scheme` / `missing_host` | a URL that does not parse or is opaque, has no scheme or another one, or has no host, or a base URL with a backslash or percent-encoding |
| `has_userinfo` / `has_query` / `has_fragment` / `has_path` | a URL with a part it may not have |
| `invalid_port` | a base URL port of 0, over 65535 or with a leading zero, or a `:` with no port |
| `invalid_host` | a base URL whose host is not an ASCII host name or a bracketed IPv6 address without a zone |
| `too_large` | the encoded request body is over `WithMaxRequestBytes` |

<br/>

### Decoding and success rules

An HTTP 200 response is decoded only when it has no Content-Encoding other than `identity` (the
transport removes the header once it has decoded gzip itself, so one that remains names an encoding
nobody decoded) and either no Content-Type or exactly one that is `application/json` or a `+json` type,
such as `application/problem+json`, with or without parameters such as `charset`. Anything else is
`unexpected_response`.

The decoder checks that the body is valid UTF-8 and one JSON object, then fills the typed result with
`encoding/json`. Unknown members are ignored; member names match as `encoding/json` matches them (exactly
first, then ignoring case). A known member of the wrong JSON type fails with `Field` set to its member
path, without indexes, such as `organic.position`; a null list item fails with its index, such as
`organic[3]`, and so does an attributes member that is not an object of strings, such as
`organic[3].attributes`. Items are never dropped, sorted or renumbered.

- **Search and Images** succeed only with a `searchParameters` object: it is in every provider example
  and echoes the request, so it is there even when there are no results. `organic` and `images` are
  never required. `{}`, `{"credits":1}` and `{"message":…}` fail as `unexpected_response`, never as
  empty successes.
- **Scrape** succeeds only when `text` is a string (`""` included); without one, the response fails as
  `malformed_response`. `markdown` may be absent, `null` or a string.

`DecodeSearchResponse`, `DecodeImagesResponse` and `DecodeScrapeResponse` apply the same decoder,
success rule and credits reading to a stored body. No request is made, so their result's `Meta` names
only the operation and the credits, and their errors never match `ErrOutcomeUnknown`.

<br/>

### Raw capture states

| Outcome | `Meta.RawCapture` with capture on | Bytes |
|---|---|---|
| Success | `complete` | `resp.RawJSON()` |
| HTTP 200 read in full, valid UTF-8 and JSON, refused by decoding or the success rule | `complete` | `(*CallError).RawJSON()` |
| Any other HTTP 200 failure | `failed` | none |
| A status other than 200 | `failed` | none (error bodies are never captured) |
| No response: transport failure, timeout or cancellation before the headers, or a done context | `no_response` | none |

With capture off the state is `off`. The states are `off` (`RawCaptureOff`), `complete`
(`RawCaptureComplete`), `no_response` (`RawCaptureNoResponse`) and `failed` (`RawCaptureFailed`). A
`*ValidationError` has no metadata. `Metadata` itself (operation, origin, path, status, attempts, elapsed
time, credits, capture state) is safe to log or store, and its JSON round-trips. `Elapsed` is local
monotonic time from just before the SDK built the request until the response body was closed, or until
the `http.Client` returned an error; it is 0 exactly when `Attempts` is 0.

<br/>

### Privacy

The SDK never logs, prints, stores or caches anything, and never reads the environment. Error text,
every `fmt` verb on an error, `%w` and `errors.Join` chains, reflection walks and `Metadata` JSON never
hold the key, a query, a scrape target, a header value, a body, provider text or GOAWAY debug data:
standard-library errors (`*url.Error`, net and JSON errors) are replaced by fixed classes, never wrapped.
Requests print only their shape with every `fmt` verb, except for `%p` on a value and a request held in
an unexported struct field, which `fmt` prints without calling their methods; `encoding/json` (and any
logger that serializes with it, such as `log/slog`'s JSON handler) still writes every field. Results
hold provider data that may echo the query or carry sensitive content: printing, logging and storing
them is the caller's responsibility.

<br/>

### Evidence and open questions

No official API reference or OpenAPI document was found: serper.dev's `/docs`, `/api`, `/pricing` and
`/openapi.json` return 404, and `docs.serper.dev` could not be checked. The SDK's contract comes from the
provider's own playground request builder and its example responses, and it assumes nothing more. Still
unresolved, and therefore not modeled: the status codes and error envelope for a bad key or exhausted
credits beyond a `message` member; rate-limit, Retry-After and request-ID headers; the `credits`
member's name and type for each operation; the full Scrape response (`metadata`, `jsonld`) and its
`includeImages`, `includeLinks` and `includeVideos` options; whether the server accepts explicitly sent
defaults and `autocorrect:false`; `num` values beyond 10 and 100; whether `location` applies to Search
and Images; and batch semantics. The success rules (`searchParameters` on every Search and Images
response, `text` on every Scrape response) hold for every provider example. Loosening Search's and
Images' rule later would not break callers; `ScrapeResponse.Text` is a plain string, so a Markdown-only
Scrape response would need a breaking change.

<br/>

### What is not covered

Out of scope by design: retries, hedging, fallbacks, caching, rate limiting and the `GET` form with an
`apiKey` query parameter.

Possible later, each only with verified evidence of its contract and the owner's approval: other
Serper endpoints (videos, places, maps, reviews, news, shopping, lens, scholar, patents, autocomplete and
others), native batch requests (array bodies), pagination helpers, Retry-After, rate-limit and
request-ID metadata, Scrape's extra include options, and provider-message diagnostics. Each would be one
more method or option, without breaking existing callers; see
[adding an operation](docs/adding-an-operation.md).

<br/>

## 📚 Documentation

- [API reference](https://pkg.go.dev/github.com/mrz1836/go-serper?tab=doc) on pkg.go.dev, with the
  package examples.
- [Adding an operation](docs/adding-an-operation.md): how a new Serper operation would be added without
  breaking callers.
- [Synthetic fixtures](testdata/synthetic/README.md): the rules for the test data.
- [Serper's site](https://serper.dev). This SDK is independent and unofficial: "Serper" names the
  provider's service, and nothing here is endorsed by it.

<br/>

## 🧪 Examples & Tests

Every test is offline: local TLS servers and in-memory transports only, never Serper and never a real
key. Unit tests and examples run in [GitHub Actions](https://github.com/mrz1836/go-serper/actions) on Go
1.26.x and 1.27.x, which covers both `encoding/json` implementations, and fuzz tests on 1.26.x. View the
[configuration file](.github/workflows/fortress.yml).

- [`examples/`](examples/main.go) is a runnable demo program: `go run ./examples` walks through every
  operation against two local fake servers.
- [`example_test.go`](example_test.go) holds the package examples shown on pkg.go.dev. They run offline
  as tests, except `Example`, which makes a real call and is only compiled. The README's Go blocks are
  quoted from them and from the demo, and a test checks that they still are.

```bash
magex test        # all tests (fast)
magex test:race   # all tests with the race detector (slower)
magex bench       # the benchmarks
```

Fuzz targets cover the three decoders, credits reading, scrape-URL and base-URL validation, and error
formatting:

```bash
go test -run '^$' -fuzz '^FuzzDecodeSearch$' -fuzztime 30s .
```

Benchmarks run each operation through an in-memory transport with raw capture off and on, plus the
offline decoders, `RawJSON`, `NewClient` and request encoding. The README makes no throughput or latency
claims: measure on your own hardware.

<br/>

## 🔧 Development

Install the [MAGE-X](https://github.com/mrz1836/mage-x) build tool:

```bash
go install github.com/mrz1836/mage-x/cmd/magex@latest
magex update:install
```

```bash
magex format:fix  # format the code (gofmt, gofumpt, gci, goimports)
magex lint        # run golangci-lint
magex help        # list every command
```

The package imports only the standard library; testify is required by the tests alone. This prints only
the module itself:

```bash
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' .
```

To add an operation, follow [adding an operation](docs/adding-an-operation.md): one more method on
`Client`, with its own request and result types, and the one-attempt and no-values-in-errors rules
intact.

<details>
<summary><strong><code>Releases</code></strong></summary>
<br/>

Create and push the next version tag:

```bash
magex version:bump bump=patch branch=master push
```

A `v*` tag runs the [fortress workflow](.github/workflows/fortress.yml), whose release job publishes the
GitHub release with [goreleaser](https://github.com/goreleaser/goreleaser) once the tests and checks
pass. The release is defined in [.goreleaser.yml](.goreleaser.yml); to try it locally, install
goreleaser (`brew install goreleaser`).

</details>

<details>
<summary><strong><code>GitHub Workflows</code></strong></summary>
<br/>

All workflows are driven by modular configuration in [`.github/env/`](.github/env/README.md), with no
YAML editing required. **[View all workflows and the control center →](.github/docs/workflows.md)**

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update the Go module dependencies, then the linters and other development tools, run:

```bash
magex deps:update
magex tools:update
```

</details>

<br/>

## 🛠️ Code Standards

Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines

Read the [AI Usage & Assistant Guidelines](.github/tech-conventions/ai-compliance.md) for details on how
AI is used in this project and how to interact with the AI assistants.

<br/>

## 👥 Maintainers

| [<img src="https://github.com/mrz1836.png" height="50" alt="MrZ" />](https://github.com/mrz1836) |
|:------------------------------------------------------------------------------------------------:|
|                                [MrZ](https://github.com/mrz1836)                                 |

<br/>

## 🤝 Contributing

View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow the
[code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?

All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to raise issues :speech_balloon:.
You can also support this project by [becoming a sponsor on GitHub](https://github.com/sponsors/mrz1836) :clap:
or by making a [**bitcoin donation**](https://mrz1818.com/?tab=tips&utm_source=github&utm_medium=sponsor-link&utm_campaign=go-serper&utm_term=go-serper&utm_content=go-serper) to ensure this journey continues indefinitely! :rocket:

[![Stars](https://img.shields.io/github/stars/mrz1836/go-serper?label=Please%20like%20us&style=social)](https://github.com/mrz1836/go-serper/stargazers)

<br/>

## 📝 License

go-serper is released under the [MIT License](LICENSE).

[![License](https://img.shields.io/github/license/mrz1836/go-serper.svg?style=flat&v=1)](LICENSE)
