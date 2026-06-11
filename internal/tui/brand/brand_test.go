package brand

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestLogoIsMultiLine(t *testing.T) {
	if strings.TrimSpace(Logo) == "" {
		t.Fatal("Logo is empty")
	}
	if lines := strings.Split(strings.TrimSpace(Logo), "\n"); len(lines) < 5 {
		t.Errorf("Logo has %d lines, want a multi-line wordmark", len(lines))
	}
	if !strings.Contains(LogoSmall, "VORTEX") {
		t.Errorf("LogoSmall = %q, want it to name VORTEX", LogoSmall)
	}
	if Tagline == "" || Version == "" {
		t.Error("Tagline/Version must be set")
	}
}

func TestColorConstantsAreValidHex(t *testing.T) {
	hex := regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	colors := map[string]string{
		"ColorPrimary":    ColorPrimary,
		"ColorSuccess":    ColorSuccess,
		"ColorWarning":    ColorWarning,
		"ColorDanger":     ColorDanger,
		"ColorPurple":     ColorPurple,
		"ColorBackground": ColorBackground,
		"ColorSurface":    ColorSurface,
		"ColorBorder":     ColorBorder,
		"ColorText":       ColorText,
		"ColorTextDim":    ColorTextDim,
		"ColorSelected":   ColorSelected,
	}
	for name, c := range colors {
		if !hex.MatchString(c) {
			t.Errorf("%s = %q, not a valid #RRGGBB hex color", name, c)
		}
	}
}

func TestMaskSecret(t *testing.T) {
	if got := MaskSecret("sk-ant-api-0123456789"); got != "sk-a****" {
		t.Errorf("MaskSecret = %q, want sk-a****", got)
	}
	for _, short := range []string{"", "ab", "abcd"} {
		if got := MaskSecret(short); got != short {
			t.Errorf("MaskSecret(%q) = %q, want unchanged", short, got)
		}
	}
}

func TestFormatCost(t *testing.T) {
	if got := FormatCost(0); got != "free" {
		t.Errorf("FormatCost(0) = %q, want free", got)
	}
	if got := FormatCost(0.001); got != "$0.001" {
		t.Errorf("FormatCost(0.001) = %q, want $0.001", got)
	}
	if got := FormatCost(1.2345); got != "$1.234" && got != "$1.235" {
		t.Errorf("FormatCost(1.2345) = %q, want three decimals", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:               "45s",
		14*time.Minute + 5*time.Second: "14m 5s",
		2*time.Hour + 14*time.Minute:   "2h 14m",
		26*time.Hour + 30*time.Minute:  "26h 30m",
		500 * time.Millisecond:         "1s",
	}
	for d, want := range cases {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestProgressBarWidth(t *testing.T) {
	for _, percent := range []float64{0, 33, 78, 100, 150, -5} {
		for _, width := range []int{1, 10, 16, 40} {
			bar := ProgressBar(percent, width)
			if got := lipgloss.Width(bar); got != width {
				t.Errorf("ProgressBar(%v, %d) rendered width = %d", percent, width, got)
			}
		}
	}
	if ProgressBar(50, 0) != "" {
		t.Error("ProgressBar with width 0 should be empty")
	}
	// 100% is fully filled, 0% fully empty.
	if full := ProgressBar(100, 4); !strings.Contains(full, "████") {
		t.Errorf("full bar = %q", full)
	}
	if empty := ProgressBar(0, 4); !strings.Contains(empty, "░░░░") {
		t.Errorf("empty bar = %q", empty)
	}
}

func TestStatusDotDiffersByHealth(t *testing.T) {
	// Tests run without a TTY, where lipgloss downgrades to no-color and both
	// dots would render identically; force truecolor so the color escapes are
	// observable, then restore.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	if StatusDot(true) == StatusDot(false) {
		t.Error("StatusDot must differ between healthy and unhealthy")
	}
	if !strings.Contains(StatusDot(true), IconPulse) {
		t.Error("StatusDot must render the pulse icon")
	}
}

func TestRiskColor(t *testing.T) {
	cases := map[string]string{
		RiskLow:      ColorSuccess,
		RiskMedium:   ColorWarning,
		RiskHigh:     ColorDanger,
		RiskCritical: ColorDanger,
	}
	for risk, want := range cases {
		if got := RiskColor(risk); got != want {
			t.Errorf("RiskColor(%q) = %q, want %q", risk, got, want)
		}
	}
}

func TestRiskBadgeContainsLevel(t *testing.T) {
	if badge := RiskBadge(RiskLow); !strings.Contains(badge, "[LOW RISK]") {
		t.Errorf("RiskBadge = %q", badge)
	}
}

func TestPillContainsText(t *testing.T) {
	if p := Pill("prod", ColorSuccess); !strings.Contains(p, "prod") || !strings.Contains(p, IconPulse) {
		t.Errorf("Pill = %q", p)
	}
}

func TestStylesInitialized(t *testing.T) {
	zero := lipgloss.NewStyle()
	styles := map[string]lipgloss.Style{
		"StyleTitle":       StyleTitle,
		"StyleSubtitle":    StyleSubtitle,
		"StyleBorder":      StyleBorder,
		"StyleSelected":    StyleSelected,
		"StyleActive":      StyleActive,
		"StyleInactive":    StyleInactive,
		"StyleSuccess":     StyleSuccess,
		"StyleWarn":        StyleWarn,
		"StyleError":       StyleError,
		"StyleCode":        StyleCode,
		"StyleUserMsg":     StyleUserMsg,
		"StyleAgentMsg":    StyleAgentMsg,
		"StyleSystemMsg":   StyleSystemMsg,
		"StyleTopBar":      StyleTopBar,
		"StyleSidebar":     StyleSidebar,
		"StyleHelp":        StyleHelp,
		"StyleApproval":    StyleApproval,
		"StyleProgress":    StyleProgress,
		"StyleCard":        StyleCard,
		"StyleTableHeader": StyleTableHeader,
		"StyleTableRow":    StyleTableRow,
		"StyleTableAlt":    StyleTableAlt,
	}
	for name, st := range styles {
		if st.String() == zero.String() && name != "" {
			// A style identical to the zero style was never configured. Compare
			// via GetForeground/GetBorder too, since String() of an empty style
			// is "".
			if st.GetForeground() == zero.GetForeground() &&
				st.GetBackground() == zero.GetBackground() &&
				!st.GetBold() && st.GetBorderStyle() == zero.GetBorderStyle() {
				t.Errorf("%s appears unconfigured", name)
			}
		}
	}
}
