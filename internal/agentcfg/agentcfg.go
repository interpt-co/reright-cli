// Package agentcfg builds the user-level hook configuration that makes each
// supported agent call reright-hook. It only returns text and paths; the caller
// decides whether and how to write them.
package agentcfg

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	Claude  = "claude"
	Codex   = "codex"
	Gemini  = "gemini"
	Copilot = "copilot"
	Cursor  = "cursor"
)

const (
	FormatJSON = "json"
	FormatTOML = "toml"
)

const (
	preTimeoutSec  = 30
	postTimeoutSec = 10
	waitForReview  = "wait_for_review"
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
	out := []string{Claude, Codex, Gemini, Copilot, Cursor}
	sort.Strings(out)
	return out
}

// For returns the snippets for one agent. bin is the command used to run
// reright-hook: a bare name or an absolute path.
func For(agent, bin string) ([]Snippet, error) {
	if strings.TrimSpace(bin) == "" {
		return nil, fmt.Errorf("agentcfg: empty reright-hook command")
	}
	q := shellQuote(bin)
	switch agent {
	case Claude:
		return claude(q)
	case Codex:
		return codex(q)
	case Gemini:
		return gemini(q)
	case Copilot:
		return copilot(q)
	case Cursor:
		return cursor(q)
	}
	return nil, fmt.Errorf("agentcfg: unknown agent %q (want %s)", agent, strings.Join(Agents(), ", "))
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

func claude(q string) ([]Snippet, error) {
	content, err := render(obj{"hooks": obj{
		"PreToolUse":       []any{obj{"matcher": "*", "hooks": []any{obj{"type": "command", "command": q + " pre", "timeout": preTimeoutSec}}}},
		"PostToolUse":      []any{obj{"matcher": "mcp__reright__" + waitForReview, "hooks": []any{obj{"type": "command", "command": q + " post", "timeout": postTimeoutSec}}}},
		"UserPromptSubmit": []any{obj{"hooks": []any{obj{"type": "command", "command": q + " prompt", "timeout": postTimeoutSec}}}},
	}})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".claude/settings.json", Format: FormatJSON, Content: content, Merge: true,
		Note: "Claude Code is the default agent, so the commands need no --agent flag.",
	}}, nil
}

func codex(q string) ([]Snippet, error) {
	content, err := render(obj{"hooks": obj{
		"PreToolUse":       []any{obj{"hooks": []any{obj{"type": "command", "command": q + " pre --agent codex", "timeout": preTimeoutSec}}}},
		"PostToolUse":      []any{obj{"matcher": "mcp__reright__" + waitForReview, "hooks": []any{obj{"type": "command", "command": q + " post --agent codex", "timeout": postTimeoutSec}}}},
		"UserPromptSubmit": []any{obj{"hooks": []any{obj{"type": "command", "command": q + " prompt --agent codex", "timeout": postTimeoutSec}}}},
	}})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".codex/hooks.json", Format: FormatJSON, Content: content, Merge: true,
		Note: "Codex asks the user to trust new hooks. [features] hooks must not be false. Managed installs can pin hooks in requirements.toml with allow_managed_hooks_only.",
	}}, nil
}

func gemini(q string) ([]Snippet, error) {
	hook := func(sub string, ms int) []any {
		return []any{obj{"matcher": ".*", "hooks": []any{obj{"type": "command", "name": "reright-" + sub, "command": q + " " + sub + " --agent gemini", "timeout": ms}}}}
	}
	prompt := []any{obj{"hooks": []any{obj{"type": "command", "name": "reright-prompt", "command": q + " prompt --agent gemini", "timeout": postTimeoutSec * 1000}}}}
	post := []any{obj{"matcher": ".*" + waitForReview, "hooks": []any{obj{"type": "command", "name": "reright-post", "command": q + " post --agent gemini", "timeout": postTimeoutSec * 1000}}}}
	content, err := render(obj{"hooks": obj{
		"BeforeTool":  hook("pre", preTimeoutSec*1000),
		"AfterTool":   post,
		"BeforeAgent": prompt,
	}})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".gemini/settings.json", Format: FormatJSON, Content: content, Merge: true,
		Note: "Gemini timeouts are in milliseconds. A deny exits 2, which Gemini treats as a block; other failing exit codes only warn, so reright-hook turns its own errors into exit 2.",
	}}, nil
}

func copilot(q string) ([]Snippet, error) {
	entry := func(sub string, sec int, matcher string) obj {
		e := obj{"type": "command", "bash": q + " " + sub + " --agent copilot", "timeoutSec": sec}
		if matcher != "" {
			e["matcher"] = matcher
		}
		return e
	}
	content, err := render(obj{"version": 1, "hooks": obj{
		"preToolUse":          []any{entry("pre", preTimeoutSec, "")},
		"postToolUse":         []any{entry("post", postTimeoutSec, ".*"+waitForReview)},
		"userPromptSubmitted": []any{entry("prompt", postTimeoutSec, "")},
	}})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".copilot/hooks/reright.json", Format: FormatJSON, Content: content,
		Note: "Copilot CLI hook file, owned by reright. A timeout fails open in the CLI, so the timeoutSec here is longer than the 20 second limit reright-hook sets for itself. Only this one file is written: whether VS Code agent mode reads ~/.copilot/hooks, and in which shape, is UNVERIFIED, and a second file could register the hooks twice.",
	}}, nil
}

func cursor(q string) ([]Snippet, error) {
	closed := func(sub, matcher string) obj {
		e := obj{"command": q + " " + sub + " --agent cursor", "timeout": preTimeoutSec, "failClosed": true}
		if matcher != "" {
			e["matcher"] = matcher
		}
		return e
	}
	content, err := render(obj{"version": 1, "hooks": obj{
		"beforeShellExecution": []any{closed("pre", "")},
		"beforeMCPExecution":   []any{closed("pre", "")},
		"beforeSubmitPrompt":   []any{obj{"command": q + " prompt --agent cursor", "timeout": postTimeoutSec}},
		"afterMCPExecution":    []any{obj{"command": q + " post --agent cursor", "timeout": postTimeoutSec, "matcher": ".*" + waitForReview}},
	}})
	if err != nil {
		return nil, err
	}
	return []Snippet{{
		Path: ".cursor/hooks.json", Format: FormatJSON, Content: content, Merge: true,
		Note: "failClosed makes Cursor block the call when the hook crashes or times out. preToolUse is supported by the adapter but left out here so shell and MCP calls are not checked twice.",
	}}, nil
}
