package draft

import (
	"reflect"
	"testing"
)

func TestOnlyTheSixKindsAreValid(t *testing.T) {
	for _, k := range Kinds {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []Kind{"", "commits", "Email", "sms", "commit", "pr", "chat", "odoo_message"} {
		if k.Valid() {
			t.Errorf("%q should not be valid", k)
		}
	}
}

func TestSectionsSplitTitleAndBody(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		text string
		want []Section
	}{
		{"email", KindEmail, "Your invoice\n\nHi Marta,\n\nIt is done.", []Section{{"Subject", "Your invoice"}, {"Body", "Hi Marta,\n\nIt is done."}}},
		{"commit as code", KindCode, "Fix rounding\n\nTotals round per line.", []Section{{"Title", "Fix rounding"}, {"Description", "Totals round per line."}}},
		{"one line", KindCode, "Fix rounding", []Section{{"", "Fix rounding"}}},
		{"no blank line after the first", KindEmail, "Hello\nthere", []Section{{"", "Hello\nthere"}}},
		{"message is one section", KindMessage, "Title\n\nBody", []Section{{"", "Title\n\nBody"}}},
		{"post is one section", KindPost, "Launch day\n\nWe shipped.", []Section{{"", "Launch day\n\nWe shipped."}}},
	}
	for _, c := range cases {
		if got := Sections(c.kind, c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, got, c.want)
		}
	}
}

func TestJoinSectionsReversesTheSplit(t *testing.T) {
	text := "Fix rounding\n\nTotals round per line.\n\nSecond paragraph."
	var parts []string
	for _, s := range Sections(KindCode, text) {
		parts = append(parts, s.Text)
	}
	if got := JoinSections(parts); got != text {
		t.Fatalf("joined %q, want %q", got, text)
	}
	if got := JoinSections([]string{"Subject", "  ", "Body\n"}); got != "Subject\n\nBody" {
		t.Fatalf("empty parts must drop: %q", got)
	}
	if got := JoinSections([]string{"Only"}); got != "Only" {
		t.Fatalf("%q", got)
	}
}
