package githook

// What reright 0.1 installed, kept here so the tests can set up a machine the way an old version left it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type legacyResult struct {
	HooksDir          string
	Files             []string
	Activated         bool
	AlreadyInstalled  bool
	ExistingHooksPath string
	Manual            string
	ManualDir         string
}

func legacyInstall(homeDir, hookBinaryPath string) (legacyResult, error) {
	cfgDir, hooksDir, stateFile := paths(homeDir)
	res := legacyResult{HooksDir: hooksDir}
	if homeDir == "" || hookBinaryPath == "" {
		return res, errors.New("githook: home directory and hook binary path are required")
	}
	if !filepath.IsAbs(hookBinaryPath) {
		return res, fmt.Errorf("githook: the hook binary path %q must be absolute", hookBinaryPath)
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return res, err
	}
	st, err := readState(stateFile)
	if err != nil {
		return res, fmt.Errorf("%w. Run reright uninstall to clean up, then install again", err)
	}
	res.AlreadyInstalled = st != nil

	names := allHookNames()
	for _, name := range names {
		p := filepath.Join(hooksDir, name)
		if b, err := os.ReadFile(p); err == nil && !bytes.Contains(b, []byte(scriptMarker)) {
			return res, fmt.Errorf("githook: %s exists and was not written by reright, not overwriting it", p)
		}
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return res, err
	}
	cfgFileJSON := filepath.Join(cfgDir, "git.json")
	for _, name := range names {
		p := filepath.Join(hooksDir, name)
		if err := os.WriteFile(p, []byte(wrapperScript(name, hookBinaryPath, cfgFileJSON)), 0o755); err != nil {
			return res, err
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return res, err
		}
		res.Files = append(res.Files, p)
	}

	if st == nil {
		st = &state{}
	}
	st.HooksDir, st.Binary, st.Files = hooksDir, hookBinaryPath, res.Files

	existing, err := globalHooksPath(homeDir)
	if err != nil {
		return res, err
	}
	res.ExistingHooksPath = existing
	switch {
	case existing == "":
		cfgFile := globalConfigFile(homeDir)
		before, readErr := os.ReadFile(cfgFile)
		st.ConfigFile, st.ConfigExisted, st.ConfigBefore = cfgFile, readErr == nil, before
		if err := os.MkdirAll(filepath.Dir(cfgFile), 0o755); err != nil {
			return res, err
		}
		if out, err := exec.Command("git", "config", "--file", cfgFile, "core.hooksPath", hooksDir).CombinedOutput(); err != nil {
			return res, fmt.Errorf("githook: setting core.hooksPath: %v: %s", err, out)
		}
		after, _ := os.ReadFile(cfgFile)
		st.ConfigAfter = sum(after)
		st.Activated = true
		res.Activated = true
	case sameDir(existing, hooksDir, homeDir):
		res.Activated = true
	default:
		res.ExistingHooksPath = existing
		res.Manual = manualStep(existing, homeDir, hookBinaryPath)
		res.ManualDir = expandHome(existing, homeDir)
		st.ManualDir = res.ManualDir
	}

	if err := writeState(stateFile, st); err != nil {
		return res, err
	}
	return res, nil
}

func wrapperScript(name, bin, cfgFile string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n" + scriptMarker + "\n")
	if name == "commit-msg" || name == "prepare-commit-msg" {
		fmt.Fprintf(&b, `if [ "$RERIGHT_BYPASS" != 1 ] && ! grep -Eq '"mode"[[:space:]]*:[[:space:]]*"off"' %s 2>/dev/null; then
	if [ -x %s ]; then
		RERIGHT_GIT_PPID=$PPID %s git %s "$@" || exit $?
	else
		printf 'reright: %%s is missing or not executable, so this commit was not checked. Run reright install again, or reright uninstall.\n' %s >&2
	fi
fi
`, legacyShellQuote(cfgFile), legacyShellQuote(bin), legacyShellQuote(bin), name, legacyShellQuote(bin))
	}
	fmt.Fprintf(&b, `hooks=$(git config --local --get core.hooksPath 2>/dev/null)
case "$hooks" in
"~/"*) hooks="$HOME/${hooks#\~/}" ;;
esac
if [ -z "$hooks" ]; then
	hooks="$(git rev-parse --git-common-dir 2>/dev/null)/hooks"
fi
target="$hooks/%s"
if [ -f "$target" ] && [ -x "$target" ] && ! [ "$target" -ef "$0" ]; then
	exec "$target" "$@"
fi
exit 0
`, name)
	return b.String()
}

func manualStep(existing, homeDir, bin string) string {
	dir := expandHome(existing, homeDir)
	q := legacyShellQuote(bin)
	var b strings.Builder
	fmt.Fprintf(&b, "core.hooksPath is already set to %s, so reright did not change it. Add these lines near the top of the hook files in that directory (create each file with a #!/bin/sh first line and chmod +x if it does not exist). Remove them again when you uninstall reright:\n", existing)
	for _, name := range hookNames {
		fmt.Fprintf(&b, "  %s/%s: if [ -x %s ]; then RERIGHT_GIT_PPID=$PPID %s git %s \"$@\" || exit $?; fi\n", dir, name, q, q, name)
	}
	return b.String()
}

func globalConfigFile(homeDir string) string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return p
	}
	legacy := filepath.Join(homeDir, ".gitconfig")
	xdg := filepath.Join(homeDir, ".config", "git", "config")
	if _, err := os.Stat(legacy); err != nil {
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}
	return legacy
}

func writeState(path string, st *state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func legacyShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
