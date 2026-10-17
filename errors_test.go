package serper_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
)

func TestSentinelTexts(t *testing.T) {
	for err, text := range map[error]string{
		serper.ErrInvalidConfig:      "serper: invalid configuration",
		serper.ErrInvalidRequest:     "serper: invalid request",
		serper.ErrUnauthorized:       "serper: unauthorized",
		serper.ErrForbidden:          "serper: forbidden",
		serper.ErrRateLimited:        "serper: rate limited",
		serper.ErrRejected:           "serper: rejected by provider",
		serper.ErrUpstream:           "serper: provider server error",
		serper.ErrUnexpectedResponse: "serper: unexpected response",
		serper.ErrMalformedResponse:  "serper: malformed response",
		serper.ErrResponseTooLarge:   "serper: response too large",
		serper.ErrTransport:          "serper: transport failure",
		serper.ErrTimeout:            "serper: timeout",
		serper.ErrOutcomeUnknown:     "serper: outcome unknown",
	} {
		assert.Equal(t, text, err.Error())
	}
}

func TestCallErrorFromExportedFields(t *testing.T) {
	for kind, sentinel := range kindSentinels() {
		for _, unknown := range []bool{false, true} {
			ce := &serper.CallError{Kind: kind, OutcomeUnknown: unknown, Meta: serper.Metadata{Op: serper.OpImages}}
			assertOnlySentinel(t, ce, kind, unknown)
			require.ErrorIs(t, fmt.Errorf("wrapped: %w", ce), sentinel)
			require.NoError(t, ce.Unwrap(), "no cause without the SDK")
			raw, ok := ce.RawJSON()
			assertCapture(t, "", raw, ok)
			assert.Equal(t, "serper: images: "+string(kind), ce.Error())
		}
	}
}

func TestCallErrorText(t *testing.T) {
	tests := []struct {
		ce   *serper.CallError
		want string
	}{
		{ce: nil, want: "serper: <nil CallError>"},
		{ce: &serper.CallError{}, want: "serper: call_failed"},
		{ce: &serper.CallError{Kind: "custom"}, want: "serper: custom"},
		{
			ce:   &serper.CallError{Kind: serper.FailureUpstream, Meta: serper.Metadata{Op: serper.OpSearch, StatusCode: 503}},
			want: "serper: search: upstream (HTTP 503)",
		},
		{
			ce: &serper.CallError{
				Kind: serper.FailureMalformedResponse, Field: "organic[3]",
				Meta: serper.Metadata{Op: serper.OpSearch, StatusCode: 200},
			},
			want: "serper: search: malformed_response (HTTP 200): organic[3]",
		},
		{
			ce:   &serper.CallError{Kind: serper.FailureTransport, Meta: serper.Metadata{Op: serper.OpScrape}},
			want: "serper: scrape: transport",
		},
	}
	for i, tt := range tests {
		assert.Equal(t, tt.want, tt.ce.Error(), "row %d", i)
	}
}

func TestCallErrorNilReceivers(t *testing.T) {
	var ce *serper.CallError
	assert.Equal(t, "(*serper.CallError)(nil)", ce.GoString())
	require.NoError(t, ce.Unwrap())
	assert.False(t, ce.Is(serper.ErrTimeout))
	raw, ok := ce.RawJSON()
	assertCapture(t, "", raw, ok)

	var ve *serper.ValidationError
	assert.Equal(t, "serper: <nil ValidationError>", ve.Error())
	require.NoError(t, ve.Unwrap())
	assert.Equal(t, "serper: validation failed: Query", (&serper.ValidationError{Field: "Query"}).Error())
}

func TestCallErrorGoString(t *testing.T) {
	used := int64(3)
	ce := &serper.CallError{
		Kind: serper.FailureMalformedResponse, Field: "text", OutcomeUnknown: true,
		Meta: serper.Metadata{
			Op: serper.OpScrape, Origin: "https://scrape.serper.dev", Path: "/", StatusCode: 200, Attempts: 1,
			Elapsed: 1500, Credits: serper.Credits{Status: serper.CreditsReported, Used: &used},
			RawCapture: serper.RawCaptureComplete,
		},
	}
	want := `&serper.CallError{Kind:"malformed_response", Field:"text", OutcomeUnknown:true, ` +
		`Meta:serper.Metadata{Op:"scrape", Origin:"https://scrape.serper.dev", Path:"/", StatusCode:200, ` +
		`Attempts:1, Elapsed:1500, Credits:serper.Credits{Status:reported, Used:3}, RawCapture:complete}}`
	assert.Equal(t, want, ce.GoString())
	assert.Equal(t, want, fmt.Sprintf("%#v", ce))

	ce.Meta.Credits = serper.Credits{}
	assert.Contains(t, ce.GoString(), "Credits:serper.Credits{Status:not_reported, Used:nil}")
}

func TestCallErrorMatchesContextErrors(t *testing.T) {
	canceled := &serper.CallError{Kind: serper.FailureCanceled}
	require.ErrorIs(t, canceled, context.Canceled)
	require.NotErrorIs(t, canceled, context.DeadlineExceeded)

	deadline := &serper.CallError{Kind: serper.FailureDeadlineExceeded}
	require.ErrorIs(t, deadline, context.DeadlineExceeded)
	require.NotErrorIs(t, deadline, serper.ErrTimeout, "the caller's deadline is not the SDK's timeout")

	timeout := &serper.CallError{Kind: serper.FailureTimeout}
	require.ErrorIs(t, timeout, serper.ErrTimeout)
	require.ErrorIs(t, timeout, context.DeadlineExceeded)
}

func TestErrorsAsAndAsType(t *testing.T) {
	c, _ := newScriptClient(t)
	got := opCaseNamed("Search").call(t.Context(), c)
	wrapped := fmt.Errorf("app: %w", got.err)

	var ce *serper.CallError
	require.ErrorAs(t, wrapped, &ce)
	assert.Equal(t, serper.FailureTransport, ce.Kind)
	byType, ok := errors.AsType[*serper.CallError](wrapped)
	require.True(t, ok)
	assert.Same(t, ce, byType)
	_, ok = errors.AsType[*serper.ValidationError](wrapped)
	assert.False(t, ok)

	_, err := c.Search(t.Context(), serper.SearchRequest{})
	ve, ok := errors.AsType[*serper.ValidationError](fmt.Errorf("app: %w", err))
	require.True(t, ok)
	assert.Equal(t, "Query", ve.Field)
	require.ErrorIs(t, ve, serper.ErrInvalidRequest)
}
