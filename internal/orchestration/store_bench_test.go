package orchestration

import (
	"path/filepath"
	"testing"
)

// BenchmarkSaveTask measures persisting one task state change, which the
// orchestrator does on every transition so a crashed run can resume.
func BenchmarkSaveTask(b *testing.B) {
	s, err := NewRunStore(filepath.Join(b.TempDir(), "orchestration.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	tasks := []*Task{{ID: "a", Name: "Task A", AgentType: "general", Input: "work",
		Meta: map[string]any{"required_capabilities": []string{"code.write"}}}}
	runID, err := s.CreateRun("bench", "", tasks)
	if err != nil {
		b.Fatal(err)
	}
	t := tasks[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.Attempts = i%3 + 1
		if i%2 == 0 {
			t.State = StateRunning
		} else {
			t.State = StatePending
		}
		if err := s.SaveTask(runID, t); err != nil {
			b.Fatal(err)
		}
	}
}
