package secrets

import (
	"context"
	"testing"
)

func TestNewAdapter_Local(t *testing.T) {
	a, err := NewAdapter(AdapterConfig{Kind: "local", Local: newStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(*LocalAdapter); !ok {
		t.Errorf("expected *LocalAdapter, got %T", a)
	}
}

func TestNewAdapter_UnknownKind(t *testing.T) {
	if _, err := NewAdapter(AdapterConfig{Kind: "consul"}); err == nil {
		t.Error("expected error for unknown kind")
	}
}

func TestNewAdapter_LocalNilStore(t *testing.T) {
	if _, err := NewAdapter(AdapterConfig{Kind: "local"}); err == nil {
		t.Error("expected error when local store is nil")
	}
}

func TestNewAdapter_VaultAddressRequired(t *testing.T) {
	if _, err := NewAdapter(AdapterConfig{Kind: "vault"}); err == nil {
		t.Error("expected error when vault Address is empty")
	}
}

func TestNewAdapter_SSMRegionRequired(t *testing.T) {
	if _, err := NewAdapter(AdapterConfig{Kind: "aws-ssm"}); err == nil {
		t.Error("expected error when ssm Region is empty")
	}
}

func TestNewAdapter_GCPProjectRequired(t *testing.T) {
	if _, err := NewAdapter(AdapterConfig{Kind: "gcp-sm"}); err == nil {
		t.Error("expected error when gcp ProjectID is empty")
	}
}

func TestLocalAdapter_GetDelegates(t *testing.T) {
	store := newStore(t)
	_ = store.Set("K", "v")
	a := NewLocalAdapter(store)
	got, err := a.Get(context.Background(), "K")
	if err != nil || got != "v" {
		t.Errorf("Get = %q, %v; want v, nil", got, err)
	}
}

func TestLocalAdapter_SetDelegates(t *testing.T) {
	store := newStore(t)
	a := NewLocalAdapter(store)
	if err := a.Set(context.Background(), "K", "v"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get("K")
	if got != "v" {
		t.Errorf("store value = %q, want v", got)
	}
}

func TestLocalAdapter_ListDelegates(t *testing.T) {
	store := newStore(t)
	_ = store.Set("A", "1")
	_ = store.Set("B", "2")
	a := NewLocalAdapter(store)
	list, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("List len = %d, want 2", len(list))
	}
}

func TestLocalAdapter_DeleteDelegates(t *testing.T) {
	store := newStore(t)
	_ = store.Set("K", "v")
	a := NewLocalAdapter(store)
	if err := a.Delete(context.Background(), "K"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Exists("K"); ok {
		t.Error("secret should be deleted via adapter")
	}
}

func TestLocalAdapter_PingNil(t *testing.T) {
	a := NewLocalAdapter(newStore(t))
	if err := a.Ping(context.Background()); err != nil {
		t.Errorf("local Ping = %v, want nil", err)
	}
}
