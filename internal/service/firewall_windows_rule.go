package service

import (
	"fmt"
	"io"
)

// Windows Firewall pre-authorisation.
//
// The first time a program opens a listening socket, Windows shows an
// interactive "Do you want to allow..." dialog and blocks inbound traffic
// until someone answers it. On a server — a VPS left running for hours, or a
// machine administered over RDP that nobody is watching — there is no one to
// click it, so VORTEX would start, appear healthy, and silently refuse inbound
// connections. Creating the rule at install time removes the dialog entirely,
// because a program with an explicit rule is never prompted for.
//
// The rule is deliberately narrow. It authorises the vortex executable only,
// and only on the private and domain profiles: a VPS's public profile is the
// hostile side, and an operator who genuinely wants VORTEX reachable from the
// internet should say so explicitly rather than inherit it from an installer.
// FirewallProfiles overrides that when they do.

// firewallRuleName is the Windows Firewall rule VORTEX manages. Install
// creates it and Uninstall deletes it by this exact name, so re-running the
// installer replaces the rule rather than accumulating duplicates.
const firewallRuleName = "VORTEX"

// defaultFirewallProfiles are the netsh profiles the rule applies to. Public
// is excluded on purpose — see the note above.
const defaultFirewallProfiles = "private,domain"

// firewallAddArgs builds the netsh invocation that authorises execPath.
// Re-running it after a delete is what makes install idempotent.
func firewallAddArgs(execPath, profiles string) []string {
	if profiles == "" {
		profiles = defaultFirewallProfiles
	}
	return []string{
		"advfirewall", "firewall", "add", "rule",
		"name=" + firewallRuleName,
		"dir=in",
		"action=allow",
		"program=" + execPath,
		"enable=yes",
		"profile=" + profiles,
		"description=Allows inbound connections to the VORTEX server. Created by 'vortex service install'.",
	}
}

// firewallDeleteArgs builds the netsh invocation that removes the rule.
func firewallDeleteArgs() []string {
	return []string{"advfirewall", "firewall", "delete", "rule", "name=" + firewallRuleName}
}

// addFirewallRule authorises execPath in Windows Firewall. It deletes any
// existing rule of the same name first so repeated installs do not stack up
// duplicate entries; that delete is expected to fail on a clean machine and is
// ignored.
//
// A failure here is reported but never fatal: the service is still installed
// and usable from loopback, and an operator can add the rule by hand. Failing
// the whole install because a firewall rule could not be written would be a
// worse trade — netsh needs elevation, and not every install runs elevated.
func addFirewallRule(execPath, profiles string, dryRun bool, out io.Writer) {
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would run: netsh %s\n", joinArgs(firewallDeleteArgs()))
		fmt.Fprintf(out, "[dry-run] would run: netsh %s\n", joinArgs(firewallAddArgs(execPath, profiles)))
		return
	}
	_ = run("netsh", firewallDeleteArgs()...) // no rule yet on a clean machine
	if err := run("netsh", firewallAddArgs(execPath, profiles)...); err != nil {
		fmt.Fprintf(out, "Warning: could not create the Windows Firewall rule (%v).\n", err)
		fmt.Fprintln(out, "Run the installer from an elevated prompt, or add it manually:")
		fmt.Fprintf(out, "  netsh %s\n", joinArgs(firewallAddArgs(execPath, profiles)))
		return
	}
	fmt.Fprintf(out, "Created Windows Firewall rule %q for %s (profiles: %s).\n",
		firewallRuleName, execPath, profilesOrDefault(profiles))
	fmt.Fprintln(out, "VORTEX will not prompt to allow network access, so it can start unattended.")
}

// removeFirewallRule deletes the rule created by addFirewallRule. Like the
// add path it is best-effort: an already-absent rule is not an error.
func removeFirewallRule(dryRun bool, out io.Writer) {
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would run: netsh %s\n", joinArgs(firewallDeleteArgs()))
		return
	}
	if err := run("netsh", firewallDeleteArgs()...); err != nil {
		fmt.Fprintf(out, "Note: no Windows Firewall rule named %q to remove.\n", firewallRuleName)
		return
	}
	fmt.Fprintf(out, "Removed Windows Firewall rule %q.\n", firewallRuleName)
}

// profilesOrDefault renders the profile list actually used.
func profilesOrDefault(profiles string) string {
	if profiles == "" {
		return defaultFirewallProfiles
	}
	return profiles
}

// joinArgs renders args for display. netsh arguments contain spaces (the
// description), so they are shown quoted to stay copy-pasteable.
func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		if containsSpace(a) {
			out += `"` + a + `"`
			continue
		}
		out += a
	}
	return out
}

func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' {
			return true
		}
	}
	return false
}
