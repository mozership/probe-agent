package scenario

import (
	"net/url"
	"strings"
)

// hosts are the hosts the agent may open pages of.
type hosts []string

// allows reports whether the agent may open raw. A page of no host, such
// as about:blank, is allowed. An entry is a host, a host and port, or
// *.domain for every host under domain.
func (h hosts) allows(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return u.Scheme == "about"
	}
	for _, e := range h {
		e = strings.ToLower(e)
		host, name := strings.ToLower(u.Host), strings.ToLower(u.Hostname())
		switch {
		case e == host || e == name:
			return true
		case strings.HasPrefix(e, "*.") && strings.HasSuffix(name, e[1:]):
			return true
		}
	}
	return false
}
