package onboard

import (
	"encoding/json"
	"strings"
	"testing"
)

const hook = "/home/u/.local/bin/reright-hook"

func TestMergeHooksIntoEmptyAndMissing(t *testing.T) {
	for _, in := range []string{"", "  \n", "{}"} {
		out, err := MergeHooks([]byte(in), hook)
		if err != nil {
			t.Fatal(err)
		}
		missing, err := MissingHooks(out, hook)
		if err != nil || len(missing) != 0 {
			t.Fatalf("input %q: missing %v, %v", in, missing, err)
		}
	}
}

func TestMergeHooksKeepsOtherSettingsAndIsIdempotent(t *testing.T) {
	in := `{"numbers": 12345678901234567890, "html": "<a&b>", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}]}], "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "other"}]}]}}`
	once, err := MergeHooks([]byte(in), hook)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := MergeHooks(once, hook)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("merge is not idempotent:\n%s\n%s", once, twice)
	}
	s := string(once)
	for _, want := range []string{"12345678901234567890", "<a&b>", `"command": "notify"`, `"command": "other"`} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	if strings.Count(s, hook+" pre") != 1 {
		t.Fatalf("duplicate pre hook:\n%s", s)
	}
	var parsed map[string]any
	if err := json.Unmarshal(once, &parsed); err != nil {
		t.Fatal(err)
	}
}

func TestMergeHooksReplacesStaleReerightHookPath(t *testing.T) {
	old := `{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "/old/place/reright-hook pre"}]}]}}`
	out, err := MergeHooks([]byte(old), hook)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "/old/place") {
		t.Fatalf("stale hook kept:\n%s", out)
	}
}

func TestStripHooksRemovesOnlyOurs(t *testing.T) {
	merged, _ := MergeHooks([]byte(`{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "mine"}, {"type": "command", "command": "/x/reright-hook pre"}]}]}}`), hook)
	out, err := StripHooks(merged)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, hookMarker) || !strings.Contains(s, `"mine"`) {
		t.Fatalf("strip result:\n%s", s)
	}
	if strings.Contains(s, "PostToolUse") || strings.Contains(s, "UserPromptSubmit") {
		t.Fatalf("empty events left behind:\n%s", s)
	}
}

func TestStripHooksDropsEmptyHooksKeyAndLeavesUntouchedFilesAlone(t *testing.T) {
	merged, _ := MergeHooks([]byte(`{"model": "x"}`), hook)
	out, _ := StripHooks(merged)
	if strings.Contains(string(out), "hooks") || !strings.Contains(string(out), `"model": "x"`) {
		t.Fatalf("got %s", out)
	}
	plain := "{ \"model\":   \"x\" }"
	same, err := StripHooks([]byte(plain))
	if err != nil || string(same) != plain {
		t.Fatalf("untouched file was rewritten: %q %v", same, err)
	}
}

func TestBadSettingsShapes(t *testing.T) {
	for _, in := range []string{"{not json", "[]", "null", `{"hooks": []}`, `{"hooks": {"PreToolUse": {}}}`} {
		if _, err := MergeHooks([]byte(in), hook); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestMissingHooksReportsWhichAreAbsent(t *testing.T) {
	out, _ := MergeHooks([]byte("{}"), hook)
	missing, _ := MissingHooks(out, "/elsewhere/reright-hook")
	if len(missing) != 3 {
		t.Fatalf("missing %v", missing)
	}
	partial := `{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "` + hook + ` pre"}]}]}}`
	missing, _ = MissingHooks([]byte(partial), hook)
	if len(missing) != 2 || !strings.Contains(missing[0], "PostToolUse") {
		t.Fatalf("missing %v", missing)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/a/b-c_d.e": "/a/b-c_d.e",
		"/a b/c":     "'/a b/c'",
		"/it's/x":    `'/it'\''s/x'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestAddAndStripBlock(t *testing.T) {
	if got := AddBlock(""); got != ruleBlock {
		t.Fatalf("empty file: %q", got)
	}
	base := "# Mine\n\ntext"
	added := AddBlock(base)
	if !HasBlock(added) || !strings.HasPrefix(added, base+"\n\n"+blockStart) {
		t.Fatalf("added: %q", added)
	}
	if AddBlock(added) != added {
		t.Fatal("adding twice changed the file")
	}
	if got := StripBlock(added); got != base+"\n" {
		t.Fatalf("stripped: %q", got)
	}
	if StripBlock("no block") != "no block" || HasBlock("no block") {
		t.Fatal("false positive")
	}
}

func TestServerAndCodeValidation(t *testing.T) {
	if checkCode("rrs_abcdefghijkl") != nil {
		t.Fatal("good code refused")
	}
	for _, c := range []string{"", "rrs_", "abc", "rrd_abcdefghijkl"} {
		if checkCode(c) == nil {
			t.Errorf("%q accepted", c)
		}
	}
}
