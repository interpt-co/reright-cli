package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/draft"
)

func runAgent(t *testing.T, f *fixture, agent, sub string, payload any) (string, int) {
	t.Helper()
	var b []byte
	switch p := payload.(type) {
	case string:
		b = []byte(p)
	default:
		b, _ = json.Marshal(p)
	}
	var out, errOut bytes.Buffer
	code := RunAgent(context.Background(), agent, sub, bytes.NewReader(b), &out, &errOut, f.env)
	return out.String(), code
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args       []string
		agent, sub string
		err        bool
	}{
		{[]string{"pre"}, AgentClaude, "pre", false},
		{[]string{"pre", "--agent", "claude"}, AgentClaude, "pre", false},
		{[]string{"--agent=claude", "post"}, AgentClaude, "post", false},
		{[]string{"pre", "--agent", "codex"}, "codex", "pre", false},
		{[]string{"--agent=cursor", "post"}, "cursor", "post", false},
		{[]string{"pre", "--agent", "windsurf"}, "", "pre", true},
		{[]string{"pre", "--agnet", "claude"}, "", "pre", true},
		{[]string{"pre", "extra"}, "", "pre", true},
		{[]string{"pre", "--agent", "claude", "--agent", "claude"}, "", "pre", true},
		{[]string{"--agent", "claude"}, "", "", true},
		{nil, "", "", true},
	}
	for _, tc := range cases {
		agent, sub, err := ParseArgs(tc.args)
		if agent != tc.agent || sub != tc.sub || (err != nil) != tc.err {
			t.Errorf("%v: %q %q %v", tc.args, agent, sub, err)
		}
	}
	for sub, want := range map[string]int{"pre": 2, "": 2, "bogus": 2, "post": 1, "prompt": 1, "stop": 1} {
		if got := UsageExit(sub); got != want {
			t.Errorf("UsageExit(%q) = %d, want %d", sub, got, want)
		}
	}
	f := newFixture(t)
	var out, errOut bytes.Buffer
	if code := RunAgent(context.Background(), AgentClaude, "bogus", strings.NewReader("{}"), &out, &errOut, f.env); code != 2 {
		t.Fatalf("unknown subcommand exits %d", code)
	}
}

func TestUnknownAgent(t *testing.T) {
	f := newFixture(t)
	var out, errOut bytes.Buffer
	if code := RunAgent(context.Background(), "windsurf", "pre", strings.NewReader("{}"), &out, &errOut, f.env); code != 2 {
		t.Fatalf("pre with an unknown agent exits %d, want 2", code)
	}
	if code := RunAgent(context.Background(), "windsurf", "post", strings.NewReader("{}"), &out, &errOut, f.env); code != 1 {
		t.Fatalf("post with an unknown agent exits %d, want 1", code)
	}
}

// Hook configs that reright 0.1 wrote for other agents still call reright-hook --agent NAME. Those calls
// fail open: exit 0, nothing on stdout, at most one line on stderr, even for a call that would be blocked.
func TestOldAgentConfigsFailOpen(t *testing.T) {
	commit := `{"session_id":"s","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`
	for _, agent := range []string{"codex", "gemini", "copilot", "cursor"} {
		for _, sub := range []string{"pre", "post", "prompt", "stop"} {
			f := newFixture(t)
			f.run(t, "post", waitResult("s", "rejected")) // a reject lock that would block a Claude call
			healed := false
			f.env.Heal = func() { healed = true }
			args := []string{sub, "--agent", agent}
			a, s, err := ParseArgs(args)
			if err != nil || a != agent || s != sub {
				t.Fatalf("%v: %q %q %v", args, a, s, err)
			}
			var out, errOut bytes.Buffer
			code := RunAgent(context.Background(), a, s, strings.NewReader(commit), &out, &errOut, f.env)
			if code != 0 || out.Len() != 0 || strings.Count(errOut.String(), "\n") > 1 || healed || f.checker.calls != 0 {
				t.Fatalf("%v: exit %d, stdout %q, stderr %q, healed %v, server calls %d", args, code, out.String(), errOut.String(), healed, f.checker.calls)
			}
		}
	}
}

func TestCheckerFailuresDeny(t *testing.T) {
	for name, cerr := range map[string]error{"server down": errors.New("connection refused"), "timeout": context.DeadlineExceeded} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.checker.err = cerr
			if d, reason := f.run(t, "pre", bash("s1", `git commit -m "Fix the login redirect"`)); d != "deny" || !strings.Contains(reason, cerr.Error()) {
				t.Fatalf("decision %q reason %q", d, reason)
			}
			f.vars["RERIGHT_OFFLINE"] = "allow"
			if d, _ := f.run(t, "pre", bash("s1", `git commit -m "Fix the login redirect"`)); d != "" {
				t.Fatal("RERIGHT_OFFLINE=allow was ignored")
			}
		})
	}
}

type panicChecker struct{}

func (panicChecker) Approved(context.Context, string) (bool, error) { panic("boom") }

func TestCheckerPanicDenies(t *testing.T) {
	f := newFixture(t)
	f.env.Checker = panicChecker{}
	if d, reason := f.run(t, "pre", bash("s1", `git commit -m "Fix the login redirect"`)); d != "deny" || !strings.Contains(reason, "boom") {
		t.Fatalf("decision %q reason %q", d, reason)
	}
}

// The scope of the hook after the rework: a heredoc commit and gh pr create are still checked, a script run
// through an interpreter is let through even when it runs git inside.
func TestScopeHeredocAndGhCaughtScriptsLetThrough(t *testing.T) {
	f := newFixture(t)
	heredoc := "git commit -m \"$(cat <<'EOF'\nFix the login redirect\n\nIt looped on expired sessions.\nEOF\n)\""
	if d, reason := f.run(t, "pre", bash("s1", heredoc)); d != "deny" || !strings.Contains(reason, "rewrite") {
		t.Fatalf("heredoc commit: decision %q reason %q", d, reason)
	}
	f.checker.approved[draft.Hash("Fix the login redirect\n\nIt looped on expired sessions.")] = true
	if d, _ := f.run(t, "pre", bash("s1", heredoc)); d != "" {
		t.Fatal("the approved heredoc commit was blocked")
	}
	if d, _ := f.run(t, "pre", bash("s1", `gh pr create --title "Add the export button" --body "Adds a button that exports the report."`)); d != "deny" {
		t.Fatal("gh pr create was not checked")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "commit.py"), []byte("import os\nos.system('git commit -m x')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := f.checker.calls
	py := bash("s1", "python3 commit.py")
	py["cwd"] = dir
	if d, reason := f.run(t, "pre", py); d != "" {
		t.Fatalf("the python script was blocked: %s", reason)
	}
	for _, cmd := range []string{"curl -d 'a=b' https://example.com/hook", `bash -c 'git commit -m x'`, `eval "git commit -m x"`} {
		if d, reason := f.run(t, "pre", bash("s1", cmd)); d != "" {
			t.Fatalf("%s was blocked: %s", cmd, reason)
		}
	}
	browser := tool("s1", "mcp__claude-in-chrome__javascript_tool")
	browser["tool_input"] = map[string]any{"text": "document.execCommand('insertText', false, 'Hello there')"}
	if d, _ := f.run(t, "pre", browser); d != "" {
		t.Fatal("the browser tool was blocked")
	}
	if f.checker.calls != calls {
		t.Fatal("a call outside the scope asked the server")
	}
}

func TestLockNames(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 400)
	runAgent(t, f, AgentClaude, "post", waitResult(long, "rejected"))
	if d, _ := f.run(t, "pre", tool(long, "Read")); d != "deny" {
		t.Fatal("a long session id lost its lock")
	}
	if d, _ := f.run(t, "pre", tool(long+"y", "Read")); d != "" {
		t.Fatal("a long session id collided with another")
	}
	entries, _ := os.ReadDir(filepath.Join(f.env.StateDir, "locks"))
	if len(entries) != 1 || len(entries[0].Name()) > 100 {
		t.Fatalf("lock names: %v", entries)
	}

	f = newFixture(t)
	nosess := waitResult("", "rejected")
	nosess["cwd"] = "/work/a"
	f.run(t, "post", nosess)
	other := tool("s-other", "Read")
	if d, _ := f.run(t, "pre", other); d != "" {
		t.Fatal("a session-less lock blocked a session that has an id")
	}
	same := tool("", "Read")
	same["cwd"] = "/work/a"
	if d, _ := f.run(t, "pre", same); d != "deny" {
		t.Fatal("the session-less lock was not found again")
	}
	diff := tool("", "Read")
	diff["cwd"] = "/work/b"
	if d, _ := f.run(t, "pre", diff); d != "" {
		t.Fatal("a session-less lock leaked into another directory")
	}
}

func TestPluginServerNameLocks(t *testing.T) {
	f := newFixture(t)
	p := waitResult("s", "rejected")
	p["tool_name"] = "mcp__plugin_reright_reright__rewrite_result"
	f.run(t, "post", p)
	if d, _ := f.run(t, "pre", tool("s", "Read")); d != "deny" {
		t.Fatal("the plugin's server name did not lock")
	}
}

// Only reright's own tools set the reject lock. A rejected-looking result from any other tool is ignored.
func TestPostFromAnotherToolDoesNotLock(t *testing.T) {
	f := newFixture(t)
	other := waitResult("s", "rejected")
	other["tool_name"] = "mcp__other__rewrite_result"
	f.run(t, "post", other)
	shell := waitResult("s", "rejected")
	shell["tool_name"] = "Bash"
	f.run(t, "post", shell)
	if d, _ := f.run(t, "pre", tool("s", "Read")); d != "" {
		t.Fatal("a result from another tool locked the session")
	}
}
