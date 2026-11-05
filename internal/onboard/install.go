package onboard

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/githook"
	"github.com/interpt-co/reright-cli/internal/release"
)

const (
	DefaultServer   = "https://app.reright.it"
	DefaultTestWait = 10 * time.Minute
	hookBinary      = "reright-hook"
)

type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{ Home string }

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "HOME="+r.Home)
	return cmd.CombinedOutput()
}

type Options struct {
	Home       string
	Server     string
	Code       string
	ReleaseURL string
	PublicKey  ed25519.PublicKey
	GOOS       string
	GOARCH     string
	HookPath   string
	DeviceName string
	Runner     Runner
	HTTP       *http.Client
	Out        io.Writer
	SkipTest   bool
	TestWait   time.Duration

	// Agents limits install to these agents. Only claude is accepted.
	Agents []string
	// AllSupported writes Claude's config even when Claude Code was not found.
	AllSupported bool
	// NoGit is accepted for scripts written for earlier versions. There is no git layer to skip.
	NoGit  bool
	DryRun bool
	Yes    bool
	Force  bool
}

var ErrTestNotApproved = errors.New("the test draft was not approved")

func (o *Options) defaults() {
	p := Paths{o.Home}
	if o.Server == "" {
		o.Server = DefaultServer
	}
	if o.ReleaseURL == "" {
		o.ReleaseURL = release.DefaultBaseURL
	}
	if o.HookPath == "" {
		o.HookPath = p.DefaultHook()
	}
	if abs, err := filepath.Abs(o.HookPath); err == nil {
		o.HookPath = abs
	}
	if o.DeviceName == "" {
		o.DeviceName, _ = os.Hostname()
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{Home: o.Home}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.TestWait == 0 {
		o.TestWait = DefaultTestWait
	}
}

func step(out io.Writer, n int, format string, args ...any) {
	fmt.Fprintf(out, "[%d/6] %s\n", n, fmt.Sprintf(format, args...))
}

const placeholderToken = "rrd_placeholder"

type selection struct {
	Agent string
	How   string
	Skip  string
	Plan  agentPlan
}

func (s selection) selected() bool { return s.Skip == "" }

func selectAgents(o Options, env planEnv) ([]selection, error) {
	requested, err := normalizeAgents(o.Agents, false)
	if err != nil {
		return nil, err
	}
	candidates := agentOrder
	if len(requested) > 0 {
		candidates = requested
	}
	var sels []selection
	for _, name := range candidates {
		how, found := detectAgent(name, o.Runner, o.Home)
		s := selection{Agent: name, How: how}
		if !found && !o.AllSupported {
			s.Skip = "not installed (no " + strings.Join(agentSpecs[name].Binaries, " or ") + " on PATH, no ~/" + agentSpecs[name].Dirs[0] + "). Use --all-supported to write its config anyway"
		}
		sels = append(sels, s)
	}
	var notFound, failed []string
	for i := range sels {
		s := &sels[i]
		if !s.selected() {
			notFound = append(notFound, s.Agent)
			continue
		}
		plan, err := buildPlan(s.Agent, env)
		if err != nil {
			return nil, err
		}
		var keep []fileEdit
		for _, e := range plan.Edits {
			cur, _, rerr := readOptional(e.Path)
			if rerr == nil {
				_, rerr = e.apply(cur)
			}
			if rerr == nil {
				_, rerr = planWrite(e.Path, o.Force)
			}
			if rerr == nil {
				keep = append(keep, e)
				continue
			}
			if e.Optional {
				plan.Partial = append(plan.Partial, fmt.Sprintf("%s was not changed: %v. Add the %s server by hand with the URL %s and an Authorization header that carries your device token (~/.config/reright/token)", e.Path, rerr, mcpName, mcpURL(env.Server)))
				continue
			}
			s.Skip = fmt.Sprintf("failed: %s: %v", e.Path, rerr)
			break
		}
		plan.Edits = keep
		s.Plan = plan
		if !s.selected() {
			if len(requested) > 0 {
				return nil, fmt.Errorf("%s. Nothing was changed and your setup code has not been used", s.Skip[len("failed: "):])
			}
			failed = append(failed, s.Agent)
		}
	}
	if !anySelected(sels) {
		switch {
		case len(failed) > 0:
			return nil, fmt.Errorf("no agent could be configured (%s). Nothing was changed and your setup code has not been used", strings.Join(failed, ", "))
		case len(requested) > 0:
			return nil, fmt.Errorf("%s not found on this machine, so nothing can be set up. Install the agent first or pass --all-supported. Your setup code has not been used", strings.Join(notFound, ", "))
		}
		return nil, errors.New("Claude Code was not found (no claude on PATH, no ~/.claude). reright sets up Claude Code only. Install it first, or pass --all-supported to write its config anyway. Your setup code has not been used")
	}
	return sels, nil
}

func anySelected(sels []selection) bool {
	for _, s := range sels {
		if s.selected() {
			return true
		}
	}
	return false
}

type agentResult struct {
	Agent   string
	Done    []string
	NotDone []string
	Skip    string
}

func (r agentResult) status() string {
	switch {
	case r.Skip != "":
		return "not configured: " + r.Skip
	case len(r.NotDone) > 0:
		return "partly configured"
	}
	return "configured"
}

func printSummary(out io.Writer, results []agentResult, git []string) {
	fmt.Fprintln(out, "\nSummary")
	for _, r := range results {
		fmt.Fprintf(out, "  %-8s %s\n", r.Agent, r.status())
		for _, d := range r.Done {
			fmt.Fprintf(out, "           done: %s\n", d)
		}
		for _, d := range r.NotDone {
			fmt.Fprintf(out, "           not done: %s\n", d)
		}
	}
	for i, g := range git {
		label := ""
		if i == 0 {
			label = "git"
		}
		fmt.Fprintf(out, "  %-8s %s\n", label, g)
	}
}

func describeEdit(e fileEdit) string {
	var what []string
	for _, p := range e.Parts {
		switch p.Kind {
		case "hooks":
			what = append(what, "hooks (enforced)")
		case "mcp":
			what = append(what, "MCP server")
		case "rules":
			what = append(what, "rules text")
		}
	}
	return strings.Join(what, " and ") + " in " + e.Path
}

func resultFor(s selection) agentResult {
	r := agentResult{Agent: s.Agent, Skip: s.Skip}
	if s.Skip != "" {
		return r
	}
	r.NotDone = append(r.NotDone, s.Plan.Partial...)
	return r
}

var ErrAgentsSkipped = errors.New("some detected agents were skipped")

type SkippedError struct{ Skipped []string }

func (e *SkippedError) Error() string {
	return "reright is installed, but " + fmt.Sprint(len(e.Skipped)) + " detected agent(s) were skipped because their config could not be edited: " + strings.Join(e.Skipped, "; ") +
		". Fix those files, then run reright install --agent NAME again with a new setup code (or add the entries by hand)"
}

func (e *SkippedError) Is(target error) bool { return target == ErrAgentsSkipped }

func damagedManifest(p Paths, err error) error {
	return fmt.Errorf("%s cannot be used: %w. Your setup code has not been used. Move that file away (it only lists what install changed) or run reright uninstall --force, then run install again", p.ManifestFile(), err)
}

func preflightDirs(o Options, p Paths) error {
	for _, d := range []struct{ what, path string }{{"the hook binary", o.HookPath}, {"reright's config", p.TokenFile()}} {
		if err := dirWritable(nearestDir(d.path)); err != nil {
			return fmt.Errorf("cannot write %s in %s: %v. Nothing was changed and your setup code has not been used", d.what, nearestDir(d.path), err)
		}
	}
	if fi, err := os.Lstat(o.HookPath); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory, not a file. Nothing was changed and your setup code has not been used", o.HookPath)
	}
	return nil
}

func Install(ctx context.Context, o Options) error {
	o.defaults()
	p := Paths{o.Home}
	server, err := checkServerURL(o.Server)
	if err != nil {
		return err
	}
	if !o.DryRun {
		if err := checkCode(o.Code); err != nil {
			return err
		}
		if len(o.PublicKey) != ed25519.PublicKeySize {
			return errors.New("this build of reright has no release key compiled in, so it cannot verify downloads. Download the official reright binary again")
		}
		if !release.Supported(o.GOOS, o.GOARCH) {
			return fmt.Errorf("no reright-hook build for %s/%s (Linux and macOS on amd64 and arm64 only)", o.GOOS, o.GOARCH)
		}
	}
	if _, _, err := loadManifest(p); err != nil {
		return damagedManifest(p, err)
	}
	env := planEnv{Home: o.Home, HookPath: o.HookPath, Server: server, Token: placeholderToken}
	sels, err := selectAgents(o, env)
	if err != nil {
		return err
	}
	if o.DryRun {
		return dryRun(o, sels)
	}
	if err := preflightDirs(o, p); err != nil {
		return err
	}

	asset := release.AssetName(hookBinary, o.GOOS, o.GOARCH)
	step(o.Out, 1, "Downloading %s and checking its signed checksum", asset)
	hook, err := fetchVerified(ctx, o.HTTP, o.ReleaseURL, o.PublicKey, asset)
	if err != nil {
		return fmt.Errorf("%w. Your setup code has not been used", err)
	}

	step(o.Out, 2, "Exchanging the setup code for a device token")
	ex, err := exchange(ctx, o.HTTP, server, o.Code, o.DeviceName)
	if err != nil {
		return err
	}

	results, gitLines, err := applyInstall(ctx, o, p, ex, hook, sels)
	if err != nil {
		return err
	}
	printSummary(o.Out, results, gitLines)

	var skipped []string
	for _, s := range sels {
		if strings.HasPrefix(s.Skip, "failed: ") {
			skipped = append(skipped, s.Agent+" ("+strings.TrimPrefix(s.Skip, "failed: ")+")")
		}
	}
	finish := func() error {
		if len(skipped) > 0 {
			return &SkippedError{Skipped: skipped}
		}
		return nil
	}

	if o.SkipTest {
		fmt.Fprintln(o.Out, "\nSkipped the test draft. Run reright doctor to check the setup.")
		return finish()
	}
	step(o.Out, 6, "Sending a test draft. If reright asks, approve it in your browser")
	res, err := submitTestDraft(ctx, ex.Server, ex.Token, o.TestWait, func(u string) {
		fmt.Fprintf(o.Out, "Review link: %s\n", u)
	})
	if err != nil {
		return fmt.Errorf("installed, but the test draft failed: %w. Run reright doctor", err)
	}
	if res.Status != "approved" {
		return fmt.Errorf("%w (status %s). The hooks and MCP server are installed; approve the draft at %s and run reright doctor", ErrTestNotApproved, res.Status, res.URL)
	}
	fmt.Fprintln(o.Out, "Test draft approved. reright is set up. Start a new session in each configured agent to use it.")
	return finish()
}

func applyInstall(ctx context.Context, o Options, p Paths, ex exchanged, hook []byte, sels []selection) ([]agentResult, []string, error) {
	unlock, err := lockConfig(p)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (the setup code is spent; generate a new one)", err)
	}
	defer unlock()

	m, _, err := loadManifest(p)
	if err != nil {
		return nil, nil, fmt.Errorf("%s changed while install ran: %w (the setup code is spent; generate a new one)", p.ManifestFile(), err)
	}
	m.Server, m.HookPath = ex.Server, o.HookPath

	step(o.Out, 3, "Installing the hook binary at %s", o.HookPath)
	if err := writeFile(o.HookPath, hook, 0o755); err != nil {
		return nil, nil, fmt.Errorf("install hook: %w (the setup code is spent; generate a new one)", err)
	}
	if err := writePrivate(p.TokenFile(), []byte(ex.Token+"\n")); err != nil {
		return nil, nil, err
	}
	if err := writePrivate(p.URLFile(), []byte(ex.Server+"\n")); err != nil {
		return nil, nil, err
	}
	if err := saveManifest(p, m); err != nil {
		return nil, nil, err
	}

	step(o.Out, 4, "Configuring agents (user-level files only)")
	env := planEnv{Home: o.Home, HookPath: o.HookPath, Server: ex.Server, Token: ex.Token}
	var results []agentResult
	for _, s := range sels {
		r := resultFor(s)
		if !s.selected() {
			results = append(results, r)
			continue
		}
		plan, err := buildPlan(s.Agent, env)
		if err != nil {
			return nil, nil, err
		}
		plan.Partial = s.Plan.Partial
		var kept []fileEdit
		for _, e := range plan.Edits {
			if containsPath(s.Plan.Edits, e.Path) {
				kept = append(kept, e)
			}
		}
		plan.Edits = kept
		rec := m.record(s.Agent)
		for _, e := range plan.Edits {
			if err := writeEdit(p, s.Agent, &m, rec, e, o.Force); err != nil {
				return nil, nil, fmt.Errorf("%s: %w. Run reright uninstall to undo the steps that worked", s.Agent, err)
			}
			r.Done = append(r.Done, describeEdit(e))
		}
		if plan.ClaudeMCP {
			if err := registerClaude(ctx, o, ex, rec, &m, p, &r); err != nil {
				return nil, nil, err
			}
		}
		results = append(results, r)
	}

	step(o.Out, 5, "Checking for git hooks from an earlier version")
	gitLines := removeGitLayer(o.Home)
	m.Git = ""
	if err := saveManifest(p, m); err != nil {
		return nil, nil, err
	}
	return results, gitLines, nil
}

func containsPath(edits []fileEdit, path string) bool {
	for _, e := range edits {
		if e.Path == path {
			return true
		}
	}
	return false
}

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return line
}

func registerClaude(ctx context.Context, o Options, ex exchanged, rec *AgentRecord, m *Manifest, p Paths, r *agentResult) error {
	claude, err := o.Runner.LookPath("claude")
	if err != nil {
		r.NotDone = append(r.NotDone, "MCP server: claude is not on PATH. Install Claude Code and run reright install again")
		return nil
	}
	addArgs := func(name string) []string {
		return []string{"mcp", "add", "--scope", "user", "--transport", "http", name, ex.Server + "/mcp", "--header", "Authorization: Bearer " + ex.Token}
	}
	current, getErr := o.Runner.Run(ctx, claude, "mcp", "get", mcpName)
	existed := getErr == nil && strings.TrimSpace(string(current)) != ""
	if existed {
		probe := mcpName + "-check-" + sha([]byte(ex.Token))[:6]
		if out, err := o.Runner.Run(ctx, claude, addArgs(probe)...); err != nil {
			return fmt.Errorf("claude mcp add failed: %w: %s. The MCP server named %s that claude already has was left as it was. Run reright uninstall to undo the steps that worked, then try again with a new setup code", err, out, mcpName)
		}
		o.Runner.Run(ctx, claude, "mcp", "remove", "--scope", "user", probe)
		o.Runner.Run(ctx, claude, "mcp", "remove", "--scope", "user", mcpName)
	}
	rec.MCPCLI = true
	if out, err := o.Runner.Run(ctx, claude, addArgs(mcpName)...); err != nil {
		saveManifest(p, *m)
		gone := ""
		if existed {
			gone = " The MCP server named " + mcpName + " that claude had before (" + firstLine(current) + ") was removed first, so add it back by hand if it was yours."
		}
		return fmt.Errorf("claude mcp add failed: %w: %s.%s Run reright uninstall to undo the steps that worked, then try again with a new setup code", err, out, gone)
	}
	if existed {
		r.Done = append(r.Done, "an MCP server named "+mcpName+" already existed in claude ("+firstLine(current)+"). It was replaced with reright's")
	}
	r.Done = append(r.Done, "MCP server registered with claude mcp add (user scope). claude has no config-file route reright can use, so the token was on that command line briefly")
	return nil
}

// removeGitLayer takes away the git hooks reright 0.1 installed, if they are there, and says what it did.
// It never fails: a layer that cannot be removed is reported, and doctor keeps warning about it.
func removeGitLayer(home string) []string {
	present := githook.Present(home)
	notes, err := githook.Remove(home)
	if !present && len(notes) == 0 && err == nil {
		return nil
	}
	if err != nil {
		return append(notes, "the git hooks of an earlier version could not be removed: "+err.Error()+". Run reright upgrade again, or remove ~/.config/reright/git-hooks and unset core.hooksPath by hand")
	}
	return append([]string{"removed the git hooks of an earlier version. Commit messages are checked by the Claude Code hook"}, notes...)
}

func dryRun(o Options, sels []selection) error {
	fmt.Fprintln(o.Out, "Dry run. Nothing was downloaded, written or changed.")
	fmt.Fprintf(o.Out, "Would install the hook binary at %s and keep the device token in %s (mode 600).\n", o.HookPath, Paths{o.Home}.TokenFile())
	var results []agentResult
	for _, s := range sels {
		r := resultFor(s)
		if s.selected() {
			for _, e := range s.Plan.Edits {
				r.Done = append(r.Done, "would write "+describeEdit(e))
			}
			if s.Plan.ClaudeMCP {
				r.Done = append(r.Done, "would run: claude mcp add --scope user --transport http reright "+mcpURL(o.Server)+" --header \"Authorization: Bearer <token>\"")
			}
		}
		results = append(results, r)
	}
	var git []string
	if githook.Present(o.Home) {
		git = []string{"would remove the git hooks an earlier version installed in ~/.config/reright/git-hooks and put core.hooksPath back"}
	}
	printSummary(o.Out, results, git)
	return nil
}

func uniqueStrings(in ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range in {
		for _, v := range l {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func writeEdit(p Paths, agent string, m *Manifest, rec *AgentRecord, e fileEdit, force bool) error {
	plan, err := planWrite(e.Path, force)
	if err != nil {
		return err
	}
	cur, existed, err := readOptional(e.Path)
	if err != nil {
		return err
	}
	next, err := e.apply(cur)
	if err != nil {
		return fmt.Errorf("%s: %w", e.Path, err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(e.Path); err == nil {
		mode = info.Mode().Perm()
	}
	origMode := mode
	if e.Private {
		mode = 0o600
	}
	backupPath := filepath.Join(p.BackupDir(), agent, sha([]byte(e.Path))[:8]+"-"+filepath.Base(e.Path))
	fr := rec.file(e.Path)
	if fr == nil {
		nf := FileRecord{Path: e.Path, Existed: existed}
		if existed {
			nf.Mode = uint32(origMode)
			nf.Backup = backupPath
			if err := writePrivate(nf.Backup, cur); err != nil {
				return err
			}
		}
		rec.Files = append(rec.Files, nf)
		fr = rec.file(e.Path)
	} else if edited := fr.AfterSHA == "" || !existed || sha(cur) != fr.AfterSHA; edited {
		if !existed {
			if fr.Backup != "" {
				os.Remove(fr.Backup)
			}
			fr.Existed, fr.Backup, fr.Mode = false, "", 0
		} else {
			stripped, err := stripAll(uniqueStrings(fr.Tags, e.tags()), cur)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Path, err)
			}
			if fr.Existed || !effectivelyEmpty(stripped) {
				fr.Existed, fr.Mode, fr.Backup = true, uint32(origMode), backupPath
				if err := writePrivate(fr.Backup, stripped); err != nil {
					return err
				}
			}
		}
	}
	if plan.Link != "" && fr.Link == "" {
		fr.Link = plan.Link
	}
	fr.Kinds, fr.Tags = e.kinds(), e.tags()
	fr.AfterSHA = ""
	if err := saveManifest(p, *m); err != nil {
		return err
	}
	if err := writeFile(plan.Dest, next, mode); err != nil {
		return err
	}
	fr.AfterSHA = sha(next)
	return saveManifest(p, *m)
}
