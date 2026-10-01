package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/draft"
)

type agentCase struct {
	name     string
	shell    func(session, cmd string) any
	gmail    func(session, subject, body string) any
	other    func(session string) any
	post     func(session, status string) any
	prompt   func(session string) any
	denyCode int
	parse    func(t *testing.T, out string) (deny bool, reason string)
}

func rejectedText(status string) string {
	b, _ := json.Marshal(map[string]string{"id": "abc", "status": status, "reason": "too long"})
	return string(b)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func parseClaudeStyle(t *testing.T, out string) (bool, string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var d struct {
		H struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	if d.H.Event != "PreToolUse" {
		t.Fatalf("hookEventName %q in %s", d.H.Event, out)
	}
	return d.H.Decision == "deny", d.H.Reason
}

func parseGemini(t *testing.T, out string) (bool, string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var d struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	return d.Decision == "deny", d.Reason
}

func parseCursor(t *testing.T, out string) (bool, string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var d struct {
		Permission   string `json:"permission"`
		UserMessage  string `json:"user_message"`
		AgentMessage string `json:"agent_message"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	if d.Permission == "deny" && (d.UserMessage == "" || d.AgentMessage == "") {
		t.Fatalf("deny without messages: %s", out)
	}
	return d.Permission == "deny", d.AgentMessage
}

func parseCopilotCLI(t *testing.T, out string) (bool, string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var d struct {
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	return d.Decision == "deny", d.Reason
}

var agentCases = []agentCase{
	{
		name: AgentCodex,
		shell: func(s, cmd string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "PreToolUse", "model": "gpt-5", "permission_mode": "default",
				"tool_name": "Bash", "tool_use_id": "call_1", "tool_input": map[string]any{"command": cmd}}
		},
		gmail: func(s, subject, body string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "PreToolUse", "tool_name": "mcp__claude_ai_Gmail__send_message",
				"tool_input": map[string]any{"subject": subject, "body": body}}
		},
		other: func(s string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "PreToolUse", "tool_name": "apply_patch", "tool_input": map[string]any{"input": "*** Begin Patch"}}
		},
		post: func(s, status string) any {
			return map[string]any{"session_id": s, "hook_event_name": "PostToolUse", "tool_name": "mcp__reright__wait_for_review",
				"tool_response": map[string]any{"content": []any{map[string]any{"type": "text", "text": rejectedText(status)}}}}
		},
		prompt: func(s string) any {
			return map[string]any{"session_id": s, "hook_event_name": "UserPromptSubmit", "prompt": "go on"}
		},
		parse: parseClaudeStyle,
	},
	{
		name: AgentGemini,
		shell: func(s, cmd string) any {
			return map[string]any{"session_id": s, "transcript_path": "/tmp/t.json", "cwd": "/tmp", "hook_event_name": "BeforeTool", "timestamp": "2026-09-30T10:00:00Z",
				"tool_name": "run_shell_command", "tool_input": map[string]any{"command": cmd}}
		},
		gmail: func(s, subject, body string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "BeforeTool", "tool_name": "mcp_claude_ai_Gmail_send_message",
				"tool_input": map[string]any{"subject": subject, "body": body}}
		},
		other: func(s string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "BeforeTool", "tool_name": "read_file", "tool_input": map[string]any{"file_path": "/x"}}
		},
		post: func(s, status string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "AfterTool", "tool_name": "mcp_reright_wait_for_review",
				"tool_input":    map[string]any{"id": "abc"},
				"tool_response": map[string]any{"llmContent": rejectedText(status), "returnDisplay": ""}}
		},
		prompt: func(s string) any {
			return map[string]any{"session_id": s, "cwd": "/tmp", "hook_event_name": "BeforeAgent", "prompt": "go on"}
		},
		denyCode: 2,
		parse:    parseGemini,
	},
	{
		name: AgentCopilot + "-cli",
		shell: func(s, cmd string) any {
			return map[string]any{"sessionId": s, "timestamp": 1704614600000, "cwd": "/tmp", "toolName": "bash", "toolArgs": jsonString(map[string]any{"command": cmd})}
		},
		gmail: func(s, subject, body string) any {
			return map[string]any{"sessionId": s, "timestamp": 1704614600000, "cwd": "/tmp", "toolName": "mcp_claude_ai_Gmail_send_message",
				"toolArgs": jsonString(map[string]any{"subject": subject, "body": body})}
		},
		other: func(s string) any {
			return map[string]any{"sessionId": s, "timestamp": 1704614600000, "cwd": "/tmp", "toolName": "view", "toolArgs": `{"path":"/x"}`}
		},
		post: func(s, status string) any {
			return map[string]any{"sessionId": s, "timestamp": 1704614700000, "cwd": "/tmp", "toolName": "mcp_reright_wait_for_review", "toolArgs": `{"id":"abc"}`,
				"toolResult": map[string]any{"resultType": "success", "textResultForLlm": rejectedText(status)}}
		},
		prompt: func(s string) any {
			return map[string]any{"sessionId": s, "timestamp": 1704614500000, "cwd": "/tmp", "prompt": "go on"}
		},
		parse: parseCopilotCLI,
	},
	{
		name: AgentCopilot + "-vscode",
		shell: func(s, cmd string) any {
			return map[string]any{"timestamp": "2026-09-30T10:00:00.000Z", "cwd": "/tmp", "sessionId": s, "hookEventName": "PreToolUse", "hook_event_name": "PreToolUse",
				"tool_name": "runTerminalCommand", "tool_input": map[string]any{"command": cmd}, "tool_use_id": "u1"}
		},
		gmail: func(s, subject, body string) any {
			return map[string]any{"cwd": "/tmp", "sessionId": s, "hook_event_name": "PreToolUse", "tool_name": "mcp_claude_ai_Gmail_send_message",
				"tool_input": map[string]any{"subject": subject, "body": body}}
		},
		other: func(s string) any {
			return map[string]any{"cwd": "/tmp", "sessionId": s, "hook_event_name": "PreToolUse", "tool_name": "readFile", "tool_input": map[string]any{"filePath": "/x"}}
		},
		post: func(s, status string) any {
			return map[string]any{"cwd": "/tmp", "sessionId": s, "hook_event_name": "PostToolUse", "tool_name": "mcp_reright_wait_for_review",
				"tool_input": map[string]any{"id": "abc"}, "tool_response": rejectedText(status)}
		},
		prompt: func(s string) any {
			return map[string]any{"cwd": "/tmp", "sessionId": s, "hook_event_name": "UserPromptSubmit", "prompt": "go on"}
		},
		parse: parseClaudeStyle,
	},
	{
		name: AgentCursor,
		shell: func(s, cmd string) any {
			return map[string]any{"conversation_id": s, "generation_id": "g1", "model": "m", "hook_event_name": "beforeShellExecution", "cursor_version": "1.7.2",
				"workspace_roots": []string{"/tmp"}, "command": cmd, "cwd": "/tmp", "sandbox": false}
		},
		gmail: func(s, subject, body string) any {
			return map[string]any{"conversation_id": s, "generation_id": "g1", "hook_event_name": "beforeMCPExecution", "workspace_roots": []string{"/tmp"},
				"server": "claude_ai_Gmail", "tool_name": "send_message", "tool_input": jsonString(map[string]any{"subject": subject, "body": body})}
		},
		other: func(s string) any {
			return map[string]any{"conversation_id": s, "generation_id": "g1", "hook_event_name": "preToolUse", "workspace_roots": []string{"/tmp"},
				"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}, "tool_use_id": "u1"}
		},
		post: func(s, status string) any {
			return map[string]any{"conversation_id": s, "generation_id": "g1", "hook_event_name": "afterMCPExecution", "workspace_roots": []string{"/tmp"},
				"server": "reright", "tool_name": "wait_for_review", "tool_input": `{"id":"abc"}`, "result_json": rejectedText(status), "duration": 1200}
		},
		prompt: func(s string) any {
			return map[string]any{"conversation_id": s, "generation_id": "g1", "hook_event_name": "beforeSubmitPrompt", "prompt": "go on", "attachments": []any{}}
		},
		parse: parseCursor,
	},
}

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
	code := RunAgent(context.Background(), strings.SplitN(agent, "-", 2)[0], sub, bytes.NewReader(b), &out, &errOut, f.env)
	return out.String(), code
}

func TestAgentPre(t *testing.T) {
	for _, tc := range agentCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			expect := func(label string, payload any, wantDeny bool, wantReason string) {
				t.Helper()
				out, code := runAgent(t, f, tc.name, "pre", payload)
				deny, reason := tc.parse(t, out)
				if deny != wantDeny {
					t.Fatalf("%s: deny=%v reason=%q out=%s", label, deny, reason, out)
				}
				wantCode := 0
				if wantDeny {
					wantCode = tc.denyCode
				}
				if code != wantCode {
					t.Fatalf("%s: exit %d, want %d", label, code, wantCode)
				}
				if wantDeny && !strings.Contains(reason, wantReason) {
					t.Fatalf("%s: reason %q lacks %q", label, reason, wantReason)
				}
			}

			expect("unapproved shell commit", tc.shell("s1", `git commit -m "Hi"`), true, "submit_for_review")
			f.checker.approved[draft.Hash("Hi")] = true
			expect("approved shell commit", tc.shell("s1", `git commit -m "Hi"`), false, "")
			expect("shell without text", tc.shell("s1", "ls -la"), false, "")
			expect("shell problem", tc.shell("s1", "git commit"), true, "editor")

			expect("unapproved gmail", tc.gmail("s1", "Subject", "Body text"), true, "email subject and body")
			f.checker.approved[draft.Hash("Subject\n\nBody text")] = true
			expect("approved gmail", tc.gmail("s1", "Subject", "Body text"), false, "")

			calls := f.checker.calls
			expect("unrelated tool", tc.other("s1"), false, "")
			if f.checker.calls != calls {
				t.Fatal("an unrelated tool triggered a server check")
			}

			expect("unparseable payload", "not json", true, "could not read")
			expect("json array payload", "[1,2]", true, "could not read")
			expect("empty payload", "", true, "could not read")
		})
	}
}

func TestAgentCheckerFailures(t *testing.T) {
	failures := map[string]error{
		"server down": errors.New("connection refused"),
		"timeout":     context.DeadlineExceeded,
		"canceled":    context.Canceled,
	}
	for _, tc := range agentCases {
		for name, cerr := range failures {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				f := newFixture(t)
				f.checker.err = cerr
				out, code := runAgent(t, f, tc.name, "pre", tc.shell("s1", `git commit -m "Hi"`))
				deny, reason := tc.parse(t, out)
				if !deny || !strings.Contains(reason, cerr.Error()) || code != tc.denyCode {
					t.Fatalf("deny=%v reason=%q code=%d", deny, reason, code)
				}
				f.vars["RERIGHT_OFFLINE"] = "allow"
				out, code = runAgent(t, f, tc.name, "pre", tc.shell("s1", `git commit -m "Hi"`))
				if deny, _ := tc.parse(t, out); deny || code != 0 {
					t.Fatalf("offline allow ignored: %s", out)
				}
			})
		}
	}
}

type panicChecker struct{}

func (panicChecker) Approved(context.Context, string) (bool, error) { panic("boom") }

func TestAgentCheckerPanicDenies(t *testing.T) {
	for _, tc := range agentCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.env.Checker = panicChecker{}
			out, code := runAgent(t, f, tc.name, "pre", tc.shell("s1", `git commit -m "Hi"`))
			if deny, reason := tc.parse(t, out); !deny || !strings.Contains(reason, "boom") || code != tc.denyCode {
				t.Fatalf("deny=%v reason=%q code=%d", deny, reason, code)
			}
		})
	}
}

func TestAgentRejectLockCycle(t *testing.T) {
	for _, tc := range agentCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if out, code := runAgent(t, f, tc.name, "post", tc.post("s1", "approved")); code != 0 || strings.TrimSpace(out) != "" {
				t.Fatalf("post approved: %q %d", out, code)
			}
			if out, _ := runAgent(t, f, tc.name, "pre", tc.other("s1")); func() bool { d, _ := tc.parse(t, out); return d }() {
				t.Fatal("an approved result locked the session")
			}
			if out, code := runAgent(t, f, tc.name, "post", tc.post("s1", "rejected")); code != 0 || strings.TrimSpace(out) != "" {
				t.Fatalf("post rejected: %q %d", out, code)
			}
			out, code := runAgent(t, f, tc.name, "pre", tc.other("s1"))
			deny, reason := tc.parse(t, out)
			if !deny || code != tc.denyCode || !strings.Contains(reason, "abc") || !strings.Contains(reason, "too long") {
				t.Fatalf("locked session: deny=%v code=%d reason=%q", deny, code, reason)
			}
			if out, _ := runAgent(t, f, tc.name, "pre", tc.other("s2")); func() bool { d, _ := tc.parse(t, out); return d }() {
				t.Fatal("the lock leaked into another session")
			}
			f.vars["RERIGHT_BYPASS"] = "1"
			if out, _ := runAgent(t, f, tc.name, "pre", tc.other("s1")); func() bool { d, _ := tc.parse(t, out); return !d }() {
				t.Fatal("RERIGHT_BYPASS lifted a reject lock")
			}
			delete(f.vars, "RERIGHT_BYPASS")
			pout, pcode := runAgent(t, f, tc.name, "prompt", tc.prompt("s1"))
			if pcode != 0 {
				t.Fatalf("prompt exit %d", pcode)
			}
			if tc.name == AgentCursor && strings.TrimSpace(pout) != `{"continue":true}` {
				t.Fatalf("cursor prompt output %q", pout)
			}
			if tc.name != AgentCursor && strings.TrimSpace(pout) != "" {
				t.Fatalf("prompt printed %q", pout)
			}
			if out, _ := runAgent(t, f, tc.name, "pre", tc.other("s1")); func() bool { d, _ := tc.parse(t, out); return d }() {
				t.Fatal("the lock survived a user prompt")
			}
		})
	}
}

func TestAgentVariants(t *testing.T) {
	gmailText := "Subject\n\nBody text"
	gmailArgs := map[string]any{"subject": "Subject", "body": "Body text"}
	cases := []struct {
		name     string
		agent    string
		payload  any
		wantDeny bool
		parse    func(*testing.T, string) (bool, string)
		contains string
	}{
		{"codex shell array command", AgentCodex, map[string]any{"session_id": "s", "tool_name": "shell", "tool_input": map[string]any{"command": []string{"bash", "-lc", `git commit -m "Hi"`}}}, true, parseClaudeStyle, "commit message"},
		{"codex shell without command", AgentCodex, map[string]any{"session_id": "s", "tool_name": "shell", "tool_input": map[string]any{"foo": "bar"}}, true, parseClaudeStyle, "no command found"},
		{"gemini mcp_context", AgentGemini, map[string]any{"session_id": "s", "tool_name": "mcp_gmail_send_message", "mcp_context": map[string]any{"server_name": "claude_ai_Gmail", "tool_name": "send_message"}, "tool_input": gmailArgs}, true, parseGemini, "email subject and body"},
		{"gemini shell no command", AgentGemini, map[string]any{"session_id": "s", "tool_name": "run_shell_command", "tool_input": map[string]any{}}, true, parseGemini, "no command found"},
		{"copilot slash mcp name", AgentCopilot, map[string]any{"sessionId": "s", "toolName": "claude_ai_Gmail/send_message", "toolArgs": jsonString(gmailArgs)}, true, parseCopilotCLI, "email subject and body"},
		{"copilot bad toolArgs", AgentCopilot, map[string]any{"sessionId": "s", "toolName": "bash", "toolArgs": "{nope"}, true, parseCopilotCLI, "could not read"},
		{"copilot toolArgs object", AgentCopilot, map[string]any{"sessionId": "s", "toolName": "bash", "toolArgs": map[string]any{"command": `git commit -m "Hi"`}}, true, parseCopilotCLI, "commit message"},
		{"copilot bad toolArgs on unrelated tool", AgentCopilot, map[string]any{"sessionId": "s", "toolName": "view", "toolArgs": "[1]"}, true, parseCopilotCLI, "could not read"},
		{"cursor preToolUse shell", AgentCursor, map[string]any{"conversation_id": "s", "hook_event_name": "preToolUse", "tool_name": "Shell", "tool_input": map[string]any{"command": `git commit -m "Hi"`}}, true, parseCursor, "commit message"},
		{"cursor mcp without server name uses url", AgentCursor, map[string]any{"conversation_id": "s", "hook_event_name": "beforeMCPExecution", "tool_name": "send_message", "url": "https://gmail.example/mcp", "tool_input": jsonString(gmailArgs)}, true, parseCursor, "email subject and body"},
		{"cursor mcp bad tool_input", AgentCursor, map[string]any{"conversation_id": "s", "hook_event_name": "beforeMCPExecution", "server": "x", "tool_name": "t", "tool_input": "{nope"}, true, parseCursor, "could not read"},
		{"cursor prompt event through pre", AgentCursor, map[string]any{"conversation_id": "s", "hook_event_name": "beforeSubmitPrompt", "prompt": "hi"}, false, parseCursor, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			out, _ := runAgent(t, f, tc.agent, "pre", tc.payload)
			deny, reason := tc.parse(t, out)
			if deny != tc.wantDeny || !strings.Contains(reason, tc.contains) {
				t.Fatalf("deny=%v reason=%q out=%s", deny, reason, out)
			}
		})
	}

	t.Run("approved gmail through underscore name", func(t *testing.T) {
		f := newFixture(t)
		f.checker.approved[draft.Hash(gmailText)] = true
		out, _ := runAgent(t, f, AgentGemini, "pre", map[string]any{"session_id": "s", "tool_name": "mcp_claude_ai_Gmail_send_message", "tool_input": gmailArgs})
		if deny, _ := parseGemini(t, out); deny {
			t.Fatal(out)
		}
	})
}

func TestAgentCopilotDenyShapes(t *testing.T) {
	f := newFixture(t)
	cli, _ := runAgent(t, f, AgentCopilot, "pre", agentCases[2].shell("s", `git commit -m "Hi"`))
	if strings.Contains(cli, "hookSpecificOutput") {
		t.Fatalf("CLI payload got the VS Code shape: %s", cli)
	}
	vs, _ := runAgent(t, f, AgentCopilot, "pre", agentCases[3].shell("s", `git commit -m "Hi"`))
	if strings.Contains(vs, `"permissionDecision":"deny","permissionDecisionReason"`) && !strings.Contains(vs, "hookSpecificOutput") {
		t.Fatalf("VS Code payload got the CLI shape: %s", vs)
	}
	bad, _ := runAgent(t, f, AgentCopilot, "pre", "not json")
	if !strings.Contains(bad, "hookSpecificOutput") || !strings.Contains(bad, `"permissionDecision":"deny"`) {
		t.Fatalf("unparseable copilot input should carry both shapes: %s", bad)
	}
}

func TestGeminiDenyWritesStderr(t *testing.T) {
	f := newFixture(t)
	var out, errOut bytes.Buffer
	b, _ := json.Marshal(agentCases[1].shell("s", `git commit -m "Hi"`))
	code := RunAgent(context.Background(), AgentGemini, "pre", bytes.NewReader(b), &out, &errOut, f.env)
	if code != 2 || !strings.Contains(errOut.String(), "submit_for_review") {
		t.Fatalf("code %d stderr %q", code, errOut.String())
	}
}

func TestAgentFromArgs(t *testing.T) {
	cases := []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{nil, AgentClaude, false},
		{[]string{"--agent", "codex"}, AgentCodex, false},
		{[]string{"--agent=cursor"}, AgentCursor, false},
		{[]string{"--agent"}, "", true},
		{[]string{"--agent", "nope"}, "", true},
	}
	for _, tc := range cases {
		got, err := AgentFromArgs(tc.args)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%v: got %q, %v", tc.args, got, err)
		}
	}
}

func TestUnknownAgent(t *testing.T) {
	f := newFixture(t)
	var out, errOut bytes.Buffer
	if code := RunAgent(context.Background(), "nope", "pre", strings.NewReader("{}"), &out, &errOut, f.env); code != 2 {
		t.Fatalf("pre with unknown agent exits %d, want 2", code)
	}
	if code := RunAgent(context.Background(), "nope", "post", strings.NewReader("{}"), &out, &errOut, f.env); code != 1 {
		t.Fatalf("post with unknown agent exits %d, want 1", code)
	}
}
