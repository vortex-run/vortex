package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *SecretStore {
	t.Helper()
	s, err := NewSecretStore(filepath.Join(t.TempDir(), "secrets"), []byte("test-secret-key"))
	if err != nil {
		t.Fatalf("NewSecretStore: %v", err)
	}
	return s
}

func TestStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "secrets")
	if _, err := NewSecretStore(dir, []byte("k")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("store directory not created: %v", err)
	}
}

func TestStore_EmptyKeyError(t *testing.T) {
	if _, err := NewSecretStore(t.TempDir(), nil); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestStore_SetGetRoundTrip(t *testing.T) {
	s := newStore(t)
	if err := s.Set("DB_PASSWORD", "s3cr3t-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get("DB_PASSWORD")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cr3t-value" {
		t.Errorf("got %q, want s3cr3t-value", got)
	}
}

func TestStore_GetNotExist(t *testing.T) {
	s := newStore(t)
	if _, err := s.Get("MISSING"); !os.IsNotExist(err) {
		t.Errorf("Get unknown secret err = %v, want os.ErrNotExist", err)
	}
}

func TestStore_FileIsNotPlaintext(t *testing.T) {
	s := newStore(t)
	value := "PLAINTEXT_SHOULD_NOT_APPEAR"
	if err := s.Set("API_KEY", value); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.fileFor("API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), value) {
		t.Error("stored file contains the plaintext value (not encrypted)")
	}
}

func TestStore_DifferentKeysDifferentCiphertext(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "s1")
	dir2 := filepath.Join(t.TempDir(), "s2")
	s1, _ := NewSecretStore(dir1, []byte("key-one"))
	s2, _ := NewSecretStore(dir2, []byte("key-two"))
	_ = s1.Set("K", "same-value")
	_ = s2.Set("K", "same-value")

	raw1, _ := os.ReadFile(s1.fileFor("K"))
	raw2, _ := os.ReadFile(s2.fileFor("K"))
	if string(raw1) == string(raw2) {
		t.Error("same value under different keys should produce different ciphertext")
	}
}

func TestStore_DeleteRemoves(t *testing.T) {
	s := newStore(t)
	_ = s.Set("TMP", "v")
	if err := s.Delete("TMP"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("TMP"); !os.IsNotExist(err) {
		t.Errorf("Get after Delete err = %v, want ErrNotExist", err)
	}
}

func TestStore_DeleteIdempotent(t *testing.T) {
	s := newStore(t)
	if err := s.Delete("NEVER_SET"); err != nil {
		t.Errorf("Delete of missing secret should be nil, got %v", err)
	}
}

func TestStore_List(t *testing.T) {
	s := newStore(t)
	_ = s.Set("A", "1")
	_ = s.Set("B", "2")
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range list {
		got[n] = true
	}
	if !got["A"] || !got["B"] {
		t.Errorf("List = %v, want A and B", list)
	}
}

func TestStore_ListEmpty(t *testing.T) {
	s := newStore(t)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("List on empty store = %v, want empty", list)
	}
}

func TestStore_ExistsTrue(t *testing.T) {
	s := newStore(t)
	_ = s.Set("PRESENT", "v")
	ok, err := s.Exists("PRESENT")
	if err != nil || !ok {
		t.Errorf("Exists = %v, %v; want true, nil", ok, err)
	}
}

func TestStore_ExistsFalse(t *testing.T) {
	s := newStore(t)
	ok, err := s.Exists("ABSENT")
	if err != nil || ok {
		t.Errorf("Exists = %v, %v; want false, nil", ok, err)
	}
}

func TestStore_InvalidNameError(t *testing.T) {
	s := newStore(t)
	if err := s.Set("my-secret", "value"); err == nil {
		t.Error("expected error for name with a hyphen")
	}
	if err := s.Set("with space", "value"); err == nil {
		t.Error("expected error for name with a space")
	}
}

func TestStore_Overwrite(t *testing.T) {
	s := newStore(t)
	_ = s.Set("K", "old")
	if err := s.Set("K", "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("K")
	if got != "new" {
		t.Errorf("after overwrite got %q, want new", got)
	}
}
