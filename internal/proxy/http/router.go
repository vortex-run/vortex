package proxyhttp

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Route is a registered routing rule.
type Route struct {
	Pattern string
	Handler http.Handler
}

// parsedRoute is a Route split into host and path components for matching.
type parsedRoute struct {
	route      Route
	host       string // "" for path-only; may be "*.suffix" for wildcard
	pathPrefix string // "" means host-only (no path constraint)
	exactPath  bool   // true if the pattern path had no trailing /*
}

// Router dispatches requests to handlers using a fixed six-tier priority order
// (most specific first):
//
//  1. exact host + exact path
//  2. exact host + path prefix
//  3. exact host (no path constraint)
//  4. wildcard host + path prefix
//  5. wildcard host (no path constraint)
//  6. path prefix only (any host)
//
// The first matching tier wins. With no match it returns 404 with a JSON body.
type Router struct {
	tier1, tier2, tier3 []parsedRoute // exact host: path / prefix / host-only
	tier4, tier5        []parsedRoute // wildcard host: prefix / host-only
	tier6               []parsedRoute // path-only
	all                 []Route
}

// NewRouter returns an empty Router.
func NewRouter() *Router { return &Router{} }

// Handle registers handler under pattern. Pattern forms:
//
//	"host/path"     exact host + exact path
//	"host/path/*"   exact host + path prefix
//	"host"          exact host
//	"*.suffix/..."  wildcard host (with optional path/prefix)
//	"/path" "/path/*" path-only (any host)
func (r *Router) Handle(pattern string, handler http.Handler) {
	pr := parsePattern(pattern, handler)
	switch {
	case pr.host == "": // path-only
		r.tier6 = append(r.tier6, pr)
	case isWildcardHost(pr.host):
		if pr.pathPrefix != "" {
			r.tier4 = append(r.tier4, pr)
		} else {
			r.tier5 = append(r.tier5, pr)
		}
	default: // exact host
		switch {
		case pr.pathPrefix != "" && pr.exactPath:
			r.tier1 = append(r.tier1, pr)
		case pr.pathPrefix != "":
			r.tier2 = append(r.tier2, pr)
		default:
			r.tier3 = append(r.tier3, pr)
		}
	}
	r.all = append(r.all, pr.route)
}

// parsePattern splits a pattern into host/path components.
func parsePattern(pattern string, handler http.Handler) parsedRoute {
	pr := parsedRoute{route: Route{Pattern: pattern, Handler: handler}}

	if strings.HasPrefix(pattern, "/") {
		// Path-only.
		pr.pathPrefix, pr.exactPath = parsePath(pattern)
		pr.host = ""
		return pr
	}

	host := pattern
	path := ""
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		host = pattern[:i]
		path = pattern[i:]
	}
	pr.host = strings.ToLower(host)
	if path != "" {
		pr.pathPrefix, pr.exactPath = parsePath(path)
	}
	return pr
}

// parsePath returns the comparable path prefix and whether the match is exact
// (no trailing "/*"). For "/v2/*" it returns ("/v2", false); for "/v2/users" it
// returns ("/v2/users", true).
func parsePath(path string) (prefix string, exact bool) {
	if strings.HasSuffix(path, "/*") {
		return strings.TrimSuffix(path, "/*"), false
	}
	if path == "/*" {
		return "", false
	}
	return path, true
}

func isWildcardHost(host string) bool { return strings.HasPrefix(host, "*.") }

// ServeHTTP routes req to the first matching handler by tier, or replies 404.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host := hostOnly(req.Host)
	path := req.URL.Path

	tiers := [][]parsedRoute{r.tier1, r.tier2, r.tier3, r.tier4, r.tier5, r.tier6}
	for _, tier := range tiers {
		for _, pr := range tier {
			if pr.matches(host, path) {
				pr.route.Handler.ServeHTTP(w, req)
				return
			}
		}
	}
	r.notFound(w, host, path)
}

// matches reports whether this parsed route matches the request host/path.
func (pr parsedRoute) matches(host, path string) bool {
	if !pr.hostMatches(host) {
		return false
	}
	if pr.pathPrefix == "" {
		// Host-only route, or "/*" path-only: matches any path.
		return true
	}
	if pr.exactPath {
		return path == pr.pathPrefix
	}
	return path == pr.pathPrefix || strings.HasPrefix(path, pr.pathPrefix+"/")
}

// hostMatches handles exact, wildcard, and path-only (empty host) matching.
func (pr parsedRoute) hostMatches(host string) bool {
	switch {
	case pr.host == "":
		return true // path-only matches any host
	case isWildcardHost(pr.host):
		suffix := pr.host[1:] // ".suffix"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	default:
		return host == pr.host
	}
}

func (r *Router) notFound(w http.ResponseWriter, host, path string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "no route",
		"host":  host,
		"path":  path,
	})
}

// Routes returns all registered routes (for stats/dashboard).
func (r *Router) Routes() []Route {
	out := make([]Route, len(r.all))
	copy(out, r.all)
	return out
}

// hostOnly strips any port from a Host header value and lowercases it.
func hostOnly(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(host)
}
