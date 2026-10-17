package testutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixture returns the contents of the file name, a slash-separated path under
// testdata, failing tb when it cannot be read.
func Fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	require.NoError(tb, err)
	return data
}

// SearchBody returns a synthetic Search answer, an SDK fixture: the
// searchParameters echo, n organic results on example.com with a sitelink
// and an attribute each, one related question and search, and credits 1.
func SearchBody(n int) []byte {
	b := []byte(`{"searchParameters":{"q":"example query","type":"search","engine":"google"},"organic":[`)
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		id := strconv.Itoa(i + 1)
		b = append(b, `{"title":"Example result `...)
		b = append(b, id...)
		b = append(b, `","link":"https://www.example.com/page/`...)
		b = append(b, id...)
		b = append(b, `","snippet":"A synthetic snippet for result `...)
		b = append(b, id...)
		b = append(b, ` of the example query.","date":"3 days ago","position":`...)
		b = append(b, id...)
		b = append(b, `,"sitelinks":[{"title":"About","link":"https://www.example.com/about"}],`...)
		b = append(b, `"attributes":{"Founded":"2001"}}`...)
	}
	b = append(b, `],"peopleAlsoAsk":[{"question":"What is an example?","snippet":"A synthetic answer.",`...)
	b = append(b, `"title":"Example","link":"https://www.example.org/faq"}],`...)
	b = append(b, `"relatedSearches":[{"query":"example query related"}],"credits":1}`...)
	return b
}

// ImagesBody returns a synthetic Images answer, an SDK fixture: the
// searchParameters echo, n image results on example.net, and credits 1.
func ImagesBody(n int) []byte {
	b := []byte(`{"searchParameters":{"q":"example images","type":"images","engine":"google"},"images":[`)
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		id := strconv.Itoa(i + 1)
		b = append(b, `{"title":"Example image `...)
		b = append(b, id...)
		b = append(b, `","imageUrl":"https://images.example.net/`...)
		b = append(b, id...)
		b = append(b, `.jpg","imageWidth":1280,"imageHeight":720,"thumbnailUrl":"https://thumbs.example.net/`...)
		b = append(b, id...)
		b = append(b, `.jpg","thumbnailWidth":300,"thumbnailHeight":168,"source":"Example Source","domain":"www.example.net",`...)
		b = append(b, `"link":"https://www.example.net/gallery/`...)
		b = append(b, id...)
		b = append(b, `","googleUrl":"https://www.example.com/imgres?id=`...)
		b = append(b, id...)
		b = append(b, `","position":`...)
		b = append(b, id...)
		b = append(b, '}')
	}
	return append(b, `],"credits":1}`...)
}

// ScrapeBody returns a synthetic Scrape answer, an SDK fixture, whose text
// is textBytes bytes of plain ASCII, with the same text as markdown when
// markdown is set, and credits 2.
func ScrapeBody(textBytes int, markdown bool) []byte {
	text := strings.Repeat("Example text. ", textBytes/14+1)[:textBytes]
	b := []byte(`{"text":"`)
	b = append(b, text...)
	b = append(b, '"')
	if markdown {
		b = append(b, `,"markdown":"`...)
		b = append(b, text...)
		b = append(b, '"')
	}
	return append(b, `,"credits":2}`...)
}
