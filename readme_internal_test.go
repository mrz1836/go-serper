package serper

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readmeSources are the compiled files whose code the README's Go blocks must
// quote: the package's examples and the example program.
func readmeSources() []string {
	return []string{"example_test.go", "examples/main.go"}
}

// readmeDocs are the Markdown files whose links and placeholders are checked.
func readmeDocs() []string {
	return []string{"README.md", "docs/adding-an-operation.md", "testdata/synthetic/README.md"}
}

// readmeFile returns the file name, failing tb when it cannot be read.
func readmeFile(tb testing.TB, name string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.FromSlash(name))
	require.NoError(tb, err)
	return string(data)
}

// readmeProse returns md without its fenced code blocks, so that code such as
// errors.AsType[*serper.CallError](err) is never read as a link.
func readmeProse(md string) string {
	return regexp.MustCompile("(?s)```.*?```").ReplaceAllString(md, "")
}

// readmeNormalize collapses every run of whitespace to one space, so that a
// README block matches its source at any indentation.
func readmeNormalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// readmeFences returns the content of every fenced code block in md whose
// info string is lang.
func readmeFences(md, lang string) []string {
	re := regexp.MustCompile("(?s)```" + regexp.QuoteMeta(lang) + "\n(.*?)\n```")
	matches := re.FindAllStringSubmatch(md, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func TestReadmeGoBlocksAreCompiled(t *testing.T) {
	var sources strings.Builder
	for _, name := range readmeSources() {
		sources.WriteString(readmeNormalize(readmeFile(t, name)))
		sources.WriteString("\n")
	}
	compiled := sources.String()
	for _, doc := range []string{"README.md"} {
		blocks := readmeFences(readmeFile(t, doc), "go")
		require.NotEmpty(t, blocks)
		for i, block := range blocks {
			assert.Contains(t, compiled, readmeNormalize(block),
				"%s Go block %d is not quoted verbatim from %v:\n%s", doc, i+1, readmeSources(), block)
		}
	}
}

func TestReadmeHasNoPlaceholders(t *testing.T) {
	for _, doc := range readmeDocs() {
		md := readmeFile(t, doc)
		assert.NotContains(t, md, "<!--", "every placeholder in %s must be filled in", doc)
		for _, leftover := range []string{"go-template", "TODO", "TBD", "lorem"} {
			assert.NotContains(t, md, leftover, "%s has no leftover %q", doc, leftover)
		}
	}
}

func TestReadmeRelativeLinksResolve(t *testing.T) {
	re := regexp.MustCompile(`(?:\]\(|href=")([^)"#][^)"]*)`)
	for _, doc := range readmeDocs()[:2] {
		links := re.FindAllStringSubmatch(readmeProse(readmeFile(t, doc)), -1)
		require.NotEmpty(t, links, "%s has relative links", doc)
		for _, m := range links {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			_, err := os.Stat(filepath.Join(filepath.Dir(doc), filepath.FromSlash(target)))
			assert.NoError(t, err, "%s links to a missing file: %s", doc, target)
		}
	}
}

// readmeSlug returns GitHub's anchor for a heading: lowercase, keeping
// letters, marks, numbers, underscores, hyphens and spaces, with each space
// turned into a hyphen.
func readmeSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestReadmeSlug(t *testing.T) {
	assert.Equal(t, "-examples--tests", readmeSlug("🧪 Examples & Tests"))
	assert.Equal(t, "raw-capture-and-replay", readmeSlug("Raw capture and replay"))
	assert.Equal(t, "\ufe0f-behavior", readmeSlug("⚙️ Behavior"), "the emoji's variation selector is a mark")
}

func TestReadmeAnchorsMatchHeadings(t *testing.T) {
	for _, doc := range readmeDocs()[:2] {
		md := readmeFile(t, doc)
		slugs := map[string]bool{}
		inFence := false
		for line := range strings.SplitSeq(md, "\n") {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence || !strings.HasPrefix(line, "#") {
				continue
			}
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			heading = strings.ReplaceAll(heading, "&nbsp;", " ")
			slugs[readmeSlug(heading)] = true
		}
		anchors := regexp.MustCompile(`(?:\]\(|href=")#([^)"]+)`).FindAllStringSubmatch(readmeProse(md), -1)
		if doc == "README.md" {
			require.NotEmpty(t, anchors)
		}
		for _, m := range anchors {
			anchor, err := url.PathUnescape(m[1])
			require.NoError(t, err)
			assert.True(t, slugs[anchor], "%s links to #%s, which no heading produces", doc, m[1])
		}
	}
	// A link into the README from another document must name a heading too.
	readmeSlugs := map[string]bool{}
	for line := range strings.SplitSeq(readmeFile(t, "README.md"), "\n") {
		if strings.HasPrefix(line, "#") {
			readmeSlugs[readmeSlug(strings.ReplaceAll(strings.TrimSpace(strings.TrimLeft(line, "#")), "&nbsp;", " "))] = true
		}
	}
	links := regexp.MustCompile(`README\.md#([^)"]+)`).FindAllStringSubmatch(readmeFile(t, "docs/adding-an-operation.md"), -1)
	require.NotEmpty(t, links)
	for _, m := range links {
		assert.True(t, readmeSlugs[m[1]], "docs link to README.md#%s, which no heading produces", m[1])
	}
}

// TestReadmeLimitsMatchCode checks every default and limit the README states
// against the constant that enforces it. A claim may wrap across lines.
func TestReadmeLimitsMatchCode(t *testing.T) {
	md := readmeNormalize(readmeFile(t, "README.md"))
	tr := newTransport()
	tests := []struct {
		claim string
		ok    bool
	}{
		{"30 seconds (`DefaultTimeout`), every operation", DefaultTimeout == 30*time.Second},
		{"more than 0, at most 10 minutes (`MaxTimeout`)", MaxTimeout == 10*time.Minute},
		{"64 KiB (`DefaultMaxRequestBytes`) | 1 KiB to 1 MiB", DefaultMaxRequestBytes == 64<<10 &&
			minRequestBytesLimit == 1<<10 && maxRequestBytesLimit == 1<<20},
		{"8 MiB (`DefaultMaxResponseBytes`) | 1 KiB to 64 MiB (`MaxResponseBytes`)", DefaultMaxResponseBytes == 8<<20 &&
			minResponseBytesLimit == 1<<10 && MaxResponseBytes == 64<<20},
		{"`go-serper` (`DefaultUserAgent`) | 1 to 256 bytes", DefaultUserAgent == "go-serper" && maxUserAgentBytes == 256},
		{"`https://google.serper.dev`", DefaultSearchBaseURL == "https://google.serper.dev"},
		{"`https://scrape.serper.dev`", DefaultScrapeBaseURL == "https://scrape.serper.dev"},
		{"| API key | 1 to 256 bytes of visible ASCII |", maxKeyBytes == 256},
		{
			"| Encoded request body | 64 KiB by default (`WithMaxRequestBytes`, 1 KiB to 1 MiB) |",
			DefaultMaxRequestBytes == 64<<10 && maxRequestBytesLimit == 1<<20,
		},
		{
			"| HTTP 200 body, after decompression | 8 MiB by default (`WithMaxResponseBytes`, 1 KiB to 64 MiB) |",
			DefaultMaxResponseBytes == 8<<20 && MaxResponseBytes == 64<<20,
		},
		{"before any byte arrives | 1 MiB |", maxPrealloc == 1<<20},
		{"| Error body read for connection reuse | 4 KiB, never kept, within the call's deadline |", maxErrorBodyBytes == 4<<10},
		{"| 100 empty reads in a row |", maxEmptyReads == 100},
		{"| Response headers (SDK-built transport) | 64 KiB |", tr.MaxResponseHeaderBytes == 64<<10},
		{
			"| Connection setup (SDK-built transport) | 10-second dial timeout (DNS lookup included), 10-second TLS handshake timeout |",
			dialTimeout == 10*time.Second && tr.TLSHandshakeTimeout == 10*time.Second,
		},
		{
			"| Idle connections (SDK-built transport) | 8 per host, 16 in all, closed after 4 seconds idle |",
			tr.MaxIdleConnsPerHost == 8 && tr.MaxIdleConns == 16 && tr.IdleConnTimeout == 4*time.Second,
		},
		{
			"| Operation timeout (`WithTimeout`, `WithScrapeTimeout`) | 30 seconds by default, more than 0, at most 10 minutes |",
			DefaultTimeout == 30*time.Second && MaxTimeout == 10*time.Minute,
		},
		{
			"gives up on a dial after 10 seconds (the DNS lookup included) and on a TLS handshake after 10 seconds",
			dialTimeout == 10*time.Second && tr.TLSHandshakeTimeout == 10*time.Second,
		},
		{"uses a 30-second TCP keep-alive", dialKeepAlive == 30*time.Second},
		{
			"keeps at most 8 idle connections per host and 16 in all, each for 4 seconds",
			tr.MaxIdleConnsPerHost == 8 && tr.MaxIdleConns == 16 && tr.IdleConnTimeout == 4*time.Second,
		},
		{
			"as the SDK transport's 10-second dial or TLS handshake timeout",
			dialTimeout == 10*time.Second && tr.TLSHandshakeTimeout == 10*time.Second,
		},
		{"speaks **HTTP/1.1 only**", tr.Protocols != nil && tr.Protocols.HTTP1() && !tr.Protocols.HTTP2()},
		{"**never reads the proxy environment variables**", tr.Proxy == nil},
		{"requires TLS 1.2 or later", tr.TLSClientConfig.MinVersion == 0x0303},
		{"an optional port from 1 to 65535", validPort("h:1", "1") && validPort("h:65535", "65535") && !validPort("h:0", "0")},
	}
	for _, tt := range tests {
		assert.Contains(t, md, readmeNormalize(tt.claim))
		assert.True(t, tt.ok, "the code no longer matches the README claim %q", tt.claim)
	}
}

func TestReadmeFencesAreGoOrBash(t *testing.T) {
	for _, doc := range readmeDocs() {
		inFence := false
		for i, line := range strings.Split(readmeFile(t, doc), "\n") {
			if !strings.HasPrefix(line, "```") {
				continue
			}
			if info := strings.TrimPrefix(line, "```"); !inFence {
				assert.Contains(t, []string{"go", "bash"}, info, "%s:%d opens a fence as %q", doc, i+1, info)
			}
			inFence = !inFence
		}
		assert.False(t, inFence, "%s closes every fence", doc)
	}
}

// readmeSourceFiles parses the package's non-test source files.
func readmeSourceFiles(tb testing.TB) []*ast.File {
	tb.Helper()
	names, err := filepath.Glob("*.go")
	require.NoError(tb, err)
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		require.NoError(tb, err)
		files = append(files, f)
	}
	require.NotEmpty(tb, files)
	return files
}

// readmeDecls returns the exported names the package's non-test source files
// declare (see readmeDeclNames).
func readmeDecls(tb testing.TB) map[string]bool {
	tb.Helper()
	out := map[string]bool{}
	for _, f := range readmeSourceFiles(tb) {
		for _, decl := range f.Decls {
			for _, name := range readmeDeclNames(decl) {
				out[name] = true
			}
		}
	}
	return out
}

// readmeDeclNames returns the exported names decl declares: "Name" at top
// level, and "Type.Name" for a method or a struct field.
func readmeDeclNames(decl ast.Decl) []string {
	var out []string
	add := func(prefix string, idents ...*ast.Ident) {
		for _, ident := range idents {
			if ident.IsExported() {
				out = append(out, prefix+ident.Name)
			}
		}
	}
	switch d := decl.(type) {
	case *ast.FuncDecl:
		add(readmeRecvPrefix(d.Recv), d.Name)
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				add("", s.Name)
				if st, ok := s.Type.(*ast.StructType); ok {
					for _, field := range st.Fields.List {
						add(s.Name.Name+".", field.Names...)
					}
				}
			case *ast.ValueSpec:
				add("", s.Names...)
			}
		}
	}
	return out
}

// readmeRecvPrefix returns "Type." for the receiver of a method, or "" for a
// function.
func readmeRecvPrefix(recv *ast.FieldList) string {
	if recv == nil {
		return ""
	}
	typ := recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	return readmeTypeName(typ) + "."
}

// TestReadmeIdentifiersExist checks every backticked SDK identifier in the
// README's prose, such as `WithTimeout`, `*serper.CallError`, `Credits.Used`
// or `WithRawCapture(true)`, against the package's declarations: a name with
// an SDK prefix must be declared, and a member of a declared type must be one
// of its fields or methods. Other names, such as `GetBody`, are not the SDK's.
func TestReadmeIdentifiersExist(t *testing.T) {
	decls := readmeDecls(t)
	sdk := regexp.MustCompile(`^(?:With|Err|Default|Max|Failure|Op|Credits|RawCapture|Decode)[A-Z]`)
	ident := regexp.MustCompile(`^(?:\(?\*)?(?:serper\.)?([A-Z]\w*)\)?(?:\.([A-Z]\w*))?(?:\(.*\))?$`)
	checked := map[string]bool{}
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(readmeProse(readmeFile(t, "README.md")), -1) {
		parts := ident.FindStringSubmatch(m[1])
		if parts == nil {
			continue
		}
		name, member := parts[1], parts[2]
		switch {
		case decls[name] && member != "":
			assert.True(t, decls[name+"."+member], "the README names %s, which %s does not declare", m[1], name)
			checked[name+"."+member] = true
		case decls[name]:
			checked[name] = true
		case sdk.MatchString(name):
			assert.Fail(t, "the README names an undeclared identifier", "%s", m[1])
		}
	}
	for _, want := range []string{"WithTimeout", "WithRawCapture", "ErrOutcomeUnknown", "CallError.Unwrap", "Credits.Used"} {
		assert.True(t, checked[want], "the README's %s is checked", want)
	}
}

// readmeTable returns the rows of the Markdown table in md whose header row is
// header, each split into its trimmed cells.
func readmeTable(tb testing.TB, md, header string) [][]string {
	tb.Helper()
	_, rest, ok := strings.Cut(md, header+"\n")
	require.True(tb, ok, "the README has the table %q", header)
	var rows [][]string
	for line := range strings.SplitSeq(rest, "\n") {
		if !strings.HasPrefix(line, "|") {
			break
		}
		if strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	require.NotEmpty(tb, rows, "the table %q has rows", header)
	return rows
}

// TestReadmeStatusTableMatchesCode checks each status row of the error
// reference against statusKind and statusOutcomeUnknown: every status it
// names has the row's kind and outcome, and no status row goes unchecked.
func TestReadmeStatusTableMatchesCode(t *testing.T) {
	tests := []struct {
		row   string // the start of the row's first cell
		codes []int
		kind  FailureKind
	}{
		{"HTTP 401", []int{http.StatusUnauthorized}, FailureUnauthorized},
		{"HTTP 403", []int{http.StatusForbidden}, FailureForbidden},
		{"HTTP 429", []int{http.StatusTooManyRequests}, FailureRateLimited},
		{"Any other HTTP 4xx", []int{400, 402, 404, 405, 409, 413, 422, 451, 499}, FailureRejected},
		{"HTTP 5xx", []int{500, 501, 502, 503, 504, 599}, FailureUpstream},
		{"HTTP 3xx other than 303 (never followed)", []int{300, 301, 302, 304, 307, 308, 399}, FailureUnexpectedResponse},
		{
			"HTTP 303 (never followed), 1xx, a 2xx other than 200, below 100, 600 and above",
			[]int{303, 100, 101, 199, 201, 204, 206, 299, 0, 1, 99, 600, 999},
			FailureUnexpectedResponse,
		},
	}
	rows := readmeTable(t, readmeFile(t, "README.md"), "| What happened | `Kind` | `errors.Is` matches | `OutcomeUnknown` |")
	checked := map[string]bool{}
	for _, tt := range tests {
		i := slices.IndexFunc(rows, func(r []string) bool { return strings.HasPrefix(r[0], tt.row) })
		require.GreaterOrEqual(t, i, 0, "the error reference has a row %q", tt.row)
		checked[rows[i][0]] = true
		assert.Equal(t, "`"+string(tt.kind)+"`", rows[i][1], "the row %q names its kind", tt.row)
		assert.Contains(t, rows[i][2], "`"+kindSentinelName(tt.kind)+"`", "the row %q names its sentinel", tt.row)
		for _, code := range tt.codes {
			assert.Equal(t, tt.kind, statusKind(code), "HTTP %d, as the row %q says", code, tt.row)
			assert.Equal(t, rows[i][3] == "true", statusOutcomeUnknown(code), "HTTP %d outcome, as %q says", code, tt.row)
		}
	}
	for _, r := range rows {
		if strings.HasPrefix(r[0], "HTTP ") && !strings.HasPrefix(r[0], "HTTP 200") || strings.HasPrefix(r[0], "Any other HTTP") {
			assert.True(t, checked[r[0]], "the status row %q is checked against statusKind", r[0])
		}
	}
}

// kindSentinelName returns the exported name of a kind's sentinel.
func kindSentinelName(kind FailureKind) string {
	names := map[error]string{
		ErrUnauthorized: "ErrUnauthorized", ErrForbidden: "ErrForbidden", ErrRateLimited: "ErrRateLimited",
		ErrRejected: "ErrRejected", ErrUpstream: "ErrUpstream", ErrUnexpectedResponse: "ErrUnexpectedResponse",
	}
	return names[kindSentinel(kind)]
}

// readmeConsts returns the string constants of the package's non-test source
// files whose type is named typ, or, for an empty typ, the untyped ones whose
// name starts with prefix, by name.
func readmeConsts(tb testing.TB, typ, prefix string) map[string]string {
	tb.Helper()
	out := map[string]string{}
	for _, f := range readmeSourceFiles(tb) {
		for _, spec := range readmeConstSpecs(f) {
			if readmeTypeName(spec.Type) != typ {
				continue
			}
			for i, ident := range spec.Names {
				lit, ok := spec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !strings.HasPrefix(ident.Name, prefix) {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				require.NoError(tb, err)
				out[ident.Name] = v
			}
		}
	}
	require.NotEmpty(tb, out, "the package has %s constants", typ+prefix)
	return out
}

// readmeConstSpecs returns the value specs of f's const declarations that
// give each name a value.
func readmeConstSpecs(f *ast.File) []*ast.ValueSpec {
	var out []*ast.ValueSpec
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Values) == len(vs.Names) {
				out = append(out, vs)
			}
		}
	}
	return out
}

// readmeTypeName returns the name of a type expression that is a plain
// identifier, or "".
func readmeTypeName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// readmeAssertPairs asserts that md spells every constant of consts as
// "`value` (`Name`)", and that every such pair in md whose name starts with
// prefix names a constant with that value.
func readmeAssertPairs(tb testing.TB, md, prefix string, consts map[string]string) {
	tb.Helper()
	md = readmeNormalize(md)
	for name, value := range consts {
		assert.Contains(tb, md, "`"+value+"` (`"+name+"`)", "the README names %s", name)
	}
	re := regexp.MustCompile("`([a-z_]+)` \\(`(" + prefix + "\\w+)`\\)")
	for _, m := range re.FindAllStringSubmatch(md, -1) {
		v, ok := consts[m[2]]
		assert.True(tb, ok, "the README names %s, which is not a constant", m[2])
		assert.Equal(tb, v, m[1], "the README spells %s", m[2])
	}
}

// readmeEnumNames returns the names of the constants in the package's const
// block whose first constant has type typ, such as an iota enumeration.
func readmeEnumNames(tb testing.TB, typ string) []string {
	tb.Helper()
	var out []string
	for _, f := range readmeSourceFiles(tb) {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST || len(gd.Specs) == 0 {
				continue
			}
			if first, ok := gd.Specs[0].(*ast.ValueSpec); !ok || readmeTypeName(first.Type) != typ {
				continue
			}
			for _, spec := range gd.Specs {
				for _, ident := range spec.(*ast.ValueSpec).Names {
					out = append(out, ident.Name)
				}
			}
		}
	}
	require.NotEmpty(tb, out, "the package has %s constants", typ)
	return out
}

// readmeAssertEnum asserts that values holds every constant of the enum type
// typ, and that md spells each as "`value` (`Name`)" (see readmeAssertPairs).
func readmeAssertEnum[T fmt.Stringer](tb testing.TB, md, typ, prefix string, values map[string]T) {
	tb.Helper()
	assert.ElementsMatch(tb, readmeEnumNames(tb, typ), slices.Collect(maps.Keys(values)), "the test lists every %s", typ)
	consts := make(map[string]string, len(values))
	for name, v := range values {
		consts[name] = v.String()
	}
	readmeAssertPairs(tb, md, prefix, consts)
}

// TestReadmeConstantsMatchCode checks the README's lists of constants against
// the constants the package declares.
func TestReadmeConstantsMatchCode(t *testing.T) {
	md := readmeFile(t, "README.md")
	t.Run("FailureKind", func(t *testing.T) {
		readmeAssertPairs(t, md, "Failure", readmeConsts(t, "FailureKind", ""))
	})
	t.Run("Operation", func(t *testing.T) {
		readmeAssertPairs(t, md, "Op", readmeConsts(t, "Operation", ""))
	})
	t.Run("CreditsStatus", func(t *testing.T) {
		readmeAssertEnum(t, md, "CreditsStatus", "Credits", map[string]CreditsStatus{
			"CreditsNotReported": CreditsNotReported, "CreditsReported": CreditsReported, "CreditsInvalid": CreditsInvalid,
		})
	})
	t.Run("RawCaptureState", func(t *testing.T) {
		readmeAssertEnum(t, md, "RawCaptureState", "RawCapture", map[string]RawCaptureState{
			"RawCaptureOff": RawCaptureOff, "RawCaptureComplete": RawCaptureComplete,
			"RawCaptureNoResponse": RawCaptureNoResponse, "RawCaptureFailed": RawCaptureFailed,
		})
	})
	t.Run("validation reasons", func(t *testing.T) {
		var listed []string
		for _, r := range readmeTable(t, md, "| `Reason` | Meaning |") {
			for code := range strings.SplitSeq(r[0], " / ") {
				listed = append(listed, strings.Trim(code, "`"))
			}
		}
		assert.ElementsMatch(t, slices.Collect(maps.Values(readmeConsts(t, "", "reason"))), listed)
	})
	t.Run("raw capture states", func(t *testing.T) {
		rows := readmeTable(t, md, "| Outcome | `Meta.RawCapture` with capture on | Bytes |")
		states := make([]string, 0, len(rows))
		for _, r := range rows {
			states = append(states, strings.Trim(r[1], "`"))
		}
		for _, s := range []RawCaptureState{RawCaptureComplete, RawCaptureFailed, RawCaptureNoResponse} {
			assert.Contains(t, states, s.String())
		}
		assert.Contains(t, md, "With capture off the state is `"+RawCaptureOff.String()+"`")
	})
	t.Run("credits statuses", func(t *testing.T) {
		rows := readmeTable(t, md, "| `credits` in the body | `Credits.Status` | `Value()` |")
		statuses := make([]string, 0, len(rows))
		for _, r := range rows {
			statuses = append(statuses, strings.Trim(r[1], "`"))
		}
		assert.Equal(t, []string{CreditsNotReported.String(), CreditsReported.String(), CreditsInvalid.String()}, statuses)
	})
}
