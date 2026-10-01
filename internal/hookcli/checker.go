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
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
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
