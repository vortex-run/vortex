package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"

	schema "github.com/vortex-run/vortex/config"
)

// schemaFile is the embedded schema's name, used in error messages.
const schemaFile = schema.Filename

// schemaDefinition is the top-level definition in schema.cue that user config
// is unified against.
const schemaDefinition = "#Config"

// schemaBytes returns the embedded master schema source. Used by Load and by
// tests that exercise loadFromBytes directly.
func schemaBytes() []byte { return schema.Source }

// LoadError is a structured configuration error carrying, where available, the
// source file, line, column, and a human-readable message. Its Error() string
// is formatted for direct display to an operator.
type LoadError struct {
	Path    string // the config file the problem relates to
	Line    int    // 1-based line; 0 if unknown
	Column  int    // 1-based column; 0 if unknown
	Field   string // CUE field path, e.g. "cluster.name"; may be empty
	Message string // human-readable description
}

func (e *LoadError) Error() string {
	loc := e.Path
	if e.Line > 0 {
		loc += ":" + strconv.Itoa(e.Line)
		if e.Column > 0 {
			loc += ":" + strconv.Itoa(e.Column)
		}
	}
	var b strings.Builder
	b.WriteString("config error")
	if loc != "" {
		b.WriteString(" at " + loc)
	}
	if e.Field != "" {
		b.WriteString(" (field " + e.Field + ")")
	}
	b.WriteString(": " + e.Message)
	return b.String()
}

// LoadErrors is a sorted collection of LoadError. Load returns this type so a
// caller can report every problem at once rather than one per run.
type LoadErrors []*LoadError

func (es LoadErrors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n")
}

// Load reads and validates the config file at path, applies environment
// variable overrides, and returns a fully-typed *Config. On any validation
// problem it returns a LoadErrors describing each issue with file:line:field.
//
// path may be "" to use DefaultPath.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}

	userSrc, err := os.ReadFile(path)
	if err != nil {
		return nil, &LoadError{Path: path, Message: "cannot read config file: " + err.Error()}
	}

	return loadFromBytes(path, userSrc, schema.Source, osEnv())
}

// loadFromBytes is the testable core: it takes the config source, the schema
// source, and an environment-variable lookup map, and produces a *Config or a
// LoadErrors. Keeping it pure (no filesystem/process access) makes the
// validation and override logic directly testable.
func loadFromBytes(path string, userSrc, schemaSrc []byte, env map[string]string) (*Config, error) {
	ctx := cuecontext.New()

	schemaVal := ctx.CompileBytes(schemaSrc, cue.Filename(schemaFile))
	if err := schemaVal.Err(); err != nil {
		return nil, toLoadErrors(schemaFile, err)
	}
	def := schemaVal.LookupPath(cue.ParsePath(schemaDefinition))
	if err := def.Err(); err != nil {
		return nil, toLoadErrors(schemaFile, err)
	}

	userVal := ctx.CompileBytes(userSrc, cue.Filename(path))
	if err := userVal.Err(); err != nil {
		return nil, toLoadErrors(path, err)
	}

	// Unify user config with the schema: type/constraint violations and unknown
	// fields surface here, and schema defaults fill in unset optional fields.
	unified := def.Unify(userVal)
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		return nil, toLoadErrors(path, err)
	}

	var cfg Config
	if err := unified.Decode(&cfg); err != nil {
		return nil, toLoadErrors(path, err)
	}

	applyEnvOverrides(&cfg, env)

	if err := cfg.computeHash(); err != nil {
		return nil, &LoadError{Path: path, Message: err.Error()}
	}
	return &cfg, nil
}

// toLoadErrors converts a CUE error into a sorted LoadErrors, extracting source
// positions and field paths where CUE provides them.
func toLoadErrors(path string, err error) LoadErrors {
	var out LoadErrors
	for _, e := range cueerrors.Errors(err) {
		le := &LoadError{Path: path, Message: e.Error()}
		if pos := e.Position(); pos.IsValid() {
			if pos.Filename() != "" {
				le.Path = pos.Filename()
			}
			le.Line = pos.Line()
			le.Column = pos.Column()
		}
		if p := e.Path(); len(p) > 0 {
			le.Field = strings.Join(p, ".")
			// CUE's generic message often repeats the path; keep it concise.
			le.Message = cleanMessage(e)
		}
		out = append(out, le)
	}
	if len(out) == 0 {
		out = append(out, &LoadError{Path: path, Message: err.Error()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// cleanMessage renders a CUE error's message via its format args, which is
// generally tighter than the default Error() string.
func cleanMessage(e cueerrors.Error) string {
	format, args := e.Msg()
	if format == "" {
		return e.Error()
	}
	return fmt.Sprintf(format, args...)
}
