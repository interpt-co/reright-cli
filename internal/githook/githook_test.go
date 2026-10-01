package githook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/interpt-co/reright-cli/internal/draft"
)

type fakeChecker struct {
	approved map[string]bool
	err      error
	calls    int
}

func (f *fakeChecker) Approved(_ context.Context, sha string) (bool, error) {
	f.calls++
	return f.approved[sha], f.err
}

type fileChecker struct{}

func (fileChecker) Approved(_ context.Context, sha string) (bool, error) {
	if os.Getenv("GITHOOK_TEST_DOWN") == "1" {
		return false, errors.New("connection refused")
	}
	b, _ := os.ReadFile(os.Getenv("GITHOOK_TEST_APPROVED"))
	for _, l := range strings.Split(string(b), "\n") {
		if l == sha {
			return true, nil
		}
	}
	return false, nil
}

func TestMain(m *testing.M) {
	if os.Getenv("GITHOOK_TEST_BINARY") == "1" {
		env := Env{Checker: fileChecker{}, Getenv: os.Getenv, Interactive: func() bool { return os.Getenv("GITHOOK_TEST_TTY") == "1" }}
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, env))
	}
	os.Exit(m.Run())
}

func writeMsg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "MSG")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func unit(t *testing.T, vars map[string]string, checker Checker, args ...string) (int, string) {
	t.Helper()
	if vars == nil {
		vars = map[string]string{}
	}
	var errOut bytes.Buffer
	env := Env{
		Checker:     checker,
		Getenv:      func(k string) string { return vars[k] },
		Cwd:         t.TempDir(),
		ConfigDir:   t.TempDir(),
		Interactive: func() bool { return false },
	}
	code := Main(args, strings.NewReader(""), &bytes.Buffer{}, &errOut, env)
	return code, errOut.String()
}

func TestNotApprovedBlocksWithInstructions(t *testing.T) {
	fc := &fakeChecker{approved: map[string]bool{}}
	code, msg := unit(t, nil, fc, "commit-msg", writeMsg(t, "Add feature\n\nBody.\n"))
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"submit_for_review", "kind \"commit\"", "wait_for_review", "final_text"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr lacks %q: %s", want, msg)
		}
	}
}

func TestApprovedPassesAndHashMatchesDraft(t *testing.T) {
	fc := &fakeChecker{approved: map[string]bool{draft.Hash("Add feature\n\nBody."): true}}
	raw := "Add feature  \r\n\r\n\r\nBody.\n\n# Please enter the commit message for your changes.\n#\n"
	if code, msg := unit(t, nil, fc, "git", "commit-msg", writeMsg(t, raw)); code != 0 {
		t.Fatalf("exit %d: %s", code, msg)
	}
}

func TestEmptyMessageSkipped(t *testing.T) {
	fc := &fakeChecker{approved: map[string]bool{}}
	raw := "\n# Please enter the commit message for your changes.\n"
	if code, _ := unit(t, nil, fc, "commit-msg", writeMsg(t, raw)); code != 0 || fc.calls != 0 {
		t.Fatalf("exit %d, calls %d", code, fc.calls)
	}
}

func TestFailClosedAndOffline(t *testing.T) {
	fc := &fakeChecker{err: errors.New("no route")}
	f := writeMsg(t, "x\n")
	agent := map[string]string{"CLAUDECODE": "1"}
	code, msg := unit(t, agent, fc, "commit-msg", f)
	if code != 1 || !strings.Contains(msg, "RERIGHT_OFFLINE=allow") {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if code, _ := unit(t, map[string]string{"CLAUDECODE": "1", "RERIGHT_OFFLINE": "allow"}, fc, "commit-msg", f); code != 0 {
		t.Fatalf("offline allow gave exit %d", code)
	}
	if code, _ := unit(t, agent, nil, "commit-msg", f); code != 1 {
		t.Fatalf("nil checker gave exit %d", code)
	}
}

func TestNoTerminalCommitFailsOpenWhenServerIsDown(t *testing.T) {
	fc := &fakeChecker{err: errors.New("no route")}
	f := writeMsg(t, "x\n")
	code, msg := unit(t, nil, fc, "commit-msg", f)
	if code != 0 || !strings.Contains(msg, "let through") {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if code, _ := unit(t, nil, nil, "commit-msg", f); code != 0 {
		t.Fatalf("nil checker gave exit %d", code)
	}
	fc = &fakeChecker{approved: map[string]bool{}}
	code, msg = unit(t, nil, fc, "commit-msg", f)
	if code != 1 || !strings.Contains(msg, "tty_heuristic") {
		t.Fatalf("an unapproved no-terminal commit should still be blocked and mention tty_heuristic: %d %s", code, msg)
	}
	if code, _ := unit(t, map[string]string{"RERIGHT_ENFORCE": "1"}, &fakeChecker{err: errors.New("no route")}, "commit-msg", f); code != 1 {
		t.Fatalf("explicit enforcement must fail closed, got %d", code)
	}
}

type slowChecker struct{}

func (slowChecker) Approved(ctx context.Context, _ string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(30 * time.Second):
		return true, nil
	}
}

func TestNoTerminalCommitNeverWaitsLongForTheServer(t *testing.T) {
	start := time.Now()
	code, _ := unit(t, nil, slowChecker{}, "commit-msg", writeMsg(t, "x\n"))
	if code != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("exit %d after %v", code, time.Since(start))
	}
}

func TestPrepareCommitMsgOnlyChecksFinalMessages(t *testing.T) {
	fc := &fakeChecker{approved: map[string]bool{}}
	f := writeMsg(t, "x\n")
	if code, _ := unit(t, nil, fc, "prepare-commit-msg", f); code != 0 {
		t.Fatalf("no source: exit %d", code)
	}
	if code, _ := unit(t, nil, fc, "prepare-commit-msg", f, "merge"); code != 0 {
		t.Fatalf("merge source: exit %d", code)
	}
	if code, _ := unit(t, nil, fc, "prepare-commit-msg", f, "message"); code != 1 {
		t.Fatalf("message source: exit %d", code)
	}
}

func TestUsage(t *testing.T) {
	if code, _ := unit(t, nil, nil, "commit-msg"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if code, _ := unit(t, nil, nil, "pre-push", "x"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestEnforcementDecision(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name        string
		cfg         Config
		vars        map[string]string
		interactive bool
		want        bool
	}{
		{"human at terminal", Config{Mode: ModeAgents}, nil, true, false},
		{"no terminal", Config{Mode: ModeAgents}, nil, false, true},
		{"no terminal, heuristic off", Config{Mode: ModeAgents, TTYHeuristic: &no}, nil, false, false},
		{"claude code marker on a terminal", Config{Mode: ModeAgents}, map[string]string{"CLAUDECODE": "1"}, true, true},
		{"gemini marker", Config{Mode: ModeAgents}, map[string]string{"GEMINI_CLI": "1"}, true, true},
		{"AGENT known value", Config{Mode: ModeAgents}, map[string]string{"AGENT": "goose"}, true, true},
		{"AGENT unknown value", Config{Mode: ModeAgents}, map[string]string{"AGENT": "smith"}, true, false},
		{"enforce env", Config{Mode: ModeAgents}, map[string]string{"RERIGHT_ENFORCE": "1"}, true, true},
		{"bypass beats marker", Config{Mode: ModeAgents}, map[string]string{"RERIGHT_BYPASS": "1", "CLAUDECODE": "1"}, false, false},
		{"bypass beats enforce", Config{Mode: ModeAgents}, map[string]string{"RERIGHT_BYPASS": "1", "RERIGHT_ENFORCE": "1"}, false, false},
		{"always", Config{Mode: ModeAlways}, nil, true, true},
		{"always but bypass", Config{Mode: ModeAlways}, map[string]string{"RERIGHT_BYPASS": "1"}, true, false},
		{"off beats enforce", Config{Mode: ModeOff}, map[string]string{"RERIGHT_ENFORCE": "1"}, false, false},
		{"tty heuristic on explicitly", Config{Mode: ModeAgents, TTYHeuristic: &yes}, nil, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{Getenv: func(k string) string { return tc.vars[k] }, Interactive: func() bool { return tc.interactive }}
			got, _ := shouldEnforce(tc.cfg, env)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if cfg, err := LoadConfig(dir); err != nil || cfg.Mode != ModeAgents {
		t.Fatalf("missing file: %+v %v", cfg, err)
	}
	os.WriteFile(filepath.Join(dir, "git.json"), []byte(`{"mode":"always"}`), 0o644)
	if cfg, err := LoadConfig(dir); err != nil || cfg.Mode != ModeAlways {
		t.Fatalf("%+v %v", cfg, err)
	}
	os.WriteFile(filepath.Join(dir, "git.json"), []byte(`{"mode":"sometimes"}`), 0o644)
	if cfg, err := LoadConfig(dir); err == nil || cfg.Mode != ModeAgents {
		t.Fatalf("bad mode accepted: %+v %v", cfg, err)
	}
	os.WriteFile(filepath.Join(dir, "git.json"), []byte(`{`), 0o644)
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestFinalize(t *testing.T) {
	tests := []struct{ name, raw, mode, want string }{
		{"strip", "Title\n\n# comment\nBody  \n\n\n\nMore\n# other\n", "strip", "Title\n\nBody\n\nMore"},
		{"whitespace keeps comments", "Title\n\n# Heading\nBody\n", "whitespace", "Title\n\n# Heading\nBody"},
		{"scissors", "Title\n# ------------------------ >8 ------------------------\ndiff\n", "scissors", "Title"},
		{"verbatim", "Title  \n\n\nBody\n", "verbatim", "Title\n\n\nBody"},
	}
	for _, tc := range tests {
		if got := finalize(tc.raw, tc.mode, "#"); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	if m := cleanupMode("", "x\n# Please enter the commit message for your changes.\n", "#"); m != "strip" {
		t.Errorf("template hint mode %q", m)
	}
	if m := cleanupMode("", "x\n", "#"); m != "whitespace" {
		t.Errorf("plain mode %q", m)
	}
}

func TestMarkersAreDataAndFlagged(t *testing.T) {
	verified := 0
	for _, m := range AgentMarkers {
		if m.Var == "" || m.Agent == "" || m.Source == "" {
			t.Errorf("incomplete marker %+v", m)
		}
		if m.Verified {
			verified++
		} else if !strings.HasPrefix(m.Source, "UNVERIFIED") {
			t.Errorf("%s not verified and not flagged: %s", m.Var, m.Source)
		}
	}
	if verified == 0 {
		t.Error("no verified marker")
	}
	if _, ok := DetectAgent(func(string) string { return "" }); ok {
		t.Error("empty env detected as agent")
	}
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}
