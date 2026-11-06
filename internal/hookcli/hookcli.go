// Package hookcli implements the hook subcommands of reright-hook for Claude Code.
package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/audience"
	"github.com/interpt-co/reright-cli/internal/draft"
	"github.com/interpt-co/reright-cli/internal/hookcheck"
	"github.com/interpt-co/reright-cli/internal/toggle"
)

// Checker asks the reright server whether a text hash was approved.
type Checker interface {
	Approved(ctx context.Context, sha string) (bool, error)
}

// StatusChecker asks the reright server for the status of drafts by id. An id the account does not own
// comes back as "unknown".
type StatusChecker interface {
	Statuses(ctx context.Context, ids []string) (map[string]string, error)
}

type Env struct {
	StateDir string
	Checker  Checker
	Getenv   func(string) string
	// ConfigDir holds enforcement.json, the user's on/off switches. Empty means no switches.
	ConfigDir string
	// Now is the clock for timed switches and update notices. Nil means time.Now.
	Now func() time.Time
	// Version is the version of this hook. The update notice is off for "dev" and for an empty value.
	Version string
	// ReleaseURL is where version.txt is fetched for the update notice. Empty means the official releases.
	ReleaseURL string
	// HTTP is the client for that fetch. Nil means a client with a short timeout.
	HTTP *http.Client
	// Status asks the server where drafts stand, so the Stop check can resolve them without the agent.
	// Nil means the Stop check knows only what the agent fetched.
	Status StatusChecker
	// Audience decides whether other people read the commit messages of the repository at dir. Commit, tag
	// and merge messages are checked only where it says shared. Nil means every repository is shared.
	Audience func(ctx context.Context, dir string) audience.Decision
	// Heal brings the Claude Code hook settings up to date after an upgrade. Nil means no healing. It runs
	// after the decision is written, and its errors and panics are ignored.
	Heal func()
}

// Run executes one hook subcommand for Claude Code: pre (PreToolUse), post
// (PostToolUse on rewrite and rewrite_result), prompt (UserPromptSubmit)
// or stop (Stop). Decisions go
// to stdout as hook JSON; the exit code is 0 unless the subcommand is unknown.
func Run(ctx context.Context, sub string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	return RunAgent(ctx, AgentClaude, sub, stdin, stdout, stderr, env)
}

// RunAgent is Run with the agent named by --agent. Claude Code is the only one.
func RunAgent(ctx context.Context, agent, sub string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	if legacyAgents[agent] {
		fmt.Fprintf(stderr, "reright-hook: reright no longer supports %s, so this call was let through. Run reright uninstall --agent %s to remove the old hook config.\n", agent, agent)
		return 0
	}
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
		if decodeErr == nil && (isRewriteResult(c.Tool) || isRewrite(c.Tool)) {
			post(c, stderr, env)
			if err := recordWait(c, env); err != nil {
				fmt.Fprintln(stderr, "reright-hook:", err)
			}
		}
		if decodeErr == nil && isRewrite(c.Tool) {
			if err := recordSubmit(c, env); err != nil {
				fmt.Fprintln(stderr, "reright-hook:", err)
			}
		}
	case "stop":
		if ad.stop != nil {
			block, note := stopCheck(ctx, c, decodeErr, env)
			ad.stop(stdout, block, note)
		}
	case "prompt":
		if decodeErr == nil {
			clearLocks(env.StateDir, c)
		}
		if ad.notice != nil {
			if n := updateNotice(ctx, env); n.User != "" {
				ad.notice(stdout, n)
			}
		}
	default:
		fmt.Fprintf(stderr, "reright-hook: unknown subcommand %q (want pre, post, prompt or stop)\n", sub)
		return UsageExit("")
	}
	if env.Heal != nil {
		heal(env.Heal)
	}
	return 0
}

func heal(f func()) {
	defer func() { recover() }()
	f()
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
	if !lockExempt(in.Tool) {
		b, err := os.ReadFile(lockPath(env.StateDir, in))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "reright: could not read this agent's reject lock (" + err.Error() + "), so the call is blocked. Tell the user."
		}
		if err == nil {
			var l lock
			json.Unmarshal(b, &l)
			return lockMessage(l)
		}
	}
	if reason := guardSwitches(in); reason != "" {
		return reason
	}
	if env.Getenv("RERIGHT_BYPASS") == "1" {
		return ""
	}
	off := switchedOff(env, in.Cwd)
	decided := map[string]audience.Decision{}
	own := func(dir string) bool {
		if env.Audience == nil {
			return false
		}
		d, ok := decided[dir]
		if !ok {
			d = env.Audience(ctx, dir)
			decided[dir] = d
		}
		return !d.Shared
	}
	res := hookcheck.InspectWith(in.Tool, in.Input, in.Cwd, hookcheck.Options{OwnRepo: own})
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
		// The rule asks agents to send only free text through reright, so a version, a URL, a name or code goes
		// out as it is. A title and body come as one text and are judged together.
		if !draft.IsFreeText(t.Value) {
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
			why := ""
			if d, seen := decided[t.Dir]; seen && t.Dir != "" {
				why = " reright checks commit messages in this repository because " + d.Why + ". If nobody besides the user reads them, the user can run `git config reright.commits never` in it; do not run it yourself."
			}
			return fmt.Sprintf("reright: the %s did not go through reright. Call the reright tool rewrite with this exact text and its context, wait for it to finish, then run this again using final_text exactly as returned.%s", t.Source, why)
		}
	}
	return ""
}

// switchedOff returns a lookup for the user's on/off switches. A missing file means everything is on.
// A damaged file also means everything is on: when in doubt the hook checks.
func switchedOff(env Env, cwd string) func(check string) bool {
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
	return func(check string) bool { return f.Off(check, cwd, t) }
}

// waived reports whether every check a problem call might touch is switched off.
func waived(off func(string) bool, checks []string) bool {
	every := true
	for _, k := range toggle.Checks {
		every = every && off(k)
	}
	if every {
		return true // "reright disable all" is the master switch, for calls no single check describes
	}
	for _, k := range checks {
		if k == "" || !off(k) {
			return false
		}
	}
	return len(checks) > 0
}

// switchHint tells the agent what to pass on to the user when a call it cannot fix is blocked.
func switchHint(checks []string) string {
	var named []string
	for _, k := range checks {
		if k != "" {
			named = append(named, k)
		}
	}
	if len(named) == 0 || len(named) < len(checks) {
		named = []string{"all"}
	}
	return " If this block is wrong, only the user can lift it: ask them to run `reright disable " + strings.Join(named, " ") + " --for 1h` in their own terminal (`reright enable " + strings.Join(named, " ") + "` turns it back on). Do not try to run it yourself."
}

// guardSwitches stops an agent from changing the switches. Only the user turns checks off,
// from their own terminal or with the agent's shell-escape prefix, neither of which reaches this hook.
func guardSwitches(in call) string {
	if in.Tool != "Bash" {
		return ""
	}
	var b struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.Input, &b) != nil || !hookcheck.SwitchCommand(b.Command) {
		return ""
	}
	return "reright: only the user can turn reright's checks on or off. Do not run `reright disable` or `reright enable`, do not set the reright git config keys (reright.commits, reright.email), and do not edit enforcement.json. Tell the user what you need and ask them to run it in their own terminal."
}

func post(in call, stderr io.Writer, env Env) {
	id, reason, found := findRejection(in.Response)
	if !found {
		return
	}
	// A rejection the session has already seen was already reported, so seeing it again locks nothing.
	if !markRejected(env.StateDir, in, id, env.now()) {
		return
	}
	if err := addToLock(env.StateDir, in, id, reason); err != nil {
		fmt.Fprintln(stderr, "reright-hook:", err)
	}
}

func findRejection(raw json.RawMessage) (id, reason string, found bool) {
	r, ok := findResult(raw, func(m map[string]any) bool { return m["status"] == "rejected" })
	return r.ID, r.Reason, ok
}

// isRewriteResult matches rewrite_result, the tool that waits on an id.
func isRewriteResult(tool string) bool { return isRerightTool(tool, rewriteResultTool) }

// isRewrite matches the rewrite tool, which submits and waits in one call, so its result can be
// pending (track it), approved or rejected (lock).
func isRewrite(tool string) bool { return isRerightTool(tool, rewriteTool) }

func isRerightTool(tool, want string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(tool), "mcp__")
	if !ok {
		return false
	}
	server, name, ok := strings.Cut(rest, "__")
	return ok && name == want && (server == "reright" || (strings.HasPrefix(server, "plugin_") && strings.HasSuffix(server, "_reright")))
}

const (
	rewriteTool       = "rewrite"
	rewriteResultTool = "rewrite_result"
)
