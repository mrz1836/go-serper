package testutil_test

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestEsc(t *testing.T) {
	assert.Equal(t, `"\u0061\\"`, testutil.Esc(`"~u0061~~"`))
	assert.Equal(t, "plain", testutil.Esc("plain"))
}

func TestJSONQuote(t *testing.T) {
	assert.Equal(t, `"plain"`, testutil.JSONQuote("plain"))
	assert.Equal(t, `"a\"b\\c\n\u003c\u2028"`, testutil.JSONQuote("a\"b\\c\n<\u2028"))
	// Go 1.26 escapes the replacement character and Go 1.27 writes it as is;
	// either way the literal decodes to U+FFFD.
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(testutil.JSONQuote("\xff")), &decoded))
	assert.Equal(t, string(utf8.RuneError), decoded, "invalid UTF-8 becomes U+FFFD")
}

func TestJSONQuoteNoHTML(t *testing.T) {
	assert.Equal(t, `"<a&b>"`, testutil.JSONQuoteNoHTML("<a&b>"))
	assert.Equal(t, `"\u2028\u2029"`, testutil.JSONQuoteNoHTML("\u2028\u2029"), "line separators stay escaped")
	assert.Equal(t, `"tab\tnl\n\u0001"`, testutil.JSONQuoteNoHTML("tab\tnl\n\x01"))
	assert.Equal(t, `"<é>"`, testutil.JSONQuoteNoHTML("<é>"), "non-ASCII text stays as is")
}
