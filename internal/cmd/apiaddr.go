package cmd

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// The CLI commands that talk to a running server (stop, status, reload, ui,
// code) must reach it where `vortex start` put it: GUBERN_API_LISTEN when set.
// They used to assume 127.0.0.1:9090, so after moving the API (say to
// 127.0.0.1:9191) `gubern stop` on Windows, which shuts down through the API,
// could no longer stop the server, and status/reload looked at the wrong port.

// defaultAPIPort is the management API port from GUBERN_API_LISTEN, or 9090.
func defaultAPIPort() int {
	_, port := listenHostPort()
	return port
}

// localAPIAddr is host:port for reaching the local server's API on port. The
// host is the listen host when that is a loopback address, otherwise
// 127.0.0.1 (a wildcard bind such as ":9090" or "0.0.0.0:9090" includes it,
// and the /internal endpoints accept loopback callers only).
func localAPIAddr(port int) string {
	host, _ := listenHostPort()
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// defaultAPIBase is the URL of the local server's API.
func defaultAPIBase() string { return "http://" + localAPIAddr(defaultAPIPort()) }

func listenHostPort() (string, int) {
	host, port := "127.0.0.1", 9090
	v := strings.TrimSpace(os.Getenv("GUBERN_API_LISTEN"))
	if v == "" {
		return host, port
	}
	h, p, err := net.SplitHostPort(v)
	if err != nil {
		return host, port
	}
	if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
		port = n
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		host = h
	}
	return host, port
}
