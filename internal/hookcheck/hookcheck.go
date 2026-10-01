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

func readRelative(dir, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	return string(b), err
}
