package agentcfg

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/hookcli"
)

func TestForEveryAgent(t *testing.T) {
	for _, agent := range Agents() {
		snips, err := For(agent, "/home/u/.local/bin/reright-hook")
		if err != nil || len(snips) == 0 {
			t.Fatalf("%s: %v %d", agent, err, len(snips))
		}
		for _, s := range snips {
			if filepath.IsAbs(s.Path) || strings.Contains(s.Path, "..") {
				t.Errorf("%s: path %q is not relative to home", agent, s.Path)
			}
			if !strings.HasPrefix(s.Path, ".") {
				t.Errorf("%s: path %q is not a dot directory in the home", agent, s.Path)
			}
			if s.Format != FormatJSON {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(s.Content), &v); err != nil {
				t.Errorf("%s %s: invalid JSON: %v", agent, s.Path, err)
			}
			if !strings.Contains(s.Content, "/home/u/.local/bin/reright-hook") {
				t.Errorf("%s %s: binary path missing", agent, s.Path)
			}
			if agent != Claude && !strings.Contains(s.Content, "--agent "+agent) {
				t.Errorf("%s %s: no --agent flag", agent, s.Path)
			}
		}
	}
}

func TestAgentNamesMatchHookcli(t *testing.T) {
	have := map[string]bool{}
	for _, a := range hookcli.Agents() {
		have[a] = true
	}
	for _, a := range Agents() {
		if !have[a] {
			t.Errorf("agentcfg agent %q is unknown to hookcli", a)
		}
	}
	if len(have) != len(Agents()) {
		t.Errorf("hookcli has %v, agentcfg has %v", hookcli.Agents(), Agents())
	}
}

func TestCursorFailsClosed(t *testing.T) {
	snips, _ := For(Cursor, "reright-hook")
	var cfg struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command    string `json:"command"`
			FailClosed bool   `json:"failClosed"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(snips[0].Content), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version %d", cfg.Version)
	}
	for _, ev := range []string{"beforeShellExecution", "beforeMCPExecution"} {
		h := cfg.Hooks[ev]
		if len(h) != 1 || !h[0].FailClosed || h[0].Command != "reright-hook pre --agent cursor" {
			t.Errorf("%s: %+v", ev, h)
		}
	}
	if snips[0].Path != ".cursor/hooks.json" {
		t.Fatal(snips[0].Path)
	}
}

func TestCodexAndGeminiShape(t *testing.T) {
	c, _ := For(Codex, "reright-hook")
	if c[0].Path != ".codex/hooks.json" || !strings.Contains(c[0].Content, `"PreToolUse"`) || !strings.Contains(c[0].Content, "mcp__reright__wait_for_review") {
		t.Fatalf("codex: %+v", c[0])
	}
	g, _ := For(Gemini, "reright-hook")
	if g[0].Path != ".gemini/settings.json" || !strings.Contains(g[0].Content, `"BeforeTool"`) || !strings.Contains(g[0].Content, `"AfterTool"`) || !strings.Contains(g[0].Content, `"BeforeAgent"`) {
		t.Fatalf("gemini: %+v", g[0])
	}
	if !g[0].Merge {
		t.Fatal("gemini settings.json must be merged, not replaced")
	}
}

func TestCopilotOwnsItsFiles(t *testing.T) {
	c, _ := For(Copilot, "reright-hook")
	if len(c) != 1 || c[0].Merge || !strings.Contains(c[0].Content, `"version": 1`) || !strings.Contains(c[0].Content, `"timeoutSec": 30`) {
		t.Fatalf("copilot: %+v", c)
	}
}

func TestQuotingAndErrors(t *testing.T) {
	s, err := For(Codex, "/home/my user/bin/reright-hook")
	if err != nil || !strings.Contains(s[0].Content, `'/home/my user/bin/reright-hook' pre --agent codex`) {
		t.Fatalf("%v %v", err, s)
	}
	if _, err := For("nope", "x"); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if _, err := For(Codex, " "); err == nil {
		t.Fatal("empty command accepted")
	}
}

func TestHookPathQuoting(t *testing.T) {
	cases := map[string]string{
		"/home/u/hY(1)/reright-hook": `'/home/u/hY(1)/reright-hook'`,
		"/home/a b/reright-hook":     `'/home/a b/reright-hook'`,
		"/home/o'k/reright-hook":     `'/home/o'\''k/reright-hook'`,
		"/home/$HOME/reright-hook":   `'/home/$HOME/reright-hook'`,
		"/home/a;b/reright-hook":     `'/home/a;b/reright-hook'`,
		"/home/a&b|c/reright-hook":   `'/home/a&b|c/reright-hook'`,
		"/home/a*?/reright-hook":     `'/home/a*?/reright-hook'`,
		"/home/u/reright-hook":       `/home/u/reright-hook`,
	}
	for _, agent := range Agents() {
		for in, want := range cases {
			snips, err := For(agent, in)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range snips {
				enc, _ := json.Marshal(want + " pre")
				if !strings.Contains(s.Content, strings.Trim(string(enc), `"`)) {
					t.Errorf("%s %q: %s", agent, in, s.Content)
				}
			}
		}
	}
}

func TestMatchersAndSingleCopilotFile(t *testing.T) {
	c, _ := For(Codex, "reright-hook")
	if strings.Contains(c[0].Content, `"matcher": "*"`) {
		t.Fatal(`codex uses the undocumented matcher "*"`)
	}
	cu, _ := For(Cursor, "reright-hook")
	if !strings.Contains(cu[0].Content, "wait_for_review") {
		t.Fatalf("cursor post has no matcher: %s", cu[0].Content)
	}
	co, _ := For(Copilot, "reright-hook")
	if len(co) != 1 {
		t.Fatalf("copilot writes %d hook files", len(co))
	}
}
