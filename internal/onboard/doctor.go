package onboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/interpt-co/reright-cli/internal/toggle"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/githook"
)

type DoctorOptions struct {
	Home   string
	Runner Runner
	HTTP   *http.Client
	Out    io.Writer
	Agents []string
	Dir    string
	// Version is the version of the running reright. The doctor compares it with the installed reright-hook.
	Version string
}

type Check struct {
	Name   string
	OK     bool
	Warn   bool
	Detail string
	Fix    string
}

const reinstallHint = "generate a new setup code on the dashboard and run reright install again"

const probeInput = `{"session_id":"reright-doctor","cwd":"/","tool_name":"Read","tool_input":{"file_path":"/dev/null"}}`

const denyProbeInput = `{"session_id":"reright-doctor","cwd":"/","tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`

type doctor struct {
	o      DoctorOptions
	checks []Check
}

func (d *doctor) emit(c Check) bool {
	d.checks = append(d.checks, c)
	label := "ok   "
	switch {
	case c.Warn:
		label = "warn "
	case !c.OK:
		label = "FAIL "
	}
	line := fmt.Sprintf("%s %s: %s", label, c.Name, c.Detail)
	if c.Fix != "" && (!c.OK || c.Warn) {
		line += " Fix: " + c.Fix
	}
	fmt.Fprintln(d.o.Out, line)
	return c.OK
}

type fixedError struct {
	err error
	fix string
}

func (e fixedError) Error() string { return e.err.Error() }
func (e fixedError) Unwrap() error { return e.err }

func withFix(fix, format string, args ...any) error {
	return fixedError{fmt.Errorf(format, args...), fix}
}

func (d *doctor) add(name string, err error, okDetail string) bool {
	c := Check{Name: name, OK: err == nil, Detail: okDetail}
	if err != nil {
		c.Detail = err.Error()
		var fe fixedError
		if errors.As(err, &fe) {
			c.Fix = fe.fix
		}
	}
	return d.emit(c)
}

func (d *doctor) addFix(name string, err error, okDetail, fix string) bool {
	c := Check{Name: name, OK: err == nil, Detail: okDetail}
	if err != nil {
		c.Detail, c.Fix = err.Error(), fix
	}
	return d.emit(c)
}

func (d *doctor) warn(name, detail, fix string) {
	d.emit(Check{Name: name, OK: true, Warn: true, Detail: detail, Fix: fix})
}

func Doctor(ctx context.Context, o DoctorOptions) []Check {
	p := Paths{o.Home}
	if o.Runner == nil {
		o.Runner = ExecRunner{Home: o.Home}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	d := &doctor{o: o}

	tok, server := readSecret(p.TokenFile()), readSecret(p.URLFile())
	configOK := d.add("config", configProblem(p, tok, server), fmt.Sprintf("token and server URL found for %s", server))
	if configOK {
		d.add("server", checkServer(ctx, o.HTTP, server, tok), "the server accepts this device token")
		d.add("mcp", checkMCP(ctx, server, tok), "the MCP endpoint lists submit_for_review and wait_for_review")
	}

	m, hadManifest, _ := loadManifest(p)
	hook := m.HookPath
	if hook == "" {
		hook = p.DefaultHook()
	}
	d.add("hook binary", checkHookBinary(ctx, hook, o.Home), "runs and lets a harmless call through: "+hook)
	d.clientVersions(ctx, hook)

	agents, err := doctorAgents(o, m, hadManifest)
	if err != nil {
		d.add("agents", err, "")
	}
	env := planEnv{Home: o.Home, HookPath: hook, Server: server, Token: tok}
	for _, a := range agents {
		d.agent(ctx, a, env)
	}

	d.skippedAgents(agents, env)
	d.git(m, hadManifest)
	d.environment()
	d.projectFiles(agents)
	return d.checks
}

func doctorAgents(o DoctorOptions, m Manifest, had bool) ([]string, error) {
	requested, err := normalizeAgents(o.Agents)
	if err != nil {
		return nil, err
	}
	if len(requested) > 0 {
		return requested, nil
	}
	if had && len(m.Agents) > 0 {
		return m.agentNames(), nil
	}
	var out []string
	for _, n := range agentOrder {
		if _, ok := detectAgent(n, o.Runner, o.Home); ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []string{agentClaude}
	}
	return out, nil
}

func (d *doctor) agent(ctx context.Context, name string, env planEnv) {
	plan, err := buildPlan(name, env)
	if err != nil {
		d.add(name, err, "")
		return
	}
	fixHint := "run reright install --agent " + name + " with a new setup code"
	for _, kind := range []string{"hooks", "mcp", "rules"} {
		label := map[string]string{"hooks": " hooks", "mcp": " mcp entry", "rules": " rules"}[kind]
		if kind == "mcp" && plan.ClaudeMCP {
			d.add(name+label, checkRegistered(ctx, d.o.Runner), "claude knows the reright MCP server")
			continue
		}
		edits := partsOfKind(plan, kind)
		if kind == "rules" && len(edits) == 0 && len(plan.Manual) > 0 {
			d.warn(name+label, "cannot be checked because the rules live in the agent's settings", "make sure the text from reright install --dry-run is pasted there")
			continue
		}
		var problems, where []string
		for _, e := range edits {
			raw, exists, err := readOptional(e.Path)
			switch {
			case err != nil:
				problems = append(problems, err.Error())
			case !exists && e.Optional:
				continue
			case !exists:
				problems = append(problems, e.Path+" does not exist")
			default:
				if err := e.Parts[0].Check(raw); err != nil {
					problems = append(problems, e.Path+": "+err.Error())
				}
			}
			where = append(where, e.Path)
		}
		if kind == "hooks" {
			if why := hooksSwitchedOff(name, d.o.Home); why != "" {
				problems = append(problems, why)
			}
			if name == agentClaude && len(problems) == 0 {
				if cmd := configuredPreCommand(d.o.Home); cmd != "" {
					if err := checkConfiguredPreCommand(ctx, cmd); err != nil {
						problems = append(problems, err.Error())
					}
				}
			}
		}
		var cerr error
		if len(problems) > 0 {
			cerr = errors.New(strings.Join(problems, "; "))
		}
		detail := "found in " + strings.Join(where, ", ")
		if kind == "hooks" {
			detail = "all hook commands present with --agent " + name + " in " + strings.Join(where, ", ")
			if name == agentClaude {
				detail = "all three hooks point at " + env.HookPath
			}
		}
		d.addFix(name+label, cerr, detail, fixHint)
	}
	if name == agentCodex {
		raw, _, _ := readOptional(filepath.Join(d.o.Home, ".codex", "config.toml"))
		var err error
		if codexHooksDisabled(raw) {
			err = errors.New("~/.codex/config.toml turns hooks off under [features], so Codex ignores the reright hooks")
		}
		d.addFix("codex hooks enabled", err, "hooks are not turned off in ~/.codex/config.toml", "remove the hooks = false line under [features]")
		if _, err := os.Stat(filepath.Join(d.o.Home, ".codex", "AGENTS.override.md")); err == nil {
			d.warn("codex rules", "~/.codex/AGENTS.override.md exists, and Codex reads it instead of AGENTS.md, so the reright rules may not load", "add the rule block to AGENTS.override.md or remove that file")
		}
		if v := os.Getenv("CODEX_HOME"); v != "" {
			d.warn("codex home", "CODEX_HOME is set to "+v+", but reright writes to ~/.codex", "copy the reright entries into that directory or unset CODEX_HOME")
		}
	}
	if name == agentCopilot {
		if v := os.Getenv("COPILOT_HOME"); v != "" {
			d.warn("copilot home", "COPILOT_HOME is set to "+v+", but reright writes to ~/.copilot", "copy the reright entries into that directory or unset COPILOT_HOME")
		}
	}
	for _, u := range agentSpecs[name].Unverified {
		d.warn(name+" unverified", u, "")
	}
}

func (d *doctor) skippedAgents(configured []string, env planEnv) {
	if len(d.o.Agents) > 0 {
		return
	}
	have := map[string]bool{}
	for _, a := range configured {
		have[a] = true
	}
	for _, name := range agentOrder {
		if have[name] {
			continue
		}
		if _, found := detectAgent(name, d.o.Runner, d.o.Home); !found {
			continue
		}
		plan, err := buildPlan(name, env)
		if err != nil {
			continue
		}
		for _, e := range plan.Edits {
			cur, _, rerr := readOptional(e.Path)
			if rerr == nil {
				_, rerr = e.apply(cur)
			}
			if rerr != nil && !e.Optional {
				d.emit(Check{Name: name + " config", Detail: fmt.Sprintf("%s was detected, but %s cannot be edited (%v), so install skipped it and reright is not set up for it", name, e.Path, rerr), Fix: "fix that file, then run reright install --agent " + name + " with a new setup code"})
				break
			}
		}
	}
}

func hooksSwitchedOff(agent, home string) string {
	var path string
	switch agent {
	case agentClaude:
		path = filepath.Join(home, ".claude", "settings.json")
	case agentGemini:
		path = filepath.Join(home, ".gemini", "settings.json")
	default:
		return ""
	}
	raw, _, _ := readOptional(path)
	m, err := decodeSettings(raw)
	if err != nil {
		return ""
	}
	switch agent {
	case agentClaude:
		if v, _ := m["disableAllHooks"].(bool); v {
			return path + " sets disableAllHooks to true, so Claude Code ignores every hook"
		}
	case agentGemini:
		if cfg, ok := m["hooksConfig"].(map[string]any); ok {
			if v, isBool := cfg["enabled"].(bool); isBool && !v {
				return path + " sets hooksConfig.enabled to false, so Gemini CLI ignores every hook"
			}
		}
		if tools, ok := m["tools"].(map[string]any); ok {
			if v, isBool := tools["enableHooks"].(bool); isBool && !v {
				return path + " sets tools.enableHooks to false, so Gemini CLI ignores every hook"
			}
		}
	}
	return ""
}

func configuredPreCommand(home string) string {
	raw, _, _ := readOptional(filepath.Join(home, ".claude", "settings.json"))
	m, err := decodeSettings(raw)
	if err != nil {
		return ""
	}
	hooks, _ := hooksOf(m)
	groups, _ := hooks["PreToolUse"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		if matcher, _ := gm["matcher"].(string); matcher != "*" {
			continue
		}
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, hookMarker) {
				return cmd
			}
		}
	}
	return ""
}

func (d *doctor) git(m Manifest, had bool) {
	info, err := githook.Status(d.o.Home)
	switch {
	case err != nil:
		d.addFix("git layer", err, "", "check that git is installed")
	case had && m.Git == "skipped":
		d.warn("git layer", "skipped at install with --no-git, so commits made outside agent hooks are not checked", "run reright install again without --no-git")
	case had && m.Git == "manual" && !info.Active:
		d.warn("git layer", "your global core.hooksPath is "+info.GlobalHooksPath+", so the wrappers are not active", "add the two lines that install printed to the hook files in that directory")
	case !info.Installed || len(info.Missing) > 0:
		d.addFix("git layer", errors.New("the git wrappers are not installed in "+info.HooksDir), "", "run reright install again (it installs the git layer unless you pass --no-git)")
	case !info.Active:
		d.addFix("git layer", fmt.Errorf("git does not use the wrappers: core.hooksPath is %q", info.GlobalHooksPath), "", "run git config --global core.hooksPath "+info.HooksDir)
	case info.Mode == "off":
		d.warn("git layer", "installed and active, but mode is off in ~/.config/reright/git.json, so no commit is checked", "set mode to agents or always")
	default:
		detail := "active through core.hooksPath " + info.HooksDir + ", mode " + info.Mode + ". Limits: a repository with its own core.hooksPath (husky and similar) bypasses the layer, and doctor can only see the repository it runs in"
		if info.Mode != githook.ModeAlways && info.TTYHeuristic {
			detail += ". Commits with no terminal (IDEs, git GUIs) are checked like agent commits when the server answers and pass when it does not. Set \"tty_heuristic\": false in ~/.config/reright/git.json to stop checking them"
		}
		d.emit(Check{Name: "git layer", OK: true, Detail: detail})
		d.repoHooksPath(info)
	}
}

func (d *doctor) repoHooksPath(info githook.Info) {
	dir := d.o.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if local := githook.RepoHooksPath(dir); local != "" {
		d.warn("git layer here", "this repository sets its own core.hooksPath ("+local+"), so git does not use the reright wrappers here and commits in it are not checked by the git layer", "rely on the agent hooks in this repository, or add the two reright lines to the hook files in "+local+" (see docs/agents/git-layer.md)")
	}
}

func (d *doctor) environment() {
	f, err := toggle.Load(filepath.Join(d.o.Home, ".config", "reright"))
	if err != nil {
		d.warn("switches", err.Error()+", so every kind is checked", "run reright enable all to rewrite it")
	}
	now := time.Now()
	for _, k := range toggle.Kinds {
		if f.Off(k, "", now) || (f.Disabled[k].Until.IsZero() || now.Before(f.Disabled[k].Until)) && len(f.Disabled[k].Dirs) > 0 {
			if _, on := f.Disabled[k]; on {
				d.warn("switches", k+" checks are switched off", "reright status shows how long; reright enable "+k+" turns them back on")
			}
		}
	}
	for _, v := range []string{"RERIGHT_BYPASS", "RERIGHT_OFFLINE"} {
		if val := os.Getenv(v); val != "" {
			effect := "an agent that inherits it skips the text checks"
			if v == "RERIGHT_OFFLINE" {
				effect = "an agent that inherits it lets text through when the server is unreachable"
			}
			d.warn("environment", v+"="+val+" is set in this shell, and "+effect, "unset "+v+" in your shell profile and in the agent's environment")
		}
	}
}

func (d *doctor) projectFiles(agents []string) {
	dir := d.o.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		return
	}
	seen := map[string]bool{}
	for i := 0; i < 32 && dir != d.o.Home && dir != filepath.Dir(dir); i++ {
		for _, a := range agents {
			for _, rel := range agentSpecs[a].ProjectFiles {
				path := filepath.Join(dir, rel)
				if a == agentClaude && !seen[path+"off"] && projectDisablesHooks(path) {
					seen[path+"off"] = true
					d.warn("claude project hooks", path+" sets disableAllHooks to true, so Claude Code ignores every hook in this project, reright's included", "remove that setting")
				}
				if seen[path+a] || !projectFileDefinesHooks(path) {
					continue
				}
				seen[path+a] = true
				d.warn(a+" project hooks", path+" defines hooks at project level. The agent can edit that file and change or add hooks", "keep hook configuration at user level only, and review this file")
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func projectDisablesHooks(path string) bool {
	raw, exists, _ := readOptional(path)
	if !exists {
		return false
	}
	m, err := decodeSettings(raw)
	if err != nil {
		return false
	}
	v, _ := m["disableAllHooks"].(bool)
	return v
}

func projectFileDefinesHooks(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		entries, _ := os.ReadDir(path)
		return len(entries) > 0
	}
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte("hooks"))
}

func Failed(checks []Check) int {
	n := 0
	for _, c := range checks {
		if !c.OK && !c.Warn {
			n++
		}
	}
	return n
}

func Warned(checks []Check) int {
	n := 0
	for _, c := range checks {
		if c.Warn {
			n++
		}
	}
	return n
}

func configProblem(p Paths, tok, server string) error {
	switch {
	case tok == "":
		return withFix(reinstallHint, "no device token in %s", p.TokenFile())
	case server == "":
		return withFix(reinstallHint, "no server URL in %s", p.URLFile())
	}
	return nil
}

func checkServer(ctx context.Context, hc *http.Client, server, tok string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/check?sha="+strings.Repeat("0", 64), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := hc.Do(req)
	if err != nil {
		return withFix("check your network and the server URL", "cannot reach %s: %w", server, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return withFix(reinstallHint, "%s rejected the device token. It was probably revoked", server)
	}
	return withFix("try again later or check the server status", "%s answered %s", server, resp.Status)
}

func checkMCP(ctx context.Context, server, tok string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cs, err := mcpSession(ctx, server, tok)
	if err != nil {
		return withFix("check the device token and the server URL, or "+reinstallHint, "could not open the MCP connection at %s/mcp: %w", server, err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		return withFix("try again later", "could not list MCP tools: %w", err)
	}
	found := map[string]bool{}
	for _, t := range tools.Tools {
		found[t.Name] = true
	}
	for _, want := range []string{"submit_for_review", "wait_for_review"} {
		if !found[want] {
			return withFix("check that the server is up to date", "the MCP server has no %s tool", want)
		}
	}
	return nil
}

type fakeChecker struct {
	URL  string
	stop func()
}

func startFakeChecker() (*fakeChecker, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"approved":false}`)
	})}
	go srv.Serve(ln)
	return &fakeChecker{URL: "http://" + ln.Addr().String(), stop: func() { srv.Close() }}, nil
}

func probeEnv(home, url string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || strings.HasPrefix(k, "RERIGHT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "RERIGHT_URL="+url, "RERIGHT_TOKEN=doctor-probe")
}

func runProbe(ctx context.Context, env []string, input string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func withProbe(ctx context.Context, fn func(env []string) error) error {
	fc, err := startFakeChecker()
	if err != nil {
		return fmt.Errorf("could not start the local test checker: %w", err)
	}
	defer fc.stop()
	tmp, err := os.MkdirTemp("", "reright-doctor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	return fn(probeEnv(tmp, fc.URL))
}

func denied(out string, err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return true
	}
	return err == nil && strings.Contains(out, "deny")
}

// clientVersions reports the versions of reright and reright-hook and warns when they differ or the hook is too
// old to say. Both are replaced together by reright upgrade.
func (d *doctor) clientVersions(ctx context.Context, hook string) {
	if d.o.Version == "" {
		return
	}
	hv := hookVersion(ctx, hook)
	switch {
	case hv == "":
		d.warn("client version", fmt.Sprintf("reright %s, and reright-hook does not report a version, so it is an older build", d.o.Version), "run reright upgrade")
	case hv != d.o.Version:
		d.warn("client version", fmt.Sprintf("reright %s but reright-hook %s", d.o.Version, hv), "run reright upgrade")
	default:
		d.emit(Check{Name: "client version", OK: true, Detail: fmt.Sprintf("reright and reright-hook are both %s", hv)})
	}
}

// hookVersion runs the installed hook with --version. It returns "" when the hook has no such flag.
func hookVersion(ctx context.Context, hook string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, hook, "--version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func checkHookBinary(ctx context.Context, hook, home string) error {
	info, err := os.Stat(hook)
	if err != nil {
		return withFix(reinstallHint, "%s not found", hook)
	}
	if info.Mode()&0o111 == 0 {
		return withFix("chmod +x "+hook+", or "+reinstallHint, "%s is not executable", hook)
	}
	return withProbe(ctx, func(env []string) error {
		out, err := runProbe(ctx, env, probeInput, hook, "pre")
		if err != nil {
			return withFix(reinstallHint, "%s failed to run: %v %s", hook, err, out)
		}
		if strings.Contains(out, "deny") {
			return withFix(reinstallHint, "the hook blocked a harmless call: %s", out)
		}
		out, err = runProbe(ctx, env, denyProbeInput, hook, "pre")
		if !denied(out, err) {
			return withFix(reinstallHint, "%s did not block git commit -m x, which no reviewer approved (output %q, result %v). It is not the real reright-hook, or it cannot check the server", hook, out, err)
		}
		return nil
	})
}

func checkConfiguredPreCommand(ctx context.Context, command string) error {
	return withProbe(ctx, func(env []string) error {
		out, err := runProbe(ctx, env, denyProbeInput, "/bin/sh", "-c", command)
		if !denied(out, err) {
			return withFix(reinstallHint, "the configured hook command %q did not block git commit -m x when run through sh -c (output %q, result %v)", command, out, err)
		}
		return nil
	})
}

var commandRunes = regexp.MustCompile("[;&|`\n]|\\$\\(")

func shellWords(cmd string) ([]string, error) {
	if commandRunes.MatchString(cmd) {
		return nil, fmt.Errorf("the command %q chains or substitutes other commands", cmd)
	}
	out, err := exec.Command("/bin/sh", "-c", "set -f; printf '%s\\0' "+cmd).Output()
	if err != nil {
		return nil, fmt.Errorf("sh could not read %q: %w", cmd, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

func sameCommand(a, b string) bool {
	if a == b {
		return true
	}
	wa, err := shellWords(a)
	if err != nil {
		return false
	}
	wb, err := shellWords(b)
	if err != nil || len(wa) != len(wb) {
		return false
	}
	for i := range wa {
		if wa[i] != wb[i] {
			return false
		}
	}
	return true
}

func checkRegistered(ctx context.Context, r Runner) error {
	claude, err := r.LookPath("claude")
	if err != nil {
		return withFix("install Claude Code, then run reright install --agent claude", "the claude command is not on PATH")
	}
	if out, err := r.Run(ctx, claude, "mcp", "get", "reright"); err != nil {
		return withFix("run reright install --agent claude with a new setup code", "claude has no reright MCP server (%s)", strings.TrimSpace(string(out)))
	}
	return nil
}
