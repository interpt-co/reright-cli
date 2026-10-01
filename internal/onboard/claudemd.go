package onboard

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed rule-block.md
var ruleBlock string

const (
	blockStart = "<!-- reright:start -->"
	blockEnd   = "<!-- reright:end -->"
)

var blockPattern = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(blockStart) + `.*?` + regexp.QuoteMeta(blockEnd) + `\n?`)

func HasBlock(md string) bool {
	return strings.Contains(md, blockStart) && strings.Contains(md, blockEnd)
}

func StripBlock(md string) string {
	return blockPattern.ReplaceAllString(md, "")
}

func AddBlock(md string) string {
	md = StripBlock(md)
	if md != "" && !strings.HasSuffix(md, "\n") {
		md += "\n"
	}
	if md != "" {
		md += "\n"
	}
	return md + ruleBlock
}
