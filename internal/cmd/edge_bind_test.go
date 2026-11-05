package cmd

import "testing"

// TestEdgeLocalAddr: in-process probes of a route follow
// GUBERN_EDGE_BIND_HOST, falling back to loopback when the edge binds every
// interface.
func TestEdgeLocalAddr(t *testing.T) {
	cases := map[string]string{
		"":          "127.0.0.1:8443",
		"0.0.0.0":   "127.0.0.1:8443",
		"::":        "127.0.0.1:8443",
		"10.0.0.5":  "10.0.0.5:8443",
		"127.0.0.1": "127.0.0.1:8443",
		"fd00::5":   "[fd00::5]:8443",
		" 10.0.0.5": "10.0.0.5:8443",
	}
	for host, want := range cases {
		t.Setenv("GUBERN_EDGE_BIND_HOST", host)
		if got := edgeLocalAddr(8443); got != want {
			t.Errorf("GUBERN_EDGE_BIND_HOST=%q: edgeLocalAddr = %q, want %q", host, got, want)
		}
	}
}
