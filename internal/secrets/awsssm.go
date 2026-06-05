package secrets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	ssmTimeout  = 10 * time.Second
	ssmService  = "ssm"
	awsAlgo     = "AWS4-HMAC-SHA256"
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// AWSSSMAdapter is a secret Adapter backed by AWS SSM Parameter Store, talking
// to the JSON-1.1 API directly with a minimal SigV4 signer (no AWS SDK).
type AWSSSMAdapter struct {
	cfg      AWSSSMConfig
	client   *http.Client
	endpoint string // https://ssm.<region>.amazonaws.com/
	host     string // ssm.<region>.amazonaws.com
}

// NewAWSSSMAdapter builds an AWSSSMAdapter. Region is required; Prefix defaults
// to "/vortex/".
func NewAWSSSMAdapter(cfg AWSSSMConfig) (Adapter, error) {
	if cfg.Region == "" {
		return nil, errors.New("secrets: aws-ssm adapter requires a Region")
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "/vortex/"
	}
	host := "ssm." + cfg.Region + ".amazonaws.com"
	return &AWSSSMAdapter{
		cfg:      cfg,
		client:   &http.Client{Timeout: ssmTimeout},
		endpoint: "https://" + host + "/",
		host:     host,
	}, nil
}

// call invokes an SSM API target with a JSON body and returns the raw response
// and status code.
func (a *AWSSSMAdapter) call(ctx context.Context, target string, body any) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	req.Host = a.host

	if err := sigV4Sign(req, payload, a.cfg.AccessKey, a.cfg.SecretKey, a.cfg.Region, ssmService, time.Now().UTC()); err != nil {
		return nil, 0, err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// isParameterNotFound reports whether an SSM error body indicates the parameter
// does not exist.
func isParameterNotFound(raw []byte) bool {
	return strings.Contains(string(raw), "ParameterNotFound")
}

// Get returns the value of the named parameter (with the prefix applied).
func (a *AWSSSMAdapter) Get(ctx context.Context, name string) (string, error) {
	raw, code, err := a.call(ctx, "AmazonSSM.GetParameter",
		map[string]any{"Name": a.cfg.Prefix + name, "WithDecryption": true})
	if err != nil {
		return "", fmt.Errorf("secrets: ssm get %s: %w", name, err)
	}
	if code != http.StatusOK {
		if isParameterNotFound(raw) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("secrets: ssm get %s: %s", name, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Parameter struct {
			Value string `json:"Value"`
		} `json:"Parameter"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("secrets: decoding ssm response: %w", err)
	}
	return out.Parameter.Value, nil
}

// Set stores value as a SecureString parameter, overwriting any existing one.
func (a *AWSSSMAdapter) Set(ctx context.Context, name, value string) error {
	raw, code, err := a.call(ctx, "AmazonSSM.PutParameter", map[string]any{
		"Name": a.cfg.Prefix + name, "Value": value, "Type": "SecureString", "Overwrite": true,
	})
	if err != nil {
		return fmt.Errorf("secrets: ssm set %s: %w", name, err)
	}
	if code != http.StatusOK {
		return fmt.Errorf("secrets: ssm set %s: %s", name, strings.TrimSpace(string(raw)))
	}
	return nil
}

// List returns parameter names under the prefix (with the prefix stripped).
func (a *AWSSSMAdapter) List(ctx context.Context) ([]string, error) {
	raw, code, err := a.call(ctx, "AmazonSSM.GetParametersByPath",
		map[string]any{"Path": a.cfg.Prefix, "Recursive": false, "WithDecryption": false})
	if err != nil {
		return nil, fmt.Errorf("secrets: ssm list: %w", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("secrets: ssm list: %s", strings.TrimSpace(string(raw)))
	}
	var out struct {
		Parameters []struct {
			Name string `json:"Name"`
		} `json:"Parameters"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("secrets: decoding ssm list: %w", err)
	}
	names := make([]string, 0, len(out.Parameters))
	for _, p := range out.Parameters {
		names = append(names, strings.TrimPrefix(p.Name, a.cfg.Prefix))
	}
	return names, nil
}

// Delete removes the named parameter. It is idempotent (ParameterNotFound is
// not an error).
func (a *AWSSSMAdapter) Delete(ctx context.Context, name string) error {
	raw, code, err := a.call(ctx, "AmazonSSM.DeleteParameter",
		map[string]any{"Name": a.cfg.Prefix + name})
	if err != nil {
		return fmt.Errorf("secrets: ssm delete %s: %w", name, err)
	}
	if code != http.StatusOK {
		if isParameterNotFound(raw) {
			return nil
		}
		return fmt.Errorf("secrets: ssm delete %s: %s", name, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Ping performs a lightweight authenticated call to confirm connectivity. Any
// HTTP response (even an auth/permission error) proves the endpoint is
// reachable; only a network failure is treated as unreachable.
func (a *AWSSSMAdapter) Ping(ctx context.Context) error {
	_, _, err := a.call(ctx, "AmazonSSM.ListTagsForResource",
		map[string]any{"ResourceType": "Parameter", "ResourceId": "/"})
	if err != nil {
		return fmt.Errorf("secrets: ssm ping: %w", err)
	}
	return nil
}

// sigV4Sign signs req with AWS Signature Version 4 (HMAC-SHA256), adding the
// X-Amz-Date and Authorization headers. payload is the exact request body.
func sigV4Sign(req *http.Request, payload []byte, accessKey, secretKey, region, service string, now time.Time) error {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)

	payloadHash := hexSHA256(payload)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	// Canonical request. We sign host, x-amz-date, and x-amz-target.
	target := req.Header.Get("X-Amz-Target")
	signedHeaders := "host;x-amz-date;x-amz-target"
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-date:" + amzDate + "\n" +
		"x-amz-target:" + target + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		"/",
		"", // empty query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign.
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		awsAlgo,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	// Signing key and signature.
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	auth := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgo, accessKey, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return nil
}

func hexSHA256(b []byte) string {
	if len(b) == 0 {
		return emptySHA256
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
