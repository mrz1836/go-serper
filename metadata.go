package serper

import (
	"errors"
	"strconv"
	"time"
)

// Operation names a Serper operation.
type Operation string

// Operations.
const (
	// OpSearch is POST /search on the search origin (Search).
	OpSearch Operation = "search"

	// OpImages is POST /images on the search origin (Images).
	OpImages Operation = "images"

	// OpScrape is POST / on the scrape origin (Scrape).
	OpScrape Operation = "scrape"
)

// Metadata describes one call. It is safe to log or serialize: it never holds
// the API key, the query, the scrape target, a body or a header value. Every
// response and every *CallError carries one.
type Metadata struct {
	// Op is the operation.
	Op Operation `json:"op"`

	// Origin is the service origin the call used, such as
	// "https://google.serper.dev". It is "" for a Decode helper.
	Origin string `json:"origin"`

	// Path is the request path: "/search", "/images" or "/". It is "" for a
	// Decode helper.
	Path string `json:"path"`

	// StatusCode is the HTTP status received, or 0 when no response was
	// received.
	StatusCode int `json:"statusCode"`

	// Attempts is the number of requests the SDK issued: 0 or 1. An injected
	// transport may retry on its own; the SDK cannot see or count that.
	Attempts int `json:"attempts"`

	// Elapsed is the local monotonic time from just before the SDK built the
	// request and handed it to the http.Client until the response body was
	// closed (after the whole body was read, the limit was hit, a read
	// failed, or an error body was discarded), or until the http.Client
	// returned an error. It excludes validation, encoding, decoding and
	// RawJSON copies. It is 0 exactly when Attempts is 0: a measured call
	// reports at least 1ns. It is client time, not provider processing time
	// or a billing unit.
	Elapsed time.Duration `json:"elapsed"`

	// Credits is the provider's report of the credits the call used, read
	// from the top-level "credits" member of an HTTP 200 body that reached the
	// decoder and was valid UTF-8 and one JSON object, even one the decoder
	// then refused.
	Credits Credits `json:"credits"`

	// RawCapture reports what happened to raw capture for this call.
	RawCapture RawCaptureState `json:"rawCapture"`
}

// goString prints the metadata as a Go-like literal, for CallError.GoString.
func (m Metadata) goString() string {
	return "serper.Metadata{Op:" + strconv.Quote(string(m.Op)) +
		", Origin:" + strconv.Quote(m.Origin) +
		", Path:" + strconv.Quote(m.Path) +
		", StatusCode:" + strconv.Itoa(m.StatusCode) +
		", Attempts:" + strconv.Itoa(m.Attempts) +
		", Elapsed:" + strconv.FormatInt(int64(m.Elapsed), 10) +
		", Credits:" + m.Credits.goString() +
		", RawCapture:" + m.RawCapture.String() + "}"
}

// Credits is the provider's report of the credits one call used. Serper's
// examples never show it, so its member name and type are not documented: the
// SDK reads a top-level "credits" member (matched as encoding/json matches
// member names: exactly first, then ignoring case) and never fails a call over
// it.
type Credits struct {
	// Status says whether credits were reported.
	Status CreditsStatus `json:"status"`

	// Used is the number reported. It is non-nil only when Status is
	// CreditsReported, and a reported 0 is kept as 0.
	Used *int64 `json:"used,omitempty"`
}

// Value returns the credits used and true when they were reported, and
// (0, false) otherwise: unknown is not zero.
func (c Credits) Value() (int64, bool) {
	if c.Status != CreditsReported || c.Used == nil {
		return 0, false
	}
	return *c.Used, true
}

// goString prints the credits as a Go-like literal.
func (c Credits) goString() string {
	used := "nil"
	if c.Used != nil {
		used = strconv.FormatInt(*c.Used, 10)
	}
	return "serper.Credits{Status:" + c.Status.String() + ", Used:" + used + "}"
}

// CreditsStatus says whether, and how, a response reported its credits.
type CreditsStatus uint8

// Credits statuses.
const (
	// CreditsNotReported means the body had no "credits" member, or it was
	// null, or no HTTP 200 JSON body was read. Unknown, not zero.
	CreditsNotReported CreditsStatus = iota

	// CreditsReported means "credits" was a JSON integer from 0 to
	// 9223372036854775807, written in plain digits.
	CreditsReported

	// CreditsInvalid means "credits" was present but not such an integer:
	// a fraction, an exponent ("2e0") or decimal spelling ("2.0"), a negative
	// number, a number too large, a string, or any other type. The value is
	// dropped; the result is still returned.
	CreditsInvalid
)

// String returns "not_reported", "reported" or "invalid", or
// "CreditsStatus(<n>)" for any other value.
func (s CreditsStatus) String() string {
	if name, ok := s.name(); ok {
		return name
	}
	return "CreditsStatus(" + strconv.Itoa(int(s)) + ")"
}

// MarshalText returns the status's name. Any other value fails.
func (s CreditsStatus) MarshalText() ([]byte, error) {
	name, ok := s.name()
	if !ok {
		return nil, errUnknownCreditsStatus
	}
	return []byte(name), nil
}

// UnmarshalText sets the status from its name. Any other text fails, and the
// error never quotes it.
func (s *CreditsStatus) UnmarshalText(text []byte) error {
	for v := range CreditsInvalid + 1 {
		if name, _ := v.name(); name == string(text) {
			*s = v
			return nil
		}
	}
	return errUnknownCreditsStatus
}

func (s CreditsStatus) name() (string, bool) {
	switch s {
	case CreditsNotReported:
		return "not_reported", true
	case CreditsReported:
		return "reported", true
	case CreditsInvalid:
		return "invalid", true
	}
	return "", false
}

// RawCaptureState reports what raw capture did for one call.
type RawCaptureState uint8

// Raw capture states.
const (
	// RawCaptureOff means capture was not requested.
	RawCaptureOff RawCaptureState = iota

	// RawCaptureComplete means the whole HTTP 200 body was kept: on a
	// response, or on a *CallError for a valid JSON body that decoding
	// refused.
	RawCaptureComplete

	// RawCaptureNoResponse means capture was requested but no response was
	// received: a transport failure, a timeout or cancellation before the
	// headers, or a context that was done before sending.
	RawCaptureNoResponse

	// RawCaptureFailed means capture was requested and a response arrived,
	// but no body was kept: a status other than 200, or an HTTP 200 body that
	// was too large, cut short, stalled, not decodable, not JSON by its
	// Content-Type, or not valid UTF-8 or JSON, or that the call's deadline or
	// the caller's context ended before it was read in full.
	RawCaptureFailed
)

// String returns "off", "complete", "no_response" or "failed", or
// "RawCaptureState(<n>)" for any other value.
func (s RawCaptureState) String() string {
	if name, ok := s.name(); ok {
		return name
	}
	return "RawCaptureState(" + strconv.Itoa(int(s)) + ")"
}

// MarshalText returns the state's name. Any other value fails.
func (s RawCaptureState) MarshalText() ([]byte, error) {
	name, ok := s.name()
	if !ok {
		return nil, errUnknownRawCaptureState
	}
	return []byte(name), nil
}

// UnmarshalText sets the state from its name. Any other text fails, and the
// error never quotes it.
func (s *RawCaptureState) UnmarshalText(text []byte) error {
	for v := range RawCaptureFailed + 1 {
		if name, _ := v.name(); name == string(text) {
			*s = v
			return nil
		}
	}
	return errUnknownRawCaptureState
}

func (s RawCaptureState) name() (string, bool) {
	switch s {
	case RawCaptureOff:
		return "off", true
	case RawCaptureComplete:
		return "complete", true
	case RawCaptureNoResponse:
		return "no_response", true
	case RawCaptureFailed:
		return "failed", true
	}
	return "", false
}

// Errors of the enum text methods. They never quote the text.
var (
	errUnknownCreditsStatus   = errors.New("serper: unknown credits status")
	errUnknownRawCaptureState = errors.New("serper: unknown raw capture state")
)
