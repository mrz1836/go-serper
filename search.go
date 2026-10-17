package serper

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// SearchRequest is one web search: POST /search on the search origin.
//
// Every field is sent byte for byte as given: the SDK never trims,
// normalizes or rewrites a value, and never sends a default of its own. A
// zero or empty field is omitted, so the provider applies its own default.
//
// String, GoString and Format print only the request's shape (which fields
// are set, byte counts and numbers), never the query, as a best-effort guard
// against accidental logging. fmt cannot call them for %p on a value, nor for
// a request held in an unexported struct field, and encoding/json (and any
// logger that serializes values with it, such as log/slog's JSON handler)
// writes every field: log a request's String, never the request.
type SearchRequest struct {
	// Query is the search query, sent as "q". Required: 1 or more bytes of
	// valid UTF-8, with the provider's operators (quotes, -site: and so on)
	// passed through unchanged.
	Query string

	// Location is an opaque location string, sent as "location" when set.
	// Whether Search honors it is not documented.
	Location string

	// GL is the country code, sent as "gl" when set, such as "us". The SDK
	// never forces one.
	GL string

	// HL is the language code, sent as "hl" when set, such as "en". The SDK
	// never forces one.
	HL string

	// Num is the number of results asked for, sent as "num" when set. It must
	// be 0 (omitted) or at least 1; the provider may return fewer.
	Num int

	// Autocorrect is sent as "autocorrect" when set: nil omits it, and
	// new(false) sends false.
	Autocorrect *bool

	// TBS is an opaque time filter, sent as "tbs" when set, such as "qdr:w".
	TBS string

	// Page is the result page, sent as "page" when set. It must be 0
	// (omitted) or at least 1. One call requests exactly one page: the SDK
	// never fetches the next one.
	Page int
}

// queryRequest is the field list SearchRequest and ImagesRequest share, so
// that both convert to it for validation and encoding. It is defined from
// SearchRequest, so the conversion from ImagesRequest compiles only while
// the two field lists stay identical.
type queryRequest SearchRequest

// queryWire is the JSON body of Search and Images, in the member order of the
// provider's own request builder. It omits zero values only: unlike that
// builder, it never drops a value the caller set, such as gl "us" or num 10.
type queryWire struct {
	Q           string `json:"q"`
	Location    string `json:"location,omitempty"`
	GL          string `json:"gl,omitempty"`
	HL          string `json:"hl,omitempty"`
	Num         int    `json:"num,omitempty"`
	Autocorrect *bool  `json:"autocorrect,omitempty"`
	TBS         string `json:"tbs,omitempty"`
	Page        int    `json:"page,omitempty"`
}

// wire validates r for op and returns its JSON body shape. Errors name the
// field, never its value.
func (r queryRequest) wire(op Operation) (*queryWire, error) {
	if r.Query == "" {
		return nil, invalidRequest(op, "Query", reasonRequired)
	}
	for _, f := range [...]struct{ name, value string }{
		{"Query", r.Query}, {"Location", r.Location}, {"GL", r.GL}, {"HL", r.HL}, {"TBS", r.TBS},
	} {
		if reason := textProblem(f.value); reason != "" {
			return nil, invalidRequest(op, f.name, reason)
		}
	}
	switch {
	case r.Num < 0:
		return nil, invalidRequest(op, "Num", reasonOutOfRange)
	case r.Page < 0:
		return nil, invalidRequest(op, "Page", reasonOutOfRange)
	}
	return &queryWire{
		Q: r.Query, Location: r.Location, GL: r.GL, HL: r.HL,
		Num: r.Num, Autocorrect: r.Autocorrect, TBS: r.TBS, Page: r.Page,
	}, nil
}

// textBytes returns the total length of the body's text members.
func (w *queryWire) textBytes() int {
	return len(w.Q) + len(w.Location) + len(w.GL) + len(w.HL) + len(w.TBS)
}

// describe prints the shape of r as a value of type typ.
func (r queryRequest) describe(typ string) string {
	return describe(typ,
		field{"query", bytesOrUnset(r.Query)},
		field{"location", setOrUnset(r.Location != "")},
		field{"gl", setOrUnset(r.GL != "")},
		field{"hl", setOrUnset(r.HL != "")},
		field{"num", intOrUnset(r.Num)},
		field{"autocorrect", boolOrUnset(r.Autocorrect)},
		field{"tbs", setOrUnset(r.TBS != "")},
		field{"page", intOrUnset(r.Page)},
	)
}

// String prints the request's shape, such as
// "serper.SearchRequest{query: 12 bytes, location: unset, ...}", never its
// values.
func (r SearchRequest) String() string { return queryRequest(r).describe("serper.SearchRequest") }

// GoString returns the same text as String, so that %#v prints the shape only.
func (r SearchRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb, %d and %x included, reaches the query by reflection.
func (r SearchRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// SearchResponse is the typed part of a Search response. Lists keep the
// provider's order and values exactly as returned: nothing is sorted,
// renumbered, deduplicated, parsed or filled in. A nil list or pointer means
// the member was absent or null; an empty, non-nil list means it was [].
// Members the SDK does not type, such as answerBox or topStories, are
// reachable only through a raw capture (see WithRawCapture).
//
// It round-trips through encoding/json with the provider's member names.
// Meta and the captured body are never marshaled.
type SearchResponse struct {
	// SearchParameters is the provider's echo of the request. It is always
	// present on success, and may hold the query.
	SearchParameters *SearchParameters `json:"searchParameters,omitzero"`

	// KnowledgeGraph is the knowledge panel, when there is one.
	KnowledgeGraph *KnowledgeGraph `json:"knowledgeGraph,omitzero"`

	// Organic is the organic results, in the provider's order.
	Organic []OrganicResult `json:"organic,omitzero"`

	// PeopleAlsoAsk is the related questions.
	PeopleAlsoAsk []PeopleAlsoAsk `json:"peopleAlsoAsk,omitzero"`

	// RelatedSearches is the related queries.
	RelatedSearches []RelatedSearch `json:"relatedSearches,omitzero"`

	// Meta describes the call.
	Meta Metadata `json:"-"`

	raw rawBody
}

// RawJSON returns a new copy of the exact HTTP 200 body the decoder read, and
// true, when raw capture was on for the call; otherwise (nil, false). Each
// call allocates a new copy, so changing it never changes the response or a
// later copy.
func (r *SearchResponse) RawJSON() ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	return r.raw.copyOut()
}

func (r *SearchResponse) finish(meta Metadata, raw rawBody) {
	r.Meta, r.raw = meta, raw
}

// SearchParameters is the provider's echo of a Search or Images request, as
// returned. A zero number means it was not echoed.
type SearchParameters struct {
	Query       string `json:"q,omitzero"`
	Type        string `json:"type,omitzero"`
	Engine      string `json:"engine,omitzero"`
	GL          string `json:"gl,omitzero"`
	HL          string `json:"hl,omitzero"`
	Location    string `json:"location,omitzero"`
	TBS         string `json:"tbs,omitzero"`
	Num         int    `json:"num,omitzero"`
	Page        int    `json:"page,omitzero"`
	Autocorrect *bool  `json:"autocorrect,omitzero"`
}

// OrganicResult is one organic search result.
type OrganicResult struct {
	Title   string `json:"title,omitzero"`
	Link    string `json:"link,omitzero"`
	Snippet string `json:"snippet,omitzero"`

	// Date is the provider's text, such as "Aug 31, 2022" or "3 days ago":
	// never parsed.
	Date string `json:"date,omitzero"`

	// Position is the position as returned, never renumbered; 0 means it was
	// not returned.
	Position int `json:"position,omitzero"`

	Sitelinks  []Sitelink `json:"sitelinks,omitzero"`
	Attributes Attributes `json:"attributes,omitzero"`
}

// Sitelink is one sitelink of an organic result.
type Sitelink struct {
	Title string `json:"title,omitzero"`
	Link  string `json:"link,omitzero"`
}

// KnowledgeGraph is a knowledge panel.
type KnowledgeGraph struct {
	Title             string     `json:"title,omitzero"`
	Type              string     `json:"type,omitzero"`
	Website           string     `json:"website,omitzero"`
	ImageURL          string     `json:"imageUrl,omitzero"`
	Description       string     `json:"description,omitzero"`
	DescriptionSource string     `json:"descriptionSource,omitzero"`
	DescriptionLink   string     `json:"descriptionLink,omitzero"`
	Attributes        Attributes `json:"attributes,omitzero"`
}

// PeopleAlsoAsk is one related question.
type PeopleAlsoAsk struct {
	Question string `json:"question,omitzero"`
	Snippet  string `json:"snippet,omitzero"`
	Title    string `json:"title,omitzero"`
	Link     string `json:"link,omitzero"`
}

// RelatedSearch is one related query.
type RelatedSearch struct {
	Query string `json:"query,omitzero"`
}

// Search runs one web search: POST https://google.serper.dev/search (or the
// WithSearchBaseURL origin).
//
// It spends the account's credits and makes exactly one attempt: no retries,
// redirects, pagination or caching. A repeat of an identical request is a new
// call, and may be billed again. After a 5xx, a 303, a 1xx or a status
// outside 100 to 599, an HTTP 2xx response that could not be used, or a
// timeout, cancellation or transport failure once the request was sent
// (unless no connection was made), the call may already be billed (the error
// matches ErrOutcomeUnknown).
//
// Success requires an HTTP 200 JSON object with a searchParameters object;
// organic is never required, so a search with no results succeeds with an
// empty or nil Organic.
//
// Errors are a *ValidationError (nothing sent) or a *CallError.
func (c *Client) Search(ctx context.Context, req SearchRequest, opts ...CallOption) (*SearchResponse, error) {
	if err := c.prepare(ctx, OpSearch); err != nil {
		return nil, err
	}
	w, err := queryRequest(req).wire(OpSearch)
	if err != nil {
		return nil, err
	}
	return call(ctx, c, &c.search, w, opts, decodeSearch)
}

// DecodeSearchResponse decodes a Search response body, such as one kept with
// raw capture, offline: no request, and the same decoder, success rule and
// credits reading as Search. A failure is a *CallError that never matches
// ErrOutcomeUnknown; the result's Meta names only the operation and the
// credits. Member names match as encoding/json matches them: exactly first,
// then ignoring case.
func DecodeSearchResponse(data []byte) (*SearchResponse, error) {
	return decodeOffline(OpSearch, data, decodeSearch)
}

// searchMirror is the decoding shape of a Search response: pointer list items,
// so that a null item can be told from an empty one, and the raw credits.
type searchMirror struct {
	SearchParameters *SearchParameters `json:"searchParameters"`
	KnowledgeGraph   *knowledgeMirror  `json:"knowledgeGraph"`
	Organic          []*organicMirror  `json:"organic"`
	PeopleAlsoAsk    []*PeopleAlsoAsk  `json:"peopleAlsoAsk"`
	RelatedSearches  []*RelatedSearch  `json:"relatedSearches"`
	Credits          json.RawMessage   `json:"credits"`
}

// organicMirror is the decoding shape of an organic result.
type organicMirror struct {
	Title      string           `json:"title"`
	Link       string           `json:"link"`
	Snippet    string           `json:"snippet"`
	Date       string           `json:"date"`
	Position   int              `json:"position"`
	Sitelinks  []*Sitelink      `json:"sitelinks"`
	Attributes attributesMirror `json:"attributes"`
}

// knowledgeMirror is the decoding shape of a knowledge panel.
type knowledgeMirror struct {
	Title             string           `json:"title"`
	Type              string           `json:"type"`
	Website           string           `json:"website"`
	ImageURL          string           `json:"imageUrl"`
	Description       string           `json:"description"`
	DescriptionSource string           `json:"descriptionSource"`
	DescriptionLink   string           `json:"descriptionLink"`
	Attributes        attributesMirror `json:"attributes"`
}

// decodeSearch decodes a Search response: shape first (malformed_response),
// then the success rule (unexpected_response).
func decodeSearch(data []byte) (*SearchResponse, Credits, *decodeError) {
	var m searchMirror
	derr := decodeObject(data, &m)
	credits := creditsOf(m.Credits)
	if derr != nil {
		return nil, credits, derr
	}
	res := &SearchResponse{SearchParameters: m.SearchParameters}
	if m.KnowledgeGraph != nil {
		kg := m.KnowledgeGraph
		if kg.Attributes.bad {
			return nil, credits, malformed(errWrongType, "knowledgeGraph.attributes")
		}
		res.KnowledgeGraph = &KnowledgeGraph{
			Title: kg.Title, Type: kg.Type, Website: kg.Website, ImageURL: kg.ImageURL,
			Description: kg.Description, DescriptionSource: kg.DescriptionSource,
			DescriptionLink: kg.DescriptionLink, Attributes: kg.Attributes.list,
		}
	}
	organic, derr := organicResults(m.Organic)
	if derr != nil {
		return nil, credits, derr
	}
	res.Organic = organic
	if res.PeopleAlsoAsk, derr = values(m.PeopleAlsoAsk, "peopleAlsoAsk"); derr != nil {
		return nil, credits, derr
	}
	if res.RelatedSearches, derr = values(m.RelatedSearches, "relatedSearches"); derr != nil {
		return nil, credits, derr
	}
	if res.SearchParameters == nil {
		return nil, credits, missingParameters()
	}
	return res, credits, nil
}

// organicResults copies the organic results, failing on a null result,
// sitelink or malformed attributes at its exact location.
func organicResults(items []*organicMirror) ([]OrganicResult, *decodeError) {
	if items == nil {
		return nil, nil
	}
	out := make([]OrganicResult, len(items))
	for i, it := range items {
		if it == nil {
			return nil, nullItem("organic", i)
		}
		if it.Attributes.bad {
			return nil, malformed(errWrongType, "organic["+strconv.Itoa(i)+"].attributes")
		}
		sitelinks, derr := values(it.Sitelinks, "organic["+strconv.Itoa(i)+"].sitelinks")
		if derr != nil {
			return nil, derr
		}
		out[i] = OrganicResult{
			Title: it.Title, Link: it.Link, Snippet: it.Snippet, Date: it.Date, Position: it.Position,
			Sitelinks: sitelinks, Attributes: it.Attributes.list,
		}
	}
	return out, nil
}

// values copies a list of pointer items into a list of values, failing on a
// null item at field[i]. nil stays nil, and [] stays an empty, non-nil list.
func values[T any](items []*T, field string) ([]T, *decodeError) {
	if items == nil {
		return nil, nil
	}
	out := make([]T, len(items))
	for i, it := range items {
		if it == nil {
			return nil, nullItem(field, i)
		}
		out[i] = *it
	}
	return out, nil
}

// missingParameters is the failure of a Search or Images response without a
// searchParameters object.
func missingParameters() *decodeError {
	return &decodeError{
		kind: FailureUnexpectedResponse, cause: errMissingMember, field: "searchParameters", validJSON: true,
	}
}
