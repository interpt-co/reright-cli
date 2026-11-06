package hookcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Locks and pending lists belong to one agent: the main thread of a session, or one of its subagents.
// The main thread's file sits at base/<session hash>, the name reright 0.2.2 and older used. A subagent's
// file sits at base/<session hash>.agents/<agent hash>, so a user prompt can clear the whole session.

func sessionKey(c call) string {
	key := "session:" + c.Session
	if c.Session == "" {
		key = "cwd:" + c.Cwd
	}
	return hashName(key)
}

func hashName(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func agentFile(base string, c call) string {
	if c.Agent == "" {
		return filepath.Join(base, sessionKey(c))
	}
	return filepath.Join(base, sessionKey(c)+".agents", hashName("agent:"+c.Agent))
}

// lockPath is the reject lock of the agent that made the call. Without a session id the lock is per
// working directory, or shared by all id-less sessions when there is no directory either. That can block
// an unrelated id-less session after a reject, never the other way round, and the next prompt clears it.
func lockPath(stateDir string, c call) string { return agentFile(filepath.Join(stateDir, "locks"), c) }

func pendingPath(stateDir string, c call) string {
	return agentFile(filepath.Join(stateDir, "pending"), c)
}

// clearLocks removes every reject lock in the call's session. A user prompt has no agent id, and the user
// answering lifts the locks of the subagents too.
func clearLocks(stateDir string, c call) {
	base := filepath.Join(stateDir, "locks")
	os.Remove(filepath.Join(base, sessionKey(c)))
	os.RemoveAll(filepath.Join(base, sessionKey(c)+".agents"))
}

type lock struct {
	DraftID  string   `json:"draft_id"`
	Reason   string   `json:"reason"`
	DraftIDs []string `json:"draft_ids,omitempty"`
}

func (l lock) ids() []string {
	if len(l.DraftIDs) > 0 {
		return l.DraftIDs
	}
	if l.DraftID != "" {
		return []string{l.DraftID}
	}
	return nil
}

// addToLock arms the caller's lock, or adds the draft to the lock it already has. A batch of rejections
// ends up as one lock that names them all.
func addToLock(stateDir string, c call, id, reason string) error {
	path := lockPath(stateDir, c)
	var l lock
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &l)
	}
	ids := l.ids()
	for _, have := range ids {
		if have == id {
			return nil
		}
	}
	if l.DraftID == "" {
		l.DraftID, l.Reason = id, reason
	}
	l.DraftIDs = append(ids, id)
	b, _ := json.Marshal(l)
	return writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func lockMessage(l lock) string {
	ids := l.ids()
	what := "draft " + idList(ids)
	if len(ids) > 1 {
		what = "drafts " + idList(ids)
	}
	reason := ""
	if l.Reason != "" {
		reason = " (reason: " + l.Reason + ")"
	}
	return fmt.Sprintf("reright: the reviewer rejected %s%s. Stop the current task. Do not retry, rephrase, resend or work around it. Tell the user the text was rejected and wait for his instructions. Until he answers you can still check your other drafts with rewrite_result, message the user or other agents, and end your turn.", what, reason)
}

// lockExempt names the calls a reject lock lets through: checking other drafts, talking to the user or
// to other agents, and stopping background work. None of them can use or resend the rejected text, and
// they still get the usual checks.
func lockExempt(tool string) bool {
	if isRewriteResult(tool) {
		return true
	}
	switch tool {
	case "SendMessage", "AskUserQuestion", "TaskStop":
		return true
	}
	return false
}

const rejectedKeep = 7 * 24 * time.Hour

// markRejected records that the session has seen draft id rejected. It reports true only the first time,
// so checking a rejection again never locks again. The marker is created atomically, which keeps parallel
// post hooks from both thinking they were first. An error counts as first: when in doubt the hook locks.
func markRejected(stateDir string, c call, id string, now time.Time) bool {
	dir := filepath.Join(stateDir, "rejected", sessionKey(c))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, hashName("draft:"+id)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false
	}
	if err != nil {
		return true
	}
	f.Close()
	pruneRejected(filepath.Join(stateDir, "rejected"), now)
	return true
}

func seenRejected(stateDir string, c call, id string) bool {
	_, err := os.Stat(filepath.Join(stateDir, "rejected", sessionKey(c), hashName("draft:"+id)))
	return err == nil
}

func pruneRejected(base string, now time.Time) {
	sessions, _ := os.ReadDir(base)
	for _, s := range sessions {
		dir := filepath.Join(base, s.Name())
		marks, _ := os.ReadDir(dir)
		left := len(marks)
		for _, m := range marks {
			if info, err := m.Info(); err == nil && now.Sub(info.ModTime()) > rejectedKeep {
				if os.Remove(filepath.Join(dir, m.Name())) == nil {
					left--
				}
			}
		}
		if left == 0 {
			os.Remove(dir)
		}
	}
}

// idList names up to three ids and counts the rest.
func idList(ids []string) string {
	shown := ids
	if len(shown) > 3 {
		shown = shown[:3]
	}
	out := strings.Join(shown, ", ")
	if more := len(ids) - len(shown); more > 0 {
		out += fmt.Sprintf(" and %d more", more)
	}
	return out
}
