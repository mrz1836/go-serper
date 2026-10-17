package serper

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Response bounds.
const (
	// maxErrorBodyBytes bounds the discard of a body whose status is not
	// 200, read only so that the connection can be reused, and never kept.
	maxErrorBodyBytes = 4 << 10

	// maxPrealloc bounds the buffer sized from a declared length before any
	// byte arrives, so that a response that declares a large body and then
	// stalls cannot pin that much memory until the timeout.
	maxPrealloc = 1 << 20

	// maxEmptyReads is how many reads in a row may return nothing before a
	// body counts as stalled.
	maxEmptyReads = 100
)

// exchange makes the one attempt of a call of ep with body, within the
// endpoint's timeout, and returns the whole HTTP 200 body, within the size
// limit, and the call's metadata. Every failure is a *CallError, in this
// order: a caller's context that is already done (nothing is sent); the
// caller's context, the SDK's timeout and transport failures before a
// response; then a status other than 200, which stands even when it arrives
// late; then, for HTTP 200, the Content-Encoding, the Content-Type, the
// declared length and the body read; and last, an HTTP 200 response that the
// call's context did not outlive. The metadata's Credits are left for the
// decoder, and its RawCapture is RawCaptureOff on success.
func (c *Client) exchange(ctx context.Context, ep *endpoint, body []byte, capture bool) ([]byte, Metadata, *CallError) {
	meta := Metadata{Op: ep.op, Origin: ep.origin, Path: ep.path}
	if err := ctx.Err(); err != nil {
		kind, cause := contextKind(err)
		meta.RawCapture = captureState(capture, RawCaptureNoResponse)
		return nil, meta, newCallError(kind, meta, cause)
	}
	callCtx, cancel := context.WithTimeoutCause(ctx, ep.timeout, errSDKTimeout)
	defer cancel()

	meta.Attempts = 1
	sent := newSentBody(body)
	start := time.Now()
	resp, err := c.send(callCtx, ep, sent) //nolint:bodyclose // the body guard closes it exactly once
	if err != nil {
		meta.Elapsed = elapsedSince(start)
		meta.RawCapture = captureState(capture, RawCaptureNoResponse)
		ce := classify(ctx, callCtx, err, meta)
		// The request may have been processed once the transport read its
		// body, whatever the error says; before that, only a failure that
		// shows no connection was made settles it.
		ce.OutcomeUnknown = sent.wasRead() || !ce.detail().class.beforeConnection()
		return nil, meta, ce
	}
	meta.StatusCode = resp.StatusCode
	guard := watchBody(callCtx, resp.Body)
	var data []byte
	var ce *CallError
	// A status other than 200 decides the outcome without its body, so it
	// stands even when it arrived after the deadline. An HTTP 200 body is
	// read only while the call's context is live.
	if resp.StatusCode != http.StatusOK || callCtx.Err() == nil {
		data, ce = c.readResponse(ctx, callCtx, resp, meta)
	}
	guard.release()
	meta.Elapsed = elapsedSince(start)
	if ce == nil && callCtx.Err() != nil {
		// The call's deadline or the caller's context ended before the HTTP
		// 200 response was complete: either a transport that ignores the
		// context responded late, or the body was read before the close took
		// effect. Either way the call failed, whatever was read.
		ce = classify(ctx, callCtx, callCtx.Err(), meta)
		ce.OutcomeUnknown = true
	}
	if ce != nil {
		meta.RawCapture = captureState(capture, RawCaptureFailed)
		ce.Meta = meta
		return nil, meta, ce
	}
	return data, meta, nil
}

// elapsedSince returns the monotonic time since start, at least 1ns, so that
// a measured call never reports the 0 that means no attempt.
func elapsedSince(start time.Time) time.Duration {
	return max(time.Since(start), time.Nanosecond)
}

// captureState returns state when capture was requested, and RawCaptureOff
// otherwise.
func captureState(capture bool, state RawCaptureState) RawCaptureState {
	if !capture {
		return RawCaptureOff
	}
	return state
}

// bodyGuard closes a response body exactly once: when the call's context
// ends, so that a read that does not watch the context still stops, or when
// release runs, which the caller must do before returning. A close error
// after a complete read changes nothing.
type bodyGuard struct {
	body io.Closer
	once sync.Once
	stop func() bool
}

// watchBody returns the guard of body, closing it when callCtx ends. The
// context.AfterFunc starts a goroutine only if callCtx ends first.
func watchBody(callCtx context.Context, body io.Closer) *bodyGuard {
	g := &bodyGuard{body: body}
	g.stop = context.AfterFunc(callCtx, g.close)
	return g
}

// close closes the body, the first time only.
func (g *bodyGuard) close() { g.once.Do(func() { _ = g.body.Close() }) }

// release stops watching the call's context and closes the body.
func (g *bodyGuard) release() {
	g.stop()
	g.close()
}

// readResponse classifies resp and, for HTTP 200, reads its whole body
// within the size limit. Any other status fails by the status alone, after a
// bounded discard of its body that is never kept; a 1xx body, which is the
// raw connection, is never read.
func (c *Client) readResponse(ctx, callCtx context.Context, resp *http.Response, meta Metadata) ([]byte, *CallError) {
	status := resp.StatusCode
	if status != http.StatusOK {
		if status >= http.StatusOK {
			discard(resp.Body, resp.ContentLength)
		}
		kind := statusKind(status)
		ce := newCallError(kind, meta, errStatus)
		ce.OutcomeUnknown = statusOutcomeUnknown(status)
		return nil, ce
	}
	switch {
	case encoded(resp.Header):
		return nil, sentFailure(FailureUnexpectedResponse, meta, errContentEncoding, classNone)
	case !jsonContentType(resp.Header):
		return nil, sentFailure(FailureUnexpectedResponse, meta, errContentType, classNone)
	case resp.ContentLength > c.maxResponseBytes:
		return nil, sentFailure(FailureResponseTooLarge, meta, errResponseTooLarge, classNone)
	}
	data, err := readBody(resp.Body, resp.ContentLength, c.maxResponseBytes)
	switch {
	case err != nil:
		ce := classify(ctx, callCtx, err, meta)
		ce.OutcomeUnknown = true
		return nil, ce
	case resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength:
		// The standard transport reports a short body as io.ErrUnexpectedEOF;
		// an injected one might end it cleanly instead, or run past the
		// declared length. Either way the framing is broken.
		return nil, sentFailure(FailureTransport, meta, errTransportClosed, classClosed)
	}
	return data, nil
}

// sentFailure returns the CallError of an HTTP 200 response that could not be
// used: the request reached the provider, so its outcome is unknown.
func sentFailure(kind FailureKind, meta Metadata, cause error, class transportClass) *CallError {
	ce := newCallErrorDetail(kind, meta, callDetail{cause: cause, class: class})
	ce.OutcomeUnknown = true
	return ce
}

// statusKind classifies an HTTP status other than 200.
func statusKind(code int) FailureKind {
	switch {
	case code == http.StatusUnauthorized:
		return FailureUnauthorized
	case code == http.StatusForbidden:
		return FailureForbidden
	case code == http.StatusTooManyRequests:
		return FailureRateLimited
	case code >= 400 && code < 500:
		return FailureRejected
	case code >= 500 && code < 600:
		return FailureUpstream
	}
	return FailureUnexpectedResponse
}

// statusOutcomeUnknown reports whether a status other than 200 leaves open
// whether the provider processed the request: a 5xx, a 2xx other than 200, a
// 1xx, a 303 See Other (the usual response to a POST that was processed) and any
// status outside 100 to 599 do; any other 3xx and every 4xx, the server's own
// refusal, do not.
func statusOutcomeUnknown(code int) bool {
	return code < 300 || code >= 500 || code == http.StatusSeeOther
}

// classify maps a failed exchange onto a CallError: the caller's context
// first, then the SDK's own timeout or any network timeout, then an
// oversized body, else a transport failure of a fixed class. The original
// error is never kept; its class is, so that the caller can tell whether a
// connection was made.
func classify(caller, callCtx context.Context, err error, meta Metadata) *CallError {
	class := classOf(err)
	if cerr := caller.Err(); cerr != nil {
		kind, cause := contextKind(cerr)
		return newCallErrorDetail(kind, meta, callDetail{cause: cause, class: class})
	}
	// isNetTimeout also catches context.DeadlineExceeded, whose Timeout
	// method reports true, anywhere in err's tree.
	if errors.Is(context.Cause(callCtx), errSDKTimeout) || isNetTimeout(err) {
		return newCallErrorDetail(FailureTimeout, meta, callDetail{cause: context.DeadlineExceeded, class: class})
	}
	if errors.Is(err, errResponseTooLarge) {
		return newCallError(FailureResponseTooLarge, meta, errResponseTooLarge)
	}
	return newCallErrorDetail(FailureTransport, meta, callDetail{cause: class.cause(), class: class})
}

// readBody reads r to EOF, but never more than limit bytes: one more fails
// with errResponseTooLarge. A declared length, already known to be within the
// limit, sizes the buffer exactly when it is at most maxPrealloc, with one
// spare byte to see EOF without growing. A body of unknown or large declared
// length is read by io.ReadAll, which grows as bytes arrive.
func readBody(r io.Reader, contentLength, limit int64) ([]byte, error) {
	capped := &capReader{r: &progressReader{r: r}, limit: limit}
	if contentLength < 0 || contentLength > maxPrealloc {
		return io.ReadAll(capped)
	}
	buf := make([]byte, 0, contentLength+1)
	for len(buf) < cap(buf) {
		n, err := capped.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
	// The body is longer than declared: the framing is broken, whatever
	// follows.
	return buf, nil
}

// capReader passes on at most limit bytes of r. The read that finds a byte
// beyond the limit returns the bytes within it together with
// errResponseTooLarge, and so does every read after it. Unlike io.LimitReader,
// it never reports a body cut at the limit as a clean end.
type capReader struct {
	r        io.Reader
	limit    int64
	read     int64
	exceeded bool
}

// Read reads from the wrapped reader within the limit, plus one byte to
// detect a longer body.
func (c *capReader) Read(p []byte) (int, error) {
	if c.exceeded {
		return 0, errResponseTooLarge
	}
	if room := c.limit - c.read + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	if c.read+int64(n) > c.limit {
		n = int(c.limit - c.read)
		c.read = c.limit
		c.exceeded = true
		return n, errResponseTooLarge
	}
	c.read += int64(n)
	return n, err
}

// progressReader fails a body that makes no progress: maxEmptyReads reads in
// a row that return neither bytes nor an error. Without it, a body from an
// injected transport that keeps answering (0, nil) would spin a CPU until the
// call's deadline.
type progressReader struct {
	r     io.Reader
	empty int
}

// Read reads from the wrapped reader, counting reads that make no progress. A
// count outside 0 to len(b), which no correct reader returns, fails the body
// instead of panicking later.
func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n < 0 || n > len(b) {
		return 0, errInvalidRead
	}
	if n > 0 || err != nil || len(b) == 0 {
		p.empty = 0
		return n, err
	}
	if p.empty++; p.empty >= maxEmptyReads {
		return 0, errTransportStalled
	}
	return 0, nil
}

// discard reads and drops at most maxErrorBodyBytes of an error body, so that
// the connection can be reused, and skips one declared longer. A stalled read
// ends with the call's context, which closes the body.
func discard(body io.Reader, contentLength int64) {
	if contentLength > maxErrorBodyBytes {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(&progressReader{r: body}, maxErrorBodyBytes))
}

// encoded reports whether h carries a Content-Encoding other than identity.
// The transport removes the header when it decodes gzip itself, so one that
// remains names an encoding nobody decoded. Every value counts, as a list of
// codings, under any spelling of the header name an injected transport may
// set; empty list elements are ignored.
func encoded(h http.Header) bool {
	for k, vs := range h {
		if !strings.EqualFold(k, "Content-Encoding") {
			continue
		}
		for _, v := range vs {
			for coding := range strings.SplitSeq(v, ",") {
				coding = strings.Trim(coding, " \t")
				if coding != "" && !strings.EqualFold(coding, "identity") {
					return true
				}
			}
		}
	}
	return false
}

// jsonContentType reports whether h declares JSON or nothing: no
// Content-Type at all, or exactly one that parses as application/json or a
// type/subtype whose subtype ends in "+json", such as
// application/problem+json. Every spelling of the header name an injected
// transport may set counts.
func jsonContentType(h http.Header) bool {
	var values []string
	for k, vs := range h {
		if strings.EqualFold(k, "Content-Type") {
			values = append(values, vs...)
		}
	}
	switch len(values) {
	case 0:
		return true
	case 1:
	default:
		return false
	}
	media, _, err := mime.ParseMediaType(values[0])
	if err != nil {
		return false
	}
	typ, sub, ok := strings.Cut(media, "/")
	return ok && typ != "" && (media == mediaJSON || (len(sub) > len("+json") && strings.HasSuffix(sub, "+json")))
}

// result is a response type: it receives its call's metadata and captured
// body once decoding succeeds.
type result interface {
	finish(meta Metadata, raw rawBody)
}

// decoder decodes the HTTP 200 body of one operation and reads its credits,
// which it reports even when decoding fails.
type decoder[R result] func(data []byte) (R, Credits, *decodeError)

// call runs the shared part of every operation after its own validation:
// encode wire within the request limit, make the one attempt on ep, and
// decode the response. With capture on, the body the decoder saw is kept on
// the result, or on the *CallError when it was valid JSON that decoding
// refused.
func call[R result](ctx context.Context, c *Client, ep *endpoint, wire wireBody, opts []CallOption,
	decode decoder[R],
) (R, error) {
	var zero R
	body, err := encodeBody(ep.op, wire, c.maxRequestBytes)
	if err != nil {
		return zero, err
	}
	capture := rawCapture(c.defaultRawCapture, opts)
	data, meta, ce := c.exchange(ctx, ep, body, capture)
	if ce != nil {
		return zero, ce
	}
	res, credits, derr := decode(data)
	meta.Credits = credits
	if derr != nil {
		d := callDetail{cause: derr.cause}
		meta.RawCapture = captureState(capture, RawCaptureFailed)
		if capture && derr.validJSON {
			meta.RawCapture = RawCaptureComplete
			d.raw = exactSize(data)
		}
		ce := newCallErrorDetail(derr.kind, meta, d)
		ce.Field = derr.field
		ce.OutcomeUnknown = true
		return zero, ce
	}
	var raw rawBody
	if capture {
		meta.RawCapture = RawCaptureComplete
		raw = newRawBody(data)
	}
	res.finish(meta, raw)
	return res, nil
}

// decodeOffline runs decode on data for a Decode helper: no request, so the
// metadata names only the operation and the credits, and a failure never
// leaves an outcome unknown.
func decodeOffline[R result](op Operation, data []byte, decode decoder[R]) (R, error) {
	res, credits, derr := decode(data)
	meta := Metadata{Op: op, Credits: credits}
	if derr != nil {
		var zero R
		return zero, derr.callError(meta)
	}
	res.finish(meta, nil)
	return res, nil
}
