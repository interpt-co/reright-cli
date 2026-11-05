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

// denyProbeInput commits a message in plain words, because the hook lets text that is not free text,
// such as "x" or a version, through without asking the server.
const denyProbeInput = `{"session_id":"reright-doctor","cwd":"/","tool_name":"Bash","tool_input":{"command":"git commit -m 'reright doctor checks that this commit is blocked'"}}`

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
		d.add("mcp", checkMCP(ctx, server, tok), "the MCP endpoint lists the reright tools")
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
	d.legacyAgents(m)
	d.git()
	d.environment()
	d.projectFiles(agents)
	return d.checks
}

func doctorAgents(o DoctorOptions, m Manifest, had bool) ([]string, error) {
	requested, err := normalizeAgents(o.Agents, false)
	if err != nil {
		return nil, err
	}
	if len(requested) > 0 {
		return requested, nil
	}
	if had && m.Agents[agentClaude] != nil {
		return []string{agentClaude}, nil
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
	for _, check := range []string{"hooks", "mcp", "rules"} {
		label := map[string]string{"hooks": " hooks", "mcp": " mcp entry", "rules": " rules"}[check]
		if check == "mcp" && plan.ClaudeMCP {
			d.add(name+label, checkRegistered(ctx, d.o.Runner), "claude knows the reright MCP server")
			continue
		}
		edits := partsOfKind(plan, check)
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
		if check == "hooks" {
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
		if check == "hooks" {
			detail = "every hook points at " + env.HookPath
		}
		d.addFix(name+label, cerr, detail, fixHint)
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
	if agent != agentClaude {
		return ""
	}
	path := filepath.Join(home, ".claude", "settings.json")
	raw, _, _ := readOptional(path)
	m, err := decodeSettings(raw)
	if err != nil {
		return ""
	}
	if v, _ := m["disableAllHooks"].(bool); v {
		return path + " sets disableAllHooks to true, so Claude Code ignores every hook"
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

// legacyAgents warns about agents an earlier version set up and this one no longer checks.
func (d *doctor) legacyAgents(m Manifest) {
	if len(d.o.Agents) > 0 {
		return
	}
	for _, name := range m.legacyNames() {
		d.warn(name, "was set up by an earlier reright, which no longer supports it, so its hooks are not checked and may block its tool calls", "run reright uninstall --agent "+name+" to remove what was written for it")
	}
}

// git reports a git layer that an earlier version installed and that is still there.
func (d *doctor) git() {
	if !githook.Present(d.o.Home) {
		return
	}
	info, err := githook.Status(d.o.Home)
	switch {
	case err != nil:
		d.warn("git layer", "an earlier reright installed git hooks in "+info.HooksDir+", and reading the git config failed: "+err.Error(), "run reright upgrade to remove them")
	case info.Active:
		d.warn("git layer", "the global core.hooksPath still points at the git hooks an earlier reright installed in "+info.HooksDir+". reright no longer uses them", "run reright upgrade to remove them and put core.hooksPath back")
	default:
		d.warn("git layer", "files of the git layer an earlier reright installed are still in "+info.HooksDir, "run reright upgrade to remove them")
	}
}

func (d *doctor) environment() {
	f, err := toggle.Load(filepath.Join(d.o.Home, ".config", "reright"))
	if err != nil {
		d.warn("switches", err.Error()+", so every check is checked", "run reright enable all to rewrite it")
	}
	now := time.Now()
	for _, k := range toggle.Checks {
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
	if found["rewrite"] && found["rewrite_result"] {
		return nil
	}
	return withFix("check that the server is up to date", "the MCP server does not have the rewrite and rewrite_result tools")
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
			return withFix(reinstallHint, "%s did not block a test commit that no reviewer approved (output %q, result %v). It is not the real reright-hook, or it cannot check the server", hook, out, err)
		}
		return nil
	})
}

func checkConfiguredPreCommand(ctx context.Context, command string) error {
	return withProbe(ctx, func(env []string) error {
		out, err := runProbe(ctx, env, denyProbeInput, "/bin/sh", "-c", command)
		if !denied(out, err) {
			return withFix(reinstallHint, "the configured hook command %q did not block a test commit when run through sh -c (output %q, result %v)", command, out, err)
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
