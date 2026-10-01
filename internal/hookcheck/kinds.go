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
	case strings.HasPrefix(t.Source, "browser"):
		return "browser"
	}
	return ""
}

// ProblemKinds lists the kinds a call that could not be read might have been sending.
// A problem can be waived only when every entry is switched off; "" stands for something
// no switch covers, so a call that might also run curl or an unknown tool stays blocked.
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
	server := serverOf(tool)
	switch {
	case isGmailServer(server):
		return []string{"email"}
	case isChromeServer(server):
		return []string{"browser"}
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
		case "curl", "wget":
			seen["http"] = true
		case "mail", "mailx", "sendmail", "mutt", "msmtp", "swaks", "smtplib":
			seen["email"] = true
		default:
			seen[""] = true
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
