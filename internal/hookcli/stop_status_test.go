package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeStatus struct {
	status map[string]string
	err    error
	asked  [][]string
}

func (f *fakeStatus) Statuses(_ context.Context, ids []string) (map[string]string, error) {
	f.asked = append(f.asked, append([]string(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, id := range ids {
		if s, ok := f.status[id]; ok {
			out[id] = s
		} else {
			out[id] = "unknown"
		}
	}
	return out, nil
}

func statusFixture(t *testing.T) (*fixture, *fakeStatus) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	f := stopFixture(t, &now)
	st := &fakeStatus{status: map[string]string{}}
	f.env.Status = st
	return f, st
}

func TestStopResolvesARejectionWithoutAPoll(t *testing.T) {
	f, st := statusFixture(t)
	f.run(t, "post", submitResult("s1", "d1"))
	st.status["d1"] = "rejected"
	s := f.stop(t, "s1")
	if s.Decision == "block" {
		t.Fatalf("the Stop hook asked for a draft the server says is rejected: %+v", s)
	}
	if !strings.Contains(s.SystemMessage, "d1") || !strings.Contains(s.SystemMessage, "rejected") {
		t.Fatalf("the person was not told: %+v", s)
	}
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "deny" {
		t.Fatal("a rejection found at Stop did not lock the agent")
	}
	if d, r := f.run(t, "pre", preResult("s1", "d1")); d != "" {
		t.Fatalf("rewrite_result blocked: %s", r)
	}
	if s := f.stop(t, "s1"); s.Decision == "block" || s.SystemMessage != "" {
		t.Fatalf("the rejection was reported twice: %+v", s)
	}
	// Polling it afterwards does not lock again once the user has answered.
	f.run(t, "prompt", prompt("s1"))
	f.run(t, "post", waitFor("s1", "d1", "rejected"))
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("a rejection the Stop hook already reported locked again")
	}
}

func TestStopResolvesABatchRejectionInOneGo(t *testing.T) {
	f, st := statusFixture(t)
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("d%02d", i)
		f.run(t, "post", submitResult("s1", id))
		st.status[id] = "rejected"
	}
	if s := f.stop(t, "s1"); s.Decision == "block" || !strings.Contains(s.SystemMessage, "more") {
		t.Fatalf("stop after a batch rejection: %+v", s)
	}
	if len(st.asked) != 1 || len(st.asked[0]) != 25 {
		t.Fatalf("server asked %v", st.asked)
	}
	f.run(t, "prompt", prompt("s1"))
	if s := f.stop(t, "s1"); s.Decision == "block" {
		t.Fatalf("rejected drafts came back after the prompt: %+v", s)
	}
}

func TestStopKeepsWaitingOnPendingAndApproved(t *testing.T) {
	f, st := statusFixture(t)
	f.run(t, "post", submitResult("s1", "p1"))
	f.run(t, "post", submitResult("s1", "a1"))
	f.run(t, "post", submitResult("s1", "x1"))
	st.status["p1"] = "pending"
	st.status["a1"] = "approved"
	st.status["x1"] = "expired"
	s := f.stop(t, "s1")
	if s.Decision != "block" || !strings.Contains(s.Reason, "rewrite_result(id p1)") || !strings.Contains(s.Reason, "rewrite_result(id a1)") || strings.Contains(s.Reason, "x1") {
		t.Fatalf("stop = %+v", s)
	}
	if !strings.Contains(s.Reason, "a1 was approved") {
		t.Fatalf("the agent was not told a1 is ready: %s", s.Reason)
	}
	if !strings.Contains(s.SystemMessage, "x1") {
		t.Fatalf("the expired draft was dropped without a word: %+v", s)
	}
}

func TestStopFallsBackWhenTheServerIsDown(t *testing.T) {
	f, st := statusFixture(t)
	st.err = errors.New("offline")
	f.run(t, "post", submitResult("s1", "d1"))
	if s := f.stop(t, "s1"); s.Decision != "block" || !strings.Contains(s.Reason, "d1") {
		t.Fatalf("stop with the server down: %+v", s)
	}
}

func TestSubagentStopAsksOnlyForItsOwnDrafts(t *testing.T) {
	f, st := statusFixture(t)
	f.run(t, "post", submitResult("s1", "mine"))
	f.run(t, "post", withAgent(submitResult("s1", "sub1"), "a1"))
	f.run(t, "post", withAgent(submitResult("s1", "sub2"), "a1"))
	f.run(t, "post", withAgent(submitResult("s1", "other"), "a2"))
	st.status["sub1"] = "rejected"
	st.status["sub2"] = "pending"
	s := f.stopAgent(t, "s1", "a1")
	if s.Decision == "block" {
		t.Fatalf("a subagent that got a rejection was kept going: %+v", s)
	}
	got := append([]string(nil), st.asked[0]...)
	sort.Strings(got)
	if len(st.asked) != 1 || strings.Join(got, ",") != "sub1,sub2" {
		t.Fatalf("subagent stop asked the server about %v", st.asked)
	}
	if d, _ := f.run(t, "pre", tool("s1", "Read")); d != "" {
		t.Fatal("the subagent's rejection locked the parent")
	}
	if s := f.stopAgent(t, "s1", "a2"); s.Decision != "block" || !strings.Contains(s.Reason, "other") {
		t.Fatalf("the other subagent: %+v", s)
	}
}

func TestHTTPStatusesAsksInBatches(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drafts/status" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		paths = append(paths, r.URL.RawQuery)
		var out []map[string]string
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			out = append(out, map[string]string{"id": id, "status": "rejected"})
		}
		json.NewEncoder(w).Encode(map[string]any{"drafts": out})
	}))
	defer srv.Close()
	var ids []string
	for i := 0; i < 60; i++ {
		ids = append(ids, fmt.Sprintf("d%d", i))
	}
	got, err := HTTPChecker{BaseURL: srv.URL, Token: "tok"}.Statuses(context.Background(), ids)
	if err != nil || len(got) != 60 || got["d59"] != "rejected" || len(paths) != 2 {
		t.Fatalf("got %v err %v requests %d", len(got), err, len(paths))
	}
	if _, err := (HTTPChecker{BaseURL: srv.URL, Token: "wrong"}).Statuses(context.Background(), ids[:1]); err == nil {
		t.Fatal("an error answer was taken as a status")
	}
}
