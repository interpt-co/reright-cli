package hookcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pendingCap = 30 * time.Minute

type pendingDraft struct {
	ID          string    `json:"id"`
	ReviewURL   string    `json:"review_url,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type pendingState struct {
	Drafts []pendingDraft `json:"drafts"`
}

func (env Env) now() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

func loadPending(path string) (pendingState, error) {
	var st pendingState
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func savePending(path string, st pendingState) error {
	if len(st.Drafts) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	tmp := path + "." + hex.EncodeToString(sum[:4]) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func recordSubmit(in call, env Env) error {
	r, ok := findResult(in.Response, func(m map[string]any) bool { return m["status"] == "pending" })
	if !ok || r.ID == "" || env.StateDir == "" {
		return nil
	}
	path := pendingPath(env.StateDir, in)
	st, err := loadPending(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		st = pendingState{}
	}
	kept := st.Drafts[:0]
	for _, d := range st.Drafts {
		if d.ID != r.ID {
			kept = append(kept, d)
		}
	}
	st.Drafts = append(kept, pendingDraft{ID: r.ID, ReviewURL: r.URL, SubmittedAt: env.now()})
	return savePending(path, st)
}

func recordWait(in call, env Env) error {
	r, ok := findResult(in.Response, func(m map[string]any) bool { _, has := m["status"].(string); return has })
	if !ok || r.ID == "" || r.Status == "pending" || env.StateDir == "" {
		return nil
	}
	path := pendingPath(env.StateDir, in)
	st, err := loadPending(path)
	if err != nil {
		return nil
	}
	kept := st.Drafts[:0]
	for _, d := range st.Drafts {
		if d.ID != r.ID {
			kept = append(kept, d)
		}
	}
	st.Drafts = kept
	return savePending(path, st)
}

// stopCheck decides whether the agent may end its turn. block is the reason to keep it going, note a line
// for the person about drafts reright stopped waiting for. Every error lets the agent stop.
func stopCheck(c call, decodeErr error, env Env) (block, note string) {
	defer func() {
		if recover() != nil {
			block, note = "", ""
		}
	}()
	if decodeErr != nil || env.StateDir == "" {
		return "", ""
	}
	path := pendingPath(env.StateDir, c)
	st, err := loadPending(path)
	if err != nil {
		return "", ""
	}
	now := env.now()
	var live, old []pendingDraft
	dropped := false
	for _, d := range st.Drafts {
		if seenRejected(env.StateDir, c, d.ID) {
			dropped = true // rejected, so resolved, even when another agent of the session saw it
			continue
		}
		if now.Sub(d.SubmittedAt) < pendingCap {
			live = append(live, d)
		} else {
			old = append(old, d)
		}
	}
	if len(old) > 0 || dropped {
		st.Drafts = live
		if savePending(path, st) != nil {
			return "", ""
		}
	}
	if len(old) > 0 {
		note = staleNote(old)
	}
	if len(live) == 0 {
		return "", note
	}
	// An agent under a reject lock was told to stop and wait for the user, so it may end its turn. The
	// person still sees which drafts are open.
	if _, err := os.Stat(lockPath(env.StateDir, c)); err == nil {
		return "", joinNotes(note, lockedNote(live))
	}
	return blockReason(live), note
}

func blockReason(live []pendingDraft) string {
	if len(live) == 1 {
		id := live[0].ID
		return fmt.Sprintf("reright: still waiting for review (id %s). Don't end your turn: call rewrite_result(id %s) until approved/rejected.", id, id)
	}
	ids := make([]string, len(live))
	calls := make([]string, len(live))
	for i, d := range live {
		ids[i] = d.ID
		calls[i] = "rewrite_result(id " + d.ID + ")"
	}
	return fmt.Sprintf("reright: still waiting for review (ids %s). Don't end your turn: call %s until approved/rejected.", strings.Join(ids, ", "), strings.Join(calls, ", "))
}

func lockedNote(live []pendingDraft) string {
	ids := make([]string, len(live))
	for i, d := range live {
		ids[i] = d.ID
	}
	if len(ids) == 1 {
		return "reright: the agent stopped after a rejection. Draft " + ids[0] + " is still waiting for review."
	}
	return "reright: the agent stopped after a rejection. Drafts " + strings.Join(ids, ", ") + " are still waiting for review."
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}

func staleNote(old []pendingDraft) string {
	var parts []string
	for _, d := range old {
		if d.ReviewURL != "" {
			parts = append(parts, d.ID+", at "+d.ReviewURL)
		} else {
			parts = append(parts, d.ID)
		}
	}
	if len(parts) == 1 {
		return "reright: still waiting for review (id " + parts[0] + "). The agent stopped waiting after 30 minutes."
	}
	return "reright: still waiting for review (ids " + strings.Join(parts, ", ") + "). The agent stopped waiting after 30 minutes."
}

func claudeStop(stdout io.Writer, block, note string) {
	out := map[string]string{}
	if block != "" {
		out["decision"] = "block"
		out["reason"] = block
	}
	if note != "" {
		out["systemMessage"] = note
	}
	if len(out) > 0 {
		json.NewEncoder(stdout).Encode(out)
	}
}

type toolResult struct {
	ID, Status, URL, Reason string
}

// findResult looks through a tool response for the first object with an id and a status that match. MCP
// results arrive either as structured content or as JSON inside a text block, so both shapes are searched.
func findResult(raw json.RawMessage, match func(map[string]any) bool) (toolResult, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return toolResult{}, false
	}
	var r toolResult
	var walk func(any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			if _, has := t["status"]; has && match(t) {
				r.ID, _ = t["id"].(string)
				r.Status, _ = t["status"].(string)
				r.URL, _ = t["review_url"].(string)
				r.Reason, _ = t["reason"].(string)
				return true
			}
			for _, c := range t {
				if walk(c) {
					return true
				}
			}
		case []any:
			for _, c := range t {
				if walk(c) {
					return true
				}
			}
		case string:
			if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					return walk(inner)
				}
			}
		}
		return false
	}
	ok := walk(v)
	return r, ok
}
