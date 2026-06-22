package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

// statsFixture backs the memory-panel test.
var statsFixture = tui.AgentsData{Skills: 12, Episodes: 47, Sessions: 8}

// sizedCode returns a CodeModel with a realistic window size applied.
func sizedCode(opts ...CodeOption) CodeModel {
	m := NewCode(nil, opts...)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(CodeModel)
}

// blurred returns the model with input focus toggled off (Esc) so hotkeys work.
func blurred(m CodeModel) CodeModel {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	return updated.(CodeModel)
}

func codeKey(m CodeModel, s string) CodeModel {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return updated.(CodeModel)
}

func TestCode_InitializesAllPanels(t *testing.T) {
	m := sizedCode(WithProject("S:\\myapp"), WithModel("deepseek-chat"))
	out := m.View()
	for _, want := range []string{
		"VORTEX CODE", "S:\\myapp", "deepseek-chat",
		"AGENTS", "Coordinator", "Code Agent", "Test Agent", "Review", "DevOps",
		"MEMORY", "TASK PROGRESS",
		"Session started",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if len(m.AgentRoster()) != 5 {
		t.Errorf("roster = %d agents, want 5", len(m.AgentRoster()))
	}
}

func TestCode_ActivityEntryPrefixes(t *testing.T) {
	m := sizedCode()
	m.addEntry("user", "build a REST API", "message")
	m.addEntry("coordinator", "Planning the work", "message")
	m.addEntry("system", "Plan ready (3 steps)", "message")
	out := m.renderFeed()
	if !strings.Contains(out, "▸ You: build a REST API") {
		t.Errorf("user entry missing ▸ prefix:\n%s", out)
	}
	if !strings.Contains(out, "◆ coordinator: Planning the work") {
		t.Errorf("agent entry missing ◆ prefix:\n%s", out)
	}
	if !strings.Contains(out, "→ Plan ready (3 steps)") {
		t.Errorf("system entry missing → prefix:\n%s", out)
	}
	// Entries carry timestamps.
	if !strings.Contains(out, "[") || !strings.Contains(out, ":") {
		t.Error("entries should be timestamped")
	}
}

func TestCode_StepFraming(t *testing.T) {
	m := sizedCode()
	m.ingestReply("Step 1/3: Code Agent\n› Writing main.py...\n✓ Created 4 files\n✗ tests failed")
	out := m.renderFeed()
	if !strings.Contains(out, "┌─ Step 1/3") {
		t.Errorf("step start not framed:\n%s", out)
	}
	if !strings.Contains(out, "│ › Writing main.py...") {
		t.Errorf("step item not framed:\n%s", out)
	}
	if !strings.Contains(out, "└─ ") || !strings.Contains(out, "✓ Created 4 files") {
		t.Errorf("step end not framed:\n%s", out)
	}
	if !strings.Contains(out, "✗ tests failed") {
		t.Errorf("error line missing:\n%s", out)
	}
}

func TestCode_ProgressTracksSteps(t *testing.T) {
	m := sizedCode()
	m.ingestReply("Step 2 of 4: running tests")
	p := m.Progress()
	if p.Current != 2 || p.Total != 4 {
		t.Fatalf("progress = %d/%d, want 2/4", p.Current, p.Total)
	}
	if p.Percent != 50 {
		t.Errorf("percent = %v, want 50", p.Percent)
	}
	if !strings.Contains(m.renderSidebar(), "Step 2 of 4") {
		t.Error("sidebar should show the step counter")
	}
}

func TestCode_PauseToggles(t *testing.T) {
	m := blurred(sizedCode())
	m = codeKey(m, "p")
	if !m.Paused() {
		t.Fatal("P should pause")
	}
	if !strings.Contains(m.View(), "PAUSED") {
		t.Error("paused banner missing")
	}
	m = codeKey(m, "p")
	if m.Paused() {
		t.Error("P should resume")
	}
}

func TestCode_StopConfirmation(t *testing.T) {
	m := blurred(sizedCode())
	m.working = true
	m = codeKey(m, "s")
	if !m.ConfirmingStop() {
		t.Fatal("S during a task should ask for confirmation")
	}
	if out := m.View(); !strings.Contains(out, "Stop task?") || !strings.Contains(out, "[Y] Stop") {
		t.Errorf("stop confirmation box missing:\n%s", out)
	}
	// N keeps the task running.
	m = codeKey(m, "n")
	if m.ConfirmingStop() || !m.Working() {
		t.Error("N should dismiss and keep working")
	}
	// Y stops it.
	m = codeKey(m, "s")
	m = codeKey(m, "y")
	if m.Working() {
		t.Error("Y should stop the task")
	}
	found := false
	for _, e := range m.Activity() {
		if strings.Contains(e.Content, "Task stopped") {
			found = true
		}
	}
	if !found {
		t.Error("stop should be recorded in the feed")
	}
}

func TestCode_StopIgnoredWhenIdle(t *testing.T) {
	m := blurred(sizedCode())
	m = codeKey(m, "s")
	if m.ConfirmingStop() {
		t.Error("S with no running task should be a no-op")
	}
}

func TestCode_TelegramForwardWithoutClient(t *testing.T) {
	m := blurred(sizedCode())
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Fatal("T should produce a forward command")
	}
	reply, ok := cmd().(codeReplyMsg)
	if !ok {
		t.Fatalf("T command produced %T, want codeReplyMsg", cmd())
	}
	// No client in tests → a clear error, not a panic.
	if reply.err == nil {
		t.Error("forward without a client should error")
	}
	_ = updated
}

func TestCode_HelpOverlayToggles(t *testing.T) {
	m := blurred(sizedCode())
	m = codeKey(m, "?")
	if !m.HelpOpen() {
		t.Fatal("? should open help")
	}
	out := m.View()
	for _, want := range []string{"Help — VORTEX Code", "Pause/resume", "build a REST API with JWT auth"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing %q", want)
		}
	}
	m = codeKey(m, "?")
	if m.HelpOpen() {
		t.Error("? should close help")
	}
}

func TestCode_EnterSubmitsAndMarksBusy(t *testing.T) {
	m := sizedCode()
	m.input.SetValue("build a calculator")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)
	if !m.Working() {
		t.Fatal("Enter should start a task")
	}
	if cmd == nil {
		t.Fatal("Enter should produce a submit command")
	}
	// Roster flips to busy.
	for _, a := range m.AgentRoster() {
		if a.Name == "Coordinator" && a.Status != "busy" {
			t.Error("Coordinator should be busy during a task")
		}
	}
	// The user message is in the feed.
	out := m.renderFeed()
	if !strings.Contains(out, "You: build a calculator") {
		t.Errorf("feed missing user message:\n%s", out)
	}
	// Reply completion resets state and fills the feed.
	updated, _ = m.Update(codeReplyMsg{content: "Step 1/2: plan\n✓ done"})
	m = updated.(CodeModel)
	if m.Working() {
		t.Error("reply should end the working state")
	}
	if m.Progress().Percent != 100 {
		t.Errorf("completion should set 100%%, got %v", m.Progress().Percent)
	}
}

func TestCode_ReplyErrorShownInFeed(t *testing.T) {
	m := sizedCode()
	updated, _ := m.Update(codeReplyMsg{err: errFake})
	m = updated.(CodeModel)
	if out := m.renderFeed(); !strings.Contains(out, "✗ fake failure") {
		t.Errorf("error not rendered:\n%s", out)
	}
}

// errFake is a reusable test error.
var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake failure" }

func TestCode_TeamModeWrapsGoal(t *testing.T) {
	team := NewCode(nil)
	if !team.team {
		t.Fatal("team mode should default on")
	}
	solo := NewCode(nil, WithoutTeam())
	if solo.team {
		t.Error("WithoutTeam should disable orchestration")
	}
}

func TestCode_StandaloneQuitKey(t *testing.T) {
	m := blurred(sizedCode(Standalone()))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q in standalone mode should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should map to tea.Quit in standalone mode")
	}
	// Non-standalone: q is not a quit key (typed or ignored).
	inApp := blurred(sizedCode())
	_, cmd = inApp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("q must not quit when embedded in the main TUI")
		}
	}
}

func TestCode_MemoryPanelShowsStats(t *testing.T) {
	m := sizedCode()
	updated, _ := m.Update(codeStatsMsg{stats: &statsFixture})
	m = updated.(CodeModel)
	out := m.renderSidebar()
	for _, want := range []string{"12 learned", "47 stored", "8 total"} {
		if !strings.Contains(out, want) {
			t.Errorf("memory panel missing %q:\n%s", want, out)
		}
	}
}

func TestCode_InputFocusReporting(t *testing.T) {
	m := sizedCode()
	if !m.IsInputFocused() {
		t.Fatal("input should start focused")
	}
	m = blurred(m)
	if m.IsInputFocused() {
		t.Error("Esc should blur the input")
	}
	m = blurred(m) // toggles back
	if !m.IsInputFocused() {
		t.Error("Esc again should refocus")
	}
}

func TestCode_ElapsedShownWhileWorking(t *testing.T) {
	m := sizedCode()
	m.working = true
	m.workStart = time.Now().Add(-65 * time.Second)
	if out := m.renderSidebar(); !strings.Contains(out, "1m 5s") {
		t.Errorf("elapsed time missing:\n%s", out)
	}
}

func TestCode_TeamModeHeader(t *testing.T) {
	m := sizedCode(WithTeam(), WithProject(`S:\proj`))
	out := m.View()
	if !strings.Contains(out, "👥 Team") {
		t.Errorf("team mode header should show 👥 Team:\n%s", out)
	}
	if !m.TeamMode() {
		t.Error("WithTeam should enable team mode")
	}
}

func TestCode_SoloModeHeader(t *testing.T) {
	m := sizedCode(WithoutTeam(), WithProject(`S:\proj`))
	out := m.View()
	if !strings.Contains(out, "👤 Solo") {
		t.Errorf("solo mode header should show 👤 Solo:\n%s", out)
	}
	if m.TeamMode() {
		t.Error("WithoutTeam should disable team mode")
	}
}

func TestCode_SoloShowsSingleAgent(t *testing.T) {
	m := sizedCode(WithoutTeam())
	side := m.renderSidebar()
	// Solo mode collapses the roster to one "Agent" entry.
	if !strings.Contains(side, "Agent") {
		t.Errorf("solo roster should show a single Agent:\n%s", side)
	}
	if strings.Contains(side, "Test Agent") || strings.Contains(side, "Review") {
		t.Errorf("solo mode should not list the specialist roster:\n%s", side)
	}
}

func TestCode_TeamShowsFullRoster(t *testing.T) {
	m := sizedCode(WithTeam())
	side := m.renderSidebar()
	for _, want := range []string{"Coordinator", "Code Agent", "Test Agent", "Review"} {
		if !strings.Contains(side, want) {
			t.Errorf("team roster missing %q:\n%s", want, side)
		}
	}
}

func TestCode_ProjectPanel(t *testing.T) {
	info := &ProjectInfo{Name: "FastAPI Auth", Stack: []string{"Python", "FastAPI", "PG"}, TestCmd: "pytest tests/"}
	m := sizedCode(WithProjectInfo(info))
	if m.ProjectInfo() == nil || m.ProjectInfo().Name != "FastAPI Auth" {
		t.Fatalf("project info not set: %+v", m.ProjectInfo())
	}
	side := m.renderSidebar()
	for _, want := range []string{"PROJECT", "FastAPI Auth", "Python", "pytest tests/"} {
		if !strings.Contains(side, want) {
			t.Errorf("PROJECT panel missing %q:\n%s", want, side)
		}
	}
}

func TestCode_NoProjectPanelWhenAbsent(t *testing.T) {
	m := sizedCode(WithTeam())
	if strings.Contains(m.renderSidebar(), "PROJECT") {
		t.Error("PROJECT panel should not render without AGENTS.md info")
	}
}
