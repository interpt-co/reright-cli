// Package toggle holds the user's choice to switch some of reright's checks
// off, for a while or in one directory. The state is one JSON
// file that only the reright command writes. The hook reads it on every call,
// so a change takes effect in the running session with no restart.
package toggle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checks are the groups of text the user can switch off. "all" in a command means every one of them.
var Checks = []string{"commit", "gh", "email"}

// Descriptions say what each check covers, for status output and help.
var Descriptions = map[string]string{
	"commit": "git commit and tag messages",
	"gh":     "gh pr, issue, release and api text",
	"email":  "Gmail send, draft, reply and forward",
}

// Rule switches one check off. Until is zero for "until I turn it back on".
// Dirs is empty for everywhere, otherwise only calls whose working directory is inside one of them.
type Rule struct {
	Until time.Time `json:"until,omitempty"`
	Dirs  []string  `json:"dirs,omitempty"`
}

// File is the on-disk state.
type File struct {
	Disabled map[string]Rule `json:"disabled,omitempty"`
}

// Path is where the state lives inside the reright config directory.
func Path(cfgDir string) string { return filepath.Join(cfgDir, "enforcement.json") }

// Valid reports whether name is a check.
func Valid(name string) bool {
	for _, k := range Checks {
		if k == name {
			return true
		}
	}
	return false
}

// Expand turns the names a user typed into checks. "all" means every check.
func Expand(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		switch {
		case n == "all":
			for _, k := range Checks {
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		case Valid(n):
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		default:
			return nil, fmt.Errorf("unknown check %q (want %s or all)", n, strings.Join(Checks, ", "))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("name at least one check (%s or all)", strings.Join(Checks, ", "))
	}
	return out, nil
}

// Load reads the state. A missing file is an empty state. A damaged file is an
// error, and callers must then keep enforcing. Entries for checks this version does not
// have (browser and http in older versions) are dropped.
func Load(cfgDir string) (File, error) {
	b, err := os.ReadFile(Path(cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("%s is not valid JSON: %w", Path(cfgDir), err)
	}
	for k := range f.Disabled {
		if !Valid(k) {
			delete(f.Disabled, k)
		}
	}
	return f, nil
}

func save(cfgDir string, f File) error {
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return err
	}
	if len(f.Disabled) == 0 {
		err := os.Remove(Path(cfgDir))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cfgDir, ".enforcement-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path(cfgDir))
}

// Off reports whether check is switched off for a call made in cwd at now.
func (f File) Off(check, cwd string, now time.Time) bool {
	r, ok := f.Disabled[check]
	if !ok {
		return false
	}
	if !r.Until.IsZero() && !now.Before(r.Until) {
		return false
	}
	if len(r.Dirs) == 0 {
		return true
	}
	if cwd == "" {
		return false
	}
	for _, d := range r.Dirs {
		if inside(d, cwd) {
			return true
		}
	}
	return false
}

func inside(dir, cwd string) bool {
	dir, cwd = filepath.Clean(dir), filepath.Clean(cwd)
	if cwd == dir {
		return true
	}
	rel, err := filepath.Rel(dir, cwd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Disable switches checks off. Dirs must be absolute. A check that is already off is replaced.
func Disable(cfgDir string, checks []string, until time.Time, dirs []string) error {
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			return fmt.Errorf("directory %q is not absolute", d)
		}
	}
	f, err := Load(cfgDir)
	if err != nil {
		// Replacing a damaged file is what the user asked for.
		f = File{}
	}
	if f.Disabled == nil {
		f.Disabled = map[string]Rule{}
	}
	for _, k := range checks {
		f.Disabled[k] = Rule{Until: until, Dirs: dirs}
	}
	return save(cfgDir, f)
}

// Enable switches checks back on. It also clears expired rules.
func Enable(cfgDir string, checks []string, now time.Time) error {
	f, err := Load(cfgDir)
	if err != nil {
		f = File{}
	}
	for _, k := range checks {
		delete(f.Disabled, k)
	}
	for k, r := range f.Disabled {
		if !r.Until.IsZero() && !now.Before(r.Until) {
			delete(f.Disabled, k)
		}
	}
	return save(cfgDir, f)
}

// Lines describes the state, one line per check, for `reright status`.
func (f File) Lines(now time.Time) []string {
	var out []string
	for _, k := range Checks {
		line := fmt.Sprintf("%-8s on   %s", k, Descriptions[k])
		if r, ok := f.Disabled[k]; ok && (r.Until.IsZero() || now.Before(r.Until)) {
			line = fmt.Sprintf("%-8s OFF  %s", k, scope(r, now))
		}
		out = append(out, line)
	}
	return out
}

func scope(r Rule, now time.Time) string {
	parts := []string{}
	if r.Until.IsZero() {
		parts = append(parts, "until you turn it back on")
	} else {
		parts = append(parts, "until "+r.Until.Local().Format("Mon 15:04")+" ("+r.Until.Sub(now).Round(time.Minute).String()+" left)")
	}
	if len(r.Dirs) > 0 {
		d := append([]string(nil), r.Dirs...)
		sort.Strings(d)
		parts = append(parts, "only in "+strings.Join(d, ", "))
	}
	return strings.Join(parts, ", ")
}
