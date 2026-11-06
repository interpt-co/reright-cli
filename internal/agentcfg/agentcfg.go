// Package agentcfg builds the user-level hook configuration that makes Claude Code
// call reright-hook. It only returns text and paths; the caller decides whether and
// how to write them. Claude Code is the only supported agent.
package agentcfg

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const Claude = "claude"

const FormatJSON = "json"

const (
	preTimeoutSec  = 30
	postTimeoutSec = 10
	rewriteTool    = "rewrite"
	rewriteResult  = "rewrite_result"
)

// Snippet is one config file, or a fragment of one, for an agent.
type Snippet struct {
	// Path is relative to the user's home directory. It is never inside a repository.
	Path    string
	Format  string
	Content string
	// Merge is true when Content is a fragment that must be merged into the file at
	// Path (which usually has other settings), and false when reright owns the whole file.
	Merge bool
	Note  string
}

// Agents lists the agents For accepts.
func Agents() []string {
	return []string{Claude}
}

// For returns the snippets for one agent. bin is the command used to run
// reright-hook: a bare name or an absolute path.
func For(agent, bin string) ([]Snippet, error) {
	if strings.TrimSpace(bin) == "" {
		return nil, fmt.Errorf("agentcfg: empty reright-hook command")
	}
	q := shellQuote(bin)
	if agent == Claude {
		return claude(q)
	}
	return nil, fmt.Errorf("agentcfg: unsupported agent %q (reright supports %s only)", agent, strings.Join(Agents(), ", "))
}

var safeShell = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if safeShell.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func render(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

type obj = map[string]any

// ClaudeHook is one entry reright adds to the hooks of ~/.claude/settings.json.
type ClaudeHook struct {
	Event   string
	Matcher string
	Sub     string
	Timeout int
}

// ClaudeHooks is the single list of Claude Code hooks. The installer merges it into
// settings.json and doctor checks for it.
var ClaudeHooks = []ClaudeHook{
	{"PreToolUse", "*", "pre", preTimeoutSec},
	{"PostToolUse", ClaudePostMatcher, "post", postTimeoutSec},
	{"UserPromptSubmit", "", "prompt", postTimeoutSec},
	{"Stop", "", "stop", postTimeoutSec},
	{"SubagentStop", "", "stop", postTimeoutSec},
}

// ClaudePostMatcher picks the reright MCP tools the post hook watches.
const ClaudePostMatcher = "mcp__reright__(" + rewriteTool + "|" + rewriteResult + ")"

func claude(q string) ([]Snippet, error) {
	hooks := obj{}
	for _, h := range ClaudeHooks {
		group := obj{"hooks": []any{obj{"type": "command", "command": q + " " + h.Sub, "timeout": h.Timeout}}}
		if h.Matcher != "" {
			group["matcher"] = h.Matcher
		}
		hooks[h.Event] = []any{group}
	}
	content, err := render(obj{"hooks": hooks})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".claude/settings.json", Format: FormatJSON, Content: content, Merge: true,
		Note: "Claude Code is the default agent, so the commands need no --agent flag.",
	}}, nil
}
