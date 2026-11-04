package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stopOut struct {
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	SystemMessage string `json:"systemMessage"`
}

func stopFixture(t *testing.T, now *time.Time) *fixture {
	f := newFixture(t)
	f.env.Now = func() time.Time { return *now }
	return f
}

func (f *fixture) runRaw(t *testing.T, agent, sub string, in any) (string, int) {
	t.Helper()
	var b []byte
	switch v := in.(type) {
	case string:
		b = []byte(v)
	default:
		b, _ = json.Marshal(v)
	}
	var out, errOut bytes.Buffer
	code := RunAgent(context.Background(), agent, sub, bytes.NewReader(b), &out, &errOut, f.env)
	return out.String(), code
}

func (f *fixture) stop(t *testing.T, session string) stopOut {
	t.Helper()
	out, code := f.runRaw(t, AgentClaude, "stop", map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "Stop", "stop_hook_active": false})
	if code != 0 {
		t.Fatalf("stop exit %d", code)
	}
	var s stopOut
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("bad stop output %q: %v", out, err)
		}
	}
	return s
}

func submitResult(session, id string) map[string]any {
	payload, _ := json.Marshal(map[string]string{"id": id, "review_url": "https://app.reright.it/review/" + id, "status": "pending", "instruction": "..."})
	return map[string]any{"session_id": session, "hook_event_name": "PostToolUse", "tool_name": "mcp__reright__submit_for_review",
		"tool_response": []any{map[string]any{"type": "text", "text": string(payload)}}}
}

func waitFor(session, id, status string) map[string]any {
	payload, _ := json.Marshal(map[string]string{"id": id, "status": status, "instruction": "..."})
	return map[string]any{"session_id": session, "hook_event_name": "PostToolUse", "tool_name": "mcp__reright__wait_for_review",
		"tool_response": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}}}
}

func TestStopBlocksWhileADraftIsPending(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	s := f.stop(t, "s1")
	if s.Decision != "block" || !strings.Contains(s.Reason, "(id d1)") || !strings.Contains(s.Reason, "call wait_for_review(id d1)") {
		t.Fatalf("stop = %+v", s)
	}
	f.run(t, "post", waitFor("s1", "d1", "pending"))
	if s := f.stop(t, "s1"); s.Decision != "block" {
		t.Fatalf("a pending wait result cleared the draft: %+v", s)
	}
}

func TestStopAllowsOnceTheDraftIsDecided(t *testing.T) {
	for _, status := range []string{"approved", "rejected", "expired"} {
		now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
		f := stopFixture(t, &now)
		f.run(t, "post", submitResult("s1", "d1"))
		f.run(t, "post", waitFor("s1", "d1", status))
		if s := f.stop(t, "s1"); s != (stopOut{}) {
			t.Errorf("%s: stop = %+v", status, s)
		}
	}
}

func TestStopNamesEveryPendingDraft(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	f.run(t, "post", submitResult("s1", "d2"))
	f.run(t, "post", submitResult("s1", "d3"))
	f.run(t, "post", waitFor("s1", "d2", "approved"))
	s := f.stop(t, "s1")
	if s.Decision != "block" || !strings.Contains(s.Reason, "d1, d3") || strings.Contains(s.Reason, "d2") {
		t.Fatalf("stop = %+v", s)
	}
}

func TestStopGivesUpAfterThirtyMinutes(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	now = now.Add(29 * time.Minute)
	if s := f.stop(t, "s1"); s.Decision != "block" {
		t.Fatalf("at 29 minutes: %+v", s)
	}
	now = now.Add(2 * time.Minute)
	s := f.stop(t, "s1")
	if s.Decision != "" || !strings.Contains(s.SystemMessage, "d1") || !strings.Contains(s.SystemMessage, "https://app.reright.it/review/d1") {
		t.Fatalf("after 30 minutes: %+v", s)
	}
	if s := f.stop(t, "s1"); s != (stopOut{}) {
		t.Fatalf("the note was shown twice: %+v", s)
	}
}

func TestStopIgnoresOtherSessions(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	if s := f.stop(t, "s2"); s != (stopOut{}) {
		t.Fatalf("another session was blocked: %+v", s)
	}
	f.run(t, "post", waitFor("s2", "d1", "approved"))
	if s := f.stop(t, "s1"); s.Decision != "block" {
		t.Fatalf("a wait in another session cleared the draft: %+v", s)
	}
}

func TestStopFailsOpen(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	if s := f.stop(t, "s1"); s != (stopOut{}) {
		t.Fatalf("no state: %+v", s)
	}
	if err := os.MkdirAll(pendingPath(f.env.StateDir, call{Session: "s2"}), 0o700); err != nil {
		t.Fatal(err)
	}
	if s := f.stop(t, "s2"); s != (stopOut{}) {
		t.Fatalf("unreadable state: %+v", s)
	}
	path := pendingPath(f.env.StateDir, call{Session: "s3"})
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("{not json"), 0o600)
	if s := f.stop(t, "s3"); s != (stopOut{}) {
		t.Fatalf("damaged state: %+v", s)
	}
	for _, in := range []string{"not json", "", "[]"} {
		if out, code := f.runRaw(t, AgentClaude, "stop", in); code != 0 || strings.TrimSpace(out) != "" {
			t.Errorf("input %q: code %d, out %q", in, code, out)
		}
	}
	f.env.StateDir = ""
	if out, code := f.runRaw(t, AgentClaude, "stop", map[string]any{"session_id": "s1"}); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("no state dir: code %d, out %q", code, out)
	}
}

func TestStopOnlyBlocksForClaude(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	for _, agent := range []string{AgentCodex, AgentGemini, AgentCopilot, AgentCursor} {
		if out, code := f.runRaw(t, agent, "stop", map[string]any{"session_id": "s1"}); code != 0 || strings.TrimSpace(out) != "" {
			t.Errorf("%s: code %d, out %q", agent, code, out)
		}
	}
}

func TestPostRecordsSubmitFromPluginAndStructuredContent(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", map[string]any{"session_id": "s1", "tool_name": "mcp__plugin_reright_reright__submit_for_review",
		"tool_response": map[string]any{"structuredContent": map[string]any{"id": "p1", "status": "pending", "review_url": "https://x/p1"}}})
	if s := f.stop(t, "s1"); s.Decision != "block" || !strings.Contains(s.Reason, "p1") {
		t.Fatalf("plugin submit not recorded: %+v", s)
	}
	f.run(t, "post", map[string]any{"session_id": "s1", "tool_name": "mcp__plugin_reright_reright__wait_for_review",
		"tool_response": map[string]any{"structuredContent": map[string]any{"id": "p1", "status": "approved", "final_text": "x"}}})
	if s := f.stop(t, "s1"); s != (stopOut{}) {
		t.Fatalf("plugin wait did not clear: %+v", s)
	}
	f.run(t, "post", map[string]any{"session_id": "s1", "tool_name": "mcp__other__submit_for_review",
		"tool_response": map[string]any{"structuredContent": map[string]any{"id": "o1", "status": "pending"}}})
	if s := f.stop(t, "s1"); s != (stopOut{}) {
		t.Fatalf("another server's tool was recorded: %+v", s)
	}
}

func TestRejectedWaitStillLocksAfterSubmit(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "abc"))
	f.run(t, "post", waitResult("s1", "rejected"))
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "deny" {
		t.Fatal("the reject lock is gone")
	}
	if s := f.stop(t, "s1"); s != (stopOut{}) {
		t.Fatalf("a rejected draft kept the agent from stopping: %+v", s)
	}
}

func TestStopUsageErrorsDoNotBlock(t *testing.T) {
	if got := UsageExit("stop"); got == 2 {
		t.Fatal("exit 2 from a Stop hook blocks the agent from stopping")
	}
	if _, sub, err := ParseArgs([]string{"stop"}); err != nil || sub != "stop" {
		t.Fatalf("stop: %q %v", sub, err)
	}
}

func TestHealRunsForClaudeOnlyAndFailsOpen(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.env.Heal = func() { calls++; panic("boom") }
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatalf("decision %q", d)
	}
	if out, code := f.runRaw(t, AgentClaude, "prompt", map[string]any{"session_id": "s1"}); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("prompt: code %d, out %q", code, out)
	}
	if calls != 2 {
		t.Fatalf("heal ran %d times for Claude, want 2", calls)
	}
	f.runRaw(t, AgentCodex, "prompt", map[string]any{"session_id": "s1"})
	if calls != 2 {
		t.Fatal("heal ran for Codex")
	}
}
