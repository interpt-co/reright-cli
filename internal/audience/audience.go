// Package audience decides whether other people read the commit messages of a git repository, so the
// hook checks commit and tag messages only where someone besides the user will see them.
//
// A repository counts as shared when one of these holds:
//
//   - git config reright.commits is "always";
//   - it has a remote on a hosting service (any URL with a host, not a path on this machine) and a commit
//     in the last 200 was written by someone other than the user;
//   - it has such a remote and the remote is public (asked of GitHub and GitLab without credentials);
//   - it is a shallow clone with such a remote, because the history that would show other authors is missing.
//
// It counts as the user's own when reright.commits is "never", when it has no hosted remote, or when every
// recent commit is the user's and the remote is not public. When git cannot answer, the repository counts
// as shared, so the hook checks.
//
// The user is every user.email in git config (system, global and repository), every reright.email, and,
// for a GitHub remote, the noreply address of the remote's owner. Bot authors ([bot] in the address) are
// ignored.
package audience

import (
	"context"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// Commits is how many recent commits are read for other authors.
const Commits = 200

// Decision says whether a repository's commit messages are read by others, and why.
type Decision struct {
	Shared bool
	Why    string
}

// Detector runs the test. The zero value runs git from PATH and asks no hosting service.
type Detector struct {
	// Git runs git in dir with args and returns its standard output. Nil means exec.
	Git func(ctx context.Context, dir string, args ...string) (string, error)
	// Public reports whether a hosted remote can be read without signing in. Nil means never public.
	// An error makes the repository count as shared.
	Public func(ctx context.Context, r Remote) (bool, error)
}

// Remote is a hosted remote, parsed from its URL.
type Remote struct {
	URL  string
	Host string // lower case, without port or user
	Path string // without leading slash and .git suffix
}

func (d Detector) git(ctx context.Context, dir string, args ...string) (string, error) {
	if d.Git != nil {
		return d.Git(ctx, dir, args...)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

// Decide applies the test to the repository that contains dir.
func (d Detector) Decide(ctx context.Context, dir string) Decision {
	if _, err := d.git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		return Decision{true, "git could not read the repository here"}
	}
	mode, _ := d.git(ctx, dir, "config", "--get", "reright.commits")
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always":
		return Decision{true, "git config reright.commits is always"}
	case "never":
		return Decision{false, "git config reright.commits is never"}
	}
	urls, _ := d.git(ctx, dir, "config", "--get-regexp", `^remote\..*\.(url|pushurl)$`)
	var remotes []Remote
	for _, line := range strings.Split(urls, "\n") {
		_, u, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if r, hosted := ParseRemote(strings.TrimSpace(u)); hosted {
			remotes = append(remotes, r)
		}
	}
	if len(remotes) == 0 {
		return Decision{false, "it has no remote on a hosting service"}
	}
	if shallow, _ := d.git(ctx, dir, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(shallow) == "true" {
		return Decision{true, "it is a shallow clone, so other authors may be missing from its history"}
	}
	me := map[string]bool{}
	for _, key := range []string{"user.email", "reright.email"} {
		out, _ := d.git(ctx, dir, "config", "--get-all", key)
		for _, e := range strings.Split(out, "\n") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				me[e] = true
			}
		}
	}
	owners := map[string]bool{}
	for _, r := range remotes {
		if r.Host == "github.com" {
			if owner, _, ok := strings.Cut(r.Path, "/"); ok {
				owners[strings.ToLower(owner)] = true
			}
		}
	}
	log, err := d.git(ctx, dir, "log", "--all", "-n", strconv.Itoa(Commits), "--format=%aE")
	if err != nil {
		return Decision{true, "git could not read the history"}
	}
	for _, e := range strings.Split(log, "\n") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || me[e] || strings.Contains(e, "[bot]") || owners[noreplyLogin(e)] {
			continue
		}
		return Decision{true, "commits by " + e + " are in its recent history"}
	}
	if d.Public != nil {
		for _, r := range remotes {
			public, err := d.Public(ctx, r)
			if err != nil {
				return Decision{true, "could not tell whether " + r.Host + "/" + r.Path + " is public (" + err.Error() + ")"}
			}
			if public {
				return Decision{true, r.Host + "/" + r.Path + " is public"}
			}
		}
	}
	return Decision{false, "only the user's commits are in its recent history and its remote is not public"}
}

// noreplyLogin returns the login of a GitHub noreply address (123+login@users.noreply.github.com or
// login@users.noreply.github.com), or "".
func noreplyLogin(email string) string {
	local, ok := strings.CutSuffix(email, "@users.noreply.github.com")
	if !ok {
		return ""
	}
	if _, login, ok := strings.Cut(local, "+"); ok {
		return login
	}
	return local
}

// ParseRemote reads a remote URL. hosted is false for a path on this machine (a plain path or file://).
func ParseRemote(raw string) (r Remote, hosted bool) {
	r.URL = raw
	if raw == "" || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") || strings.HasPrefix(raw, "~") {
		return r, false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" || u.Hostname() == "" {
			return r, false
		}
		r.Host = strings.ToLower(u.Hostname())
		r.Path = cleanPath(u.Path)
		return r, true
	}
	// scp-like: [user@]host:path, where the colon comes before any slash
	colon := strings.Index(raw, ":")
	if colon <= 0 || (strings.Contains(raw[:colon], "/")) {
		return r, false
	}
	host := raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if host == "" {
		return r, false
	}
	r.Host = strings.ToLower(host)
	r.Path = cleanPath(raw[colon+1:])
	return r, true
}

func cleanPath(p string) string {
	p = strings.Trim(p, "/")
	return strings.TrimSuffix(p, ".git")
}
