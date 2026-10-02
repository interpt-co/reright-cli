package hookcli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type releaseStub struct {
	srv   *httptest.Server
	hits  int
	value string
}

func newReleaseStub(t *testing.T, value string) *releaseStub {
	t.Helper()
	s := &releaseStub{value: value}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits++
		if r.URL.Path != "/version.txt" || s.value == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(s.value + "\n"))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func noticeEnv(t *testing.T, stub *releaseStub, version string, now *time.Time) Env {
	return Env{
		StateDir:   t.TempDir(),
		Getenv:     func(string) string { return "" },
		Version:    version,
		ReleaseURL: stub.srv.URL,
		Now:        func() time.Time { return *now },
	}
}

func TestUpdateNoticeOncePerDay(t *testing.T) {
	stub := newReleaseStub(t, "v0.1.2")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	env := noticeEnv(t, stub, "v0.1.1", &now)
	ctx := context.Background()

	msg := updateNotice(ctx, env)
	if !strings.Contains(msg, "v0.1.2 is available") || !strings.Contains(msg, "reright upgrade") {
		t.Fatalf("first notice %q", msg)
	}
	if again := updateNotice(ctx, env); again != "" {
		t.Fatalf("the same notice was shown twice in a day: %q", again)
	}
	if stub.hits != 1 {
		t.Fatalf("the release was checked %d times in a day, want 1", stub.hits)
	}
	now = now.Add(25 * time.Hour)
	if next := updateNotice(ctx, env); next == "" || stub.hits != 2 {
		t.Fatalf("a day later: notice %q, checks %d", next, stub.hits)
	}
}

func TestUpdateNoticeStaysQuietWhenCurrentOrUnknown(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct{ latest, version string }{
		"current":     {"v0.1.2", "v0.1.2"},
		"ahead":       {"v0.1.1", "v0.1.2"},
		"dev build":   {"v0.1.2", "dev"},
		"empty":       {"v0.1.2", ""},
		"nothing yet": {"", "v0.1.1"},
		"unreadable":  {"latest", "v0.1.1"},
	} {
		stub := newReleaseStub(t, c.latest)
		if msg := updateNotice(context.Background(), noticeEnv(t, stub, c.version, &now)); msg != "" {
			t.Errorf("%s: unexpected notice %q", name, msg)
		}
	}
	stub := newReleaseStub(t, "v0.1.2")
	env := noticeEnv(t, stub, "v0.1.1", &now)
	env.Getenv = func(k string) string {
		if k == "RERIGHT_NO_UPDATE_CHECK" {
			return "1"
		}
		return ""
	}
	if msg := updateNotice(context.Background(), env); msg != "" || stub.hits != 0 {
		t.Errorf("the opt out was ignored: %q after %d checks", msg, stub.hits)
	}
}

func TestUpdateNoticeWhenTheReleaseIsUnreachable(t *testing.T) {
	stub := newReleaseStub(t, "")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	env := noticeEnv(t, stub, "v0.1.1", &now)
	if msg := updateNotice(context.Background(), env); msg != "" {
		t.Fatalf("notice without a release: %q", msg)
	}
	if updateNotice(context.Background(), env); stub.hits != 1 {
		t.Fatalf("a failed check was retried at once: %d", stub.hits)
	}
}

func TestServerPolicyRequiresAnUpgradeOnEveryPrompt(t *testing.T) {
	stub := newReleaseStub(t, "v0.1.2")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	env := noticeEnv(t, stub, "v0.1.1", &now)
	WritePolicy(env.StateDir)("required", "v0.1.3")
	for i := 0; i < 2; i++ {
		msg := updateNotice(context.Background(), env)
		if !strings.Contains(msg, "too old for this server") || !strings.Contains(msg, "v0.1.3 is available") {
			t.Fatalf("prompt %d: %q", i, msg)
		}
	}
	if stub.hits != 0 {
		t.Error("a required upgrade should not need the release check")
	}
}

func TestServerPolicyAvailableFeedsTheNotice(t *testing.T) {
	stub := newReleaseStub(t, "")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	env := noticeEnv(t, stub, "v0.1.1", &now)
	WritePolicy(env.StateDir)("available", "v0.1.4")
	if msg := updateNotice(context.Background(), env); !strings.Contains(msg, "v0.1.4 is available") {
		t.Fatalf("notice %q", msg)
	}
}

func TestCheckerSendsItsVersionAndRecordsThePolicy(t *testing.T) {
	var gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClient = r.Header.Get("X-Reright-Client")
		w.Header().Set("X-Reright-Upgrade", "required")
		w.Header().Set("X-Reright-Latest", "v0.1.2")
		w.Write([]byte(`{"approved":false}`))
	}))
	defer srv.Close()
	var up, latest string
	c := HTTPChecker{BaseURL: srv.URL, Token: "t", Version: "v0.1.1", Policy: func(u, l string) { up, latest = u, l }}
	if _, err := c.Approved(context.Background(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if gotClient != "reright-hook/v0.1.1" || up != "required" || latest != "v0.1.2" {
		t.Fatalf("client %q, policy %q %q", gotClient, up, latest)
	}
}

func TestPromptShowsTheNoticeToClaudeOnly(t *testing.T) {
	stub := newReleaseStub(t, "v0.1.2")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	env := noticeEnv(t, stub, "v0.1.1", &now)
	var out, errOut bytes.Buffer
	in := `{"session_id":"s","cwd":"/","hook_event_name":"UserPromptSubmit","prompt":"hi"}`
	if code := RunAgent(context.Background(), AgentClaude, "prompt", strings.NewReader(in), &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"systemMessage"`) || !strings.Contains(out.String(), "reright upgrade") {
		t.Fatalf("claude output %q", out.String())
	}
	out.Reset()
	env.StateDir = t.TempDir()
	if code := RunAgent(context.Background(), AgentGemini, "prompt", strings.NewReader(in), &out, &errOut, env); code != 0 || out.Len() != 0 {
		t.Fatalf("gemini got output %q (exit %d)", out.String(), code)
	}
}
