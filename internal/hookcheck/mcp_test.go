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
		{"js insertText single", "mcp__claude-in-chrome__javascript_tool", `{"action":"javascript_exec","tabId":1,"text":"document.execCommand('insertText', false, 'Line one\\n\\nLine two')"}`, []string{"Line one\n\nLine two"}, false},
		{"js insertText double", "mcp__claude-in-chrome__javascript_tool", `{"text":"el.focus(); document.execCommand(\"insertText\", false, \"Caf\\u00e9 \\\"ok\\\"\")"}`, []string{`Café "ok"`}, false},
		{"js template literal", "mcp__claude-in-chrome__javascript_tool", "{\"text\":\"document.execCommand('insertText', false, `Multi\\nline`)\"}", []string{"Multi\nline"}, false},
		{"js template interpolation", "mcp__claude-in-chrome__javascript_tool", "{\"text\":\"document.execCommand('insertText', false, `Hi ${name}`)\"}", nil, true},
		{"js variable", "mcp__claude-in-chrome__javascript_tool", `{"text":"const t = 'x'; document.execCommand('insertText', false, t)"}`, nil, true},
		{"js input event", "mcp__claude-in-chrome__javascript_tool", `{"text":"el.dispatchEvent(new InputEvent('beforeinput', {inputType: 'insertText', data: 'x'}))"}`, nil, true},
		{"js concatenation after literal", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.execCommand('insertText', false, 'Approved' + extra)"}`, nil, true},
		{"js value assignment long", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.querySelector('textarea').value = 'one two three four five six seven eight nine ten eleven twelve'"}`, []string{"one two three four five six seven eight nine ten eleven twelve"}, false},
		{"js value assignment short", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.querySelector('#search').value = 'invoice 42'"}`, nil, false},
		{"js value assignment variable", "mcp__claude-in-chrome__javascript_tool", `{"text":"el.innerHTML = note"}`, nil, true},
		{"js value comparison", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.querySelector('input').value === ''"}`, nil, false},
		{"js message_post", "mcp__claude-in-chrome__javascript_tool", `{"text":"fetch('/web/dataset/call_kw', {method:'POST', body: JSON.stringify({params:{model:'project.task', method:'message_post'}})})"}`, nil, true},
		{"js fetch post", "mcp__claude-in-chrome__javascript_tool", `{"text":"fetch('/x',{method:'POST',body:'a note for someone'})"}`, nil, true},
		{"js fetch put", "mcp__claude-in-chrome__javascript_tool", `{"text":"await fetch(url, {method: \"PUT\", headers: h})"}`, nil, true},
		{"js xhr post", "mcp__claude-in-chrome__javascript_tool", `{"text":"var x = new XMLHttpRequest(); x.open('POST', '/x'); x.send('hi')"}`, nil, true},
		{"js beacon", "mcp__claude-in-chrome__javascript_tool", `{"text":"navigator.sendBeacon('/x', 'hi')"}`, nil, true},
		{"js fetch get", "mcp__claude-in-chrome__javascript_tool", `{"text":"fetch('/x').then(r => r.json())"}`, nil, false},
		{"js unrelated", "mcp__claude-in-chrome__javascript_tool", `{"text":"document.title"}`, nil, false},
		{"type short", "mcp__claude-in-chrome__computer", `{"action":"type","text":"search term"}`, nil, false},
		{"type long", "mcp__claude-in-chrome__computer", `{"action":"type","text":"one two three four five six seven eight nine ten eleven twelve"}`, []string{"one two three four five six seven eight nine ten eleven twelve"}, false},
		{"type newline", "mcp__claude-in-chrome__computer", `{"action":"type","text":"Hi\nthere"}`, []string{"Hi\nthere"}, false},
		{"click has no text", "mcp__claude-in-chrome__computer", `{"action":"left_click","coordinate":[1,2]}`, nil, false},
		{"form input long", "mcp__claude-in-chrome__form_input", `{"ref":"r1","value":"a b c d e f g h i j k l m"}`, []string{"a b c d e f g h i j k l m"}, false},
		{"form input bool", "mcp__claude-in-chrome__form_input", `{"ref":"r1","value":true}`, nil, false},
		{"batch nested", "mcp__claude-in-chrome__browser_batch", `{"actions":[{"tool":"javascript_tool","input":{"text":"document.execCommand('insertText', false, 'Nested note')"}}]}`, []string{"Nested note"}, false},
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

func TestParseJSString(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`'plain'`, "plain", true},
		{`"tab\there"`, "tab\there", true},
		{`'😀'`, "😀", true},
		{`'\u{1F600}'`, "😀", true},
		{`'\x41'`, "A", true},
		{"'a\\\nb'", "ab", true},
		{`'it\'s'`, "it's", true},
		{`'\q'`, "q", true},
		{`'unterminated`, "", false},
		{"'raw\nnewline'", "", false},
		{`x`, "", false},
	}
	for _, c := range cases {
		got, _, ok := parseJSString(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseJSString(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
