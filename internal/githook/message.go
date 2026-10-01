package githook

import (
	"strings"

	"github.com/interpt-co/reright-cli/internal/draft"
)

const templateHint = "Please enter the commit message"

func cleanupMode(configured, raw, commentChar string) string {
	switch configured {
	case "strip", "whitespace", "verbatim", "scissors":
		return configured
	}
	for _, l := range strings.Split(raw, "\n") {
		if strings.HasPrefix(l, commentChar) && strings.Contains(l, templateHint) {
			return "strip"
		}
	}
	return "whitespace"
}

func finalize(raw, mode, commentChar string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if mode == "verbatim" {
		return draft.Normalize(raw)
	}
	scissors := commentChar + " ------------------------ >8 ------------------------"
	lines := strings.Split(raw, "\n")
	var kept []string
	for _, l := range lines {
		if mode != "whitespace" && l == scissors {
			break
		}
		if mode == "strip" && strings.HasPrefix(l, commentChar) {
			continue
		}
		kept = append(kept, strings.TrimRight(l, " \t"))
	}
	var out []string
	for _, l := range kept {
		if l == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, l)
	}
	return draft.Normalize(strings.Join(out, "\n"))
}
