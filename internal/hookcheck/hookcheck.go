// Package hookcheck finds the human-facing text inside a Claude Code tool call,
// so the hook can check it against what the reviewer approved.
package hookcheck

import (
	"os"
	"path/filepath"
)

// Text is one piece of outgoing text found in a tool call.
type Text struct {
	Source string
	Value  string
	// Dir is the directory a git commit, tag or merge runs in. It is empty for other texts.
	Dir string
}

// Result lists the texts a tool call would send. Problem is set when the call
// sends text the hook cannot read reliably; such a call must be denied.
type Result struct {
	Texts   []Text
	Problem string
}

func (r *Result) add(source, value string) {
	r.Texts = append(r.Texts, Text{Source: source, Value: value})
}

func (r *Result) fail(msg string) {
	if r.Problem == "" {
		r.Problem = msg
	}
}

func (r *Result) merge(o Result) {
	r.Texts = append(r.Texts, o.Texts...)
	if o.Problem != "" {
		r.fail(o.Problem)
	}
}

func problem(msg string) Result { return Result{Problem: msg} }

// Options change what Inspect looks for.
type Options struct {
	// OwnRepo reports whether only the user reads the commit messages of the repository at dir. Commits,
	// tags and merges there are let through without a look at their text. Nil means every repository is
	// shared. It is asked only when the hook can tell for sure which repository a call runs in.
	OwnRepo func(dir string) bool
}

func readRelative(dir, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	return string(b), err
}
