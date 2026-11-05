package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolSpecs_EveryLocalToolHasOne: a tool without a spec can't be offered
// to a model, so every local tool (normal and trusted sets) needs one, with a
// schema that serializes and whose required fields exist.
func TestToolSpecs_EveryLocalToolHasOne(t *testing.T) {
	cfg := LocalFSConfig{Root: t.TempDir()}
	sets := map[string][]Tool{"local": NewLocalTools(cfg), "trusted": NewTrustedLocalTools(cfg)}
	for set, tools := range sets {
		for _, tool := range tools {
			spec, ok := SpecFor(tool)
			if !ok {
				t.Errorf("%s tool %q has no ToolSpec", set, tool.Name())
				continue
			}
			if spec.Name != tool.Name() || spec.Description == "" || spec.Risk == "" {
				t.Errorf("%s: incomplete spec %+v", tool.Name(), spec)
			}
			if _, err := json.Marshal(spec.Parameters); err != nil {
				t.Errorf("%s: schema doesn't serialize: %v", tool.Name(), err)
			}
			props, _ := spec.Parameters["properties"].(map[string]any)
			req, _ := spec.Parameters["required"].([]string)
			for _, r := range req {
				if _, ok := props[r]; !ok {
					t.Errorf("%s: required %q not in properties", tool.Name(), r)
				}
			}
			if _, ok := props["session_id"]; ok {
				t.Errorf("%s: session_id is runtime-supplied and must not be model-visible", tool.Name())
			}
		}
	}
}

// TestToolSpecs_SideEffectMatchesTool: the spec's SideEffect flag agrees
// with tools that declare SideEffecting() (used by the effect ledger).
func TestToolSpecs_SideEffectMatchesTool(t *testing.T) {
	for _, tool := range NewLocalTools(LocalFSConfig{Root: t.TempDir()}) {
		spec, _ := SpecFor(tool)
		if se, ok := tool.(interface{ SideEffecting() bool }); ok && se.SideEffecting() && !spec.SideEffect {
			t.Errorf("%s is side-effecting but its spec says SideEffect=false", tool.Name())
		}
	}
}

func TestToolRegistry_Specs(t *testing.T) {
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	specs := reg.Specs()
	if len(specs) != len(reg.List()) {
		t.Errorf("Specs() = %d, registered tools = %d", len(specs), len(reg.List()))
	}
	for i := 1; i < len(specs); i++ {
		if specs[i-1].Name > specs[i].Name {
			t.Fatal("Specs() not sorted by name")
		}
	}
}

func TestValidateArgs(t *testing.T) {
	spec, _ := SpecFor(WriteLocalFileTool{})
	commit, _ := SpecFor(GitCommitTool{})
	term, _ := SpecFor(RunTerminalTool{})
	cases := []struct {
		name string
		spec ToolSpec
		args map[string]any
		err  string // substring; "" = valid
	}{
		{"valid", spec, map[string]any{"path": "a.go", "content": "x", "create_dirs": true}, ""},
		{"missing required", spec, map[string]any{"path": "a.go"}, `missing required argument "content"`},
		{"unknown arg", spec, map[string]any{"path": "a", "content": "x", "mode": "0777"}, `unknown argument "mode"`},
		{"wrong type", spec, map[string]any{"path": 7.0, "content": "x"}, "want string"},
		{"array ok", commit, map[string]any{"message": "m", "files": []any{"a", "b"}}, ""},
		{"array bad item", commit, map[string]any{"message": "m", "files": []any{"a", 3.0}}, "item 1"},
		{"number ok", term, map[string]any{"command": "ls", "timeout": 30.0}, ""},
		{"session_id not accepted from model", spec, map[string]any{"path": "a", "content": "x", "session_id": "x"}, "unknown argument"},
	}
	for _, c := range cases {
		err := ValidateArgs(c.spec, c.args)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.err)
		}
	}
}
