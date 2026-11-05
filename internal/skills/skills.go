// Package skills loads Agent Skills (the open SKILL.md format, agentskills.io)
// so the agent can use procedural know-how written for Claude Code, Codex,
// OpenCode and others unchanged.
//
// A skill is knowledge, never authority: loading one runs nothing, grants no
// tool and changes no policy. Only the name and description are shown to the
// model up front (progressive disclosure); the body is read when a skill is
// activated. allowed-tools is recorded as a request that policy may narrow.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vortex-run/vortex/internal/userdir"
)

// Skill is one installed skill.
type Skill struct {
	Name          string            // spec: required; [a-z0-9-], ≤64 chars, matches its directory
	Description   string            // spec: required; ≤1024 chars; the only text shown before activation
	License       string            // spec: optional
	Compatibility string            // spec: optional, free text
	Metadata      map[string]string // spec: optional, untrusted
	AllowedTools  []string          // spec (experimental): a request, never a grant
	Dir           string            // skill directory
	Scope         string            // where it was found (e.g. "project", "user")
	Hash          string            // sha256 of SKILL.md, for pinning and audit
}

// Limits from the Agent Skills specification.
const (
	maxNameLen        = 64
	maxDescriptionLen = 1024
	maxSkillFileBytes = 256 << 10 // generous; keeps a hostile file from flooding context
)

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse reads a SKILL.md document found in dir.
func Parse(doc []byte, dir, scope string) (Skill, error) {
	if len(doc) > maxSkillFileBytes {
		return Skill{}, fmt.Errorf("SKILL.md is larger than %d bytes", maxSkillFileBytes)
	}
	front, _, err := splitFrontmatter(string(doc))
	if err != nil {
		return Skill{}, err
	}
	fields, meta, err := parseFrontmatter(front)
	if err != nil {
		return Skill{}, fmt.Errorf("frontmatter: %w", err)
	}
	sum := sha256.Sum256(doc)
	s := Skill{
		Name:          fields["name"],
		Description:   strings.TrimSpace(fields["description"]),
		License:       fields["license"],
		Compatibility: fields["compatibility"],
		Metadata:      meta,
		AllowedTools:  strings.Fields(fields["allowed-tools"]),
		Dir:           dir,
		Scope:         scope,
		Hash:          hex.EncodeToString(sum[:]),
	}
	if err := s.validate(); err != nil {
		return Skill{}, err
	}
	return s, nil
}

func (s Skill) validate() error {
	switch {
	case s.Name == "":
		return errors.New("missing required field 'name'")
	case len(s.Name) > maxNameLen || !nameRE.MatchString(s.Name):
		return fmt.Errorf("name %q must be 1-%d lowercase letters, digits and single hyphens", s.Name, maxNameLen)
	case s.Dir != "" && filepath.Base(s.Dir) != s.Name:
		return fmt.Errorf("name %q must match its directory %q", s.Name, filepath.Base(s.Dir))
	case s.Description == "":
		return errors.New("missing required field 'description'")
	case len(s.Description) > maxDescriptionLen:
		return fmt.Errorf("description is longer than %d characters", maxDescriptionLen)
	}
	return nil
}

// Body reads the skill's instructions (everything after the frontmatter).
// Called only when the skill is activated.
func (s Skill) Body() (string, error) {
	doc, err := readSkillFile(filepath.Join(s.Dir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(doc)
	if hex.EncodeToString(sum[:]) != s.Hash {
		return "", fmt.Errorf("skill %q changed on disk since it was loaded; reload before use", s.Name)
	}
	_, body, err := splitFrontmatter(string(doc))
	return strings.TrimSpace(body), err
}

func readSkillFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path) // no symlinked SKILL.md
	}
	if fi.Size() > maxSkillFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxSkillFileBytes)
	}
	return os.ReadFile(path) //nolint:gosec // path built from a scanned skill root
}

// Root is a directory that contains skill directories, with a scope label.
type Root struct {
	Dir   string
	Scope string
}

// Problem is a skill that failed to load; reported, never fatal.
type Problem struct {
	Path string
	Err  error
}

// Discover loads every <root>/<name>/SKILL.md. Later roots override earlier
// ones with the same name (list roots lowest precedence first). Invalid
// skills are returned as problems and skipped.
func Discover(roots []Root) ([]Skill, []Problem) {
	byName := map[string]Skill{}
	var problems []Problem
	for _, r := range roots {
		entries, err := os.ReadDir(r.Dir)
		if err != nil {
			continue // a missing root is normal
		}
		for _, e := range entries {
			if !e.IsDir() { // DirEntry.IsDir is false for a symlink to a dir: not followed
				continue
			}
			dir := filepath.Join(r.Dir, e.Name())
			path := filepath.Join(dir, "SKILL.md")
			doc, err := readSkillFile(path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					problems = append(problems, Problem{Path: path, Err: err})
				}
				continue
			}
			s, err := Parse(doc, dir, r.Scope)
			if err != nil {
				problems = append(problems, Problem{Path: path, Err: err})
				continue
			}
			byName[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// DefaultRoots are the conventional skill locations for a workspace and a
// user config dir, lowest precedence first: user, then project. The
// .claude/.agents/.opencode directories are read so skills written for those
// tools work unchanged.
func DefaultRoots(workspace, userConfigDir string) []Root {
	var roots []Root
	if userConfigDir != "" {
		roots = append(roots, Root{Dir: filepath.Join(userdir.App(userConfigDir), "skills"), Scope: "user"})
	}
	if workspace != "" {
		for _, d := range []string{".opencode/skills", ".claude/skills", ".agents/skills"} {
			roots = append(roots, Root{Dir: filepath.Join(workspace, filepath.FromSlash(d)), Scope: "project"})
		}
	}
	return roots
}

// Catalog renders the discovery text for a system prompt: name and
// description only, framed as reference material the model may consult.
// Paths are shown relative to base when they are inside it.
func Catalog(skills []Skill, base string) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills available (procedures you can follow; they grant no permissions). ")
	b.WriteString("To use one, read its SKILL.md with read_file before acting:\n")
	for _, s := range skills {
		path := filepath.Join(s.Dir, "SKILL.md")
		if base != "" {
			if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", s.Name, oneLine(s.Description), filepath.ToSlash(path))
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
