package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGetHelp_NonEmptyForAllViews(t *testing.T) {
	for id := HelpOverview; id <= HelpSecrets; id++ {
		c := GetHelp(id)
		if c.Title == "" {
			t.Errorf("view %d: empty title", id)
		}
		if len(c.Sections) == 0 {
			t.Errorf("view %d (%s): no sections", id, c.Title)
		}
		for _, sec := range c.Sections {
			if sec.Title == "" || len(sec.Items) == 0 {
				t.Errorf("view %d: empty section %+v", id, sec)
			}
		}
	}
}

func TestGetHelp_AgentsListsSlashCommands(t *testing.T) {
	c := GetHelp(HelpAgents)
	var keys []string
	for _, sec := range c.Sections {
		for _, it := range sec.Items {
			keys = append(keys, it.Key)
		}
	}
	joined := strings.Join(keys, " ")
	for _, want := range []string{"/ls", "/read", "/run", "/search", "/git", "/undo", "/history", "/help"} {
		if !strings.Contains(joined, want) {
			t.Errorf("agents help missing slash command %q", want)
		}
	}
}

func TestGetHelp_AgentsHasShortcuts(t *testing.T) {
	c := GetHelp(HelpAgents)
	want := map[string]bool{"Enter": false, "Ctrl+L": false, "Y+Enter": false, "N+Enter": false}
	for _, sec := range c.Sections {
		for _, it := range sec.Items {
			if _, ok := want[it.Key]; ok {
				want[it.Key] = true
			}
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("agents help missing shortcut %q", k)
		}
	}
}

func TestGetHelp_CodeExamplesAndHowItWorks(t *testing.T) {
	c := GetHelp(HelpCode)
	body := renderContent(c)
	for _, want := range []string{"Pause/resume", "Coordinator plans", "build a REST API with auth"} {
		if !strings.Contains(body, want) {
			t.Errorf("code help missing %q", want)
		}
	}
}

func TestGetHelp_UnknownFallsBackToGeneric(t *testing.T) {
	c := GetHelp(HelpViewID(999))
	if c.Title != genericHelp.Title {
		t.Errorf("unknown view help = %q, want generic", c.Title)
	}
}

func TestHelpModel_RendersTitle(t *testing.T) {
	m := NewHelp(HelpLogs)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	out := updated.(HelpModel).View()
	if !strings.Contains(out, "Help — Log Viewer") {
		t.Errorf("help overlay should title the view:\n%s", out)
	}
	if !strings.Contains(out, "follow mode") {
		t.Errorf("logs help should explain follow mode:\n%s", out)
	}
}

func TestHelpModel_ClosesOnEscAndQuestionMark(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("?")},
		{Type: tea.KeyRunes, Runes: []rune("q")},
	} {
		m := NewHelp(HelpAgents)
		updated, _ := m.Update(k)
		if !updated.(HelpModel).Closed() {
			t.Errorf("key %v should close the overlay", k)
		}
	}
}

func TestHelpModel_StaysOpenOnOtherKeys(t *testing.T) {
	m := NewHelp(HelpAgents)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if updated.(HelpModel).Closed() {
		t.Error("unrelated key should not close the overlay")
	}
}

// renderContent flattens help content to a string for assertions.
func renderContent(c HelpContent) string {
	var b strings.Builder
	b.WriteString(c.Title + "\n")
	for _, sec := range c.Sections {
		b.WriteString(sec.Title + "\n")
		for _, it := range sec.Items {
			b.WriteString(it.Key + " " + it.Action + " " + it.Example + "\n")
		}
	}
	return b.String()
}
