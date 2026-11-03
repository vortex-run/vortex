package a2a

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

// TokenHeader carries the shared A2A token on every request.
const TokenHeader = "X-A2A-Token"

// NewToken returns a random 256-bit token for RequireToken.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// RequireToken guards an A2A handler tree with a shared token. The /a2a/
// routes sit outside API-key auth, and RPC submits tasks to specialist agents
// that write files through the trusted (approval-free) tool registry, so the
// tree must not be reachable by whoever can reach the API port. The
// coordinator and specialists share one process with the server, so a
// per-process random token passed to their AgentClient authenticates them
// without trusting the network position of the caller (which a same-host
// reverse proxy would spoof). An empty token rejects everything.
func RequireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(TokenHeader)
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "a2a: missing or invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
