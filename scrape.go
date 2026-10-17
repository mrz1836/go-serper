package serper

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// ScrapeRequest is one page scrape: POST / on the scrape origin. Serper
// fetches the target, not the SDK.
//
// String, GoString and Format print only the request's shape, never the
// target; see SearchRequest for their limits.
type ScrapeRequest struct {
	// URL is the page to scrape, sent as "url" exactly as given. Required:
	// valid UTF-8 that parses as an absolute http or https URL with a host
	// and no userinfo. The SDK applies no other policy to the target, such as
	// which hosts are allowed: that is the caller's decision.
	URL string

	// IncludeMarkdown asks for a Markdown rendering too. It is sent as
	// "includeMarkdown": true only when set, as the provider's playground
	// does.
	IncludeMarkdown bool
}

// scrapeWire is the JSON body of Scrape.
type scrapeWire struct {
	URL             string `json:"url"`
	IncludeMarkdown bool   `json:"includeMarkdown,omitempty"`
}

// textBytes returns the total length of the body's text members.
func (w *scrapeWire) textBytes() int { return len(w.URL) }

// String prints the request's shape, such as
// "serper.ScrapeRequest{url: 31 bytes, includeMarkdown: true}", never the
// target.
func (r ScrapeRequest) String() string {
	return describe("serper.ScrapeRequest",
		field{"url", bytesOrUnset(r.URL)},
		field{"includeMarkdown", strconv.FormatBool(r.IncludeMarkdown)},
	)
}

// GoString returns the same text as String, so that %#v prints the shape only.
func (r ScrapeRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb, %d and %x included, reaches the target by reflection.
func (r ScrapeRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// wire validates r and returns its JSON body shape. Errors name the field,
// never its value.
func (r ScrapeRequest) wire() (*scrapeWire, error) {
	if reason := scrapeURLProblem(r.URL); reason != "" {
		return nil, invalidRequest(OpScrape, "URL", reason)
	}
	return &scrapeWire{URL: r.URL, IncludeMarkdown: r.IncludeMarkdown}, nil
}

// ScrapeResponse is the typed part of a Scrape response. Members the SDK does
// not type, such as metadata, are reachable only through a raw capture (see
// WithRawCapture). It round-trips through encoding/json with the provider's
// member names; Meta and the captured body are never marshaled.
type ScrapeResponse struct {
	// Text is the page text. It is always present on success, and "" is a
	// valid returned value: the SDK never turns a failure into an empty Text.
	Text string `json:"text"`

	// Markdown is the Markdown rendering, nil when it was not returned. It is
	// never derived from Text.
	Markdown *string `json:"markdown,omitzero"`

	// Meta describes the call.
	Meta Metadata `json:"-"`

	raw rawBody
}

// RawJSON returns a new copy of the exact HTTP 200 body the decoder read, and
// true, when raw capture was on for the call; otherwise (nil, false). Each
// call allocates a new copy.
func (r *ScrapeResponse) RawJSON() ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	return r.raw.copyOut()
}

func (r *ScrapeResponse) finish(meta Metadata, raw rawBody) {
	r.Meta, r.raw = meta, raw
}

// Scrape asks Serper to fetch and extract one page: POST
// https://scrape.serper.dev (or the WithScrapeBaseURL origin), a separate
// service at its root path. The SDK never contacts the target itself.
//
// It spends the account's credits (the provider's playground says more for
// harder pages) and makes exactly one attempt; see Search for what that means
// for repeats and unknown outcomes. Its deadline is WithScrapeTimeout's,
// which defaults to WithTimeout's.
//
// Success requires an HTTP 200 JSON object whose "text" is a string, ""
// included; "markdown" may be absent, null or a string.
//
// Errors are a *ValidationError (nothing sent) or a *CallError.
func (c *Client) Scrape(ctx context.Context, req ScrapeRequest, opts ...CallOption) (*ScrapeResponse, error) {
	if err := c.prepare(ctx, OpScrape); err != nil {
		return nil, err
	}
	w, err := req.wire()
	if err != nil {
		return nil, err
	}
	return call(ctx, c, &c.scrape, w, opts, decodeScrape)
}

// DecodeScrapeResponse decodes a Scrape response body offline, with the same
// decoder, success rule and credits reading as Scrape. A failure is a
// *CallError that never matches ErrOutcomeUnknown; the result's Meta names
// only the operation and the credits. Member names match as encoding/json
// matches them: exactly first, then ignoring case.
func DecodeScrapeResponse(data []byte) (*ScrapeResponse, error) {
	return decodeOffline(OpScrape, data, decodeScrape)
}

// scrapeMirror is the decoding shape of a Scrape response: pointers, so that a
// missing or null text can be told from "".
type scrapeMirror struct {
	Text     *string         `json:"text"`
	Markdown *string         `json:"markdown"`
	Credits  json.RawMessage `json:"credits"`
}

// decodeScrape decodes a Scrape response.
func decodeScrape(data []byte) (*ScrapeResponse, Credits, *decodeError) {
	var m scrapeMirror
	derr := decodeObject(data, &m)
	credits := creditsOf(m.Credits)
	if derr != nil {
		return nil, credits, derr
	}
	if m.Text == nil {
		return nil, credits, malformed(errMissingMember, "text")
	}
	return &ScrapeResponse{Text: *m.Text, Markdown: m.Markdown}, credits, nil
}
