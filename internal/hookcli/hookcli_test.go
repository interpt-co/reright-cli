package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/draft"
)

type fakeChecker struct {
	approved map[string]bool
	err      error
	calls    int
}

func (f *fakeChecker) Approved(_ context.Context, sha string) (bool, error) {
	f.calls++
	return f.approved[sha], f.err
}

type fixture struct {
	env     Env
	checker *fakeChecker
	vars    map[string]string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{checker: &fakeChecker{approved: map[string]bool{}}, vars: map[string]string{}}
	f.env = Env{StateDir: t.TempDir(), Checker: f.checker, Getenv: func(k string) string { return f.vars[k] }}
	return f
}

// run feeds one hook event and returns the permission decision ("" means allowed silently) and reason.
func (f *fixture) run(t *testing.T, sub string, in map[string]any) (string, string) {
	t.Helper()
	b, _ := json.Marshal(in)
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), sub, bytes.NewReader(b), &out, &errOut, f.env); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if out.Len() == 0 {
		return "", ""
	}
	var dec struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &dec); err != nil {
		t.Fatalf("bad hook output %q: %v", out.String(), err)
	}
	if dec.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName = %q", dec.HookSpecificOutput.HookEventName)
	}
	return dec.HookSpecificOutput.PermissionDecision, dec.HookSpecificOutput.PermissionDecisionReason
}

func bash(session, cmd string) map[string]any {
	return map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
}

func tool(session, name string) map[string]any {
	return map[string]any{"session_id": session, "cwd": "/tmp", "hook_event_name": "PreToolUse", "tool_name": name, "tool_input": map[string]any{"file_path": "/x"}}
}

func waitResult(session, status string) map[string]any {
	payload, _ := json.Marshal(map[string]string{"id": "abc", "status": status, "reason": "too long", "instruction": "..."})
	return map[string]any{"session_id": session, "hook_event_name": "PostToolUse", "tool_name": "mcp__reright__wait_for_review",
		"tool_response": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}}}
}

func TestPreAllowsUnrelatedTool(t *testing.T) {
	f := newFixture(t)
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatalf("decision %q", d)
	}
	if f.checker.calls != 0 {
		t.Fatal("an unrelated tool triggered a server check")
	}
}

func TestPreDeniesUnapprovedCommit(t *testing.T) {
	f := newFixture(t)
	d, reason := f.run(t, "pre", bash("s1", `git commit -m "Hi"`))
	if d != "deny" || !strings.Contains(reason, "submit_for_review") {
		t.Fatalf("decision %q reason %q", d, reason)
	}
}

func TestPreAllowsApprovedCommit(t *testing.T) {
	f := newFixture(t)
	f.checker.approved[draft.Hash("Hi")] = true
	if d, r := f.run(t, "pre", bash("s1", `git commit -m "Hi"`)); d != "" {
		t.Fatalf("decision %q reason %q", d, r)
	}
}

func TestPreAllowsApprovedHeredocCommit(t *testing.T) {
	f := newFixture(t)
	f.checker.approved[draft.Hash("Don't break\n\nBody line.")] = true
	cmd := "git commit -m \"$(cat <<'EOF'\nDon't break\n\nBody line.\nEOF\n)\""
	if d, r := f.run(t, "pre", bash("s1", cmd)); d != "" {
		t.Fatalf("decision %q reason %q", d, r)
	}
}

func TestPreDeniesWhenOnePartUnapproved(t *testing.T) {
	f := newFixture(t)
	f.checker.approved[draft.Hash("New title")] = true
	if d, _ := f.run(t, "pre", bash("s1", `gh pr edit 1 --title "New title" --body "Sneaky body"`)); d != "deny" {
		t.Fatalf("decision %q", d)
	}
}

func TestPreDeniesProblem(t *testing.T) {
	f := newFixture(t)
	d, reason := f.run(t, "pre", bash("s1", `git commit`))
	if d != "deny" || !strings.Contains(reason, "editor") {
		t.Fatalf("decision %q reason %q", d, reason)
	}
}

func TestPreServerDown(t *testing.T) {
	f := newFixture(t)
	f.checker.err = errors.New("connection refused")
	d, reason := f.run(t, "pre", bash("s1", `git commit -m "Hi"`))
	if d != "deny" || !strings.Contains(reason, "connection refused") {
		t.Fatalf("decision %q reason %q", d, reason)
	}
	f.vars["RERIGHT_OFFLINE"] = "allow"
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "Hi"`)); d != "" {
		t.Fatalf("offline allow ignored: %q", d)
	}
}

func TestBypass(t *testing.T) {
	f := newFixture(t)
	f.vars["RERIGHT_BYPASS"] = "1"
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "Hi"`)); d != "" {
		t.Fatalf("bypass ignored: %q", d)
	}
}

func TestRejectLockFlow(t *testing.T) {
	f := newFixture(t)
	if d, _ := f.run(t, "post", waitResult("s1", "rejected")); d != "" {
		t.Fatalf("post produced a decision %q", d)
	}
	d, reason := f.run(t, "pre", tool("s1", "Read"))
	if d != "deny" || !strings.Contains(reason, "abc") || !strings.Contains(reason, "too long") {
		t.Fatalf("locked session: decision %q reason %q", d, reason)
	}
	if d, _ := f.run(t, "pre", tool("s2", "Read")); d != "" {
		t.Fatal("the lock leaked into another session")
	}
	f.vars["RERIGHT_BYPASS"] = "1"
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "deny" {
		t.Fatal("RERIGHT_BYPASS lifted a reject lock")
	}
	f.run(t, "prompt", map[string]any{"session_id": "s1", "hook_event_name": "UserPromptSubmit", "prompt": "ok, do X instead"})
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("the lock survived a user prompt")
	}
}

func TestPostIgnoresApproved(t *testing.T) {
	f := newFixture(t)
	f.run(t, "post", waitResult("s1", "approved"))
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("an approved result locked the session")
	}
}

func TestPostStructuredContent(t *testing.T) {
	f := newFixture(t)
	in := map[string]any{"session_id": "s1", "tool_name": "mcp__reright__wait_for_review",
		"tool_response": map[string]any{"structuredContent": map[string]any{"id": "x9", "status": "rejected"}}}
	f.run(t, "post", in)
	if d, reason := f.run(t, "pre", tool("s1", "Read")); d != "deny" || !strings.Contains(reason, "x9") {
		t.Fatalf("decision %q reason %q", d, reason)
	}
}

func TestSessionIDSanitized(t *testing.T) {
	f := newFixture(t)
	f.run(t, "post", waitResult("../../escape", "rejected"))
	entries, err := os.ReadDir(filepath.Join(f.env.StateDir, "locks"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("locks dir: %v %v", entries, err)
	}
	if strings.ContainsAny(entries[0].Name(), "/.") && entries[0].Name() != filepath.Base(entries[0].Name()) {
		t.Fatalf("unsafe lock name %q", entries[0].Name())
	}
	if _, err := os.Stat(filepath.Join(f.env.StateDir, "..", "..", "escape")); err == nil {
		t.Fatal("lock written outside the state dir")
	}
	if d, _ := f.run(t, "pre", tool("../../escape", "Read")); d != "deny" {
		t.Fatal("sanitized lock not found again for the same session")
	}
}

func TestPreBadInput(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	Run(context.Background(), "pre", strings.NewReader("not json"), &out, &bytes.Buffer{}, f.env)
	if !strings.Contains(out.String(), `"deny"`) {
		t.Fatalf("bad input not denied: %s", out.String())
	}
}

func TestHTTPChecker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"approved": r.URL.Query().Get("sha") == "good"})
	}))
	defer srv.Close()
	c := HTTPChecker{BaseURL: srv.URL + "/", Token: "tok"}
	if ok, err := c.Approved(context.Background(), "good"); !ok || err != nil {
		t.Fatalf("good: %v %v", ok, err)
	}
	if ok, err := c.Approved(context.Background(), "bad"); ok || err != nil {
		t.Fatalf("bad: %v %v", ok, err)
	}
	if _, err := (HTTPChecker{BaseURL: srv.URL, Token: "wrong"}).Approved(context.Background(), "good"); err == nil {
		t.Fatal("401 should be an error")
	}
	if _, err := (HTTPChecker{BaseURL: srv.URL}).Approved(context.Background(), "good"); err == nil {
		t.Fatal("a missing token should be an error")
	}
}

func TestLockReadErrorDenies(t *testing.T) {
	f := newFixture(t)
	// A directory where the lock file should be cannot be read as a lock.
	if err := os.MkdirAll(lockPath(f.env.StateDir, call{Session: "s1"}), 0o700); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "deny" {
		t.Fatal("an unreadable lock must deny, not allow")
	}
}
