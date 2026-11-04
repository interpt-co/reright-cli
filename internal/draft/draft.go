// Package draft defines a review item and the text normalization used to
// match approved text against what an agent later tries to send.
package draft

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
	"time"
	"unicode"
)

type Kind string

// The kinds an agent is told to use. They describe what the reader receives. Where the text goes (a
// recipient, a channel, a repository) belongs in the target and the context, not in the kind.
const (
	KindEmail    Kind = "email"
	KindMessage  Kind = "message"
	KindPost     Kind = "post"
	KindDocument Kind = "document"
	KindCode     Kind = "code"
	KindOther    Kind = "other"
)

// Older kinds. Agents set up before the kinds were reduced still send them, and drafts already stored
// carry them. They are accepted and shown as the kind they stand for now.
const (
	KindCommit      Kind = "commit"
	KindPR          Kind = "pr"
	KindPRComment   Kind = "pr_comment"
	KindCodeComment Kind = "code_comment"
	KindOdooLogNote Kind = "odoo_log_note"
	KindOdooMessage Kind = "odoo_message"
	KindChat        Kind = "chat"
)

var Kinds = []Kind{KindEmail, KindMessage, KindPost, KindDocument, KindCode, KindOther}

var legacyKinds = map[Kind]Kind{
	KindCommit:      KindCode,
	KindPR:          KindCode,
	KindPRComment:   KindCode,
	KindCodeComment: KindCode,
	KindOdooLogNote: KindMessage,
	KindOdooMessage: KindMessage,
	KindChat:        KindMessage,
}

// Canonical returns the current kind for k: an older kind becomes the kind it stands for, and a current
// kind is returned as it is.
func (k Kind) Canonical() Kind {
	if c, ok := legacyKinds[k]; ok {
		return c
	}
	return k
}

func (k Kind) Valid() bool {
	if _, ok := legacyKinds[k]; ok {
		return true
	}
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

// HasTitle reports whether the first line of this kind's text can be a title or subject.
func (k Kind) HasTitle() bool {
	switch k {
	case KindEmail, KindCode, KindCommit, KindPR:
		return true
	}
	return false
}

// Section is one named part of a draft's text. The review page shows each part in its own editor.
type Section struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Sections splits text into the parts the reviewer edits one by one. An email splits into a subject and
// a body, and code text (a commit, a PR) into a title and a description, the way SplitTitle does. Any
// other text, and text without a blank line after its first line, is a single section without a name.
func Sections(k Kind, text string) []Section {
	if k.HasTitle() {
		if title, body, ok := SplitTitle(text); ok {
			first, second := "Title", "Description"
			if k.Canonical() == KindEmail {
				first, second = "Subject", "Body"
			}
			if body == "" {
				return []Section{{Name: first, Text: title}}
			}
			return []Section{{Name: first, Text: title}, {Name: second, Text: body}}
		}
	}
	return []Section{{Text: text}}
}

// JoinSections is the inverse of Sections for text that was split: the parts with a blank line between.
func JoinSections(parts []string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimRight(p, " \t\r\n"))
		}
	}
	return strings.Join(kept, "\n\n")
}

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusExpired  Status = "expired"
)

const (
	VoiceQueued    = "queued"
	VoiceSuggested = "suggested"
	VoiceDropped   = "dropped"
	VoiceTimeout   = "timeout"
	VoiceSkipped   = "skipped"
	DecidedByVoice = "voice"
)

type Draft struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Text         string    `json:"text"`
	Context      string    `json:"context"`
	Diff         string    `json:"diff,omitempty"`
	Target       string    `json:"target"`
	Origin       string    `json:"origin,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Run          string    `json:"run,omitempty"`
	Status       Status    `json:"status"`
	FinalText    string    `json:"final_text,omitempty"`
	RejectReason string    `json:"reject_reason,omitempty"`
	Suggestion   string    `json:"suggestion,omitempty"`
	VoiceNote    string    `json:"voice_note,omitempty"`
	VoiceState   string    `json:"voice_state,omitempty"`
	DecidedBy    string    `json:"decided_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	DecidedAt    time.Time `json:"decided_at"`
	Purged       bool      `json:"purged,omitempty"`
	Seq          int64     `json:"-"`
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a random 10 character id that is safe to put in a URL.
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(idEncoding.EncodeToString(b))
}

// Normalize removes the formatting differences that shells, git and browsers
// introduce, so the same words always produce the same hash.
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(Normalize(s)))
	return hex.EncodeToString(sum[:])
}

// SplitTitle splits "title\n\nbody". ok is false when the first line is not
// followed by a blank line.
func SplitTitle(s string) (title, body string, ok bool) {
	n := Normalize(s)
	first, rest, found := strings.Cut(n, "\n")
	if !found {
		return first, "", false
	}
	if !strings.HasPrefix(rest, "\n") {
		return "", "", false
	}
	return first, Normalize(rest), true
}

// ApprovalHashes lists the hashes that count as approved once final is
// approved. Kinds with a title also approve the title and the body on their
// own, so `gh pr edit --title` or `--body` pass separately.
func ApprovalHashes(k Kind, final string) []string {
	hashes := []string{Hash(final)}
	if !k.HasTitle() {
		return hashes
	}
	title, body, ok := SplitTitle(final)
	if !ok {
		return hashes
	}
	hashes = append(hashes, Hash(title))
	if body != "" {
		hashes = append(hashes, Hash(body))
	}
	return hashes
}

const (
	MaxAgentLen  = 60
	MaxRunLen    = 120
	MaxTargetLen = 300
	MaxOriginLen = 300
)

// NoneSentinel is the filter value that means "no agent, run or target", so
// a label may never equal it.
const NoneSentinel = "-"

func dropped(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// Label collapses whitespace, control and format characters (bidi overrides,
// zero width characters) to single spaces and cuts the result to max
// characters, so names stay one short line. A label of just "-" would collide
// with the filter value for "none", so it becomes "- (name)".
func Label(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, dropped), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	if s == NoneSentinel {
		return "- (name)"
	}
	return s
}

// StripFormat removes control-flow and format characters such as bidi
// overrides from text that is shown to a person, and keeps line breaks and tabs.
func StripFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
