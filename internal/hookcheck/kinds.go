package hookcheck

import (
	"encoding/json"
	"strings"
)

// Kind names the group of text a Text belongs to, matching internal/toggle.
// It is empty for text that no switch covers.
func (t Text) Kind() string {
	switch {
	case strings.HasPrefix(t.Source, "git "):
		return "commit"
	case strings.HasPrefix(t.Source, "gh "):
		return "gh"
	case strings.HasPrefix(t.Source, "email"):
		return "email"
	}
	return ""
}

// ProblemKinds lists the kinds a call that could not be read might have been sending.
// A problem can be waived only when every entry is switched off; "" stands for something
// no switch covers.
func ProblemKinds(tool string, input json.RawMessage) []string {
	if tool == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) != nil {
			return []string{""}
		}
		return bashKinds(in.Command)
	}
	if isGmailServer(serverOf(tool)) {
		return []string{"email"}
	}
	return []string{""}
}

func bashKinds(command string) []string {
	seen := map[string]bool{}
	for _, m := range sensitiveWord.FindAllStringSubmatch(command, -1) {
		switch strings.ToLower(m[2]) {
		case "git":
			seen["commit"] = true
		case "gh":
			seen["gh"] = true
		}
	}
	if len(seen) == 0 {
		return []string{""}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	return out
}
