package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestKeys_CommandRegisters(t *testing.T) {
	root := NewRootCommand()
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "keys" {
			found = true
		}
	}
	if !found {
		t.Error("keys should be registered on the root command")
	}
}

func TestKeys_Subcommands(t *testing.T) {
	c := newKeysCommand()
	want := map[string]bool{"add": false, "list": false, "remove": false, "status": false, "mode": false, "test": false}
	for _, sub := range c.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("keys is missing the %q subcommand", name)
		}
	}
}

// findSub returns the named subcommand of c, or nil.
func findSub(c *cobra.Command, name string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

func TestKeys_AddFlags(t *testing.T) {
	add := findSub(newKeysCommand(), "add")
	if add == nil {
		t.Fatal("add subcommand not found")
	}
	for _, flag := range []string{"provider", "key", "model", "priority", "budget", "label"} {
		if add.Flags().Lookup(flag) == nil {
			t.Errorf("add command missing --%s flag", flag)
		}
	}
}

func TestKeys_RemoveAndModeRequireArgs(t *testing.T) {
	rm := findSub(newKeysCommand(), "remove")
	if rm == nil || rm.Args == nil {
		t.Fatal("remove should declare an Args validator")
	}
	// remove takes exactly one arg.
	if err := rm.Args(rm, []string{}); err == nil {
		t.Error("remove with no args should error")
	}
	if err := rm.Args(rm, []string{"slot-1"}); err != nil {
		t.Errorf("remove with one arg should be valid: %v", err)
	}
	mode := findSub(newKeysCommand(), "mode")
	if mode == nil || mode.Args == nil {
		t.Fatal("mode should declare an Args validator")
	}
	if err := mode.Args(mode, []string{}); err == nil {
		t.Error("mode with no args should error")
	}
}

func TestKeys_TestAcceptsOptionalArg(t *testing.T) {
	tc := findSub(newKeysCommand(), "test")
	if tc == nil || tc.Args == nil {
		t.Fatal("test should declare an Args validator")
	}
	if err := tc.Args(tc, []string{}); err != nil {
		t.Errorf("test with no args (all slots) should be valid: %v", err)
	}
	if err := tc.Args(tc, []string{"slot-1"}); err != nil {
		t.Errorf("test with one arg should be valid: %v", err)
	}
	if err := tc.Args(tc, []string{"a", "b"}); err == nil {
		t.Error("test with two args should error")
	}
}

func TestKeys_SlotIDHelpers(t *testing.T) {
	if got := normalizeSlotID("1"); got != "slot-1" {
		t.Errorf("normalizeSlotID(1) = %q", got)
	}
	if got := normalizeSlotID("slot-3"); got != "slot-3" {
		t.Errorf("normalizeSlotID(slot-3) = %q", got)
	}
	if got := slotShort("slot-2"); got != "2" {
		t.Errorf("slotShort(slot-2) = %q", got)
	}
	if got := slotNumber("slot-4"); got != 4 {
		t.Errorf("slotNumber(slot-4) = %d", got)
	}
}

func TestKeys_ModePersistence(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := loadKeysMode(); got != "auto" {
		t.Errorf("default mode = %q, want auto", got)
	}
	if err := saveKeysMode("quality"); err != nil {
		t.Fatalf("saveKeysMode: %v", err)
	}
	if got := loadKeysMode(); got != "quality" {
		t.Errorf("loaded mode = %q, want quality", got)
	}
}

func TestKeys_TitleProvider(t *testing.T) {
	cases := map[string]string{"deepseek": "DeepSeek", "openai": "OpenAI", "claude": "Claude", "": "—"}
	for in, want := range cases {
		if got := titleProvider(in); got != want {
			t.Errorf("titleProvider(%q) = %q, want %q", in, got, want)
		}
	}
}
