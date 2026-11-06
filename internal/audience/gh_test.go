package audience

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeHosts(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func noAPI(t *testing.T) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		t.Error("asked gh api although the hosts file names the account")
		return "", errors.New("no")
	}
}

func TestGhHostsFile(t *testing.T) {
	home := t.TempDir()
	writeHosts(t, filepath.Join(home, ".config", "gh"), `github.com:
    git_protocol: ssh
    users:
        Paro:
            oauth_token: gho_secret
        work-paro:
    user: paro
ghe.example.com:
    user: enterprise-me
`)
	g := GHAccount{Home: home, Getenv: func(string) string { return "" }, API: noAPI(t)}
	got := g.Logins(context.Background())
	if !slices.Equal(got, []string{"paro", "work-paro"}) {
		t.Fatalf("logins = %v", got)
	}
}

func TestGhConfigDirFromTheEnvironment(t *testing.T) {
	cfg := t.TempDir()
	writeHosts(t, cfg, "github.com:\n  user: other\n")
	env := map[string]string{"GH_CONFIG_DIR": cfg}
	g := GHAccount{Home: t.TempDir(), Getenv: func(k string) string { return env[k] }, API: noAPI(t)}
	if got := g.Logins(context.Background()); !slices.Equal(got, []string{"other"}) {
		t.Fatalf("logins = %v", got)
	}
	xdg := t.TempDir()
	writeHosts(t, filepath.Join(xdg, "gh"), "github.com:\n  user: xdg-me\n")
	env = map[string]string{"XDG_CONFIG_HOME": xdg}
	if got := g.Logins(context.Background()); !slices.Equal(got, []string{"xdg-me"}) {
		t.Fatalf("logins = %v", got)
	}
}

func TestGhAPIFallbackIsCached(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	calls := 0
	answer, fail := "Paro\n", error(nil)
	g := GHAccount{Home: t.TempDir(), Getenv: func(string) string { return "" }, CacheDir: t.TempDir(), Now: func() time.Time { return now },
		API: func(context.Context) (string, error) { calls++; return answer, fail }}
	ctx := context.Background()
	if got := g.Logins(ctx); !slices.Equal(got, []string{"paro"}) {
		t.Fatalf("logins = %v", got)
	}
	g.Logins(ctx)
	if calls != 1 {
		t.Fatalf("gh api called %d times", calls)
	}
	now = now.Add(PublicTTL + time.Minute)
	answer, fail = "", errors.New("gh: not logged in")
	if got := g.Logins(ctx); got != nil {
		t.Fatalf("an expired cache and a failing gh gave %v", got)
	}
	if calls != 2 {
		t.Fatalf("expired cache not asked again: %d", calls)
	}
}

func TestNoGhMeansNoLogins(t *testing.T) {
	g := GHAccount{Home: t.TempDir(), Getenv: func(k string) string {
		if k == "PATH" {
			return t.TempDir()
		}
		return ""
	}}
	t.Setenv("PATH", t.TempDir()) // no gh anywhere
	if got := g.Logins(context.Background()); got != nil {
		t.Fatalf("logins without gh = %v", got)
	}
}
