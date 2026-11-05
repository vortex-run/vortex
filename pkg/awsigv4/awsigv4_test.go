package awsigv4

import (
	"encoding/hex"
	"testing"
)

func TestHexSHA256Empty(t *testing.T) {
	if got := HexSHA256(nil); got != EmptyPayloadHash {
		t.Fatalf("nil: got %s", got)
	}
	if got := HexSHA256([]byte{}); got != EmptyPayloadHash {
		t.Fatalf("empty: got %s", got)
	}
}

func TestSigningKeyDeterministic(t *testing.T) {
	a := SigningKey("secret", "20260101", "us-east-1", "ssm")
	b := SigningKey("secret", "20260101", "us-east-1", "ssm")
	if hex.EncodeToString(a) != hex.EncodeToString(b) {
		t.Fatal("SigningKey not deterministic")
	}
	sig := Sign(a, "string-to-sign")
	if len(sig) != 64 {
		t.Fatalf("signature hex len = %d", len(sig))
	}
}

func TestURIEncode(t *testing.T) {
	cases := map[string]string{
		"anthropic.claude-3-5-sonnet-20240620-v1:0": "anthropic.claude-3-5-sonnet-20240620-v1%3A0",
		"a b/c~d_e":       "a%20b%2Fc~d_e",
		"ሴ":               "%E1%88%B4",
		URIEncode("v1:0"): "v1%253A0", // second pass, as non-S3 canonical URIs need
	}
	for in, want := range cases {
		if got := URIEncode(in); got != want {
			t.Errorf("URIEncode(%q) = %q, want %q", in, got, want)
		}
	}
}
