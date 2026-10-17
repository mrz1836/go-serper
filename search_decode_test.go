package serper_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
	"github.com/mrz1836/go-serper/internal/testutil"
)

// The Search decoding suite. Its helpers (srdLive, srdRun..., the value
// tables) are the shared decoding harness, in decode_harness_test.go.

// srdSearchPaths returns every typed member path of a Search answer, but the
// attributes, whose failures carry an index (see TestSearchDecodeAttributes).
func srdSearchPaths() []srdPath {
	const (
		kg      = `{"searchParameters":{},"knowledgeGraph":{#}}`
		organic = `{"searchParameters":{},"organic":[{"title":"first"},{#}]}`
		link    = `{"searchParameters":{},"organic":[{},{"sitelinks":[{"title":"first"},{#}]}]}`
		paa     = `{"searchParameters":{},"peopleAlsoAsk":[{"question":"first"},{#}]}`
		related = `{"searchParameters":{},"relatedSearches":[{"query":"first"},{#}]}`
	)
	paths := srdParameterPaths()
	paths = append(paths,
		srdPath{field: "knowledgeGraph", kind: srdKindObject, template: `{"searchParameters":{},"knowledgeGraph":@}`},
		srdPath{field: "organic", kind: srdKindList, template: `{"searchParameters":{},"organic":@}`},
		srdPath{field: "organic", kind: srdKindItem, template: `{"searchParameters":{},"organic":[{},@]}`},
		srdPath{field: "organic.position", kind: srdKindInt, template: strings.Replace(organic, "#", `"position":@`, 1)},
		srdPath{field: "organic.sitelinks", kind: srdKindList, template: `{"searchParameters":{},"organic":[{},{"sitelinks":@}]}`},
		srdPath{field: "organic.sitelinks", kind: srdKindItem, template: `{"searchParameters":{},"organic":[{"sitelinks":[{},@]}]}`},
		srdPath{field: "peopleAlsoAsk", kind: srdKindList, template: `{"searchParameters":{},"peopleAlsoAsk":@}`},
		srdPath{field: "peopleAlsoAsk", kind: srdKindItem, template: `{"searchParameters":{},"peopleAlsoAsk":[{},@]}`},
		srdPath{field: "relatedSearches", kind: srdKindList, template: `{"searchParameters":{},"relatedSearches":@}`},
		srdPath{field: "relatedSearches", kind: srdKindItem, template: `{"searchParameters":{},"relatedSearches":[{},@]}`},
	)
	paths = append(paths, srdStrings("knowledgeGraph", kg,
		"title", "type", "website", "imageUrl", "description", "descriptionSource", "descriptionLink")...)
	paths = append(paths, srdStrings("organic", organic, "title", "link", "snippet", "date")...)
	paths = append(paths, srdStrings("organic.sitelinks", link, "title", "link")...)
	paths = append(paths, srdStrings("peopleAlsoAsk", paa, "question", "snippet", "title", "link")...)
	return append(paths, srdStrings("relatedSearches", related, "query")...)
}

// srdSearchNames returns every member name of a Search answer.
func srdSearchNames() []string {
	return append(srdParameterNames(),
		"knowledgeGraph", "title", "website", "imageUrl", "description", "descriptionSource", "descriptionLink",
		"attributes", "organic", "link", "snippet", "date", "position", "sitelinks", "peopleAlsoAsk", "question",
		"relatedSearches", "query",
	)
}

// srdEveryMember is the decoding of search/every-member.json.
func srdEveryMember() *serper.SearchResponse {
	return &serper.SearchResponse{
		SearchParameters: &serper.SearchParameters{
			Query: "every member example", Type: "search", Engine: "google", GL: "gb", HL: "fr",
			Location: "Example Town, Example Region", TBS: "qdr:m", Num: 20, Page: 3, Autocorrect: new(true),
		},
		KnowledgeGraph: &serper.KnowledgeGraph{
			Title: "Example Museum", Type: "Museum", Website: "https://museum.example.org/",
			ImageURL:          "https://images.example.net/museum.webp",
			Description:       "An invented museum, described for the SDK's tests.",
			DescriptionSource: "Example Reference", DescriptionLink: "https://reference.example.org/museum",
			Attributes: serper.Attributes{
				{Name: "Founded", Value: "1888"},
				{Name: "Address", Value: "1 Example Way"},
				{Name: "Founded", Value: "1889"},
				{Name: "", Value: "an empty name"},
				{Name: "Empty value", Value: ""},
			},
		},
		Organic: []serper.OrganicResult{
			{
				Title: "Example Museum - Home", Link: "https://museum.example.org/home",
				Snippet: "Opening hours and exhibitions of the invented museum.", Date: "Jan 2, 2023", Position: 1,
				Sitelinks: []serper.Sitelink{
					{Title: "Exhibitions", Link: "https://museum.example.org/exhibitions"},
					{Title: "Visit", Link: "https://museum.example.org/visit"},
				},
				Attributes: serper.Attributes{{Name: "Rating", Value: "4.8"}, {Name: "Reviews", Value: "1,024"}},
			},
			{
				Title: "Example Museum on Example Maps", Link: "https://maps.example.com/museum",
				Snippet: "Directions to the invented museum.", Date: "2 weeks ago", Position: 2,
				Sitelinks: []serper.Sitelink{}, Attributes: serper.Attributes{},
			},
		},
		PeopleAlsoAsk: []serper.PeopleAlsoAsk{
			{
				Question: "When did the example museum open?", Snippet: "A synthetic answer about an opening.",
				Title: "Example Museum History", Link: "https://museum.example.org/history",
			},
			{
				Question: "Is the example museum free?", Snippet: "A synthetic answer about admission.",
				Title: "Example Museum Tickets", Link: "https://museum.example.org/tickets",
			},
		},
		RelatedSearches: []serper.RelatedSearch{{Query: "example museum hours"}, {Query: "example museum tickets"}},
		Meta:            serper.Metadata{Op: serper.OpSearch, Credits: reported(5)},
	}
}

func TestSearchDecodeEveryMember(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	got := l.decode(t, o, fixture(t, "search/every-member.json"))
	assert.Equal(t, srdEveryMember(), valueOf[serper.SearchResponse](t, got), "every typed member, exactly as returned")
}

func TestSearchDecodeWrongTypes(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	t.Run("wrong", func(t *testing.T) { srdRunWrongTypes(t, l, o, srdSearchPaths()) })
	t.Run("right", func(t *testing.T) { srdRunRightTypes(t, l, o, srdSearchPaths()) })
	t.Run("the earliest wrong type is reported", func(t *testing.T) {
		// encoding/json documents that Unmarshal reports the earliest of the
		// type errors it skips over.
		for body, field := range map[string]string{
			`{"searchParameters":{"num":"1"},"organic":[{"position":"1"}]}`:                                  "searchParameters.num",
			`{"organic":[{"position":"1"}],"searchParameters":{"num":"1"}}`:                                  "organic.position",
			`{"searchParameters":{},"relatedSearches":[{"query":1}],"organic":[{"sitelinks":[{"link":1}]}]}`: "relatedSearches.query",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
}

func TestSearchDecodeNullItems(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	tests := []struct {
		name, body, field string
	}{
		{"organic at 0", `{"searchParameters":{},"organic":[null]}`, "organic[0]"},
		{"organic later", `{"searchParameters":{},"organic":[{"title":"a"},{"title":"b"},null,{"title":"d"}]}`, "organic[2]"},
		{"organic, the first of two", `{"searchParameters":{},"organic":[{},null,null]}`, "organic[1]"},
		{"organic with whitespace", "{\"searchParameters\":{},\"organic\":[ {} ,\r\n\tnull ]}", "organic[1]"},
		{"sitelink at 0 of result 0", `{"searchParameters":{},"organic":[{"sitelinks":[null]}]}`, "organic[0].sitelinks[0]"},
		{"sitelink at 0 of result 1", `{"searchParameters":{},"organic":[{},{"sitelinks":[null,{}]}]}`, "organic[1].sitelinks[0]"},
		{
			"sitelink later of a later result", `{"searchParameters":{},"organic":[{},{},{"sitelinks":[{},{},{},null]}]}`,
			"organic[2].sitelinks[3]",
		},
		{"null result before a null sitelink", `{"searchParameters":{},"organic":[null,{"sitelinks":[null]}]}`, "organic[0]"},
		{"null sitelink before a null result", `{"searchParameters":{},"organic":[{"sitelinks":[null]},null]}`, "organic[0].sitelinks[0]"},
		{"peopleAlsoAsk at 0", `{"searchParameters":{},"peopleAlsoAsk":[null]}`, "peopleAlsoAsk[0]"},
		{"peopleAlsoAsk later", `{"searchParameters":{},"peopleAlsoAsk":[{"question":"a"},{"question":"b"},null]}`, "peopleAlsoAsk[2]"},
		{"relatedSearches at 0", `{"searchParameters":{},"relatedSearches":[null]}`, "relatedSearches[0]"},
		{
			"relatedSearches later", `{"searchParameters":{},"relatedSearches":[{"query":"a"},{},{"query":"c"},null]}`,
			"relatedSearches[3]",
		},
		{"organic at the end of a big list", srdBigSearchNull(1000), "organic[999]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op})
		})
	}
}

// srdBigSearchNull returns a Search answer with n organic results, the last
// of them null.
func srdBigSearchNull(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = `{"title":"Example result ` + strconv.Itoa(i) + `","position":` + strconv.Itoa(i+1) + `}`
	}
	items[n-1] = `null`
	return `{"searchParameters":{},"organic":[` + strings.Join(items, ",") + `]}`
}

func TestSearchDecodeAbsentNullEmpty(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, r *serper.SearchResponse)
	}{
		{"every list and pointer absent", `{"searchParameters":{}}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, &serper.SearchParameters{}, r.SearchParameters, "an empty searchParameters object is kept")
			assert.Nil(t, r.KnowledgeGraph)
			assert.Nil(t, r.Organic)
			assert.Nil(t, r.PeopleAlsoAsk)
			assert.Nil(t, r.RelatedSearches)
		}},
		{"every list and pointer null", `{"searchParameters":{"autocorrect":null},"knowledgeGraph":null,"organic":null,` +
			`"peopleAlsoAsk":null,"relatedSearches":null}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Nil(t, r.SearchParameters.Autocorrect)
			assert.Nil(t, r.KnowledgeGraph)
			assert.Nil(t, r.Organic)
			assert.Nil(t, r.PeopleAlsoAsk)
			assert.Nil(t, r.RelatedSearches)
		}},
		{"every list empty and the panel an empty object", `{"searchParameters":{},"knowledgeGraph":{},"organic":[],` +
			`"peopleAlsoAsk":[],"relatedSearches":[]}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, &serper.KnowledgeGraph{}, r.KnowledgeGraph, "{} is a non-nil, empty panel")
			assert.Equal(t, []serper.OrganicResult{}, r.Organic, "[] is a non-nil, empty list")
			assert.Equal(t, []serper.PeopleAlsoAsk{}, r.PeopleAlsoAsk)
			assert.Equal(t, []serper.RelatedSearch{}, r.RelatedSearches)
		}},
		{"autocorrect false and true", `{"searchParameters":{"autocorrect":false},"organic":[]}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, new(false), r.SearchParameters.Autocorrect, "false is kept apart from absent")
		}},
		{"null scalars are left zero", `{"searchParameters":{"q":null,"num":null,"page":null},"organic":[{"title":null,` +
			`"position":null,"sitelinks":[{"link":null}]}]}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, &serper.SearchParameters{}, r.SearchParameters)
			assert.Equal(t, []serper.OrganicResult{{Sitelinks: []serper.Sitelink{{}}}}, r.Organic)
		}},
		{"sitelinks and attributes absent, null and empty", `{"searchParameters":{},"organic":[{},` +
			`{"sitelinks":null,"attributes":null},{"sitelinks":[],"attributes":{}}]}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			require.Len(t, r.Organic, 3)
			for i, item := range r.Organic[:2] {
				assert.Nil(t, item.Sitelinks, "result %d", i)
				assert.Nil(t, item.Attributes, "result %d", i)
			}
			assert.Equal(t, []serper.Sitelink{}, r.Organic[2].Sitelinks)
			assert.Equal(t, serper.Attributes{}, r.Organic[2].Attributes)
		}},
		{"panel attributes absent", `{"searchParameters":{},"knowledgeGraph":{"title":"t"}}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Nil(t, r.KnowledgeGraph.Attributes)
		}},
		{"panel attributes null", `{"searchParameters":{},"knowledgeGraph":{"attributes":null}}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Nil(t, r.KnowledgeGraph.Attributes)
		}},
		{"panel attributes empty", `{"searchParameters":{},"knowledgeGraph":{"attributes":{}}}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, serper.Attributes{}, r.KnowledgeGraph.Attributes)
		}},
		{"empty items", `{"searchParameters":{},"organic":[{}],"peopleAlsoAsk":[{}],"relatedSearches":[{}]}`, func(t *testing.T, r *serper.SearchResponse) {
			t.Helper()
			assert.Equal(t, []serper.OrganicResult{{}}, r.Organic, "an empty item is kept as a zero item")
			assert.Equal(t, []serper.PeopleAlsoAsk{{}}, r.PeopleAlsoAsk)
			assert.Equal(t, []serper.RelatedSearch{{}}, r.RelatedSearches)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			tt.check(t, valueOf[serper.SearchResponse](t, got))
		})
	}
	for i, body := range []string{`{}`, `{"searchParameters":null}`, `{"organic":[],"knowledgeGraph":{}}`} {
		t.Run("searchParameters missing "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", Op: o.op})
		})
	}
}

// srdAttributeSpots returns the places attributes are typed in a Search
// answer: a body with @ for the attributes value, the Field of a failure, and
// how to read the decoded attributes.
func srdAttributeSpots() []struct {
	field    string
	template string
	get      func(*serper.SearchResponse) serper.Attributes
} {
	return []struct {
		field    string
		template string
		get      func(*serper.SearchResponse) serper.Attributes
	}{
		{
			"organic[0].attributes", `{"searchParameters":{},"organic":[{"title":"t","attributes":@}]}`,
			func(r *serper.SearchResponse) serper.Attributes { return r.Organic[0].Attributes },
		},
		{
			"organic[2].attributes", `{"searchParameters":{},"organic":[{},{"attributes":{"k":"v"}},{"attributes":@,"title":"t"}]}`,
			func(r *serper.SearchResponse) serper.Attributes { return r.Organic[2].Attributes },
		},
		{
			"knowledgeGraph.attributes", `{"searchParameters":{},"knowledgeGraph":{"title":"t","attributes":@}}`,
			func(r *serper.SearchResponse) serper.Attributes { return r.KnowledgeGraph.Attributes },
		},
	}
}

func TestSearchDecodeAttributes(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	many := make(serper.Attributes, 500)
	members := make([]string, len(many))
	for i := range many {
		many[i] = serper.Attribute{Name: "Name " + strconv.Itoa(len(many)-i), Value: strconv.Itoa(i)}
		members[i] = testutil.JSONQuote(many[i].Name) + ":" + testutil.JSONQuote(many[i].Value)
	}
	kept := []struct {
		name  string
		value string
		want  serper.Attributes
	}{
		{"empty object", `{}`, serper.Attributes{}},
		{"null", `null`, nil},
		{"order kept, never sorted", `{"b":"2","a":"1","c":"3"}`, serper.Attributes{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}, {Name: "c", Value: "3"}}},
		{"duplicates kept", `{"k":"1","k":"2","k":"1"}`, serper.Attributes{{Name: "k", Value: "1"}, {Name: "k", Value: "2"}, {Name: "k", Value: "1"}}},
		{"empty name and value", `{"":""}`, serper.Attributes{{Name: "", Value: ""}}},
		{"escaped name and value", `{"\u0048eight":"\u0034\u0035 m"}`, serper.Attributes{{Name: "Height", Value: "45 m"}}},
		{"names differing in case kept apart", `{"Height":"1","height":"2","HEIGHT":"3"}`, serper.Attributes{
			{Name: "Height", Value: "1"}, {Name: "height", Value: "2"}, {Name: "HEIGHT", Value: "3"},
		}},
		{"names that are member names", `{"title":"t","attributes":"a"}`, serper.Attributes{{Name: "title", Value: "t"}, {Name: "attributes", Value: "a"}}},
		{"whitespace", "{ \"a\" :\t\"1\" ,\r\n\"b\":\"2\" }", serper.Attributes{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}}},
		{"quotes and backslashes", `{"a\"b":"c\\d","e\/f":"\ud83d\ude00\u2028"}`, serper.Attributes{
			{Name: `a"b`, Value: `c\d`}, {Name: "e/f", Value: "\U0001F600\u2028"},
		}},
		{"many, in order", `{` + strings.Join(members, ",") + `}`, many},
	}
	for _, spot := range srdAttributeSpots() {
		for _, tt := range kept {
			t.Run(spot.field+"/"+tt.name, func(t *testing.T) {
				body := []byte(srdAt(spot.template, tt.value))
				got := l.decode(t, o, body)
				srdAssertPlain(t, o, body, got)
				assert.Equal(t, tt.want, spot.get(valueOf[serper.SearchResponse](t, got)))
			})
		}
		for _, value := range []string{
			`[]`, `[{"a":"b"}]`, `["a","b"]`, `"a"`, `""`, `1`, `-1.5`, `true`, `false`, `{"a":1}`, `{"a":null}`,
			`{"a":true}`, `{"a":[]}`, `{"a":["b"]}`, `{"a":{}}`, `{"a":{"b":"c"}}`, `{"a":"ok","b":2}`,
			`{"a":"ok","b":{"c":"d"}}`,
		} {
			t.Run(spot.field+"/refused "+value, func(t *testing.T) {
				got := l.decode(t, o, []byte(srdAt(spot.template, value)))
				_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: spot.field, Op: o.op})
			})
		}
	}
	t.Run("the first malformed attributes in document order", func(t *testing.T) {
		body := `{"searchParameters":{},"organic":[{"attributes":{"a":"b"}},{"attributes":[]},{"attributes":1}]}`
		got := l.decode(t, o, []byte(body))
		_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: "organic[1].attributes", Op: o.op})
	})
	t.Run("Get", func(t *testing.T) { srdCheckAttributesGet(t, l, o) })
}

// srdCheckAttributesGet checks Get on decoded attributes: the first of
// duplicates, names matched exactly (case and Unicode form included).
func srdCheckAttributesGet(t *testing.T, l *srdLive, o opCase) {
	t.Helper()
	body := `{"searchParameters":{},"knowledgeGraph":{"attributes":{"Height":"45 m","height":"low",` +
		`"Height":"46 m","":"blank","caf\u00e9":"composed","Empty":""}},"organic":[{"attributes":{}},{}]}`
	res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(body)))
	attrs := res.KnowledgeGraph.Attributes
	for _, tt := range []struct {
		name, want string
		ok         bool
	}{
		{"Height", "45 m", true},
		{"height", "low", true},
		{"HEIGHT", "", false},
		{"", "blank", true},
		{"caf\u00e9", "composed", true},
		{"cafe\u0301", "", false},
		{"Empty", "", true},
		{"Height ", "", false},
		{"Weight", "", false},
	} {
		value, ok := attrs.Get(tt.name)
		assert.Equal(t, tt.ok, ok, "Get(%q)", tt.name)
		assert.Equal(t, tt.want, value, "Get(%q)", tt.name)
	}
	_, ok := res.Organic[0].Attributes.Get("")
	assert.False(t, ok, "empty attributes have no name, not even the empty one")
	_, ok = res.Organic[1].Attributes.Get("")
	assert.False(t, ok, "absent attributes have none")
}

func TestSearchDecodeUnknownMembers(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	srdRunNoise(t, l, o, `{<top>"searchParameters":{<params>"q":"example"},"knowledgeGraph":{<panel>"title":"Example"},`+
		`"organic":[{<organic>"title":"Example","sitelinks":[{<sitelink>"title":"Tours"}],"attributes":{"a":"b"}}],`+
		`"peopleAlsoAsk":[{<question>"question":"Why?"}],"relatedSearches":[{<related>"query":"example"}]<end>}`)
	t.Run("near misses of member names are unknown", func(t *testing.T) {
		body := `{"searchParameters":{"num ":"x","q_":1},"organic ":[null],"organics":1,"_organic":{},` +
			`"organic":[{"position ":"x","sitelink":[null],"attribute":1,"Attributes ":[]}],"knowledge_graph":1,` +
			`"peopleAlsoAsked":[null],"relatedSearch":[null],"credit":"x"}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		res := valueOf[serper.SearchResponse](t, got)
		assert.Equal(t, []serper.OrganicResult{{}}, res.Organic)
		assert.Equal(t, serper.Credits{}, res.Meta.Credits)
	})
	t.Run("members of other operations are unknown", func(t *testing.T) {
		body := `{"searchParameters":{},"images":[null],"text":1,"markdown":[],"metadata":"x"}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
	})
}

func TestSearchDecodeMemberMatching(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	t.Run("respelled names", func(t *testing.T) { srdRunRenamed(t, l, o, "search", srdSearchNames()) })
	t.Run("escaped names", func(t *testing.T) {
		body := `{"\u0073earchParameters":{"\u0071":"x"},"\u006frganic":[{"t\u0069tle":"t"}],"cr\u0065dits":2}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		res := valueOf[serper.SearchResponse](t, got)
		assert.Equal(t, "x", res.SearchParameters.Query)
		assert.Equal(t, []serper.OrganicResult{{Title: "t"}}, res.Organic)
		assert.Equal(t, reported(2), res.Meta.Credits)
	})
	t.Run("failure fields use the SDK's names, not the body's spelling", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"SEARCHPARAMETERS":{"NUM":"x"}}`:                                "searchParameters.num",
			`{"searchParameters":{},"Organic":[{"SiteLinks":[{"TITLE":1}]}]}`: "organic.sitelinks.title",
			`{"searchParameters":{},"ORGANIC":[{},{"ATTRIBUTES":1}]}`:         "organic[1].attributes",
			`{"searchParameters":{},"knowledgegraph":{"Attributes":[]}}`:      "knowledgeGraph.attributes",
			`{"searchParameters":{},"ORGANIC":[{"SITELINKS":[null]}]}`:        "organic[0].sitelinks[0]",
			`{"searchParameters":{},"RELATEDSEARCHES":[{},null]}`:             "relatedSearches[1]",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
	t.Run("duplicate members", func(t *testing.T) { srdCheckDuplicates(t, l, o) })
}

// srdCheckDuplicates checks duplicate members, decoded as encoding/json
// decodes them: the last of a scalar or a list wins, a null has no effect on
// a scalar, and an object or list item that comes again is decoded into the
// one already there, so members the later one leaves out are kept.
func srdCheckDuplicates(t *testing.T, l *srdLive, o opCase) {
	t.Helper()
	okTests := []struct {
		name string
		body string
		want *serper.SearchResponse
	}{
		{"string, last wins", `{"searchParameters":{"q":"a","q":"b"}}`, &serper.SearchResponse{SearchParameters: &serper.SearchParameters{Query: "b"}}},
		{"int, last wins", `{"searchParameters":{"num":1,"num":2}}`, &serper.SearchResponse{SearchParameters: &serper.SearchParameters{Num: 2}}},
		{"bool, last wins", `{"searchParameters":{"autocorrect":true,"autocorrect":false}}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{Autocorrect: new(false)},
		}},
		{"null after a string has no effect", `{"searchParameters":{"q":"a","q":null}}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{Query: "a"},
		}},
		{"null after a bool clears the pointer", `{"searchParameters":{"autocorrect":true,"autocorrect":null}}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{},
		}},
		{"list, last wins", `{"searchParameters":{},"relatedSearches":[{"query":"a"},{"query":"b"}],"relatedSearches":[{"query":"c"}]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, RelatedSearches: []serper.RelatedSearch{{Query: "c"}},
		}},
		{"list then null", `{"searchParameters":{},"organic":[{}],"organic":null}`, &serper.SearchResponse{SearchParameters: &serper.SearchParameters{}}},
		{"null then list", `{"searchParameters":{},"organic":null,"organic":[]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, Organic: []serper.OrganicResult{},
		}},
		{"list then empty list", `{"searchParameters":{},"organic":[{"title":"a"}],"organic":[]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, Organic: []serper.OrganicResult{},
		}},
		{"null item then item", `{"searchParameters":{},"organic":[null],"organic":[{"title":"a"}]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, Organic: []serper.OrganicResult{{Title: "a"}},
		}},
		{"list items decoded into the earlier ones", `{"searchParameters":{},"organic":[{"title":"a","link":"https://www.example.com/a",` +
			`"position":1},{"title":"x"}],"organic":[{"title":"b"}]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{},
			Organic:          []serper.OrganicResult{{Title: "b", Link: "https://www.example.com/a", Position: 1}},
		}},
		{"object decoded into the earlier one", `{"searchParameters":{"q":"a","gl":"us"},"searchParameters":{"q":"b"}}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{Query: "b", GL: "us"},
		}},
		{"attributes, last wins", `{"searchParameters":{},"knowledgeGraph":{"attributes":{"a":"1"},"attributes":{"b":"2"}}}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, KnowledgeGraph: &serper.KnowledgeGraph{Attributes: serper.Attributes{{Name: "b", Value: "2"}}},
		}},
		{"exact name then another spelling: the last wins", `{"searchParameters":{},"organic":[{"title":"a"}],"Organic":[{"title":"b"}]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, Organic: []serper.OrganicResult{{Title: "b"}},
		}},
		{"another spelling then the exact name: the last wins", `{"searchParameters":{},"ORGANIC":[{"title":"b"}],"organic":[{"title":"a"}]}`, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{}, Organic: []serper.OrganicResult{{Title: "a"}},
		}},
	}
	for _, tt := range okTests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			tt.want.Meta = serper.Metadata{Op: serper.OpSearch}
			assert.Equal(t, tt.want, valueOf[serper.SearchResponse](t, got))
		})
	}
	t.Run("good attributes then good ones: the last wins", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(`{"searchParameters":{},"organic":[{"attributes":{"x":"y"},"attributes":{"a":"b"}}]}`)))
		assert.Equal(t, serper.Attributes{{Name: "a", Value: "b"}}, res.Organic[0].Attributes)
	})
	t.Run("credits, last wins", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(`{"credits":"x","searchParameters":{},"credits":3}`)))
		assert.Equal(t, reported(3), res.Meta.Credits)
	})
	for _, tt := range []struct {
		name, body string
		kind       serper.FailureKind
		field      string
	}{
		// encoding/json keeps the earliest type error, even when a later
		// duplicate has the right type.
		{"wrong type then right type", `{"searchParameters":{"q":1,"q":"b"}}`, serper.FailureMalformedResponse, "searchParameters.q"},
		{"right type then wrong type", `{"searchParameters":{"q":"b","q":1}}`, serper.FailureMalformedResponse, "searchParameters.q"},
		{
			"good attributes then malformed ones", `{"searchParameters":{},"organic":[{"attributes":{"a":"b"},"attributes":[]}]}`,
			serper.FailureMalformedResponse, "organic[0].attributes",
		},
		{
			// A bad copy stays bad, as encoding/json keeps the first type
			// error of any other repeated member.
			"malformed attributes then good ones", `{"searchParameters":{},"organic":[{"attributes":[],"attributes":{"a":"b"}}]}`,
			serper.FailureMalformedResponse, "organic[0].attributes",
		},
		{"item then null item", `{"searchParameters":{},"organic":[{}],"organic":[null]}`, serper.FailureMalformedResponse, "organic[0]"},
		{"searchParameters then null", `{"searchParameters":{},"searchParameters":null}`, serper.FailureUnexpectedResponse, "searchParameters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{Kind: tt.kind, Field: tt.field, Op: o.op})
		})
	}
}

// srdEveryString is a Search answer with @ as every string member, the name
// and value of an attribute included.
const srdEveryString = `{"searchParameters":{"q":@,"type":@,"engine":@,"gl":@,"hl":@,"location":@,"tbs":@},` +
	`"knowledgeGraph":{"title":@,"type":@,"website":@,"imageUrl":@,"description":@,"descriptionSource":@,` +
	`"descriptionLink":@,"attributes":{@:@}},"organic":[{"title":@,"link":@,"snippet":@,"date":@,` +
	`"sitelinks":[{"title":@,"link":@}],"attributes":{@:@}}],"peopleAlsoAsk":[{"question":@,"snippet":@,` +
	`"title":@,"link":@}],"relatedSearches":[{"query":@}]}`

// srdEveryStringWant is the decoding of srdEveryString with s as every string.
func srdEveryStringWant(s string) *serper.SearchResponse {
	return &serper.SearchResponse{
		SearchParameters: &serper.SearchParameters{Query: s, Type: s, Engine: s, GL: s, HL: s, Location: s, TBS: s},
		KnowledgeGraph: &serper.KnowledgeGraph{
			Title: s, Type: s, Website: s, ImageURL: s, Description: s, DescriptionSource: s, DescriptionLink: s,
			Attributes: serper.Attributes{{Name: s, Value: s}},
		},
		Organic: []serper.OrganicResult{{
			Title: s, Link: s, Snippet: s, Date: s, Sitelinks: []serper.Sitelink{{Title: s, Link: s}},
			Attributes: serper.Attributes{{Name: s, Value: s}},
		}},
		PeopleAlsoAsk:   []serper.PeopleAlsoAsk{{Question: s, Snippet: s, Title: s, Link: s}},
		RelatedSearches: []serper.RelatedSearch{{Query: s}},
		Meta:            serper.Metadata{Op: serper.OpSearch},
	}
}

func TestSearchDecodeStrings(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	for _, tt := range srdTexts() {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(srdAt(srdEveryString, tt.literal)))
			res := valueOf[serper.SearchResponse](t, got)
			assert.Equal(t, srdEveryStringWant(tt.want), res, "every string member decoded exactly")
			srdAssertRoundTrip(t, o, res)
		})
	}
	for _, tt := range srdBadStrings() {
		t.Run("refused "+tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(srdAt(srdEveryString, tt.body)))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
		})
	}
	t.Run("very long strings", func(t *testing.T) {
		long, literal := srdLong(512 << 10)
		value, valueLiteral := srdLong(64 << 10)
		plain := strings.Repeat("Example text. ", (1<<20)/14+1)
		body := `{"searchParameters":{"q":"long"},"knowledgeGraph":{"description":` + literal + `},"organic":[{"snippet":"` +
			plain + `","attributes":{"Long":` + valueLiteral + `}}],"relatedSearches":[{"query":` + literal + `}]}`
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(body)))
		assert.Equal(t, long, res.KnowledgeGraph.Description, "kept whole")
		assert.Equal(t, plain, res.Organic[0].Snippet)
		assert.Equal(t, serper.Attributes{{Name: "Long", Value: value}}, res.Organic[0].Attributes)
		assert.Equal(t, long, res.RelatedSearches[0].Query)
		srdAssertRoundTrip(t, o, res)
	})
}

func TestSearchDecodePositionsAndDates(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	positions := []struct {
		literal string
		want    int
	}{
		{"3", 3},
		{"1", 1},
		{"1", 1},
		{"0", 0},
		{"-2", -2},
		{strconv.Itoa(math.MaxInt), math.MaxInt},
		{strconv.Itoa(math.MinInt), math.MinInt},
		{"-0", 0},
		{"2", 2},
	}
	dates := []string{
		"Aug 31, 2022", "2022-08-31", "31/08/2022", "2022-08-31T10:00:00Z", "3 days ago", "", "not a date",
		"1661904000", "yesterday",
	}
	items := make([]string, len(positions))
	want := make([]serper.OrganicResult, 0, len(positions)+1)
	for i, p := range positions {
		items[i] = `{"title":"Result ` + strconv.Itoa(i) + `","position":` + p.literal + `,"date":` + testutil.JSONQuote(dates[i]) + `}`
		want = append(want, serper.OrganicResult{Title: "Result " + strconv.Itoa(i), Position: p.want, Date: dates[i]})
	}
	body := `{"searchParameters":{},"organic":[` + strings.Join(items, ",") + `,{"title":"No position"}]}`
	want = append(want, serper.OrganicResult{Title: "No position"})
	res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(body)))
	assert.Equal(t, want, res.Organic, "positions out of order, duplicated, zero and negative, and dates, kept as returned")
	srdAssertRoundTrip(t, o, res)

	res = valueOf[serper.SearchResponse](t, l.decode(t, o, fixture(t, "search/positions.json")))
	got := make([]int, 0, len(res.Organic))
	for _, r := range res.Organic {
		got = append(got, r.Position)
	}
	assert.Equal(t, []int{3, 1, 1, 0, -2, 2147483647, 1, 0}, got)
	assert.Equal(t, res.Organic[2], res.Organic[6], "duplicate results are both kept")
	assert.Equal(t, []serper.RelatedSearch{{Query: "example"}, {Query: "example"}}, res.RelatedSearches)
}

// srdBigSearch returns a Search answer with n of every list item, positions
// counting down, and result 0 with n sitelinks and n attributes, and its
// decoding.
func srdBigSearch(n int) (string, *serper.SearchResponse) {
	want := &serper.SearchResponse{
		SearchParameters: &serper.SearchParameters{Query: "big"},
		Organic:          make([]serper.OrganicResult, n),
		PeopleAlsoAsk:    make([]serper.PeopleAlsoAsk, n),
		RelatedSearches:  make([]serper.RelatedSearch, n),
		Meta:             serper.Metadata{Op: serper.OpSearch, Credits: reported(1)},
	}
	organic, questions, related := make([]string, n), make([]string, n), make([]string, n)
	links, attrs := make([]string, n), make([]string, n)
	want.Organic[0].Sitelinks = make([]serper.Sitelink, n)
	want.Organic[0].Attributes = make(serper.Attributes, n)
	for i := range n {
		id := strconv.Itoa(i)
		link := "https://www.example.com/r/" + id
		links[i] = `{"title":"Link ` + id + `","link":"` + link + `"}`
		want.Organic[0].Sitelinks[i] = serper.Sitelink{Title: "Link " + id, Link: link}
		attrs[i] = `"Attribute ` + strconv.Itoa(n-i) + `":"` + id + `"`
		want.Organic[0].Attributes[i] = serper.Attribute{Name: "Attribute " + strconv.Itoa(n-i), Value: id}
		organic[i] = `{"title":"Result ` + id + `","link":"` + link + `","position":` + strconv.Itoa(n-i) + `}`
		want.Organic[i].Title, want.Organic[i].Link, want.Organic[i].Position = "Result "+id, link, n-i
		questions[i] = `{"question":"Question ` + id + `?"}`
		want.PeopleAlsoAsk[i] = serper.PeopleAlsoAsk{Question: "Question " + id + "?"}
		related[i] = `{"query":"related ` + strconv.Itoa(i%10) + `"}`
		want.RelatedSearches[i] = serper.RelatedSearch{Query: "related " + strconv.Itoa(i%10)}
	}
	organic[0] = `{"title":"Result 0","link":"https://www.example.com/r/0","position":` + strconv.Itoa(n) +
		`,"sitelinks":[` + strings.Join(links, ",") + `],"attributes":{` + strings.Join(attrs, ",") + `}}`
	body := `{"searchParameters":{"q":"big"},"organic":[` + strings.Join(organic, ",") + `],"peopleAlsoAsk":[` +
		strings.Join(questions, ",") + `],"relatedSearches":[` + strings.Join(related, ",") + `],"credits":1}`
	return body, want
}

func TestSearchDecodeBigLists(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	body, want := srdBigSearch(1000)
	res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(body)))
	assert.Equal(t, want, res, "big lists keep their order, duplicates and positions")
	srdAssertRoundTrip(t, o, res)

	generated := testutil.SearchBody(1000)
	res = valueOf[serper.SearchResponse](t, l.decode(t, o, generated))
	require.Len(t, res.Organic, 1000)
	for i, r := range res.Organic {
		if !assert.Equal(t, i+1, r.Position) || !assert.Equal(t, "Example result "+strconv.Itoa(i+1), r.Title) {
			break
		}
	}
}

func TestSearchDecodeShapeBeforeSuccessRule(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	for i, tt := range []struct {
		body, field string
	}{
		{`{"organic":[null]}`, "organic[0]"},
		{`{"searchParameters":{"num":"x"}}`, "searchParameters.num"},
		{`{"searchParameters":[]}`, "searchParameters"},
		{`{"organic":{}}`, "organic"},
		{`{"searchParameters":null,"organic":[{"position":"1"}]}`, "organic.position"},
		{`{"knowledgeGraph":"x"}`, "knowledgeGraph"},
		{`{"knowledgeGraph":{"attributes":[]}}`, "knowledgeGraph.attributes"},
		{`{"organic":[{"attributes":"x"}]}`, "organic[0].attributes"},
		{`{"organic":[{"sitelinks":[null]}]}`, "organic[0].sitelinks[0]"},
		{`{"peopleAlsoAsk":[null]}`, "peopleAlsoAsk[0]"},
		{`{"searchParameters":null,"relatedSearches":[{},null]}`, "relatedSearches[1]"},
		{`{"credits":4,"relatedSearches":[{"query":1}]}`, "relatedSearches.query"},
	} {
		t.Run("malformed "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", tt.body)
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op, Credits: srdCreditsIn(tt.body),
			})
		})
	}
	for i, body := range []string{`{"organic":[{}]}`, `{"searchParameters":null,"organic":[{"title":"t"}]}`, `{"knowledgeGraph":{},"credits":4}`} {
		t.Run("unexpected "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", Op: o.op, Credits: srdCreditsIn(body),
			})
		})
	}
}

func TestSearchDecodeCredits(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	malformed, unexpected := serper.FailureMalformedResponse, serper.FailureUnexpectedResponse
	srdRunCredits(t, l, o, []srdCreditsCase{
		{"success", `{"searchParameters":{},"organic":[]}`, "", ""},
		{"success with an empty object", `{"searchParameters":{}}`, "", ""},
		{"wrong type", `{"searchParameters":{"num":"x"},"organic":[]}`, malformed, "searchParameters.num"},
		{"wrong type deep", `{"searchParameters":{},"organic":[{"sitelinks":[{"title":1}]}]}`, malformed, "organic.sitelinks.title"},
		{"null item", `{"searchParameters":{},"organic":[{},null]}`, malformed, "organic[1]"},
		{"null sitelink", `{"searchParameters":{},"organic":[{"sitelinks":[null]}]}`, malformed, "organic[0].sitelinks[0]"},
		{"malformed attributes", `{"searchParameters":{},"knowledgeGraph":{"attributes":[]}}`, malformed, "knowledgeGraph.attributes"},
		{"no searchParameters", `{"organic":[]}`, unexpected, "searchParameters"},
		{"null searchParameters", `{"searchParameters":null}`, unexpected, "searchParameters"},
		{"empty object", `{}`, unexpected, "searchParameters"},
	})
	t.Run("only the top-level member counts", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(`{"searchParameters":{"credits":4},"organic":[{"credits":4}]}`)))
		assert.Equal(t, serper.Credits{}, res.Meta.Credits)
	})
	t.Run("the member name matches as encoding/json matches", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, []byte(`{"searchParameters":{},"CREDITS":4}`)))
		assert.Equal(t, reported(4), res.Meta.Credits)
	})
	for i, body := range []string{`{"credits":4,"searchParameters":{}`, `{"credits":4,"searchParameters":{}} {}`, `[{"credits":4}]`} {
		t.Run("not one valid object "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: malformed, Op: o.op})
		})
	}
}

func TestSearchDecodeAddedFixtures(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Search")
	t.Run("null members", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, fixture(t, "search/null-members.json")))
		assert.Equal(t, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{Query: "an example query"},
			Meta:             serper.Metadata{Op: serper.OpSearch},
		}, res)
	})
	t.Run("empty members", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, fixture(t, "search/empty-members.json")))
		assert.Equal(t, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{},
			KnowledgeGraph:   &serper.KnowledgeGraph{Attributes: serper.Attributes{}},
			Organic:          []serper.OrganicResult{{Sitelinks: []serper.Sitelink{}, Attributes: serper.Attributes{}}, {}},
			PeopleAlsoAsk:    []serper.PeopleAlsoAsk{{}},
			RelatedSearches:  []serper.RelatedSearch{},
			Meta:             serper.Metadata{Op: serper.OpSearch, Credits: reported(0)},
		}, res)
	})
	t.Run("escapes", func(t *testing.T) {
		res := valueOf[serper.SearchResponse](t, l.decode(t, o, fixture(t, "search/escapes.json")))
		assert.Equal(t, &serper.SearchResponse{
			SearchParameters: &serper.SearchParameters{Query: "caf\u00e9 \"quoted\" \\ back/slash", Type: "search", Engine: "google"},
			KnowledgeGraph: &serper.KnowledgeGraph{
				Title:       "Example \U0001F600 Panel",
				Description: "Line one\nLine two\ttabbed \u2028 separators \u2029 end",
				Attributes:  serper.Attributes{{Name: "Height", Value: "45\u00a0m"}, {Name: "Note", Value: "<b>&amp;</b> <i>"}},
			},
			Organic: []serper.OrganicResult{{
				Title: "Raw separators \u2028 and \u2029", Link: "https://www.example.com/caf%C3%A9?q=a&b=c",
				Snippet: "Lone \uFFFD surrogate and NUL \x00 kept", Date: "3 days ago", Position: 1,
			}},
			RelatedSearches: []serper.RelatedSearch{{Query: `C:\example\path`}},
			Meta:            serper.Metadata{Op: serper.OpSearch, Credits: reported(1)},
		}, res)
	})
}
