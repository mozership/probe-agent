package scenario

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// A route is where a page is, as a replay compares it: the screen, not the
// URL. A URL carries what changes between two visits to the same screen,
// such as the ID of the record a run made, a token or a timestamp, so a
// path segment or a query value that looks minted that way reads as :id.
// Every other query value is part of the screen: ?tab=notes shows other
// controls than no tab. The fragment is left out, except that an
// application that routes in it (#/notes/1) is taken by that path. A
// location on the host of the test is compared by its path, so that a
// recording follows the application to another host.

const mintedID = ":id"

var (
	uuidPattern   = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hexPattern    = regexp.MustCompile(`^(?i)[0-9a-f]{8,}$`)
	digitsPattern = regexp.MustCompile(`^\d+$`)
	// prefixedID is a record ID such as INV-2041; page-2 is a route word.
	prefixedID  = regexp.MustCompile(`^[A-Za-z]+-\d{2,}$`)
	datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?$`)
)

// mintedSegment says whether a path segment looks minted per record: a
// UUID, a run of hex or of digits, a long token with letters and two digits
// or more (companies-v2 has one), a prefixed record ID, or text with
// spaces, which no router puts in a path.
func mintedSegment(s string) bool {
	return uuidPattern.MatchString(s) || hexPattern.MatchString(s) || digitsPattern.MatchString(s) ||
		token(s, "_-") || prefixedID.MatchString(s) || strings.ContainsFunc(s, unicode.IsSpace)
}

// mintedValue says whether a query value looks minted, by the rules of a
// segment and those of the wider alphabet a value has: a signed token or a
// timestamp, and a date, which an application fills in with today.
func mintedValue(s string) bool {
	return s != "" && (mintedSegment(s) || token(s, "_-.~+/=:") || datePattern.MatchString(s))
}

// token says whether s is 12 characters or more of letters, digits and
// extra, with a letter and two digits or more.
func token(s, extra string) bool {
	if len(s) < 12 {
		return false
	}
	digits, letter := 0, false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c < 128 && unicode.IsLetter(c):
			letter = true
		case !strings.ContainsRune(extra, c):
			return false
		}
	}
	return digits >= 2 && letter
}

// routeOf returns the route of a location, as a string to compare.
func routeOf(location string) string {
	u, err := url.Parse(location)
	if err != nil || (u.IsAbs() && u.Scheme != "http" && u.Scheme != "https") || (!u.IsAbs() && !strings.HasPrefix(location, "/")) {
		return location
	}
	origin := ""
	if u.IsAbs() {
		origin = u.Scheme + "://" + u.Host
	}
	path, query := u.EscapedPath(), u.RawQuery
	if strings.HasPrefix(u.Fragment, "/") {
		fpath, fquery, _ := strings.Cut(u.Fragment, "?")
		path = fpath
		query = strings.Trim(query+"&"+fquery, "&")
	}

	var segments []string
	for _, raw := range strings.Split(path, "/") {
		if raw == "" {
			continue
		}
		s, err := url.PathUnescape(raw)
		if err != nil {
			s = raw
		}
		if mintedSegment(s) {
			s = mintedID
		}
		segments = append(segments, s)
	}
	var terms []string
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		k, _ = url.QueryUnescape(k)
		v, _ = url.QueryUnescape(v)
		if mintedValue(k) {
			k = mintedID
		}
		if mintedValue(v) {
			v = mintedID
		}
		terms = append(terms, k+"="+v)
	}
	slices.Sort(terms)
	b, _ := json.Marshal([]any{origin, segments, terms})
	return string(b)
}

// sameRoute says whether two locations are the same screen.
func sameRoute(a, b string) bool {
	return routeOf(a) == routeOf(b)
}
