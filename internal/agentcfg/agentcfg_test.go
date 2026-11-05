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

func TestQuotingAndErrors(t *testing.T) {
	s, err := For(Claude, "/home/my user/bin/reright-hook")
	if err != nil || !strings.Contains(s[0].Content, `'/home/my user/bin/reright-hook' pre`) {
		t.Fatalf("%v %v", err, s)
	}
	for _, agent := range []string{"nope", "codex", "gemini", "copilot", "cursor"} {
		if _, err := For(agent, "x"); err == nil {
			t.Fatalf("unsupported agent %s accepted", agent)
		}
	}
	if _, err := For(Claude, " "); err == nil {
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

func TestClaudeStopsOnlyWhenNothingIsPending(t *testing.T) {
	snips, err := For(Claude, "/home/u/.local/bin/reright-hook")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(snips[0].Content), &cfg); err != nil {
		t.Fatal(err)
	}
	stop := cfg.Hooks["Stop"]
	if len(stop) != 1 || stop[0].Hooks[0].Command != "/home/u/.local/bin/reright-hook stop" {
		t.Fatalf("Stop = %+v", stop)
	}
	post := cfg.Hooks["PostToolUse"]
	if len(post) != 1 || post[0].Matcher != "mcp__reright__(rewrite|rewrite_result)" {
		t.Fatalf("PostToolUse = %+v", post)
	}
}
