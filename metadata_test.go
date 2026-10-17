package serper_test

import (
	"encoding"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestEnumText(t *testing.T) {
	t.Run("CreditsStatus", func(t *testing.T) {
		assertEnumText(t, map[serper.CreditsStatus]string{
			serper.CreditsNotReported: "not_reported",
			serper.CreditsReported:    "reported",
			serper.CreditsInvalid:     "invalid",
		}, serper.CreditsStatus(9), "CreditsStatus(9)", "Reported")
	})
	t.Run("RawCaptureState", func(t *testing.T) {
		assertEnumText(t, map[serper.RawCaptureState]string{
			serper.RawCaptureOff:        "off",
			serper.RawCaptureComplete:   "complete",
			serper.RawCaptureNoResponse: "no_response",
			serper.RawCaptureFailed:     "failed",
		}, serper.RawCaptureState(200), "RawCaptureState(200)", "COMPLETE")
	})
}

// assertEnumText asserts that every value of names prints, marshals and
// unmarshals as its name; that bad, a value with no name, prints as badString
// and fails to marshal; and that unknown, a text that is no name, and an empty
// text fail to unmarshal, with an error that never quotes the text.
func assertEnumText[T interface {
	comparable
	fmt.Stringer
	encoding.TextMarshaler
}, P interface {
	*T
	encoding.TextUnmarshaler
}](t *testing.T, names map[T]string, bad T, badString, unknown string,
) {
	t.Helper()
	for v, name := range names {
		assert.Equal(t, name, v.String())
		text, err := v.MarshalText()
		require.NoError(t, err, name)
		assert.Equal(t, name, string(text))
		var back T
		require.NoError(t, P(&back).UnmarshalText(text), name)
		assert.Equal(t, v, back)
	}
	assert.Equal(t, badString, bad.String())
	_, err := bad.MarshalText()
	require.Error(t, err)
	var v T
	err = P(&v).UnmarshalText([]byte(unknown))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), unknown, "the text is never quoted")
	require.Error(t, P(&v).UnmarshalText(nil))
}

func TestMetadataJSONRoundTrip(t *testing.T) {
	used := int64(0)
	m := &serper.Metadata{
		Op: serper.OpSearch, Origin: "https://google.serper.dev", Path: "/search", StatusCode: 200, Attempts: 1,
		Elapsed: 1234 * time.Millisecond, Credits: serper.Credits{Status: serper.CreditsReported, Used: &used},
		RawCapture: serper.RawCaptureComplete,
	}
	assertRoundTrip(t, m, `{"op":"search","origin":"https://google.serper.dev","path":"/search",`+
		`"statusCode":200,"attempts":1,"elapsed":1234000000,"credits":{"status":"reported","used":0},`+
		`"rawCapture":"complete"}`)
	assertRoundTrip(t, &serper.Metadata{}, `{"op":"","origin":"","path":"","statusCode":0,"attempts":0,`+
		`"elapsed":0,"credits":{"status":"not_reported"},"rawCapture":"off"}`)

	var back serper.Metadata
	require.Error(t, json.Unmarshal([]byte(`{"credits":{"status":"guessed"}}`), &back))
	require.Error(t, json.Unmarshal([]byte(`{"rawCapture":"partial"}`), &back))
	_, err := json.Marshal(serper.Metadata{RawCapture: 9})
	require.Error(t, err)
}

func TestCreditsValue(t *testing.T) {
	n := int64(5)
	for _, tt := range []struct {
		name string
		c    serper.Credits
		want int64
		ok   bool
	}{
		{name: "not reported", c: serper.Credits{}},
		{name: "invalid", c: serper.Credits{Status: serper.CreditsInvalid}},
		{name: "reported without Used", c: serper.Credits{Status: serper.CreditsReported}},
		{name: "invalid with Used", c: serper.Credits{Status: serper.CreditsInvalid, Used: &n}},
		{name: "reported", c: serper.Credits{Status: serper.CreditsReported, Used: &n}, want: 5, ok: true},
	} {
		got, ok := tt.c.Value()
		assert.Equal(t, tt.want, got, tt.name)
		assert.Equal(t, tt.ok, ok, tt.name)
	}
}

func TestMetadataPrintsNoMarkers(t *testing.T) {
	// Answers that carry every marker: a refused body and an error body.
	refused := `{"searchParameters":{"q":"` + testQuery + `"},"organic":"` + testKey + `","images":"` + testKey + `","text":7}`
	failed := `{"message":"` + testTarget + `"}`
	c, _ := newScriptClient(t,
		testutil.Step{Body: refused}, testutil.Step{Status: 500, Body: failed},
		testutil.Step{Body: refused}, testutil.Step{Status: 500, Body: failed},
		testutil.Step{Body: refused}, testutil.Step{Status: 500, Body: failed},
	)
	for _, o := range append(opCases(), opCases()...) {
		got := o.call(t.Context(), c, serper.WithRawCapture(true))
		ce := callError(t, got.err)
		data, err := json.Marshal(ce.Meta)
		require.NoError(t, err)
		assertErrorClean(t, ce, leakMarkers()...)
		testutil.AssertRedacted(t, string(data), leakMarkers(), "metadata JSON")
	}
}
