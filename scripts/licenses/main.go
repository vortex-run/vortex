// Command licenses writes THIRD_PARTY_LICENSES: the license (and NOTICE)
// texts of every Go module compiled into the vortex binary and every
// production npm package bundled into the embedded dashboard. MIT and BSD
// require their notices to ship with binaries, and Apache-2.0 requires
// NOTICE files to be passed on, so release archives include this file.
//
// It fails when a dependency ships no license file, unless the module is in
// knownLicenseless with a reason; a silent gap would ship a non-compliant
// release.
//
// Usage (from the repository root; the release hooks run it):
//
//	go run ./scripts/licenses -o THIRD_PARTY_LICENSES
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// knownLicenseless lists dependencies that ship no top-level license file,
// with where their license is declared. Their README is included instead,
// plus any license files in subdirectories (vendored components).
var knownLicenseless = map[string]string{
	"github.com/mattn/go-localereader": "MIT, declared in the module's README (no LICENSE file is shipped)",
	"victory-vendor":                   "MIT AND ISC: MIT per its README; vendored d3 packages carry their own ISC licenses under lib-vendor/",
}

// maxNestedDepth bounds the subdirectory search for vendored license files.
const maxNestedDepth = 3

// dep is one third-party component and the files that carry its notices.
type dep struct {
	Kind    string // "go" or "npm"
	Name    string
	Version string
	Note    string
	Files   []string // absolute paths, sorted
}

func main() {
	out := flag.String("o", "THIRD_PARTY_LICENSES", "output file")
	pkg := flag.String("pkg", "./cmd/gubern", "Go package whose dependencies are listed")
	dash := flag.String("dashboard", "web/dashboard", "dashboard directory (package-lock.json + node_modules)")
	flag.Parse()

	goDeps, err := goDependencies(*pkg)
	if err != nil {
		fatal(err)
	}
	npmDeps, err := npmDependencies(*dash)
	if err != nil {
		fatal(err)
	}
	var buf bytes.Buffer
	if err := render(&buf, append(goDeps, npmDeps...)); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("licenses: wrote %s (%d Go modules, %d npm packages)\n", *out, len(goDeps), len(npmDeps))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "licenses:", err)
	os.Exit(1)
}

// goDependencies lists the non-main modules linked into pkg.
func goDependencies(pkg string) ([]dep, error) {
	cmd := exec.Command("go", "list", "-deps", "-f",
		"{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", pkg)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	return parseGoList(raw)
}

// parseGoList turns `go list` lines into deps, deduplicated by module.
func parseGoList(raw []byte) ([]dep, error) {
	seen := map[string]bool{}
	var deps []dep
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		d, err := withNotices(dep{Kind: "go", Name: parts[0], Version: parts[1]}, parts[2])
		if err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, sc.Err()
}

// npmDependencies lists production (non-dev) packages from package-lock.json,
// reading their notices from node_modules. Dev-only packages are build tools
// that don't ship in the bundle.
func npmDependencies(dir string) ([]dep, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil, err
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			License string `json:"license"`
			Dev     bool   `json:"dev"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("package-lock.json: %w", err)
	}
	var deps []dep
	for path, p := range lock.Packages {
		if path == "" || p.Dev || !strings.HasPrefix(path, "node_modules/") {
			continue
		}
		name := path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		d, err := withNotices(dep{Kind: "npm", Name: name, Version: p.Version, Note: p.License},
			filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("%w (run `npm ci` in %s first)", err, dir)
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// noticeFile reports whether name is a license or notice file.
func noticeFile(name string) bool {
	n := strings.ToUpper(name)
	for _, p := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// withNotices fills d.Files from the top level of dir. A dependency with no
// license file is an error unless it is in knownLicenseless, in which case its
// README stands in.
func withNotices(d dep, dir string) (dep, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return d, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
	}
	var readme string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch {
		case noticeFile(e.Name()):
			d.Files = append(d.Files, filepath.Join(dir, e.Name()))
		case strings.HasPrefix(strings.ToUpper(e.Name()), "README"):
			readme = filepath.Join(dir, e.Name())
		}
	}
	if len(d.Files) == 0 {
		reason, ok := knownLicenseless[d.Name]
		if !ok || readme == "" {
			return d, fmt.Errorf("%s %s@%s ships no license file", d.Kind, d.Name, d.Version)
		}
		nested, err := nestedNotices(dir)
		if err != nil {
			return d, err
		}
		d.Note, d.Files = reason, append([]string{readme}, nested...)
	}
	sort.Strings(d.Files)
	return d, nil
}

// nestedNotices finds license files below dir (not at its top level), up to
// maxNestedDepth deep, skipping nested node_modules.
func nestedNotices(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if e.IsDir() {
			if e.Name() == "node_modules" || depth > maxNestedDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > 1 && noticeFile(e.Name()) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// noticeLabel names a notice file by its last two path elements, so vendored
// files read "d3-array/LICENSE" rather than a bare "LICENSE".
func noticeLabel(path string) string {
	return filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
}

// render writes the notices file, sorted for reproducible output.
func render(w *bytes.Buffer, deps []dep) error {
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].Kind != deps[j].Kind {
			return deps[i].Kind < deps[j].Kind
		}
		return deps[i].Name < deps[j].Name
	})
	fmt.Fprintf(w, "Third-party software notices for Gubern\n\n")
	fmt.Fprintf(w, "Gubern is licensed under the Apache License 2.0 (see LICENSE).\n")
	fmt.Fprintf(w, "It includes the following third-party components, whose license\n")
	fmt.Fprintf(w, "and notice texts are reproduced below. Generated by scripts/licenses.\n\n")
	for _, d := range deps {
		fmt.Fprintf(w, "%s\n%s %s %s\n", strings.Repeat("=", 78), d.Kind, d.Name, d.Version)
		if d.Note != "" {
			fmt.Fprintf(w, "Declared license: %s\n", d.Note)
		}
		for _, f := range d.Files {
			text, err := os.ReadFile(f)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("%s vanished while writing", f)
				}
				return err
			}
			fmt.Fprintf(w, "%s\n-- %s --\n\n%s\n", strings.Repeat("-", 78), noticeLabel(f),
				strings.TrimRight(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n"))
		}
		w.WriteString("\n")
	}
	return nil
}
