package serper

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

// fzdSpace is JSON's insignificant whitespace (RFC 8259).
const fzdSpace = " \t\r\n"

// fzdSchema is the member tree one operation's decoder types, written out
// from the documented response members rather than read from the decoder.
type fzdSchema struct {
	op Operation
	// members lists the member names under each index-free path; "" is the
	// top level.
	members map[string][]string
	// lists marks the paths that hold a list, whose items a Field indexes.
	lists map[string]bool
	// attrs marks the paths that hold a provider attributes object.
	attrs map[string]bool
	// top is the JSON kind each typed top-level member must have when it is
	// not null: '{' an object, '[' a list, '"' a string.
	top map[string]byte
	// required is the top-level member the success rule needs.
	required string
	// fieldRE matches a Field built only from the member names, dots,
	// brackets and decimal indexes, and nothing else.
	fieldRE *regexp.Regexp
}

// fzdNewSchema returns the schema of op and compiles its Field oracle.
func fzdNewSchema(op Operation, members map[string][]string, lists, attrs []string, top map[string]byte,
	required string,
) fzdSchema {
	set := map[string]bool{}
	for _, names := range members {
		for _, n := range names {
			set[n] = true
		}
	}
	quoted := make([]string, 0, len(set))
	for n := range set {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	slices.Sort(quoted)
	seg := `(?:` + strings.Join(quoted, "|") + `)(?:\[(?:0|[1-9][0-9]*)\])?`
	s := fzdSchema{
		op: op, members: members, lists: map[string]bool{}, attrs: map[string]bool{}, top: top, required: required,
		fieldRE: regexp.MustCompile(`^` + seg + `(?:\.` + seg + `)*$`),
	}
	for _, l := range lists {
		s.lists[l] = true
	}
	for _, a := range attrs {
		s.attrs[a] = true
	}
	return s
}

// fzdParameterMembers are the members of a searchParameters echo.
func fzdParameterMembers() []string {
	return []string{"q", "type", "engine", "gl", "hl", "location", "tbs", "num", "page", "autocorrect"}
}

// fzdSearchSchema is the documented member tree of a Search answer.
func fzdSearchSchema() fzdSchema {
	return fzdNewSchema(OpSearch, map[string][]string{
		"":                 {"searchParameters", "knowledgeGraph", "organic", "peopleAlsoAsk", "relatedSearches"},
		"searchParameters": fzdParameterMembers(),
		"knowledgeGraph": {
			"title", "type", "website", "imageUrl", "description", "descriptionSource", "descriptionLink",
			"attributes",
		},
		"organic":           {"title", "link", "snippet", "date", "position", "sitelinks", "attributes"},
		"organic.sitelinks": {"title", "link"},
		"peopleAlsoAsk":     {"question", "snippet", "title", "link"},
		"relatedSearches":   {"query"},
	},
		[]string{"organic", "organic.sitelinks", "peopleAlsoAsk", "relatedSearches"},
		[]string{"knowledgeGraph.attributes", "organic.attributes"},
		map[string]byte{
			"searchParameters": '{', "knowledgeGraph": '{', "organic": '[', "peopleAlsoAsk": '[', "relatedSearches": '[',
		},
		"searchParameters")
}

// fzdImagesSchema is the documented member tree of an Images answer.
func fzdImagesSchema() fzdSchema {
	return fzdNewSchema(OpImages, map[string][]string{
		"":                 {"searchParameters", "images"},
		"searchParameters": fzdParameterMembers(),
		"images": {
			"title", "imageUrl", "imageWidth", "imageHeight", "thumbnailUrl", "thumbnailWidth", "thumbnailHeight",
			"source", "domain", "link", "googleUrl", "position",
		},
	},
		[]string{"images"}, nil,
		map[string]byte{"searchParameters": '{', "images": '['},
		"searchParameters")
}

// fzdScrapeSchema is the documented member tree of a Scrape answer.
func fzdScrapeSchema() fzdSchema {
	return fzdNewSchema(OpScrape, map[string][]string{"": {"text", "markdown"}}, nil, nil,
		map[string]byte{"text": '"', "markdown": '"'}, "text")
}

// fzdSchemaOf returns the schema of op.
func fzdSchemaOf(op Operation) fzdSchema {
	switch op {
	case OpSearch:
		return fzdSearchSchema()
	case OpImages:
		return fzdImagesSchema()
	case OpScrape:
		return fzdScrapeSchema()
	default:
		panic("no schema for operation " + strconv.Quote(string(op)))
	}
}

// fzdSegment is one dot-separated part of a Field.
type fzdSegment struct {
	indexed bool // it ends in "[n]"
	list    bool // its path holds a list
}

// walk checks that field is a path of the tree, each name a member of the
// path before it and an index only on a list, and returns its segments and
// its index-free path.
func (s fzdSchema) walk(field string) ([]fzdSegment, string, bool) {
	segs := make([]fzdSegment, 0, strings.Count(field, ".")+1)
	path := ""
	for part := range strings.SplitSeq(field, ".") {
		name, _, indexed := strings.Cut(part, "[")
		if !slices.Contains(s.members[path], name) {
			return nil, "", false
		}
		if path != "" {
			path += "."
		}
		path += name
		if indexed && !s.lists[path] {
			return nil, "", false
		}
		segs = append(segs, fzdSegment{indexed: indexed, list: s.lists[path]})
	}
	return segs, path, true
}

// fzdMember is one top-level member of a JSON object, as sent.
type fzdMember struct {
	name string
	raw  json.RawMessage
}

// fzdMembersOf returns the top-level members of data, a valid JSON object, in
// document order and with duplicates, as encoding/json's token reader sees
// them: names unescaped, values raw.
func fzdMembersOf(tb testing.TB, data []byte) []fzdMember {
	tb.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	require.NoError(tb, err)
	require.Equal(tb, json.Delim('{'), tok)
	var out []fzdMember
	for dec.More() {
		key, err := dec.Token()
		require.NoError(tb, err)
		name, ok := key.(string)
		require.True(tb, ok, "an object key is a string")
		var raw json.RawMessage
		require.NoError(tb, dec.Decode(&raw))
		out = append(out, fzdMember{name: name, raw: raw})
	}
	return out
}

// fzdLast returns the value of the last member whose name matches name as
// encoding/json matches a struct field: exactly or ignoring case, Unicode
// folding included (strings.EqualFold, which encoding/json's foldName is
// documented to agree with). The last one wins, as each overwrites the one
// before it.
func fzdLast(members []fzdMember, name string) (json.RawMessage, bool) {
	for _, m := range slices.Backward(members) {
		if strings.EqualFold(m.name, name) {
			return m.raw, true
		}
	}
	return nil, false
}

// fzdFirstByte returns the first byte of data that is not JSON whitespace,
// or 0 when there is none.
func fzdFirstByte(data []byte) byte {
	for _, c := range data {
		if !strings.ContainsRune(fzdSpace, rune(c)) {
			return c
		}
	}
	return 0
}

// fzdDigits reports whether raw is one or more ASCII decimal digits.
func fzdDigits(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// fzdWantCredits is the credits oracle, from the contract: an absent member
// or null is not reported; plain ASCII digits that strconv.ParseInt fits in
// an int64 are reported, 0 included; anything else is invalid.
func fzdWantCredits(raw []byte, present bool) Credits {
	if !present || string(raw) == "null" {
		return Credits{}
	}
	if !fzdDigits(raw) {
		return Credits{Status: CreditsInvalid}
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return Credits{Status: CreditsInvalid}
	}
	return Credits{Status: CreditsReported, Used: &n}
}

// fzdCreditsFrom is the credits a body's decoding must report: those of its
// last top-level "credits" member when the body is a valid JSON object, and
// not reported otherwise, since nothing was decoded.
func fzdCreditsFrom(members []fzdMember, object bool) Credits {
	if !object {
		return Credits{}
	}
	raw, ok := fzdLast(members, "credits")
	return fzdWantCredits(raw, ok)
}

// fzdCheckCreditsShape asserts the invariants of any Credits value.
func fzdCheckCreditsShape(t *testing.T, c Credits) {
	t.Helper()
	assert.Contains(t, []CreditsStatus{CreditsNotReported, CreditsReported, CreditsInvalid}, c.Status)
	assert.Equal(t, c.Status == CreditsReported, c.Used != nil, "Used is set exactly when reported")
	n, ok := c.Value()
	assert.Equal(t, c.Status == CreditsReported, ok)
	if c.Used != nil {
		assert.Equal(t, *c.Used, n)
		assert.GreaterOrEqual(t, n, int64(0), "plain digits are never negative")
	}
}

// fzdShape is what a typed top-level member decoded to: present (a non-nil
// pointer or list) and, for a list, its length (-1 for an object).
type fzdShape struct {
	present bool
	n       int
}

// fzdListShape returns the shape of a decoded list.
func fzdListShape[T any](list []T) fzdShape { return fzdShape{present: list != nil, n: len(list)} }

// fzdObjectShape returns the shape of a decoded object pointer.
func fzdObjectShape(present bool) fzdShape {
	if !present {
		return fzdShape{}
	}
	return fzdShape{present: true, n: -1}
}

// fzdShapeOfRaw returns the shape a member's last value must decode to:
// absent or null is nil, an object is present, a list keeps its length; and
// whether the list holds a null item.
func fzdShapeOfRaw(tb testing.TB, raw json.RawMessage, ok bool) (fzdShape, bool) {
	tb.Helper()
	switch {
	case !ok || string(raw) == "null":
		return fzdShape{}, false
	case raw[0] == '{':
		return fzdShape{present: true, n: -1}, false
	case raw[0] == '[':
		var items []json.RawMessage
		require.NoError(tb, json.Unmarshal(raw, &items))
		return fzdShape{present: true, n: len(items)},
			slices.ContainsFunc(items, func(it json.RawMessage) bool { return string(it) == "null" })
	}
	return fzdShape{present: true, n: -2}, false
}

// fzdStringOrNull reads raw as a JSON string (a pointer to it) or null (nil).
// ok is false for any other JSON value.
func fzdStringOrNull(tb testing.TB, raw json.RawMessage) (*string, bool) {
	tb.Helper()
	if string(raw) == "null" {
		return nil, true
	}
	if raw[0] != '"' {
		return nil, false
	}
	var s string
	require.NoError(tb, json.Unmarshal(raw, &s))
	return &s, true
}

// fzdFixedCauses are the only causes a decoding failure may keep.
func fzdFixedCauses() []error {
	return []error{errInvalidUTF8, errInvalidJSON, errNotObject, errWrongType, errNullItem, errMissingMember}
}

// fzdWantError renders the documented Error text of a Decode helper's
// failure, which has no status: "serper: <op>: <kind>[: <field>]".
func fzdWantError(op Operation, kind FailureKind, field string) string {
	s := "serper: " + string(op) + ": " + string(kind)
	if field != "" {
		s += ": " + field
	}
	return s
}

// fzdSentinel is the documented sentinel of a decoding failure kind.
func fzdSentinel(kind FailureKind) error {
	if kind == FailureUnexpectedResponse {
		return ErrUnexpectedResponse
	}
	return ErrMalformedResponse
}

// fzdTarget is one operation under fuzzing: its schema, its internal decoder,
// its public Decode helper, and accessors for what the checks compare.
type fzdTarget[R any] struct {
	schema fzdSchema
	decode func([]byte) (*R, Credits, *decodeError)
	public func([]byte) (*R, error)
	meta   func(*R) Metadata
	rawOf  func(*R) ([]byte, bool)
	shapes func(*R) map[string]fzdShape
	// exact, when set, is a complete oracle for the operation, run on every
	// valid JSON object.
	exact func(t *testing.T, members []fzdMember, res *R, derr *decodeError)
}

// fzdSearchTarget returns the Search target.
func fzdSearchTarget() fzdTarget[SearchResponse] {
	return fzdTarget[SearchResponse]{
		schema: fzdSearchSchema(), decode: decodeSearch, public: DecodeSearchResponse,
		meta:  func(r *SearchResponse) Metadata { return r.Meta },
		rawOf: func(r *SearchResponse) ([]byte, bool) { return r.RawJSON() },
		shapes: func(r *SearchResponse) map[string]fzdShape {
			return map[string]fzdShape{
				"searchParameters": fzdObjectShape(r.SearchParameters != nil),
				"knowledgeGraph":   fzdObjectShape(r.KnowledgeGraph != nil),
				"organic":          fzdListShape(r.Organic),
				"peopleAlsoAsk":    fzdListShape(r.PeopleAlsoAsk),
				"relatedSearches":  fzdListShape(r.RelatedSearches),
			}
		},
	}
}

// fzdImagesTarget returns the Images target.
func fzdImagesTarget() fzdTarget[ImagesResponse] {
	return fzdTarget[ImagesResponse]{
		schema: fzdImagesSchema(), decode: decodeImages, public: DecodeImagesResponse,
		meta:  func(r *ImagesResponse) Metadata { return r.Meta },
		rawOf: func(r *ImagesResponse) ([]byte, bool) { return r.RawJSON() },
		shapes: func(r *ImagesResponse) map[string]fzdShape {
			return map[string]fzdShape{
				"searchParameters": fzdObjectShape(r.SearchParameters != nil),
				"images":           fzdListShape(r.Images),
			}
		},
	}
}

// fzdScrapeTarget returns the Scrape target, whose flat shape allows a
// complete oracle.
func fzdScrapeTarget() fzdTarget[ScrapeResponse] {
	return fzdTarget[ScrapeResponse]{
		schema: fzdScrapeSchema(), decode: decodeScrape, public: DecodeScrapeResponse,
		meta:   func(r *ScrapeResponse) Metadata { return r.Meta },
		rawOf:  func(r *ScrapeResponse) ([]byte, bool) { return r.RawJSON() },
		shapes: func(*ScrapeResponse) map[string]fzdShape { return nil },
		exact:  fzdScrapeExact,
	}
}

// fzdScrapeWant decodes a Scrape answer's members by the contract alone:
// every "text" and "markdown" member must be a string or null, the first one
// that is not naming the failure (decoding reports the earliest wrong type);
// the last "text" must be a string; the last "markdown" is kept, null as nil.
func fzdScrapeWant(tb testing.TB, members []fzdMember) (*ScrapeResponse, string) {
	tb.Helper()
	var text, markdown *string
	for _, m := range members {
		for _, name := range []string{"text", "markdown"} {
			if !strings.EqualFold(m.name, name) {
				continue
			}
			v, ok := fzdStringOrNull(tb, m.raw)
			if !ok {
				return nil, name
			}
			if name == "text" {
				text = v
			} else {
				markdown = v
			}
		}
	}
	if text == nil {
		return nil, ""
	}
	return &ScrapeResponse{Text: *text, Markdown: markdown}, ""
}

// fzdScrapeExact compares decodeScrape with fzdScrapeWant.
func fzdScrapeExact(t *testing.T, members []fzdMember, res *ScrapeResponse, derr *decodeError) {
	t.Helper()
	want, wrong := fzdScrapeWant(t, members)
	switch {
	case wrong != "":
		require.NotNil(t, derr, "a %s that is not a string or null fails", wrong)
		require.ErrorIs(t, derr.cause, errWrongType)
		assert.Equal(t, wrong, derr.field)
	case want == nil:
		require.NotNil(t, derr, "a missing or null text fails")
		require.ErrorIs(t, derr.cause, errMissingMember)
		assert.Equal(t, "text", derr.field)
	default:
		require.Nil(t, derr, "a string text with a string or null markdown succeeds")
		assert.Equal(t, want, res)
	}
}

// fzdCheck runs every property of the decoder of tg on data.
func fzdCheck[R any](t *testing.T, tg fzdTarget[R], data []byte) {
	t.Helper()
	res, credits, derr := tg.decode(data)
	require.NotEqual(t, res == nil, derr == nil, "a result or a decodeError, never both or neither")
	wellFormed := utf8.Valid(data) && json.Valid(data)
	object := wellFormed && fzdFirstByte(data) == '{'
	var members []fzdMember
	if object {
		members = fzdMembersOf(t, data)
	}
	assert.Equal(t, fzdCreditsFrom(members, object), credits, "credits of the last top-level credits member")
	fzdCheckCreditsShape(t, credits)
	if object && tg.exact != nil {
		tg.exact(t, members, res, derr)
	}
	if derr == nil {
		require.True(t, object, "success implies valid UTF-8, one valid JSON value and an object")
		fzdCheckSuccess(t, tg, data, res, credits, members)
		return
	}
	fzdCheckFailure(t, tg, data, derr, credits, members)
}

// fzdCheckSuccess checks a decoded result: its typed top-level members keep
// the shape of their last value, it round-trips through encoding/json, and
// the public Decode helper returns the same result with only Op and Credits
// in its Meta and no capture.
func fzdCheckSuccess[R any](t *testing.T, tg fzdTarget[R], data []byte, res *R, credits Credits,
	members []fzdMember,
) {
	t.Helper()
	s := tg.schema
	for _, m := range members {
		for name, kind := range s.top {
			if strings.EqualFold(m.name, name) {
				assert.True(t, string(m.raw) == "null" || m.raw[0] == kind,
					"a success has every %s as %c or null, got %.40s", name, kind, m.raw)
			}
		}
	}
	req, ok := fzdLast(members, s.required)
	require.True(t, ok && req[0] == s.top[s.required], "a success has a %s value of kind %c", s.required, s.top[s.required])
	for name, got := range tg.shapes(res) {
		raw, ok := fzdLast(members, name)
		want, hasNull := fzdShapeOfRaw(t, raw, ok)
		assert.Equal(t, want, got, "%s keeps the shape of its last value", name)
		assert.False(t, hasNull, "a success has no null item in %s", name)
	}

	out, err := json.Marshal(res)
	require.NoError(t, err, "a result re-marshals with encoding/json")
	require.True(t, json.Valid(out))
	back := new(R)
	require.NoError(t, json.Unmarshal(out, back))
	assert.Equal(t, res, back, "a result round-trips through encoding/json")

	pub, err := tg.public(data)
	require.NoError(t, err, "the Decode helper agrees with the decoder")
	require.NotNil(t, pub)
	assert.Equal(t, Metadata{Op: s.op, Credits: credits}, tg.meta(pub))
	pubOut, err := json.Marshal(pub)
	require.NoError(t, err)
	assert.Equal(t, string(out), string(pubOut), "the Decode helper's result marshals to the same JSON")
	raw, captured := tg.rawOf(pub)
	assert.False(t, captured, "a Decode helper result never holds a capture")
	assert.Nil(t, raw)
}

// fzdCheckFailure checks a decodeError against the contract's order of
// checks and its Field grammar, and the public Decode helper's CallError
// against it.
func fzdCheckFailure[R any](t *testing.T, tg fzdTarget[R], data []byte, derr *decodeError, credits Credits,
	members []fzdMember,
) {
	t.Helper()
	wellFormed := utf8.Valid(data) && json.Valid(data)
	assert.Equal(t, wellFormed, derr.validJSON, "validJSON is valid UTF-8 and one valid JSON value")
	assert.Contains(t, []FailureKind{FailureMalformedResponse, FailureUnexpectedResponse}, derr.kind)
	assert.True(t, slices.ContainsFunc(fzdFixedCauses(), func(e error) bool { return errors.Is(derr.cause, e) }),
		"cause %v is a fixed decoding cause", derr.cause)
	require.NoError(t, errors.Unwrap(derr.cause), "a fixed cause wraps nothing")
	switch {
	case !utf8.Valid(data):
		fzdCheckCause(t, derr, errInvalidUTF8, FailureMalformedResponse, "")
	case !json.Valid(data):
		fzdCheckCause(t, derr, errInvalidJSON, FailureMalformedResponse, "")
	case fzdFirstByte(data) != '{':
		fzdCheckCause(t, derr, errNotObject, FailureMalformedResponse, "")
	default:
		fzdCheckObjectFailure(t, tg.schema, derr, members)
	}

	pub, err := tg.public(data)
	assert.Nil(t, pub, "a failed Decode helper returns no result")
	ce := testutil.RequireErrorAs[*CallError](t, err)
	assert.Equal(t, derr.kind, ce.Kind)
	assert.Equal(t, derr.field, ce.Field)
	assert.False(t, ce.OutcomeUnknown, "a Decode helper never leaves an outcome unknown")
	require.NotErrorIs(t, err, ErrOutcomeUnknown)
	require.ErrorIs(t, err, fzdSentinel(derr.kind))
	require.ErrorIs(t, ce.Unwrap(), derr.cause)
	assert.Equal(t, Metadata{Op: tg.schema.op, Credits: credits}, ce.Meta)
	assert.Equal(t, fzdWantError(tg.schema.op, derr.kind, derr.field), err.Error())
	raw, captured := ce.RawJSON()
	assert.False(t, captured, "a Decode helper's failure never holds a capture")
	assert.Nil(t, raw)
}

// fzdCheckCause asserts a failure decided before any member was looked at.
func fzdCheckCause(t *testing.T, derr *decodeError, cause error, kind FailureKind, field string) {
	t.Helper()
	require.ErrorIs(t, derr.cause, cause)
	assert.Equal(t, kind, derr.kind)
	assert.Equal(t, field, derr.field)
}

// fzdCheckObjectFailure checks the failure of a valid JSON object: a Field
// from the documented member names only, and the shape each cause implies.
func fzdCheckObjectFailure(t *testing.T, s fzdSchema, derr *decodeError, members []fzdMember) {
	t.Helper()
	field := derr.field
	require.True(t, s.fieldRE.MatchString(field), "Field %q is built only from known member names", field)
	segs, path, ok := s.walk(field)
	require.True(t, ok, "Field %q is a path of the documented member tree", field)
	everyListIndexed := !slices.ContainsFunc(segs, func(g fzdSegment) bool { return g.list && !g.indexed })
	anyIndexed := slices.ContainsFunc(segs, func(g fzdSegment) bool { return g.indexed })
	last := segs[len(segs)-1]
	switch {
	case errors.Is(derr.cause, errWrongType):
		assert.Equal(t, FailureMalformedResponse, derr.kind)
		if anyIndexed {
			assert.True(t, everyListIndexed && s.attrs[path] && !last.indexed,
				"an indexed wrong-type Field names an attributes object at its item: %q", field)
		}
	case errors.Is(derr.cause, errNullItem):
		assert.Equal(t, FailureMalformedResponse, derr.kind)
		assert.True(t, last.indexed && everyListIndexed, "a null item's Field indexes it and each list above it: %q", field)
	case errors.Is(derr.cause, errMissingMember):
		want := FailureUnexpectedResponse
		if s.op == OpScrape {
			want = FailureMalformedResponse
		}
		assert.Equal(t, want, derr.kind)
		assert.Equal(t, s.required, field)
		raw, ok := fzdLast(members, s.required)
		assert.True(t, !ok || string(raw) == "null", "only an absent or null %s is missing", s.required)
	default:
		assert.Fail(t, "a valid JSON object fails only by wrong type, null item or missing member",
			"cause %v", derr.cause)
	}
}

// fzdAddSeeds adds every seed to f.
func fzdAddSeeds(f *testing.F, seeds ...[]string) {
	f.Helper()
	for _, list := range seeds {
		for _, s := range list {
			f.Add([]byte(s))
		}
	}
}

// fzdFixtures returns the named SDK fixtures, under testdata/synthetic.
func fzdFixtures(f *testing.F, names ...string) []string {
	f.Helper()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, string(testutil.Fixture(f, "synthetic/"+n)))
	}
	return out
}

// fzdCommonSeeds are edge cases every decoder must survive.
func fzdCommonSeeds() []string {
	deep := `{"a":` + strings.Repeat("[", 200) + strings.Repeat("]", 200) + `}`
	return []string{
		"", " ", "null", "true", "0", `"x"`, "[]", "[{}]", "{}", " \t\r\n{} \n", "{} x", "{}{}", "{", "}",
		`{"a":}`, `{"a" 1}`, `{"a":1,}`, "\xff", `{"a":"` + "\xff" + `"}`, "\xef\xbb\xbf{}", " {}",
		`{"a":"\ud800"}`, `{"credits":1}`, `{"credits":null}`, `{"credits":-1}`, `{"credits":1.0}`,
		`{"credits":1e0}`, `{"credits":"1"}`, `{"credits":9223372036854775807}`, `{"credits":9223372036854775808}`,
		`{"CREDITS":2}`, `{"credits":1,"Credits":null}`, `{"credits":3}`, `{"credits":[1]}`, deep,
		`{"unknown":{"x":[1,2,{"y":null}]}}`,
	}
}

// fzdSearchSeeds are Search answers at the edges of the contract.
func fzdSearchSeeds(f *testing.F) []string {
	f.Helper()
	sp := `"searchParameters":{}`
	return append(fzdFixtures(f, "search/full.json", "search/empty.json"),
		string(testutil.SearchBody(3)),
		`{"searchParameters":{"q":"example","type":"search","engine":"google"},"organic":[]}`,
		`{"searchParameters":null}`, `{`+sp+`}`, `{"searchParameters":[]}`, `{"searchParameters":"q"}`,
		`{"searchParameters":{"num":"10"}}`, `{"searchParameters":{"num":1.5}}`,
		`{"searchParameters":{"autocorrect":"yes"}}`, `{`+sp+`,"organic":[]}`, `{`+sp+`,"organic":null}`,
		`{`+sp+`,"organic":{}}`, `{`+sp+`,"organic":[null]}`, `{`+sp+`,"organic":[{},null]}`,
		`{`+sp+`,"organic":[{"sitelinks":[{},null]}]}`, `{`+sp+`,"organic":[{"sitelinks":{}}]}`,
		`{`+sp+`,"organic":[{"sitelinks":[{"title":1}]}]}`, `{`+sp+`,"organic":[{"attributes":7}]}`,
		`{`+sp+`,"organic":[{"attributes":{"a":1}}]}`, `{`+sp+`,"organic":[{"attributes":{"a":"1","a":"2"}}]}`,
		`{`+sp+`,"organic":[{"position":"1"}]}`, `{`+sp+`,"organic":[{"position":9223372036854775808}]}`,
		`{`+sp+`,"knowledgeGraph":{"attributes":[]}}`, `{`+sp+`,"knowledgeGraph":null}`,
		`{`+sp+`,"knowledgeGraph":{"title":1}}`, `{`+sp+`,"peopleAlsoAsk":[null]}`,
		`{`+sp+`,"relatedSearches":[{},null]}`, `{`+sp+`,"relatedSearches":[{"query":["x"]}]}`,
		`{"SEARCHPARAMETERS":{},"Organic":[{"POSITION":3}]}`, `{`+sp+`,"searchParameters":null}`,
		`{"searchParameters":null,`+sp+`}`, `{"organic":[null],`+sp+`}`,
		`{"organic":[null],"organic":[{}],`+sp+`}`, `{"organic":7}`,
		"{\"ſearchParameters\":{},\"KnowledgeGraph\":{}}",
	)
}

// fzdImagesSeeds are Images answers at the edges of the contract.
func fzdImagesSeeds(f *testing.F) []string {
	f.Helper()
	sp := `"searchParameters":{}`
	return append(fzdFixtures(f, "images/full.json", "images/empty.json"),
		string(testutil.ImagesBody(3)),
		`{"searchParameters":{"q":"example","type":"images","engine":"google"},"images":[]}`,
		`{"searchParameters":null}`, `{`+sp+`}`, `{"images":[]}`, `{`+sp+`,"images":null}`,
		`{`+sp+`,"images":[null]}`, `{`+sp+`,"images":[{},null]}`, `{`+sp+`,"images":{}}`,
		`{`+sp+`,"images":[{"imageWidth":"1"}]}`, `{`+sp+`,"images":[{"position":1.5}]}`,
		`{"Images":[{}],"SearchParameters":{"Q":"x","NUM":10}}`, `{"searchParameters":{"page":[]},"images":[null]}`,
	)
}

// fzdScrapeSeeds are Scrape answers at the edges of the contract.
func fzdScrapeSeeds(f *testing.F) []string {
	f.Helper()
	return append(fzdFixtures(f, "scrape/text.json", "scrape/markdown.json"),
		string(testutil.ScrapeBody(64, true)), `{"text":"Example page text."}`,
		`{"text":""}`, `{"text":null}`, `{"text":1}`, `{"text":{}}`, `{"text":[]}`, `{"markdown":"m"}`,
		`{"markdown":1,"text":""}`, `{"text":"","markdown":1}`, `{"text":"a","markdown":null}`,
		`{"text":"a","text":null}`, `{"text":null,"text":"a"}`, `{"TEXT":"a","MarkDown":"b"}`,
		`{"text":"a","Text":5}`, `{"text":"\ud800"}`, `{"text":"<b>&</b> "}`,
		`{"markdown":"x","markdown":null,"text":""}`, `{"metadata":{"title":"t"},"text":"x","credits":2}`,
	)
}

func FuzzDecodeSearch(f *testing.F) {
	fzdAddSeeds(f, fzdCommonSeeds(), fzdSearchSeeds(f))
	tg := fzdSearchTarget()
	f.Fuzz(func(t *testing.T, data []byte) { fzdCheck(t, tg, data) })
}

func FuzzDecodeImages(f *testing.F) {
	fzdAddSeeds(f, fzdCommonSeeds(), fzdImagesSeeds(f))
	tg := fzdImagesTarget()
	f.Fuzz(func(t *testing.T, data []byte) { fzdCheck(t, tg, data) })
}

func FuzzDecodeScrape(f *testing.F) {
	fzdAddSeeds(f, fzdCommonSeeds(), fzdScrapeSeeds(f))
	tg := fzdScrapeTarget()
	f.Fuzz(func(t *testing.T, data []byte) { fzdCheck(t, tg, data) })
}

// fzdCreditsReader reads the credits one decoding path reports for a body.
type fzdCreditsReader struct {
	name string
	read func(tb testing.TB, body []byte) Credits
}

// fzdPublicCredits returns the credits a Decode helper reports, on its
// result or its CallError.
func fzdPublicCredits[R any](decode func([]byte) (*R, error), meta func(*R) Metadata) func(testing.TB, []byte) Credits {
	return func(tb testing.TB, body []byte) Credits {
		tb.Helper()
		res, err := decode(body)
		if err != nil {
			return testutil.RequireErrorAs[*CallError](tb, err).Meta.Credits
		}
		return meta(res).Credits
	}
}

// fzdCreditsReaders returns every path that reads credits from a body.
func fzdCreditsReaders() []fzdCreditsReader {
	return []fzdCreditsReader{
		{"decodeSearch", func(_ testing.TB, b []byte) Credits { _, c, _ := decodeSearch(b); return c }},
		{"decodeImages", func(_ testing.TB, b []byte) Credits { _, c, _ := decodeImages(b); return c }},
		{"decodeScrape", func(_ testing.TB, b []byte) Credits { _, c, _ := decodeScrape(b); return c }},
		{"DecodeSearchResponse", fzdPublicCredits(DecodeSearchResponse, func(r *SearchResponse) Metadata { return r.Meta })},
		{"DecodeImagesResponse", fzdPublicCredits(DecodeImagesResponse, func(r *ImagesResponse) Metadata { return r.Meta })},
		{"DecodeScrapeResponse", fzdPublicCredits(DecodeScrapeResponse, func(r *ScrapeResponse) Metadata { return r.Meta })},
	}
}

// fzdCreditsBodies wraps value as the top-level credits member of a failing
// answer and of a successful answer of each operation.
func fzdCreditsBodies(value []byte) [][]byte {
	v := string(value)
	return [][]byte{
		[]byte(`{"credits":` + v + `}`),
		[]byte(`{"searchParameters":{},"credits":` + v + `}`),
		[]byte(`{"text":"","credits":` + v + `,"organic":[]}`),
		[]byte(`{"credits":` + v + `,"searchParameters":{},"text":"t","organic":7}`),
	}
}

func FuzzCredits(f *testing.F) {
	for _, s := range []string{
		"0", "1", "42", "007", "00", "9223372036854775807", "9223372036854775808", "99999999999999999999",
		"-1", "-0", "1.0", "1e0", "1E3", "0.5", "2.", "+1", "null", "nul", "NULL", `"1"`, `""`, "true", "false",
		"[]", "[1]", "{}", `{"a":1}`, " 5", " 5 ", "5\n", "\t7\r\n", "", "١", "0x1F", "1_000", "\xff",
	} {
		f.Add([]byte(s))
	}
	readers := fzdCreditsReaders()
	f.Fuzz(func(t *testing.T, raw []byte) {
		got := creditsOf(raw)
		assert.Equal(t, fzdWantCredits(raw, raw != nil), got, "creditsOf(%q)", raw)
		fzdCheckCreditsShape(t, got)
		assert.Equal(t, Credits{}, creditsOf(nil), "an absent member is not reported")
		if len(raw) == 0 {
			for _, r := range readers {
				for _, body := range []string{`{}`, `{"searchParameters":{}}`, `{"text":""}`} {
					assert.Equal(t, Credits{}, r.read(t, []byte(body)), "%s on %s: absent", r.name, body)
				}
			}
		}
		if !utf8.Valid(raw) || !json.Valid(raw) {
			return
		}
		want := fzdWantCredits(bytes.Trim(raw, fzdSpace), true)
		for _, body := range fzdCreditsBodies(raw) {
			for _, r := range readers {
				c := r.read(t, body)
				assert.Equal(t, want, c, "%s on %s", r.name, body)
				fzdCheckCreditsShape(t, c)
			}
		}
	})
}
