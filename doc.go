// Package serper is a small, typed, standard-library-only Go client for three
// Serper (https://serper.dev) operations: web search, image search and page
// scrape. It is an independent, unofficial SDK, not affiliated with Serper or
// Google.
//
// Create one Client with NewClient and your API key, share it, and call one
// method per operation. Every method takes a context and a request struct,
// and makes exactly one HTTP attempt.
//
//	client, err := serper.NewClient(apiKey)
//	if err != nil {
//		return err
//	}
//	res, err := client.Search(ctx, serper.SearchRequest{Query: "golang http"})
//	if err != nil {
//		return err
//	}
//	for _, o := range res.Organic {
//		fmt.Println(o.Position, o.Title, o.Link)
//	}
//
// # Operations and billing
//
//	Method   Request                                 Effect
//	Search   POST https://google.serper.dev/search   PAID: spends credits
//	Images   POST https://google.serper.dev/images   PAID: spends credits
//	Scrape   POST https://scrape.serper.dev/         PAID; Serper fetches the target
//	Decode*  (no request)                            decodes a kept body, offline
//
// No official API reference was found: the request and response shapes the
// SDK uses come from the provider's own playground and examples. Nothing is
// paid unless you call a method. The SDK never retries, hedges, follows a
// redirect, pages through results, batches, caches or rate-limits, and it
// disables the standard library's automatic resends. A repeated request is a
// new call, and may be billed again.
//
// After a 5xx, a 303, a 1xx or a status outside 100 to 599, an HTTP 2xx
// response that could not be used, or a timeout, cancellation or transport
// failure once the request was sent, the error matches ErrOutcomeUnknown: the
// provider may already have processed and billed the call. A failure leaves
// the outcome known only when the transport never read the request body and
// the failure shows that no connection was made: a DNS or dial failure, a
// failure to connect to a proxy, or a TLS certificate refused in the
// handshake. Requests that fail local validation,
// and calls whose context is already done, send nothing.
//
// When a response reports the credits it used, Metadata.Credits holds them;
// the member is undocumented, so unknown is never reported as zero. The SDK
// does no pricing arithmetic.
//
// # Errors
//
// Validation failures are *ValidationError values matching ErrInvalidConfig or
// ErrInvalidRequest: nothing was sent. Every other failure is a *CallError,
// whose Kind matches its sentinel (or context.Canceled or
// context.DeadlineExceeded) with errors.Is, and whose Meta, Field and
// OutcomeUnknown say what happened. ErrTimeout reports the SDK's own timeout
// or a timeout the network reported, and also matches
// context.DeadlineExceeded, so check it first.
//
// Error strings hold only the operation, the kind or validation reason, the
// name of a refused field, the HTTP status, a fixed transport class and a JSON
// location: never the API key, the query, the scrape target, header values,
// response bodies or provider messages.
//
// # Raw capture
//
// WithRawCapture (per call) and WithDefaultRawCapture (per Client) keep the
// exact HTTP 200 body the decoder read, bounded by WithMaxResponseBytes, for
// replay storage. RawJSON returns a new copy each time, and
// DecodeSearchResponse, DecodeImagesResponse and DecodeScrapeResponse decode
// a kept body offline with the same rules. A valid JSON body that decoding
// refused is kept on the *CallError instead, so that schema drift can be
// stored and studied.
//
// # Trust boundaries and privacy
//
// The API key travels only in the X-API-KEY header, to the configured
// origins: https://google.serper.dev and https://scrape.serper.dev by
// default, or the https origins given to WithSearchBaseURL and
// WithScrapeBaseURL, which receive the key too. An injected http.Client's
// transport sees the key and every body, and may retry on its own; on any
// transport, an httptrace.ClientTrace in the caller's context sees every
// request header, the key included. The SDK's own transport speaks HTTP/1.1 only and never reads
// proxy environment variables. The standard library itself may log stray
// bytes a server sends on an idle connection.
//
// The SDK never logs, prints, stores or caches anything, and never reads the
// environment. Requests print only their shape with fmt, except for %p on a
// value and a request held in an unexported struct field, which fmt prints
// without calling their methods; encoding/json writes every field. Results
// hold provider data that may echo the query or carry sensitive content:
// printing, logging and storing them is the caller's responsibility.
//
// # Scope and extension
//
// Implemented: Search, Images and Scrape. Out of scope by design: retries,
// hedging, fallbacks, caching, rate limiting and the GET form with an apiKey
// query parameter. Possible later, each once the provider's contract for it is
// verified: other Serper endpoints, batch requests, pagination helpers,
// Retry-After, rate-limit and request-ID metadata, Scrape's extra include
// options and provider-message diagnostics. An operation would be added as one
// more method on Client with its own request and result types, without
// breaking existing callers (see docs/adding-an-operation.md in the
// repository).
package serper
