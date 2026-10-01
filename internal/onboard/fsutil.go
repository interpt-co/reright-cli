package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func resolveTarget(path string) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return path, false
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, true
	}
	cur := path
	for i := 0; i < 16; i++ {
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			break
		}
		next, err := os.Readlink(cur)
		if err != nil {
			break
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(cur), next)
		}
		cur = next
	}
	return cur, true
}

func nearestDir(path string) string {
	dir := filepath.Dir(path)
	for i := 0; i < 64; i++ {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir
}

func dirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".reright-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

type writePlan struct {
	Dest      string
	Link      string
	BreakLink bool
}

func planWrite(path string, force bool) (writePlan, error) {
	real, isLink := resolveTarget(path)
	plan := writePlan{Dest: real}
	if isLink {
		plan.Link, _ = os.Readlink(path)
	}
	if fi, err := os.Stat(real); err == nil {
		if fi.IsDir() {
			return plan, fmt.Errorf("%s is a directory", real)
		}
		if fi.Mode().Perm()&0o200 == 0 && !force {
			return plan, fmt.Errorf("%s is read-only (mode %04o), so reright will not replace it. Make it writable, or run again with --force to replace it anyway", real, fi.Mode().Perm())
		}
		if fi.Mode().Perm()&0o200 != 0 {
			f, err := os.OpenFile(real, os.O_WRONLY, 0)
			if err != nil {
				return plan, fmt.Errorf("%s cannot be written: %w", real, err)
			}
			f.Close()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return plan, err
	}
	if err := dirWritable(nearestDir(real)); err != nil {
		if isLink && force {
			if err2 := dirWritable(nearestDir(path)); err2 != nil {
				return plan, fmt.Errorf("neither %s nor the folder of the link %s is writable: %w", real, path, err2)
			}
			plan.Dest, plan.BreakLink = path, true
			return plan, nil
		}
		hint := "Fix its permissions"
		if isLink {
			hint = "It is a link to a read-only location (home-manager and nix do this). Edit the source it is generated from, or run again with --force to replace the link with a regular file"
		}
		return plan, fmt.Errorf("%s cannot be written because its folder is not writable (%v). %s", real, err, hint)
	}
	return plan, nil
}

func stripJSONC(raw []byte) []byte {
	var out []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			out = append(out, c)
			for i++; i < len(raw); i++ {
				out = append(out, raw[i])
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
					out = append(out, raw[i])
					continue
				}
				if raw[i] == '"' {
					break
				}
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	var res []byte
	for i := 0; i < len(out); i++ {
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, out[i])
	}
	return res
}

func isJSONC(raw []byte) bool {
	if json.Valid(raw) {
		return false
	}
	return json.Valid(stripJSONC(raw))
}
