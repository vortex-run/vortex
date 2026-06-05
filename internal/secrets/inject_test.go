package secrets

import (
	"slices"
	"strings"
	"testing"
)

func TestResolve_AllPresent(t *testing.T) {
	s := newStore(t)
	_ = s.Set("A", "1")
	_ = s.Set("B", "2")
	m, err := Resolve(s, []string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "2" {
		t.Errorf("Resolve = %v, want A=1 B=2", m)
	}
}

func TestResolve_MissingKeyError(t *testing.T) {
	s := newStore(t)
	_ = s.Set("A", "1")
	_ = s.Set("B", "2")
	// C is missing.
	_, err := Resolve(s, []string{"A", "B", "C"})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "C") {
		t.Errorf("error should name the missing key C: %v", err)
	}
}

func TestResolve_EmptyKeys(t *testing.T) {
	s := newStore(t)
	m, err := Resolve(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("Resolve(nil) = %v, want empty map", m)
	}
}

func TestInjectEnv_AddsToEmpty(t *testing.T) {
	s := newStore(t)
	_ = s.Set("DB_PASSWORD", "pw")
	env, err := InjectEnv(s, []string{"DB_PASSWORD"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "DB_PASSWORD=pw") {
		t.Errorf("env = %v, want it to contain DB_PASSWORD=pw", env)
	}
}

func TestInjectEnv_OverridesExisting(t *testing.T) {
	s := newStore(t)
	_ = s.Set("DB_PASSWORD", "new")
	existing := []string{"PATH=/bin", "DB_PASSWORD=old"}
	env, err := InjectEnv(s, []string{"DB_PASSWORD"}, existing)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(env, "DB_PASSWORD=old") {
		t.Error("env still contains the old DB_PASSWORD value")
	}
	if !slices.Contains(env, "DB_PASSWORD=new") {
		t.Error("env missing the overridden DB_PASSWORD=new")
	}
	if !slices.Contains(env, "PATH=/bin") {
		t.Error("env should preserve unrelated existing entries")
	}
}

func TestInjectEnv_DoesNotModifyExisting(t *testing.T) {
	s := newStore(t)
	_ = s.Set("DB_PASSWORD", "new")
	existing := []string{"DB_PASSWORD=old"}
	_, err := InjectEnv(s, []string{"DB_PASSWORD"}, existing)
	if err != nil {
		t.Fatal(err)
	}
	if existing[0] != "DB_PASSWORD=old" {
		t.Errorf("input slice was mutated: %v", existing)
	}
}

func TestValidateKeys_AllValid(t *testing.T) {
	if err := ValidateKeys([]string{"DB_PASSWORD", "JWT_SECRET", "KEY123"}); err != nil {
		t.Errorf("expected valid, got %v", err)
	}
}

func TestValidateKeys_ListsInvalid(t *testing.T) {
	err := ValidateKeys([]string{"GOOD", "bad-key", "also bad", "OK_2"})
	if err == nil {
		t.Fatal("expected error for invalid names")
	}
	if !strings.Contains(err.Error(), "bad-key") || !strings.Contains(err.Error(), "also bad") {
		t.Errorf("error should list all invalid names: %v", err)
	}
}
