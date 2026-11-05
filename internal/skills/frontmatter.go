package skills

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The Agent Skills frontmatter has six fields: name, description, license,
// compatibility, metadata (a map) and allowed-tools. Rather than take on a
// YAML dependency for that, parseFrontmatter reads the subset those fields
// need — plain/quoted scalars, | and > block scalars, and a one-level
// metadata map — and rejects anything else instead of guessing.

var errNoFrontmatter = errors.New("SKILL.md must start with a '---' frontmatter block")

// splitFrontmatter separates the frontmatter from the body.
func splitFrontmatter(doc string) (front, body string, err error) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")
	doc = strings.TrimPrefix(doc, string(rune(0xFEFF))) // UTF-8 BOM
	if !strings.HasPrefix(doc, "---\n") {
		return "", "", errNoFrontmatter
	}
	rest := doc[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", errors.New("SKILL.md frontmatter has no closing '---'")
	}
	front = rest[:end]
	body = rest[end+len("\n---"):]
	body = strings.TrimPrefix(strings.TrimLeft(body, "-"), "\n")
	return front, body, nil
}

// parseFrontmatter returns top-level scalar fields and the metadata map.
func parseFrontmatter(front string) (map[string]string, map[string]string, error) {
	fields := map[string]string{}
	meta := map[string]string{}
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			return nil, nil, fmt.Errorf("line %d: unexpected indentation", i+1)
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, fmt.Errorf("line %d: expected 'key: value'", i+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if _, dup := fields[key]; dup {
			return nil, nil, fmt.Errorf("line %d: duplicate key %q", i+1, key)
		}
		switch {
		case key == "metadata" && val == "":
			// One-level map of indented "k: v" lines.
			for i+1 < len(lines) && isIndented(lines[i+1]) {
				i++
				k, v, ok := strings.Cut(strings.TrimSpace(lines[i]), ":")
				if !ok {
					return nil, nil, fmt.Errorf("line %d: expected 'key: value' in metadata", i+1)
				}
				s, err := scalar(strings.TrimSpace(v))
				if err != nil {
					return nil, nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				meta[strings.TrimSpace(k)] = s
			}
			fields[key] = ""
		case val == "|" || val == ">" || val == "|-" || val == ">-":
			var block []string
			for i+1 < len(lines) && (isIndented(lines[i+1]) || strings.TrimSpace(lines[i+1]) == "") {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			for len(block) > 0 && block[len(block)-1] == "" {
				block = block[:len(block)-1]
			}
			if strings.HasPrefix(val, ">") {
				fields[key] = foldLines(block)
			} else {
				fields[key] = strings.Join(block, "\n")
			}
		case strings.HasPrefix(val, "[") || strings.HasPrefix(val, "{") || strings.HasPrefix(val, "&") || strings.HasPrefix(val, "*"):
			return nil, nil, fmt.Errorf("line %d: %q: flow collections, anchors and aliases aren't supported", i+1, key)
		default:
			s, err := scalar(val)
			if err != nil {
				return nil, nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			fields[key] = s
		}
	}
	return fields, meta, nil
}

func isIndented(line string) bool {
	return len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && strings.TrimSpace(line) != ""
}

// foldLines joins a > block: lines join with spaces, blank lines become breaks.
func foldLines(block []string) string {
	var out []string
	var para []string
	for _, l := range block {
		if l == "" {
			out = append(out, strings.Join(para, " "))
			para = nil
			continue
		}
		para = append(para, l)
	}
	out = append(out, strings.Join(para, " "))
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// scalar decodes a plain, single- or double-quoted value.
func scalar(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, `"`):
		s, err := strconv.Unquote(v)
		if err != nil {
			return "", fmt.Errorf("bad double-quoted string %s", v)
		}
		return s, nil
	case strings.HasPrefix(v, "'"):
		if len(v) < 2 || !strings.HasSuffix(v, "'") {
			return "", fmt.Errorf("bad single-quoted string %s", v)
		}
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'"), nil
	default:
		if i := strings.Index(v, " #"); i >= 0 { // trailing comment
			v = strings.TrimSpace(v[:i])
		}
		return v, nil
	}
}
