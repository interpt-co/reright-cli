// Package hookcli implements the hook subcommands of reright-hook for Claude Code and other agents.
package hookcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/draft"
	"github.com/interpt-co/reright-cli/internal/hookcheck"
	"github.com/interpt-co/reright-cli/internal/toggle"
)

// Checker asks the reright server whether a text hash was approved.
type Checker interface {
	Approved(ctx context.Context, sha string) (bool, error)
}

type Env struct {
	StateDir string
	Checker  Checker
	Getenv   func(string) string
	// ConfigDir holds enforcement.json, the user's on/off switches. Empty means no switches.
	ConfigDir string
	// Now is the clock for timed switches. Nil means time.Now.
	Now func() time.Time
}

type lock struct {
	DraftID string `json:"draft_id"`
	Reason  string `json:"reason"`
}

// Run executes one hook subcommand for Claude Code: pre (PreToolUse), post
// (PostToolUse on wait_for_review) or prompt (UserPromptSubmit). Decisions go
// to stdout as hook JSON; the exit code is 0 unless the subcommand is unknown.
func Run(ctx context.Context, sub string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	return RunAgent(ctx, AgentClaude, sub, stdin, stdout, stderr, env)
}

// RunAgent is Run for any supported agent. The payload is read in that agent's
// shape and decisions are written in that agent's deny format.
func RunAgent(ctx context.Context, agent, sub string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	ad, ok := adapters[agent]
	if !ok {
		fmt.Fprintf(stderr, "reright-hook: unknown agent %q (want %s)\n", agent, strings.Join(Agents(), ", "))
		return UsageExit(sub)
	}
	var c call
	var decodeErr error
	raw, err := io.ReadAll(stdin)
	if err != nil {
		decodeErr = err
	} else {
		c, decodeErr = ad.decode(raw)
	}
	switch sub {
	case "pre":
		if reason := preReason(ctx, c, decodeErr, env); reason != "" {
			return ad.deny(stdout, stderr, c, reason)
		}
	case "post":
		if decodeErr == nil && isWaitForReview(c.Tool) {
			post(c, stderr, env)
		}
	case "prompt":
		if decodeErr == nil {
			os.Remove(lockPath(env.StateDir, c))
		}
		if ad.promptOK != nil {
			ad.promptOK(stdout)
		}
	default:
		fmt.Fprintf(stderr, "reright-hook: unknown subcommand %q (want pre, post or prompt)\n", sub)
		return UsageExit("")
	}
	return 0
}

func preReason(ctx context.Context, c call, decodeErr error, env Env) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			reason = fmt.Sprintf("reright-hook failed while checking the call (%v), so the call is blocked.", r)
		}
	}()
	if decodeErr != nil {
		return "reright-hook could not read the hook input (" + decodeErr.Error() + "), so the call is blocked."
	}
	return pre(ctx, c, env)
}

func pre(ctx context.Context, in call, env Env) string {
	b, err := os.ReadFile(lockPath(env.StateDir, in))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "reright: could not read this session's reject lock (" + err.Error() + "), so the call is blocked. Tell the user."
	}
	if err == nil {
		var l lock
		json.Unmarshal(b, &l)
		reason := ""
		if l.Reason != "" {
			reason = " (reason: " + l.Reason + ")"
		}
		return fmt.Sprintf("reright: the reviewer rejected draft %s%s. Stop the current task. Do not retry, rephrase or work around it. Tell the user the text was rejected and wait for his instructions.", l.DraftID, reason)
	}
	if reason := guardSwitches(in); reason != "" {
		return reason
	}
	if env.Getenv("RERIGHT_BYPASS") == "1" {
		return ""
	}
	off := switchedOff(env, in.Cwd)
	res := hookcheck.Inspect(in.Tool, in.Input, in.Cwd)
	if res.Problem != "" && !waived(off, hookcheck.ProblemKinds(in.Tool, in.Input)) {
		return "reright: " + res.Problem + " Nothing was sent." + switchHint(hookcheck.ProblemKinds(in.Tool, in.Input))
	}
	for _, t := range res.Texts {
		if strings.TrimSpace(t.Value) == "" {
			continue
		}
		if k := t.Kind(); k != "" && off(k) {
			continue
		}
		ok, err := env.Checker.Approved(ctx, draft.Hash(t.Value))
		if err != nil {
			if env.Getenv("RERIGHT_OFFLINE") == "allow" {
				return ""
			}
			return fmt.Sprintf("reright: could not reach the review server to check the %s (%v). Nothing was sent. Tell the user; they can set RERIGHT_OFFLINE=allow to work offline.", t.Source, err)
		}
		if !ok {
			return fmt.Sprintf("reright: the %s was not approved. Call the reright tool submit_for_review with this exact text and its context, wait with wait_for_review until it is approved, then run this again using final_text exactly as returned.", t.Source)
		}
	}
	return ""
}

// switchedOff returns a lookup for the user's on/off switches. A missing file means everything is on.
// A damaged file also means everything is on: when in doubt the hook checks.
func switchedOff(env Env, cwd string) func(kind string) bool {
	if env.ConfigDir == "" {
		return func(string) bool { return false }
	}
	f, err := toggle.Load(env.ConfigDir)
	if err != nil {
		return func(string) bool { return false }
	}
	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	t := now()
	return func(kind string) bool { return f.Off(kind, cwd, t) }
}

// waived reports whether every kind a problem call might touch is switched off.
func waived(off func(string) bool, kinds []string) bool {
	every := true
	for _, k := range toggle.Kinds {
		every = every && off(k)
	}
	if every {
		return true // "reright disable all" is the master switch, for calls no single kind describes
	}
	for _, k := range kinds {
		if k == "" || !off(k) {
			return false
		}
	}
	return len(kinds) > 0
}

// switchHint tells the agent what to pass on to the user when a call it cannot fix is blocked.
func switchHint(kinds []string) string {
	var named []string
	for _, k := range kinds {
		if k != "" {
			named = append(named, k)
		}
	}
	if len(named) == 0 || len(named) < len(kinds) {
		named = []string{"all"}
	}
	return " If this block is wrong, only the user can lift it: ask them to run `reright disable " + strings.Join(named, " ") + " --for 1h` in their own terminal (`reright enable " + strings.Join(named, " ") + "` turns it back on). Do not try to run it yourself."
}

var switchCommand = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])reright(\s+-\S+)*\s+(disable|enable)([^A-Za-z0-9_-]|$)|enforcement\.json`)

// guardSwitches stops an agent from changing the switches. Only the user turns checks off,
// from their own terminal or with the agent's shell-escape prefix, neither of which reaches this hook.
func guardSwitches(in call) string {
	if in.Tool != "Bash" {
		return ""
	}
	var b struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.Input, &b) != nil || !switchCommand.MatchString(b.Command) {
		return ""
	}
	return "reright: only the user can turn reright's checks on or off. Do not run `reright disable` or `reright enable`, and do not edit enforcement.json. Tell the user what you need and ask them to run it in their own terminal."
}

func post(in call, stderr io.Writer, env Env) {
	id, reason, found := findRejection(in.Response)
	if !found {
		return
	}
	path := lockPath(env.StateDir, in)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintln(stderr, "reright-hook:", err)
		return
	}
	b, _ := json.Marshal(lock{DraftID: id, Reason: reason})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fmt.Fprintln(stderr, "reright-hook:", err)
	}
}

// findRejection looks through a tool response for a wait_for_review result
// with status "rejected". MCP results arrive either as structured content or
// as JSON inside a text block, so both shapes are searched.
func findRejection(raw json.RawMessage) (id, reason string, found bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", "", false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			if t["status"] == "rejected" {
				id, _ = t["id"].(string)
				reason, _ = t["reason"].(string)
				return true
			}
			for _, c := range t {
				if walk(c) {
					return true
				}
			}
		case []any:
			for _, c := range t {
				if walk(c) {
					return true
				}
			}
		case string:
			if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					return walk(inner)
				}
			}
		}
		return false
	}
	found = walk(v)
	return id, reason, found
}

func isWaitForReview(tool string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(tool), "mcp__")
	if !ok {
		return false
	}
	server, name, ok := strings.Cut(rest, "__")
	return ok && name == waitForReviewTool && (server == "reright" || (strings.HasPrefix(server, "plugin_") && strings.HasSuffix(server, "_reright")))
}

const waitForReviewTool = "wait_for_review"

// lockPath names the lock file after a hash of the session id, so any id
// length is safe. Without a session id the lock is per working directory, or
// shared by all id-less sessions when there is no directory either. That can
// block an unrelated id-less session after a reject, never the other way
// round, and the next prompt clears it.
func lockPath(stateDir string, c call) string {
	key := "session:" + c.Session
	if c.Session == "" {
		key = "cwd:" + c.Cwd
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(stateDir, "locks", hex.EncodeToString(sum[:16]))
}
