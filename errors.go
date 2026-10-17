package serper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors. Match them with errors.Is.
//
// ErrInvalidConfig and ErrInvalidRequest are local: an error matching either
// means no request was sent. Every other sentinel matches a *CallError: a
// call that was attempted, a call that a done context stopped before it was
// sent, or a body that a Decode helper refused.
//
// The HTTP-status sentinels name generic HTTP classes. No error contract was
// found in Serper's public material, so which status it sends for a bad key or
// exhausted credits is not known.
var (
	// ErrInvalidConfig reports an invalid NewClient argument or option, or a
	// Client that was not created by NewClient. No request was sent.
	ErrInvalidConfig = errors.New("serper: invalid configuration")

	// ErrInvalidRequest reports a request that fails local validation, or a
	// nil context. No request was sent.
	ErrInvalidRequest = errors.New("serper: invalid request")

	// ErrUnauthorized reports HTTP 401.
	ErrUnauthorized = errors.New("serper: unauthorized")

	// ErrForbidden reports HTTP 403.
	ErrForbidden = errors.New("serper: forbidden")

	// ErrRateLimited reports HTTP 429. The SDK never sleeps or retries: the
	// caller decides whether and when to try again.
	ErrRateLimited = errors.New("serper: rate limited")

	// ErrRejected reports any other HTTP 4xx status.
	ErrRejected = errors.New("serper: rejected by provider")

	// ErrUpstream reports an HTTP 5xx status.
	ErrUpstream = errors.New("serper: provider server error")

	// ErrUnexpectedResponse reports a response the SDK cannot use: an HTTP
	// status that is not 200 and not 4xx or 5xx (a redirect, which is never
	// followed, included), an undecoded Content-Encoding, a Content-Type that
	// is not JSON, or an HTTP 200 JSON object that breaks the operation's
	// success rule (such as a search response without searchParameters).
	ErrUnexpectedResponse = errors.New("serper: unexpected response")

	// ErrMalformedResponse reports an HTTP 200 body, or a body given to a
	// Decode helper, that is not valid UTF-8, not one valid JSON value, not a
	// JSON object, holds a known member of the wrong JSON type or a null list
	// item, or lacks Scrape's text.
	ErrMalformedResponse = errors.New("serper: malformed response")

	// ErrResponseTooLarge reports an HTTP 200 body larger than the limit set
	// with WithMaxResponseBytes, by its declared length or as read.
	ErrResponseTooLarge = errors.New("serper: response too large")

	// ErrTransport reports a failure in transit: DNS, connection, TLS or
	// proxy errors, a reset, a body that ended early or ran past its declared
	// length, or a body read that made no progress.
	ErrTransport = errors.New("serper: transport failure")

	// ErrTimeout reports that the SDK's own timeout expired (WithTimeout,
	// WithScrapeTimeout, or an injected http.Client's Timeout), or that the
	// network reported a timeout. It also matches context.DeadlineExceeded, so
	// check it first to tell it apart from the caller's own deadline.
	ErrTimeout = errors.New("serper: timeout")

	// ErrOutcomeUnknown matches a CallError whose OutcomeUnknown is true: the
	// request may have been processed, and billed, by the provider.
	ErrOutcomeUnknown = errors.New("serper: outcome unknown")
)

// Fixed causes kept inside a CallError. They replace standard-library errors,
// whose messages can quote URLs, header bytes or response content.
var (
	errSDKTimeout       = errors.New("serper: the call's deadline expired")
	errStatus           = errors.New("serper: unexpected HTTP status")
	errContentEncoding  = errors.New("serper: response has an undecoded content encoding")
	errContentType      = errors.New("serper: response content type is not JSON")
	errResponseTooLarge = errors.New("serper: response body exceeds the size limit")
	errInvalidUTF8      = errors.New("serper: response is not valid UTF-8")
	errInvalidJSON      = errors.New("serper: response is not one valid JSON value")
	errNotObject        = errors.New("serper: response is not a JSON object")
	errWrongType        = errors.New("serper: response member has the wrong JSON type")
	errNullItem         = errors.New("serper: response list holds a null item")
	errMissingMember    = errors.New("serper: response lacks a required member")

	// Transport classes. The original error is discarded, because standard
	// library errors can quote URLs, proxy credentials or server bytes.
	errTransportDNS     = errors.New("serper: transport: DNS lookup failed")
	errTransportDial    = errors.New("serper: transport: connection failed")
	errTransportTLS     = errors.New("serper: transport: TLS failed")
	errTransportProxy   = errors.New("serper: transport: proxy connection failed")
	errTransportReset   = errors.New("serper: transport: connection reset")
	errTransportClosed  = errors.New("serper: transport: connection closed early")
	errTransportStalled = errors.New("serper: transport: body read made no progress")
	errInvalidRead      = errors.New("serper: transport: body read returned an invalid count")
	errTransportOther   = errors.New("serper: transport: request failed")
)

// FailureKind classifies a failed call. Each kind matches one target with
// errors.Is (a sentinel of this package, context.Canceled or
// context.DeadlineExceeded), except FailureTimeout, which matches both
// ErrTimeout and context.DeadlineExceeded. ErrOutcomeUnknown is not a kind: it
// matches through CallError.OutcomeUnknown. The values are short, fixed and
// free of request data, so they are safe to use as metric labels.
type FailureKind string

// Failure kinds.
const (
	// FailureUnauthorized matches ErrUnauthorized.
	FailureUnauthorized FailureKind = "unauthorized"

	// FailureForbidden matches ErrForbidden.
	FailureForbidden FailureKind = "forbidden"

	// FailureRateLimited matches ErrRateLimited.
	FailureRateLimited FailureKind = "rate_limited"

	// FailureRejected matches ErrRejected.
	FailureRejected FailureKind = "rejected"

	// FailureUpstream matches ErrUpstream.
	FailureUpstream FailureKind = "upstream"

	// FailureUnexpectedResponse matches ErrUnexpectedResponse.
	FailureUnexpectedResponse FailureKind = "unexpected_response"

	// FailureMalformedResponse matches ErrMalformedResponse.
	FailureMalformedResponse FailureKind = "malformed_response"

	// FailureResponseTooLarge matches ErrResponseTooLarge.
	FailureResponseTooLarge FailureKind = "response_too_large"

	// FailureTransport matches ErrTransport.
	FailureTransport FailureKind = "transport"

	// FailureTimeout matches ErrTimeout, and also context.DeadlineExceeded.
	FailureTimeout FailureKind = "timeout"

	// FailureCanceled means the caller's context was canceled. It matches
	// context.Canceled.
	FailureCanceled FailureKind = "canceled"

	// FailureDeadlineExceeded means the caller's context deadline expired. It
	// matches context.DeadlineExceeded, but not ErrTimeout.
	FailureDeadlineExceeded FailureKind = "deadline_exceeded"
)

// ValidationError describes a local validation failure: nothing was sent.
//
// Err is ErrInvalidConfig or ErrInvalidRequest. Op is the operation, or ""
// for NewClient. Field names the argument, option or request field, such as
// "apiKey", "WithTimeout", "ctx", "Client", "Query" or "URL", and Reason is a
// short fixed word, such as "out_of_range". Neither ever contains the
// rejected value.
type ValidationError struct {
	Err    error
	Op     Operation
	Field  string
	Reason string
}

// Error returns the sentinel's message followed by the operation, the field
// and the reason, such as "serper: invalid request: search: Query: required".
func (e *ValidationError) Error() string {
	if e == nil {
		return "serper: <nil ValidationError>"
	}
	var b strings.Builder
	if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString("serper: validation failed")
	}
	for _, part := range [...]string{string(e.Op), e.Field, e.Reason} {
		if part != "" {
			b.WriteString(": ")
			b.WriteString(part)
		}
	}
	return b.String()
}

// Unwrap returns the sentinel, ErrInvalidConfig or ErrInvalidRequest.
func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CallError describes a call that was attempted, a call that a done context
// stopped before it was sent, or a body that a Decode helper refused.
//
// Its Error text is built only from Meta.Op, Kind, Meta.StatusCode, Field and
// a fixed transport class: it never holds the API key, the query, the scrape
// target, header values, response bodies or provider messages, so it is safe
// to log. GoString and every fmt verb add only the other exported fields,
// which are safe as well. The cause and any captured body sit behind a
// function value that fmt and reflection-based dumpers print only as an
// address.
//
// Tests and fakes may build a CallError from its exported fields alone; it
// then matches errors.Is like one the SDK returns.
type CallError struct {
	// Kind classifies the failure.
	Kind FailureKind

	// Field is a JSON location for decoding failures, such as "organic[3]",
	// "organic.position" or "text". It names a location, never a value.
	Field string

	// OutcomeUnknown reports that the request may have been processed, and
	// billed, by the provider: after a 5xx, a 303, a 1xx or a status outside
	// 100 to 599, after an HTTP 2xx response that could not be used, and
	// after a transport failure, timeout or cancellation once the request was
	// handed to the transport. Such a failure leaves the outcome known only
	// when the transport never read the request body and the failure shows
	// that no connection was made: a DNS or dial failure, a failure to connect
	// to a proxy, or a TLS certificate refused in the handshake.
	// It is never set for a validation failure, a context that was done
	// before sending, a 3xx other than 303, a 4xx or a Decode helper. It drives
	// errors.Is(err, ErrOutcomeUnknown). It is the SDK's classification, not
	// a billing guarantee.
	OutcomeUnknown bool

	// Meta describes the call: operation, origin, path, HTTP status,
	// attempts, elapsed time, credits and raw capture state.
	Meta Metadata

	// private holds the sanitized cause, the transport class and the
	// optional captured body.
	private func() callDetail
}

// callDetail is the part of a CallError that is never printed.
type callDetail struct {
	cause error
	class transportClass
	raw   []byte
}

// newCallError returns a CallError whose cause is reachable only through
// Unwrap.
func newCallError(kind FailureKind, meta Metadata, cause error) *CallError {
	return newCallErrorDetail(kind, meta, callDetail{cause: cause})
}

// newCallErrorDetail returns a CallError holding d.
func newCallErrorDetail(kind FailureKind, meta Metadata, d callDetail) *CallError {
	return &CallError{Kind: kind, Meta: meta, private: func() callDetail { return d }}
}

// Error returns "serper: <op>: <kind>", followed by ": <class>" for a
// transport failure, " (HTTP <status>)" when a status is known and
// ": <field>" when a field is named, such as
// "serper: scrape: malformed_response (HTTP 200): text" or
// "serper: search: transport: reset".
func (e *CallError) Error() string {
	if e == nil {
		return "serper: <nil CallError>"
	}
	var b strings.Builder
	b.WriteString("serper: ")
	if e.Meta.Op != "" {
		b.WriteString(string(e.Meta.Op))
		b.WriteString(": ")
	}
	if e.Kind != "" {
		b.WriteString(string(e.Kind))
	} else {
		b.WriteString("call_failed")
	}
	if class := e.detail().class; e.Kind == FailureTransport && class != classNone {
		b.WriteString(": ")
		b.WriteString(class.String())
	}
	if e.Meta.StatusCode != 0 {
		b.WriteString(" (HTTP ")
		b.WriteString(strconv.Itoa(e.Meta.StatusCode))
		b.WriteString(")")
	}
	if e.Field != "" {
		b.WriteString(": ")
		b.WriteString(e.Field)
	}
	return b.String()
}

// Unwrap returns the sanitized cause: a context error or a fixed error of this
// package. It is never a *url.Error, a net error, a JSON decoding error or
// anything else that could quote a URL, a header or response content. It is
// nil for a CallError built from its exported fields.
func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.detail().cause
}

// Is reports whether target is the sentinel for e.Kind, or ErrOutcomeUnknown
// when e.OutcomeUnknown is set. A timeout also matches
// context.DeadlineExceeded, a caller deadline matches context.DeadlineExceeded
// and a cancellation matches context.Canceled.
func (e *CallError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrOutcomeUnknown:
		return e.OutcomeUnknown
	case context.Canceled:
		return e.Kind == FailureCanceled
	case context.DeadlineExceeded:
		return e.Kind == FailureDeadlineExceeded || e.Kind == FailureTimeout
	}
	sentinel := kindSentinel(e.Kind)
	return sentinel != nil && target == sentinel
}

// GoString prints the exported fields only, so that %#v cannot reach the cause
// or a captured body.
func (e *CallError) GoString() string {
	if e == nil {
		return "(*serper.CallError)(nil)"
	}
	return fmt.Sprintf("&serper.CallError{Kind:%q, Field:%q, OutcomeUnknown:%t, Meta:%s}",
		string(e.Kind), e.Field, e.OutcomeUnknown, e.Meta.goString())
}

// RawJSON returns a new copy of the HTTP 200 body that the SDK read completely
// and found to be valid UTF-8 and valid JSON, but then refused (a decoding or
// success-rule failure), with true. It returns (nil, false) for every other
// failure, and whenever raw capture was off for the call (see WithRawCapture).
// Each call allocates a new copy. The body is provider data: it can echo the
// query or hold other sensitive content, so think before logging it.
func (e *CallError) RawJSON() ([]byte, bool) {
	if e == nil {
		return nil, false
	}
	raw := e.detail().raw
	if raw == nil {
		return nil, false
	}
	return cloneBytes(raw), true
}

// detail returns the private part of e, which is empty for a CallError built
// from its exported fields.
func (e *CallError) detail() callDetail {
	if e.private == nil {
		return callDetail{}
	}
	return e.private()
}

// kindSentinel returns the sentinel a kind matches, or nil for the context
// kinds and any kind this package does not define.
func kindSentinel(kind FailureKind) error {
	switch kind {
	case FailureUnauthorized:
		return ErrUnauthorized
	case FailureForbidden:
		return ErrForbidden
	case FailureRateLimited:
		return ErrRateLimited
	case FailureRejected:
		return ErrRejected
	case FailureUpstream:
		return ErrUpstream
	case FailureUnexpectedResponse:
		return ErrUnexpectedResponse
	case FailureMalformedResponse:
		return ErrMalformedResponse
	case FailureResponseTooLarge:
		return ErrResponseTooLarge
	case FailureTransport:
		return ErrTransport
	case FailureTimeout:
		return ErrTimeout
	case FailureCanceled, FailureDeadlineExceeded:
		return nil
	}
	return nil
}

// contextKind returns the kind and the cause of a done context's error.
func contextKind(err error) (FailureKind, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureDeadlineExceeded, context.DeadlineExceeded
	}
	return FailureCanceled, context.Canceled
}

// Validation reasons, reported in ValidationError.Reason.
const (
	reasonRequired          = "required"
	reasonNil               = "nil"
	reasonNotInitialized    = "not_initialized"
	reasonOutOfRange        = "out_of_range"
	reasonInvalidFormat     = "invalid_format"
	reasonInvalidUTF8       = "invalid_utf8"
	reasonInvalidURL        = "invalid_url"
	reasonUnsupportedScheme = "unsupported_scheme"
	reasonMissingHost       = "missing_host"
	reasonHasUserinfo       = "has_userinfo"
	reasonHasQuery          = "has_query"
	reasonHasFragment       = "has_fragment"
	reasonHasPath           = "has_path"
	reasonInvalidHost       = "invalid_host"
	reasonInvalidPort       = "invalid_port"
	reasonTooLarge          = "too_large"
)

// invalidConfig returns a *ValidationError matching ErrInvalidConfig.
func invalidConfig(op Operation, field, reason string) error {
	return &ValidationError{Err: ErrInvalidConfig, Op: op, Field: field, Reason: reason}
}

// invalidRequest returns a *ValidationError matching ErrInvalidRequest.
func invalidRequest(op Operation, field, reason string) error {
	return &ValidationError{Err: ErrInvalidRequest, Op: op, Field: field, Reason: reason}
}
