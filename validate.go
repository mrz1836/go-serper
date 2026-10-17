package serper

import (
	"context"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// prepare runs the checks every operation starts with: a non-nil context,
// then a Client built by NewClient.
func (c *Client) prepare(ctx context.Context, op Operation) error {
	if ctx == nil {
		return invalidRequest(op, "ctx", reasonNil)
	}
	if c == nil || c.send == nil {
		return invalidConfig(op, "Client", reasonNotInitialized)
	}
	return nil
}

// checkKey validates the API key: 1 to 256 bytes of visible ASCII, so that it
// is a valid header value as it is. It is never trimmed. The error names the
// argument, never its value.
func checkKey(key string) error {
	switch {
	case key == "":
		return invalidConfig("", "apiKey", reasonRequired)
	case len(key) > maxKeyBytes || !asciiRange(key, 0x21, 0x7e):
		return invalidConfig("", "apiKey", reasonInvalidFormat)
	}
	return nil
}

// userAgentProblem returns the reason ua is not a User-Agent the SDK sends,
// or "": 1 to 256 bytes of visible ASCII and spaces, without a leading or
// trailing space.
func userAgentProblem(ua string) string {
	switch {
	case ua == "":
		return reasonRequired
	case len(ua) > maxUserAgentBytes || !asciiRange(ua, 0x20, 0x7e) ||
		ua[0] == ' ' || ua[len(ua)-1] == ' ':
		return reasonInvalidFormat
	}
	return ""
}

// originHost validates a base URL and returns its "host[:port]", or the
// reason it is refused: it must be an absolute https URL with a host, an
// optional port from 1 to 65535, and no userinfo, query, fragment, backslash,
// percent-encoding or path other than "/". The host is an ASCII host name
// (letters, digits, hyphens and dots, such as a punycode "xn--" name) or a
// bracketed IPv6 address without a zone: a non-ASCII or escaped host could
// display as one name and be sent to another once the transport maps it.
func originHost(raw string) (string, string) {
	switch {
	case raw == "":
		return "", reasonRequired
	case strings.ContainsAny(raw, "\\%"):
		return "", reasonInvalidURL
	case strings.ContainsRune(raw, '?'):
		return "", reasonHasQuery
	case strings.ContainsRune(raw, '#'):
		return "", reasonHasFragment
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Opaque != "":
		return "", reasonInvalidURL
	case u.Scheme != "https":
		return "", reasonUnsupportedScheme
	case u.User != nil:
		return "", reasonHasUserinfo
	case u.Hostname() == "":
		return "", reasonMissingHost
	case u.Path != "" && u.Path != "/":
		return "", reasonHasPath
	case !validHost(u.Host, u.Hostname()):
		return "", reasonInvalidHost
	case !validPort(u.Host, u.Port()):
		return "", reasonInvalidPort
	}
	return u.Host, ""
}

// validHost reports whether host, the host part of hostport, is a bracketed
// IPv6 address without a zone, or an ASCII host name: dot-separated labels of
// 1 to 63 letters, digits and hyphens, neither starting nor ending with a
// hyphen, 253 bytes at most.
func validHost(hostport, host string) bool {
	if strings.HasPrefix(hostport, "[") {
		ip, err := netip.ParseAddr(host)
		return err == nil && ip.Is6() && ip.Zone() == ""
	}
	if len(host) > maxHostBytes {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > maxLabelBytes || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			if !hostByte(label[i]) {
				return false
			}
		}
	}
	return true
}

// hostByte reports whether c may appear in a host name label: an ASCII letter,
// digit or hyphen.
func hostByte(c byte) bool {
	return c == '-' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// validPort reports whether hostport ends in no port at all, or in ":port"
// with a canonical decimal port from 1 to 65535. A trailing ":" alone is
// refused.
func validPort(hostport, port string) bool {
	if port == "" {
		return !strings.HasSuffix(hostport, ":")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == port
}

// textProblem returns invalid_utf8 when s is not valid UTF-8, or "".
func textProblem(s string) string {
	if !utf8.ValidString(s) {
		return reasonInvalidUTF8
	}
	return ""
}

// scrapeURLProblem returns the reason s is not a scrape target the SDK sends,
// or "": valid UTF-8 that parses as an absolute http or https URL, the scheme
// in any case, with a host and no userinfo. It is sent exactly as given: the
// SDK applies no other policy to the target, which Serper fetches, not the
// SDK.
func scrapeURLProblem(s string) string {
	switch {
	case s == "":
		return reasonRequired
	case !utf8.ValidString(s):
		return reasonInvalidUTF8
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Opaque != "":
		return reasonInvalidURL
	case u.Scheme != "http" && u.Scheme != "https":
		return reasonUnsupportedScheme
	case u.User != nil:
		return reasonHasUserinfo
	case u.Hostname() == "":
		return reasonMissingHost
	}
	return ""
}

// asciiRange reports whether every byte of s is from lo to hi.
func asciiRange(s string, lo, hi byte) bool {
	for i := range len(s) {
		if c := s[i]; c < lo || c > hi {
			return false
		}
	}
	return true
}
