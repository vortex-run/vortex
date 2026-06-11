// Package startup renders VORTEX's boot sequence for humans (brand redesign
// part 2): a banner, one ✓/✗ line per subsystem, and a final "ready" block —
// instead of a raw structured-log dump. In verbose mode (or when stdout is not
// an interactive terminal, e.g. under systemd) every method is a no-op and the
// standard logger remains the source of truth.
package startup

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// Display prints the clean boot sequence. The zero value is unusable;
// construct with NewStartupDisplay.
type Display struct {
	out     io.Writer
	verbose bool
}

// NewStartupDisplay returns a display writing to stdout. When verbose is true
// the display is inert: the raw structured logger is the boot output instead.
func NewStartupDisplay(verbose bool) *Display {
	return &Display{out: os.Stdout, verbose: verbose}
}

// SetOutput redirects the display (used by tests).
func (d *Display) SetOutput(w io.Writer) { d.out = w }

// Active reports whether the clean display is rendering (false in verbose
// mode, where the raw logger owns the console).
func (d *Display) Active() bool { return !d.verbose }

// bannerWidth is the inner width of the banner / ready boxes.
const bannerWidth = 47

// Banner prints the VORTEX mark, version, and tagline in a box.
func (d *Display) Banner() {
	if d.verbose {
		return
	}
	line := func(s string) string {
		pad := bannerWidth - len([]rune(s))
		if pad < 0 {
			pad = 0
		}
		return "║ " + s + strings.Repeat(" ", pad) + "║"
	}
	fmt.Fprintln(d.out, "╔"+strings.Repeat("═", bannerWidth+1)+"╗")
	fmt.Fprintln(d.out, line(""))
	fmt.Fprintln(d.out, line("  "+brand.LogoSmall+"  "+brand.Version))
	fmt.Fprintln(d.out, line("  "+brand.Tagline))
	fmt.Fprintln(d.out, line(""))
	fmt.Fprintln(d.out, "╚"+strings.Repeat("═", bannerWidth+1)+"╝")
	fmt.Fprintln(d.out)
}

// Step prints one subsystem coming up: a brief spinner line, overwritten in
// place (\r) with the ✓ result and its detail.
func (d *Display) Step(name, detail string) {
	if d.verbose {
		return
	}
	fmt.Fprintf(d.out, "%s %s...", brand.IconSpinner, name)
	fmt.Fprintf(d.out, "\r%s %-16s", brand.IconSuccess, name)
	if detail != "" {
		fmt.Fprintf(d.out, " %s %s", brand.IconInfo, detail)
	}
	fmt.Fprintln(d.out)
}

// StepFail prints a subsystem failure with a human explanation of the error
// (see Explain) indented beneath it.
func (d *Display) StepFail(name, errMsg string) {
	if d.verbose {
		return
	}
	fmt.Fprintf(d.out, "\r%s %s\n", brand.IconError, name)
	for _, line := range strings.Split(Explain(errMsg), "\n") {
		fmt.Fprintf(d.out, "  %s %s\n", brand.IconInfo, line)
	}
}

// ReadyConfig describes the running server for the final ready block.
type ReadyConfig struct {
	Version    string
	Cluster    string
	APIAddr    string
	Routes     []string
	AIProvider string
	Telegram   bool
}

// Ready prints the final "VORTEX is running" block with the addresses and
// next actions a new user needs.
func (d *Display) Ready(cfg ReadyConfig) {
	if d.verbose {
		return
	}
	base := "http://localhost" + normalizeAddr(cfg.APIAddr)
	fmt.Fprintln(d.out)
	fmt.Fprintln(d.out, strings.Repeat(brand.IconSep, bannerWidth))
	fmt.Fprintf(d.out, "%s VORTEX is running\n\n", brand.IconSuccess)
	fmt.Fprintf(d.out, "  Dashboard:    %s/dashboard\n", base)
	fmt.Fprintln(d.out, "  Agent chat:   run: vortex ui")
	fmt.Fprintln(d.out, "  Coding mode:  run: vortex code")
	fmt.Fprintln(d.out, "  Docs:         https://vortex.run/docs")
	fmt.Fprintln(d.out)
	var facts []string
	if cfg.Cluster != "" {
		facts = append(facts, "cluster "+cfg.Cluster)
	}
	if cfg.AIProvider != "" {
		facts = append(facts, "AI "+cfg.AIProvider)
	}
	if len(cfg.Routes) > 0 {
		facts = append(facts, fmt.Sprintf("%d routes", len(cfg.Routes)))
	}
	if cfg.Telegram {
		facts = append(facts, "telegram "+brand.IconSuccess)
	}
	if len(facts) > 0 {
		fmt.Fprintf(d.out, "  %s\n\n", strings.Join(facts, "  ·  "))
	}
	fmt.Fprintln(d.out, "  Press Ctrl+C to stop")
	fmt.Fprintln(d.out)
}

// normalizeAddr turns a listen address (":9090", "0.0.0.0:9090") into the
// ":port" suffix for a localhost URL.
func normalizeAddr(addr string) string {
	if addr == "" {
		return ":9090"
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

// Error-explanation patterns, checked in order.
var (
	reDialAddr   = regexp.MustCompile(`dial tcp ([\d.:\[\]a-fA-F]+)`)
	reSecretName = regexp.MustCompile(`secret\s+['"]?([A-Za-z0-9_.-]+)['"]?`)
)

// Explain maps a raw error string to a human explanation with a concrete fix.
// Unknown errors pass through unchanged.
func Explain(errMsg string) string {
	low := strings.ToLower(errMsg)
	switch {
	case strings.Contains(low, "i/o timeout") && strings.Contains(low, "dial tcp"):
		addr := "the configured address"
		if m := reDialAddr.FindStringSubmatch(errMsg); m != nil {
			addr = m[1]
		}
		return "Could not reach cluster peer at " + addr + ".\n" +
			"If running single node, remove peers from vortex.cue: cluster: peers: []"

	case strings.Contains(low, "secret") &&
		(strings.Contains(low, "missing") || strings.Contains(low, "not found") || strings.Contains(low, "not set")):
		name := "<name>"
		if m := reSecretName.FindStringSubmatch(low); m != nil && m[1] != "" {
			name = m[1]
		}
		return "Secret '" + name + "' is not set.\n" +
			"Set it with: vortex secret set " + name + " <value>"

	case strings.Contains(low, "certificate") || strings.Contains(low, "tls"):
		return "TLS certificate error. If testing locally, certificates will be\n" +
			"self-signed. This is normal on first start."

	default:
		return errMsg
	}
}
