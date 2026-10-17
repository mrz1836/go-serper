package serper_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// The Scrape decoding suite. Its helpers (srdLive, srdRun..., the value
// tables) are the shared decoding harness, in decode_harness_test.go.

// scdWant returns the Decode helper's Scrape response for text and markdown.
func scdWant(text string, markdown *string) *serper.ScrapeResponse {
	return &serper.ScrapeResponse{Text: text, Markdown: markdown, Meta: serper.Metadata{Op: serper.OpScrape}}
}

// scdPaths returns the typed member paths of a Scrape answer: text, which
// may not be null, and markdown, before and after text.
func scdPaths() []srdPath {
	return []srdPath{
		{field: "text", kind: srdKindString, template: `{"text":@}`, required: true},
		{field: "text", kind: srdKindString, template: `{"markdown":"# Example","text":@,"metadata":{}}`, required: true},
		{field: "markdown", kind: srdKindString, template: `{"text":"Example","markdown":@}`},
		{field: "markdown", kind: srdKindString, template: `{"markdown":@,"text":"Example"}`},
	}
}

func TestScrapeDecodeText(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	for _, tt := range []struct {
		name, body, want string
	}{
		{"empty text is a valid value", `{"text":""}`, ""},
		{"only spaces, never trimmed", `{"text":"  "}`, "  "},
		{"escaped CRLF, never normalized", `{"text":"a\r\nb\rc\n"}`, "a\r\nb\rc\n"},
		{"the word null", `{"text":"null"}`, "null"},
		{"with untyped members", `{"metadata":{"title":"Example"},"text":"Example","links":[null]}`, "Example"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			assert.Equal(t, scdWant(tt.want, nil), valueOf[serper.ScrapeResponse](t, got))
		})
	}
	for i, body := range []string{
		`{}`, `{"text":null}`, `{"text":null,"markdown":"# Example"}`, `{"metadata":{"text":"nested"}}`,
		`{"Text ":"x"}`, `{"texts":"x"}`, `{"content":"x"}`, `{"text_":"x","credits":4}`,
	} {
		t.Run("missing "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: "text", Op: o.op, Credits: srdCreditsIn(body),
			})
		})
	}
}

func TestScrapeDecodeMarkdown(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	for _, tt := range []struct {
		name string
		body string
		want *serper.ScrapeResponse
	}{
		{"absent", `{"text":"Example"}`, scdWant("Example", nil)},
		{"null", `{"text":"Example","markdown":null}`, scdWant("Example", nil)},
		{"empty", `{"text":"Example","markdown":""}`, scdWant("Example", new(""))},
		{"a string", `{"text":"Example","markdown":"# Example"}`, scdWant("Example", new("# Example"))},
		{"only spaces", `{"text":"Example","markdown":"  "}`, scdWant("Example", new("  "))},
		{"never derived from text, nor text from it", `{"markdown":"# Example","text":""}`, scdWant("", new("# Example"))},
		{"equal to text, kept apart", `{"text":"same","markdown":"same"}`, scdWant("same", new("same"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			assert.Equal(t, tt.want, valueOf[serper.ScrapeResponse](t, got))
		})
	}
	t.Run("a markdown-only reply fails", func(t *testing.T) {
		for _, body := range []string{
			`{"markdown":"# Example"}`, `{"markdown":""}`, `{"markdown":"m","text":null}`, `{"text":null,"markdown":"m"}`,
			`{"MARKDOWN":"m","credits":4}`, `{"credits":4,"markdown":"# Example","metadata":{"text":"nested"}}`,
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: "text", Op: o.op, Credits: srdCreditsIn(body),
			})
		}
	})
}

func TestScrapeDecodeWrongTypes(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	t.Run("wrong", func(t *testing.T) { srdRunWrongTypes(t, l, o, scdPaths()) })
	t.Run("right", func(t *testing.T) { srdRunRightTypes(t, l, o, scdPaths()) })
	t.Run("the earliest wrong type is reported", func(t *testing.T) {
		// encoding/json documents that Unmarshal reports the earliest of the
		// type errors it skips over.
		for body, field := range map[string]string{
			`{"text":1,"markdown":2}`: "text",
			`{"markdown":2,"text":1}`: "markdown",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
}

func TestScrapeDecodeStrings(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	for _, tt := range srdTexts() {
		t.Run(tt.name, func(t *testing.T) {
			res := valueOf[serper.ScrapeResponse](t, l.decode(t, o, []byte(srdAt(`{"text":@,"markdown":@}`, tt.literal))))
			assert.Equal(t, scdWant(tt.want, new(tt.want)), res, "text and markdown decoded exactly")
			srdAssertRoundTrip(t, o, res)
		})
	}
	for _, tt := range srdBadStrings() {
		t.Run("refused "+tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(srdAt(`{"text":@}`, tt.body)))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
		})
	}
	t.Run("very long strings", func(t *testing.T) {
		long, literal := srdLong(512 << 10)
		plain := strings.Repeat("Example page text. ", (2<<20)/19+1)
		res := valueOf[serper.ScrapeResponse](t, l.decode(t, o, []byte(`{"text":"`+plain+`","markdown":`+literal+`}`)))
		assert.Equal(t, scdWant(plain, &long), res, "kept whole")
		srdAssertRoundTrip(t, o, res)
	})
	t.Run("generated page", func(t *testing.T) {
		res := valueOf[serper.ScrapeResponse](t, l.decode(t, o, testutil.ScrapeBody(1<<20, true)))
		assert.Len(t, res.Text, 1<<20)
		require.NotNil(t, res.Markdown)
		assert.Equal(t, res.Text, *res.Markdown)
	})
}

func TestScrapeDecodeUnknownMembers(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	srdRunNoise(t, l, o, `{<top>"text":"Example text.","metadata":{<metadata>"title":"Example"},"markdown":"# Example"<end>}`)
	t.Run("near misses of member names are unknown", func(t *testing.T) {
		body := `{"text":"t","text ":1,"texts":[],"markdowns":1,"mark_down":{},"Markdown ":[],"credit":"x"}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		assert.Equal(t, scdWant("t", nil), valueOf[serper.ScrapeResponse](t, got))
	})
	t.Run("members of other operations are unknown", func(t *testing.T) {
		body := `{"text":"t","searchParameters":1,"organic":[null],"images":"x","knowledgeGraph":[]}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		assert.Equal(t, scdWant("t", nil), valueOf[serper.ScrapeResponse](t, got))
	})
}

func TestScrapeDecodeMemberMatching(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	t.Run("respelled names", func(t *testing.T) { srdRunRenamed(t, l, o, "scrape", []string{"text", "markdown", "credits"}) })
	t.Run("escaped names", func(t *testing.T) {
		body := `{"\u0074ext":"t","m\u0061rkdown":"m","cr\u0065dits":2}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		res := valueOf[serper.ScrapeResponse](t, got)
		assert.Equal(t, &serper.ScrapeResponse{
			Text: "t", Markdown: new("m"), Meta: serper.Metadata{Op: serper.OpScrape, Credits: reported(2)},
		}, res)
	})
	t.Run("failure fields use the SDK's names, not the body's spelling", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"TEXT":1}`:                           "text",
			`{"text":"t","MarkDown":[]}`:           "markdown",
			`{"Markdown":"m"}`:                     "text",
			"{\"text\":\"t\",\"mar\u212adown\":1}": "markdown",
			`{"text":"t","MARKDOWN":{}}`:           "markdown",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
	t.Run("duplicate members", func(t *testing.T) { scdCheckDuplicates(t, l, o) })
}

// scdCheckDuplicates checks duplicate members of a Scrape answer, decoded as
// encoding/json decodes them: the last wins, and a null clears a pointer.
func scdCheckDuplicates(t *testing.T, l *srdLive, o opCase) {
	t.Helper()
	for _, tt := range []struct {
		name string
		body string
		want *serper.ScrapeResponse
	}{
		{"text, last wins", `{"text":"a","text":"b"}`, scdWant("b", nil)},
		{"null text then text", `{"text":null,"text":"a"}`, scdWant("a", nil)},
		{"markdown, last wins", `{"text":"t","markdown":"a","markdown":"b"}`, scdWant("t", new("b"))},
		{"markdown then null", `{"text":"t","markdown":"a","markdown":null}`, scdWant("t", nil)},
		{"null markdown then markdown", `{"text":"t","markdown":null,"markdown":""}`, scdWant("t", new(""))},
		{"another spelling then the exact name: the last wins", `{"TEXT":"b","text":"a"}`, scdWant("a", nil)},
		{"the exact name then another spelling: the last wins", `{"text":"a","Text":"b"}`, scdWant("b", nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			assert.Equal(t, tt.want, valueOf[serper.ScrapeResponse](t, got))
		})
	}
	// encoding/json keeps the earliest type error, even when a later
	// duplicate has the right type; a later null clears the text.
	for i, tt := range []struct{ body, field string }{
		{`{"text":1,"text":"t"}`, "text"},
		{`{"text":"t","text":1}`, "text"},
		{`{"text":"t","text":null}`, "text"},
		{`{"text":"t","markdown":[],"markdown":"m"}`, "markdown"},
	} {
		t.Run("duplicate "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", tt.body)
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op})
		})
	}
}

func TestScrapeDecodeShapeBeforeSuccessRule(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	for i, tt := range []struct{ body, field string }{
		// A markdown of the wrong type is reported before the missing text.
		{`{"markdown":1}`, "markdown"},
		{`{"markdown":{},"credits":4}`, "markdown"},
		{`{"text":null,"markdown":[]}`, "markdown"},
		{`{"markdown":[null],"text":null}`, "markdown"},
		{`{"markdown":true,"text":1}`, "markdown"},
		// With no shape problem, the success rule reports the text.
		{`{"markdown":"m","credits":4}`, "text"},
		{`{"markdown":null}`, "text"},
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", tt.body)
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op, Credits: srdCreditsIn(tt.body),
			})
		})
	}
}

func TestScrapeDecodeCredits(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	malformed := serper.FailureMalformedResponse
	srdRunCredits(t, l, o, []srdCreditsCase{
		{"success", `{"text":"Example"}`, "", ""},
		{"success with empty text", `{"text":""}`, "", ""},
		{"success with markdown", `{"text":"Example","markdown":"# Example"}`, "", ""},
		{"wrong text type", `{"text":1}`, malformed, "text"},
		{"wrong markdown type", `{"text":"Example","markdown":1}`, malformed, "markdown"},
		{"markdown only", `{"markdown":"# Example"}`, malformed, "text"},
		{"null text", `{"text":null}`, malformed, "text"},
		{"empty object", `{}`, malformed, "text"},
	})
	t.Run("only the top-level member counts", func(t *testing.T) {
		res := valueOf[serper.ScrapeResponse](t, l.decode(t, o, []byte(`{"text":"t","metadata":{"credits":4}}`)))
		assert.Equal(t, serper.Credits{}, res.Meta.Credits)
	})
	t.Run("the member name matches as encoding/json matches", func(t *testing.T) {
		res := valueOf[serper.ScrapeResponse](t, l.decode(t, o, []byte(`{"text":"t","CREDITS":4}`)))
		assert.Equal(t, reported(4), res.Meta.Credits)
	})
	for i, body := range []string{`{"credits":4,"text":"t"`, `{"credits":4,"text":"t"}{}`, `[{"credits":4,"text":"t"}]`} {
		t.Run("not one valid object "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: malformed, Op: o.op})
		})
	}
}

func TestScrapeDecodeAddedFixtures(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Scrape")
	for _, tt := range []struct {
		file string
		want *serper.ScrapeResponse
	}{
		{"empty-text.json", &serper.ScrapeResponse{Meta: serper.Metadata{Op: serper.OpScrape, Credits: reported(0)}}},
		{"markdown-null.json", &serper.ScrapeResponse{
			Text: "Example Domain\nSynthetic text with no Markdown rendering.",
			Meta: serper.Metadata{Op: serper.OpScrape, Credits: reported(2)},
		}},
		{"markdown-empty.json", &serper.ScrapeResponse{
			Text: "Example Domain", Markdown: new(""), Meta: serper.Metadata{Op: serper.OpScrape, Credits: reported(1)},
		}},
		{"escapes.json", &serper.ScrapeResponse{
			Text: "caf\u00e9 \"quoted\" \\ back/slash\r\nLine \U0001F600 two\t\u2028\u2029 NUL \x00 lone \uFFFD",
			Markdown: new("# Caf\u00e9\n\n> \"quoted\" `code` \\*not emphasis\\* <b>&amp;</b>\n\n" +
				"[link](https://www.example.com/a?b=c&d=e)"),
			Meta: serper.Metadata{Op: serper.OpScrape, Credits: reported(2)},
		}},
		{"pretty.json", &serper.ScrapeResponse{
			Text:     "Example Domain\nThis synthetic page is for use in examples.",
			Markdown: new("# Example Domain\n\nThis synthetic page is for use in examples."),
			Meta:     serper.Metadata{Op: serper.OpScrape, Credits: reported(2)},
		}},
	} {
		t.Run("values of "+tt.file, func(t *testing.T) {
			assert.Equal(t, tt.want, valueOf[serper.ScrapeResponse](t, l.decode(t, o, fixture(t, "scrape/"+tt.file))))
		})
	}
}
