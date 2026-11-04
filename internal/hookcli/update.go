package hookcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/release"
)

const (
	updateEvery    = 24 * time.Hour
	updateFetchMax = 2 * time.Second
)

type updateState struct {
	CheckedAt   time.Time `json:"checked_at"`
	Latest      string    `json:"latest"`
	NotifiedAt  time.Time `json:"notified_at"`
	NotifiedFor string    `json:"notified_for"`
}

// Policy is what the server last said about this client. The hook records it when the server sends the
// X-Reright-Upgrade and X-Reright-Latest headers on a check.
type Policy struct {
	Upgrade string    `json:"upgrade"` // "required", "available" or ""
	Latest  string    `json:"latest"`
	At      time.Time `json:"at"`
}

func policyPath(dir string) string { return filepath.Join(dir, "client-policy.json") }
func updatePath(dir string) string { return filepath.Join(dir, "update.json") }

// WritePolicy returns a function for HTTPChecker.Policy that saves what the server asked for into dir.
func WritePolicy(dir string) func(upgrade, latest string) {
	return func(upgrade, latest string) {
		if dir == "" {
			return
		}
		b, err := json.Marshal(Policy{Upgrade: upgrade, Latest: latest, At: time.Now()})
		if err != nil {
			return
		}
		if os.MkdirAll(dir, 0o700) == nil {
			os.WriteFile(policyPath(dir), b, 0o600)
		}
	}
}

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, v)
	}
}

func writeJSON(path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		os.WriteFile(path, b, 0o600)
	}
}

func latestRelease(ctx context.Context, env Env) string {
	base := env.ReleaseURL
	if base == "" {
		base = release.DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, updateFetchMax)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/"+release.VersionFile, nil)
	if err != nil {
		return ""
	}
	client := env.HTTP
	if client == nil {
		client = &http.Client{Timeout: updateFetchMax}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if _, ok := release.ParseVersion(v); !ok {
		return ""
	}
	return v
}

// notice is what the prompt hook says about a newer reright: a line for the person and an instruction for
// the agent, which runs the upgrade itself.
type notice struct {
	User  string
	Agent string
}

const agentUpgradeStep = " Run `reright upgrade` now in a separate shell command, then continue your task."

// updateNotice returns the notice when a newer reright exists, or an empty one. The check runs at most once
// a day, the notice is shown at most once a day for each version, and the hook itself installs nothing. A
// server that says the client is too old gets a notice on every prompt until it is upgraded.
// RERIGHT_NO_UPDATE_CHECK=1 turns all of it off, and a local build ("dev") never gets one.
func updateNotice(ctx context.Context, env Env) notice {
	if env.Version == "" || env.Getenv("RERIGHT_NO_UPDATE_CHECK") == "1" {
		return notice{}
	}
	if _, ok := release.ParseVersion(env.Version); !ok {
		return notice{}
	}
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}
	var pol Policy
	readJSON(policyPath(env.StateDir), &pol)
	if pol.Upgrade == "required" {
		msg := "reright " + env.Version + " is too old for this server."
		agent := "Your reright client (" + env.Version + ") is too old for the review server."
		if release.Newer(pol.Latest, env.Version) {
			msg = "reright " + env.Version + " is too old for this server, and " + pol.Latest + " is available."
			agent = "Your reright client (" + env.Version + ") is too old for the review server, and " + pol.Latest + " is available."
		}
		return notice{User: msg + " Run: reright upgrade", Agent: agent + agentUpgradeStep}
	}

	var st updateState
	readJSON(updatePath(env.StateDir), &st)
	if now.Sub(st.CheckedAt) >= updateEvery {
		if v := latestRelease(ctx, env); v != "" {
			st.Latest = v
		}
		st.CheckedAt = now
	}
	latest := st.Latest
	if _, ok := release.ParseVersion(latest); !ok || release.Newer(pol.Latest, latest) {
		if _, ok := release.ParseVersion(pol.Latest); ok {
			latest = pol.Latest
		}
	}
	var n notice
	if release.Newer(latest, env.Version) && (st.NotifiedFor != latest || now.Sub(st.NotifiedAt) >= updateEvery) {
		st.NotifiedFor, st.NotifiedAt = latest, now
		n.User = "reright " + latest + " is available (you have " + env.Version + "). Run: reright upgrade"
		n.Agent = "A new reright client is available (" + latest + ", you have " + env.Version + ")." + agentUpgradeStep
	}
	writeJSON(updatePath(env.StateDir), st)
	return n
}

// claudeNotice shows the person a warning line in Claude Code and gives the agent the upgrade instruction
// as context for the prompt.
func claudeNotice(stdout io.Writer, n notice) {
	out := map[string]any{"systemMessage": n.User}
	if n.Agent != "" {
		out["hookSpecificOutput"] = map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": n.Agent}
	}
	json.NewEncoder(stdout).Encode(out)
}
