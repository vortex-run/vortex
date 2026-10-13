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
