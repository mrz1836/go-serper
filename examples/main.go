// Command examples shows the go-serper SDK end to end, offline: two local
// TLS servers play a synthetic Serper (one for each origin), so the demo
// needs no API key, makes no network call and costs nothing.
//
//	go run ./examples
package main

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

// demoKey is the demo's made-up key. A real application passes its own, from
// wherever it keeps secrets.
const demoKey = "example-demo-key"

// Synthetic responses: SDK fixtures with every value made up.
const (
	searchAnswer = `{"searchParameters":{"q":"lighthouse tours","type":"search","engine":"google"},` +
		`"knowledgeGraph":{"title":"Example Lighthouse","attributes":{"Opened":"1901","Height":"45 m"}},` +
		`"organic":[{"title":"Lighthouse tours","link":"https://www.example.com/tours","date":"3 days ago",` +
		`"position":1},{"title":"Visiting hours","link":"https://www.example.org/hours","position":2}],` +
		`"answerBox":{"snippet":"An untyped member, kept only in a raw capture."},"credits":1}`
	imagesAnswer = `{"searchParameters":{"q":"lighthouse at dusk","type":"images"},"images":[` +
		`{"title":"Dusk","imageUrl":"https://images.example.net/dusk.jpg","link":"https://photos.example.com/dusk",` +
		`"source":"Example Photos","domain":"photos.example.com","position":1}]}`
	scrapeAnswer = `{"text":"Example Domain. This synthetic page is for use in examples.",` +
		`"markdown":"# Example Domain","credits":2}`
	driftAnswer = `{"searchParameters":{"q":"schema drift"},"organic":{"changed":"shape"}}`
)

// api is what the demo runs against: one handler per origin, playing Serper.
type api interface {
	search() http.Handler
	scrape() http.Handler
}

func main() {
	if err := run(context.Background(), os.Stdout, fakeSerper{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "examples:", err)
		os.Exit(1)
	}
}

// run serves fake locally, walks through every operation against it, and
// writes what came back to w.
func run(ctx context.Context, w io.Writer, fake api) error {
	searchSrv := newServer(fake.search())
	defer searchSrv.Close()
	scrapeSrv := newServer(fake.scrape())
	defer scrapeSrv.Close()

	// A real application passes its own key and leaves out the three test
	// options: the SDK then talks to https://google.serper.dev and
	// https://scrape.serper.dev over its own HTTP/1.1 transport.
	client, err := serper.NewClient(demoKey,
		serper.WithTimeout(10*time.Second),
		serper.WithHTTPClient(searchSrv.Client()), // httptest servers share one certificate
		serper.WithSearchBaseURL(searchSrv.URL),
		serper.WithScrapeBaseURL(scrapeSrv.URL),
	)
	if err != nil {
		return describe(w, err)
	}
	steps := []func(context.Context, io.Writer, *serper.Client) error{
		search, images, scrape, replay, drift, concurrent,
	}
	for _, step := range steps {
		if err := step(ctx, w, client); err != nil {
			return describe(w, err)
		}
	}
	return nil
}

// newServer starts a local TLS server running h, whose error log is
// discarded: a server closing while a client finishes a spare TLS handshake
// logs a harmless error.
func newServer(h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	return srv
}

// search runs one web search.
func search(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "Search (spends credits)")
	res, err := client.Search(ctx, serper.SearchRequest{Query: "lighthouse tours", GL: "us"})
	if err != nil {
		return err
	}
	for _, o := range res.Organic {
		_, _ = fmt.Fprintf(w, "  %d. %s <%s>", o.Position, o.Title, o.Link)
		if o.Date != "" {
			_, _ = fmt.Fprintf(w, " %s", o.Date) // provider text, never parsed
		}
		_, _ = fmt.Fprintln(w)
	}
	if kg := res.KnowledgeGraph; kg != nil {
		opened, _ := kg.Attributes.Get("Opened")
		_, _ = fmt.Fprintf(w, "  knowledge graph: %s (opened %s)\n", kg.Title, opened)
	}
	writeCredits(w, res.Meta)
	return nil
}

// images runs one image search.
func images(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "\nImages (spends credits)")
	res, err := client.Images(ctx, serper.ImagesRequest{Query: "lighthouse at dusk", Autocorrect: new(false)})
	if err != nil {
		return err
	}
	for _, im := range res.Images {
		_, _ = fmt.Fprintf(w, "  image %s\n  on page %s\n  source %q (%s)\n", im.ImageURL, im.Link, im.Source, im.Domain)
	}
	writeCredits(w, res.Meta)
	return nil
}

// scrape asks Serper to fetch one page; the SDK never contacts the target.
func scrape(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "\nScrape (spends credits; Serper fetches the page)")
	page, err := client.Scrape(ctx, serper.ScrapeRequest{URL: "https://www.example.com/", IncludeMarkdown: true})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "  text: %d bytes\n", len(page.Text))
	if page.Markdown != nil {
		_, _ = fmt.Fprintf(w, "  markdown: %q\n", *page.Markdown)
	}
	writeCredits(w, page.Meta)
	return nil
}

// replay keeps one response's exact bytes and decodes them again, offline.
func replay(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "\nRaw capture and offline replay")
	res, err := client.Search(ctx, serper.SearchRequest{Query: "lighthouse tours"}, serper.WithRawCapture(true))
	if err != nil {
		return err
	}
	raw, ok := res.RawJSON()
	if !ok {
		return errNoCapture
	}
	// Store raw as bytes, with the input the application owns. The metadata
	// is safe to store too: it never holds the key, the query or a body.
	m := res.Meta
	_, _ = fmt.Fprintf(w, "  captured %d bytes (%s): op=%s path=%s status=%d attempts=%d\n",
		len(raw), m.RawCapture, m.Op, m.Path, m.StatusCode, m.Attempts)
	again, err := serper.DecodeSearchResponse(raw)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "  replayed offline: %d results, untyped answerBox kept: %t\n",
		len(again.Organic), strings.Contains(string(raw), `"answerBox"`))
	return nil
}

// Errors of the demo's own expectations.
var (
	errNoCapture = errors.New("examples: the capture is missing")
	errNoDrift   = errors.New("examples: the changed response was accepted")
)

// drift shows a response whose shape changed: the call fails, the error is
// safe to log, and with capture on the body is kept for later study.
func drift(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "\nA changed response shape")
	_, err := client.Search(ctx, serper.SearchRequest{Query: "schema drift"}, serper.WithRawCapture(true))
	ce, ok := errors.AsType[*serper.CallError](err)
	switch {
	case err == nil:
		return errNoDrift
	case !ok:
		return err
	}
	raw, kept := ce.RawJSON()
	_, _ = fmt.Fprintf(w, "  %v\n  outcome unknown (may be billed): %t; body kept: %t (%d bytes)\n",
		err, errors.Is(err, serper.ErrOutcomeUnknown), kept, len(raw))
	return nil
}

// concurrent shares the client between goroutines, with the caller's own
// bound on concurrency.
func concurrent(ctx context.Context, w io.Writer, client *serper.Client) error {
	_, _ = fmt.Fprintln(w, "\nConcurrent calls on one client")
	queries := []string{"lighthouse tours", "lighthouse tours", "lighthouse tours"}
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	errs := make([]error, len(queries))
	counts := make([]int, len(queries))
	for i, q := range queries {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := client.Search(ctx, serper.SearchRequest{Query: q})
			if err != nil {
				errs[i] = err
				return
			}
			counts[i] = len(res.Organic)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "  %d separate calls (no cache), results %v\n", len(queries), counts)
	return nil
}

// writeCredits reports the credits a call used, when the response said.
func writeCredits(w io.Writer, meta serper.Metadata) {
	if used, ok := meta.Credits.Value(); ok {
		_, _ = fmt.Fprintf(w, "  credits used: %d\n", used)
		return
	}
	_, _ = fmt.Fprintln(w, "  credits used: not reported (unknown, not zero)")
}

// describe writes one line about a failure, which is safe to log, and returns
// err.
func describe(w io.Writer, err error) error {
	switch {
	case errors.Is(err, serper.ErrInvalidConfig), errors.Is(err, serper.ErrInvalidRequest):
		_, _ = fmt.Fprintln(w, "refused locally, nothing sent:", err)
	case errors.Is(err, context.Canceled):
		_, _ = fmt.Fprintln(w, "stopped by the caller")
	case errors.Is(err, serper.ErrTimeout):
		_, _ = fmt.Fprintln(w, "the SDK's timeout expired; it may be billed:", errors.Is(err, serper.ErrOutcomeUnknown))
	case errors.Is(err, serper.ErrUnauthorized), errors.Is(err, serper.ErrForbidden):
		_, _ = fmt.Fprintln(w, "check the API key and the account")
	case errors.Is(err, serper.ErrRateLimited):
		_, _ = fmt.Fprintln(w, "rate limited: the SDK never retries; try again later")
	default:
		_, _ = fmt.Fprintln(w, "failed:", err)
	}
	return err
}

// fakeSerper plays both origins with the synthetic responses.
type fakeSerper struct{}

// search answers POST /search and POST /images.
func (fakeSerper) search() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != demoKey {
			reply(w, http.StatusUnauthorized, `{"message":"Synthetic: missing or invalid key."}`)
			return
		}
		var req struct {
			Q string `json:"q"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
			reply(w, http.StatusBadRequest, `{"message":"Synthetic: bad request."}`)
			return
		}
		switch {
		case r.URL.Path == "/search" && req.Q == "schema drift":
			reply(w, http.StatusOK, driftAnswer)
		case r.URL.Path == "/search":
			reply(w, http.StatusOK, searchAnswer)
		case r.URL.Path == "/images":
			reply(w, http.StatusOK, imagesAnswer)
		default:
			http.NotFound(w, r)
		}
	})
}

// scrape answers POST /.
func (fakeSerper) scrape() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != demoKey {
			reply(w, http.StatusUnauthorized, `{"message":"Synthetic: missing or invalid key."}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		reply(w, http.StatusOK, scrapeAnswer)
	})
}

// reply writes a JSON response.
func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
