// Package awsigv4 provides small AWS Signature Version 4 crypto helpers used by
// Bedrock (messaging) and SSM (secrets) without pulling in the AWS SDK.
package awsigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

const Algo = "AWS4-HMAC-SHA256"

// EmptyPayloadHash is SHA-256 of an empty body (common for GET-style calls).
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// HMACSHA256 returns HMAC-SHA256(key, data).
func HMACSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

// HexSHA256 returns the lowercase hex SHA-256 of b. Empty input uses EmptyPayloadHash.
func HexSHA256(b []byte) string {
	if len(b) == 0 {
		return EmptyPayloadHash
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SigningKey derives the SigV4 signing key for dateStamp/region/service.
func SigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := HMACSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := HMACSHA256(kDate, region)
	kService := HMACSHA256(kRegion, service)
	return HMACSHA256(kService, "aws4_request")
}

// Sign returns hex(HMAC-SHA256(signingKey, stringToSign)).
func Sign(signingKey []byte, stringToSign string) string {
	return hex.EncodeToString(HMACSHA256(signingKey, stringToSign))
}
