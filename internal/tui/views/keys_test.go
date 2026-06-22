package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func keysFixture() *tui.KeyStatusData {
	return &tui.KeyStatusData{
		Mode:     "auto",
		TotalUSD: 0.023,
		Slots: []tui.KeySlotData{
			{ID: "slot-1", Provider: "deepseek", Label: "Primary", Model: "deepseek-chat",
				MaskedKey: "sk-e****", Priority: 1, Enabled: true, Score: 87,
				RequestsToday: 142, ErrorsLast10: 1, AvgLatencyMs: 2100,
				SpentTodayUSD: 0.023, DailyBudget: 10, Active: true},
			{ID: "slot-2", Provider: "groq", Label: "Backup", Model: "llama",
				MaskedKey: "gsk-****", Priority: 2, Enabled: true, Score: 95, AvgLatencyMs: 400},
			{ID: "slot-3", Provider: "ollama", Label: "Local", MaskedKey: "",
				Priority: 3, Enabled: true, Score: 100},
		},
	}
}

func sizedKeys(t *testing.T, data *tui.KeyStatusData) KeysModel {
	t.Helper()
	m := NewKeys(nil)
	if data != nil {
		m.SetData(data)
	}
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return upd.(KeysModel)
}

func TestKeys_ShowsAllSlots(t *testing.T) {
	m := sizedKeys(t, keysFixture())
	out := m.View()
	for _, want := range []string{"API Key Slots", "Mode: auto", "DeepSeek", "Groq", "Ollama", "$0.023"} {
		if !strings.Contains(out, want) {
			t.Errorf("keys view missing %q", want)
		}
	}
}

func TestKeys_ScoreBarColors(t *testing.T) {
	// Different score bands render different fill counts.
	high := scoreBar3(90)
	mid := scoreBar3(55)
	low := scoreBar3(20)
	if high == low {
		t.Error("high and low scores should render differently")
	}
	// All bars are exactly 3 cells wide (filled + empty).
	for _, b := range []string{high, mid, low} {
		// strip ANSI by counting block runes
		fills := strings.Count(b, "█") + strings.Count(b, "░")
		if fills != 3 {
			t.Errorf("score bar %q has %d cells, want 3", b, fills)
		}
	}
}

func TestKeys_ActiveSlotMarked(t *testing.T) {
	m := sizedKeys(t, keysFixture())
	out := m.renderTable()
	if !strings.Contains(out, "Active") {
		t.Errorf("active slot not marked:\n%s", out)
	}
}

func TestKeys_Navigation(t *testing.T) {
	m := sizedKeys(t, keysFixture())
	if m.SelectedSlot() != "slot-1" {
		t.Fatalf("initial selection = %s, want slot-1", m.SelectedSlot())
	}
	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = down.(KeysModel)
	if m.SelectedSlot() != "slot-2" {
		t.Errorf("after j, selection = %s, want slot-2", m.SelectedSlot())
	}
	// Can't move past the end.
	for i := 0; i < 5; i++ {
		nxt, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = nxt.(KeysModel)
	}
	if m.SelectedSlot() != "slot-3" {
		t.Errorf("selection ran past the end: %s", m.SelectedSlot())
	}
	up, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if up.(KeysModel).SelectedSlot() != "slot-2" {
		t.Errorf("after k, selection = %s, want slot-2", up.(KeysModel).SelectedSlot())
	}
}

func TestKeys_DetailViewMasksKey(t *testing.T) {
	m := sizedKeys(t, keysFixture())
	open, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = open.(KeysModel)
	if !m.DetailOpen() {
		t.Fatal("Enter should open the detail overlay")
	}
	out := m.View()
	for _, want := range []string{"Slot 1", "DeepSeek", "sk-e****", "encrypted at rest", "87/100", "142"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q:\n%s", want, out)
		}
	}
	// The real key never appears (only the masked form is sent by the API).
	if strings.Contains(out, "sk-ent") {
		t.Error("detail must not show a raw key")
	}
	// Enter closes it again.
	closed, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed.(KeysModel).DetailOpen() {
		t.Error("Enter should toggle the detail overlay closed")
	}
}

func TestKeys_ModeAndTotalShown(t *testing.T) {
	m := sizedKeys(t, keysFixture())
	out := m.renderTable()
	if !strings.Contains(out, "Mode: auto") {
		t.Error("table header should show the mode")
	}
	if !strings.Contains(out, "Total: $0.023") {
		t.Error("table header should show the daily total")
	}
}

func TestKeys_EmptyShowsSingleProvider(t *testing.T) {
	m := sizedKeys(t, &tui.KeyStatusData{Mode: "single", Slots: nil})
	if out := m.View(); !strings.Contains(out, "Single-provider mode") {
		t.Errorf("empty slots should show single-provider message:\n%s", out)
	}
}

func TestKeys_StatusCells(t *testing.T) {
	if s := statusCell(tui.KeySlotData{Enabled: false}); !strings.Contains(s, "Disabled") {
		t.Errorf("disabled status = %q", s)
	}
	if s := statusCell(tui.KeySlotData{Enabled: true, Active: true}); !strings.Contains(s, "Active") {
		t.Errorf("active status = %q", s)
	}
	if s := statusCell(tui.KeySlotData{Enabled: true, RateLimited: true}); !strings.Contains(s, "Limited") {
		t.Errorf("rate-limited status = %q", s)
	}
	if s := statusCell(tui.KeySlotData{Enabled: true}); !strings.Contains(s, "Standby") {
		t.Errorf("standby status = %q", s)
	}
}
