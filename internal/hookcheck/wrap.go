package hookcheck

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	sensitiveWord = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_.-])(git|gh|curl|wget|mail|mailx|sendmail|mutt|msmtp|swaks|smtplib)([^A-Za-z0-9_-]|$)`)
	interpreterRe = regexp.MustCompile(`^(python[0-9.]*|node|nodejs|perl|ruby|php|deno|bun|lua|Rscript)$`)
	shells        = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true}
	mailers       = map[string]bool{"mail": true, "mailx": true, "sendmail": true, "mutt": true, "msmtp": true, "swaks": true}
	localHost     = regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*://)?(?:[^/@]*@)?(?:localhost|127\.[0-9.]+|\[?::1\]?|0\.0\.0\.0)(?::[0-9]+)?(?:[/?#]|$)`)
)

const maxDepth = 5

var wrapperFlagsWithValue = map[string]map[string]bool{
	"sudo":     {"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-r": true, "-t": true, "-T": true, "-U": true, "-D": true},
	"doas":     {"-u": true, "-C": true},
	"env":      {"-u": true, "-C": true, "-S": true, "--unset": true, "--chdir": true},
	"nice":     {"-n": true},
	"ionice":   {"-c": true, "-n": true, "-p": true},
	"stdbuf":   {"-i": true, "-o": true, "-e": true},
	"timeout":  {"-k": true, "-s": true},
	"xargs":    {"-I": true, "-n": true, "-P": true, "-d": true, "-L": true, "-E": true, "-s": true, "-a": true, "-i": false},
	"flock":    {"-w": true, "-E": true},
	"command":  {},
	"builtin":  {},
	"exec":     {"-a": true},
	"nohup":    {},
	"setsid":   {},
	"time":     {"-f": true, "-o": true},
	"unbuffer": {},
	"watch":    {"-n": true, "--interval": true},
	"busybox":  {},
	"runuser":  {"-u": true, "-g": true, "-G": true},
}

func stripWrapper(name string, args []arg) []arg {
	valued := wrapperFlagsWithValue[name]
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

func (sh shell) unreadableCommand(callText string) Result {
	if sh.hint || sensitiveWord.MatchString(callText) {
		return problem("a command name is built from a variable the hook cannot read, and the line mentions git, gh, curl or mail. Write the command out literally. " + plainArgs)
	}
	return Result{}
}

func (sh shell) dispatch(args []arg, callText string) Result {
	if len(args) == 0 {
		return Result{}
	}
	if !args[0].ok {
		return sh.unreadableCommand(callText)
	}
	name := path.Base(args[0].s)
	switch {
	case name == "git":
		return gitCall(args[1:], sh)
	case name == "gh":
		return ghCall(args[1:], sh)
	case name == "curl" || name == "wget":
		return httpTool(name, args[1:])
	case mailers[name]:
		return problem(name + " sends email, and the hook cannot check what it sends. Use the reviewed email path and send the approved text.")
	case name == "eval":
		return sh.eval(args[1:], callText)
	case name == "env":
		if r, ok := sh.envSplit(args[1:], callText); ok {
			return r
		}
	case name == "find":
		return sh.findExec(args[1:], callText)
	case name == "source" || name == ".":
		if len(args) > 1 && args[1].ok {
			return sh.script(args[1].s, runAsShell)
		}
		return Result{}
	}
	switch name {
	case "flock", "script", "su", "runuser":
		if r, ok := sh.commandFlag(args[1:], callText); ok {
			return r
		}
	}
	switch {
	case shells[name]:
		return sh.shellCall(args[1:])
	case interpreterRe.MatchString(name):
		return sh.interpreter(name, args[1:], callText)
	}
	if _, ok := wrapperFlagsWithValue[name]; ok {
		if sh.depth >= maxDepth {
			return problem("commands are nested too deeply for the hook to read.")
		}
		rest := stripWrapper(name, args[1:])
		if name == "xargs" {
			return sh.xargs(rest)
		}
		if name == "watch" {
			return sh.watch(rest, callText)
		}
		return sh.dispatch(rest, callText)
	}
	if strings.Contains(args[0].s, "/") {
		return sh.script(args[0].s, runAsExec)
	}
	return Result{}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (sh shell) watch(rest []arg, callText string) Result {
	parts := make([]string, len(rest))
	for i, a := range rest {
		if !a.ok {
			return sh.unreadableCommand(callText)
		}
		parts[i] = a.s
	}
	return sh.inner(strings.Join(parts, " "))
}

func (sh shell) envSplit(args []arg, callText string) (Result, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			return Result{}, false
		}
		var value string
		found := false
		switch {
		case a.s == "--":
			return Result{}, false
		case a.s == "--split-string":
			if i+1 >= len(args) {
				return Result{}, false
			}
			i++
			if !args[i].ok {
				return sh.unreadableCommand(callText), true
			}
			value, found = args[i].s, true
		case strings.HasPrefix(a.s, "--split-string="):
			value, found = strings.TrimPrefix(a.s, "--split-string="), true
		case strings.HasPrefix(a.s, "--"):
			if a.s == "--unset" || a.s == "--chdir" {
				i++
			}
			continue
		case strings.HasPrefix(a.s, "-") && len(a.s) > 1:
			for k := 1; k < len(a.s); k++ {
				c := a.s[k]
				if c == 'S' {
					value, found = a.s[k+1:], true
					if value == "" {
						if i+1 >= len(args) {
							return Result{}, false
						}
						i++
						if !args[i].ok {
							return sh.unreadableCommand(callText), true
						}
						value = args[i].s
					}
					break
				}
				if c == 'u' || c == 'C' {
					if k == len(a.s)-1 {
						i++
					}
					break
				}
			}
		case strings.Contains(a.s, "="):
			continue
		default:
			return Result{}, false
		}
		if !found {
			continue
		}
		for _, r := range args[i+1:] {
			if !r.ok {
				return sh.unreadableCommand(callText), true
			}
			value += " " + shellQuote(r.s)
		}
		return sh.inner(value), true
	}
	return Result{}, false
}

func (sh shell) commandFlag(args []arg, callText string) (Result, bool) {
	for i, a := range args {
		if !a.ok {
			continue
		}
		if v, ok := strings.CutPrefix(a.s, "--command="); ok {
			return sh.inner(v), true
		}
		if a.s == "--command" || (len(a.s) > 1 && a.s[0] == '-' && a.s[1] != '-' && strings.HasSuffix(a.s, "c")) {
			if i+1 >= len(args) {
				return Result{}, false
			}
			if !args[i+1].ok {
				return sh.unreadableEval(callText), true
			}
			return sh.inner(args[i+1].s), true
		}
	}
	return Result{}, false
}

func (sh shell) findExec(args []arg, callText string) Result {
	var res Result
	for i := 0; i < len(args); i++ {
		if !args[i].ok {
			continue
		}
		switch args[i].s {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && !(args[j].ok && (args[j].s == ";" || args[j].s == "+")) {
				j++
			}
			if sh.depth >= maxDepth {
				return problem("commands are nested too deeply for the hook to read.")
			}
			res.merge(sh.dispatch(args[i+1:j], callText))
			i = j
		}
	}
	return res
}

type scriptKind int

const (
	runAsExec scriptKind = iota
	runAsShell
	runAsInterpreter
)

const maxScriptBytes = 1 << 20

var shebangShell = regexp.MustCompile(`^#![ \t]*(\S*/)?(env[ \t]+)?(-\S+[ \t]+)*(bash|sh|zsh|dash|ksh)([ \t]|$)`)

func (sh shell) script(file string, kind scriptKind) Result {
	written := sh.compound && strings.Count(sh.raw, file) >= 2 && sensitiveWord.MatchString(sh.raw)
	if written {
		return problem(file + " is written and run in the same command, so the hook cannot read what it will run. Write the script first, then run it on its own.")
	}
	name := file
	if !filepath.IsAbs(name) {
		name = filepath.Join(sh.cwd, name)
	}
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxScriptBytes {
		return Result{}
	}
	b, err := os.ReadFile(name)
	if err != nil || strings.ContainsRune(string(b), 0) {
		return Result{}
	}
	body := string(b)
	first, _, _ := strings.Cut(body, "\n")
	switch {
	case shebangShell.MatchString(first), kind == runAsShell && !strings.HasPrefix(first, "#!"):
		return sh.inner(body)
	case (kind == runAsInterpreter || strings.HasPrefix(first, "#!")) && sensitiveWord.MatchString(body):
		return problem(file + " runs git, gh, curl or mail" + firstSensitiveLine(body) + ", and the hook cannot read the text inside a script. Run that command directly with the approved text.")
	}
	return Result{}
}

func (sh shell) xargs(rest []arg) Result {
	if len(rest) == 0 {
		return Result{}
	}
	if !rest[0].ok {
		return problem("xargs runs a command the hook cannot read.")
	}
	if inner := path.Base(rest[0].s); inner == "git" || inner == "gh" || inner == "curl" || inner == "wget" || mailers[inner] || shells[inner] || interpreterRe.MatchString(inner) {
		return problem("xargs feeds arguments to " + inner + " that the hook cannot read. Run " + inner + " directly with the approved text.")
	}
	return sh.dispatch(rest, "")
}

func (sh shell) inner(script string) Result {
	if sh.depth >= maxDepth {
		return problem("commands are nested too deeply for the hook to read.")
	}
	return bashAt(script, sh.cwd, sh.depth+1)
}

func (sh shell) eval(args []arg, callText string) Result {
	parts := make([]string, len(args))
	for i, a := range args {
		if !a.ok {
			return sh.unreadableEval(callText)
		}
		parts[i] = a.s
	}
	return sh.inner(strings.Join(parts, " "))
}

func (sh shell) unreadableEval(callText string) Result {
	if sh.hint || sensitiveWord.MatchString(callText) || sensitiveWord.MatchString(sh.raw) {
		return problem("eval runs a string the hook cannot read, and the line mentions git, gh, curl or mail. Write the command out literally. " + plainArgs)
	}
	return Result{}
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

func (sh shell) shellCall(args []arg) Result {
	for i, a := range args {
		if !a.ok {
			if sh.hint || sensitiveWord.MatchString(sh.raw) {
				return problem("a shell is started with an argument the hook cannot read, and the line mentions git, gh, curl or mail. " + plainArgs)
			}
			return Result{}
		}
		if inlineFlag(a.s, "c") {
			if i+1 >= len(args) {
				return Result{}
			}
			if !args[i+1].ok {
				return sh.unreadableEval(sh.raw)
			}
			return sh.inner(args[i+1].s)
		}
	}
	if f, ok := scriptArg(args, map[string]bool{"-o": true, "+o": true, "-O": true, "+O": true, "--rcfile": true, "--init-file": true}); ok {
		return sh.script(f, runAsShell)
	}
	return sh.readsStdin(args, "shell")
}

func scriptArg(args []arg, valued map[string]bool) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			return "", false
		}
		switch {
		case a.s == "--":
			if i+1 < len(args) && args[i+1].ok {
				return args[i+1].s, true
			}
			return "", false
		case a.s == "-" || a.s == "-s":
			return "", false
		case strings.HasPrefix(a.s, "-") || strings.HasPrefix(a.s, "+"):
			if valued[a.s] {
				i++
			}
		default:
			return a.s, true
		}
	}
	return "", false
}

func (sh shell) readsStdin(args []arg, what string) Result {
	for _, a := range args {
		if a.ok && a.s == "-s" || a.ok && a.s == "-" {
			return sh.stdinProblem(what)
		}
	}
	for _, a := range args {
		if !a.ok || !strings.HasPrefix(a.s, "-") {
			return Result{}
		}
	}
	return sh.stdinProblem(what)
}

func (sh shell) stdinProblem(what string) Result {
	if sensitiveWord.MatchString(sh.raw) {
		return problem("a " + what + " reads its commands from stdin and the line mentions git, gh, curl or mail, so the hook cannot read what it runs. " + plainArgs)
	}
	return Result{}
}

var interpreterValued = map[string]bool{"-m": true, "-W": true, "-X": true, "-r": true, "-I": true, "-C": true}

func (sh shell) interpreter(name string, args []arg, callText string) Result {
	text := callText
	if text == "" {
		text = sh.raw
	}
	for i, a := range args {
		if !a.ok {
			// An argument the hook cannot read only matters if the script it goes to can run git, gh, curl or mail.
			// When the script is a file the hook can read, read it instead of guessing from the line.
			if f, ok := scriptArg(args[:i], interpreterValued); ok && strings.ContainsAny(f, "./") {
				return sh.script(f, runAsInterpreter)
			}
			if sensitiveWord.MatchString(text) {
				return problem(name + " is run with an argument the hook cannot read (" + quoteLine(text) + "), and it cannot see inside the script to tell whether it runs git, gh, curl or mail. " + scriptAdvice)
			}
			return Result{}
		}
		if inlineFlag(a.s, "ceErp") || (name == "deno" && a.s == "eval") {
			if i+1 >= len(args) {
				return Result{}
			}
			code := args[i+1]
			if !code.ok || sensitiveWord.MatchString(code.s) {
				return problem(name + " code runs git, gh, curl or mail, which the hook cannot read inside a script. Run the command directly with the approved text.")
			}
			return Result{}
		}
	}
	if f, ok := scriptArg(args, interpreterValued); ok && strings.ContainsAny(f, "./") {
		return sh.script(f, runAsInterpreter)
	}
	return sh.readsStdin(args, name)
}

var (
	curlDataFlags = map[string]bool{"-d": true, "--data": true, "--data-raw": true, "--data-binary": true, "--data-urlencode": true, "--data-ascii": true, "--json": true, "-F": true, "--form": true, "--form-string": true, "-T": true, "--upload-file": true}
	wgetDataFlags = map[string]bool{"--post-data": true, "--post-file": true, "--body-data": true, "--body-file": true}
)

var httpValueFlags = map[string]bool{"-H": true, "--header": true, "-u": true, "--user": true, "-o": true, "--output": true, "-A": true, "--user-agent": true, "-e": true, "--referer": true, "-b": true, "--cookie": true, "-c": true, "--cookie-jar": true, "-w": true, "--write-out": true, "-m": true, "--max-time": true, "--connect-timeout": true, "-x": true, "--proxy": true, "-O": false, "--retry": true}

func httpTool(name string, args []arg) Result {
	sendsBody := false
	remote := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			if strings.HasPrefix(a.prefix, "-") {
				sendsBody = true
			}
			remote = true
			continue
		}
		s := a.s
		if key, _, found := strings.Cut(s, "="); found && strings.HasPrefix(s, "--") {
			s = key
		}
		shortData := len(a.s) > 2 && a.s[0] == '-' && a.s[1] != '-' && strings.ContainsRune("dFT", rune(a.s[1]))
		switch {
		case name == "curl" && curlDataFlags[s], name == "wget" && wgetDataFlags[s]:
			sendsBody = true
			if s == a.s {
				i++
			}
		case name == "curl" && shortData:
			sendsBody = true
		case a.s == "-X" || a.s == "--request" || strings.HasPrefix(a.s, "--request=") || (strings.HasPrefix(a.s, "-X") && len(a.s) > 2):
			if methodWrites(args, a) {
				sendsBody = true
			}
			if a.s == "-X" || a.s == "--request" {
				i++
			}
		case httpValueFlags[a.s]:
			i++
		case !strings.HasPrefix(a.s, "-") && !localHost.MatchString(a.s) && strings.Contains(a.s, "."):
			remote = true
		}
	}
	if sendsBody && remote {
		return problem(name + " sends a request body to a remote host, and the hook cannot tell whether it carries text a person will read. Use gh (gh pr comment --body '...') or the reviewed path for text, and keep " + name + " for requests without a body.")
	}
	return Result{}
}

func methodWrites(args []arg, a arg) bool {
	var v string
	switch {
	case strings.HasPrefix(a.s, "--request="):
		v = strings.TrimPrefix(a.s, "--request=")
	case len(a.s) > 2 && !strings.HasPrefix(a.s, "--"):
		v = a.s[2:]
	default:
		for i, x := range args {
			if x.ok && x.s == a.s && i+1 < len(args) {
				v = args[i+1].s
				break
			}
		}
	}
	switch strings.ToUpper(v) {
	case "POST", "PUT", "PATCH":
		return true
	}
	return false
}

const scriptAdvice = "Give the script a literal path the hook can open (not one built from a variable), or run the git, gh, curl or mail command directly with the approved text."

// quoteLine shortens a command line for an error message.
func quoteLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		s = s[:100] + "..."
	}
	return "`" + s + "`"
}

// firstSensitiveLine names the first line of a script that mentions git, gh, curl or mail.
func firstSensitiveLine(body string) string {
	for i, l := range strings.Split(body, "\n") {
		if sensitiveWord.MatchString(l) {
			return fmt.Sprintf(" (line %d: %s)", i+1, quoteLine(l))
		}
	}
	return ""
}
