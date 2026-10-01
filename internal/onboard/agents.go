package onboard

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/interpt-co/reright-cli/internal/agentcfg"
)

const (
	agentClaude  = agentcfg.Claude
	agentCodex   = agentcfg.Codex
	agentGemini  = agentcfg.Gemini
	agentCopilot = agentcfg.Copilot
	agentCursor  = agentcfg.Cursor
)

var agentOrder = []string{agentClaude, agentCodex, agentGemini, agentCopilot, agentCursor}

type agentSpec struct {
	Name         string
	Binaries     []string
	Dirs         []string
	ProjectFiles []string
	Unverified   []string
}

var agentSpecs = map[string]agentSpec{
	agentClaude: {
		Name:         agentClaude,
		Binaries:     []string{"claude"},
		Dirs:         []string{".claude"},
		ProjectFiles: []string{".claude/settings.json", ".claude/settings.local.json"},
	},
	agentCodex: {
		Name:         agentCodex,
		Binaries:     []string{"codex"},
		Dirs:         []string{".codex"},
		ProjectFiles: []string{".codex/hooks.json", ".codex/config.toml"},
		Unverified: []string{
			"hooks may not run with --dangerously-bypass-approvals-and-sandbox (not tested)",
			"the payload and deny format are taken from documentation and were never run against a real Codex",
			"Codex asks you to trust new hooks the first time it starts",
		},
	},
	agentGemini: {
		Name:         agentGemini,
		Binaries:     []string{"gemini"},
		Dirs:         []string{".gemini"},
		ProjectFiles: []string{".gemini/settings.json"},
		Unverified: []string{
			"hooks may not run with --yolo (not tested)",
			"the payload fields for MCP tools and the AfterTool and BeforeAgent events are UNVERIFIED",
		},
	},
	agentCopilot: {
		Name:         agentCopilot,
		Binaries:     []string{"copilot"},
		Dirs:         []string{".copilot"},
		ProjectFiles: []string{".github/hooks", ".github/copilot/hooks", ".claude/settings.json"},
		Unverified: []string{
			"hooks may not run with --allow-all-tools (not tested)",
			"whether Copilot runs hooks for MCP tools at all is UNVERIFIED, so the Gmail and browser checks may not fire",
			"a hook timeout fails open in the Copilot CLI",
			"the user-level hook location for VS Code agent mode is UNVERIFIED, and VS Code user rules are not written",
		},
	},
	agentCursor: {
		Name:         agentCursor,
		Binaries:     []string{"cursor-agent", "cursor"},
		Dirs:         []string{".cursor"},
		ProjectFiles: []string{".cursor/hooks.json"},
		Unverified: []string{
			"hooks may not run in auto-run (yolo) mode (not tested)",
			"beforeMCPExecution does not run in cloud agents",
			"user rules live in Cursor's settings, so the rules text must be pasted by hand",
		},
	},
}

func vscodeUserDir(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	}
	return filepath.Join(home, ".config", "Code", "User")
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func detectAgent(name string, r Runner, home string) (string, bool) {
	spec := agentSpecs[name]
	for _, b := range spec.Binaries {
		if path, err := r.LookPath(b); err == nil {
			return b + " on PATH (" + path + ")", true
		}
	}
	for _, d := range spec.Dirs {
		if isDir(filepath.Join(home, d)) {
			return "~/" + d + " exists", true
		}
	}
	if name == agentCopilot {
		if ext := copilotExtension(home); ext != "" {
			return "the Copilot extension is installed (" + ext + ")", true
		}
	}
	return "", false
}

func copilotExtension(home string) string {
	for _, dir := range []string{".vscode", ".vscode-insiders", ".vscode-server"} {
		matches, _ := filepath.Glob(filepath.Join(home, dir, "extensions", "github.copilot*"))
		for _, m := range matches {
			if isDir(m) {
				return m
			}
		}
	}
	return ""
}

func validAgent(name string) bool {
	_, ok := agentSpecs[name]
	return ok
}

func normalizeAgents(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if !validAgent(n) {
			return nil, fmt.Errorf("unsupported agent %q (supported: %s)", n, strings.Join(agentOrder, ", "))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

type planEnv struct {
	Home     string
	HookPath string
	Server   string
	Token    string
}

type agentPlan struct {
	Agent     string
	Edits     []fileEdit
	ClaudeMCP bool
	Manual    []string
	Partial   []string
}

func ruleFor(agent string) string { return ruleBlock }

func mcpURL(server string) string { return server + "/mcp" }

func buildPlan(agent string, e planEnv) (agentPlan, error) {
	p := agentPlan{Agent: agent}
	bearer := "Bearer " + e.Token
	url := mcpURL(e.Server)
	home := func(rel string) string { return filepath.Join(e.Home, rel) }
	rules := rulesPart(ruleFor(agent))
	switch agent {
	case agentClaude:
		p.Edits = addPart(p.Edits, home(".claude/settings.json"), false, false, claudeHooksPart(e.HookPath))
		p.Edits = addPart(p.Edits, home(".claude/CLAUDE.md"), false, false, rules)
		p.ClaudeMCP = true
		return p, nil
	case agentCodex:
		snips, err := agentcfg.For(agent, e.HookPath)
		if err != nil {
			return p, err
		}
		for _, s := range snips {
			p.Edits = addPart(p.Edits, home(s.Path), false, false, jsonHooksPart(s.Content))
		}
		p.Edits = addPart(p.Edits, home(".codex/config.toml"), true, false, tomlMCPPart(url, e.Token))
		p.Edits = addPart(p.Edits, home(".codex/AGENTS.md"), false, false, rules)
	case agentGemini:
		snips, err := agentcfg.For(agent, e.HookPath)
		if err != nil {
			return p, err
		}
		for _, s := range snips {
			p.Edits = addPart(p.Edits, home(s.Path), false, false, jsonHooksPart(s.Content))
		}
		p.Edits = addPart(p.Edits, home(".gemini/settings.json"), true, false,
			jsonMCPPart("mcpServers", map[string]any{"httpUrl": url, "headers": map[string]any{"Authorization": bearer}}))
		p.Edits = addPart(p.Edits, home(".gemini/GEMINI.md"), false, false, rules)
	case agentCopilot:
		snips, err := agentcfg.For(agent, e.HookPath)
		if err != nil {
			return p, err
		}
		for _, s := range snips {
			p.Edits = addPart(p.Edits, home(s.Path), false, false, jsonHooksPart(s.Content))
		}
		p.Edits = addPart(p.Edits, home(".copilot/mcp-config.json"), true, false,
			jsonMCPPart("mcpServers", map[string]any{"type": "http", "url": url, "headers": map[string]any{"Authorization": bearer}, "tools": []any{"*"}}))
		p.Edits = addPart(p.Edits, home(".copilot/copilot-instructions.md"), false, false, rules)
		if isDir(vscodeUserDir(e.Home)) {
			p.Edits = addPart(p.Edits, filepath.Join(vscodeUserDir(e.Home), "mcp.json"), true, true,
				jsonMCPPart("servers", map[string]any{"type": "http", "url": url, "headers": map[string]any{"Authorization": bearer}}))
		}
	case agentCursor:
		snips, err := agentcfg.For(agent, e.HookPath)
		if err != nil {
			return p, err
		}
		for _, s := range snips {
			p.Edits = addPart(p.Edits, home(s.Path), false, false, jsonHooksPart(s.Content))
		}
		p.Edits = addPart(p.Edits, home(".cursor/mcp.json"), true, false,
			jsonMCPPart("mcpServers", map[string]any{"url": url, "headers": map[string]any{"Authorization": bearer}}))
		p.Manual = append(p.Manual, "Cursor keeps user rules in Cursor Settings, Rules, so there is no file reright can write. Paste the rule text (shown below) there.")
	default:
		return p, fmt.Errorf("unsupported agent %q", agent)
	}
	return p, nil
}

func partsOfKind(p agentPlan, kind string) []fileEdit {
	var out []fileEdit
	for _, e := range p.Edits {
		for _, pt := range e.Parts {
			if pt.Kind == kind {
				out = append(out, fileEdit{Path: e.Path, Parts: []part{pt}, Optional: e.Optional})
				break
			}
		}
	}
	return out
}
