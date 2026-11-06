package hookcheck

import (
	"encoding/json"
	"strings"
)

var sendOps = map[string]bool{"send_message": true, "create_draft": true, "update_draft": true, "reply": true, "forward": true}

func serverOf(toolName string) string {
	rest := strings.TrimPrefix(toolName, "mcp__")
	if i := strings.LastIndex(rest, "__"); i >= 0 {
		return strings.ToLower(rest[:i])
	}
	return strings.ToLower(rest)
}

func isGmailServer(server string) bool {
	return strings.Contains(server, "gmail") || strings.Contains(server, "workspace")
}

// Inspect returns the human-facing text a Claude Code tool call would send. It reads shell commands
// and the Gmail tools that send or stage mail. Every other tool is let through.
func Inspect(toolName string, input json.RawMessage, cwd string) Result {
	return InspectWith(toolName, input, cwd, Options{})
}

// InspectWith is Inspect with options.
func InspectWith(toolName string, input json.RawMessage, cwd string, opts Options) Result {
	if toolName == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{}
		}
		return BashWith(in.Command, cwd, opts)
	}
	if !strings.HasPrefix(toolName, "mcp__") {
		return Result{}
	}
	server := serverOf(toolName)
	op := strings.ToLower(toolName[strings.LastIndex(toolName, "__")+2:])
	if !isGmailServer(server) || !sendOps[op] {
		return Result{}
	}
	var in map[string]any
	if err := json.Unmarshal(input, &in); err != nil {
		return problem("the email tool arguments are not a JSON object, so the hook cannot read them.")
	}
	return Gmail("mcp__"+server+"__"+op, in)
}

const plainEmail = "Send the approved text as plain `body` (and `subject`); the hook cannot check htmlBody."

// Gmail inspects the Gmail MCP tools that send or stage mail.
func Gmail(tool string, in map[string]any) Result {
	op := tool[strings.LastIndex(tool, "__")+2:]
	str := func(k string) string {
		s, _ := in[k].(string)
		return s
	}
	var r Result
	switch op {
	case "send_message", "create_draft", "update_draft":
		if op == "send_message" && str("draftId") != "" {
			// Sends a stored draft; its text was checked when the draft was written.
			return r
		}
		if str("htmlBody") != "" {
			return problem(plainEmail)
		}
		subject, body := str("subject"), str("body")
		switch {
		case subject != "" && body != "":
			r.add("email subject and body", subject+"\n\n"+body)
		case subject != "":
			r.add("email subject", subject)
		case body != "":
			r.add("email body", body)
		}
	case "reply":
		if str("htmlBody") != "" {
			return problem(plainEmail)
		}
		if b := str("body"); b != "" {
			r.add("email reply", b)
		}
	case "forward":
		if str("htmlBody") != "" {
			return problem(plainEmail)
		}
		if b := str("forwardText"); b != "" {
			r.add("email forward note", b)
		}
	}
	return r
}
