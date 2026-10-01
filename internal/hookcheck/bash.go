package hookcheck

import (
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

var mentionsGitOrGh = regexp.MustCompile(`(^|[\s;&|(/])(git|gh|curl|wget|mail|mailx|sendmail|mutt|msmtp|swaks)\s`)

const plainArgs = "Pass the approved text as a plain quoted argument (-m '...', --body '...'), a quoted heredoc (\"$(cat <<'EOF' ... EOF)\") or a file."

// shell carries what a git or gh call needs to know about the command line around it.
type shell struct {
	cwd string
	// compound is true when the line runs more than one command or redirects
	// output. A message file may then be written after the hook reads it.
	compound bool
	raw      string
	depth    int
	hint     bool
}

// readFile returns the contents of a message file, or a problem when the hook
// cannot be sure the file will still hold that text when the command runs.
func (sh shell) readFile(what, name string) (string, *Result) {
	if name == "-" {
		r := problem(what + " is read from stdin, which the hook cannot see. " + plainArgs)
		return "", &r
	}
	if sh.compound {
		r := problem(what + " comes from a file on a command line that also runs other commands or redirects output, so the hook cannot tell what the file will hold. Write the file first, then run this command on its own.")
		return "", &r
	}
	body, err := readRelative(sh.cwd, name)
	if err != nil {
		r := problem("cannot read " + what + ": " + err.Error())
		return "", &r
	}
	return body, nil
}

// Bash inspects a shell command for git commits and gh calls that carry text.
func Bash(command, cwd string) Result { return bashAt(command, cwd, 0) }

func bashAt(command, cwd string, depth int) Result {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		if mentionsGitOrGh.MatchString(command) {
			return problem("could not parse the shell command (" + err.Error() + ").")
		}
		return Result{}
	}
	sh := shell{cwd: cwd, compound: true, raw: command, depth: depth}
	if len(file.Stmts) == 1 && len(file.Stmts[0].Redirs) == 0 {
		_, single := file.Stmts[0].Cmd.(*syntax.CallExpr)
		sh.compound = !single
	}
	syntax.Walk(file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Value != nil && sensitiveWord.MatchString(nodeText(command, n.Value)) {
				sh.hint = true
			}
		case *syntax.ForClause:
			if wi, ok := n.Loop.(*syntax.WordIter); ok {
				for _, w := range wi.Items {
					if sensitiveWord.MatchString(nodeText(command, w)) {
						sh.hint = true
					}
				}
			}
		}
		return true
	})
	// A call that reads a heredoc is judged on its own text plus the heredoc body, never on the rest of the script.
	withInput := map[*syntax.CallExpr]string{}
	syntax.Walk(file, func(n syntax.Node) bool {
		if st, ok := n.(*syntax.Stmt); ok {
			if call, ok := st.Cmd.(*syntax.CallExpr); ok && len(st.Redirs) > 0 {
				withInput[call] = nodeText(command, st)
			}
		}
		return true
	})
	var res Result
	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		args := make([]arg, len(call.Args))
		for i, w := range call.Args {
			args[i].s, args[i].ok = wordValue(w)
			args[i].prefix = literalPrefix(w)
		}
		text := nodeText(command, call)
		if t, ok := withInput[call]; ok {
			text = t
		}
		res.merge(sh.dispatch(args, text))
		return true
	})
	return res
}

func nodeText(src string, n syntax.Node) string {
	start, end := int(n.Pos().Offset()), int(n.End().Offset())
	if start < 0 || end > len(src) || start > end {
		return src
	}
	return src[start:end]
}

// arg is a shell word. ok is false when its value depends on expansions the
// hook cannot evaluate (variables, command output, globs); prefix then holds
// the literal text before the first expansion, such as "--body=".
type arg struct {
	s      string
	ok     bool
	prefix string
}

func literalPrefix(w *syntax.Word) string {
	var sb strings.Builder
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			sb.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			sb.WriteString(p.Value)
		default:
			return sb.String()
		}
	}
	return sb.String()
}

func wordValue(w *syntax.Word) (string, bool) {
	var sb strings.Builder
	for _, p := range w.Parts {
		v, ok := partValue(p)
		if !ok {
			return "", false
		}
		sb.WriteString(v)
	}
	return sb.String(), true
}

func literal(parts ...syntax.WordPart) (string, bool) {
	v, err := expand.Literal(nil, &syntax.Word{Parts: parts})
	return v, err == nil
}

func partValue(p syntax.WordPart) (string, bool) {
	switch p := p.(type) {
	case *syntax.Lit:
		return unescapeUnquoted(p.Value), true
	case *syntax.SglQuoted:
		return literal(p)
	case *syntax.DblQuoted:
		if p.Dollar {
			return "", false
		}
		var sb strings.Builder
		for _, q := range p.Parts {
			var v string
			var ok bool
			switch q := q.(type) {
			case *syntax.Lit:
				v, ok = literal(&syntax.DblQuoted{Parts: []syntax.WordPart{q}})
			case *syntax.CmdSubst:
				v, ok = heredocCat(q)
			}
			if !ok {
				return "", false
			}
			sb.WriteString(v)
		}
		return sb.String(), true
	case *syntax.CmdSubst:
		return heredocCat(p)
	}
	return "", false
}

// unescapeUnquoted applies shell backslash rules outside quotes: a backslash
// keeps the next character literally, and backslash-newline joins lines.
func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] != '\n' {
				sb.WriteByte(s[i])
			}
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// heredocCat evaluates $(cat <<'EOF' ... EOF), the form Claude Code uses for
// multi-line commit messages. Anything else inside $() is not readable.
func heredocCat(cs *syntax.CmdSubst) (string, bool) {
	if len(cs.Stmts) != 1 {
		return "", false
	}
	st := cs.Stmts[0]
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 || len(st.Redirs) != 1 {
		return "", false
	}
	if name, ok := wordValue(call.Args[0]); !ok || name != "cat" {
		return "", false
	}
	r := st.Redirs[0]
	if r.Op != syntax.Hdoc {
		return "", false
	}
	body, ok := heredocBody(r)
	if !ok {
		return "", false
	}
	// Command substitution drops trailing newlines.
	return strings.TrimRight(body, "\n"), true
}

func heredocBody(r *syntax.Redirect) (string, bool) {
	if r.Hdoc == nil {
		return "", true
	}
	quoted := false
	for _, p := range r.Word.Parts {
		switch p := p.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				quoted = true
			}
		}
	}
	var sb strings.Builder
	for _, p := range r.Hdoc.Parts {
		lit, ok := p.(*syntax.Lit)
		if !ok {
			return "", false
		}
		sb.WriteString(lit.Value)
	}
	if quoted {
		return sb.String(), true
	}
	v, err := expand.Document(nil, r.Hdoc)
	return v, err == nil
}

func unreadable(what string) Result {
	return problem(what + " is built from a variable or command output the hook cannot read. " + plainArgs)
}

func gitCall(args []arg, sh shell) Result {
	dir := sh.cwd
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			if hasLiteral(args[i:], "commit") {
				return problem("git has an argument the hook cannot read before the subcommand.")
			}
			return Result{}
		}
		switch {
		case a.s == "-C" && i+1 < len(args):
			i++
			if !args[i].ok {
				return problem("git -C with a directory the hook cannot read.")
			}
			if filepath.IsAbs(args[i].s) {
				dir = args[i].s
			} else {
				dir = filepath.Join(dir, args[i].s)
			}
			continue
		case a.s == "-c" && i+1 < len(args):
			i++
			if !args[i].ok || isAliasConfig(args[i].s) {
				return problem("git -c defines or reads an alias, which can run a commit the hook cannot read. Run git commit directly with the approved text.")
			}
			continue
		case strings.HasPrefix(a.s, "-c") && isAliasConfig(a.s[2:]), strings.HasPrefix(a.s, "--config-env=") && isAliasConfig(strings.TrimPrefix(a.s, "--config-env=")):
			return problem("git -c defines or reads an alias, which can run a commit the hook cannot read. Run git commit directly with the approved text.")
		case strings.HasPrefix(a.s, "-"):
			continue
		}
		break
	}
	if i >= len(args) {
		return Result{}
	}
	sh.cwd = dir
	switch args[i].s {
	case "commit":
		return gitCommit(args[i+1:], sh)
	case "tag", "merge":
		return gitMessage(args[i].s, args[i+1:], sh)
	}
	return Result{}
}

func isAliasConfig(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "alias.")
}

var commitTextOptions = []string{"--message", "--file", "--template", "--reuse-message", "--reedit-message", "--fixup", "--squash"}

var tagMergeTextOptions = []string{"--message", "--file"}

func canonicalOption(s string, known []string) (string, bool) {
	if !strings.HasPrefix(s, "--") || len(s) < 3 {
		return s, true
	}
	name, val, hasVal := strings.Cut(s, "=")
	var match []string
	for _, k := range known {
		if k == name {
			return s, true
		}
		if strings.HasPrefix(k, name) {
			match = append(match, k)
		}
	}
	switch len(match) {
	case 0:
		return s, true
	case 1:
		if hasVal {
			return match[0] + "=" + val, true
		}
		return match[0], true
	}
	return s, false
}

func hasLiteral(args []arg, s string) bool {
	for _, a := range args {
		if a.ok && a.s == s {
			return true
		}
	}
	return false
}

func gitCommit(args []arg, sh shell) Result {
	var msgs []string
	var file string
	hasFile, reuse, noEdit := false, false, false
	for j := 0; j < len(args); j++ {
		a := args[j]
		next := func() (arg, bool) {
			if j+1 >= len(args) {
				return arg{}, false
			}
			j++
			return args[j], true
		}
		if !a.ok {
			return unreadable("A git commit argument")
		}
		s, unique := canonicalOption(a.s, commitTextOptions)
		if !unique {
			return problem("a git commit option abbreviation could stand for a message option. Spell the option out. " + plainArgs)
		}
		if s == "--" {
			break
		}
		switch {
		case s == "-m" || s == "--message":
			v, ok := next()
			if !ok || !v.ok {
				return unreadable("The commit message")
			}
			msgs = append(msgs, v.s)
		case strings.HasPrefix(s, "--message="):
			msgs = append(msgs, strings.TrimPrefix(s, "--message="))
		case s == "-F" || s == "--file":
			v, ok := next()
			if !ok || !v.ok {
				return unreadable("The commit message file name")
			}
			file, hasFile = v.s, true
		case strings.HasPrefix(s, "--file="):
			file, hasFile = strings.TrimPrefix(s, "--file="), true
		case s == "--reuse-message" || s == "--reedit-message" || s == "--fixup" || s == "--squash":
			next()
			reuse = true
		case strings.HasPrefix(s, "--reuse-message=") || strings.HasPrefix(s, "--reedit-message=") ||
			strings.HasPrefix(s, "--fixup=") || strings.HasPrefix(s, "--squash="):
			reuse = true
		case s == "--no-edit":
			noEdit = true
		case s == "-t" || s == "--template":
			next()
		case len(s) > 1 && s[0] == '-' && s[1] != '-':
		cluster:
			for k := 1; k < len(s); k++ {
				c := s[k]
				if !strings.ContainsRune("mFCct", rune(c)) {
					continue
				}
				v := arg{s: s[k+1:], ok: true}
				if v.s == "" {
					var ok bool
					if v, ok = next(); !ok || !v.ok {
						return unreadable("A git commit option value")
					}
				}
				switch c {
				case 'm':
					msgs = append(msgs, v.s)
				case 'F':
					file, hasFile = v.s, true
				case 'C', 'c':
					reuse = true
				}
				break cluster
			}
		}
	}
	switch {
	case len(msgs) > 0:
		var r Result
		r.add("git commit message", strings.Join(msgs, "\n\n"))
		return r
	case hasFile:
		body, bad := sh.readFile("the commit message", file)
		if bad != nil {
			return *bad
		}
		var r Result
		r.add("git commit message", body)
		return r
	case reuse || noEdit:
		return Result{}
	}
	return problem("git commit without -m or -F opens an editor. " + plainArgs)
}

func gitMessage(sub string, args []arg, sh shell) Result {
	var msgs []string
	var file string
	hasFile, editorTag, hasMsgFlag := false, false, false
	for j := 0; j < len(args); j++ {
		a := args[j]
		if !a.ok {
			if sub == "tag" || hasLiteral(args, "-m") {
				return unreadable("A git " + sub + " argument")
			}
			continue
		}
		s, unique := canonicalOption(a.s, tagMergeTextOptions)
		if !unique {
			return problem("a git " + sub + " option abbreviation could stand for a message option. Spell the option out. " + plainArgs)
		}
		next := func() (arg, bool) {
			if j+1 >= len(args) {
				return arg{}, false
			}
			j++
			return args[j], true
		}
		switch {
		case s == "--":
			j = len(args)
		case s == "--message" || s == "--file":
			v, ok := next()
			if !ok || !v.ok {
				return unreadable("The git " + sub + " message")
			}
			hasMsgFlag = true
			if s == "--message" {
				msgs = append(msgs, v.s)
			} else {
				file, hasFile = v.s, true
			}
		case strings.HasPrefix(s, "--message="):
			hasMsgFlag = true
			msgs = append(msgs, strings.TrimPrefix(s, "--message="))
		case strings.HasPrefix(s, "--file="):
			hasMsgFlag = true
			file, hasFile = strings.TrimPrefix(s, "--file="), true
		case s == "--annotate" || s == "--sign" || s == "--edit":
			editorTag = true
		case len(s) > 1 && s[0] == '-' && s[1] != '-':
		cluster:
			for k := 1; k < len(s); k++ {
				switch s[k] {
				case 'a', 's', 'e':
					editorTag = true
				case 'm', 'F':
					hasMsgFlag = true
					v := arg{s: s[k+1:], ok: true}
					if v.s == "" {
						var ok bool
						if v, ok = next(); !ok || !v.ok {
							return unreadable("A git " + sub + " option value")
						}
					}
					if s[k] == 'm' {
						msgs = append(msgs, v.s)
					} else {
						file, hasFile = v.s, true
					}
					break cluster
				case 'u', 'f', 'd', 'l', 'n', 'v':
				}
			}
		}
	}
	source := "git " + sub + " message"
	var r Result
	switch {
	case len(msgs) > 0:
		r.add(source, strings.Join(msgs, "\n\n"))
	case hasFile:
		body, bad := sh.readFile("the "+sub+" message", file)
		if bad != nil {
			return *bad
		}
		r.add(source, body)
	case sub == "tag" && editorTag && !hasMsgFlag:
		return problem("git tag -a without -m or -F opens an editor. " + plainArgs)
	}
	return r
}

// Flag names of gh subcommands that carry text, mapped to what they hold.
var (
	ghCommon  = map[string]string{"-t": "title", "--title": "title", "-b": "body", "--body": "body", "-F": "file", "--body-file": "file"}
	ghClose   = map[string]string{"-c": "body", "--comment": "body"}
	ghMerge   = map[string]string{"-t": "title", "--subject": "title", "-b": "body", "--body": "body", "-F": "file", "--body-file": "file"}
	ghRelease = map[string]string{"-t": "title", "--title": "title", "-n": "body", "--notes": "body", "-F": "file", "--notes-file": "file"}

	ghTextCommands = map[string]map[string]map[string]string{
		"pr":      {"create": ghCommon, "edit": ghCommon, "comment": ghCommon, "review": ghCommon, "close": ghClose, "merge": ghMerge},
		"issue":   {"create": ghCommon, "edit": ghCommon, "comment": ghCommon, "close": ghClose},
		"release": {"create": ghRelease, "edit": ghRelease},
	}
)

func ghCall(args []arg, sh shell) Result {
	var pos []string
	i := 0
	for ; i < len(args) && len(pos) < 2; i++ {
		a := args[i]
		if !a.ok {
			return Result{}
		}
		switch {
		case a.s == "-R" || a.s == "--repo":
			i++
		case strings.HasPrefix(a.s, "-"):
		default:
			pos = append(pos, a.s)
			if a.s == "api" {
				return ghAPI(args[i+1:], sh)
			}
		}
	}
	if len(pos) < 2 {
		return Result{}
	}
	flags := ghTextCommands[pos[0]][pos[1]]
	if flags == nil {
		return Result{}
	}
	return ghText(pos[0], pos[1], flags, args[i:], sh)
}

// flagFor matches a gh argument against the text flags, in the forms
// --flag value, --flag=value, -f value and -fvalue.
func flagFor(flags map[string]string, s string) (kind, value string, attached bool) {
	if name, val, found := strings.Cut(s, "="); found && strings.HasPrefix(name, "--") {
		if k := flags[name]; k != "" {
			return k, val, true
		}
		return "", "", false
	}
	if k := flags[s]; k != "" {
		return k, "", false
	}
	if len(s) > 2 && s[0] == '-' && s[1] != '-' {
		if k := flags[s[:2]]; k != "" {
			return k, s[2:], true
		}
	}
	return "", "", false
}

func ghText(group, sub string, flags map[string]string, args []arg, sh shell) Result {
	var title, body *string
	for j := 0; j < len(args); j++ {
		a := args[j]
		if !a.ok {
			// "--body=$(...)" and friends: the flag is readable, its value is not.
			if kind, _, attached := flagFor(flags, a.prefix); kind != "" && (attached || a.prefix != "") {
				return unreadable("The gh " + kind)
			}
			continue
		}
		kind, v, attached := flagFor(flags, a.s)
		if kind == "" {
			continue
		}
		if !attached {
			if j+1 >= len(args) || !args[j+1].ok {
				return unreadable("The gh " + kind)
			}
			j++
			v = args[j].s
		}
		switch kind {
		case "title":
			title = &v
		case "body":
			body = &v
		case "file":
			content, bad := sh.readFile("the gh body", v)
			if bad != nil {
				return *bad
			}
			body = &content
		}
	}
	var r Result
	source := "gh " + group + " " + sub
	if sub == "create" && group != "release" && title != nil && body != nil {
		r.add(source+" title and body", *title+"\n\n"+*body)
		return r
	}
	if title != nil {
		r.add(source+" title", *title)
	}
	if body != nil {
		r.add(source+" body", *body)
	}
	return r
}

var ghAPITextKeys = map[string]bool{"body": true, "title": true, "message": true, "notes": true}

// apiTextKey reports whether a gh api field key holds prose: body, or a nested
// form like comments[][body].
func apiTextKey(key string) bool {
	key = strings.TrimSuffix(key, "[]")
	if i := strings.LastIndex(key, "["); i >= 0 && strings.HasSuffix(key, "]") {
		key = key[i+1 : len(key)-1]
	}
	return ghAPITextKeys[key]
}

func ghAPI(args []arg, sh shell) Result {
	method := ""
	input := false
	var r Result
	for j := 0; j < len(args); j++ {
		a := args[j]
		if !a.ok {
			p := a.prefix
			if strings.HasPrefix(p, "--raw-field=") || strings.HasPrefix(p, "--field=") ||
				(len(p) > 2 && (strings.HasPrefix(p, "-f") || strings.HasPrefix(p, "-F")) && p[1] != '-') {
				return problem("gh api has a field the hook cannot read. Use -f body='...' with the approved text.")
			}
			continue
		}
		s := a.s
		next := func() (arg, bool) {
			if j+1 >= len(args) {
				return arg{}, false
			}
			j++
			return args[j], true
		}
		var field string
		typed := false
		switch {
		case s == "-X" || s == "--method":
			v, _ := next()
			method = strings.ToUpper(v.s)
			continue
		case strings.HasPrefix(s, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(s, "--method="))
			continue
		case s == "--input" || strings.HasPrefix(s, "--input="):
			input = true
			if s == "--input" {
				next()
			}
			continue
		case s == "-f" || s == "--raw-field" || s == "-F" || s == "--field":
			typed = s == "-F" || s == "--field"
			v, ok := next()
			if !ok || !v.ok {
				return problem("gh api has a field the hook cannot read. Use -f body='...' with the approved text.")
			}
			field = v.s
		case strings.HasPrefix(s, "--raw-field=") || strings.HasPrefix(s, "--field="):
			typed = strings.HasPrefix(s, "--field=")
			_, field, _ = strings.Cut(s, "=")
		case len(s) > 2 && (strings.HasPrefix(s, "-f") || strings.HasPrefix(s, "-F")) && s[1] != '-':
			typed = s[1] == 'F'
			field = s[2:]
		default:
			continue
		}
		key, val, _ := strings.Cut(field, "=")
		if key == "query" && strings.Contains(val, "mutation") {
			r.fail("gh api graphql mutations can carry text the hook cannot pick out. Use the REST form (gh pr comment, gh api -f body='...') for anything a person will read.")
			continue
		}
		if !apiTextKey(key) {
			continue
		}
		if typed && strings.HasPrefix(val, "@") {
			content, bad := sh.readFile("the gh api "+key, strings.TrimPrefix(val, "@"))
			if bad != nil {
				return *bad
			}
			val = content
		}
		r.add("gh api "+key, val)
	}
	if input && method != "GET" {
		return problem("gh api --input sends a JSON file the hook cannot check. Use -f body='...' and -f title='...' instead.")
	}
	return r
}
