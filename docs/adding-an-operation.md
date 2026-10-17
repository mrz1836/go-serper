# Adding an operation

go-serper implements three Serper operations: web search (`POST /search`) and image search
(`POST /images`) on `https://google.serper.dev`, and page scrape (`POST /`) on the separate
`https://scrape.serper.dev` origin, plus an offline `Decode*` helper for each. This note describes how a
future operation, such as one of those the [README](../README.md#what-is-not-covered) lists as not covered,
would be added without breaking existing callers. Each one needs the owner's approval first, and verified
evidence of its contract: no official API reference was found, so every request member, response member
and origin must be backed by the provider's own playground or examples.

## The pattern

An operation is **one new file**, such as `news.go`, holding **one method on `*Client`** with **its own
request and result types**, named after the method, plus a few lines in [`client.go`](../client.go) and
[`metadata.go`](../metadata.go). There is no registry, generic executor, service object, subpackage, or
central switch over result variants.

```go
// News would run one news search: POST /news on the search origin (hypothetical).
func (c *Client) News(ctx context.Context, req NewsRequest, opts ...CallOption) (*NewsResponse, error)
```

### The operation's file

[`scrape.go`](../scrape.go) is the smallest complete example; [`search.go`](../search.go) shows a request
shared by two operations. The file holds, in this order:

1. **The request type**: a plain struct passed by value, with no JSON tags, read only while it is
   encoded. Two operations that take the same fields share one field list, as `SearchRequest` and
   `ImagesRequest` share `queryRequest`.
2. **The wire struct**: unexported, with the provider's member names and member order, such as
   `scrapeWire` or `queryWire`. Values are sent byte for byte; zero values are omitted, a value the caller
   set is never dropped (even one the provider's builder treats as a default), and the SDK never sends a
   default of its own. Its `textBytes()` method, on the pointer, returns the summed length of **every**
   string member: that makes it a `wireBody` (see [`request.go`](../request.go)), and `encodeBody` uses it
   to refuse an oversized request before encoding it.
3. **The request's methods**:
   - `String`, `GoString` and `Format` print only the request's shape, built with `describe` and the
     `setOrUnset`, `bytesOrUnset`, `intOrUnset` and `boolOrUnset` helpers, with `formatAs` for `Format`.
   - `wire()` validates the request and returns a pointer to the wire struct. Each failure is
     `invalidRequest(op, field, reason)`, a `*ValidationError` naming the Go field and a fixed reason,
     never the value. Every text field goes through `textProblem` (see [`validate.go`](../validate.go)),
     or a check that includes it, as `scrapeURLProblem` does. `encoding/json` would replace invalid UTF-8
     with U+FFFD, so the value would no longer be sent byte for byte, and `marshalNoEscape` relies on the
     check. Numbers get their range checks here too.
4. **The response type**: JSON tags in the provider's names (`omitzero` on optional members, so that `[]`
   stays apart from absent), a `Meta Metadata` field tagged `json:"-"`, an unexported `raw rawBody` field,
   a nil-safe `RawJSON` method, and a `finish(meta Metadata, raw rawBody)` method that sets both, which
   makes it a `result`. Its item types follow it.
5. **The method**: `c.prepare(ctx, op)` (nil context, then an uninitialized Client), then `req.wire()`,
   then `call(ctx, c, &c.<endpoint>, w, opts, decode<Op>)`. `call` encodes the body within
   `WithMaxRequestBytes`, makes the one attempt within the endpoint's timeout, applies the status table,
   the content checks and the response limit, decodes, fills `Metadata`, and keeps the raw capture;
   nothing in it needs to change.
6. **The offline helper**: `Decode<Op>Response(data []byte)`, which returns
   `decodeOffline(op, data, decode<Op>)`.
7. **The mirror**: an unexported struct for decoding, with pointer list items (so that a null item is
   caught and reported as `<list>[i]` by `values` or `nullItem`), `attributesMirror` for attribute
   objects, `*string` where absent must stay apart from `""`, and `Credits json.RawMessage` for the
   top-level credits member.
8. **The decoder**: `decode<Op>(data []byte) (*<Op>Response, Credits, *decodeError)`. It runs
   `decodeObject` (valid UTF-8, one JSON object, wrong-type members reported at their member path), then
   `creditsOf` (before returning any failure, so a refused body still reports its credits), then the
   null-item checks, then the operation's **success rule**: the member whose presence proves the response
   is a success. `{}`, `{"credits":1}` and `{"message":…}` must never decode as a success. A missing
   success member fails with one of two kinds, and the choice must be deliberate:
   - `unexpected_response`, when the member is one the provider sends on every response of the
     operation, so that its absence means this is not one of them. Search and Images use this for
     `searchParameters`, through `missingParameters`.
   - `malformed_response`, when the member is the payload itself, so that a response without it is
     broken. Scrape uses this for `text`, through `malformed(errMissingMember, "text")`.

### Elsewhere in the package

- [`metadata.go`](../metadata.go): the `Operation` constant, in the `Operations` block, with a lowercase
  value and a comment such as `// OpNews is POST /news on the search origin (News).`, and the new path in
  the godoc of `Metadata.Path`.
- [`client.go`](../client.go): the path constant, such as `pathNews = "/news"`, in the request paths
  block; an `endpoint` field on `Client`; and its line in `NewClient`,
  `newEndpoint(OpNews, searchHost, pathNews, o.callTimeout(o.timeout))`. `callTimeout` folds an injected
  `http.Client`'s Timeout into the endpoint's timeout, and an endpoint built without it would silently
  lose that.
  Also update the `Client` godoc, which names the operations.
- A new `FailureKind` is added only when no existing kind fits. It then gets a sentinel in
  [`errors.go`](../errors.go), a case in `kindSentinel`, and a decision on whether it leaves the outcome
  unknown. A new success rule should still be named in the godoc of `ErrUnexpectedResponse` or
  `ErrMalformedResponse`.

### A new origin

A new path on an existing origin reuses that origin's host. **A new origin is a credential destination**,
and needs all of these:

- a `Default<Name>BaseURL` constant in [`client.go`](../client.go), next to the other two;
- in [`options.go`](../options.go), an `options` field filled in by `defaultOptions`, and a
  `With<Name>BaseURL` option whose godoc says that the origin receives the API key;
- a case in `options.validate`, which checks the URL with `originHost` and returns the new host, in the
  option's place in validation's fixed order;
- a `<name>Origin` field in `Client.String`;
- optionally, a `With<Name>Timeout`, built like `WithScrapeTimeout` (a value and a "set" flag, a
  `validate` case and a `Client.String` field), when the operation needs its own budget.

The README's sections on origins and trust then name the new origin as one that receives the key.

## Fixtures

Write fixtures from scratch with synthetic values (`example.com`, `example.org` and `example.net` hosts,
invented text) under `testdata/synthetic/<op>/`, labelled as SDK fixtures, and add the folder to
[the fixtures note](../testdata/synthetic/README.md). Never copy the provider's examples or site
material, and never commit a real response. Every `*.json` file in the folder must be a successful
response: the decode tests load each one.

## Tests

The same evidence as the existing operations. Shared helpers live in
[`helpers_test.go`](../helpers_test.go), [`helpers_internal_test.go`](../helpers_internal_test.go) and
[`decode_harness_test.go`](../decode_harness_test.go).

- **An `opCase`** in `opCases()`, with its smallest successful response (next to `searchOK`) and its path
  constant (the tests keep their own copy of the path block). The suites that run every operation (status
  table, validation, done contexts, raw capture states, privacy, redirects, replays, timeouts,
  concurrency, call counts) then cover the new one.
- **Every switch and table that names each operation.** Search the tests for `OpScrape` and
  `ScrapeResponse`. That finds `directCall`, `invalidCall` and `metaOf` in `helpers_test.go`; `srdPlain`
  and `srdAssertRoundTrip` in `decode_harness_test.go`; `prvCallOf`, `prvRefusals` and the raw-marker
  bodies in `privacy_test.go`; the value checks in `raw_test.go` and `concurrency_test.go`; the tables of
  `memory_test.go` and `benchmark_test.go`; `rpiOps` in `replay_internal_test.go`; and `fzdSchemaOf` in
  `fuzz_decode_internal_test.go`. Most of these panic or fail on an operation they do not know, but four
  quietly fall back: `rawExoticBody` and `rawRefusedCases` in `raw_test.go` and `cncCall` in
  `concurrency_test.go` treat it as Scrape, and `cncAnswer` as Search. A table covers only the rows it lists.
- **A third origin.** `opCase.scrape` picks one of two origins, so a third needs it replaced with a field
  naming the origin. The two-origin helpers then gain the third: the test origin constants, `servers` (with
  `of` and `other`), `newServerClient` and `newServerClientProto`, `newHTTPClientClient`, `sdkClient` and
  `srdLive`.
- **The exact wire request**, with `assertWireRequest`, `assertNoReplay` and `assertBody` (method, origin,
  path, headers, body bytes, no banned header).
- **The validation matrix**: rows in `invalidRequests()`, which `TestRequestValidation` runs on
  `newOfflineClient` (any round trip fails the test) and the privacy suite runs again with its markers.
- **Decoding**, in an `<op>_decode_test.go` suite on the `srdLive` harness, so that every case is checked
  live and offline. It covers every typed member, a wrong type at every member path, a null item in every
  list, the success rule, credits, unknown members ignored and respelled member names. It also needs
  rows in `TestDecodeFixtures` and `TestDecodeWhitespace` in [`decode_test.go`](../decode_test.go).
- **`assertCallError`** for every error. It checks the error text, the sentinels and the leak markers
  across `fmt` verbs and error chains. **`assertRoundTrip`** covers the result.
- **A fuzz target** for the decoder, `FuzzDecode<Op>` with a schema in `fuzz_decode_internal_test.go`, and
  **benchmarks**: `Benchmark<Op>`, with capture off and on, and `BenchmarkDecode<Op>`.
- **The demo**: a step in [`examples/main.go`](../examples/main.go), which walks through every operation,
  with its expected output in `examples/main_test.go`.

No test may reach the network: in-memory transports and loopback TLS servers only. A live test needs the
owner's approval of operations, inputs, destinations, call count and credit budget, a `live` build tag
and an explicit environment switch, and never runs in CI.

## Documentation to update

- [`doc.go`](../doc.go): the opening paragraph, the operations-and-billing table and the scope section.
- [README.md](../README.md):
  - the feature list;
  - the operations-and-billing table;
  - a usage section for the operation, with a Go block quoted verbatim from a new `Example` in
    `example_test.go`;
  - the Results table and notes for its result types;
  - the error reference (the success-rule row, and the list of operations with their constants);
  - the decoding and success rules;
  - the Limits table, for any new limit;
  - the list of what is not covered.
- [`readme_internal_test.go`](../readme_internal_test.go) checks the README against the code. It fails
  until the README's Go blocks are quoted from compiled code, every `Operation` constant is named as
  `` `news` (`OpNews`) ``, and every limit it lists still matches its constant. A new limit or default
  gets a claim in `TestReadmeLimitsMatchCode`.

## What must not change

- **Exactly one HTTP attempt per call.** No retries, redirects, hedging, fallbacks, pagination, batching
  or follow-on calls inside a method. Every request keeps a plain body with `GetBody` nil, never
  `http.NoBody`, and no idempotency key.
- **Nothing paid without a method call.** A failure after sending that leaves the outcome open matches
  `ErrOutcomeUnknown`, and nothing is ever resubmitted.
- **No values in errors.** `Error()` holds only the operation, the kind or validation reason, the name
  of a refused field, the HTTP status, a fixed transport class and a JSON location: never the key, a
  query, a URL, a header value, a body or provider text. `Metadata` stays safe to log.
- **The key goes only to validated `https` origins,** in the `X-API-KEY` header only.
- **Raw capture stays explicit, bounded and copy-on-read,** and is never printed or marshaled.
- The package imports only the standard library and starts no background goroutine: the only SDK code
  that runs off the caller's goroutine is the request body's `Read`, called by the transport's own
  connection goroutines, and the `context.AfterFunc` that closes a response body on timeout or
  cancellation. It has no globals other than sentinel errors, and no `init`. It never sleeps, and never
  logs, prints, stores or caches data, or reads the environment.
