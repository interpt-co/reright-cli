package draft

import "testing"

func TestIsFreeText(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"https://x.y/z", false},
		{"v1.2.3", false},
		{"fix/rounding-17", false},
		{"Closes #42", false},
		{"`go test ./...`", false},
		{"Paulo, Ana, Marta", false},
		{"Bump golang.org/x/net to 0.31.0", false},
		{"", false},
		{"   \n\t", false},
		{"42", false},
		{"@ana see https://github.com/o/r/pull/7", false},
		{"paulo@interpt.co", false},
		{"internal/draft/freetext.go", false},
		{"RERIGHT_VOICE_URL", false},
		{"IsFreeText", false},
		{"```go\nfunc main() {\n\tfmt.Println(\"hi there friend\")\n}\n```", false},
		{"- main\n- release/v2\n- fix/rounding-17", false},
		{"- Ana\n- Marta\n- Paulo", false},
		{"Bump deps: golang.org/x/net 0.31.0, golang.org/x/sys 0.27.0, golang.org/x/text 0.20.0", false},
		{"Thanks, looks good to me", true},
		{"Fix rounding on multi-currency invoices", true},
		{"I think we should merge it today", true},
		{"Thanks Ana, I'll look at it tomorrow.", true},
		{"Fix rounding of invoice totals\n\nInvoices in two currencies were rounded twice, once per line and once on the total. This rounds once, at the end.\n\n- Adds a test for EUR and USD lines\n- Closes #42", true},
		{"Run `go test ./...` before you push, please.", true},
		{"- [x] tests pass\n- [ ] docs updated\n- [x] changelog entry added", true},
		{"Fix x\n\nBecause y", true},
	}
	for _, c := range cases {
		if got := IsFreeText(c.text); got != c.want {
			t.Errorf("IsFreeText(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestIsFreeTextNeedsMoreThanAThirdPlainWords(t *testing.T) {
	// 3 plain words among 10 tokens is not enough.
	if IsFreeText("see the docs https://a.b/1 https://a.b/2 https://a.b/3 https://a.b/4 https://a.b/5 v1 v2") {
		t.Error("mostly links counted as free text")
	}
	// 4 plain words among 6 tokens is.
	if !IsFreeText("please read the notes https://a.b/1 https://a.b/2") {
		t.Error("a sentence with two links did not count")
	}
}
