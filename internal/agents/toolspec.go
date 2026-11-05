package agents

import (
	"fmt"
	"sort"
)

// ToolSpec describes a tool to a model and to the policy layer: a JSON
// Schema for its arguments plus how risky it is to run. It is the contract a
// model-driven loop advertises (native tool calling or a constrained text
// protocol) and validates proposed calls against before anything executes.
type ToolSpec struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object (type, properties, required). Keep
	// to the portable subset providers share: object, string, boolean,
	// number, integer, array of strings; no $ref, no oneOf.
	Parameters map[string]any
	Risk       RiskClass
	// SideEffect is true when running the tool changes state (files, git,
	// processes). Side-effecting calls go through approval and the effect
	// ledger.
	SideEffect bool
}

// RiskClass is a coarse, deterministic risk level for a tool. The policy
// layer decides on it; a model's own opinion of risk is never consulted.
type RiskClass string

// Risk classes, mildest first.
const (
	RiskRead           RiskClass = "read"            // read local state in scope
	RiskWorkspaceWrite RiskClass = "workspace-write" // reversible change inside the workspace
	RiskVCSWrite       RiskClass = "vcs-write"       // records history (git add/commit)
	RiskExec           RiskClass = "exec"            // runs arbitrary commands
)

// Specced is implemented by tools that describe themselves. Tools without it
// fall back to the built-in table below.
type Specced interface {
	Spec() ToolSpec
}

func obj(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// builtinSpecs covers the local tools (internal/agents local, git, search,
// undo). session_id is supplied by the runtime, never by the model, so it is
// not part of any schema.
var builtinSpecs = map[string]ToolSpec{
	"list_directory": {Description: "List the entries of a directory in the workspace.", Risk: RiskRead,
		Parameters: obj(nil, map[string]any{"path": str("Directory to list; defaults to the workspace root.")})},
	"read_file": {Description: "Read a text file in the workspace.", Risk: RiskRead,
		Parameters: obj([]string{"path"}, map[string]any{"path": str("File to read.")})},
	"write_file": {Description: "Create or overwrite a file with the given content.", Risk: RiskWorkspaceWrite, SideEffect: true,
		Parameters: obj([]string{"path", "content"}, map[string]any{
			"path":        str("File to write."),
			"content":     str("Full new content of the file."),
			"create_dirs": boolean("Create missing parent directories."),
		})},
	"edit_file": {Description: "Replace an exact snippet of a file with new text.", Risk: RiskWorkspaceWrite, SideEffect: true,
		Parameters: obj([]string{"path", "old_str", "new_str"}, map[string]any{
			"path":    str("File to edit."),
			"old_str": str("Exact text to replace; must occur in the file."),
			"new_str": str("Replacement text."),
		})},
	"run_terminal": {Description: "Run a shell command and return its output.", Risk: RiskExec, SideEffect: true,
		Parameters: obj([]string{"command"}, map[string]any{
			"command": str("Command line to run."),
			"cwd":     str("Working directory; defaults to the workspace root."),
			"timeout": map[string]any{"type": "number", "description": "Timeout in seconds."},
		})},
	"create_project": {Description: "Create a new project skeleton.", Risk: RiskWorkspaceWrite, SideEffect: true,
		Parameters: obj([]string{"name"}, map[string]any{
			"name":        str("Project name (directory)."),
			"type":        str("Project type, e.g. go, python, node."),
			"description": str("One-line description."),
			"path":        str("Parent directory; defaults to the workspace root."),
		})},
	"git_status": {Description: "Show the git branch and changed files.", Risk: RiskRead,
		Parameters: obj(nil, map[string]any{})},
	"git_diff": {Description: "Show the working-tree diff, optionally for one file.", Risk: RiskRead,
		Parameters: obj(nil, map[string]any{"file": str("Limit the diff to this file.")})},
	"git_add": {Description: "Stage files for commit.", Risk: RiskVCSWrite, SideEffect: true,
		Parameters: obj([]string{"files"}, map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to stage."},
		})},
	"git_commit": {Description: "Commit staged changes (optionally staging files first).", Risk: RiskVCSWrite, SideEffect: true,
		Parameters: obj([]string{"message"}, map[string]any{
			"message": str("Commit message."),
			"files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to stage before committing."},
		})},
	"search_files": {Description: "Search file contents in the workspace for a text pattern.", Risk: RiskRead,
		Parameters: obj([]string{"pattern"}, map[string]any{
			"pattern":        str("Text to search for."),
			"path":           str("Directory to search; defaults to the workspace root."),
			"extension":      str("Only files with this extension, e.g. go."),
			"case_sensitive": boolean("Match case exactly."),
		})},
	"find_files": {Description: "Find files whose name matches a glob.", Risk: RiskRead,
		Parameters: obj([]string{"name_pattern"}, map[string]any{
			"name_pattern": str("Glob for the file name, e.g. *_test.go."),
			"path":         str("Directory to search; defaults to the workspace root."),
		})},
	"undo": {Description: "Restore the file most recently overwritten in this session.", Risk: RiskWorkspaceWrite, SideEffect: true,
		Parameters: obj(nil, map[string]any{})},
}

// SpecFor returns the spec for t, from the tool itself or the built-in table.
func SpecFor(t Tool) (ToolSpec, bool) {
	if s, ok := t.(Specced); ok {
		return s.Spec(), true
	}
	s, ok := builtinSpecs[t.Name()]
	if !ok {
		return ToolSpec{}, false
	}
	s.Name = t.Name()
	return s, true
}

// Specs returns the specs of every registered tool that has one, sorted by
// name. Tools without a spec can't be offered to a model.
func (r *ToolRegistry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		if s, ok := SpecFor(t); ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ValidateArgs checks a model-proposed argument map against the spec's
// schema: required fields present, no unknown fields, and each value of the
// declared type. Models get arguments wrong regularly; the runtime rejects
// them itself rather than trusting a provider's "strict" mode.
func ValidateArgs(spec ToolSpec, args map[string]any) error {
	props, _ := spec.Parameters["properties"].(map[string]any)
	if req, ok := spec.Parameters["required"].([]string); ok {
		for _, name := range req {
			if _, present := args[name]; !present {
				return fmt.Errorf("%s: missing required argument %q", spec.Name, name)
			}
		}
	}
	for name, v := range args {
		p, known := props[name].(map[string]any)
		if !known {
			return fmt.Errorf("%s: unknown argument %q", spec.Name, name)
		}
		if err := checkType(p, v); err != nil {
			return fmt.Errorf("%s: argument %q: %w", spec.Name, name, err)
		}
	}
	return nil
}

func checkType(schema map[string]any, v any) error {
	want, _ := schema["type"].(string)
	switch want {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("want string, got %T", v)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", v)
		}
	case "number", "integer":
		switch n := v.(type) {
		case float64:
			if want == "integer" && n != float64(int64(n)) {
				return fmt.Errorf("want integer, got %v", n)
			}
		case int, int64:
		default:
			return fmt.Errorf("want %s, got %T", want, v)
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			if _, isStrs := v.([]string); isStrs {
				return nil
			}
			return fmt.Errorf("want array, got %T", v)
		}
		item, _ := schema["items"].(map[string]any)
		for i, e := range items {
			if err := checkType(item, e); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
	}
	return nil
}
