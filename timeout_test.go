package serper_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// Fake-clock durations of the timeout suite. Every test runs inside a
// testing/synctest bubble, where time moves only when every goroutine of the
// bubble is blocked, so a call that ends on a deadline reports exactly that
// deadline as its Elapsed, on every run.
const (
	tmoTimeout = 5 * time.Second  // the WithTimeout of most tests
	tmoEarlier = 2 * time.Second  // a deadline or cancel before tmoTimeout
	tmoLater   = 9 * time.Second  // a deadline or cancel after tmoTimeout
	tmoNever   = 20 * time.Minute // a gap longer than any accepted timeout
)

// tmoCauseMarker is planted in the cause of a caller's context and in a
// network timeout: no error may ever print it.
const tmoCauseMarker = "causemarker-91d3e7"

// tmoHang is one way an exchange hangs until its context ends: before the
// response headers, or during the read of an HTTP 200 body.
type tmoHang struct {
	name string
	// body reports a hang during the body read, after an HTTP 200.
	body bool
	// start returns the transport of one call of o, and the body it answers
	// with, nil before the headers. Call it inside the bubble, so that the
	// body's channels and timers belong to it.
	start func(o opCase) (http.RoundTripper, *testutil.Body)
}

// tmoHangs returns the hangs that last until any deadline the suite sets.
func tmoHangs() []tmoHang {
	return []tmoHang{
		{name: "BeforeHeaders", start: func(opCase) (http.RoundTripper, *testutil.Body) {
			return testutil.RoundTripFunc(testutil.BlockUntilDone), nil
		}},
		{name: "BlockingBody", body: true, start: func(o opCase) (http.RoundTripper, *testutil.Body) {
			b := testutil.NewBlockingBody(o.ok[:len(o.ok)/2])
			return testutil.TransportWith(http.StatusOK, nil, int64(len(o.ok)), b), b
		}},
		{name: "EndlessGap", body: true, start: func(o opCase) (http.RoundTripper, *testutil.Body) {
			b := testutil.NewBody(o.ok)
			b.Gap = tmoNever
			return testutil.TransportWith(http.StatusOK, nil, -1, b), b
		}},
	}
}

// tmoRemainingGap is a body hang whose second gap is longer than the time
// left before tmoTimeout, though shorter than tmoTimeout itself: the first
// half of the body arrives after 3s, the second would after 6s.
func tmoRemainingGap() tmoHang {
	start := func(o opCase) (http.RoundTripper, *testutil.Body) {
		b := testutil.NewBody(o.ok)
		b.Step = len(o.ok)/2 + 1
		b.Gap = tmoTimeout * 3 / 5
		return testutil.TransportWith(http.StatusOK, nil, int64(len(o.ok)), b), b
	}
	return tmoHang{name: "GapLongerThanTheRemainingTime", body: true, start: start}
}

// status is the HTTP status a call that hangs this way reports.
func (h tmoHang) status() int {
	if h.body {
		return http.StatusOK
	}
	return 0
}

// capture is the RawCapture state of a call that hangs this way, with raw
// capture requested or not.
func (h tmoHang) capture(on bool) serper.RawCaptureState {
	switch {
	case !on:
		return serper.RawCaptureOff
	case h.body:
		return serper.RawCaptureFailed
	}
	return serper.RawCaptureNoResponse
}

// tmoCaptureName names a subtest after the capture setting.
func tmoCaptureName(on bool) string {
	if on {
		return "CaptureOn"
	}
	return "CaptureOff"
}

// tmoSettle waits until every other goroutine of the bubble has finished or
// is blocked, then checks that body, when there is one, was closed exactly
// once: the guard's context.AfterFunc either ran once or was stopped. The
// bubble then ends, and synctest fails the test if any goroutine is left.
func tmoSettle(tb testing.TB, body *testutil.Body) {
	tb.Helper()
	synctest.Wait()
	if body != nil {
		assert.Equal(tb, 1, body.Closes(), "the body is closed exactly once")
	}
}

// tmoAssertTimeout asserts that err is the SDK's own timeout: ErrTimeout
// and context.DeadlineExceeded, never context.Canceled.
func tmoAssertTimeout(tb testing.TB, err error) {
	tb.Helper()
	require.ErrorIs(tb, err, serper.ErrTimeout)
	require.ErrorIs(tb, err, context.DeadlineExceeded)
	require.NotErrorIs(tb, err, context.Canceled)
}

// tmoAssertContextKind asserts the errors that err, a failure of kind
// timeout, deadline_exceeded or canceled, matches: the SDK's timeout matches
// ErrTimeout and context.DeadlineExceeded; the caller's deadline matches
// context.DeadlineExceeded only, and its cancellation context.Canceled only.
func tmoAssertContextKind(tb testing.TB, err error, kind serper.FailureKind) {
	tb.Helper()
	if kind == serper.FailureTimeout {
		tmoAssertTimeout(tb, err)
		return
	}
	want, other := context.Canceled, context.DeadlineExceeded
	if kind == serper.FailureDeadlineExceeded {
		want, other = other, want
	}
	require.ErrorIs(tb, err, want)
	require.NotErrorIs(tb, err, other)
	require.NotErrorIs(tb, err, serper.ErrTimeout, "the caller's context is not the SDK's deadline")
}

func TestTimeoutSDKDeadline(t *testing.T) {
	for _, o := range opCases() {
		for _, h := range append(tmoHangs(), tmoRemainingGap()) {
			for _, capture := range []bool{false, true} {
				t.Run(o.name+"/"+h.name+"/"+tmoCaptureName(capture), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						rt, body := h.start(o)
						c := newTransportClient(t, rt, serper.WithTimeout(tmoTimeout))
						got := o.call(t.Context(), c, serper.WithRawCapture(capture))
						ce := assertCallError(t, got.err, o.want(wantCall{
							Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: h.status(), Attempts: 1,
							RawCapture: h.capture(capture),
						}))
						tmoAssertTimeout(t, got.err)
						assert.Equal(t, tmoTimeout, ce.Meta.Elapsed, "the call ends exactly at its deadline")
						raw, ok := ce.RawJSON()
						assertCapture(t, "", raw, ok, "nothing is captured from a call that timed out")
						tmoSettle(t, body)
					})
				})
			}
		}
	}
}

// tmoCallerEnd is one way the caller's own context ends, derived inside the
// bubble from the test's context.
type tmoCallerEnd struct {
	name string
	// kind is the failure kind of a call under the SDK's tmoTimeout.
	kind serper.FailureKind
	// at is when the call ends.
	at time.Duration
	// derive returns the caller's context and the function that releases
	// it.
	derive func(parent context.Context) (context.Context, func())
}

// tmoCancelAfter returns a child of parent that a goroutine cancels with
// cause (nil for none) after d of fake time. The goroutine ends early when
// the context is released.
func tmoCancelAfter(parent context.Context, d time.Duration, cause error) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	go func() {
		select {
		case <-time.After(d):
			cancel(cause)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(nil) }
}

// tmoCallerEnds returns the caller contexts of the suite. A cause, even a
// network timeout, never changes the kind: the caller's context decides.
func tmoCallerEnds() []tmoCallerEnd {
	timeoutCause := testutil.TimeoutError{Msg: "caller cause " + tmoCauseMarker}
	return []tmoCallerEnd{
		{
			name: "EarlierDeadline", kind: serper.FailureDeadlineExceeded, at: tmoEarlier,
			derive: func(p context.Context) (context.Context, func()) {
				return context.WithTimeout(p, tmoEarlier)
			},
		},
		{
			name: "EarlierDeadlineWithCause", kind: serper.FailureDeadlineExceeded, at: tmoEarlier,
			derive: func(p context.Context) (context.Context, func()) {
				return context.WithTimeoutCause(p, tmoEarlier, timeoutCause)
			},
		},
		{
			name: "EarlierCancel", kind: serper.FailureCanceled, at: tmoEarlier,
			derive: func(p context.Context) (context.Context, func()) {
				return tmoCancelAfter(p, tmoEarlier, nil)
			},
		},
		{
			name: "EarlierCancelWithCause", kind: serper.FailureCanceled, at: tmoEarlier,
			derive: func(p context.Context) (context.Context, func()) {
				return tmoCancelAfter(p, tmoEarlier, timeoutCause)
			},
		},
		{
			name: "LaterDeadline", kind: serper.FailureTimeout, at: tmoTimeout,
			derive: func(p context.Context) (context.Context, func()) {
				return context.WithTimeout(p, tmoLater)
			},
		},
		{
			name: "LaterCancel", kind: serper.FailureTimeout, at: tmoTimeout,
			derive: func(p context.Context) (context.Context, func()) {
				return tmoCancelAfter(p, tmoLater, nil)
			},
		},
	}
}

func TestTimeoutCallerContext(t *testing.T) {
	for _, o := range opCases() {
		for _, end := range tmoCallerEnds() {
			for _, h := range tmoHangs() {
				t.Run(o.name+"/"+end.name+"/"+h.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, release := end.derive(t.Context())
						defer release()
						rt, body := h.start(o)
						c := newTransportClient(t, rt, serper.WithTimeout(tmoTimeout))
						got := o.call(ctx, c, serper.WithRawCapture(true))
						ce := assertCallError(t, got.err, o.want(wantCall{
							Kind: end.kind, OutcomeUnknown: true, Status: h.status(), Attempts: 1,
							RawCapture: h.capture(true),
						}), tmoCauseMarker)
						assert.Equal(t, end.at, ce.Meta.Elapsed, "the call ends exactly when the first context does")
						tmoAssertContextKind(t, got.err, end.kind)
						release() // ends the canceling goroutine before the bubble settles
						tmoSettle(t, body)
					})
				})
			}
		}
	}
}

func TestTimeoutInjectedClientTimeout(t *testing.T) {
	for _, tt := range []struct {
		name      string
		hcTimeout time.Duration
		want      time.Duration
	}{
		{name: "LowerThanWithTimeout", hcTimeout: tmoEarlier, want: tmoEarlier},
		{name: "HigherThanWithTimeout", hcTimeout: tmoLater, want: tmoTimeout},
		{name: "EqualToWithTimeout", hcTimeout: tmoTimeout, want: tmoTimeout},
	} {
		for _, o := range opCases() {
			for _, h := range tmoHangs() {
				t.Run(tt.name+"/"+o.name+"/"+h.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						rt, body := h.start(o)
						c := newHTTPClientClient(t, testKey, &http.Client{Transport: rt, Timeout: tt.hcTimeout},
							serper.WithTimeout(tmoTimeout))
						got := o.call(t.Context(), c, serper.WithRawCapture(true))
						ce := assertCallError(t, got.err, o.want(wantCall{
							Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: h.status(), Attempts: 1,
							RawCapture: h.capture(true),
						}))
						tmoAssertTimeout(t, got.err)
						assert.Equal(t, tt.want, ce.Meta.Elapsed, "min(WithTimeout, hc.Timeout) bounds the call")
						tmoSettle(t, body)
					})
				})
			}
		}
	}
}

func TestTimeoutScrapeTimeout(t *testing.T) {
	const injected, between = 3 * time.Second, 7 * time.Second
	for _, tt := range []struct {
		name      string
		hcTimeout time.Duration
		opts      []serper.Option
		search    time.Duration // the deadline of Search and Images
		scrape    time.Duration // the deadline of Scrape
	}{
		{name: "Defaults", search: serper.DefaultTimeout, scrape: serper.DefaultTimeout},
		{
			name: "UnsetFollowsWithTimeout", opts: []serper.Option{serper.WithTimeout(tmoTimeout)},
			search: tmoTimeout, scrape: tmoTimeout,
		},
		{
			name:   "Longer",
			opts:   []serper.Option{serper.WithTimeout(tmoTimeout), serper.WithScrapeTimeout(tmoLater)},
			search: tmoTimeout, scrape: tmoLater,
		},
		{
			name:   "Shorter",
			opts:   []serper.Option{serper.WithTimeout(tmoTimeout), serper.WithScrapeTimeout(tmoEarlier)},
			search: tmoTimeout, scrape: tmoEarlier,
		},
		{
			name:   "SetBeforeWithTimeout",
			opts:   []serper.Option{serper.WithScrapeTimeout(tmoEarlier), serper.WithTimeout(tmoLater)},
			search: tmoLater, scrape: tmoEarlier,
		},
		{
			name: "AloneKeepsTheDefault", opts: []serper.Option{serper.WithScrapeTimeout(tmoLater)},
			search: serper.DefaultTimeout, scrape: tmoLater,
		},
		{
			name: "Maximum", opts: []serper.Option{serper.WithScrapeTimeout(serper.MaxTimeout)},
			search: serper.DefaultTimeout, scrape: serper.MaxTimeout,
		},
		{
			name:      "InjectedTimeoutCapsBoth",
			hcTimeout: injected,
			opts:      []serper.Option{serper.WithTimeout(tmoTimeout), serper.WithScrapeTimeout(tmoLater)},
			search:    injected, scrape: injected,
		},
		{
			name:      "InjectedTimeoutBetween",
			hcTimeout: between,
			opts:      []serper.Option{serper.WithTimeout(tmoTimeout), serper.WithScrapeTimeout(tmoLater)},
			search:    tmoTimeout, scrape: between,
		},
	} {
		for _, o := range opCases() {
			want := tt.search
			if o.scrape {
				want = tt.scrape
			}
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					hc := &http.Client{Transport: testutil.RoundTripFunc(testutil.BlockUntilDone), Timeout: tt.hcTimeout}
					c := newHTTPClientClient(t, testKey, hc, tt.opts...)
					got := o.call(t.Context(), c)
					ce := assertCallError(t, got.err, o.want(wantCall{
						Kind: serper.FailureTimeout, OutcomeUnknown: true, Attempts: 1,
					}))
					tmoAssertTimeout(t, got.err)
					assert.Equal(t, want, ce.Meta.Elapsed, "the operation's own deadline")
					tmoSettle(t, nil)
				})
			})
		}
	}
}

func TestTimeoutGappedBody(t *testing.T) {
	const gap, step = 400 * time.Millisecond, 16
	for _, o := range opCases() {
		reads := (len(o.ok) + step - 1) / step
		sum := time.Duration(reads) * gap
		for _, tt := range []struct {
			name    string
			timeout time.Duration
			ok      bool
		}{
			{name: "UnderTheDeadline", timeout: sum + time.Millisecond, ok: true},
			{name: "JustOverTheDeadline", timeout: sum - time.Millisecond},
		} {
			for _, declared := range []bool{true, false} {
				t.Run(o.name+"/"+tt.name+"/declared="+strconv.FormatBool(declared), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						length := int64(-1)
						if declared {
							length = int64(len(o.ok))
						}
						b := testutil.NewBody(o.ok)
						b.Step, b.Gap = step, gap
						c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, length, b),
							serper.WithTimeout(tt.timeout))
						got := o.call(t.Context(), c, serper.WithRawCapture(true))
						if !tt.ok {
							ce := assertCallError(t, got.err, o.want(wantCall{
								Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
								RawCapture: serper.RawCaptureFailed,
							}))
							tmoAssertTimeout(t, got.err)
							assert.Equal(t, tt.timeout, ce.Meta.Elapsed)
							assert.Less(t, b.BytesRead(), int64(len(o.ok)), "the last step never arrived")
							tmoSettle(t, b)
							return
						}
						require.NoError(t, got.err)
						assert.Equal(t, sum, got.meta.Elapsed, "Elapsed is the sum of the gaps")
						assert.Equal(t, http.StatusOK, got.meta.StatusCode)
						assert.Equal(t, serper.RawCaptureComplete, got.meta.RawCapture)
						assertBody(t, o.ok, got.raw)
						// Nothing still watches the call: past its deadline, no
						// timer or AfterFunc fires and the body is not closed again.
						<-time.After(2 * tt.timeout)
						tmoSettle(t, b)
					})
				})
			}
		}
	}
}

func TestTimeoutDuringErrorBodyDiscard(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusNotFound, http.StatusInternalServerError} {
		for _, tt := range []struct {
			name string
			body func() (*testutil.Body, int64)
		}{
			{name: "BlockingUnknownLength", body: func() (*testutil.Body, int64) {
				return testutil.NewBlockingBody(`{"message":"partial`), -1
			}},
			{name: "BlockingDeclaredLength", body: func() (*testutil.Body, int64) {
				return testutil.NewBlockingBody(`{"message":"partial`), 100
			}},
			{name: "EndlessGap", body: func() (*testutil.Body, int64) {
				b := testutil.NewBody(`{"message":"never"}`)
				b.Gap = tmoNever
				return b, -1
			}},
		} {
			for _, o := range opCases() {
				t.Run(strconv.Itoa(code)+"/"+tt.name+"/"+o.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						b, length := tt.body()
						c := newTransportClient(t, testutil.TransportWith(code, nil, length, b),
							serper.WithTimeout(tmoTimeout))
						got := o.call(t.Context(), c, serper.WithRawCapture(true))
						ce := assertCallError(t, got.err, o.want(wantCall{
							Kind: statusKind(code), OutcomeUnknown: statusOutcomeUnknown(code), Status: code,
							Attempts: 1, RawCapture: serper.RawCaptureFailed,
						}))
						require.NotErrorIs(t, got.err, serper.ErrTimeout, "the status decides, not the stalled discard")
						assert.Equal(t, tmoTimeout, ce.Meta.Elapsed, "the discard ends at the deadline")
						tmoSettle(t, b)
					})
				})
			}
		}
	}
}

func TestTimeoutNetworkTimeoutError(t *testing.T) {
	netTimeout := testutil.TimeoutError{Msg: "i/o timeout " + tmoCauseMarker}
	for _, tt := range []struct {
		name string
		body bool
		rt   func(o opCase) (http.RoundTripper, *testutil.Body)
	}{
		{name: "BeforeHeaders", rt: func(opCase) (http.RoundTripper, *testutil.Body) {
			return testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				<-time.After(tmoEarlier)
				return nil, netTimeout
			}), nil
		}},
		{name: "DuringBody", body: true, rt: func(o opCase) (http.RoundTripper, *testutil.Body) {
			b := testutil.NewBody(o.ok[:len(o.ok)/2])
			b.Gap = tmoEarlier
			b.End = netTimeout
			return testutil.TransportWith(http.StatusOK, nil, int64(len(o.ok)), b), b
		}},
	} {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					rt, body := tt.rt(o)
					c := newTransportClient(t, rt, serper.WithTimeout(tmoTimeout))
					got := o.call(t.Context(), c, serper.WithRawCapture(true))
					h := tmoHang{body: tt.body}
					ce := assertCallError(t, got.err, o.want(wantCall{
						Kind: serper.FailureTimeout, OutcomeUnknown: true, Status: h.status(), Attempts: 1,
						RawCapture: h.capture(true),
					}), tmoCauseMarker)
					tmoAssertTimeout(t, got.err)
					assert.Equal(t, tmoEarlier, ce.Meta.Elapsed, "the network's timeout, before the SDK's")
					tmoSettle(t, body)
				})
			})
		}
	}
}

func TestTimeoutElapsedIsExact(t *testing.T) {
	const headers, gap = 1500 * time.Millisecond, 700 * time.Millisecond
	for _, tt := range []struct {
		name    string
		headers time.Duration // the wait before the transport answers
		gap     time.Duration // the wait before the body's one read of content
		want    time.Duration
	}{
		{name: "Instant", want: time.Nanosecond}, // a measured call reports at least 1ns
		{name: "SlowHeaders", headers: headers, want: headers},
		{name: "SlowBody", gap: gap, want: gap},
		{name: "SlowHeadersAndBody", headers: headers, gap: gap, want: headers + gap},
	} {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					b := testutil.NewBody(o.ok)
					b.Gap = tt.gap
					answer := testutil.TransportWith(http.StatusOK, nil, int64(len(o.ok)), b)
					c := newTransportClient(t, testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						<-time.After(tt.headers)
						return answer(r)
					}), serper.WithTimeout(tmoTimeout))
					got := o.call(t.Context(), c)
					require.NoError(t, got.err)
					assert.Equal(t, tt.want, got.meta.Elapsed)
					assert.Equal(t, 1, got.meta.Attempts)
					tmoSettle(t, b)
				})
			})
		}
	}
}

func TestTimeoutLateAnswerIsNeverASuccess(t *testing.T) {
	// An injected transport is trusted, but it may ignore the request's
	// context and hand back a complete HTTP 200 only after the call's
	// deadline. The deadline covers the send, so such an answer is never a
	// success: the call fails as the first context to end decides, every
	// time, and the body is closed exactly once.
	for _, end := range []struct {
		name   string
		kind   serper.FailureKind
		derive func(parent context.Context) (context.Context, func())
	}{
		{
			name: "SDKDeadline", kind: serper.FailureTimeout,
			derive: func(p context.Context) (context.Context, func()) { return context.WithCancel(p) },
		},
		{
			name: "CallerDeadline", kind: serper.FailureDeadlineExceeded,
			derive: func(p context.Context) (context.Context, func()) { return context.WithTimeout(p, tmoEarlier) },
		},
		{
			name: "CallerCancel", kind: serper.FailureCanceled,
			derive: func(p context.Context) (context.Context, func()) { return tmoCancelAfter(p, tmoEarlier, nil) },
		},
	} {
		for _, o := range opCases() {
			t.Run(end.name+"/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, release := end.derive(t.Context())
					defer release()
					b := testutil.NewBody(o.ok)
					c := newTransportClient(t, testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						testutil.Drain(r)
						<-time.After(tmoLater) // ignores r.Context()
						return testutil.Response(r, http.StatusOK, b, int64(len(o.ok))), nil
					}), serper.WithTimeout(tmoTimeout))
					got := o.call(ctx, c, serper.WithRawCapture(true))
					ce := assertCallError(t, got.err, o.want(wantCall{
						Kind: end.kind, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
						RawCapture: serper.RawCaptureFailed,
					}))
					tmoAssertContextKind(t, got.err, end.kind)
					assert.Equal(t, tmoLater, ce.Meta.Elapsed, "until the late body was closed")
					release()
					tmoSettle(t, b)
				})
			})
		}
	}
}
