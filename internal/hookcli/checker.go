package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPChecker calls GET /api/check on the reright server.
type HTTPChecker struct {
	BaseURL string
	Token   string
	Client  *http.Client
	// Version is sent as X-Reright-Client so the server can tell an old hook to upgrade.
	Version string
	// Policy, when set, is called with the server's X-Reright-Upgrade and X-Reright-Latest answers.
	Policy func(upgrade, latest string)
}

func (c HTTPChecker) Approved(ctx context.Context, sha string) (bool, error) {
	if c.Token == "" {
		return false, errors.New("no API token configured in ~/.config/reright/token")
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/api/check?sha=" + url.QueryEscape(sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Version != "" {
		req.Header.Set("X-Reright-Client", "reright-hook/"+c.Version)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if c.Policy != nil {
		c.Policy(resp.Header.Get("X-Reright-Upgrade"), resp.Header.Get("X-Reright-Latest"))
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("server answered %s", resp.Status)
	}
	var out struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("bad check response: %w", err)
	}
	return out.Approved, nil
}
