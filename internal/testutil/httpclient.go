package testutil

import (
	"net/http"
	"time"
)

// HTTPTimeout bounds every request made through Client. It is generous enough
// for a cold-started server on a loaded CI runner, and short enough that a
// genuinely stuck request fails its own test long before the suite's overall
// deadline.
const HTTPTimeout = 30 * time.Second

// Client returns an HTTP client that cannot hang.
//
// http.Get and http.DefaultClient have NO timeout, so a request that connects
// but never completes blocks forever. In a test that means the whole package
// runs out of time and Go prints `panic: test timed out` with a dump of every
// goroutine — which names the line that was waiting but not the assertion that
// failed, and takes the rest of the suite down with it. A per-request timeout
// keeps a stuck call local: one test fails, with its own name, and the others
// still run.
//
// Prefer this over http.Get/http.Post in integration tests.
func Client() *http.Client {
	return &http.Client{Timeout: HTTPTimeout}
}
