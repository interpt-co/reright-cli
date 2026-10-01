package githook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/draft"
	"github.com/interpt-co/reright-cli/internal/toggle"
)

type Checker interface {
	Approved(ctx context.Context, sha string) (bool, error)
}

type Env struct {
	StateDir    string
	Checker     Checker
	Getenv      func(string) string
	Cwd         string
	ConfigDir   string
	Interactive func() bool
	ParentArgs  func(pid int) ([]string, bool)
}

type Config struct {
	Mode         string `json:"mode"`
	TTYHeuristic *bool  `json:"tty_heuristic,omitempty"`
}

const (
	ModeAgents = "agents"
	ModeAlways = "always"
	ModeOff    = "off"
)

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if len(args) > 0 && args[0] == "git" {
		args = args[1:]
	}
	if len(args) < 2 || (args[0] != "commit-msg" && args[0] != "prepare-commit-msg") {
		fmt.Fprintln(stderr, "usage: reright-hook git commit-msg FILE | prepare-commit-msg FILE [SOURCE [SHA]]")
		return 2
	}
	name, file := args[0], args[1]
	if name == "prepare-commit-msg" && (len(args) < 3 || args[2] != "message") {
		return 0
	}

	cfg, cfgErr := LoadConfig(configDir(env))
	if cfgErr != nil {
		fmt.Fprintf(stderr, "reright: ignoring %s (%v), using mode %q.\n", filepath.Join(configDir(env), "git.json"), cfgErr, ModeAgents)
	}
	enforce, why := shouldEnforce(cfg, env)
	if !enforce {
		return 0
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if f, err := toggle.Load(configDir(env)); err == nil && f.Off("commit", cwd, time.Now()) {
		return 0
	}
	soft := why == reasonNoTerminal

	raw, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "reright: could not read the commit message file %s (%v), so the commit was aborted. Tell the user.\n", file, err)
		return 1
	}
	g := gitRunner{dir: env.Cwd}
	commentChar := g.commentChar()
	msg := finalize(string(raw), cleanupMode(g.config("commit.cleanup"), string(raw), commentChar), commentChar)
	if msg == "" {
		return 0
	}
	if reason := skipReason(g, env, msg); reason != "" {
		return 0
	}

	wait := 20 * time.Second
	if soft {
		wait = softWait
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if env.Checker == nil {
		return failClosed(stderr, env, errors.New("no checker configured"), soft)
	}
	ok, err := env.Checker.Approved(ctx, draft.Hash(msg))
	if err != nil {
		return failClosed(stderr, env, err, soft)
	}
	if ok {
		return 0
	}
	title, _, _ := strings.Cut(msg, "\n")
	fmt.Fprintf(stderr, "reright: this commit message was not approved in reright, so the commit was aborted.\n"+
		"Message title: %s\n"+
		"To fix it: call the reright tool submit_for_review with kind \"commit\", this exact message and its context. "+
		"Wait with wait_for_review until it is approved, then run git commit again with final_text exactly as returned. "+
		"If the reviewer rejects it, stop and tell the user. Do not use --no-verify or change the hook setup.\n", title)
	if soft {
		fmt.Fprintf(stderr, "If you are a person committing from an IDE or a git GUI, there is no terminal for reright to see, so it treated this commit like an agent's. "+
			"Set \"tty_heuristic\": false in %s to stop that, or commit from a terminal.\n", filepath.Join(configDir(env), "git.json"))
	}
	return 1
}

const (
	reasonNoTerminal = "no terminal"
	softWait         = 3 * time.Second
)

func failClosed(stderr io.Writer, env Env, err error, soft bool) int {
	if env.Getenv("RERIGHT_OFFLINE") == "allow" {
		return 0
	}
	if soft {
		fmt.Fprintf(stderr, "reright: could not check this commit message with the review server (%v). "+
			"Nothing marks this commit as an agent's and it has no terminal, so it was let through.\n", err)
		return 0
	}
	fmt.Fprintf(stderr, "reright: could not check this commit message with the review server (%v), so the commit was aborted. "+
		"Tell the user. They can set RERIGHT_OFFLINE=allow to commit while offline.\n", err)
	return 1
}

func configDir(env Env) string {
	if env.ConfigDir != "" {
		return env.ConfigDir
	}
	return filepath.Join(env.Getenv("HOME"), ".config", "reright")
}

func LoadConfig(dir string) (Config, error) {
	cfg := Config{Mode: ModeAgents}
	b, err := os.ReadFile(filepath.Join(dir, "git.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{Mode: ModeAgents}, err
	}
	switch cfg.Mode {
	case "":
		cfg.Mode = ModeAgents
	case ModeAgents, ModeAlways, ModeOff:
	default:
		bad := cfg.Mode
		return Config{Mode: ModeAgents}, fmt.Errorf("unknown mode %q", bad)
	}
	return cfg, nil
}

func shouldEnforce(cfg Config, env Env) (bool, string) {
	if cfg.Mode == ModeOff {
		return false, "mode off"
	}
	if env.Getenv("RERIGHT_BYPASS") == "1" {
		return false, "RERIGHT_BYPASS=1"
	}
	if env.Getenv("RERIGHT_ENFORCE") == "1" {
		return true, "RERIGHT_ENFORCE=1"
	}
	if cfg.Mode == ModeAlways {
		return true, "mode always"
	}
	if who, ok := DetectAgent(env.Getenv); ok {
		return true, who
	}
	if cfg.TTYHeuristic == nil || *cfg.TTYHeuristic {
		interactive := env.Interactive
		if interactive == nil {
			interactive = stderrIsTerminal
		}
		if !interactive() {
			return true, reasonNoTerminal
		}
	}
	return false, "looks like a person at a terminal"
}

func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

func skipReason(g gitRunner, env Env, msg string) string {
	gitDir := g.out("rev-parse", "--absolute-git-dir")
	if gitDir != "" {
		for _, f := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
			if _, err := os.Stat(filepath.Join(gitDir, f)); err == nil {
				return f + " present"
			}
		}
	}
	action := env.Getenv("GIT_REFLOG_ACTION")
	for _, p := range []string{"rebase", "cherry-pick", "revert", "merge", "am"} {
		if action == p || strings.HasPrefix(action, p+" ") || strings.HasPrefix(action, p+":") {
			return "git " + p + " in progress"
		}
	}
	if argv, ok := parentArgv(env); ok {
		switch verb := gitVerb(argv); verb {
		case "cherry-pick", "revert", "merge", "rebase", "am", "pull":
			return "git " + verb + " in progress"
		}
	}
	head := g.out("show", "-s", "--format=%B", "HEAD")
	if head != "" && draft.Hash(head) == draft.Hash(msg) && amendLikely(env) {
		return "amend with the message unchanged"
	}
	return ""
}

func parentArgv(env Env) ([]string, bool) {
	pid, err := strconv.Atoi(env.Getenv("RERIGHT_GIT_PPID"))
	if err != nil || pid <= 0 {
		return nil, false
	}
	lookup := env.ParentArgs
	if lookup == nil {
		lookup = procArgs
	}
	return lookup(pid)
}

func gitVerb(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-c" || a == "-C" || a == "--exec-path" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

func amendLikely(env Env) bool {
	argv, ok := parentArgv(env)
	if !ok {
		return true
	}
	for _, a := range argv {
		if a == "--amend" {
			return true
		}
	}
	return false
}

func procArgs(pid int) ([]string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), true
}

type gitRunner struct{ dir string }

func (g gitRunner) out(args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

func (g gitRunner) config(key string) string {
	return g.out("config", "--get", key)
}

func (g gitRunner) commentChar() string {
	for _, k := range []string{"core.commentString", "core.commentChar"} {
		if v := g.config(k); v != "" && v != "auto" {
			return v
		}
	}
	return "#"
}
