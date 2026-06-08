package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPill_NonEmpty(t *testing.T) {
	got := Pill("running", ColorSuccess)
	if got == "" {
		t.Error("Pill returned empty string")
	}
	if !strings.Contains(got, "running") {
		t.Errorf("Pill = %q, want it to contain the label", got)
	}
}

func TestStatusDot_Colors(t *testing.T) {
	green := StatusDot(true)
	red := StatusDot(false)
	if green == "" || red == "" {
		t.Fatal("StatusDot returned empty")
	}
	// Both render the "●" glyph; they should differ by color escapes.
	if !strings.Contains(green, "●") || !strings.Contains(red, "●") {
		t.Errorf("StatusDot should contain ●: green=%q red=%q", green, red)
	}
}

func TestStyles_AllDefined(t *testing.T) {
	// A non-zero style renders its content; verify each renders without panic
	// and produces output for a sample string.
	styles := map[string]lipgloss.Style{
		"Title": TitleStyle, "Subtitle": SubtitleStyle, "Border": BorderStyle,
		"Selected": SelectedStyle, "Active": ActiveStyle, "Inactive": InactiveStyle,
		"StatusOK": StatusOKStyle, "StatusWarn": StatusWarnStyle, "StatusError": StatusErrorStyle,
		"Code": CodeStyle, "TableHeader": TableHeaderStyle, "TableRow": TableRowStyle,
		"TableAltRow": TableAltRowStyle, "Help": HelpStyle, "Input": InputStyle,
		"ChatUser": ChatUserStyle, "ChatAgent": ChatAgentStyle, "ChatSystem": ChatSystemStyle,
	}
	for name, s := range styles {
		if out := s.Render("x"); out == "" {
			t.Errorf("style %s rendered empty", name)
		}
	}
}

func TestColorConstants_NonEmpty(t *testing.T) {
	colors := []string{
		ColorPrimary, ColorSuccess, ColorWarning, ColorDanger, ColorPurple,
		ColorText, ColorTextDim, ColorBorder, ColorBackground, ColorSelected,
	}
	for i, c := range colors {
		if c == "" || c[0] != '#' {
			t.Errorf("color %d = %q, want a non-empty hex color", i, c)
		}
	}
}

func TestSpinner_Valid(t *testing.T) {
	s := Spinner()
	// A valid spinner has frames and a positive FPS.
	if len(s.Spinner.Frames) == 0 {
		t.Error("spinner has no frames")
	}
	if s.Spinner.FPS <= 0 {
		t.Error("spinner FPS should be positive")
	}
}
