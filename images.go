package serper

import (
	"context"
	"encoding/json"
	"fmt"
)

// ImagesRequest is one image search: POST /images on the search origin. It
// has the same fields and rules as SearchRequest, as a type of its own.
//
// String, GoString and Format print only the request's shape, never the
// query; see SearchRequest for their limits.
type ImagesRequest struct {
	// Query is the search query, sent as "q". Required: 1 or more bytes of
	// valid UTF-8, passed through unchanged.
	Query string

	// Location is an opaque location string, sent as "location" when set.
	// Whether Images honors it is not documented.
	Location string

	// GL is the country code, sent as "gl" when set. The SDK never forces
	// one.
	GL string

	// HL is the language code, sent as "hl" when set. The SDK never forces
	// one.
	HL string

	// Num is the number of results asked for, sent as "num" when set. It must
	// be 0 (omitted) or at least 1; the provider may return fewer, and its
	// playground offers 10 and 100 (shown as using more credits).
	Num int

	// Autocorrect is sent as "autocorrect" when set: nil omits it, and
	// new(false) sends false.
	Autocorrect *bool

	// TBS is an opaque time filter, sent as "tbs" when set.
	TBS string

	// Page is the result page, sent as "page" when set. It must be 0
	// (omitted) or at least 1; one call requests exactly one page.
	Page int
}

// String prints the request's shape, such as
// "serper.ImagesRequest{query: 17 bytes, ...}", never its values.
func (r ImagesRequest) String() string { return queryRequest(r).describe("serper.ImagesRequest") }

// GoString returns the same text as String, so that %#v prints the shape only.
func (r ImagesRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb, %d and %x included, reaches the query by reflection.
func (r ImagesRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// ImagesResponse is the typed part of an Images response. Images keeps the
// provider's order and values exactly as returned; nil means absent or null,
// and an empty, non-nil list means []. It round-trips through encoding/json
// with the provider's member names; Meta and the captured body are never
// marshaled.
type ImagesResponse struct {
	// SearchParameters is the provider's echo of the request. It is always
	// present on success, and may hold the query.
	SearchParameters *SearchParameters `json:"searchParameters,omitzero"`

	// Images is the image results, in the provider's order.
	Images []ImageResult `json:"images,omitzero"`

	// Meta describes the call.
	Meta Metadata `json:"-"`

	raw rawBody
}

// RawJSON returns a new copy of the exact HTTP 200 body the decoder read, and
// true, when raw capture was on for the call; otherwise (nil, false). Each
// call allocates a new copy.
func (r *ImagesResponse) RawJSON() ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	return r.raw.copyOut()
}

func (r *ImagesResponse) finish(meta Metadata, raw rawBody) {
	r.Meta, r.raw = meta, raw
}

// ImageResult is one image result. The image file (ImageURL), the page it
// appears on (Link) and the provider's label for its source (Source) are kept
// apart, as returned: none is ever filled in from another.
type ImageResult struct {
	Title string `json:"title,omitzero"`

	// ImageURL is "imageUrl": the image file itself.
	ImageURL    string `json:"imageUrl,omitzero"`
	ImageWidth  int    `json:"imageWidth,omitzero"`
	ImageHeight int    `json:"imageHeight,omitzero"`

	// ThumbnailURL is "thumbnailUrl".
	ThumbnailURL    string `json:"thumbnailUrl,omitzero"`
	ThumbnailWidth  int    `json:"thumbnailWidth,omitzero"`
	ThumbnailHeight int    `json:"thumbnailHeight,omitzero"`

	// Source is "source": the provider's label for the source, never
	// replaced by Link.
	Source string `json:"source,omitzero"`
	Domain string `json:"domain,omitzero"`

	// Link is "link": the page the image appears on.
	Link string `json:"link,omitzero"`

	// GoogleURL is "googleUrl".
	GoogleURL string `json:"googleUrl,omitzero"`

	// Position is the position as returned, never renumbered; 0 means it was
	// not returned.
	Position int `json:"position,omitzero"`
}

// Images runs one image search: POST https://google.serper.dev/images (or
// the WithSearchBaseURL origin).
//
// It spends the account's credits and makes exactly one attempt; see Search
// for what that means for repeats and unknown outcomes. Success requires an
// HTTP 200 JSON object with a searchParameters object; images is never
// required.
//
// Errors are a *ValidationError (nothing sent) or a *CallError.
func (c *Client) Images(ctx context.Context, req ImagesRequest, opts ...CallOption) (*ImagesResponse, error) {
	if err := c.prepare(ctx, OpImages); err != nil {
		return nil, err
	}
	w, err := queryRequest(req).wire(OpImages)
	if err != nil {
		return nil, err
	}
	return call(ctx, c, &c.images, w, opts, decodeImages)
}

// DecodeImagesResponse decodes an Images response body offline, with the same
// decoder, success rule and credits reading as Images. A failure is a
// *CallError that never matches ErrOutcomeUnknown; the result's Meta names
// only the operation and the credits. Member names match as encoding/json
// matches them: exactly first, then ignoring case.
func DecodeImagesResponse(data []byte) (*ImagesResponse, error) {
	return decodeOffline(OpImages, data, decodeImages)
}

// imagesMirror is the decoding shape of an Images response.
type imagesMirror struct {
	SearchParameters *SearchParameters `json:"searchParameters"`
	Images           []*ImageResult    `json:"images"`
	Credits          json.RawMessage   `json:"credits"`
}

// decodeImages decodes an Images response: shape first (malformed_response),
// then the success rule (unexpected_response).
func decodeImages(data []byte) (*ImagesResponse, Credits, *decodeError) {
	var m imagesMirror
	derr := decodeObject(data, &m)
	credits := creditsOf(m.Credits)
	if derr != nil {
		return nil, credits, derr
	}
	images, derr := values(m.Images, "images")
	if derr != nil {
		return nil, credits, derr
	}
	if m.SearchParameters == nil {
		return nil, credits, missingParameters()
	}
	return &ImagesResponse{SearchParameters: m.SearchParameters, Images: images}, credits, nil
}
