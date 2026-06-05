package secrets

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	gcpTimeout    = 10 * time.Second
	gcpAPIBase    = "https://secretmanager.googleapis.com/v1"
	gcpTokenURL   = "https://oauth2.googleapis.com/token"
	gcpScope      = "https://www.googleapis.com/auth/cloud-platform"
	gcpMetaToken  = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	gcpTokenGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// GCPSMAdapter is a secret Adapter backed by GCP Secret Manager's REST API,
// implemented directly over net/http (no google.golang.org SDK). Auth is via an
// OAuth2 access token obtained either from a service-account JSON (signed JWT
// exchanged for a token) or, when no credentials file is given, the GCE
// metadata server.
type GCPSMAdapter struct {
	cfg     GCPSMConfig
	client  *http.Client
	creds   *gcpServiceAccount // nil → use metadata server
	apiBase string             // overridable in tests
	tokURL  string             // token-exchange URL; overridable in tests

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// gcpServiceAccount is the subset of a GCP service-account JSON we need.
type gcpServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	key         *rsa.PrivateKey
}

// NewGCPSMAdapter builds a GCPSMAdapter. ProjectID is required; Prefix defaults
// to "vortex-". If CredFile is set it is loaded as a service-account JSON;
// otherwise the GCE metadata server is used for tokens.
func NewGCPSMAdapter(cfg GCPSMConfig) (Adapter, error) {
	if cfg.ProjectID == "" {
		return nil, errors.New("secrets: gcp-sm adapter requires a ProjectID")
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "vortex-"
	}
	a := &GCPSMAdapter{
		cfg:     cfg,
		client:  &http.Client{Timeout: gcpTimeout},
		apiBase: gcpAPIBase,
		tokURL:  gcpTokenURL,
	}
	if cfg.CredFile != "" {
		sa, err := loadGCPServiceAccount(cfg.CredFile)
		if err != nil {
			return nil, err
		}
		a.creds = sa
		if sa.TokenURI != "" {
			a.tokURL = sa.TokenURI
		}
	}
	return a, nil
}

// loadGCPServiceAccount parses a service-account JSON file and its RSA key.
func loadGCPServiceAccount(path string) (*gcpServiceAccount, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied config
	if err != nil {
		return nil, fmt.Errorf("secrets: reading gcp credentials: %w", err)
	}
	var sa gcpServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("secrets: parsing gcp credentials: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("secrets: gcp credentials missing client_email or private_key")
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	sa.key = key
	return &sa, nil
}

// parseRSAPrivateKey decodes a PEM PKCS#8 (or PKCS#1) RSA private key.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("secrets: gcp private_key is not valid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("secrets: parsing gcp private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("secrets: gcp private key is not RSA")
	}
	return rsaKey, nil
}

// accessToken returns a cached OAuth2 token, refreshing when within 60s of
// expiry.
func (a *GCPSMAdapter) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.tokenExp.Add(-60*time.Second)) {
		return a.token, nil
	}
	var (
		tok string
		ttl time.Duration
		err error
	)
	if a.creds != nil {
		tok, ttl, err = a.fetchTokenFromJWT(ctx)
	} else {
		tok, ttl, err = a.fetchTokenFromMetadata(ctx)
	}
	if err != nil {
		return "", err
	}
	a.token = tok
	a.tokenExp = time.Now().Add(ttl)
	return tok, nil
}

// fetchTokenFromJWT builds a signed JWT assertion and exchanges it for an access
// token at the token endpoint.
func (a *GCPSMAdapter) fetchTokenFromJWT(ctx context.Context) (string, time.Duration, error) {
	assertion, err := a.signJWT()
	if err != nil {
		return "", 0, err
	}
	form := url.Values{
		"grant_type": {gcpTokenGrant},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.parseTokenResponse(req)
}

// fetchTokenFromMetadata gets a token from the GCE metadata server.
func (a *GCPSMAdapter) fetchTokenFromMetadata(ctx context.Context) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gcpMetaToken, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	return a.parseTokenResponse(req)
}

// parseTokenResponse executes req and reads {access_token, expires_in}.
func (a *GCPSMAdapter) parseTokenResponse(req *http.Request) (string, time.Duration, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("secrets: gcp token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("secrets: gcp token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("secrets: decoding gcp token: %w", err)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl == 0 {
		ttl = time.Hour
	}
	return out.AccessToken, ttl, nil
}

// signJWT builds and RS256-signs the service-account JWT assertion.
func (a *GCPSMAdapter) signJWT() (string, error) {
	now := time.Now()
	header := b64url(`{"alg":"RS256","typ":"JWT"}`)
	claims := map[string]any{
		"iss":   a.creds.ClientEmail,
		"scope": gcpScope,
		"aud":   a.tokURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := header + "." + b64urlBytes(cb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.creds.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("secrets: signing gcp jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// do issues an authenticated Secret Manager API request.
func (a *GCPSMAdapter) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	tok, err := a.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	return a.client.Do(req)
}

// secretID applies the prefix to a logical name.
func (a *GCPSMAdapter) secretID(name string) string { return a.cfg.Prefix + name }

// Get returns the latest enabled version's payload for name.
func (a *GCPSMAdapter) Get(ctx context.Context, name string) (string, error) {
	u := fmt.Sprintf("%s/projects/%s/secrets/%s/versions/latest:access",
		a.apiBase, a.cfg.ProjectID, a.secretID(name))
	resp, err := a.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: gcp get %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("secrets: gcp get %s returned %s", name, resp.Status)
	}
	var out struct {
		Payload struct {
			Data string `json:"data"` // base64-encoded
		} `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("secrets: decoding gcp response: %w", err)
	}
	dec, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("secrets: decoding gcp payload: %w", err)
	}
	return string(dec), nil
}

// Set creates the secret (ignoring AlreadyExists) and adds a new version with
// the base64-encoded value as payload.
func (a *GCPSMAdapter) Set(ctx context.Context, name, value string) error {
	// Create the secret container; 409 AlreadyExists is fine.
	createURL := fmt.Sprintf("%s/projects/%s/secrets?secretId=%s",
		a.apiBase, a.cfg.ProjectID, a.secretID(name))
	cresp, err := a.do(ctx, http.MethodPost, createURL,
		map[string]any{"replication": map[string]any{"automatic": map[string]any{}}})
	if err != nil {
		return fmt.Errorf("secrets: gcp create %s: %w", name, err)
	}
	_ = cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK && cresp.StatusCode != http.StatusConflict {
		return fmt.Errorf("secrets: gcp create %s returned %s", name, cresp.Status)
	}

	// Add a version with the payload.
	addURL := fmt.Sprintf("%s/projects/%s/secrets/%s:addVersion",
		a.apiBase, a.cfg.ProjectID, a.secretID(name))
	payload := base64.StdEncoding.EncodeToString([]byte(value))
	resp, err := a.do(ctx, http.MethodPost, addURL,
		map[string]any{"payload": map[string]any{"data": payload}})
	if err != nil {
		return fmt.Errorf("secrets: gcp addVersion %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("secrets: gcp addVersion %s returned %s", name, resp.Status)
	}
	return nil
}

// List returns secret names under the prefix (with the prefix stripped).
func (a *GCPSMAdapter) List(ctx context.Context) ([]string, error) {
	u := fmt.Sprintf("%s/projects/%s/secrets", a.apiBase, a.cfg.ProjectID)
	resp, err := a.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcp list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("secrets: gcp list returned %s", resp.Status)
	}
	var out struct {
		Secrets []struct {
			Name string `json:"name"` // projects/<p>/secrets/<id>
		} `json:"secrets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("secrets: decoding gcp list: %w", err)
	}
	names := make([]string, 0, len(out.Secrets))
	for _, s := range out.Secrets {
		id := s.Name
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if strings.HasPrefix(id, a.cfg.Prefix) {
			names = append(names, strings.TrimPrefix(id, a.cfg.Prefix))
		}
	}
	return names, nil
}

// Delete removes the secret and all its versions. It is idempotent (404 is not
// an error).
func (a *GCPSMAdapter) Delete(ctx context.Context, name string) error {
	u := fmt.Sprintf("%s/projects/%s/secrets/%s", a.apiBase, a.cfg.ProjectID, a.secretID(name))
	resp, err := a.do(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("secrets: gcp delete %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("secrets: gcp delete %s returned %s", name, resp.Status)
	}
	return nil
}

// Ping confirms connectivity by obtaining a token and listing secrets.
func (a *GCPSMAdapter) Ping(ctx context.Context) error {
	if _, err := a.List(ctx); err != nil {
		return fmt.Errorf("secrets: gcp ping: %w", err)
	}
	return nil
}

func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func b64urlBytes(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
