package hookcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// AgentClaude is the only agent reright-hook supports. --agent claude is still accepted.
const AgentClaude = "claude"

// call is a hook event reduced to what the checks need.
type call struct {
	Session string
	// Agent is the subagent id. Claude Code sends it only for calls made inside a subagent.
	Agent    string
	Cwd      string
	Tool     string
	Input    json.RawMessage
	Response json.RawMessage
}

type adapter struct {
	decode func(raw []byte) (call, error)
	deny   func(stdout, stderr io.Writer, c call, reason string) int
	// notice shows the person a one line message when a prompt is submitted.
	notice func(stdout io.Writer, n notice)
	stop   func(stdout io.Writer, block, note string)
}

var adapters = map[string]adapter{
	AgentClaude: {decode: decodeClaude, deny: denyClaude, notice: claudeNotice, stop: claudeStop},
}

// legacyAgents are the agents reright 0.1 could set up. Hook configs written for them may still call
// reright-hook with --agent NAME. Those calls fail open, so an old config never blocks that agent.
var legacyAgents = map[string]bool{"codex": true, "gemini": true, "copilot": true, "cursor": true}

// IsLegacyAgent reports whether name is one of the agents reright no longer supports.
func IsLegacyAgent(name string) bool { return legacyAgents[name] }

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

func decodeClaude(raw []byte) (call, error) {
	m, err := parseObject(raw)
	if err != nil {
		return call{}, err
	}
	return call{
		Session: str(m, "session_id"), Agent: str(m, "agent_id"), Cwd: str(m, "cwd"), Tool: str(m, "tool_name"),
		Input: m["tool_input"], Response: m["tool_response"],
	}, nil
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

// ParseArgs reads the command line after the program name: one subcommand
// and an optional --agent NAME or --agent=NAME, in any order. Any other flag,
// a second subcommand or a repeated --agent is an error. The subcommand is
// returned even on error so the caller can pick the exit code.
func ParseArgs(args []string) (agent, sub string, err error) {
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
		if _, ok := adapters[val]; !ok && !legacyAgents[val] {
			fail(fmt.Errorf("unknown agent %q (reright supports %s only)", val, strings.Join(Agents(), ", ")))
		}
	}
	if bad != nil {
		return "", sub, bad
	}
	if agent == "" {
		agent = AgentClaude
	}
	if sub == "" {
		return "", "", errors.New("missing subcommand (want pre, post, prompt or stop)")
	}
	return agent, sub, nil
}

// UsageExit is the exit code for a usage error. It blocks (2) for pre and for
// anything that is not a known subcommand, so a typo never turns into an allow.
func UsageExit(sub string) int {
	if sub == "post" || sub == "prompt" || sub == "stop" {
		return 1
	}
	return 2
}
