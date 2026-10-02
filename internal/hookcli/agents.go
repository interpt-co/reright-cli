package hookcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/interpt-co/reright-cli/internal/hookcheck"
)

const (
	AgentClaude  = "claude"
	AgentCodex   = "codex"
	AgentGemini  = "gemini"
	AgentCopilot = "copilot"
	AgentCursor  = "cursor"
)

const (
	styleCLI    = "cli"
	styleVSCode = "vscode"
)

// call is a hook event reduced to what the checks need.
type call struct {
	Session  string
	Cwd      string
	Tool     string
	Input    json.RawMessage
	Response json.RawMessage
	Style    string
}

type adapter struct {
	decode   func(raw []byte) (call, error)
	deny     func(stdout, stderr io.Writer, c call, reason string) int
	promptOK func(stdout io.Writer)
	// notice shows the person a one line message when a prompt is submitted. Only agents that have such a
	// channel set it.
	notice func(stdout io.Writer, msg string)
}

var adapters = map[string]adapter{
	AgentClaude:  {decode: decodeClaude, deny: denyClaude, notice: claudeNotice},
	AgentCodex:   {decode: decodeCodex, deny: denyClaude},
	AgentGemini:  {decode: decodeGemini, deny: denyGemini},
	AgentCopilot: {decode: decodeCopilot, deny: denyCopilot},
	AgentCursor:  {decode: decodeCursor, deny: denyCursor, promptOK: cursorContinue},
}

// Agents lists the agent names accepted by --agent.
func Agents() []string {
	var out []string
	for k := range adapters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseObject(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("the payload is not a JSON object")
	}
	return m, nil
}

func str(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(m[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

func firstRaw(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok && string(bytes.TrimSpace(v)) != "null" {
			return v
		}
	}
	return nil
}

func inputObject(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return json.RawMessage(`{}`), nil
		}
		raw = json.RawMessage(s)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the tool arguments are not a JSON object: %w", err)
	}
	return raw, nil
}

func responseOf(m map[string]json.RawMessage) json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, k := range []string{"tool_response", "toolResponse", "toolResult", "tool_result", "tool_output", "result_json", "result"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(out)
	return b
}

func cwdOf(m map[string]json.RawMessage) string {
	if c := str(m, "cwd"); c != "" {
		return c
	}
	var roots []string
	if json.Unmarshal(m["workspace_roots"], &roots) == nil && len(roots) > 0 {
		return roots[0]
	}
	return ""
}

var shellTools = map[string]bool{
	"bash": true, "shell": true, "sh": true, "powershell": true, "run_shell_command": true,
	"local_shell": true, "exec_command": true, "runinterminal": true, "run_in_terminal": true,
	"runterminalcommand": true, "terminal": true, "shell_command": true, "container.exec": true,
	"unified_exec": true, "execute": true, "runcommands": true, "run_terminal_cmd": true,
}

var safeShell = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func posixQuote(s string) string {
	if safeShell.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var commandKeys = []string{"command", "cmd", "script", "commandLine", "commands"}

func hasShellKey(in json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(in, &m) != nil {
		return false
	}
	for _, k := range []string{"command", "cmd"} {
		if v, ok := m[k]; ok && string(bytes.TrimSpace(v)) != "null" {
			return true
		}
	}
	return false
}

func commandText(k string, v json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, nil
	}
	var parts []string
	if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
		if k == "commands" {
			return strings.Join(parts, "\n"), nil
		}
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = posixQuote(p)
		}
		return strings.Join(quoted, " "), nil
	}
	return "", fmt.Errorf("the %q value is not a string or a list of strings", k)
}

func shellInput(in json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(in, &m); err != nil {
		return nil, err
	}
	var found []string
	for _, k := range commandKeys {
		v, ok := m[k]
		if !ok || string(bytes.TrimSpace(v)) == "null" {
			continue
		}
		s, err := commandText(k, v)
		if err != nil {
			return nil, err
		}
		found = append(found, s)
	}
	if len(found) == 0 {
		return nil, errors.New("no command found in the shell tool arguments")
	}
	for _, s := range found[1:] {
		if s != found[0] {
			return nil, errors.New("the shell tool arguments hold more than one command and they differ")
		}
	}
	return json.Marshal(map[string]string{"command": found[0]})
}

func mcpFromUnderscores(name string) string {
	rest := strings.TrimPrefix(name, "mcp_")
	lower := strings.ToLower(rest)
	for _, marker := range []string{"gmail", "claude-in-chrome", "claude_in_chrome", "workspace"} {
		if i := strings.Index(lower, marker); i >= 0 {
			end := i + len(marker)
			return "mcp__" + rest[:end] + "__" + strings.TrimPrefix(rest[end:], "_")
		}
	}
	if i := strings.Index(rest, "_"); i >= 0 {
		return "mcp__" + rest[:i] + "__" + rest[i+1:]
	}
	return "mcp__" + rest + "__"
}

func normalizeTool(name string, in json.RawMessage) (string, json.RawMessage, error) {
	switch {
	case strings.HasPrefix(name, "mcp__"):
		return name, in, nil
	case strings.HasPrefix(name, "mcp_"):
		return mcpFromUnderscores(name), in, nil
	case name == "Bash" || shellTools[strings.ToLower(name)] || hasShellKey(in):
		si, err := shellInput(in)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", name, err)
		}
		return "Bash", si, nil
	}
	return name, in, nil
}

func finish(c call, name string, rawInput json.RawMessage) (call, error) {
	in, err := inputObject(rawInput)
	if err != nil {
		return c, err
	}
	c.Tool, c.Input, err = normalizeTool(name, in)
	return c, err
}

func decodeClaude(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	return call{
		Session: str(m, "session_id"), Cwd: str(m, "cwd"), Tool: str(m, "tool_name"),
		Input: m["tool_input"], Response: m["tool_response"],
	}, nil
}

func decodeCodex(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	c := call{Session: str(m, "session_id"), Cwd: str(m, "cwd"), Response: responseOf(m)}
	return finish(c, str(m, "tool_name"), m["tool_input"])
}

func decodeGemini(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	c := call{Session: str(m, "session_id"), Cwd: str(m, "cwd"), Response: responseOf(m)}
	name := str(m, "tool_name")
	var mc map[string]json.RawMessage
	if json.Unmarshal(m["mcp_context"], &mc) == nil {
		if server, tool := str(mc, "server_name", "serverName"), str(mc, "tool_name", "toolName"); server != "" && tool != "" {
			name = "mcp__" + server + "__" + tool
		}
	}
	return finish(c, name, m["tool_input"])
}

func decodeCopilot(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	c := call{Session: str(m, "sessionId", "session_id"), Cwd: cwdOf(m), Response: responseOf(m), Style: styleVSCode}
	if _, ok := m["toolName"]; ok {
		c.Style = styleCLI
	}
	name := str(m, "toolName", "tool_name")
	if i := strings.Index(name, "/"); i > 0 && !strings.HasPrefix(name, "mcp_") {
		name = "mcp__" + name[:i] + "__" + name[i+1:]
	}
	return finish(c, name, firstRaw(m, "toolArgs", "tool_input", "toolInput"))
}

func decodeCursor(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	c := call{Session: str(m, "conversation_id", "session_id", "sessionId"), Cwd: cwdOf(m), Response: responseOf(m)}
	name := str(m, "tool_name", "tool", "toolName")
	_, hasCommand := m["command"]
	switch {
	case name == "" && hasCommand:
		return finish(c, "Bash", mustJSON(map[string]json.RawMessage{"command": m["command"]}))
	case strings.HasPrefix(name, "MCP:"):
		parts := strings.SplitN(strings.TrimPrefix(name, "MCP:"), ":", 2)
		if len(parts) == 2 {
			name = "mcp__" + parts[0] + "__" + parts[1]
		} else {
			name = "mcp__cursor__" + parts[0]
		}
	case name != "" && !strings.HasPrefix(name, "mcp__") && !shellTools[strings.ToLower(name)]:
		server := str(m, "server", "server_name", "serverName", "mcp_server")
		if server == "" && (str(m, "hook_event_name") == "beforeMCPExecution" || hookcheck.SendLike(name)) {
			server = hookcheck.UnknownServer
		}
		if server != "" {
			name = "mcp__" + server + "__" + name
		}
	}
	return finish(c, name, firstRaw(m, "tool_input", "parameters", "arguments", "toolInput"))
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func denyClaude(stdout, _ io.Writer, _ call, reason string) int {
	json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
	return 0
}

func denyGemini(stdout, stderr io.Writer, _ call, reason string) int {
	json.NewEncoder(stdout).Encode(map[string]string{"decision": "deny", "reason": reason})
	fmt.Fprintln(stderr, reason)
	return 2
}

func denyCopilot(stdout, _ io.Writer, c call, reason string) int {
	out := map[string]any{}
	if c.Style != styleVSCode {
		out["permissionDecision"] = "deny"
		out["permissionDecisionReason"] = reason
	}
	if c.Style != styleCLI {
		out["hookSpecificOutput"] = map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

func denyCursor(stdout, _ io.Writer, _ call, reason string) int {
	json.NewEncoder(stdout).Encode(map[string]string{"permission": "deny", "user_message": reason, "agent_message": reason})
	return 0
}

func cursorContinue(stdout io.Writer) {
	json.NewEncoder(stdout).Encode(map[string]bool{"continue": true})
}

// ParseArgs reads the command line after the program name: one subcommand
// and an optional --agent NAME or --agent=NAME, in any order. Any other flag,
// a second subcommand or a repeated --agent is an error. The subcommand is
// returned even on error so the caller can pick the exit code.
func ParseArgs(args []string) (agent, sub string, err error) {
	agent = ""
	var bad error
	fail := func(e error) {
		if bad == nil {
			bad = e
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var val string
		switch {
		case a == "--agent" || a == "-agent":
			if i+1 >= len(args) {
				fail(errors.New("--agent needs a value"))
				continue
			}
			i++
			val = args[i]
		case strings.HasPrefix(a, "--agent=") || strings.HasPrefix(a, "-agent="):
			val = a[strings.Index(a, "=")+1:]
		case strings.HasPrefix(a, "-"):
			fail(fmt.Errorf("unknown flag %q", a))
			continue
		default:
			if sub != "" {
				fail(fmt.Errorf("unexpected argument %q", a))
			} else {
				sub = a
			}
			continue
		}
		if agent != "" {
			fail(errors.New("--agent was given more than once"))
		}
		agent = val
		if _, ok := adapters[val]; !ok {
			fail(fmt.Errorf("unknown agent %q (want %s)", val, strings.Join(Agents(), ", ")))
		}
	}
	if bad != nil {
		return "", sub, bad
	}
	if agent == "" {
		agent = AgentClaude
	}
	if sub == "" {
		return "", "", errors.New("missing subcommand (want pre, post or prompt)")
	}
	return agent, sub, nil
}

// AgentFromArgs reads the agent from the arguments after the subcommand.
// Without --agent the agent is claude. Unrecognised arguments are an error.
func AgentFromArgs(args []string) (string, error) {
	agent, _, err := ParseArgs(append([]string{"_"}, args...))
	return agent, err
}

// UsageExit is the exit code for a usage error. It blocks (2) for pre and for
// anything that is not a known subcommand, so a typo never turns into an allow.
func UsageExit(sub string) int {
	if sub == "post" || sub == "prompt" {
		return 1
	}
	return 2
}
