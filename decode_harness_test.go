package serper_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// The decoding harness that the Search, Images and Scrape decoding suites
// share: live calls that must agree with the Decode helpers, and the tables
// and runners of member types, whitespace, credits, fixtures, respelled names,
// unknown members and strings.

// srdLive plays live calls whose HTTP 200 answer is a body the test chooses:
// through a new in-memory Script for each call, and through the two recording
// TLS servers of newServerClient, which the calls of one test share.
type srdLive struct {
	body   atomic.Pointer[[]byte]
	client *serper.Client
	srv    servers
}

// srdNewLive returns an srdLive whose TLS servers answer every request with
// HTTP 200, Content-Type application/json and the body stored last.
func srdNewLive(tb testing.TB) *srdLive {
	tb.Helper()
	l := &srdLive{}
	serve := func(w http.ResponseWriter, r *http.Request) {
		testutil.Serve(testutil.Step{Body: string(*l.body.Load())})(w, r)
	}
	l.client, l.srv = newServerClient(tb, serve, serve)
	return l
}

// decode returns the outcome of o's Decode helper on body, after asserting
// that a live call of o answered with body agrees with it, both through an
// in-memory Script and through the TLS servers: on success the same typed
// result (compared as JSON) and credits, on failure the same CallError as a
// live call reports it.
func (l *srdLive) decode(tb testing.TB, o opCase, body []byte) outcome {
	tb.Helper()
	offline := o.decode(body)

	c, script := newScriptClient(tb, testutil.Step{Body: string(body)})
	srdAssertLive(tb, o, offline, o.call(tb.Context(), c), o.origin())
	assert.Equal(tb, []string{o.path}, script.Paths(), "one request, to the operation's path")

	srv := l.srv.of(o)
	before := srv.Hits()
	l.body.Store(&body)
	srdAssertLive(tb, o, offline, o.call(tb.Context(), l.client), srv.URL())
	if reqs := srv.Requests(); assert.Len(tb, reqs, before+1, "one request to the TLS server") {
		assert.Equal(tb, o.path, reqs[before].Path)
	}
	return offline
}

// srdAssertLive asserts that live, a call of o to origin answered with HTTP
// 200 and a body, agrees with offline, the Decode helper's outcome on the same
// body.
func srdAssertLive(tb testing.TB, o opCase, offline, live outcome, origin string) {
	tb.Helper()
	if offline.err != nil {
		want := callError(tb, offline.err)
		_ = assertCallError(tb, live.err, o.wantAt(origin, wantCall{
			Kind: want.Kind, Field: want.Field, OutcomeUnknown: true, Status: http.StatusOK, Attempts: 1,
			Credits: want.Meta.Credits,
		}))
		return
	}
	require.NoError(tb, live.err, "the live call decodes what the Decode helper decodes")
	assert.Equal(tb, jsonOf(tb, offline.value), jsonOf(tb, live.value), "the live result is the Decode helper's")
	assert.Equal(tb, serper.Metadata{
		Op: o.op, Origin: origin, Path: o.path, StatusCode: http.StatusOK, Attempts: 1,
		Elapsed: live.meta.Elapsed, Credits: offline.meta.Credits,
	}, live.meta)
	assert.Positive(tb, live.meta.Elapsed)
	assert.False(tb, live.rawOK, "capture is off")
}

// srdPlain returns the JSON of body decoded by plain encoding/json into o's
// public response type. The SDK decodes like encoding/json, so for a body it
// accepts, its result must be the same.
func srdPlain(tb testing.TB, o opCase, body []byte) string {
	tb.Helper()
	var v any
	switch o.op {
	case serper.OpSearch:
		v = new(serper.SearchResponse)
	case serper.OpImages:
		v = new(serper.ImagesResponse)
	case serper.OpScrape:
		v = new(serper.ScrapeResponse)
	}
	require.NoError(tb, json.Unmarshal(body, v), "plain encoding/json accepts the body")
	return jsonOf(tb, v)
}

// srdAssertPlain asserts that got, o's Decode outcome on body, succeeded with
// the result plain encoding/json gives for o's public response type.
func srdAssertPlain(tb testing.TB, o opCase, body []byte, got outcome) {
	tb.Helper()
	require.NoError(tb, got.err)
	assert.Equal(tb, srdPlain(tb, o, body), jsonOf(tb, got.value), "decoded as encoding/json decodes it")
}

// srdAssertRoundTrip asserts that value, a decoded response of o, round-trips
// through encoding/json unchanged once its Meta is cleared (so its JSON is
// stable too), and that the SDK decodes that JSON back to the same result.
func srdAssertRoundTrip(tb testing.TB, o opCase, value any) {
	tb.Helper()
	data := jsonOf(tb, value)
	switch r := value.(type) {
	case *serper.SearchResponse:
		c := *r
		c.Meta = serper.Metadata{}
		_ = assertRoundTrip(tb, &c, data)
	case *serper.ImagesResponse:
		c := *r
		c.Meta = serper.Metadata{}
		_ = assertRoundTrip(tb, &c, data)
	case *serper.ScrapeResponse:
		c := *r
		c.Meta = serper.Metadata{}
		_ = assertRoundTrip(tb, &c, data)
	default:
		require.Failf(tb, "not a response", "%T", value)
	}
	again := o.decode([]byte(data))
	require.NoError(tb, again.err, "the SDK decodes its own JSON")
	assert.Equal(tb, data, jsonOf(tb, again.value))
}

// srdAt returns template with every @ replaced by value.
func srdAt(template, value string) string { return strings.ReplaceAll(template, "@", value) }

// srdKind is the JSON type a typed member wants.
type srdKind int

// Member kinds.
const (
	srdKindString srdKind = iota // a JSON string
	srdKindInt                   // a JSON number in plain integer digits that fits an int
	srdKindBool                  // true or false
	srdKindObject                // an object member
	srdKindList                  // an array member
	srdKindItem                  // an object item of an array
)

// srdPath is a typed member: its path as CallError.Field reports it, its kind,
// and a body with @ where its value goes.
type srdPath struct {
	field    string
	kind     srdKind
	template string
	required bool // null is refused, as Scrape's text is
}

// srdWrongValues returns JSON values that a member of kind k cannot hold:
// every other JSON type, and for an int the numbers encoding/json cannot store
// in one (digits in a string, fractions, exponents, numbers out of range).
func srdWrongValues(k srdKind) []string {
	switch k {
	case srdKindString:
		return []string{`1`, `-1.5`, `0`, `true`, `false`, `{}`, `{"text":"x"}`, `[]`, `["x"]`}
	case srdKindInt:
		return []string{
			`"1"`, `""`, `"1.5"`, `1.5`, `1.0`, `-0.5`, `1e2`, `1E2`, `9223372036854775808`,
			`-9223372036854775809`, `99999999999999999999`, `1e400`, `true`, `false`, `{}`, `[]`, `[1]`,
		}
	case srdKindBool:
		return []string{`"true"`, `"false"`, `""`, `1`, `0`, `{}`, `[]`, `[true]`}
	case srdKindObject, srdKindItem:
		return []string{`[]`, `[{}]`, `"x"`, `""`, `0`, `1`, `true`, `false`}
	case srdKindList:
		return []string{`{}`, `{"0":{}}`, `"x"`, `""`, `1`, `true`, `false`}
	}
	return nil
}

// srdRightValues returns JSON values of the right type for a member of kind k,
// null aside; nil for the kinds whose accepted values other tests cover.
func srdRightValues(k srdKind) []string {
	switch k {
	case srdKindString:
		return []string{`""`, `"x"`, `"caf\u00e9"`}
	case srdKindInt:
		return []string{`0`, `-0`, `7`, `-7`, strconv.Itoa(math.MaxInt), strconv.Itoa(math.MinInt)}
	case srdKindBool:
		return []string{`true`, `false`}
	case srdKindObject, srdKindList, srdKindItem:
	}
	return nil
}

// srdRunWrongTypes asserts, for every path and every wrong value of its kind,
// that o's decoding fails as malformed at the path's field, live and offline.
func srdRunWrongTypes(t *testing.T, l *srdLive, o opCase, paths []srdPath) {
	t.Helper()
	for _, p := range paths {
		for _, v := range srdWrongValues(p.kind) {
			t.Run(p.field+"="+v, func(t *testing.T) {
				got := l.decode(t, o, []byte(srdAt(p.template, v)))
				_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: p.field, Op: o.op})
			})
		}
	}
}

// srdRunRightTypes asserts, for every scalar path and every right value of its
// kind (and null, unless the path requires a value), that o's decoding
// succeeds with what encoding/json gives, live and offline.
func srdRunRightTypes(t *testing.T, l *srdLive, o opCase, paths []srdPath) {
	t.Helper()
	for _, p := range paths {
		values := srdRightValues(p.kind)
		if len(values) > 0 && !p.required {
			values = append(values, `null`)
		}
		for _, v := range values {
			t.Run(p.field+"="+v, func(t *testing.T) {
				body := []byte(srdAt(p.template, v))
				srdAssertPlain(t, o, body, l.decode(t, o, body))
			})
		}
	}
}

// srdParameterPaths returns the paths of searchParameters and its members,
// which Search and Images share. The templates hold searchParameters only.
func srdParameterPaths() []srdPath {
	members := []struct {
		name string
		kind srdKind
	}{
		{"q", srdKindString},
		{"type", srdKindString},
		{"engine", srdKindString},
		{"gl", srdKindString},
		{"hl", srdKindString},
		{"location", srdKindString},
		{"tbs", srdKindString},
		{"num", srdKindInt},
		{"page", srdKindInt},
		{"autocorrect", srdKindBool},
	}
	paths := make([]srdPath, 0, len(members)+1)
	paths = append(paths, srdPath{field: "searchParameters", kind: srdKindObject, template: `{"searchParameters":@}`})
	for _, m := range members {
		paths = append(paths, srdPath{
			field: "searchParameters." + m.name, kind: m.kind,
			template: `{"searchParameters":{"filler":"x",` + strconv.Quote(m.name) + `:@,"more":[1]}}`,
		})
	}
	return paths
}

// srdStrings returns paths for the string members called names of an object
// under field, in template, where # stands for the member text.
func srdStrings(field, template string, names ...string) []srdPath {
	paths := make([]srdPath, 0, len(names))
	for _, name := range names {
		paths = append(paths, srdPath{
			field: field + "." + name, kind: srdKindString,
			template: strings.Replace(template, "#", `"filler":"x",`+strconv.Quote(name)+`:@`, 1),
		})
	}
	return paths
}

// srdNamed is a named body.
type srdNamed struct {
	name string
	body string
}

// srdSpaced returns compact, a JSON text, with ws before it and after every
// structural character outside strings.
func srdSpaced(compact, ws string) string {
	var b strings.Builder
	b.WriteString(ws)
	inString, escaped := false, false
	for _, c := range []byte(compact) {
		b.WriteByte(c)
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && strings.IndexByte("{}[],:", c) >= 0:
			b.WriteString(ws)
		}
	}
	return b.String()
}

// srdWhitespaceVariants returns body, one JSON object, with JSON whitespace
// (space, tab, CR, LF) added in the ways a server may send it.
func srdWhitespaceVariants(tb testing.TB, body string) []srdNamed {
	tb.Helper()
	var compact, indented, tabbed bytes.Buffer
	require.NoError(tb, json.Compact(&compact, []byte(body)))
	require.NoError(tb, json.Indent(&indented, compact.Bytes(), "", "  "))
	require.NoError(tb, json.Indent(&tabbed, compact.Bytes(), "\t", "\t"))
	c := compact.String()
	return []srdNamed{
		{"compact", c},
		{"leading spaces", "   " + c},
		{"trailing spaces", c + "   "},
		{"leading and trailing tabs", "\t\t" + c + "\t"},
		{"trailing LF", c + "\n"},
		{"trailing CRLF", c + "\r\n"},
		{"leading CRLF", "\r\n\r\n" + c},
		{"pretty-printed", indented.String()},
		{"pretty-printed with tabs and a prefix", tabbed.String() + "\n"},
		{"pretty-printed with CRLF", strings.ReplaceAll(indented.String(), "\n", "\r\n") + "\r\n"},
		{"a space around every token", srdSpaced(c, " ")},
		{"every whitespace around every token", srdSpaced(c, " \t\r\n")},
	}
}

// srdRunWhitespace asserts that every whitespace variant of body decodes as
// body does, live and offline, and that characters JSON does not count as
// whitespace (form feed, vertical tab, NUL, NBSP, U+2028, a BOM) around it
// are refused as malformed with no field.
func srdRunWhitespace(t *testing.T, l *srdLive, o opCase, body string) {
	t.Helper()
	want := o.decode([]byte(body))
	require.NoError(t, want.err)
	for _, v := range srdWhitespaceVariants(t, body) {
		t.Run(v.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(v.body))
			require.NoError(t, got.err)
			assert.Equal(t, jsonOf(t, want.value), jsonOf(t, got.value))
			assert.Equal(t, want.meta, got.meta)
		})
	}
	for _, ws := range []string{"\f", "\v", "\x00", "\u00a0", "\u2028", "\ufeff"} {
		for _, b := range []srdNamed{{"leading", ws + body}, {"trailing", body + ws}} {
			t.Run("refused "+b.name+" "+strconv.QuoteToASCII(ws), func(t *testing.T) {
				got := l.decode(t, o, []byte(b.body))
				_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
			})
		}
	}
}

// srdRunNotObjects asserts that top-level JSON values other than an object,
// whitespace around them included, are refused as malformed with no field
// and no credits, live and offline; ok is the operation's valid object.
func srdRunNotObjects(t *testing.T, l *srdLive, o opCase, ok string) {
	t.Helper()
	for i, body := range []string{
		`null`, " null ", `[]`, "\t[ ]\r\n", `[` + ok + `]`, `[` + ok + `,` + ok + `]`,
		testutil.JSONQuote(ok), `""`, `0`, `-1`, `1.5`, `1e3`, `true`, " false\n",
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
		})
	}
}

// srdCredits is a value of the top-level "credits" member and what it
// reports.
type srdCredits struct {
	raw  string // the member's JSON value; "" for no member
	want serper.Credits
}

// srdCreditsCases returns the credits values every operation is checked
// with, on its successes and its failures.
func srdCreditsCases() []srdCredits {
	return []srdCredits{
		{"", serper.Credits{}},
		{`null`, serper.Credits{}},
		{`0`, reported(0)},
		{`7`, reported(7)},
		{strconv.FormatInt(math.MaxInt64, 10), reported(math.MaxInt64)},
		{`9223372036854775808`, invalidCredits()},
		{`7.0`, invalidCredits()},
		{`7e0`, invalidCredits()},
		{`-7`, invalidCredits()},
		{`-0`, invalidCredits()},
		{`"7"`, invalidCredits()},
		{`true`, invalidCredits()},
		{`[7]`, invalidCredits()},
		{`{"used":7}`, invalidCredits()},
	}
}

// srdCreditsBody returns body, a JSON object, with a "credits" member of value
// raw, none when raw is "", as its first member when first is set, else as its
// last.
func srdCreditsBody(body, raw string, first bool) string {
	switch {
	case raw == "":
		return body
	case body == "{}":
		return `{"credits":` + raw + `}`
	case first:
		return `{"credits":` + raw + `,` + body[1:]
	}
	return body[:len(body)-1] + `,"credits":` + raw + `}`
}

// srdCreditsCase is a body and how o decodes it: kind and field of the
// failure, or "" for success.
type srdCreditsCase struct {
	name  string
	body  string
	kind  serper.FailureKind
	field string
}

// srdRunCredits asserts, for every case and every credits value placed first
// or last, that o reads the credits whether decoding succeeds or fails, and
// never fails over them, live and offline.
func srdRunCredits(t *testing.T, l *srdLive, o opCase, cases []srdCreditsCase) {
	t.Helper()
	for _, tc := range cases {
		for _, cr := range srdCreditsCases() {
			for _, first := range []bool{true, false} {
				if cr.raw == "" && !first {
					continue // no member: one body only
				}
				body := srdCreditsBody(tc.body, cr.raw, first)
				t.Run(tc.name+"/"+body, func(t *testing.T) {
					got := l.decode(t, o, []byte(body))
					if tc.kind == "" {
						require.NoError(t, got.err, "credits never fail a result")
						assert.Equal(t, cr.want, got.meta.Credits)
						return
					}
					_ = assertCallError(t, got.err, wantCall{Kind: tc.kind, Field: tc.field, Op: o.op, Credits: cr.want})
				})
			}
		}
	}
}

// srdRunFixtures decodes every SDK fixture under testdata/synthetic/dir, which
// must include want, and asserts that each decodes live as offline, as plain
// encoding/json decodes it, and round-trips.
func srdRunFixtures(t *testing.T, l *srdLive, o opCase, dir string, want ...string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "synthetic", dir, "*.json"))
	require.NoError(t, err)
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	assert.Subset(t, names, want, "the fixtures of %s", dir)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			body := fixture(t, dir+"/"+name)
			got := l.decode(t, o, body)
			srdAssertPlain(t, o, body, got)
			srdAssertRoundTrip(t, o, got.value)
		})
	}
}

// srdRenames returns ways to respell member names that encoding/json still
// matches. It documents that it prefers an exact match of a member name but
// also accepts a case-insensitive one; its case folding is that of
// bytes.EqualFold (Unicode simple folding), under which U+017F (long s)
// folds to s and U+212A (Kelvin sign) to k.
func srdRenames() []struct {
	name   string
	rename func(string) string
} {
	return []struct {
		name   string
		rename func(string) string
	}{
		{"upper case", strings.ToUpper},
		{"lower case", strings.ToLower},
		{"first letter flipped", func(s string) string {
			if first := s[:1]; first == strings.ToUpper(first) {
				return strings.ToLower(first) + s[1:]
			}
			return strings.ToUpper(s[:1]) + s[1:]
		}},
		{"unicode folds", strings.NewReplacer("s", "\u017f", "k", "\u212a", "K", "\u212a").Replace},
	}
}

// srdRename returns body with every member called one of names, wherever it
// appears, respelled by rename. The fixtures use no attribute name that is
// also a member name, so attributes stay as they are.
func srdRename(body []byte, rename func(string) string, names ...string) []byte {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	re := regexp.MustCompile(`"(` + strings.Join(quoted, "|") + `)"(\s*):`)
	return re.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := re.FindSubmatch(m)
		return []byte(`"` + rename(string(sub[1])) + `"` + string(sub[2]) + `:`)
	})
}

// srdParameterNames are the member names of searchParameters, and credits.
func srdParameterNames() []string {
	return []string{
		"searchParameters", "q", "type", "engine", "gl", "hl", "location", "tbs", "num", "page", "autocorrect", "credits",
	}
}

// srdRunRenamed asserts that every fixture under testdata/synthetic/dir
// decodes the same with its member names respelled in every way of
// srdRenames, live and offline.
func srdRunRenamed(t *testing.T, l *srdLive, o opCase, dir string, names []string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "synthetic", dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, p := range paths {
		body := fixture(t, dir+"/"+filepath.Base(p))
		want := o.decode(body)
		require.NoError(t, want.err)
		for _, rn := range srdRenames() {
			t.Run(filepath.Base(p)+"/"+rn.name, func(t *testing.T) {
				renamed := srdRename(body, rn.rename, names...)
				got := l.decode(t, o, renamed)
				require.NoError(t, got.err)
				assert.Equal(t, jsonOf(t, want.value), jsonOf(t, got.value))
				assert.Equal(t, want.meta, got.meta, "credits are matched the same way")
				assert.Equal(t, srdPlain(t, o, renamed), jsonOf(t, got.value))
			})
		}
	}
}

// srdNoise returns unknown member values of every shape, large and deeply
// nested ones included, some of them holding typed member names with values
// that would fail if they were typed.
func srdNoise() []srdNamed {
	numbers := make([]string, 20000)
	for i := range numbers {
		numbers[i] = strconv.Itoa(i - 10000)
	}
	return []srdNamed{
		{"null", `null`},
		{"number", `-1.5e300`},
		{"string", `"an unknown member"`},
		{"bool", `false`},
		{"typed-looking object", `{"searchParameters":null,"organic":[null],"images":[null],"title":1,"text":2,` +
			`"markdown":3,"attributes":[],"credits":"x"}`},
		{"typed-looking array", `[null,{"organic":[null]},[[]]]`},
		{"large string", `"` + strings.Repeat("unknown ", 64<<10) + `"`},
		{"large array", `[` + strings.Join(numbers, ",") + `]`},
		{"deeply nested arrays", strings.Repeat("[", 9000) + strings.Repeat("]", 9000)},
		{"deeply nested objects", strings.Repeat(`{"a":`, 4000) + `null` + strings.Repeat("}", 4000)},
	}
}

// srdRunNoise asserts that template, a body with markers <name> where an
// unknown member may go (<end> as the last member of the top level, the
// others as the first member of their object), decodes the same with an
// unknown member of every shape at each marker as with none, live and
// offline, and as encoding/json decodes it.
func srdRunNoise(t *testing.T, l *srdLive, o opCase, template string) {
	t.Helper()
	markers := regexp.MustCompile(`<[a-z]+>`)
	clean := markers.ReplaceAllString(template, "")
	want := o.decode([]byte(clean))
	require.NoError(t, want.err)
	srdAssertPlain(t, o, []byte(clean), want)
	levels := markers.FindAllString(template, -1)
	require.NotEmpty(t, levels)
	for _, level := range levels {
		for _, n := range srdNoise() {
			member := `"unknownMember":` + n.body + `,`
			if level == "<end>" {
				member = `,"unknownMember":` + n.body
			}
			body := markers.ReplaceAllString(strings.Replace(template, level, member, 1), "")
			t.Run(level+"/"+n.name, func(t *testing.T) {
				got := l.decode(t, o, []byte(body))
				require.NoError(t, got.err)
				assert.Equal(t, jsonOf(t, want.value), jsonOf(t, got.value), "an unknown member is ignored")
				assert.Equal(t, want.meta, got.meta)
			})
		}
	}
	t.Run("nested beyond encoding/json's depth limit", func(t *testing.T) {
		// encoding/json refuses more than 10000 levels of nesting as invalid
		// JSON: the SDK reports it as malformed, never crashes on it.
		deep := strings.Repeat("[", 20000) + strings.Repeat("]", 20000)
		got := l.decode(t, o, []byte(strings.Replace(clean, "{", `{"unknownMember":`+deep+`,`, 1)))
		_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
	})
}

// srdText is a JSON string literal and the Go string it decodes to.
type srdText struct {
	name    string
	literal string
	want    string
}

// srdTexts returns JSON string literals that every string member must decode
// exactly: escapes, surrogate pairs, line separators, quotes and backslashes,
// kept as decoded, never trimmed or normalized.
func srdTexts() []srdText {
	smile := string(rune(0x1F600))
	return []srdText{
		{"plain", `"Example"`, "Example"},
		{"empty", `""`, ""},
		{"spaces kept", `"  padded  "`, "  padded  "},
		{"short escapes", `"\"\\\/\b\f\n\r\t"`, "\"\\/\b\f\n\r\t"},
		{"unicode escapes", `"\u0041\u00e9\u00E9"`, "A\u00e9\u00e9"},
		{"escapes beyond Latin", `"\u4e2d\u6587"`, string([]rune{0x4e2d, 0x6587})},
		{"surrogate pair", `"\ud83d\ude00"`, smile},
		{"surrogate pair in upper case", `"\uD83D\uDE00"`, smile},
		{"raw supplementary character", `"` + smile + `"`, smile},
		// encoding/json documents that an invalid UTF-16 surrogate becomes
		// U+FFFD; raw invalid UTF-8, by contrast, refuses the body.
		{"lone high surrogate", `"a\ud800b"`, "a\uFFFDb"},
		{"lone low surrogate", `"\udc00"`, "\uFFFD"},
		{"reversed pair", `"\ude00\ud83d"`, "\uFFFD\uFFFD"},
		{"raw line separators", "\"a\u2028b\u2029c\"", "a\u2028b\u2029c"},
		{"escaped line separators", `"a\u2028b\u2029c"`, "a\u2028b\u2029c"},
		{"escaped NUL", `"a\u0000b"`, "a\x00b"},
		{"escaped DEL and NEL", `"\u007f\u0085"`, "\x7f\u0085"},
		{"raw HTML", `"<b>&amp;</b>"`, "<b>&amp;</b>"},
		{"escaped HTML", `"\u003cb\u003e\u0026"`, "<b>&"},
		{"quotes and backslashes", `"say \"hi\" to C:\\dir\\"`, `say "hi" to C:\dir\`},
		{"text that looks like JSON", `"{\"organic\":[null]}"`, `{"organic":[null]}`},
		{"the word null", `"null"`, "null"},
		{"raw replacement character", "\"\uFFFD\"", "\uFFFD"},
	}
}

// srdBadStrings returns JSON string texts that are not valid JSON: a body
// holding one is refused as malformed with no field.
func srdBadStrings() []srdNamed {
	return []srdNamed{
		{"raw control character", "\"a\x01b\""},
		{"raw tab", "\"a\tb\""},
		{"raw newline", "\"a\nb\""},
		{"raw DEL is fine but a raw NUL is not", "\"\x7f\x00\""},
		{"unknown escape", `"\x41"`},
		{"single-quote escape", `"\'"`},
		{"short unicode escape", `"\u12"`},
		{"upper-case U escape", `"\U0041"`},
		{"unterminated", `"abc`},
	}
}

// srdLong returns a string of at least n bytes mixing plain text and
// characters that JSON escapes, and its JSON literal.
func srdLong(n int) (value, literal string) {
	const piece = "a\"\\\u00e9\U0001F600\u2028 "
	reps := n/len(piece) + 1
	value = strings.Repeat(piece, reps)
	return value, testutil.JSONQuote(value)
}

// srdCreditsIn returns the credits of a test body: reported 4 when it holds
// "credits":4, else none.
func srdCreditsIn(body string) serper.Credits {
	if strings.Contains(body, `"credits":4`) {
		return reported(4)
	}
	return serper.Credits{}
}
