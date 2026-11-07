package devops

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// The DevOps agent takes package names, domains and upstreams straight from a
// user's (or an AI's) message. They end up in shell commands on remote hosts,
// in remote file paths (/etc/nginx/sites-*/<domain>) and inside nginx config,
// so they are validated against what they're supposed to be before anything
// is shown for approval or sent to a host. Shell quoting alone stops command
// injection but not path traversal ("../../passwd") or nginx directive
// injection, and an approval prompt that reads "install package: nginx; curl
// … | sh" is easy to misread.

// packageNameRE matches one Debian, RPM or Alpine package spec: a name that
// starts with an alphanumeric (so it can't be an option like -o), optionally
// with a version (=1.2-3, ~ allowed) or architecture (:amd64).
var packageNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_~:=-]{0,127}$`)

// hostLabelRE matches one DNS label.
var hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// packageList splits a space-separated package list and validates each entry.
func packageList(s string) ([]string, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("devops: no package name given")
	}
	for _, f := range fields {
		if !packageNameRE.MatchString(f) {
			return nil, fmt.Errorf("devops: %q is not a valid package name", f)
		}
	}
	return fields, nil
}

// validDomain reports whether s is a hostname nginx and certbot can use as a
// site name: DNS labels separated by dots, optionally with a leading "*."
// wildcard. It rejects paths, "..", whitespace and anything else that could
// escape /etc/nginx/sites-*/ or the server_name directive.
func validDomain(s string) error {
	h := strings.TrimPrefix(s, "*.")
	if h == "" || len(s) > 253 {
		return fmt.Errorf("devops: %q is not a valid domain", s)
	}
	for _, label := range strings.Split(h, ".") {
		if !hostLabelRE.MatchString(label) {
			return fmt.Errorf("devops: %q is not a valid domain", s)
		}
	}
	return nil
}

// validUpstream reports whether s is a plain http(s)://host[:port] proxy
// target with nothing that could close or add an nginx directive.
func validUpstream(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("devops: upstream %q must be http(s)://host[:port]", s)
	}
	host, port := u.Hostname(), u.Port()
	if net.ParseIP(host) == nil && validDomain(host) != nil {
		return fmt.Errorf("devops: upstream %q has an invalid host", s)
	}
	if port != "" && strings.Trim(port, "0123456789") != "" {
		return fmt.Errorf("devops: upstream %q has an invalid port", s)
	}
	return nil
}
