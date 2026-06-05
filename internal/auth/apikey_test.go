package auth

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAPIKey_IssueReturnsIDAndSecret(t *testing.T) {
	s := NewAPIKeyStore()
	key, secret, err := s.Issue("u1", "o1", []Role{RoleViewer}, "ci token", 0)
	if err != nil {
		t.Fatal(err)
	}
	if key.ID == "" {
		t.Error("issued key has empty ID")
	}
	if secret == "" {
		t.Error("issued secret is empty")
	}
	if len(key.ID) != 16 {
		t.Errorf("key ID len = %d, want 16 hex chars", len(key.ID))
	}
}

func TestAPIKey_SecretNotStored(t *testing.T) {
	s := NewAPIKeyStore()
	key, secret, _ := s.Issue("u1", "o1", nil, "", 0)
	// The stored key must hold only a bcrypt hash, never the plaintext secret.
	if key.Hash == "" {
		t.Error("stored key should have a hash")
	}
	if key.Hash == secret {
		t.Error("hash must not equal the plaintext secret")
	}
}

func TestAPIKey_VerifySucceeds(t *testing.T) {
	s := NewAPIKeyStore()
	_, secret, _ := s.Issue("u1", "o1", []Role{RoleAdmin}, "", 0)
	key, err := s.Verify(secret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if key.UserID != "u1" || len(key.Roles) != 1 || key.Roles[0] != RoleAdmin {
		t.Errorf("verified key = %+v, want user u1 admin", key)
	}
}

func TestAPIKey_VerifyUnknownSecret(t *testing.T) {
	s := NewAPIKeyStore()
	_, _, _ = s.Issue("u1", "o1", nil, "", 0)
	if _, err := s.Verify("deadbeef.notreal"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Verify unknown = %v, want ErrNotFound", err)
	}
	if _, err := s.Verify("garbage-without-dot"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Verify malformed = %v, want ErrNotFound", err)
	}
}

func TestAPIKey_VerifyExpired(t *testing.T) {
	s := NewAPIKeyStore()
	_, secret, _ := s.Issue("u1", "o1", nil, "", time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if _, err := s.Verify(secret); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify expired = %v, want ErrExpired", err)
	}
}

func TestAPIKey_RevokeRemoves(t *testing.T) {
	s := NewAPIKeyStore()
	key, secret, _ := s.Issue("u1", "o1", nil, "", 0)
	if _, err := s.Verify(secret); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("Verify after revoke = %v, want ErrNotFound", err)
	}
}

func TestAPIKey_ListScopedToOrg(t *testing.T) {
	s := NewAPIKeyStore()
	_, _, _ = s.Issue("u1", "orgA", nil, "a1", 0)
	_, _, _ = s.Issue("u2", "orgA", nil, "a2", 0)
	_, _, _ = s.Issue("u3", "orgB", nil, "b1", 0)

	a := s.List("orgA")
	if len(a) != 2 {
		t.Errorf("orgA keys = %d, want 2", len(a))
	}
	for _, k := range a {
		if k.Hash != "" {
			t.Error("List must redact the hash")
		}
	}
	if len(s.List("orgB")) != 1 {
		t.Errorf("orgB keys = %d, want 1", len(s.List("orgB")))
	}
}

func TestAPIKey_SaveLoadRoundTrip(t *testing.T) {
	s := NewAPIKeyStore()
	_, secret, _ := s.Issue("u1", "o1", []Role{RoleOperator}, "persisted", 0)
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	s2 := NewAPIKeyStore()
	if err := s2.Load(path); err != nil {
		t.Fatal(err)
	}
	// The reloaded store must verify the same secret.
	key, err := s2.Verify(secret)
	if err != nil {
		t.Fatalf("Verify after reload: %v", err)
	}
	if key.Description != "persisted" || key.Roles[0] != RoleOperator {
		t.Errorf("reloaded key = %+v", key)
	}
}

func TestAPIKey_LoadMissingFileIsEmpty(t *testing.T) {
	s := NewAPIKeyStore()
	if err := s.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Errorf("Load missing file = %v, want nil", err)
	}
}

func TestAPIKey_ConcurrentIssueNoRace(t *testing.T) {
	s := NewAPIKeyStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Issue("u", "o", nil, "", 0); err != nil {
				t.Errorf("concurrent Issue: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(s.List("o")); got != 50 {
		t.Errorf("issued %d keys, want 50", got)
	}
}
