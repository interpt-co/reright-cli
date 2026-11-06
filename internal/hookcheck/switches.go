package hookcheck

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const stateFile = "enforcement.json"

var disableOrEnable = regexp.MustCompile(`(?i)\b(disable|enable)\b`)

// gitKeyRe finds the git config keys that decide which repositories the hook checks.
var gitKeyRe = regexp.MustCompile(`(?i)\breright\.(commits|email)\b`)

// gitConfigReads are the git config options and subcommands that only read.
var gitConfigReads = map[string]bool{"--get": true, "--get-all": true, "--get-regexp": true, "--get-urlmatch": true, "--list": true, "-l": true, "get": true, "list": true}

// The switch guard still looks inside bash -c, eval and inline interpreter code, because an agent that
// turns the checks off that way would get past every other check.
var (
	interpreterRe = regexp.MustCompile(`^(python[0-9.]*|node|nodejs|perl|ruby|php|deno|bun|lua|Rscript)$`)
	shells        = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true}
)

const maxDepth = 5

// guardWrappers are the commands the switch guard looks through. It is a longer list than the one the text
// checks use, because running reright disable through xargs or watch still turns the checks off.
var guardWrappers = map[string]map[string]bool{
	"xargs":   {"-I": true, "-n": true, "-P": true, "-d": true, "-L": true, "-E": true, "-s": true, "-a": true, "-i": false},
	"flock":   {"-w": true, "-E": true},
	"watch":   {"-n": true, "--interval": true},
	"busybox": {},
	"runuser": {"-u": true, "-g": true, "-G": true},
}

func init() {
	for name, flags := range wrapperFlagsWithValue {
		guardWrappers[name] = flags
	}
}

// stripGuardWrapper is stripWrapper over guardWrappers. timeout and flock take one positional argument
// (the duration, the lock file) before the command.
func stripGuardWrapper(name string, args []arg) []arg {
	valued := guardWrappers[name]
	i := 0
	positional := 0
	if name == "timeout" || name == "flock" {
		positional = 1
	}
	for i < len(args) {
		a := args[i]
		if !a.ok {
			break
		}
		switch {
		case a.s == "--":
			return args[i+1:]
		case strings.HasPrefix(a.s, "-") && len(a.s) > 1:
			if valued[a.s] {
				i++
			}
			i++
		case name == "env" && strings.Contains(a.s, "=") && !strings.HasPrefix(a.s, "="):
			i++
		case positional > 0:
			positional--
			i++
		default:
			return args[i:]
		}
	}
	if i > len(args) {
		i = len(args)
	}
	return args[i:]
}

func inlineFlag(s string, letters string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	if s[1] == '-' {
		return s == "--eval" || s == "--print" || s == "--command"
	}
	return strings.ContainsRune(letters, rune(s[len(s)-1]))
}

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
		return strings.Contains(command, stateFile) || (strings.Contains(command, "reright") && disableOrEnable.MatchString(command)) || gitKeyRe.MatchString(command)
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
		if _, wrapper := guardWrappers[name]; !wrapper {
			break
		}
		rest := stripGuardWrapper(name, args[1:])
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
	case name == "git":
		return gitConfigWrite(args[1:], texts[1:], command)
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
				if strings.Contains(code, stateFile) || (strings.Contains(code, "reright") && disableOrEnable.MatchString(code)) || gitKeyRe.MatchString(code) {
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

// gitConfigWrite reports whether a git call writes reright.commits or reright.email. Those keys decide
// whether commit messages are checked, so only the user sets them. Reading them is fine, and so is
// git -c reright.commits=..., because the hook reads the configuration with its own git call.
func gitConfigWrite(args []arg, texts []string, command string) bool {
	i := 0
	for ; i < len(args); i++ {
		if !args[i].ok {
			return false
		}
		s := args[i].s
		if (s == "-C" || s == "-c" || s == "--git-dir" || s == "--work-tree") && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(s, "-") {
			continue
		}
		break
	}
	if i >= len(args) || args[i].s != "config" {
		return false
	}
	key := false
	for j, a := range args[i+1:] {
		if !a.ok {
			// a key built from a variable counts when the line names one of the keys anywhere
			if gitKeyRe.MatchString(texts[i+1+j]) || gitKeyRe.MatchString(command) {
				key = true
			}
			continue
		}
		if gitConfigReads[a.s] {
			return false
		}
		if gitKeyRe.MatchString(a.s) {
			key = true
		}
	}
	return key
}
