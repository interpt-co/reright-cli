package onboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/interpt-co/reright-cli/internal/agentcfg"
)

// Claude Code is the only agent install sets up.
const agentClaude = agentcfg.Claude

var agentOrder = []string{agentClaude}

// legacyAgents are the agents reright 0.1 could also set up, with the config folder each one used. Their
// records in install.json still load, so reright uninstall can take out what was written for them, but
// install and doctor no longer handle them.
var legacyAgents = map[string]string{
	"codex":   ".codex",
	"gemini":  ".gemini",
	"copilot": ".copilot",
	"cursor":  ".cursor",
}

var legacyOrder = []string{"codex", "gemini", "copilot", "cursor"}

type agentSpec struct {
	Name         string
	Binaries     []string
	Dirs         []string
	ProjectFiles []string
}

var agentSpecs = map[string]agentSpec{
	agentClaude: {
		Name:         agentClaude,
		Binaries:     []string{"claude"},
		Dirs:         []string{".claude"},
		ProjectFiles: []string{".claude/settings.json", ".claude/settings.local.json"},
	},
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func detectAgent(name string, r Runner, home string) (string, bool) {
	spec, ok := agentSpecs[name]
	if !ok {
		return "", false
	}
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
	return "", false
}

func validAgent(name string) bool {
	_, ok := agentSpecs[name]
	return ok
}

func isLegacyAgent(name string) bool {
	_, ok := legacyAgents[name]
	return ok
}

// agentDir is the config folder of an agent, relative to the home.
func agentDir(name string) string {
	if d, ok := legacyAgents[name]; ok {
		return d
	}
	return agentSpecs[name].Dirs[0]
}

// normalizeAgents checks the names given with --agent. Only claude is accepted, except that uninstall
// (legacy true) also takes the names of the agents earlier versions set up.
func normalizeAgents(names []string, legacy bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		switch {
		case validAgent(n), legacy && isLegacyAgent(n):
		case isLegacyAgent(n):
			return nil, fmt.Errorf("reright no longer supports %s. It sets up Claude Code only (--agent claude). reright uninstall --agent %s removes what an earlier version wrote for it", n, n)
		default:
			return nil, fmt.Errorf("unsupported agent %q (reright sets up Claude Code only: --agent claude)", n)
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
	Partial   []string
}

func mcpURL(server string) string { return server + "/mcp" }

func buildPlan(agent string, e planEnv) (agentPlan, error) {
	p := agentPlan{Agent: agent}
	if agent != agentClaude {
		return p, fmt.Errorf("unsupported agent %q", agent)
	}
	home := func(rel string) string { return filepath.Join(e.Home, rel) }
	p.Edits = addPart(p.Edits, home(".claude/settings.json"), false, false, claudeHooksPart(e.HookPath))
	p.Edits = addPart(p.Edits, home(".claude/CLAUDE.md"), false, false, rulesPart(ruleBlock))
	p.ClaudeMCP = true
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
