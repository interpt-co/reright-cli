package hookcheck

import (
	"encoding/json"
	"regexp"
	"strings"
)

// UnknownServer stands in for the server name when the agent does not give one.
const UnknownServer = "unknown"

var sendOps = map[string]bool{"send_message": true, "create_draft": true, "update_draft": true, "reply": true, "forward": true}

// SendLike reports whether a bare MCP tool name is one of the Gmail tools that send or stage mail.
func SendLike(op string) bool { return sendOps[strings.ToLower(op)] }

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

func isChromeServer(server string) bool {
	return server == "claude-in-chrome" || server == "claude_in_chrome"
}

// Inspect returns the human-facing text a Claude Code tool call would send.
func Inspect(toolName string, input json.RawMessage, cwd string) Result {
	if toolName == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{}
		}
		return Bash(in.Command, cwd)
	}
	if !strings.HasPrefix(toolName, "mcp__") {
		return Result{}
	}
	server := serverOf(toolName)
	unknown := server == "" || server == UnknownServer
	var r Result
	if isGmailServer(server) || (unknown && SendLike(toolName[strings.LastIndex(toolName, "__")+2:])) {
		var in map[string]any
		if err := json.Unmarshal(input, &in); err != nil {
			return problem("the email tool arguments are not a JSON object, so the hook cannot read them.")
		}
		op := strings.ToLower(toolName[strings.LastIndex(toolName, "__")+2:])
		r.merge(Gmail("mcp__"+server+"__"+op, in))
	}
	if isChromeServer(server) || unknown {
		var in any
		if err := json.Unmarshal(input, &in); err != nil {
			return problem("the browser tool arguments are not valid JSON, so the hook cannot read them.")
		}
		r.merge(Chrome(in))
	}
	return r
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

var (
	insertTextCall = regexp.MustCompile(`insertText['"]\s*,\s*(?:false|true|null|undefined|0|1)\s*,\s*`)
	closesCall     = regexp.MustCompile(`^\s*\)`)
	// Writes that fill a text field or the page without going through insertText.
	domWrite      = regexp.MustCompile(`\.(?:value|innerHTML|innerText|textContent|outerHTML)\s*=([^=]|$)`)
	endsStatement = regexp.MustCompile(`^\s*(?:;|,|\)|\n|$)`)
	sendsDirectly = regexp.MustCompile(`message_post|insertHTML`)
	// Network calls from the page, and the shapes that make them writes.
	httpCall  = regexp.MustCompile(`(?i)fetch\s*\(|XMLHttpRequest|axios|\$\.(?:post|ajax)\b|jQuery\.(?:post|ajax)\b`)
	httpWrite = regexp.MustCompile(`(?i)method['"]?\s*[:=]\s*['"]?(?:post|put|patch)|\.open\(\s*['"](?:post|put|patch)|\bbody\s*:|\.post\(|\.send\(\s*[^)\s]`)
	beacon    = regexp.MustCompile(`sendBeacon`)
)

const plainInsert = "Insert chatter text with document.execCommand('insertText', false, '<approved text>') and pass the text as a string literal."

// Chrome inspects browser tool input, including actions nested in a batch.
func Chrome(in any) Result {
	var r Result
	walkChrome(in, &r)
	return r
}

func walkChrome(v any, r *Result) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["text"].(string); ok {
			if t["action"] == "type" {
				typed(s, r)
			} else {
				r.merge(javascript(s))
			}
		}
		if s, ok := t["value"].(string); ok {
			typed(s, r)
		}
		for _, c := range t {
			walkChrome(c, r)
		}
	case []any:
		for _, c := range t {
			walkChrome(c, r)
		}
	}
}

// typed only counts text that reads like prose: search terms and form codes pass.
func typed(s string, r *Result) {
	if strings.Contains(s, "\n") || len(strings.Fields(s)) >= 12 {
		r.add("browser typing", s)
	}
}

// javascript checks a script for the ways it can put text on a page: the
// insertText command, direct writes to a field or the DOM, and RPC posts.
func javascript(js string) Result {
	var r Result
	if sendsDirectly.MatchString(js) {
		return problem("javascript posts or inserts content directly (message_post, insertHTML). " + plainInsert)
	}
	if beacon.MatchString(js) || (httpCall.MatchString(js) && httpWrite.MatchString(js)) {
		return problem("javascript sends a network write (fetch, XMLHttpRequest or sendBeacon with a body or a POST). " + plainInsert)
	}
	if strings.Contains(js, "insertText") {
		locs := insertTextCall.FindAllStringIndex(js, -1)
		if len(locs) == 0 {
			return problem("javascript uses insertText in a form the hook cannot read. " + plainInsert)
		}
		for _, loc := range locs {
			v, n, ok := parseJSString(js[loc[1]:])
			if !ok || !closesCall.MatchString(js[loc[1]+n:]) {
				return problem("the insertText argument is not a single string literal. " + plainInsert)
			}
			r.add("browser insertText", v)
		}
	}
	for _, m := range domWrite.FindAllStringSubmatchIndex(js, -1) {
		rhs := strings.TrimLeft(js[m[2]:], " \t")
		v, n, ok := parseJSString(rhs)
		if !ok || !endsStatement.MatchString(rhs[n:]) {
			return problem("javascript writes a field or the page from something other than a single string literal. " + plainInsert)
		}
		typed(v, &r)
	}
	return r
}
