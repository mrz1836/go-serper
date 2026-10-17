package serper_test

import "github.com/mrz1836/go-serper"

// Privacy markers. Each is planted where a value could leak from: the key, a
// request field, a response body or header, a transport error or a context
// cause. No error, formatted value, reflection walk or metadata may ever show
// one. They are synthetic, not secrets. The privacy suite shares them with
// the tables it runs alongside other suites, such as invalidRequests and
// doneContexts.
const (
	prvKey         = "prvkeymarker-5e8c1d" // gitleaks:allow (synthetic test marker)
	prvQuery       = "prvquerymarker lighthouse"
	prvReqLocation = "prvreqlocationmarker, Example City"
	prvGL          = "prvglmarker"
	prvHL          = "prvhlmarker"
	prvTBS         = "prvtbsmarker"
	prvTarget      = "https://www.example.com/prvtargetpathmarker/page?prvtargetquerymarker=1#prvtargetfragmarker"

	prvMarkBody     = "prvbodymarker"
	prvMarkHeader   = "prvheadermarker"
	prvMarkLocation = "prvlocationmarker"
	prvMarkGoAway   = "prvgoawaymarker"
	prvMarkURLErr   = "prvurlerrmarker"
	prvMarkNet      = "prvnetmarker"
	prvMarkDNS      = "prvdnsmarker"
	prvMarkTLS      = "prvtlsmarker"
	prvMarkTimeout  = "prvtimeoutmarker"
	prvMarkRead     = "prvreadmarker"
	prvMarkJoin     = "prvjoinmarker"
	prvMarkCause    = "prvcausemarker"
	prvMarkRaw      = "prvrawmarker"
	prvMarkBaseURL  = "prvbaseurlmarker"
	prvMarkAgent    = "prvagentmarker"
	prvMarkUser     = "prvtargetusermarker"
)

// prvSecrets returns every privacy marker.
func prvSecrets() []string {
	return []string{
		prvKey, "prvkeymarker", prvQuery, "prvquerymarker", prvReqLocation, "prvreqlocationmarker",
		prvGL, prvHL, prvTBS, prvTarget, "prvtargetpathmarker", "prvtargetquerymarker", "prvtargetfragmarker",
		prvMarkBody, prvMarkHeader, prvMarkLocation, prvMarkGoAway, prvMarkURLErr, prvMarkNet, prvMarkDNS,
		prvMarkTLS, prvMarkTimeout, prvMarkRead, prvMarkJoin, prvMarkCause, prvMarkRaw, prvMarkBaseURL,
		prvMarkAgent, prvMarkUser,
		// Every marker ends in "marker", so this also catches a marker cut
		// short.
		"marker",
	}
}

// prvTextError is a planted error whose text is a marker.
type prvTextError string

func (e prvTextError) Error() string { return string(e) }

// prvSearchRequest returns a Search request with a marker in every text field
// and every other field set.
func prvSearchRequest() serper.SearchRequest {
	return serper.SearchRequest{
		Query: prvQuery, Location: prvReqLocation, GL: prvGL, HL: prvHL, Num: 7,
		Autocorrect: new(true), TBS: prvTBS, Page: 2,
	}
}

// prvScrapeRequest returns a Scrape request for the marker target.
func prvScrapeRequest() serper.ScrapeRequest {
	return serper.ScrapeRequest{URL: prvTarget, IncludeMarkdown: true}
}
