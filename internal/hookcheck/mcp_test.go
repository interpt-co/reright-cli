package hookcheck

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestInspect(t *testing.T) {
	cases := []struct {
		name, tool, input string
		want              []string
		problem           bool
	}{
		{"read tool", "Read", `{"file_path":"/x"}`, nil, false},
		{"bash commit", "Bash", `{"command":"git commit -m \"Hi\""}`, []string{"Hi"}, false},
		{"bash bad json", "Bash", `not json`, nil, false},
		{"gmail send", "mcp__claude_ai_Gmail__send_message", `{"to":["a@b.c"],"subject":"Subj","body":"Hello"}`, []string{"Subj\n\nHello"}, false},
		{"gmail send body only", "mcp__claude_ai_Gmail__send_message", `{"to":["a@b.c"],"body":"Hello"}`, []string{"Hello"}, false},
		{"gmail send draft", "mcp__claude_ai_Gmail__send_message", `{"draftId":"d1"}`, nil, false},
		{"gmail html only", "mcp__claude_ai_Gmail__send_message", `{"subject":"S","htmlBody":"<p>x</p>"}`, nil, true},
		{"gmail html and body", "mcp__claude_ai_Gmail__create_draft", `{"subject":"S","body":"x","htmlBody":"<p>x</p>"}`, nil, true},
		{"gmail reply", "mcp__claude_ai_Gmail__reply", `{"messageId":"m","body":"Thanks"}`, []string{"Thanks"}, false},
		{"gmail forward", "mcp__claude_ai_Gmail__forward", `{"messageId":"m","forwardText":"FYI"}`, []string{"FYI"}, false},
		{"gmail forward no text", "mcp__claude_ai_Gmail__forward", `{"messageId":"m"}`, nil, false},
		{"gmail update subject", "mcp__claude_ai_Gmail__update_draft", `{"draftId":"d","subject":"New subject"}`, []string{"New subject"}, false},
		{"gmail search", "mcp__claude_ai_Gmail__search_threads", `{"query":"from:x"}`, nil, false},
		{"gmail server name in another case", "mcp__Claude_AI_GMAIL__send_message", `{"subject":"S","body":"B"}`, []string{"S\n\nB"}, false},
		{"gmail plugin send", "mcp__plugin_gmail_gmail__send_message", `{"subject":"S","body":"B"}`, []string{"S\n\nB"}, false},
		{"browser javascript is let through", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.execCommand('insertText', false, 'Line one')"}`, nil, false},
		{"browser typing is let through", "mcp__claude-in-chrome__computer", `{"action":"type","text":"one two three four five six seven eight nine ten eleven twelve"}`, nil, false},
		{"other mcp send_message is let through", "mcp__slack__send_message", `{"text":"hi"}`, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Inspect(c.tool, json.RawMessage(c.input), t.TempDir())
			if c.problem != (res.Problem != "") {
				t.Fatalf("problem = %q, want problem: %v (texts %q)", res.Problem, c.problem, values(res))
			}
			if !c.problem && !slices.Equal(values(res), c.want) {
				t.Fatalf("texts = %q, want %q", values(res), c.want)
			}
		})
	}
}
