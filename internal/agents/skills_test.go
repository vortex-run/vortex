package agents

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func newTestSkillStore(t *testing.T) *SkillStore {
	t.Helper()
	store, err := NewSkillStore(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatalf("NewSkillStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleSkill() *Skill {
	return &Skill{
		Name:        "Create FastAPI app",
		Description: "Scaffold a FastAPI application with routes and tests",
		Trigger:     []string{"fastapi", "python", "api"},
		Steps: []SkillStep{
			{Description: "Create project directory", ToolName: "run_command"},
			{Description: "Write main.py with FastAPI app", ToolName: "write_file"},
			{Description: "Write tests", ToolName: "write_file", IsOptional: true},
		},
		CreatedFrom: "task-123",
	}
}

func TestSkillStoreSaveAndFind(t *testing.T) {
	store := newTestSkillStore(t)
	sk := sampleSkill()
	if err := store.Save(sk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if sk.ID == "" {
		t.Fatal("Save did not assign an ID")
	}

	// Find via trigger keyword (FTS index).
	got, err := store.Find("build me a fastapi service")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Find returned %d skills, want 1", len(got))
	}
	if got[0].Name != sk.Name {
		t.Errorf("Find name = %q, want %q", got[0].Name, sk.Name)
	}
	if len(got[0].Steps) != 3 {
		t.Errorf("Find steps = %d, want 3", len(got[0].Steps))
	}
	if got[0].Steps[2].IsOptional != true {
		t.Error("optional step flag lost in round-trip")
	}
}

func TestSkillStoreFindUnrelatedQueryEmpty(t *testing.T) {
	store := newTestSkillStore(t)
	if err := store.Save(sampleSkill()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Find("deploy kubernetes cluster")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Find returned %d skills for unrelated query, want 0", len(got))
	}
}

func TestSkillStoreFindEmptyQuery(t *testing.T) {
	store := newTestSkillStore(t)
	got, err := store.Find("   ")
	if err != nil || got != nil {
		t.Fatalf("Find(blank) = %v, %v; want nil, nil", got, err)
	}
}

func TestSkillStoreMarkUsed(t *testing.T) {
	store := newTestSkillStore(t)
	sk := sampleSkill()
	if err := store.Save(sk); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 2 successes + 1 failure → rate 2/3, count 3.
	for _, success := range []bool{true, true, false} {
		if err := store.MarkUsed(sk.ID, success); err != nil {
			t.Fatalf("MarkUsed: %v", err)
		}
	}
	skills, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("List returned %d skills, want 1", len(skills))
	}
	if skills[0].UsedCount != 3 {
		t.Errorf("UsedCount = %d, want 3", skills[0].UsedCount)
	}
	if math.Abs(skills[0].SuccessRate-2.0/3.0) > 1e-9 {
		t.Errorf("SuccessRate = %v, want %v", skills[0].SuccessRate, 2.0/3.0)
	}

	if err := store.MarkUsed("no-such-id", true); err == nil {
		t.Error("MarkUsed on missing skill should error")
	}
}

func TestSkillStoreListAndDelete(t *testing.T) {
	store := newTestSkillStore(t)
	a := sampleSkill()
	b := &Skill{
		Name:        "Deploy with Docker",
		Description: "Build image and run container",
		Trigger:     []string{"docker", "deploy"},
		Steps:       []SkillStep{{Description: "docker build"}, {Description: "docker run"}},
	}
	for _, sk := range []*Skill{a, b} {
		if err := store.Save(sk); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	skills, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("List returned %d skills, want 2", len(skills))
	}

	if err := store.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	skills, _ = store.List()
	if len(skills) != 1 || skills[0].ID != b.ID {
		t.Fatalf("after Delete, List = %d skills, want only %s", len(skills), b.Name)
	}
	// FTS entry gone too: query that matched a no longer returns it.
	got, _ := store.Find("fastapi")
	if len(got) != 0 {
		t.Error("deleted skill still findable via FTS")
	}
}

func TestSkillStoreStats(t *testing.T) {
	store := newTestSkillStore(t)
	if st := store.Stats(); st.Total != 0 {
		t.Fatalf("empty store Total = %d, want 0", st.Total)
	}

	a := sampleSkill()
	b := &Skill{Name: "Other", Description: "other", Trigger: []string{"other"}}
	for _, sk := range []*Skill{a, b} {
		if err := store.Save(sk); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// a used twice (both success), b never.
	_ = store.MarkUsed(a.ID, true)
	_ = store.MarkUsed(a.ID, true)

	st := store.Stats()
	if st.Total != 2 {
		t.Errorf("Total = %d, want 2", st.Total)
	}
	if math.Abs(st.AvgSuccessRate-0.5) > 1e-9 { // (1.0 + 0.0) / 2
		t.Errorf("AvgSuccessRate = %v, want 0.5", st.AvgSuccessRate)
	}
	if st.MostUsed != a.Name {
		t.Errorf("MostUsed = %q, want %q", st.MostUsed, a.Name)
	}
}

func TestSkillStoreSaveValidation(t *testing.T) {
	store := newTestSkillStore(t)
	if err := store.Save(nil); err == nil {
		t.Error("Save(nil) should error")
	}
	if err := store.Save(&Skill{Name: "  "}); err == nil {
		t.Error("Save with blank name should error")
	}
}

func TestSkillStoreConcurrentAccess(t *testing.T) {
	store := newTestSkillStore(t)
	sk := sampleSkill()
	if err := store.Save(sk); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			_ = store.Save(&Skill{
				Name:        fmt.Sprintf("Skill %d", i),
				Description: "concurrent",
				Trigger:     []string{"concurrent"},
			})
		}(i)
		go func() {
			defer wg.Done()
			_ = store.MarkUsed(sk.ID, true)
		}()
		go func() {
			defer wg.Done()
			_, _ = store.Find("concurrent fastapi")
		}()
	}
	wg.Wait()

	skills, err := store.List()
	if err != nil {
		t.Fatalf("List after concurrent ops: %v", err)
	}
	if len(skills) != 11 {
		t.Errorf("List returned %d skills, want 11", len(skills))
	}
}
