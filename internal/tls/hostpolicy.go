package vtls

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// ErrHostNotConfigured is returned for a TLS handshake whose SNI names no
// configured route host.
var ErrHostNotConfigured = errors.New("vtls: host not configured")

// maxPersistedLeaves caps how many leaf certificates the internal CA persists
// when no host allow-list is known (path-only routes accept any SNI). Past the
// cap, leaves for new names live only in a bounded in-memory cache, so an
// anonymous client cycling SNI values cannot grow the store without bound
// (audit F-16).
const maxPersistedLeaves = 64

// maxEphemeralLeaves bounds the in-memory cache of non-persisted leaves.
const maxEphemeralLeaves = 256

// canonicalHost returns the one form a TLS server name is looked up, issued
// and stored under: IDNA ToASCII, lowercased, trailing dot removed. Case and
// Unicode-compatibility variants of a name (EXAMPLE.com, fullwidth ｅxample.com)
// map to the same key instead of separate store files (audit F-16).
func canonicalHost(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return "", nil
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil {
		return "", fmt.Errorf("vtls: invalid server name %q: %w", name, err)
	}
	return strings.ToLower(ascii), nil
}

// hostAllowList holds the canonical route hosts a certificate may be served
// for. A nil list means "unrestricted" (some route accepts any host).
type hostAllowList struct {
	exact    map[string]bool
	suffixes []string // ".example.com" for "*.example.com"
}

// newHostAllowList canonicalises hosts. It returns nil (unrestricted) for an
// empty list.
func newHostAllowList(hosts []string) (*hostAllowList, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	l := &hostAllowList{exact: map[string]bool{}}
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h == "" {
			continue
		}
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
		if strings.HasPrefix(h, "*.") {
			c, err := canonicalHost(h[2:])
			if err != nil {
				return nil, err
			}
			l.suffixes = append(l.suffixes, "."+c)
			continue
		}
		c, err := canonicalHost(h)
		if err != nil {
			return nil, err
		}
		l.exact[c] = true
	}
	return l, nil
}

// allows reports whether canonical name may be served. Wildcards match like
// the HTTP router: "*.example.com" matches any name ending in ".example.com".
func (l *hostAllowList) allows(name string) bool {
	if l == nil {
		return true
	}
	if l.exact[name] {
		return true
	}
	for _, s := range l.suffixes {
		if strings.HasSuffix(name, s) && len(name) > len(s) {
			return true
		}
	}
	return false
}

// leafCache is a small bounded in-memory map of non-persisted leaves.
type leafCache struct {
	mu sync.Mutex
	m  map[string]*tls.Certificate
}

// get returns a cached leaf that is still valid for at least renewBefore.
func (c *leafCache) get(name string, renewBefore time.Duration) *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	cert := c.m[name]
	if cert == nil || cert.Leaf == nil || time.Until(cert.Leaf.NotAfter) < renewBefore {
		return nil
	}
	return cert
}

// put stores a leaf, evicting an arbitrary entry when full.
func (c *leafCache) put(name string, cert *tls.Certificate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]*tls.Certificate)
	}
	if _, ok := c.m[name]; !ok && len(c.m) >= maxEphemeralLeaves {
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[name] = cert
}
