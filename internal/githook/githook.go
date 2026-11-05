// Package githook removes the git layer that reright 0.1 installed, and keeps `reright-hook git`
// working for the wrappers that are still in place until that happens.
//
// The layer was a set of wrapper scripts in ~/.config/reright/git-hooks with the global core.hooksPath
// pointing at them. The commit-msg and prepare-commit-msg wrappers ran `reright-hook git NAME "$@"`
// and then handed over to the repository's own hook. reright no longer checks commits there (the agent
// hook does), so that call is now a pass-through that never fails a commit.
package githook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env is what the pass-through needs from its caller.
type Env struct {
	Getenv func(string) string
	// Cwd is where git runs the hook, the top of the work tree. Empty means the current directory.
	Cwd string
	// Home is the user's home directory, used to skip reright's own wrappers. Empty means $HOME.
	Home string
}

// passEnv marks a hook started by the pass-through, so a hook that calls reright-hook git again
// does not loop.
const passEnv = "RERIGHT_GIT_PASSTHROUGH"

// Main runs `reright-hook git HOOK [ARGS...]`. It never fails a commit on its own account.
//
// The wrappers and the lines reright 0.1 told people to add by hand set RERIGHT_GIT_PPID and run the
// repository's own hook themselves afterwards, so for them Main does nothing and exits 0. Any other caller
// gets the repository's own hook (from its core.hooksPath, else .git/hooks), with the same arguments and
// stdin, and its exit code. With no such hook the exit code is 0.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if len(args) > 0 && args[0] == "git" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" || strings.ContainsRune(args[0], '/') {
		fmt.Fprintln(stderr, "usage: reright-hook git HOOK [ARGS...]. reright no longer checks commits in git; run reright upgrade to remove the old git hooks.")
		return 0
	}
	if env.Getenv("RERIGHT_GIT_PPID") != "" || env.Getenv(passEnv) == "1" {
		return 0
	}
	target := repoHook(env, args[0])
	if target == "" {
		return 0
	}
	cmd := exec.Command(target, args[1:]...)
	cmd.Dir = env.Cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = append(os.Environ(), passEnv+"=1")
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "reright-hook: could not run %s (%v). The commit goes on.\n", target, err)
	return 0
}

// repoHook finds the repository's own hook called name, the way the 0.1 wrappers did. It skips
// reright's wrapper directory, so the pass-through never runs a wrapper.
func repoHook(env Env, name string) string {
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = env.Cwd
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	home := env.Home
	if home == "" {
		home = env.Getenv("HOME")
	}
	dir := git("config", "--local", "--get", "core.hooksPath")
	switch {
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	case dir == "":
		common := git("rev-parse", "--git-common-dir")
		if common == "" {
			return ""
		}
		dir = filepath.Join(common, "hooks")
	}
	if !filepath.IsAbs(dir) {
		base := env.Cwd
		if base == "" {
			base, _ = os.Getwd()
		}
		dir = filepath.Join(base, dir)
	}
	if home != "" {
		_, ours, _ := paths(home)
		if sameDir(dir, ours, home) {
			return ""
		}
	}
	target := filepath.Join(dir, name)
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return target
}
