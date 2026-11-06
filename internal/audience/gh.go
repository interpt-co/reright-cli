package audience

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GHAccount finds the GitHub logins the user is signed in to with the gh command. It reads the gh hosts
// file first. When that names no github.com account, it asks `gh api user` and keeps the answer in
// CacheDir for PublicTTL. When gh is missing or fails, it returns nothing, so no noreply address counts as
// the user's.
type GHAccount struct {
	Home     string
	Getenv   func(string) string
	CacheDir string // empty means no cache
	Now      func() time.Time
	// API runs `gh api user --jq .login` and returns its output. Nil means exec gh from PATH.
	API func(ctx context.Context) (string, error)
}

func (g GHAccount) getenv(k string) string {
	if g.Getenv != nil {
		return g.Getenv(k)
	}
	return os.Getenv(k)
}

func (g GHAccount) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Logins implements Detector.GitHubLogins.
func (g GHAccount) Logins(ctx context.Context) []string {
	if logins := hostsLogins(g.hostsFile()); len(logins) > 0 {
		return logins
	}
	cache := ""
	if g.CacheDir != "" {
		cache = filepath.Join(g.CacheDir, "gh-login")
		if b, err := os.ReadFile(cache); err == nil {
			if at, login, ok := strings.Cut(strings.TrimSpace(string(b)), " "); ok {
				if t, err := strconv.ParseInt(at, 10, 64); err == nil {
					if age := g.now().Sub(time.Unix(t, 0)); age >= 0 && age < PublicTTL && login != "" {
						return []string{login}
					}
				}
			}
		}
	}
	api := g.API
	if api == nil {
		api = func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login").Output()
			return string(out), err
		}
	}
	out, err := api(ctx)
	login := strings.ToLower(strings.TrimSpace(out))
	if err != nil || login == "" || strings.ContainsAny(login, " \n\t") {
		return nil
	}
	if cache != "" && os.MkdirAll(g.CacheDir, 0o700) == nil {
		os.WriteFile(cache, []byte(strconv.FormatInt(g.now().Unix(), 10)+" "+login+"\n"), 0o600)
	}
	return []string{login}
}

func (g GHAccount) hostsFile() string {
	if d := g.getenv("GH_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "hosts.yml")
	}
	if d := g.getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh", "hosts.yml")
	}
	if g.Home == "" {
		return ""
	}
	return filepath.Join(g.Home, ".config", "gh", "hosts.yml")
}

// hostsLogins reads the github.com accounts from a gh hosts.yml: the active `user:` and the keys under
// `users:`. It reads only the plain layout gh writes, and anything else gives no logins.
func hostsLogins(path string) []string {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'`))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	inHost, inUsers := false, false
	usersIndent, childIndent := 0, 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		text := strings.TrimSpace(line)
		if indent == 0 {
			inHost = strings.TrimSuffix(text, ":") == "github.com"
			inUsers = false
			continue
		}
		if !inHost {
			continue
		}
		if inUsers && indent > usersIndent {
			// only the keys right under users: are logins; deeper lines are their settings
			if childIndent == 0 {
				childIndent = indent
			}
			if indent == childIndent {
				if key, _, ok := strings.Cut(text, ":"); ok {
					add(key)
				}
			}
			continue
		}
		inUsers = false
		key, val, ok := strings.Cut(text, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "user":
			add(val)
		case "users":
			inUsers, usersIndent, childIndent = true, indent, 0
		}
	}
	return out
}
