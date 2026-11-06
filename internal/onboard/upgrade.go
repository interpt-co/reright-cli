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
	"regexp"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/githook"
	"github.com/interpt-co/reright-cli/internal/release"
)

// cliBinary is the name of the reright command in release assets.
const cliBinary = "reright"

type UpgradeOptions struct {
	Home       string
	ReleaseURL string
	PublicKey  ed25519.PublicKey
	GOOS       string
	GOARCH     string
	Current    string // version of the running reright, "dev" for a local build
	ExePath    string // the running reright binary, which is replaced
	HookPath   string // empty means the path recorded at install, else the default
	CheckOnly  bool   // only say whether a newer release exists
	Force      bool   // replace the binaries even when the release is not newer
	HTTP       *http.Client
	Out        io.Writer
}

// Latest downloads the signed checksums and returns the version of the newest release. The checksums are
// verified with the compiled-in key before anything in them is read.
func Latest(ctx context.Context, o UpgradeOptions) (string, map[string]string, error) {
	o.defaults()
	if len(o.PublicKey) != ed25519.PublicKeySize {
		return "", nil, errors.New("this build of reright has no release key compiled in, so it cannot verify downloads. Download the official reright binary again")
	}
	table, err := signedTable(ctx, o.HTTP, o.ReleaseURL, o.PublicKey)
	if err != nil {
		return "", nil, err
	}
	raw, err := fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.VersionFile, maxTextFetch)
	if err != nil {
		return "", nil, fmt.Errorf("%w (this release predates the upgrade command, so install it again from the dashboard)", err)
	}
	latest := strings.TrimSpace(string(raw))
	if _, ok := release.ParseVersion(latest); !ok {
		return "", nil, fmt.Errorf("the release names a version that cannot be read: %q", latest)
	}
	return latest, table, nil
}

func (o *UpgradeOptions) defaults() {
	if o.ReleaseURL == "" {
		o.ReleaseURL = release.DefaultBaseURL
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

// Upgrade replaces reright and reright-hook with the newest signed release. The device token and the agent
// configuration are not touched, so no setup code is needed. It also removes the git hooks an earlier
// version installed, even when the programs are already current.
func Upgrade(ctx context.Context, o UpgradeOptions) error {
	o.defaults()
	if !release.Supported(o.GOOS, o.GOARCH) {
		return fmt.Errorf("no reright build for %s/%s (Linux and macOS on amd64 and arm64 only)", o.GOOS, o.GOARCH)
	}
	if o.CheckOnly {
		if githook.Present(o.Home) {
			fmt.Fprintln(o.Out, "The git hooks an earlier reright installed are still there. reright upgrade removes them.")
		}
	} else {
		for _, line := range removeGitLayer(o.Home) {
			fmt.Fprintln(o.Out, "Git: "+line)
		}
	}
	latest, table, err := Latest(ctx, o)
	if err != nil {
		return err
	}
	hookPath := o.HookPath
	if hookPath == "" {
		hookPath = Paths{o.Home}.DefaultHook()
		if m, _, err := loadManifest(Paths{o.Home}); err == nil && m.HookPath != "" {
			hookPath = m.HookPath
		}
	}
	_, statErr := os.Stat(hookPath)
	hookInstalled := statErr == nil

	// Each program is judged on its own: reright may be current while the hook is older, or too old to say.
	_, known := release.ParseVersion(o.Current)
	cliStale := release.Newer(latest, o.Current) || !known // a local build can always be replaced
	hookSeen := ""
	hookStale := false
	if hookInstalled {
		hookSeen = hookVersion(ctx, hookPath)
		hookStale = hookSeen != latest
	}
	stale := cliStale || hookStale
	if o.CheckOnly {
		switch {
		case cliStale:
			fmt.Fprintf(o.Out, "reright %s is available (you have %s). Run: reright upgrade\n", latest, o.Current)
		case hookStale:
			seen := hookSeen
			if seen == "" {
				seen = "an older build with no version"
			}
			fmt.Fprintf(o.Out, "reright %s is current, but reright-hook is %s and the release is %s. Run: reright upgrade\n", o.Current, seen, latest)
		default:
			fmt.Fprintf(o.Out, "reright %s is up to date.\n", o.Current)
		}
		return nil
	}
	if !stale && !o.Force {
		fmt.Fprintf(o.Out, "reright %s is up to date.\n", o.Current)
		return nil
	}
	if o.ExePath == "" {
		return errors.New("cannot tell where the running reright is, so it cannot be replaced")
	}

	fmt.Fprintf(o.Out, "Downloading reright %s and checking its signed checksums\n", latest)
	cli, err := fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.AssetName(cliBinary, o.GOOS, o.GOARCH), maxDownload)
	if err != nil {
		return err
	}
	var hook []byte
	if hookInstalled {
		if hook, err = fetchAsset(ctx, o.HTTP, o.ReleaseURL, table, release.AssetName(hookBinary, o.GOOS, o.GOARCH), maxDownload); err != nil {
			return err
		}
	}

	// The hook goes first: it is what agents run on every call. A failure after it leaves a working pair.
	if hookInstalled {
		if err := writeFile(hookPath, hook, 0o755); err != nil {
			return fmt.Errorf("replace %s: %w", hookPath, err)
		}
		fmt.Fprintf(o.Out, "Replaced %s\n", hookPath)
	} else {
		fmt.Fprintf(o.Out, "reright-hook is not installed at %s, so only reright is replaced. Run reright install to set up your agents.\n", hookPath)
	}
	if err := writeFile(o.ExePath, cli, 0o755); err != nil {
		return fmt.Errorf("replace %s: %w (the hook was already replaced)", o.ExePath, err)
	}
	fmt.Fprintf(o.Out, "Replaced %s\n", o.ExePath)
	if hookInstalled {
		refreshClaudeHooks(Paths{o.Home}, hookPath, o.Out)
		// The new hook knows the new rules text for ~/.claude/CLAUDE.md, so it writes it. A hook older
		// than this does the same on its first call.
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		exec.CommandContext(hctx, hookPath, "heal").Run()
		cancel()
	}
	fmt.Fprintf(o.Out, "Upgraded to reright %s (was reright %s). Run reright doctor to check the setup.\n", latest, o.Current)
	return nil
}

// refreshClaudeHooks adds hook entries this version knows about to a Claude Code setup that reright installed
// and that still has some of its hooks. A file without any reright hook is left alone.
func refreshClaudeHooks(p Paths, hookPath string, out io.Writer) {
	unlock, err := lockConfig(p)
	if err != nil {
		return
	}
	defer unlock()
	missing, err := healSettings(p, hookPath)
	if err != nil {
		fmt.Fprintf(out, "Could not add the new Claude Code hooks to %s (%v). Run reright doctor.\n", p.Settings(), err)
		return
	}
	if len(missing) > 0 {
		fmt.Fprintf(out, "Added the new Claude Code hooks to %s: %s\n", p.Settings(), strings.Join(missing, ", "))
	}
}

// healSettings merges the current hook entries into ~/.claude/settings.json when reright installed them
// there and some, but not all, are missing. It returns what it added. The caller holds the config lock.
// An empty hookPath means the one recorded at install, else the default.
func healSettings(p Paths, hookPath string) ([]string, error) {
	m, had, err := loadManifest(p)
	if err != nil || !had || m.Agents[agentClaude] == nil {
		return nil, err
	}
	if hookPath == "" {
		hookPath = m.HookPath
	}
	if hookPath == "" {
		hookPath = p.DefaultHook()
	}
	raw, err := os.ReadFile(p.Settings())
	if err != nil {
		return nil, err
	}
	missing, err := MissingHooks(raw, hookPath)
	if err != nil || len(missing) == 0 || len(missing) == len(hookSpecs) {
		return nil, err
	}
	e := fileEdit{Path: p.Settings(), Parts: []part{claudeHooksPart(hookPath)}}
	if err := writeEdit(p, agentClaude, &m, m.record(agentClaude), e, false); err != nil {
		return nil, err
	}
	return missing, nil
}

const healEvery = 24 * time.Hour

// HealClaudeHooks is run by reright-hook for Claude Code. An older reright upgrade replaces the binaries but
// not the settings, so the new hook adds the entries it needs itself, at most once a day for each settings
// file. It also removes the git hooks an earlier version installed. RERIGHT_NO_SELF_HEAL=1 turns it off.
// It also brings the reright rule block in ~/.claude/CLAUDE.md up to date. It writes nothing to the terminal;
// the caller ignores errors. The once-a-day marker is kept per rule block, so a hook with a new rule block
// heals on its first call after an upgrade even when the old hook healed earlier that day.
func HealClaudeHooks(home, stateDir string, getenv func(string) string, now time.Time) error {
	return healClaude(home, stateDir, getenv, now, false)
}

// HealClaudeHooksNow is HealClaudeHooks without the once-a-day wait. reright upgrade runs it through the
// new hook (reright-hook heal), so the new rule block is written at once.
func HealClaudeHooksNow(home, stateDir string, getenv func(string) string, now time.Time) error {
	return healClaude(home, stateDir, getenv, now, true)
}

func healClaude(home, stateDir string, getenv func(string) string, now time.Time, force bool) error {
	if getenv("RERIGHT_NO_SELF_HEAL") == "1" || home == "" || stateDir == "" {
		return nil
	}
	p := Paths{home}
	marker := filepath.Join(stateDir, "selfheal", sha([]byte(p.Settings() + "\x00" + ruleBlock))[:16])
	if b, err := os.ReadFile(marker); err == nil && !force {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil && now.Sub(t) < healEvery && !t.After(now) {
			return nil
		}
	}
	if err := writePrivate(marker, []byte(now.UTC().Format(time.RFC3339)+"\n")); err != nil {
		return err
	}
	unlock, err := tryLockConfig(p)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = healSettings(p, "")
	if rerr := healRuleBlock(p); err == nil {
		err = rerr
	}
	// The git hooks of 0.1 are not used any more. An upgrade run by a 0.1 reright does not know to remove
	// them, so the new hook does it.
	if githook.Present(home) {
		if _, gerr := githook.Remove(home); err == nil {
			err = gerr
		}
	}
	return err
}

// ruleBlockSpan finds the reright block from its start marker to its end marker, and nothing around it.
var ruleBlockSpan = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(blockStart) + `.*?` + regexp.QuoteMeta(blockEnd))

// healRuleBlock replaces the text between the reright markers in ~/.claude/CLAUDE.md with the current rule
// block when it differs. Everything outside the markers stays byte for byte, and a file without both markers
// is left alone. When the install record still matches the file, it is updated, so uninstall can still
// restore the backup. The caller holds the config lock.
func healRuleBlock(p Paths) error {
	path := p.ClaudeMD()
	dest, _ := resolveTarget(path)
	cur, exists, err := readOptional(dest)
	if err != nil || !exists {
		return err
	}
	loc := ruleBlockSpan.FindIndex(cur)
	if loc == nil {
		return nil
	}
	want := strings.TrimRight(ruleBlock, "\n")
	if string(cur[loc[0]:loc[1]]) == want {
		return nil
	}
	next := append(append(append([]byte(nil), cur[:loc[0]]...), want...), cur[loc[1]:]...)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(dest); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeFile(dest, next, mode); err != nil {
		return err
	}
	m, had, err := loadManifest(p)
	if err != nil || !had {
		return nil
	}
	if rec := m.Agents[agentClaude]; rec != nil {
		if fr := rec.file(path); fr != nil && fr.AfterSHA == sha(cur) {
			fr.AfterSHA = sha(next)
			return saveManifest(p, m)
		}
	}
	return nil
}
