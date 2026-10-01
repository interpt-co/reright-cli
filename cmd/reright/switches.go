package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/toggle"
)

const switchUsage = `       reright status
       reright disable KIND... [--for DURATION] [--here | --dir PATH]...
       reright enable KIND...

kinds: ` + "commit, gh, email, browser, http" + `, or all. --for 2h switches them back on by itself; --here or --dir limits the switch-off to one directory tree.`

func configDir(home string) string { return filepath.Join(home, ".config", "reright") }

// splitArgs separates kind names from flags so "disable commit --for 1h" and "disable --for 1h commit" both work.
func splitArgs(args []string) (names []string, forDur string, dirs []string, here bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if k := strings.Index(a, "="); k > 0 && strings.HasPrefix(a, "--") {
				return a[k+1:], nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--here":
			here = true
		case a == "--for" || strings.HasPrefix(a, "--for="):
			if forDur, err = value(); err != nil {
				return
			}
		case a == "--dir" || strings.HasPrefix(a, "--dir="):
			var d string
			if d, err = value(); err != nil {
				return
			}
			dirs = append(dirs, d)
		case strings.HasPrefix(a, "-"):
			err = fmt.Errorf("unknown flag %s", a)
			return
		default:
			names = append(names, a)
		}
	}
	return
}

func runStatus(home string, stdout io.Writer, now time.Time) error {
	f, err := toggle.Load(configDir(home))
	if err != nil {
		fmt.Fprintf(stdout, "warning: %v\nTreating every kind as on until it is fixed. `reright enable all` rewrites it.\n\n", err)
	}
	for _, l := range f.Lines(now) {
		fmt.Fprintln(stdout, l)
	}
	return nil
}

func runDisable(args []string, home string, stdout io.Writer, now time.Time) error {
	names, forDur, dirs, here, err := splitArgs(args)
	if err != nil {
		return err
	}
	kinds, err := toggle.Expand(names)
	if err != nil {
		return err
	}
	var until time.Time
	if forDur != "" {
		d, perr := time.ParseDuration(forDur)
		if perr != nil || d <= 0 {
			return fmt.Errorf("--for %q is not a duration like 30m or 2h", forDur)
		}
		until = now.Add(d)
	}
	if here {
		wd, werr := os.Getwd()
		if werr != nil {
			return werr
		}
		dirs = append(dirs, wd)
	}
	for i, d := range dirs {
		abs, aerr := filepath.Abs(d)
		if aerr != nil {
			return aerr
		}
		dirs[i] = filepath.Clean(abs)
	}
	if err := toggle.Disable(configDir(home), kinds, until, dirs); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Switched off: %s.\n", strings.Join(kinds, ", "))
	f, _ := toggle.Load(configDir(home))
	for _, l := range f.Lines(now) {
		if strings.Contains(l, " OFF ") {
			fmt.Fprintln(stdout, "  "+l)
		}
	}
	fmt.Fprintln(stdout, "Takes effect in running sessions. Turn it back on with `reright enable "+strings.Join(kinds, " ")+"`.")
	return nil
}

func runEnable(args []string, home string, stdout io.Writer, now time.Time) error {
	names, forDur, dirs, here, err := splitArgs(args)
	if err != nil {
		return err
	}
	if forDur != "" || len(dirs) > 0 || here {
		return errors.New("enable takes only kind names")
	}
	kinds, err := toggle.Expand(names)
	if err != nil {
		return err
	}
	if err := toggle.Enable(configDir(home), kinds, now); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Switched on: %s.\n", strings.Join(kinds, ", "))
	return nil
}
