package serper_test

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper"
)

func TestDecodeSearchFull(t *testing.T) {
	res, err := serper.DecodeSearchResponse(fixture(t, "search/full.json"))
	require.NoError(t, err)
	assert.Equal(t, &serper.SearchParameters{
		Query: "example lighthouse", Type: "search", Engine: "google", GL: "us", HL: "en",
		Location: "Example City", TBS: "qdr:w", Num: 10, Page: 2, Autocorrect: new(false),
	}, res.SearchParameters)
	assert.Equal(t, &serper.KnowledgeGraph{
		Title: "Example Lighthouse", Type: "Lighthouse", Website: "https://www.example.org/",
		ImageURL:    "https://images.example.net/lighthouse.png",
		Description: "A synthetic description of an invented lighthouse.", DescriptionSource: "Example Encyclopedia",
		DescriptionLink: "https://www.example.org/wiki/lighthouse",
		Attributes:      serper.Attributes{{"Height", "45 m"}, {"Opened", "1901"}, {"Height", "46 m"}},
	}, res.KnowledgeGraph, "attributes keep order and duplicates")
	require.Len(t, res.Organic, 3)
	assert.Equal(t, serper.OrganicResult{
		Title: "Example Lighthouse - Official Site", Link: "https://www.example.com/lighthouse",
		Snippet: "Visit the invented lighthouse on the example coast.", Date: "Aug 31, 2022", Position: 1,
		Sitelinks: []serper.Sitelink{
			{Title: "Tours", Link: "https://www.example.com/lighthouse/tours"},
			{Title: "History", Link: "https://www.example.com/lighthouse/history"},
		},
		Attributes: serper.Attributes{{"Hours", "9 to 5"}, {"Admission", "Free"}},
	}, res.Organic[0])
	assert.Equal(t, "3 days ago", res.Organic[1].Date, "dates are provider text, never parsed")
	assert.Nil(t, res.Organic[1].Sitelinks)
	assert.Nil(t, res.Organic[1].Attributes)
	assert.Equal(t, 7, res.Organic[2].Position, "positions are kept as returned")
	assert.Equal(t, []serper.PeopleAlsoAsk{{
		Question: "How tall is the example lighthouse?", Snippet: "A synthetic answer.", Title: "Example FAQ",
		Link: "https://www.example.org/faq",
	}}, res.PeopleAlsoAsk)
	assert.Equal(t, []serper.RelatedSearch{{"example lighthouse tours"}, {"example lighthouse history"}},
		res.RelatedSearches)
	assert.Equal(t, serper.Metadata{Op: serper.OpSearch, Credits: reported(1)}, res.Meta)
	raw, ok := res.RawJSON()
	assertCapture(t, "", raw, ok, "a Decode helper keeps no capture")
}

func TestDecodeSearchEmpty(t *testing.T) {
	res, err := serper.DecodeSearchResponse(fixture(t, "search/empty.json"))
	require.NoError(t, err)
	assert.NotNil(t, res.Organic, "[] stays apart from absent")
	assert.Empty(t, res.Organic)
	assert.Nil(t, res.KnowledgeGraph)
	assert.Nil(t, res.PeopleAlsoAsk)

	res, err = serper.DecodeSearchResponse([]byte(`{"searchParameters":{}}`))
	require.NoError(t, err, "organic is never required")
	assert.Nil(t, res.Organic)
	assert.Equal(t, &serper.SearchParameters{}, res.SearchParameters)
}

func TestDecodeImagesFull(t *testing.T) {
	res, err := serper.DecodeImagesResponse(fixture(t, "images/full.json"))
	require.NoError(t, err)
	assert.Equal(t, &serper.SearchParameters{
		Query: "lighthouse at dusk", Type: "images", Engine: "google", Num: 10, Autocorrect: new(false),
	}, res.SearchParameters)
	require.Len(t, res.Images, 2)
	assert.Equal(t, serper.ImageResult{
		Title: "Lighthouse at dusk", ImageURL: "https://images.example.net/dusk.jpg", ImageWidth: 1920,
		ImageHeight: 1080, ThumbnailURL: "https://thumbs.example.net/dusk.jpg", ThumbnailWidth: 300,
		ThumbnailHeight: 168, Source: "Example Photo Stories", Domain: "photos.example.com",
		Link: "https://photos.example.com/stories/dusk", GoogleURL: "https://www.example.com/imgres?id=dusk",
		Position: 1,
	}, res.Images[0])
	assert.Equal(t, "example.org", res.Images[1].Source, "the source label is kept, never replaced by the link")
	assert.Empty(t, res.Images[1].ThumbnailURL)
	assert.Equal(t, serper.Metadata{Op: serper.OpImages, Credits: reported(1)}, res.Meta)

	res, err = serper.DecodeImagesResponse(fixture(t, "images/empty.json"))
	require.NoError(t, err)
	assert.NotNil(t, res.Images)
	assert.Empty(t, res.Images)
	assert.Equal(t, serper.Credits{}, res.Meta.Credits)
}

func TestDecodeScrape(t *testing.T) {
	res, err := serper.DecodeScrapeResponse(fixture(t, "scrape/text.json"))
	require.NoError(t, err)
	assert.Equal(t, "Example Domain\nThis synthetic page is for use in examples.", res.Text)
	assert.Nil(t, res.Markdown, "never derived from text")
	assert.Equal(t, reported(2), res.Meta.Credits)

	res, err = serper.DecodeScrapeResponse(fixture(t, "scrape/markdown.json"))
	require.NoError(t, err)
	require.NotNil(t, res.Markdown)
	assert.Equal(t, "# Example Domain\n\nThis synthetic page is for use in examples.", *res.Markdown)

	for body, want := range map[string]serper.ScrapeResponse{
		`{"text":""}`:                  {Text: ""},
		`{"text":"","markdown":null}`:  {Text: ""},
		`{"text":"a","markdown":""}`:   {Text: "a", Markdown: new("")},
		`{"text":"é\n","x":[1]}`:       {Text: "é\n"},
		` {"markdown":"m","text":"t"}`: {Text: "t", Markdown: new("m")},
	} {
		res, err := serper.DecodeScrapeResponse([]byte(body))
		require.NoError(t, err, body)
		assert.Equal(t, want.Text, res.Text, body)
		assert.Equal(t, want.Markdown, res.Markdown, body)
	}
}

func TestDecodeRefusals(t *testing.T) {
	for _, o := range opCases() {
		for _, tt := range []struct{ name, body string }{
			{"empty", ``},
			{"spaces", "  \n"},
			{"truncated", `{"searchParameters":{}`},
			{"trailing data", `{"searchParameters":{},"text":""} {}`},
			{"trailing garbage", `{"searchParameters":{},"text":""}x`},
			{"invalid UTF-8", "{\"searchParameters\":{},\"text\":\"\xff\"}"},
			{"bare word", `hello`},
			{"BOM", "\ufeff{\"searchParameters\":{},\"text\":\"\"}"},
			{"array with trailing data", `[] []`},
			{"null", `null`},
			{"empty array", `[]`},
			{"array of an answer", `[{"text":""}]`},
			{"string", `"text"`},
			{"number", `1`},
			{"boolean", `true`},
		} {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				got := o.decode([]byte(tt.body))
				_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
			})
		}
	}
}

func TestDecodeSuccessRules(t *testing.T) {
	for _, o := range []opCase{opCaseNamed("Search"), opCaseNamed("Images")} {
		for i, body := range []string{
			`{}`, `{"credits":1}`, `{"message":"Synthetic: not a success."}`, `{"searchParameters":null}`,
			`{"organic":[],"images":[]}`, `{"SearchParameters ":{}}`,
		} {
			t.Run(o.name+"/"+strconv.Itoa(i), func(t *testing.T) {
				t.Logf("input %q", body)
				got := o.decode([]byte(body))
				_ = assertCallError(t, got.err, wantCall{
					Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", Op: o.op,
					Credits: creditsOfBody(body),
				})
			})
		}
	}
	for i, body := range []string{`{}`, `{"credits":1}`, `{"message":"Synthetic."}`, `{"text":null}`, `{"Markdown":"m"}`} {
		t.Run("Scrape/"+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := opCaseNamed("Scrape").decode([]byte(body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: "text", Op: serper.OpScrape, Credits: creditsOfBody(body),
			})
		})
	}
}

// creditsOfBody returns the credits a test body reports: only {"credits":1}
// reports any.
func creditsOfBody(body string) serper.Credits {
	if body == `{"credits":1}` {
		return reported(1)
	}
	return serper.Credits{}
}

func TestDecodeCredits(t *testing.T) {
	tests := []struct {
		raw  string
		want serper.Credits
	}{
		{raw: "", want: serper.Credits{}},
		{raw: `"credits":null`, want: serper.Credits{}},
		{raw: `"credits":0`, want: reported(0)},
		{raw: `"credits":1`, want: reported(1)},
		{raw: `"credits":10`, want: reported(10)},
		{raw: `"credits":` + strconv.FormatInt(math.MaxInt64, 10), want: reported(math.MaxInt64)},
		{raw: `"credits":9223372036854775808`, want: invalidCredits()},
		{raw: `"credits":1.0`, want: invalidCredits()},
		{raw: `"credits":1e0`, want: invalidCredits()},
		{raw: `"credits":0.5`, want: invalidCredits()},
		{raw: `"credits":-1`, want: invalidCredits()},
		{raw: `"credits":-0`, want: invalidCredits()},
		{raw: `"credits":"2"`, want: invalidCredits()},
		{raw: `"credits":true`, want: invalidCredits()},
		{raw: `"credits":[1]`, want: invalidCredits()},
		{raw: `"credits":{"used":1}`, want: invalidCredits()},
	}
	for _, tt := range tests {
		for _, o := range opCases() {
			body := `{` + tt.raw
			if tt.raw != "" {
				body += ","
			}
			body += o.ok[1:]
			got := o.decode([]byte(body))
			require.NoError(t, got.err, "credits never fail a result: %s", body)
			assert.Equal(t, tt.want, got.meta.Credits, body)
			used, ok := got.meta.Credits.Value()
			assert.Equal(t, tt.want.Status == serper.CreditsReported, ok)
			if ok {
				assert.Equal(t, *tt.want.Used, used)
			}
		}
	}
}

func TestDecodeCreditsOnFailures(t *testing.T) {
	got := opCaseNamed("Search").decode([]byte(`{"credits":3,"searchParameters":{},"organic":[null]}`))
	_ = assertCallError(t, got.err, wantCall{
		Kind: serper.FailureMalformedResponse, Field: "organic[0]", Op: serper.OpSearch, Credits: reported(3),
	})
	got = opCaseNamed("Scrape").decode([]byte(`{"text":7,"credits":2}`))
	_ = assertCallError(t, got.err, wantCall{
		Kind: serper.FailureMalformedResponse, Field: "text", Op: serper.OpScrape, Credits: reported(2),
	}) // credits are read even past a member of the wrong type
	got = opCaseNamed("Scrape").decode([]byte(`[{"credits":2}]`))
	_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: serper.OpScrape}) // only a top-level member counts
}

func TestDecodeWhitespace(t *testing.T) {
	l := srdNewLive(t)
	for _, tt := range []struct{ op, fixture string }{
		{"Search", "search/every-member.json"},
		{"Images", "images/kept-apart.json"},
		{"Scrape", "scrape/escapes.json"},
	} {
		o := opCaseNamed(tt.op)
		t.Run(o.name+"/smallest", func(t *testing.T) { srdRunWhitespace(t, l, o, o.ok) })
		t.Run(o.name+"/"+tt.fixture, func(t *testing.T) { srdRunWhitespace(t, l, o, string(fixture(t, tt.fixture))) })
		t.Run(o.name+"/not an object", func(t *testing.T) { srdRunNotObjects(t, l, o, o.ok) })
	}
}

func TestDecodeFixtures(t *testing.T) {
	l := srdNewLive(t)
	for _, tt := range []struct {
		op, dir string
		want    []string
	}{
		{"Search", "search", []string{
			"empty.json", "full.json", "every-member.json", "null-members.json", "empty-members.json", "escapes.json",
			"positions.json",
		}},
		{"Images", "images", []string{"empty.json", "full.json", "kept-apart.json", "sparse.json", "null-images.json"}},
		{"Scrape", "scrape", []string{
			"text.json", "markdown.json", "empty-text.json", "markdown-null.json", "markdown-empty.json", "escapes.json",
			"pretty.json",
		}},
	} {
		t.Run(tt.op, func(t *testing.T) { srdRunFixtures(t, l, opCaseNamed(tt.op), tt.dir, tt.want...) })
	}
}

func TestResponsesRoundTrip(t *testing.T) {
	search, err := serper.DecodeSearchResponse(fixture(t, "search/full.json"))
	require.NoError(t, err)
	search.Meta = serper.Metadata{}
	assertRoundTrip(t, search, `{"searchParameters":{"q":"example lighthouse","type":"search","engine":"google",`+
		`"gl":"us","hl":"en","location":"Example City","tbs":"qdr:w","num":10,"page":2,"autocorrect":false},`+
		`"knowledgeGraph":{"title":"Example Lighthouse","type":"Lighthouse","website":"https://www.example.org/",`+
		`"imageUrl":"https://images.example.net/lighthouse.png","description":"A synthetic description of an `+
		`invented lighthouse.","descriptionSource":"Example Encyclopedia","descriptionLink":`+
		`"https://www.example.org/wiki/lighthouse","attributes":{"Height":"45 m","Opened":"1901","Height":"46 m"}},`+
		`"organic":[{"title":"Example Lighthouse - Official Site","link":"https://www.example.com/lighthouse",`+
		`"snippet":"Visit the invented lighthouse on the example coast.","date":"Aug 31, 2022","position":1,`+
		`"sitelinks":[{"title":"Tours","link":"https://www.example.com/lighthouse/tours"},{"title":"History",`+
		`"link":"https://www.example.com/lighthouse/history"}],"attributes":{"Hours":"9 to 5","Admission":"Free"}},`+
		`{"title":"Lighthouses of the Example Coast","link":"https://www.example.net/coast","snippet":`+
		`"A synthetic guide.","date":"3 days ago","position":2},{"title":"A result with a position out of order",`+
		`"link":"https://www.example.org/odd","snippet":"Positions are kept as returned.","position":7}],`+
		`"peopleAlsoAsk":[{"question":"How tall is the example lighthouse?","snippet":"A synthetic answer.",`+
		`"title":"Example FAQ","link":"https://www.example.org/faq"}],"relatedSearches":[{"query":`+
		`"example lighthouse tours"},{"query":"example lighthouse history"}]}`)

	empty, err := serper.DecodeSearchResponse(fixture(t, "search/empty.json"))
	require.NoError(t, err)
	empty.Meta = serper.Metadata{}
	assertRoundTrip(t, empty, `{"searchParameters":{"q":"an example query with no results","type":"search",`+
		`"engine":"google"},"organic":[]}`)

	images, err := serper.DecodeImagesResponse(fixture(t, "images/full.json"))
	require.NoError(t, err)
	images.Meta = serper.Metadata{}
	back := assertRoundTrip(t, images, `{"searchParameters":{"q":"lighthouse at dusk","type":"images",`+
		`"engine":"google","num":10,"autocorrect":false},"images":[{"title":"Lighthouse at dusk","imageUrl":`+
		`"https://images.example.net/dusk.jpg","imageWidth":1920,"imageHeight":1080,"thumbnailUrl":`+
		`"https://thumbs.example.net/dusk.jpg","thumbnailWidth":300,"thumbnailHeight":168,"source":`+
		`"Example Photo Stories","domain":"photos.example.com","link":"https://photos.example.com/stories/dusk",`+
		`"googleUrl":"https://www.example.com/imgres?id=dusk","position":1},{"title":"A second invented image",`+
		`"imageUrl":"https://images.example.net/second.png","imageWidth":640,"imageHeight":480,"source":`+
		`"example.org","domain":"www.example.org","link":"https://www.example.org/gallery","position":2}]}`)
	assert.Len(t, back.Images, 2)

	page, err := serper.DecodeScrapeResponse(fixture(t, "scrape/markdown.json"))
	require.NoError(t, err)
	page.Meta = serper.Metadata{}
	assertRoundTrip(t, page, `{"text":"Example Domain\nThis synthetic page is for use in examples.",`+
		`"markdown":"# Example Domain\n\nThis synthetic page is for use in examples."}`)
	assertRoundTrip(t, &serper.ScrapeResponse{}, `{"text":""}`)
}

func TestAttributes(t *testing.T) {
	a := serper.Attributes{{"b", "1"}, {"a", "<&>"}, {"b", "2"}, {"", ""}}
	v, ok := a.Get("b")
	assert.True(t, ok)
	assert.Equal(t, "1", v, "the first of duplicates")
	_, ok = a.Get("B")
	assert.False(t, ok, "names match exactly")
	v, ok = a.Get("")
	assert.True(t, ok)
	assert.Empty(t, v)
	_, ok = serper.Attributes(nil).Get("a")
	assert.False(t, ok)

	data, err := json.Marshal(a)
	require.NoError(t, err)
	// Ordered, duplicates kept, escaped as encoding/json escapes.
	assertBody(t, `{"b":"1","a":"\u003c\u0026\u003e","b":"2","":""}`, data)
	var back serper.Attributes
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, a, back)

	for in, want := range map[string]serper.Attributes{
		`null`:      nil,
		`{}`:        {},
		`{"k":"v"}`: {{"k", "v"}},
	} {
		var got serper.Attributes
		require.NoError(t, json.Unmarshal([]byte(in), &got), in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{`[]`, `"x"`, `1`, `{"k":1}`, `{"k":null}`, `{"k":{}}`, `{"k":["v"]}`} {
		var got serper.Attributes
		uerr := json.Unmarshal([]byte(in), &got)
		require.Error(t, uerr, in)
		assert.NotContains(t, uerr.Error(), in, "the input is never quoted")
	}

	data, err = json.Marshal(serper.Attributes(nil))
	require.NoError(t, err)
	assert.Equal(t, "null", string(data))
	data, err = json.Marshal(serper.Attributes{})
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}
