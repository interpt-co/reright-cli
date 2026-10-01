package hookcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/draft"
)

func codexShell(tool string, input map[string]any) map[string]any {
	return map[string]any{"session_id": "s", "cwd": "/tmp", "tool_name": tool, "tool_input": input}
}

func TestArgvCommandsAreQuoted(t *testing.T) {
	f := newFixture(t)
	f.checker.approved[draft.Hash("hello world, totally different text")] = true
	out, _ := runAgent(t, f, AgentCodex, "pre", codexShell("shell", map[string]any{"command": []string{"git", "commit", "-m", "hello world, totally different text"}}))
	if deny, reason := parseClaudeStyle(t, out); deny {
		t.Fatalf("approved multi-word message denied: %s", reason)
	}
	f = newFixture(t)
	f.checker.approved[draft.Hash("hello")] = true
	out, _ = runAgent(t, f, AgentCodex, "pre", codexShell("shell", map[string]any{"command": []string{"git", "commit", "-m", "hello world, totally different text"}}))
	if deny, _ := parseClaudeStyle(t, out); !deny {
		t.Fatal("a different multi-word message was read as -m hello and allowed")
	}
}

func TestShellToolsUnderOtherNames(t *testing.T) {
	names := []string{"shell_command", "container.exec", "unified_exec", "execute", "runCommands", "run_terminal_cmd", "some_new_tool"}
	for _, key := range []string{"command", "cmd"} {
		for _, name := range names {
			for _, val := range []any{`git commit -m "Hi"`, []string{"git", "commit", "-m", "Hi"}} {
				for _, agent := range []string{AgentCodex, AgentGemini, AgentCopilot, AgentCursor} {
					f := newFixture(t)
					var payload any
					switch agent {
					case AgentCopilot:
						payload = map[string]any{"sessionId": "s", "toolName": name, "toolArgs": jsonString(map[string]any{key: val})}
					case AgentCursor:
						payload = map[string]any{"conversation_id": "s", "hook_event_name": "preToolUse", "tool_name": name, "tool_input": map[string]any{key: val}}
					default:
						payload = codexShell(name, map[string]any{key: val})
					}
					out, _ := runAgent(t, f, agent, "pre", payload)
					if strings.TrimSpace(out) == "" {
						t.Errorf("%s %s %s %v: unapproved commit passed", agent, name, key, val)
					}
				}
			}
		}
	}
}

func TestBothCommandKeysInspected(t *testing.T) {
	f := newFixture(t)
	f.checker.approved[draft.Hash("Hi")] = true
	out, _ := runAgent(t, f, AgentCodex, "pre", codexShell("shell_command", map[string]any{"command": `git commit -m "Hi"`, "cmd": `git commit -m "Other"`}))
	if deny, _ := parseClaudeStyle(t, out); !deny {
		t.Fatal("disagreeing command and cmd were allowed")
	}
	out, _ = runAgent(t, f, AgentCodex, "pre", codexShell("shell_command", map[string]any{"command": `git commit -m "Hi"`, "cmd": `git commit -m "Hi"`}))
	if deny, _ := parseClaudeStyle(t, out); deny {
		t.Fatal("agreeing keys denied")
	}
	out, _ = runAgent(t, f, AgentCodex, "pre", codexShell("shell_command", map[string]any{"command": map[string]any{"x": 1}}))
	if deny, _ := parseClaudeStyle(t, out); !deny {
		t.Fatal("an object command was allowed")
	}
}

func TestArgvWrappers(t *testing.T) {
	cmds := [][]string{
		{"bash", "-ic", `git commit -m hi`},
		{"bash", "--login", "-c", `git commit -m hi`},
		{"env", "X=1", "bash", "-c", `git commit -m hi`},
		{"sh", "-lc", `git commit -m hi`},
		{"zsh", "-c", `git commit -m hi`},
		{"/usr/bin/env", "-i", "X=1", "bash", "-ec", `git commit -m hi`},
	}
	for _, c := range cmds {
		f := newFixture(t)
		out, _ := runAgent(t, f, AgentCodex, "pre", codexShell("shell", map[string]any{"command": c}))
		if deny, _ := parseClaudeStyle(t, out); !deny {
			t.Errorf("%v allowed", c)
		}
	}
	f := newFixture(t)
	f.checker.approved[draft.Hash("hi")] = true
	out, _ := runAgent(t, f, AgentCodex, "pre", codexShell("shell", map[string]any{"command": cmds[0]}))
	if deny, r := parseClaudeStyle(t, out); deny {
		t.Fatalf("approved wrapped commit denied: %s", r)
	}
}

func TestPostNeedsWaitForReview(t *testing.T) {
	rej := rejectedText("rejected")
	payloads := map[string]map[string]any{
		AgentCursor:  {"conversation_id": "s", "hook_event_name": "afterMCPExecution", "server": "evil", "tool_name": "read", "result_json": rej},
		AgentCopilot: {"sessionId": "s", "cwd": "/tmp", "hook_event_name": "PostToolUse", "tool_name": "readFile", "tool_response": rej},
		AgentGemini:  {"session_id": "s", "hook_event_name": "AfterTool", "tool_name": "read_file", "tool_response": map[string]any{"llmContent": rej}},
		AgentCodex:   {"session_id": "s", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_response": rej},
		AgentClaude:  {"session_id": "s", "hook_event_name": "PostToolUse", "tool_name": "Read", "tool_response": rej},
	}
	for agent, p := range payloads {
		f := newFixture(t)
		runAgent(t, f, agent, "post", p)
		if _, err := os.Stat(lockPath(f.env.StateDir, call{Session: "s"})); err == nil {
			t.Errorf("%s: a foreign tool result locked the session", agent)
		}
	}
	f := newFixture(t)
	runAgent(t, f, AgentCursor, "post", map[string]any{"conversation_id": "s", "server": "other", "tool_name": "wait_for_review", "result_json": rej})
	if _, err := os.Stat(lockPath(f.env.StateDir, call{Session: "s"})); err == nil {
		t.Error("another server's wait_for_review locked the session")
	}
}

func TestCursorMCPWithoutServer(t *testing.T) {
	cases := []map[string]any{
		{"tool_name": "send_message", "tool_input": jsonString(map[string]any{"subject": "S", "body": "B"})},
		{"tool_name": "create_draft", "command": "node /opt/srv.js", "tool_input": jsonString(map[string]any{"subject": "S", "body": "B"})},
		{"tool_name": "reply", "tool_input": jsonString(map[string]any{"body": "B"})},
		{"tool_name": "forward", "tool_input": jsonString(map[string]any{"forwardText": "B"})},
		{"tool_name": "javascript_tool", "tool_input": jsonString(map[string]any{"action": "javascript_exec", "text": "document.execCommand('insertText', false, 'unapproved words')"})},
	}
	for _, c := range cases {
		c["conversation_id"], c["hook_event_name"] = "s", "beforeMCPExecution"
		f := newFixture(t)
		out, _ := runAgent(t, f, AgentCursor, "pre", c)
		if deny, _ := parseCursor(t, out); !deny {
			t.Errorf("%v passed", c["tool_name"])
		}
	}
}

func TestServerNameCase(t *testing.T) {
	gm := map[string]any{"subject": "S", "body": "B"}
	for _, name := range []string{"mcp__Google_Workspace__send_message", "mcp__GMAIL__send_message", "mcp__my-workspace__send_message"} {
		f := newFixture(t)
		out, _ := runAgent(t, f, AgentCodex, "pre", map[string]any{"session_id": "s", "tool_name": name, "tool_input": gm})
		if deny, _ := parseClaudeStyle(t, out); !deny {
			t.Errorf("%s passed", name)
		}
	}
	js := map[string]any{"action": "javascript_exec", "text": "document.execCommand('insertText', false, 'unapproved words')"}
	for _, name := range []string{"mcp__Claude-In-Chrome__javascript_tool", "mcp__claude_in_chrome__javascript_tool", "mcp_claude_in_chrome_javascript_tool"} {
		f := newFixture(t)
		out, _ := runAgent(t, f, AgentGemini, "pre", map[string]any{"session_id": "s", "tool_name": name, "tool_input": js})
		if deny, _ := parseGemini(t, out); !deny {
			t.Errorf("%s passed", name)
		}
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args       []string
		agent, sub string
		err        bool
	}{
		{[]string{"pre"}, AgentClaude, "pre", false},
		{[]string{"pre", "--agent", "codex"}, AgentCodex, "pre", false},
		{[]string{"--agent", "codex", "pre"}, AgentCodex, "pre", false},
		{[]string{"--agent=cursor", "post"}, AgentCursor, "post", false},
		{[]string{"pre", "--agnet", "codex"}, "", "pre", true},
		{[]string{"pre", "--agent=codx"}, "", "pre", true},
		{[]string{"pre", "extra"}, "", "pre", true},
		{[]string{"pre", "--agent", "codex", "--agent", "gemini"}, "", "pre", true},
		{[]string{"--agent", "codex"}, "", "", true},
		{nil, "", "", true},
	}
	for _, tc := range cases {
		agent, sub, err := ParseArgs(tc.args)
		if agent != tc.agent || sub != tc.sub || (err != nil) != tc.err {
			t.Errorf("%v: %q %q %v", tc.args, agent, sub, err)
		}
	}
	if _, err := AgentFromArgs([]string{"--agnet", "x"}); err == nil {
		t.Error("AgentFromArgs accepted a typo")
	}
	for sub, want := range map[string]int{"pre": 2, "": 2, "bogus": 2, "post": 1, "prompt": 1} {
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
	p["tool_name"] = "mcp__plugin_reright_reright__wait_for_review"
	f.run(t, "post", p)
	if d, _ := f.run(t, "pre", tool("s", "Read")); d != "deny" {
		t.Fatal("the plugin's server name did not lock")
	}
}
