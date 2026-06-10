package secrets

import (
	"errors"
	"os"
	"testing"
	"time"
)

func rotationStore(t *testing.T) *SecretStore {
	t.Helper()
	s, err := NewSecretStore(t.TempDir(), []byte("rotation-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetWithMetadata_StoresValueAndMetadata(t *testing.T) {
	s := rotationStore(t)
	expires := time.Now().Add(90 * 24 * time.Hour)
	err := s.SetWithMetadata("db_password", "hunter2", SecretMetadata{
		ExpiresAt:   expires,
		RotateEvery: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Value round-trips like a plain Set.
	v, err := s.Get("db_password")
	if err != nil {
		t.Fatal(err)
	}
	if v != "hunter2" {
		t.Errorf("value = %q", v)
	}

	meta, err := s.GetMetadata("db_password")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "db_password" {
		t.Errorf("Name = %q", meta.Name)
	}
	if !meta.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", meta.ExpiresAt, expires)
	}
	if meta.RotateEvery != 30*24*time.Hour {
		t.Errorf("RotateEvery = %v", meta.RotateEvery)
	}
	if meta.Version != 1 {
		t.Errorf("Version = %d, want 1", meta.Version)
	}
	if meta.CreatedAt.IsZero() || meta.LastRotated.IsZero() {
		t.Error("CreatedAt/LastRotated should default to now, got zero")
	}
}

func TestSetWithMetadata_VersionIncrementsAndCreatedAtSticks(t *testing.T) {
	s := rotationStore(t)
	if err := s.SetWithMetadata("jwt", "v1", SecretMetadata{}); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetMetadata("jwt")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetWithMetadata("jwt", "v2", SecretMetadata{}); err != nil {
		t.Fatal(err)
	}
	second, err := s.GetMetadata("jwt")
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != first.Version+1 {
		t.Errorf("Version = %d, want %d", second.Version, first.Version+1)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt changed on rotation: %v → %v", first.CreatedAt, second.CreatedAt)
	}
}

func TestGetMetadata_NotExistForPlainSet(t *testing.T) {
	s := rotationStore(t)
	if err := s.Set("plain", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMetadata("plain"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist for plain-set secret, got %v", err)
	}
}

func TestIsExpired_TrueAfterExpiry(t *testing.T) {
	s := rotationStore(t)
	err := s.SetWithMetadata("old", "v", SecretMetadata{
		ExpiresAt: time.Now().Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.IsExpired("old")
	if err != nil {
		t.Fatal(err)
	}
	if !expired {
		t.Error("secret past its ExpiresAt should be expired")
	}
}

func TestIsExpired_FalseBeforeExpiryAndWithoutMetadata(t *testing.T) {
	s := rotationStore(t)
	err := s.SetWithMetadata("fresh", "v", SecretMetadata{
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if expired, _ := s.IsExpired("fresh"); expired {
		t.Error("secret before its ExpiresAt should not be expired")
	}

	if err := s.Set("nometa", "v"); err != nil {
		t.Fatal(err)
	}
	if expired, _ := s.IsExpired("nometa"); expired {
		t.Error("secret without metadata should never expire")
	}
}

func TestDueForRotation_TriggersInsideWarningWindow(t *testing.T) {
	s := rotationStore(t)
	// Expires in 5 days — inside the 7-day warning window.
	err := s.SetWithMetadata("soon", "v", SecretMetadata{
		ExpiresAt: time.Now().Add(5 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	due, err := s.DueForRotation("soon")
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Error("secret expiring in 5 days should be due for rotation")
	}

	// Expires in 30 days — outside the window.
	err = s.SetWithMetadata("later", "v", SecretMetadata{
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueForRotation("later"); due {
		t.Error("secret expiring in 30 days should not be due yet")
	}
}

func TestDueForRotation_RotateEveryInterval(t *testing.T) {
	s := rotationStore(t)
	// Last rotated 25 days ago with a 30-day interval: deadline in 5 days,
	// inside the warning window.
	err := s.SetWithMetadata("periodic", "v", SecretMetadata{
		LastRotated: time.Now().Add(-25 * 24 * time.Hour),
		RotateEvery: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	due, err := s.DueForRotation("periodic")
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Error("secret 5 days from its rotation deadline should be due")
	}

	// Freshly rotated: not due.
	err = s.SetWithMetadata("freshrot", "v", SecretMetadata{
		RotateEvery: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueForRotation("freshrot"); due {
		t.Error("freshly rotated secret should not be due")
	}
}

func TestCheckRotation_ReportsExpiredAndDue(t *testing.T) {
	s := rotationStore(t)
	mustSet := func(name string, meta SecretMetadata) {
		t.Helper()
		if err := s.SetWithMetadata(name, "v", meta); err != nil {
			t.Fatal(err)
		}
	}
	mustSet("expired_one", SecretMetadata{ExpiresAt: time.Now().Add(-30 * 24 * time.Hour)})
	mustSet("due_one", SecretMetadata{ExpiresAt: time.Now().Add(3 * 24 * time.Hour)})
	mustSet("healthy", SecretMetadata{ExpiresAt: time.Now().Add(300 * 24 * time.Hour)})
	if err := s.Set("nometa", "v"); err != nil {
		t.Fatal(err)
	}

	alerts, err := s.CheckRotation()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(alerts), alerts)
	}
	byName := map[string]RotationAlert{}
	for _, a := range alerts {
		byName[a.Name] = a
	}
	if a, ok := byName["expired_one"]; !ok || !a.Expired {
		t.Errorf("expired_one should be reported as expired: %+v", a)
	}
	if a, ok := byName["due_one"]; !ok || a.Expired {
		t.Errorf("due_one should be reported as due (not expired): %+v", a)
	}
}

func TestDelete_RemovesMetadata(t *testing.T) {
	s := rotationStore(t)
	err := s.SetWithMetadata("gone", "v", SecretMetadata{
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMetadata("gone"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("metadata should be deleted with the secret, got %v", err)
	}
}
