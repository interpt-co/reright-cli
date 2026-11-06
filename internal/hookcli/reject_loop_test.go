package hookcli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These tests replay the reject loop of 2026-11-06: a batch of drafts was rejected, the Stop hook kept
// asking for their results, and the reject lock blocked every call that could fetch them.

func withAgent(in map[string]any, agentID string) map[string]any {
	if agentID != "" {
		in["agent_id"] = agentID
		in["agent_type"] = "general-purpose"
	}
	return in
}

func preResult(session, id string) map[string]any {
	return map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "PreToolUse",
		"tool_name": "mcp__reright__rewrite_result", "tool_input": map[string]any{"id": id}}
}

func preTool(session, name string, input map[string]any) map[string]any {
	return map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "PreToolUse", "tool_name": name, "tool_input": input}
}

func prompt(session string) map[string]any {
	return map[string]any{"session_id": session, "hook_event_name": "UserPromptSubmit", "prompt": "next"}
}

func (f *fixture) stopAgent(t *testing.T, session, agentID string) stopOut {
	t.Helper()
	in := map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "Stop", "stop_hook_active": false}
	if agentID != "" {
		in = withAgent(in, agentID)
		in["hook_event_name"] = "SubagentStop"
	}
	out, code := f.runRaw(t, AgentClaude, "stop", in)
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

func TestRejectLockLetsTheAgentFinishUp(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	for _, id := range []string{"d1", "d2", "d3"} {
		f.run(t, "post", submitResult("s1", id))
	}
	f.run(t, "post", waitFor("s1", "d1", "rejected"))

	if d, r := f.run(t, "pre", preResult("s1", "d2")); d != "" {
		t.Fatalf("the lock blocked rewrite_result for another id: %s", r)
	}
	if d, r := f.run(t, "pre", preResult("s1", "d1")); d != "" {
		t.Fatalf("the lock blocked checking the rejected id again: %s", r)
	}
	if d, r := f.run(t, "pre", preTool("s1", "SendMessage", map[string]any{"to": "agent-2", "message": "d1 was rejected"})); d != "" {
		t.Fatalf("the lock blocked a message to another agent: %s", r)
	}
	if d, r := f.run(t, "pre", preTool("s1", "AskUserQuestion", map[string]any{"questions": []any{}})); d != "" {
		t.Fatalf("the lock blocked a question to the user: %s", r)
	}
	for _, name := range []string{"Bash", "Read", "mcp__reright__rewrite", "mcp__reright__keep_original"} {
		d, r := f.run(t, "pre", preTool("s1", name, map[string]any{"command": "ls", "text": "same text again", "id": "d1"}))
		if d != "deny" || !strings.Contains(r, "d1") {
			t.Errorf("%s under a reject lock: decision %q reason %q", name, d, r)
		}
	}
	if s := f.stop(t, "s1"); s.Decision == "block" {
		t.Fatalf("the Stop hook kept a locked agent going: %+v", s)
	}
}

func TestReCheckingAResolvedRejectionDoesNotLockAgain(t *testing.T) {
	f := newFixture(t)
	f.run(t, "post", waitFor("s1", "d1", "rejected"))
	f.run(t, "prompt", prompt("s1"))
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("the lock survived the user's prompt")
	}
	f.run(t, "post", waitFor("s1", "d1", "rejected"))
	if d, r := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatalf("checking a rejection the agent already reported locked it again: %s", r)
	}
	// The same rejection seen through a subagent of the session does not lock it either.
	f.run(t, "post", withAgent(waitFor("s1", "d1", "rejected"), "a1"))
	if d, r := f.run(t, "pre", withAgent(tool("s1", "Read"), "a1")); d != "" {
		t.Fatalf("a rejection already reported in the session locked a subagent: %s", r)
	}
	// A new rejection still locks.
	f.run(t, "post", waitFor("s1", "d2", "rejected"))
	if d, r := f.run(t, "pre", tool("s1", "Read")); d != "deny" || !strings.Contains(r, "d2") {
		t.Fatalf("a new rejection did not lock: %q %s", d, r)
	}
}

func TestBatchRejectionMakesOneLockAndDrainsPending(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	var ids []string
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("d%02d", i)
		ids = append(ids, id)
		f.run(t, "post", submitResult("s1", id))
	}
	for _, id := range ids {
		if d, r := f.run(t, "pre", preResult("s1", id)); d != "" {
			t.Fatalf("rewrite_result(%s) blocked: %s", id, r)
		}
		f.run(t, "post", waitFor("s1", id, "rejected"))
	}
	d, r := f.run(t, "pre", tool("s1", "Read"))
	if d != "deny" || !strings.Contains(r, "d01") || !strings.Contains(r, "more") || strings.Contains(r, "d25") {
		t.Fatalf("lock reason after 25 rejections: %q %s", d, r)
	}
	f.run(t, "prompt", prompt("s1"))
	if s := f.stop(t, "s1"); s.Decision == "block" {
		t.Fatalf("rejected drafts are still pending: %+v", s)
	}
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("the lock survived the prompt")
	}
}

func TestStopListsOnlyTheCallersDrafts(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "mine"))
	f.run(t, "post", withAgent(submitResult("s1", "sub1"), "a1"))
	f.run(t, "post", withAgent(submitResult("s1", "sub2"), "a2"))

	s := f.stop(t, "s1")
	if s.Decision != "block" || !strings.Contains(s.Reason, "mine") || strings.Contains(s.Reason, "sub") {
		t.Fatalf("parent stop listed other agents' drafts: %+v", s)
	}
	s = f.stopAgent(t, "s1", "a1")
	if s.Decision != "block" || !strings.Contains(s.Reason, "sub1") || strings.Contains(s.Reason, "sub2") || strings.Contains(s.Reason, "mine") {
		t.Fatalf("subagent stop: %+v", s)
	}
	f.run(t, "post", withAgent(waitFor("s1", "sub1", "approved"), "a1"))
	if s := f.stopAgent(t, "s1", "a1"); s.Decision == "block" {
		t.Fatalf("subagent still blocked after its draft was approved: %+v", s)
	}
}

func TestOneAgentsRejectionDoesNotLockTheOthers(t *testing.T) {
	f := newFixture(t)
	f.run(t, "post", withAgent(waitFor("s1", "x1", "rejected"), "a1"))
	if d, _ := f.run(t, "pre", withAgent(tool("s1", "Read"), "a1")); d != "deny" {
		t.Fatal("the subagent that saw the rejection is not locked")
	}
	if d, r := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatalf("a subagent's rejection locked the parent: %s", r)
	}
	if d, r := f.run(t, "pre", withAgent(tool("s1", "Read"), "a2")); d != "" {
		t.Fatalf("a subagent's rejection locked another subagent: %s", r)
	}
	f.run(t, "prompt", prompt("s1"))
	if d, _ := f.run(t, "pre", withAgent(tool("s1", "Read"), "a1")); d != "" {
		t.Fatal("the user's prompt did not clear the subagent's lock")
	}
}

func TestStopDropsDraftsSeenRejectedElsewhere(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	f.run(t, "post", submitResult("s1", "d1"))
	f.run(t, "post", submitResult("s1", "d2"))
	// The parent's d1 rejection reaches the session through another agent.
	f.run(t, "post", withAgent(waitFor("s1", "d1", "rejected"), "a1"))
	s := f.stop(t, "s1")
	if s.Decision != "block" || strings.Contains(s.Reason, "d1") || !strings.Contains(s.Reason, "d2") {
		t.Fatalf("stop after d1 was rejected: %+v", s)
	}
}
