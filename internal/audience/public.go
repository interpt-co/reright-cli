package audience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PublicTTL is how long an answer from a hosting service is kept.
const PublicTTL = 24 * time.Hour

// HostingProbe asks GitHub and GitLab, without credentials, whether a repository can be read by anyone.
// Other hosts are never public to it, so their repositories are judged by their authors alone. Answers are
// cached in Dir for PublicTTL; errors are not cached.
type HostingProbe struct {
	Dir    string // cache directory; empty means no cache
	Client *http.Client
	Now    func() time.Time
	// BaseGitHub and BaseGitLab replace the API addresses in tests.
	BaseGitHub, BaseGitLab string
}

func (p HostingProbe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Public implements Detector.Public.
func (p HostingProbe) Public(ctx context.Context, r Remote) (bool, error) {
	u := p.apiURL(r)
	if u == "" {
		return false, nil
	}
	cache := ""
	if p.Dir != "" {
		sum := sha256.Sum256([]byte(u))
		cache = filepath.Join(p.Dir, hex.EncodeToString(sum[:12]))
		if b, err := os.ReadFile(cache); err == nil {
			if at, public, ok := strings.Cut(strings.TrimSpace(string(b)), " "); ok {
				if t, err := strconv.ParseInt(at, 10, 64); err == nil {
					age := p.now().Sub(time.Unix(t, 0))
					if age >= 0 && age < PublicTTL {
						return public == "public", nil
					}
				}
			}
		}
	}
	public, err := p.ask(ctx, u)
	if err != nil {
		return false, err
	}
	if cache != "" {
		word := "private"
		if public {
			word = "public"
		}
		if os.MkdirAll(p.Dir, 0o700) == nil {
			os.WriteFile(cache, []byte(strconv.FormatInt(p.now().Unix(), 10)+" "+word+"\n"), 0o600)
		}
	}
	return public, nil
}

func (p HostingProbe) apiURL(r Remote) string {
	switch r.Host {
	case "github.com", "www.github.com":
		parts := strings.Split(r.Path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return ""
		}
		base := p.BaseGitHub
		if base == "" {
			base = "https://api.github.com"
		}
		return base + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	case "gitlab.com", "www.gitlab.com":
		if !strings.Contains(r.Path, "/") {
			return ""
		}
		base := p.BaseGitLab
		if base == "" {
			base = "https://gitlab.com"
		}
		return base + "/api/v4/projects/" + url.PathEscape(r.Path)
	}
	return ""
}

func (p HostingProbe) ask(ctx context.Context, u string) (bool, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "reright-hook")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusUnauthorized:
		// Private repositories answer 404 to an anonymous caller, so nobody outside can read them.
		return false, nil
	}
	return false, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
}
