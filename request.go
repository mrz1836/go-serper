package serper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
)

// mediaJSON is the media type the SDK sends in Accept and Content-Type.
const mediaJSON = "application/json"

// newRequest builds the one HTTP request of a call: POST of body to ep's
// origin and fixed path. The URL is assembled from its parts, never parsed
// from a string, and never holds request data.
//
// The standard library resends a request on its own when it thinks that is
// safe: HTTP/1 retries a request with a GetBody (or an idempotency key) on a
// reused connection that fails, and the HTTP/2 transport resends such a
// request after a refused stream, a GOAWAY or a protocol error, even when the
// server may already have processed it. The body is therefore a plain reader
// with GetBody nil and no Idempotency-Key is sent, so every call is exactly
// one attempt, and a 307 or 308 cannot be followed either.
func newRequest(ctx context.Context, ep *endpoint, body *sentBody, apiKey, userAgent string) *http.Request {
	req := &http.Request{
		Method:        http.MethodPost,
		URL:           &url.URL{Scheme: "https", Host: ep.host, Path: ep.path},
		Header:        requestHeader(apiKey, userAgent),
		Body:          body,
		ContentLength: body.size,
		Host:          ep.host,
	}
	return req.WithContext(ctx)
}

// sentBody is the body of one request: a plain reader over the encoded JSON,
// never http.NoBody and without a GetBody, that records whether the transport
// ever read it. A transport that never read the body cannot have sent the
// request, so its failure leaves no outcome open. The transport may read it on
// a goroutine of its own, so the flag is atomic.
type sentBody struct {
	r    bytes.Reader
	size int64
	read atomic.Bool
}

// newSentBody returns the body of a request carrying b.
func newSentBody(b []byte) *sentBody {
	s := &sentBody{size: int64(len(b))}
	s.r.Reset(b)
	return s
}

// Read reads from the body and records that it was read.
func (s *sentBody) Read(p []byte) (int, error) {
	s.read.Store(true)
	return s.r.Read(p)
}

// Close does nothing: the bytes are in memory.
func (s *sentBody) Close() error { return nil }

// wasRead reports whether the transport ever read the body.
func (s *sentBody) wasRead() bool { return s.read.Load() }

// requestHeader returns a fresh header map for one call: exactly X-Api-Key,
// Content-Type, Accept and User-Agent. The values share one backing array,
// allocated per call so that no two requests share header memory; each is a
// full slice expression, so an append to one cannot overwrite the next.
//
// It never sets Authorization, Cookie, Referer, Expect or an idempotency key:
// with an Idempotency-Key header, the standard library may resend a POST.
// Accept-Encoding is left to the transport, which then decodes gzip itself.
func requestHeader(apiKey, userAgent string) http.Header {
	v := append(make([]string, 0, 4), apiKey, mediaJSON, mediaJSON, userAgent)
	h := make(http.Header, 4)
	h["X-Api-Key"] = v[0:1:1]
	h["Content-Type"] = v[1:2:2]
	h["Accept"] = v[2:3:3]
	h["User-Agent"] = v[3:4:4]
	return h
}

// wireBody is the JSON body shape of an operation.
type wireBody interface {
	// textBytes returns the total length of the body's text members. Every
	// byte of text encodes to at least one byte of JSON, so a body whose text
	// alone is over a limit is refused before anything is encoded.
	textBytes() int
}

// encodeBody encodes the wire struct w as the JSON body of a call of op,
// without HTML escaping and without a trailing newline, and refuses a body
// larger than limit bytes, checked on the text alone first, so that an
// oversized request never costs its encoding. encoding/json still escapes
// U+2028 and U+2029.
func encodeBody(op Operation, w wireBody, limit int64) ([]byte, error) {
	if int64(w.textBytes()) > limit {
		return nil, invalidRequest(op, "request", reasonTooLarge)
	}
	b := marshalNoEscape(w)
	if int64(len(b)) > limit {
		return nil, invalidRequest(op, "request", reasonTooLarge)
	}
	return b, nil
}

// setOrUnset names whether a field is filled in.
func setOrUnset(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

// bytesOrUnset prints a text field's length, such as "12 bytes", or "unset".
func bytesOrUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return strconv.Itoa(len(s)) + " bytes"
}

// boolOrUnset prints an optional boolean: "unset", "true" or "false".
func boolOrUnset(b *bool) string {
	if b == nil {
		return "unset"
	}
	return strconv.FormatBool(*b)
}

// intOrUnset prints an optional number, where 0 means unset.
func intOrUnset(n int) string {
	if n == 0 {
		return "unset"
	}
	return strconv.Itoa(n)
}

// formatAs prints s for every fmt verb, quoted for %q.
func formatAs(f fmt.State, verb rune, s string) {
	if verb == 'q' {
		s = strconv.Quote(s)
	}
	_, _ = io.WriteString(f, s)
}
