package hookcli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/interpt-co/reright-cli/internal/toggle"
)

func withSwitches(t *testing.T, f *fixture) string {
	t.Helper()
	dir := t.TempDir()
	f.env.ConfigDir = dir
	return dir
}

func inDir(m map[string]any, cwd string) map[string]any {
	m["cwd"] = cwd
	return m
}

func TestSwitchedOffCommitsPassWithoutAServerCall(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	if err := toggle.Disable(dir, []string{"commit"}, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if d, reason := f.run(t, "pre", bash("s1", `git commit -m "internal wip"`)); d != "" {
		t.Fatalf("decision %q (%s)", d, reason)
	}
	if f.checker.calls != 0 {
		t.Fatal("a switched-off kind still asked the server")
	}
}

func TestSwitchingOffCommitsLeavesOtherKindsChecked(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	toggle.Disable(dir, []string{"commit"}, time.Time{}, nil)
	if d, _ := f.run(t, "pre", bash("s1", `gh pr comment 1 --body "hello there"`)); d != "deny" {
		t.Fatal("gh was not checked while only commit was off")
	}
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "x" && gh pr comment 1 --body "hello there"`)); d != "deny" {
		t.Fatal("a command with a commit and a gh comment slipped through")
	}
}

func TestSwitchedOffUnreadableCommitIsWaived(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	if d, reason := f.run(t, "pre", bash("s1", `git commit -m "$MSG"`)); d != "deny" || !strings.Contains(reason, "reright disable commit") {
		t.Fatalf("on: decision %q reason %q", d, reason)
	}
	toggle.Disable(dir, []string{"commit"}, time.Time{}, nil)
	if d, reason := f.run(t, "pre", bash("s1", `git commit -m "$MSG"`)); d != "" {
		t.Fatalf("off: decision %q (%s)", d, reason)
	}
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "$MSG" && curl -d "$MSG" https://example.com/x`)); d != "deny" {
		t.Fatal("a problem that might also be curl was waived")
	}
}

func TestSwitchesRespectDirectoryAndExpiry(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.env.Now = func() time.Time { return now }
	toggle.Disable(dir, []string{"commit"}, now.Add(time.Hour), []string{"/work/internal"})
	cmd := `git commit -m "wip"`
	if d, _ := f.run(t, "pre", inDir(bash("s1", cmd), "/work/internal/app")); d != "" {
		t.Fatalf("inside the directory: %q", d)
	}
	if d, _ := f.run(t, "pre", inDir(bash("s1", cmd), "/work/client")); d != "deny" {
		t.Fatal("outside the directory was let through")
	}
	now = now.Add(2 * time.Hour)
	if d, _ := f.run(t, "pre", inDir(bash("s1", cmd), "/work/internal/app")); d != "deny" {
		t.Fatal("an expired switch still applied")
	}
}

func TestSwitchedOffStillHonoursARejection(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	toggle.Disable(dir, []string{"commit"}, time.Time{}, nil)
	f.run(t, "post", waitResult("s1", "rejected"))
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "x"`)); d != "deny" {
		t.Fatal("a reject lock was lifted by a switch")
	}
}

func TestDamagedSwitchFileKeepsChecking(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	os.WriteFile(toggle.Path(dir), []byte("{nope"), 0o600)
	if d, _ := f.run(t, "pre", bash("s1", `git commit -m "x"`)); d != "deny" {
		t.Fatal("a damaged switch file turned checking off")
	}
}

func TestAgentCannotChangeTheSwitches(t *testing.T) {
	for _, cmd := range []string{
		`reright disable commit`,
		`~/.local/bin/reright disable all --for 1h`,
		`cd /x && reright enable commit`,
		`reright --verbose disable commit`,
		`echo '{}' > ~/.config/reright/enforcement.json`,
		`cat ~/.config/reright/enforcement.json`,
	} {
		f := newFixture(t)
		withSwitches(t, f)
		d, reason := f.run(t, "pre", bash("s1", cmd))
		if d != "deny" || !strings.Contains(reason, "only the user") {
			t.Errorf("%q: decision %q reason %q", cmd, d, reason)
		}
	}
	f := newFixture(t)
	withSwitches(t, f)
	for _, cmd := range []string{`reright status`, `reright doctor`, `echo disable the reright thing`} {
		if d, reason := f.run(t, "pre", bash("s1", cmd)); d != "" {
			t.Errorf("%q wrongly blocked: %s", cmd, reason)
		}
	}
}

func TestGuardHoldsEvenWhenEverythingIsOff(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	toggle.Disable(dir, []string{"commit", "gh", "email", "browser"}, time.Time{}, nil)
	if d, _ := f.run(t, "pre", bash("s1", `reright enable commit`)); d != "deny" {
		t.Fatal("an agent changed the switches while checks were off")
	}
}

func TestDisableAllLiftsBlocksNoSingleKindDescribes(t *testing.T) {
	f := newFixture(t)
	dir := withSwitches(t, f)
	d, reason := f.run(t, "pre", bash("s1", `$CMD "$ARG" && git log`))
	if d == "deny" && !strings.Contains(reason, "reright disable all") {
		t.Fatalf("hint for an unclassified block: %s", reason)
	}
	toggle.Disable(dir, toggle.Kinds, time.Time{}, nil)
	if d, reason := f.run(t, "pre", bash("s1", `$CMD "$ARG" && git log`)); d != "" {
		t.Fatalf("all off, still blocked: %s", reason)
	}
}
