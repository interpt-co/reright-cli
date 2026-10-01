package draft

import (
	"regexp"
	"slices"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"crlf", "a\r\nb\r\n", "a\nb"},
		{"lone cr", "a\rb", "a\nb"},
		{"trailing spaces", "a  \nb\t\n", "a\nb"},
		{"outer blank lines", "\n\n  \na\n\nb\n\n\n", "a\n\nb"},
		{"inner blank lines kept", "a\n\n\nb", "a\n\n\nb"},
		{"leading spaces kept", "  a\n    b", "  a\n    b"},
		{"empty", "\n \n", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("%s: Normalize(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestHashIgnoresFormattingNoise(t *testing.T) {
	if Hash("fix: x\n") != Hash("fix: x") {
		t.Error("trailing newline changed the hash")
	}
	if Hash("a\r\nb") != Hash("a\nb") {
		t.Error("CRLF changed the hash")
	}
	if Hash("fix: x") == Hash("fix: y") {
		t.Error("different texts share a hash")
	}
	if len(Hash("x")) != 64 {
		t.Errorf("hash length %d, want 64", len(Hash("x")))
	}
}

func TestSplitTitle(t *testing.T) {
	cases := []struct {
		in, title, body string
		ok              bool
	}{
		{"Title\n\nBody line\nmore", "Title", "Body line\nmore", true},
		{"Title only", "Title only", "", false},
		{"Title\nno blank line", "", "", false},
		{"\n\nTitle\n\n\nBody\n", "Title", "Body", true},
	}
	for _, c := range cases {
		title, body, ok := SplitTitle(c.in)
		if title != c.title || body != c.body || ok != c.ok {
			t.Errorf("SplitTitle(%q) = %q, %q, %v; want %q, %q, %v", c.in, title, body, ok, c.title, c.body, c.ok)
		}
	}
}

func TestApprovalHashes(t *testing.T) {
	full := "Subject\n\nBody"
	got := ApprovalHashes(KindCommit, full)
	want := []string{Hash(full), Hash("Subject"), Hash("Body")}
	if !slices.Equal(got, want) {
		t.Errorf("commit hashes = %v, want %v", got, want)
	}
	if got := ApprovalHashes(KindPRComment, full); !slices.Equal(got, []string{Hash(full)}) {
		t.Errorf("pr_comment should only hash the full text, got %v", got)
	}
	if got := ApprovalHashes(KindPR, "Title only"); !slices.Equal(got, []string{Hash("Title only")}) {
		t.Errorf("title-only pr should hash once, got %v", got)
	}
}

func TestKindValid(t *testing.T) {
	for _, k := range Kinds {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	if Kind("tweet").Valid() {
		t.Error("unknown kind reported valid")
	}
	if !KindEmail.HasTitle() || KindChat.HasTitle() {
		t.Error("HasTitle wrong")
	}
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{10}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := NewID()
		if !re.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestLabelDropsFormatCharacters(t *testing.T) {
	for _, r := range []rune{0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0xfeff} {
		got := Label("ab"+string(r)+"cd", 20)
		if got != "ab cd" {
			t.Errorf("U+%04X survived or glued: %q", r, got)
		}
	}
	if got := Label("‮⁦", 20); got != "" {
		t.Errorf("only format characters should leave nothing, got %q", got)
	}
}

func TestLabelRenamesTheNoneSentinel(t *testing.T) {
	for _, in := range []string{"-", " - ", "​-​"} {
		if got := Label(in, 10); got != "- (name)" {
			t.Errorf("Label(%q) = %q", in, got)
		}
	}
	if got := Label("- (name)", 20); got != "- (name)" {
		t.Errorf("a renamed label must stay stable, got %q", got)
	}
	if got := Label("a-b", 10); got != "a-b" {
		t.Errorf("got %q", got)
	}
}

func TestStripFormatKeepsLineBreaks(t *testing.T) {
	if got := StripFormat("a‮b\nc\td​e"); got != "ab\nc\tde" {
		t.Errorf("got %q", got)
	}
}
