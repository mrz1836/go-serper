package serper_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mrz1836/go-serper"
)

// Synthetic provider responses for the examples: SDK fixtures with every value
// made up.
const (
	exSearch = `{"searchParameters":{"q":"\"go http client\" -site:example.com","type":"search",` +
		`"engine":"google","gl":"us","num":10},"organic":[` +
		`{"title":"An HTTP client guide","link":"https://www.example.org/guide","snippet":"Synthetic.",` +
		`"date":"Aug 31, 2022","position":1},` +
		`{"title":"Timeouts explained","link":"https://www.example.net/timeouts","snippet":"Synthetic.",` +
		`"date":"3 days ago","position":2,"attributes":{"Level":"Intermediate"}}],"credits":1}`
	exPage2 = `{"searchParameters":{"q":"example query","type":"search","page":2},"organic":[` +
		`{"title":"A result on page two","link":"https://www.example.com/p2","position":11}]}`
	exImages = `{"searchParameters":{"q":"lighthouse at dusk","type":"images","autocorrect":false},"images":[` +
		`{"title":"Lighthouse at dusk","imageUrl":"https://images.example.net/dusk.jpg","imageWidth":1920,` +
		`"imageHeight":1080,"source":"Example Photo Stories","domain":"photos.example.com",` +
		`"link":"https://photos.example.com/stories/dusk","position":1}],"credits":1}`
	exScrape = `{"text":"Example Domain. This synthetic page is for use in examples.",` +
		`"markdown":"# Example Domain\n\nThis synthetic page is for use in examples.","credits":2}`
	exDrift = `{"searchParameters":{"q":"example query"},"organic":{"unexpected":"shape"}}`
)

// exServers starts two local TLS servers that stand in for Serper's two
// origins, so the examples run offline, need no key and cost nothing. Each
// answers POST to a path with the body given for it, and anything else with
// HTTP 404, or with status when it is set.
func exServers(status int, search, scrape map[string]string) (searchSrv, scrapeSrv *httptest.Server) {
	serve := func(bodies map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, ok := bodies[r.URL.Path]
			if r.Method != http.MethodPost || !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if status != 0 {
				w.WriteHeader(status)
			}
			_, _ = io.WriteString(w, body)
		}
	}
	return httptest.NewTLSServer(serve(search)), httptest.NewTLSServer(serve(scrape))
}

// exClient returns a client for the stand-in servers. A real application
// passes its own key and leaves out the three test options.
func exClient(searchSrv, scrapeSrv *httptest.Server) *serper.Client {
	client, err := serper.NewClient("example-api-key",
		serper.WithHTTPClient(searchSrv.Client()), // httptest servers share one certificate
		serper.WithSearchBaseURL(searchSrv.URL),
		serper.WithScrapeBaseURL(scrapeSrv.URL),
	)
	if err != nil {
		panic(err)
	}
	return client
}

// Example runs one web search with a key read from the environment. It makes
// a real, paid call, so it has no Output: go test only compiles it.
func Example() {
	client, err := serper.NewClient(os.Getenv("SERPER_API_KEY"))
	if err != nil {
		log.Fatal(err) // a *serper.ValidationError, such as a missing key; nothing was sent
	}
	res, err := client.Search(context.Background(), serper.SearchRequest{Query: "golang http client"})
	if err != nil {
		log.Fatal(err) // never contains the key, the query or a body
	}
	for _, o := range res.Organic {
		fmt.Println(o.Position, o.Title, o.Link)
	}
}

// ExampleNewClient builds one reusable client with finite timeouts. It is
// safe for concurrent use. NewClient makes no call and reads no environment
// variable or file: the application decides where the key comes from.
func ExampleNewClient() {
	apiKey := "example-api-key" // from your application's secret store

	client, err := serper.NewClient(apiKey,
		serper.WithTimeout(20*time.Second),
		serper.WithScrapeTimeout(45*time.Second), // an explicit, longer scrape budget
		serper.WithMaxResponseBytes(4<<20),
	)
	if err != nil {
		fmt.Println(err) // a *serper.ValidationError; nothing was sent
		return
	}
	fmt.Println(client) // settings and origins, never the key

	_, err = serper.NewClient(apiKey, serper.WithTimeout(time.Hour))
	fmt.Println(err) // names the option, never the value
	// Output:
	// serper.Client{searchOrigin: https://google.serper.dev, scrapeOrigin: https://scrape.serper.dev, timeout: 20s, scrapeTimeout: 45s, maxRequestBytes: 65536, maxResponseBytes: 4194304, userAgent: "go-serper", defaultRawCapture: false, httpClient: default}
	// serper: invalid configuration: WithTimeout: out_of_range
}

// ExampleClient_Search runs one web search. Results keep the provider's
// order and values; credits are reported only when the response carries them.
func ExampleClient_Search() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exSearch}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	res, err := client.Search(ctx, serper.SearchRequest{Query: `"go http client" -site:example.com`, GL: "us", Num: 10})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, o := range res.Organic {
		fmt.Println(o.Position, o.Title, o.Link, o.Date) // provider order, values as returned
	}
	if used, ok := res.Meta.Credits.Value(); ok {
		fmt.Println("credits used:", used)
	} // otherwise unknown, not zero
	// Output:
	// 1 An HTTP client guide https://www.example.org/guide Aug 31, 2022
	// 2 Timeouts explained https://www.example.net/timeouts 3 days ago
	// credits used: 1
}

// ExampleClient_Images runs one image search. The image file, the page it
// appears on and the provider's label for its source stay separate.
func ExampleClient_Images() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/images": exImages}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	imgs, err := client.Images(ctx, serper.ImagesRequest{Query: "lighthouse at dusk", Autocorrect: new(false)})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, im := range imgs.Images {
		fmt.Println(im.ImageURL, im.Link, im.Source, im.Domain)
	}
	// Output:
	// https://images.example.net/dusk.jpg https://photos.example.com/stories/dusk Example Photo Stories photos.example.com
}

// ExampleClient_Scrape asks Serper to fetch one page. The target is data sent
// to Serper: the SDK never fetches it.
func ExampleClient_Scrape() {
	searchSrv, scrapeSrv := exServers(0, nil, map[string]string{"/": exScrape})
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	page, err := client.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/post?id=7", IncludeMarkdown: true})
	if err != nil {
		fmt.Println(err) // a failed scrape is never an empty success
		return
	}
	fmt.Println(len(page.Text))
	if page.Markdown != nil {
		fmt.Println(len(*page.Markdown))
	}
	// Output:
	// 59
	// 61
}

// replayRecord is how one application might store a captured response for
// replay, together with the input it owns.
type replayRecord struct {
	CorrelationID string           `json:"correlationId"`
	Op            serper.Operation `json:"op"`
	Query         string           `json:"query"`
	Body          []byte           `json:"body"` // stored as bytes, never re-encoded
	Meta          serper.Metadata  `json:"meta"`
}

// ExampleWithRawCapture keeps the exact response bytes of one call for replay
// storage, and decodes them again later, offline.
func ExampleWithRawCapture() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exSearch}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	req := serper.SearchRequest{Query: "example query"}
	res, err := client.Search(ctx, req, serper.WithRawCapture(true))
	if err != nil {
		fmt.Println(err)
		return
	}
	raw, ok := res.RawJSON() // an owned copy: the exact bytes the decoder saw
	if !ok {
		return
	}
	rec := replayRecord{CorrelationID: "req-0001", Op: res.Meta.Op, Query: req.Query, Body: raw, Meta: res.Meta}
	fmt.Println(rec.Op, rec.Meta.RawCapture, len(rec.Body))

	// Later, offline: the same decoder, success rule and credits reading.
	again, err := serper.DecodeSearchResponse(rec.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(again.Organic), again.Organic[0].Title)
	// Output:
	// search complete 439
	// 2 An HTTP client guide
}

// ExampleCallError tells failures apart. Errors are safe to log: they never
// contain the key, the query, a URL or a body.
func ExampleCallError() {
	searchSrv, scrapeSrv := exServers(http.StatusTooManyRequests,
		map[string]string{"/search": `{"message":"Synthetic: slow down."}`}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	_, err := client.Search(ctx, serper.SearchRequest{Query: "example query"})
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled): // the caller canceled
	case errors.Is(err, serper.ErrTimeout): // the SDK's timeout (also matches context.DeadlineExceeded)
	case errors.Is(err, serper.ErrRateLimited): // HTTP 429: no automatic retry; the caller decides
		fmt.Println("rate limited; outcome unknown:", errors.Is(err, serper.ErrOutcomeUnknown))
	case errors.Is(err, serper.ErrResponseTooLarge): // raise WithMaxResponseBytes deliberately
	}
	if ce, ok := errors.AsType[*serper.CallError](err); ok {
		fmt.Printf("serper %s: kind=%s status=%d attempts=%d outcomeUnknown=%t\n",
			ce.Meta.Op, ce.Kind, ce.Meta.StatusCode, ce.Meta.Attempts, ce.OutcomeUnknown)
	}
	fmt.Println(err)
	// Output:
	// rate limited; outcome unknown: false
	// serper search: kind=rate_limited status=429 attempts=1 outcomeUnknown=false
	// serper: search: rate_limited (HTTP 429)
}

// ExampleCallError_RawJSON keeps a response that the decoder refused, for
// later study, when raw capture is on. Here the provider changed the shape of
// organic.
func ExampleCallError_RawJSON() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exDrift}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	_, err := client.Search(ctx, serper.SearchRequest{Query: "example query"}, serper.WithRawCapture(true))
	fmt.Println(err)
	if ce, ok := errors.AsType[*serper.CallError](err); ok {
		if raw, ok := ce.RawJSON(); ok { // schema drift: keep the response for later replay
			fmt.Println(ce.Meta.RawCapture, len(raw))
		}
	}
	// Output:
	// serper: search: malformed_response (HTTP 200): organic
	// complete 75
}

// ExampleValidationError shows a request refused before anything was sent.
func ExampleValidationError() {
	client, err := serper.NewClient("example-api-key")
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = client.Search(context.Background(), serper.SearchRequest{Query: "example", Num: -1})
	if ve, ok := errors.AsType[*serper.ValidationError](err); ok {
		fmt.Printf("invalid %s.%s (%s); nothing sent\n", ve.Op, ve.Field, ve.Reason)
	}
	// Output:
	// invalid search.Num (out_of_range); nothing sent
}

// ExampleClient_Search_page asks for one more page, deliberately: the SDK
// never fetches all pages or guesses where results end.
func ExampleClient_Search_page() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exPage2}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	res, err := client.Search(ctx, serper.SearchRequest{Query: "example query", Page: 2})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.SearchParameters.Page, res.Organic[0].Position)
	// Output:
	// 2 11
}

// ExampleClient_Search_concurrent shares one client between goroutines. The
// caller owns the concurrency bound; per-call options never touch shared
// configuration.
func ExampleClient_Search_concurrent() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exSearch}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)
	ctx := context.Background()

	queries := []string{"alpha", "beta", "alpha"} // a duplicate stays a separate call
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	out, errs := make([]*serper.SearchResponse, len(queries)), make([]error, len(queries))
	for i, q := range queries {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = client.Search(ctx, serper.SearchRequest{Query: q}, serper.WithRawCapture(i == 0))
		})
	}
	wg.Wait() // results are keyed by index, never by query string
	for i := range queries {
		if errs[i] != nil {
			fmt.Println(i, errs[i])
			continue
		}
		fmt.Println(i, len(out[i].Organic), out[i].Meta.RawCapture)
	}
	// Output:
	// 0 2 complete
	// 1 2 off
	// 2 2 off
}

// ExampleClient_Search_handler uses the client in a net/http handler: the
// effective deadline is whichever comes first, the request's context or the
// client's timeout.
func ExampleClient_Search_handler() {
	searchSrv, scrapeSrv := exServers(0, map[string]string{"/search": exSearch}, nil)
	defer searchSrv.Close()
	defer scrapeSrv.Close()
	client := exClient(searchSrv, scrapeSrv)

	handler := func(c *serper.Client) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			res, err := c.Search(ctx, serper.SearchRequest{Query: r.FormValue("q")})
			if err != nil {
				http.Error(w, "search unavailable", http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(res.Organic)
		}
	}(client)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/find?q=example", nil))
	fmt.Println(rec.Code, strings.Count(rec.Body.String(), `"position"`))

	rec = httptest.NewRecorder()
	handler(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/find", nil))
	fmt.Print(rec.Code, " ", rec.Body.String()) // an empty query is refused before sending
	// Output:
	// 200 2
	// 502 search unavailable
}

// webSearcher is the tiny interface application code can own, so that its
// tests can fake the SDK: the SDK exports no interface of its own.
type webSearcher interface {
	Search(ctx context.Context, req serper.SearchRequest, opts ...serper.CallOption) (*serper.SearchResponse, error)
}

// fakeSearcher is an application's test double for webSearcher.
type fakeSearcher struct{ titles []string }

func (f fakeSearcher) Search(context.Context, serper.SearchRequest, ...serper.CallOption) (*serper.SearchResponse, error) {
	res := &serper.SearchResponse{}
	for i, t := range f.titles {
		res.Organic = append(res.Organic, serper.OrganicResult{Title: t, Position: i + 1})
	}
	return res, nil
}

// firstTitle is application code that depends only on its own interface.
func firstTitle(ctx context.Context, s webSearcher, q string) (string, error) {
	res, err := s.Search(ctx, serper.SearchRequest{Query: q})
	if err != nil {
		return "", err
	}
	if len(res.Organic) == 0 {
		return "", nil
	}
	return res.Organic[0].Title, nil
}

// ExampleClient_Search_interface tests application code with an interface the
// caller owns, satisfied by *serper.Client and by a fake.
func ExampleClient_Search_interface() {
	var _ webSearcher = (*serper.Client)(nil) // the real client satisfies it

	title, err := firstTitle(context.Background(), fakeSearcher{titles: []string{"Synthetic first"}}, "q")
	fmt.Println(title, err)
	// Output:
	// Synthetic first <nil>
}

// ExampleAttributes_Get reads one attribute by name. Attributes keep the
// provider's order and duplicates.
func ExampleAttributes_Get() {
	res, err := serper.DecodeSearchResponse([]byte(`{"searchParameters":{},"knowledgeGraph":` +
		`{"title":"Example Lighthouse","attributes":{"Height":"45 m","Opened":"1901","Height":"46 m"}}}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	attrs := res.KnowledgeGraph.Attributes
	height, ok := attrs.Get("Height")
	fmt.Println(height, ok, len(attrs))
	_, ok = attrs.Get("Closed")
	fmt.Println(ok)
	// Output:
	// 45 m true 3
	// false
}

// ExampleCredits_Value tells reported credits apart from unknown ones.
func ExampleCredits_Value() {
	for _, body := range []string{
		`{"text":"","credits":2}`,
		`{"text":""}`,
		`{"text":"","credits":"two"}`,
	} {
		page, err := serper.DecodeScrapeResponse([]byte(body))
		if err != nil {
			fmt.Println(err)
			continue
		}
		used, ok := page.Meta.Credits.Value()
		fmt.Println(page.Meta.Credits.Status, used, ok)
	}
	// Output:
	// reported 2 true
	// not_reported 0 false
	// invalid 0 false
}
