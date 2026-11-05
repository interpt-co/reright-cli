package hookcheck

import (
	"path"
	"regexp"
	"strings"
)

// sensitiveWord finds the commands whose text the hook checks.
var sensitiveWord = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_.-])(git|gh)([^A-Za-z0-9_-]|$)`)

// wrapperFlagsWithValue lists the simple wrappers that run the command after them, with the flags that take
// a value. The hook looks through them, so `sudo git commit -m x` is checked like `git commit -m x`.
var wrapperFlagsWithValue = map[string]map[string]bool{
	"sudo":     {"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-r": true, "-t": true, "-T": true, "-U": true, "-D": true},
	"doas":     {"-u": true, "-C": true},
	"env":      {"-u": true, "-C": true, "-S": true, "--unset": true, "--chdir": true},
	"nice":     {"-n": true},
	"ionice":   {"-c": true, "-n": true, "-p": true},
	"stdbuf":   {"-i": true, "-o": true, "-e": true},
	"timeout":  {"-k": true, "-s": true},
	"command":  {},
	"builtin":  {},
	"exec":     {"-a": true},
	"nohup":    {},
	"setsid":   {},
	"time":     {"-f": true, "-o": true},
	"unbuffer": {},
}

func stripWrapper(name string, args []arg) []arg {
	valued := wrapperFlagsWithValue[name]
	i := 0
	positional := 0
	if name == "timeout" {
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

// dispatch checks one simple command. Only git and gh are read, directly or behind the wrappers above.
// Anything else, including a command whose name the hook cannot read, is let through.
func (sh shell) dispatch(args []arg) Result {
	for depth := 0; len(args) > 0 && args[0].ok; depth++ {
		name := path.Base(args[0].s)
		switch {
		case name == "git":
			return gitCall(args[1:], sh)
		case name == "gh":
			return ghCall(args[1:], sh)
		}
		if _, ok := wrapperFlagsWithValue[name]; !ok || depth >= maxDepth {
			break
		}
		args = stripWrapper(name, args[1:])
	}
	return Result{}
}
