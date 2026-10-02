package onboard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/interpt-co/reright-cli/internal/release"
)

const (
	codePrefix   = "rrs_"
	maxDownload  = 64 << 20
	maxTextFetch = 1 << 20
)

var ErrCodeRefused = errors.New("setup code refused")

func checkServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("server URL %q is not a valid URL", raw)
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", fmt.Errorf("server URL %q must use https (plain http is allowed only for localhost)", raw)
	}
	return u.String(), nil
}

func checkCode(code string) error {
	if !strings.HasPrefix(code, codePrefix) || len(code) < len(codePrefix)+8 {
		return fmt.Errorf("that does not look like a reright setup code (they start with %s). Copy the code from the dashboard again", codePrefix)
	}
	return nil
}

func apiError(resp *http.Response) string {
	var body struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxTextFetch))
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		return body.Error
	}
	return resp.Status
}

type exchanged struct {
	Token  string
	Server string
}

func exchange(ctx context.Context, hc *http.Client, server, code, device string) (exchanged, error) {
	payload, _ := json.Marshal(map[string]string{"code": code, "name": device})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/api/setup/exchange", bytes.NewReader(payload))
	if err != nil {
		return exchanged{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return exchanged{}, fmt.Errorf("could not reach %s: %w", server, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return exchanged{}, fmt.Errorf("%w: the code is invalid, expired or already used. A code works once and lasts ten minutes. Generate a new one on the dashboard", ErrCodeRefused)
	case http.StatusConflict:
		return exchanged{}, fmt.Errorf("%w: %s", ErrCodeRefused, apiError(resp))
	case http.StatusTooManyRequests:
		return exchanged{}, fmt.Errorf("the server is rate limiting setup attempts (%s). Wait a minute and run it again with the same code", apiError(resp))
	default:
		return exchanged{}, fmt.Errorf("the server answered %s", apiError(resp))
	}
	var out struct {
		Token     string `json:"token"`
		ServerURL string `json:"server_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTextFetch)).Decode(&out); err != nil || out.Token == "" {
		return exchanged{}, errors.New("the server sent an unreadable token response")
	}
	res := exchanged{Token: out.Token, Server: server}
	if out.ServerURL != "" {
		if s, err := checkServerURL(out.ServerURL); err == nil {
			res.Server = s
		}
	}
	return res, nil
}

func get(ctx context.Context, hc *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response too large", u)
	}
	return b, nil
}

// signedTable downloads checksums.txt and its signature, checks the signature against the compiled-in release
// key and returns the table of checksums. Nothing listed in it is trusted before this succeeds.
func signedTable(ctx context.Context, hc *http.Client, baseURL string, pub ed25519.PublicKey) (map[string]string, error) {
	base := strings.TrimRight(baseURL, "/")
	sums, err := get(ctx, hc, base+"/"+release.ChecksumsName, maxTextFetch)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	sig, err := get(ctx, hc, base+"/"+release.SignatureName, maxTextFetch)
	if err != nil {
		return nil, fmt.Errorf("download signature: %w", err)
	}
	if err := release.Verify(pub, sums, string(sig)); err != nil {
		return nil, fmt.Errorf("the checksums file is not signed by the reright release key, so nothing was installed: %w", err)
	}
	return release.ParseChecksums(sums)
}

// fetchAsset downloads one release file and checks it against the signed table.
func fetchAsset(ctx context.Context, hc *http.Client, baseURL string, table map[string]string, asset string, limit int64) ([]byte, error) {
	want, ok := table[asset]
	if !ok {
		return nil, fmt.Errorf("%s is not listed in the signed checksums", asset)
	}
	bin, err := get(ctx, hc, strings.TrimRight(baseURL, "/")+"/"+asset, limit)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s does not match its signed checksum, so nothing was installed", asset)
	}
	return bin, nil
}

func fetchVerified(ctx context.Context, hc *http.Client, baseURL string, pub ed25519.PublicKey, asset string) ([]byte, error) {
	table, err := signedTable(ctx, hc, baseURL, pub)
	if err != nil {
		return nil, err
	}
	return fetchAsset(ctx, hc, baseURL, table, asset, maxDownload)
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

func mcpSession(ctx context.Context, server, tok string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "reright-cli", Version: "1.0.0"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             server + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{token: tok, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
}

func callTool(ctx context.Context, cs *mcp.ClientSession, name string, args, out any) error {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}
	if res.IsError {
		msg := name + " failed"
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				msg = t.Text
			}
		}
		return errors.New(msg)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

type testResult struct {
	Status string
	Reason string
	URL    string
}

const testDraftText = "Test message from reright setup\n\nThis draft checks that your setup works. Approve it in the browser to finish."

func submitTestDraft(ctx context.Context, server, tok string, wait time.Duration, announce func(reviewURL string)) (testResult, error) {
	cs, err := mcpSession(ctx, server, tok)
	if err != nil {
		return testResult{}, fmt.Errorf("could not open the MCP connection: %w", err)
	}
	defer cs.Close()
	var sub struct {
		ID        string `json:"id"`
		ReviewURL string `json:"review_url"`
	}
	err = callTool(ctx, cs, "submit_for_review", map[string]string{
		"kind":    "other",
		"text":    testDraftText,
		"context": "This is the test draft that reright setup submits. Approving it confirms that the hooks and the MCP server work on this machine.",
		"target":  "reright setup",
		"origin":  "reright install",
	}, &sub)
	if err != nil {
		return testResult{}, fmt.Errorf("submit test draft: %w", err)
	}
	announce(sub.ReviewURL)
	deadline := time.Now().Add(wait)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return testResult{Status: "pending", URL: sub.ReviewURL}, nil
		}
		secs := min(int(left.Seconds())+1, 30)
		var w struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		if err := callTool(ctx, cs, "wait_for_review", map[string]any{"id": sub.ID, "max_wait_seconds": secs}, &w); err != nil {
			return testResult{}, fmt.Errorf("wait for the test draft: %w", err)
		}
		if w.Status != "pending" {
			return testResult{Status: w.Status, Reason: w.Reason, URL: sub.ReviewURL}, nil
		}
	}
}
