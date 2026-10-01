package onboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/interpt-co/reright-cli/internal/githook"
)

type UninstallOptions struct {
	Home   string
	Runner Runner
	Out    io.Writer
	Agents []string
	Force  bool
}

func removeEmptyDirs(path, root string) {
	dir := filepath.Dir(path)
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
	if dir == root {
		os.Remove(dir)
	}
}

func restoreRecord(rec FileRecord, hadManifest bool, root string, out io.Writer) error {
	dest, isLink := resolveTarget(rec.Path)
	cur, exists, err := readOptional(dest)
	if err != nil || !exists {
		return err
	}
	mode := os.FileMode(0o644)
	switch {
	case rec.Mode != 0:
		mode = os.FileMode(rec.Mode)
	case currentMode(dest) != 0:
		mode = os.FileMode(currentMode(dest))
	}
	if hadManifest && rec.AfterSHA != "" && sha(cur) == rec.AfterSHA {
		if !rec.Existed {
			fmt.Fprintf(out, "Removed %s (it did not exist before install)\n", dest)
			if err := os.Remove(dest); err != nil {
				return err
			}
			if !isLink {
				removeEmptyDirs(rec.Path, root)
			}
			return nil
		}
		if rec.Link != "" && !isLink {
			if err := os.Remove(rec.Path); err != nil {
				return err
			}
			if err := os.Symlink(rec.Link, rec.Path); err != nil {
				return err
			}
			fmt.Fprintf(out, "Put the link %s -> %s back (install had replaced it with a regular file)\n", rec.Path, rec.Link)
			return nil
		}
		orig, err := os.ReadFile(rec.Backup)
		if err != nil {
			return fmt.Errorf("read backup %s: %w", rec.Backup, err)
		}
		fmt.Fprintf(out, "Restored %s from the backup taken at install\n", dest)
		return writeFile(dest, orig, mode)
	}
	stripped, err := stripAll(rec.Tags, cur)
	if err != nil {
		return fmt.Errorf("%s: %w", rec.Path, err)
	}
	if string(stripped) == string(cur) {
		fmt.Fprintf(out, "%s had nothing from reright in it\n", dest)
		return nil
	}
	if !rec.Existed && effectivelyEmpty(stripped) {
		fmt.Fprintf(out, "%s changed after install. Removed it, because only reright's entries were left in it\n", dest)
		if err := os.Remove(dest); err != nil {
			return err
		}
		if !isLink {
			removeEmptyDirs(rec.Path, root)
		}
		return nil
	}
	fmt.Fprintf(out, "%s changed after install, so only the reright entries were removed\n", dest)
	if info, err := os.Stat(dest); err == nil {
		mode = info.Mode().Perm()
	}
	return writeFile(dest, stripped, mode)
}

func Uninstall(ctx context.Context, o UninstallOptions) error {
	p := Paths{o.Home}
	if o.Runner == nil {
		o.Runner = ExecRunner{Home: o.Home}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	requested, err := normalizeAgents(o.Agents)
	if err != nil {
		return err
	}
	if isDir(p.ConfigDir()) {
		unlock, err := lockConfig(p)
		if err != nil {
			return err
		}
		defer unlock()
	}
	m, hadManifest, err := loadManifest(p)
	damaged := false
	if err != nil {
		if !errors.Is(err, errBadManifest) {
			return fmt.Errorf("read %s: %w", p.ManifestFile(), err)
		}
		if !o.Force {
			return fmt.Errorf("%s cannot be used: %w. Uninstall cannot tell which files install changed. Run reright uninstall --force to remove reright's entries from the default config files (your backups in %s are kept), or move the record away and remove the entries by hand", p.ManifestFile(), err, p.BackupDir())
		}
		damaged = true
		m, hadManifest = Manifest{}, false
		fmt.Fprintf(o.Out, "The install record is damaged (%v). Going on because of --force: reright entries are removed where they are found, and the backups in %s stay.\n", err, p.BackupDir())
	}
	if !hadManifest && !damaged && len(requested) > 0 {
		fmt.Fprintf(o.Out, "No install record found, so reright install did not set up %s. Nothing was changed.\n", strings.Join(requested, ", "))
		return nil
	}
	hook := m.HookPath
	if hook == "" {
		hook = p.DefaultHook()
	}
	targets := requested
	if len(targets) == 0 {
		targets = agentOrder
		if hadManifest {
			targets = m.agentNames()
		}
	}
	if !hadManifest && !damaged {
		fmt.Fprintln(o.Out, "No install record found. Removing reright entries where they are found.")
	}
	for _, agent := range targets {
		rec := m.Agents[agent]
		if rec == nil {
			if hadManifest {
				fmt.Fprintf(o.Out, "%s was not set up by reright install, so nothing was changed\n", agent)
				continue
			}
			plan, err := buildPlan(agent, planEnv{Home: o.Home, HookPath: hook, Server: "https://invalid.example", Token: placeholderToken})
			if err != nil {
				return err
			}
			rec = &AgentRecord{MCPCLI: plan.ClaudeMCP}
			for _, e := range plan.Edits {
				rec.Files = append(rec.Files, FileRecord{Path: e.Path, Tags: e.tags(), Existed: true})
			}
		}
		root := filepath.Join(o.Home, agentSpecs[agent].Dirs[0])
		for _, f := range rec.Files {
			if err := restoreRecord(f, hadManifest, root, o.Out); err != nil {
				if !o.Force {
					return fmt.Errorf("%w. Fix the permissions of that file and run uninstall again, or run reright uninstall --force to skip files that cannot be written", err)
				}
				fmt.Fprintf(o.Out, "Skipped %s: %v\n", f.Path, err)
			}
		}
		if rec.MCPCLI {
			removeClaudeMCP(ctx, o)
		}
		delete(m.Agents, agent)
	}
	full := len(requested) == 0 || (hadManifest && len(m.Agents) == 0)
	if !full {
		if hadManifest {
			if err := saveManifest(p, m); err != nil {
				return err
			}
		}
		fmt.Fprintf(o.Out, "Removed reright from %s. The hook binary, token and git layer stay for the other agents.\n", strings.Join(targets, ", "))
		return nil
	}
	notes, err := githook.UninstallNotes(o.Home)
	if err != nil {
		return fmt.Errorf("remove the git layer: %w", err)
	}
	fmt.Fprintln(o.Out, "Removed the git layer (or it was not installed)")
	for _, n := range notes {
		fmt.Fprintf(o.Out, "Git layer: %s\n", n)
	}
	files := []string{hook, p.TokenFile(), p.URLFile(), p.ManifestFile()}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if damaged {
		fmt.Fprintf(o.Out, "The backups in %s were kept, so you can compare them with the files above.\n", p.BackupDir())
	} else if err := os.RemoveAll(p.BackupDir()); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(p.StateDir(), "locks")); err != nil {
		return err
	}
	os.Remove(p.LockFile())
	os.Remove(p.ConfigDir())
	fmt.Fprintln(o.Out, "reright is removed from this machine. The device token still works on the server until you revoke this device on the dashboard.")
	return nil
}

func removeClaudeMCP(ctx context.Context, o UninstallOptions) {
	claude, err := o.Runner.LookPath("claude")
	if err != nil {
		fmt.Fprintln(o.Out, "claude is not on PATH, so the MCP server was not removed. Run: claude mcp remove --scope user reright")
		return
	}
	if _, err := o.Runner.Run(ctx, claude, "mcp", "remove", "--scope", "user", "reright"); err == nil {
		fmt.Fprintln(o.Out, "Removed the reright MCP server from claude")
	} else {
		fmt.Fprintln(o.Out, "The reright MCP server was not registered with claude")
	}
}
