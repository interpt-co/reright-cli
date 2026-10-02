package hookcheck

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const stateFile = "enforcement.json"

var disableOrEnable = regexp.MustCompile(`(?i)\b(disable|enable)\b`)

// SwitchCommand reports whether a shell command would change reright's on/off switches: it runs
// `reright disable` or `reright enable`, or it names the state file as a path. Only the user may do
// that, so the hook refuses it when an agent tries. Text that merely mentions those words, in a
// heredoc, an echo or a commit message, is not a change and does not count.
func SwitchCommand(command string) bool { return switchAt(command, 0) }

func switchAt(command string, depth int) bool {
	if depth > maxDepth {
		return strings.Contains(command, "reright")
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// Cannot read it, so fall back to the plain words.
		return strings.Contains(command, stateFile) || (strings.Contains(command, "reright") && disableOrEnable.MatchString(command))
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *syntax.Stmt:
			for _, r := range n.Redirs {
				if r.Word != nil && strings.Contains(nodeText(command, r.Word), stateFile) {
					found = true
				}
			}
		case *syntax.CallExpr:
			if len(n.Args) > 0 && switchCall(command, n, depth) {
				found = true
			}
		}
		return !found
	})
	return found
}

func switchCall(command string, call *syntax.CallExpr, depth int) bool {
	args := make([]arg, len(call.Args))
	texts := make([]string, len(call.Args))
	for i, w := range call.Args {
		args[i].s, args[i].ok = wordValue(w)
		texts[i] = nodeText(command, w)
	}
	// strip wrappers such as sudo and env, keeping the texts in step
	for len(args) > 0 && args[0].ok {
		name := path.Base(args[0].s)
		if _, wrapper := wrapperFlagsWithValue[name]; !wrapper {
			break
		}
		rest := stripWrapper(name, args[1:])
		cut := len(args) - len(rest)
		args, texts = args[cut:], texts[cut:]
	}
	if len(args) == 0 {
		return false
	}
	name := ""
	if args[0].ok {
		name = path.Base(args[0].s)
	}
	pathLike := func(i int) bool {
		t := texts[i]
		return strings.Contains(t, stateFile) && !strings.ContainsAny(strings.Trim(t, `"'`), " \t\n")
	}
	switch {
	case name == "":
		// the command itself is built from something the hook cannot read
		for _, a := range args[1:] {
			if a.ok && disableOrEnable.MatchString(a.s) && (a.s == "disable" || a.s == "enable") {
				return true
			}
		}
	case name == "reright":
		for _, a := range args[1:] {
			if a.ok && strings.HasPrefix(a.s, "-") {
				continue
			}
			return !a.ok || a.s == "disable" || a.s == "enable"
		}
		return false
	case shells[name]:
		for i := 1; i < len(args)-1; i++ {
			if args[i].ok && (args[i].s == "-c" || inlineFlag(args[i].s, "c")) {
				if args[i+1].ok {
					return switchAt(args[i+1].s, depth+1)
				}
				return strings.Contains(texts[i+1], "reright") || strings.Contains(texts[i+1], stateFile)
			}
		}
	case name == "eval":
		parts := make([]string, 0, len(args)-1)
		for _, a := range args[1:] {
			if !a.ok {
				return strings.Contains(strings.Join(texts[1:], " "), "reright")
			}
			parts = append(parts, a.s)
		}
		return switchAt(strings.Join(parts, " "), depth+1)
	case interpreterRe.MatchString(name):
		for i, a := range args[1:] {
			if a.ok && (inlineFlag(a.s, "ceErp") || a.s == "eval") && i+2 < len(args) {
				code := texts[i+2]
				if args[i+2].ok {
					code = args[i+2].s
				}
				if strings.Contains(code, stateFile) || (strings.Contains(code, "reright") && disableOrEnable.MatchString(code)) {
					return true
				}
			}
		}
	}
	for i := 1; i < len(args); i++ {
		if pathLike(i) {
			return true
		}
	}
	return false
}
