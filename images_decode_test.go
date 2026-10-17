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

// The Images decoding suite. Its helpers (srdLive, srdRun..., the value
// tables) are the shared decoding harness, in decode_harness_test.go.

// imdKeptApart is the decoding of images/kept-apart.json.
func imdKeptApart() *serper.ImagesResponse {
	return &serper.ImagesResponse{
		SearchParameters: &serper.SearchParameters{
			Query: "example harbor images", Type: "images", Engine: "google", GL: "ca", HL: "en-CA",
			Location: "Example Harbor", TBS: "isz:l", Num: 100, Page: 4, Autocorrect: new(true),
		},
		Images: []serper.ImageResult{
			{
				Title:    "Example harbor at noon",
				ImageURL: "https://files.example.net/harbor/noon-full.jpg", ImageWidth: 4032, ImageHeight: 3024,
				ThumbnailURL:   "https://thumbs.example.org/harbor/noon-small.jpg",
				ThumbnailWidth: 259, ThumbnailHeight: 194,
				Source: "Example Harbor Gazette", Domain: "gazette.example.com",
				Link:      "https://gazette.example.com/articles/noon",
				GoogleURL: "https://www.example.com/imgres?imgurl=noon&docid=1", Position: 1,
			},
			{
				Title:    "Example harbor at night",
				ImageURL: "https://cdn.example.org/night.png", ImageWidth: 800, ImageHeight: 600,
				ThumbnailURL: "https://thumbs.example.net/night.png", ThumbnailWidth: 160, ThumbnailHeight: 120,
				Source: "Example Night Photos", Domain: "night.example.net", Link: "https://night.example.net/gallery/7",
				GoogleURL: "https://www.example.com/imgres?imgurl=night&docid=2", Position: 2,
			},
		},
		Meta: serper.Metadata{Op: serper.OpImages, Credits: reported(3)},
	}
}

func TestImagesDecodeEveryMember(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	res := valueOf[serper.ImagesResponse](t, l.decode(t, o, fixture(t, "images/kept-apart.json")))
	assert.Equal(t, imdKeptApart(), res, "every typed member, exactly as returned")
	for i, img := range res.Images {
		distinct := map[string]bool{}
		for _, v := range []string{img.ImageURL, img.Link, img.Source, img.Domain, img.GoogleURL, img.ThumbnailURL} {
			distinct[v] = true
		}
		assert.Len(t, distinct, 6, "image %d: the six location members are kept apart", i)
	}
}

func TestImagesDecodeKeptApart(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	t.Run("sparse fixture", func(t *testing.T) {
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, fixture(t, "images/sparse.json")))
		assert.Equal(t, []serper.ImageResult{
			{Title: "Only the image file", ImageURL: "https://files.example.net/only-image.jpg", Position: 1},
			{Title: "Only the page", Link: "https://www.example.org/only-page", Position: 2},
			{Title: "Only the source label", Source: "Example Source Label", Position: 3},
			{Title: "Only the domain", Domain: "only.example.com", Position: 4},
			{Title: "Only the Google URL", GoogleURL: "https://www.example.com/imgres?only=google", Position: 5},
			{
				Title: "Only the thumbnail", ThumbnailURL: "https://thumbs.example.net/only-thumb.jpg",
				ThumbnailWidth: 120, ThumbnailHeight: 90, Position: 6,
			},
			{
				Title: "Image file and page only", ImageURL: "https://files.example.net/pair.jpg",
				Link: "https://www.example.org/pair", Position: 7,
			},
			{Position: 8},
			{},
		}, res.Images, "nothing is filled in from another member")
		assert.Equal(t, reported(1), res.Meta.Credits)
	})
	only := []struct {
		member string
		want   func(v string) serper.ImageResult
	}{
		{"imageUrl", func(v string) serper.ImageResult { return serper.ImageResult{ImageURL: v} }},
		{"link", func(v string) serper.ImageResult { return serper.ImageResult{Link: v} }},
		{"source", func(v string) serper.ImageResult { return serper.ImageResult{Source: v} }},
		{"domain", func(v string) serper.ImageResult { return serper.ImageResult{Domain: v} }},
		{"googleUrl", func(v string) serper.ImageResult { return serper.ImageResult{GoogleURL: v} }},
		{"thumbnailUrl", func(v string) serper.ImageResult { return serper.ImageResult{ThumbnailURL: v} }},
		{"title", func(v string) serper.ImageResult { return serper.ImageResult{Title: v} }},
	}
	for _, tt := range only {
		t.Run("only "+tt.member, func(t *testing.T) {
			value := "https://only.example.com/" + tt.member
			body := `{"searchParameters":{},"images":[{` + strconv.Quote(tt.member) + `:"` + value + `"}]}`
			res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(body)))
			assert.Equal(t, []serper.ImageResult{tt.want(value)}, res.Images)
		})
	}
	t.Run("equal values are kept in each member", func(t *testing.T) {
		const same = "https://www.example.com/same"
		body := `{"searchParameters":{},"images":[{"imageUrl":"` + same + `","link":"` + same + `","googleUrl":"` + same +
			`","thumbnailUrl":"` + same + `","source":"example.com","domain":"example.com"}]}`
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(body)))
		assert.Equal(t, []serper.ImageResult{{
			ImageURL: same, Link: same, GoogleURL: same, ThumbnailURL: same, Source: "example.com", Domain: "example.com",
		}}, res.Images)
	})
}

// imdPaths returns every typed member path of an Images answer.
func imdPaths() []srdPath {
	const item = `{"searchParameters":{},"images":[{"title":"first"},{#}]}`
	paths := srdParameterPaths()
	paths = append(paths,
		srdPath{field: "images", kind: srdKindList, template: `{"searchParameters":{},"images":@}`},
		srdPath{field: "images", kind: srdKindItem, template: `{"searchParameters":{},"images":[{},@]}`},
	)
	paths = append(paths, srdStrings("images", item,
		"title", "imageUrl", "thumbnailUrl", "source", "domain", "link", "googleUrl")...)
	for _, name := range []string{"imageWidth", "imageHeight", "thumbnailWidth", "thumbnailHeight", "position"} {
		paths = append(paths, srdPath{
			field: "images." + name, kind: srdKindInt,
			template: strings.Replace(item, "#", `"filler":"x",`+strconv.Quote(name)+`:@`, 1),
		})
	}
	return paths
}

func TestImagesDecodeWrongTypes(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	t.Run("wrong", func(t *testing.T) { srdRunWrongTypes(t, l, o, imdPaths()) })
	t.Run("right", func(t *testing.T) { srdRunRightTypes(t, l, o, imdPaths()) })
	t.Run("the earliest wrong type is reported", func(t *testing.T) {
		// encoding/json documents that Unmarshal reports the earliest of the
		// type errors it skips over.
		for body, field := range map[string]string{
			`{"images":[{"imageWidth":1.5,"imageHeight":"2"}],"searchParameters":{"num":"x"}}`: "images.imageWidth",
			`{"searchParameters":{"autocorrect":1},"images":[{"position":"1"}]}`:               "searchParameters.autocorrect",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
}

func TestImagesDecodeNullItems(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	items := make([]string, 1000)
	for i := range items {
		items[i] = `{"title":"Example image ` + strconv.Itoa(i) + `"}`
	}
	items[999] = `null`
	for _, tt := range []struct {
		name, body, field string
	}{
		{"at 0", `{"searchParameters":{},"images":[null]}`, "images[0]"},
		{"at 0 before others", `{"searchParameters":{},"images":[null,{"title":"b"}]}`, "images[0]"},
		{"later", `{"searchParameters":{},"images":[{},{"title":"b"},{},{"position":4},null,{}]}`, "images[4]"},
		{"the first of two", `{"searchParameters":{},"images":[{},null,null]}`, "images[1]"},
		{"with whitespace", "{\"searchParameters\":{},\"images\":[\r\n {} ,\tnull\n]}", "images[1]"},
		{"at the end of a big list", `{"searchParameters":{},"images":[` + strings.Join(items, ",") + `]}`, "images[999]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op})
		})
	}
}

func TestImagesDecodeAbsentNullEmpty(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	for _, tt := range []struct {
		name string
		body string
		want *serper.ImagesResponse
	}{
		{"images absent", `{"searchParameters":{}}`, &serper.ImagesResponse{SearchParameters: &serper.SearchParameters{}}},
		{"images null", `{"searchParameters":{},"images":null}`, &serper.ImagesResponse{SearchParameters: &serper.SearchParameters{}}},
		{"images empty", `{"searchParameters":{},"images":[]}`, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{}, Images: []serper.ImageResult{},
		}},
		{"an empty item", `{"searchParameters":{},"images":[{}]}`, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{}, Images: []serper.ImageResult{{}},
		}},
		{"null scalars are left zero", `{"searchParameters":{"q":null,"num":null,"autocorrect":null},"images":[{"title":null,` +
			`"imageUrl":null,"imageWidth":null,"position":null}]}`, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{}, Images: []serper.ImageResult{{}},
		}},
		{"autocorrect false", `{"searchParameters":{"autocorrect":false},"images":[]}`, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{Autocorrect: new(false)}, Images: []serper.ImageResult{},
		}},
		{"autocorrect true", `{"searchParameters":{"autocorrect":true}}`, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{Autocorrect: new(true)},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			tt.want.Meta = serper.Metadata{Op: serper.OpImages}
			assert.Equal(t, tt.want, valueOf[serper.ImagesResponse](t, got))
		})
	}
	for i, body := range []string{`{}`, `{"searchParameters":null}`, `{"images":[]}`, `{"images":null,"credits":1}`} {
		t.Run("searchParameters missing "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			credits := serper.Credits{}
			if strings.Contains(body, "credits") {
				credits = reported(1)
			}
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", Op: o.op, Credits: credits,
			})
		})
	}
	t.Run("null images fixture", func(t *testing.T) {
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, fixture(t, "images/null-images.json")))
		assert.Equal(t, &serper.ImagesResponse{
			SearchParameters: &serper.SearchParameters{Query: "an example image query", Type: "images", Engine: "google"},
			Meta:             serper.Metadata{Op: serper.OpImages},
		}, res)
	})
}

func TestImagesDecodeUnknownMembers(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	srdRunNoise(t, l, o, `{<top>"searchParameters":{<params>"q":"example","type":"images"},"images":[{<image>"title":"Example",`+
		`"imageUrl":"https://images.example.net/a.jpg"},{<later>"position":2}]<end>}`)
	t.Run("near misses of member names are unknown", func(t *testing.T) {
		body := `{"searchParameters":{"num ":"x"},"image":[null],"images ":1,"imagesResults":{},` +
			`"images":[{"imageURL_":1,"image_url":2,"width":"x","Position ":"y","thumbnail":[]}],"credit":"x"}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		res := valueOf[serper.ImagesResponse](t, got)
		assert.Equal(t, []serper.ImageResult{{}}, res.Images)
		assert.Equal(t, serper.Credits{}, res.Meta.Credits)
	})
	t.Run("members of other operations are unknown", func(t *testing.T) {
		body := `{"searchParameters":{},"organic":[null],"knowledgeGraph":"x","peopleAlsoAsk":1,"text":1,"markdown":[]}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
	})
}

// imdNames returns every member name of an Images answer.
func imdNames() []string {
	return append(srdParameterNames(), "images", "title", "imageUrl", "imageWidth", "imageHeight", "thumbnailUrl",
		"thumbnailWidth", "thumbnailHeight", "source", "domain", "link", "googleUrl", "position")
}

func TestImagesDecodeMemberMatching(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	t.Run("respelled names", func(t *testing.T) { srdRunRenamed(t, l, o, "images", imdNames()) })
	t.Run("escaped names", func(t *testing.T) {
		body := `{"\u0073earchParameters":{"\u0071":"x"},"\u0069mages":[{"imageU\u0072l":` +
			`"https://images.example.net/a.jpg"}],"\u0063redits":2}`
		got := l.decode(t, o, []byte(body))
		srdAssertPlain(t, o, []byte(body), got)
		res := valueOf[serper.ImagesResponse](t, got)
		assert.Equal(t, []serper.ImageResult{{ImageURL: "https://images.example.net/a.jpg"}}, res.Images)
		assert.Equal(t, reported(2), res.Meta.Credits)
	})
	t.Run("failure fields use the SDK's names, not the body's spelling", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"SEARCHPARAMETERS":{"Page":"x"}}`:                     "searchParameters.page",
			`{"searchParameters":{},"IMAGES":[{"ImageWidth":"1"}]}`: "images.imageWidth",
			`{"searchParameters":{},"Images":[{"GOOGLEURL":1}]}`:    "images.googleUrl",
			`{"searchParameters":{},"iMaGeS":[{},null]}`:            "images[1]",
		} {
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Field: field, Op: o.op})
		}
	})
	t.Run("duplicate members", func(t *testing.T) { imdCheckDuplicates(t, l, o) })
}

// imdCheckDuplicates checks duplicate members of an Images answer, decoded as
// encoding/json decodes them: the last of a scalar or a list wins, and an
// item that comes again is decoded into the one already there.
func imdCheckDuplicates(t *testing.T, l *srdLive, o opCase) {
	t.Helper()
	params := &serper.SearchParameters{}
	for _, tt := range []struct {
		name string
		body string
		want []serper.ImageResult
	}{
		{
			"string, last wins", `{"searchParameters":{},"images":[{"imageUrl":"https://a.example.net/","imageUrl":"https://b.example.net/"}]}`,
			[]serper.ImageResult{{ImageURL: "https://b.example.net/"}},
		},
		{"int, last wins", `{"searchParameters":{},"images":[{"position":1,"position":2}]}`, []serper.ImageResult{{Position: 2}}},
		{
			"null after a string has no effect", `{"searchParameters":{},"images":[{"link":"https://www.example.org/","link":null}]}`,
			[]serper.ImageResult{{Link: "https://www.example.org/"}},
		},
		{
			"list, last wins", `{"searchParameters":{},"images":[{"title":"a"},{"title":"b"}],"images":[{"title":"c"}]}`,
			[]serper.ImageResult{{Title: "c"}},
		},
		{"list then null", `{"searchParameters":{},"images":[{}],"images":null}`, nil},
		{"null then empty list", `{"searchParameters":{},"images":null,"images":[]}`, []serper.ImageResult{}},
		{"items decoded into the earlier ones", `{"searchParameters":{},"images":[{"title":"a","source":"Example","position":1}],` +
			`"images":[{"title":"b"}]}`, []serper.ImageResult{{Title: "b", Source: "Example", Position: 1}}},
		{
			"another spelling then the exact name: the last wins", `{"searchParameters":{},"IMAGES":[{"title":"b"}],"images":[{"title":"a"}]}`,
			[]serper.ImageResult{{Title: "a"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			srdAssertPlain(t, o, []byte(tt.body), got)
			assert.Equal(t, &serper.ImagesResponse{
				SearchParameters: params, Images: tt.want, Meta: serper.Metadata{Op: serper.OpImages},
			}, valueOf[serper.ImagesResponse](t, got))
		})
	}
	for _, tt := range []struct {
		name, body string
		kind       serper.FailureKind
		field      string
	}{
		// encoding/json keeps the earliest type error, even when a later
		// duplicate has the right type.
		{
			"wrong type then right type", `{"searchParameters":{},"images":[{"imageWidth":"1","imageWidth":1}]}`,
			serper.FailureMalformedResponse, "images.imageWidth",
		},
		{"item then null item", `{"searchParameters":{},"images":[{}],"images":[null]}`, serper.FailureMalformedResponse, "images[0]"},
		{"searchParameters then null", `{"searchParameters":{},"searchParameters":null}`, serper.FailureUnexpectedResponse, "searchParameters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{Kind: tt.kind, Field: tt.field, Op: o.op})
		})
	}
}

// imdEveryString is an Images answer with @ as every string member.
const imdEveryString = `{"searchParameters":{"q":@,"type":@,"engine":@,"gl":@,"hl":@,"location":@,"tbs":@},` +
	`"images":[{"title":@,"imageUrl":@,"thumbnailUrl":@,"source":@,"domain":@,"link":@,"googleUrl":@}]}`

func TestImagesDecodeStrings(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	for _, tt := range srdTexts() {
		t.Run(tt.name, func(t *testing.T) {
			res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(srdAt(imdEveryString, tt.literal))))
			s := tt.want
			assert.Equal(t, &serper.ImagesResponse{
				SearchParameters: &serper.SearchParameters{Query: s, Type: s, Engine: s, GL: s, HL: s, Location: s, TBS: s},
				Images: []serper.ImageResult{{
					Title: s, ImageURL: s, ThumbnailURL: s, Source: s, Domain: s, Link: s, GoogleURL: s,
				}},
				Meta: serper.Metadata{Op: serper.OpImages},
			}, res, "every string member decoded exactly")
			srdAssertRoundTrip(t, o, res)
		})
	}
	for _, tt := range srdBadStrings() {
		t.Run("refused "+tt.name, func(t *testing.T) {
			got := l.decode(t, o, []byte(srdAt(imdEveryString, tt.body)))
			_ = assertCallError(t, got.err, wantCall{Kind: serper.FailureMalformedResponse, Op: o.op})
		})
	}
	t.Run("very long strings", func(t *testing.T) {
		long, literal := srdLong(512 << 10)
		plain := "https://images.example.net/" + strings.Repeat("long-path/", (1<<20)/10)
		body := `{"searchParameters":{"q":` + literal + `},"images":[{"title":` + literal + `,"imageUrl":"` + plain + `"}]}`
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(body)))
		assert.Equal(t, long, res.SearchParameters.Query, "kept whole")
		assert.Equal(t, []serper.ImageResult{{Title: long, ImageURL: plain}}, res.Images)
		srdAssertRoundTrip(t, o, res)
	})
}

func TestImagesDecodeNumbers(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	numbers := []struct {
		literal string
		want    int
	}{
		{"3", 3},
		{"1", 1},
		{"1", 1},
		{"0", 0},
		{"-2", -2},
		{"-0", 0},
		{strconv.Itoa(math.MaxInt), math.MaxInt},
		{strconv.Itoa(math.MinInt), math.MinInt},
	}
	items := make([]string, 0, len(numbers))
	want := make([]serper.ImageResult, 0, len(numbers))
	for i, n := range numbers {
		items = append(items, `{"title":"Image `+strconv.Itoa(i)+`","position":`+n.literal+`,"imageWidth":`+n.literal+
			`,"imageHeight":`+n.literal+`,"thumbnailWidth":`+n.literal+`,"thumbnailHeight":`+n.literal+`}`)
		want = append(want, serper.ImageResult{
			Title: "Image " + strconv.Itoa(i), Position: n.want, ImageWidth: n.want, ImageHeight: n.want,
			ThumbnailWidth: n.want, ThumbnailHeight: n.want,
		})
	}
	body := `{"searchParameters":{},"images":[` + strings.Join(items, ",") + `]}`
	res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(body)))
	assert.Equal(t, want, res.Images, "positions and sizes out of order, duplicated, zero and negative, kept as returned")
	srdAssertRoundTrip(t, o, res)
}

// imdBigImages returns an Images answer with n images, positions counting
// down, every third one without a thumbnail and every tenth a copy of the
// one before, and its decoding.
func imdBigImages(n int) (string, *serper.ImagesResponse) {
	want := &serper.ImagesResponse{
		SearchParameters: &serper.SearchParameters{Query: "big", Type: "images"},
		Images:           make([]serper.ImageResult, n),
		Meta:             serper.Metadata{Op: serper.OpImages, Credits: reported(1)},
	}
	items := make([]string, n)
	for i := range n {
		id := strconv.Itoa(i)
		img := serper.ImageResult{
			Title: "Image " + id, ImageURL: "https://images.example.net/" + id + ".jpg", ImageWidth: 640 + i,
			ImageHeight: 480, Source: "Example Source " + strconv.Itoa(i%7), Domain: "www.example.org",
			Link: "https://www.example.org/page/" + id, GoogleURL: "https://www.example.com/imgres?id=" + id, Position: n - i,
		}
		item := `{"title":"Image ` + id + `","imageUrl":"` + img.ImageURL + `","imageWidth":` + strconv.Itoa(img.ImageWidth) +
			`,"imageHeight":480,"source":"` + img.Source + `","domain":"www.example.org","link":"` + img.Link +
			`","googleUrl":"` + img.GoogleURL + `","position":` + strconv.Itoa(n-i)
		if i%3 != 0 {
			img.ThumbnailURL, img.ThumbnailWidth, img.ThumbnailHeight = "https://thumbs.example.net/"+id+".jpg", 160, 120
			item += `,"thumbnailUrl":"` + img.ThumbnailURL + `","thumbnailWidth":160,"thumbnailHeight":120`
		}
		items[i], want.Images[i] = item+`}`, img
		if i%10 == 9 {
			items[i], want.Images[i] = items[i-1], want.Images[i-1]
		}
	}
	return `{"searchParameters":{"q":"big","type":"images"},"images":[` + strings.Join(items, ",") + `],"credits":1}`, want
}

func TestImagesDecodeBigList(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	body, want := imdBigImages(1000)
	res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(body)))
	assert.Equal(t, want, res, "a big list keeps its order, duplicates and positions")
	srdAssertRoundTrip(t, o, res)

	res = valueOf[serper.ImagesResponse](t, l.decode(t, o, testutil.ImagesBody(1000)))
	require.Len(t, res.Images, 1000)
	for i, img := range res.Images {
		if !assert.Equal(t, i+1, img.Position) || !assert.Equal(t, "Example image "+strconv.Itoa(i+1), img.Title) {
			break
		}
	}
}

func TestImagesDecodeShapeBeforeSuccessRule(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	for i, tt := range []struct {
		body, field string
	}{
		{`{"images":[null]}`, "images[0]"},
		{`{"images":[{},null],"credits":4}`, "images[1]"},
		{`{"images":[{"imageWidth":"1"}]}`, "images.imageWidth"},
		{`{"searchParameters":null,"images":"x"}`, "images"},
		{`{"searchParameters":{"page":1.5}}`, "searchParameters.page"},
		{`{"searchParameters":"x","credits":4}`, "searchParameters"},
		{`{"images":[{},{"position":true}],"credits":4}`, "images.position"},
	} {
		t.Run("malformed "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", tt.body)
			got := l.decode(t, o, []byte(tt.body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureMalformedResponse, Field: tt.field, Op: o.op, Credits: srdCreditsIn(tt.body),
			})
		})
	}
	for i, body := range []string{
		`{"images":[{}]}`, `{"searchParameters":null,"images":[]}`, `{"images":[],"credits":4}`,
		`{"organic":[{}],"knowledgeGraph":{}}`,
	} {
		t.Run("unexpected "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{
				Kind: serper.FailureUnexpectedResponse, Field: "searchParameters", Op: o.op, Credits: srdCreditsIn(body),
			})
		})
	}
}

func TestImagesDecodeCredits(t *testing.T) {
	l := srdNewLive(t)
	o := opCaseNamed("Images")
	malformed, unexpected := serper.FailureMalformedResponse, serper.FailureUnexpectedResponse
	srdRunCredits(t, l, o, []srdCreditsCase{
		{"success", `{"searchParameters":{},"images":[]}`, "", ""},
		{"success with images", `{"searchParameters":{"type":"images"},"images":[{"title":"t","position":1}]}`, "", ""},
		{"wrong type", `{"searchParameters":{},"images":[{"thumbnailHeight":"x"}]}`, malformed, "images.thumbnailHeight"},
		{"wrong parameter type", `{"searchParameters":{"autocorrect":"yes"}}`, malformed, "searchParameters.autocorrect"},
		{"null item", `{"searchParameters":{},"images":[{},{},null]}`, malformed, "images[2]"},
		{"no searchParameters", `{"images":[]}`, unexpected, "searchParameters"},
		{"null searchParameters", `{"searchParameters":null,"images":[{}]}`, unexpected, "searchParameters"},
		{"empty object", `{}`, unexpected, "searchParameters"},
	})
	t.Run("only the top-level member counts", func(t *testing.T) {
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(`{"searchParameters":{"credits":4},"images":[{"credits":4}]}`)))
		assert.Equal(t, serper.Credits{}, res.Meta.Credits)
	})
	t.Run("the member name matches as encoding/json matches", func(t *testing.T) {
		res := valueOf[serper.ImagesResponse](t, l.decode(t, o, []byte(`{"searchParameters":{},"Credits":4}`)))
		assert.Equal(t, reported(4), res.Meta.Credits)
	})
	for i, body := range []string{`{"credits":4,"searchParameters":{}`, `{"credits":4,"searchParameters":{}}x`, `[{"credits":4}]`} {
		t.Run("not one valid object "+strconv.Itoa(i), func(t *testing.T) {
			t.Logf("input %q", body)
			got := l.decode(t, o, []byte(body))
			_ = assertCallError(t, got.err, wantCall{Kind: malformed, Op: o.op})
		})
	}
}
