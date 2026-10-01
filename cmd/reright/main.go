package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/onboard"
	"github.com/interpt-co/reright-cli/internal/release"
)

var releasePublicKey string

const usage = `usage: reright install --code CODE [--server URL] [--agent NAME]... [--all-supported] [--no-git] [--dry-run] [--yes] [--no-test] [--force]
       reright doctor [--agent NAME]...
       reright uninstall [--agent NAME]... [--force]

` + switchUsage + `

agents: claude, codex, gemini, copilot, cursor. Without --agent, install sets up every agent it finds.`

type agentList []string

func (a *agentList) String() string { return strings.Join(*a, ",") }

func (a *agentList) Set(v string) error {
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n != "" {
			*a = append(*a, n)
		}
	}
	return nil
}

func warnings(checks []onboard.Check) string {
	if n := onboard.Warned(checks); n > 0 {
		return fmt.Sprintf(" %d warning(s) above need a look.", n)
	}
	return ""
}

func agentFlags(name string, args []string, stderr io.Writer, force *bool) (agentList, error) {
	var agents agentList
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&agents, "agent", "limit to this agent (repeatable)")
	if force != nil {
		fs.BoolVar(force, "force", false, "remove reright's entries even when the install record is damaged or a file cannot be written")
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, errors.New(usage)
	}
	return agents, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reright: cannot find your home directory:", err)
		os.Exit(1)
	}
	os.Exit(run(ctx, os.Args[1:], home, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, home string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "install":
		err = runInstall(ctx, args[1:], home, stdout, stderr)
	case "doctor":
		agents, aerr := agentFlags("doctor", args[1:], stderr, nil)
		if aerr != nil {
			fmt.Fprintln(stderr, aerr)
			return 2
		}
		checks := onboard.Doctor(ctx, onboard.DoctorOptions{Home: home, Out: stdout, Agents: agents})
		if n := onboard.Failed(checks); n > 0 {
			fmt.Fprintf(stdout, "%d check(s) failed.%s\n", n, warnings(checks))
			return 1
		}
		fmt.Fprintf(stdout, "All checks passed.%s\n", warnings(checks))
		return 0
	case "status":
		err = runStatus(home, stdout, time.Now())
	case "disable":
		err = runDisable(args[1:], home, stdout, time.Now())
	case "enable":
		err = runEnable(args[1:], home, stdout, time.Now())
	case "uninstall":
		var force bool
		agents, aerr := agentFlags("uninstall", args[1:], stderr, &force)
		if aerr != nil {
			fmt.Fprintln(stderr, aerr)
			return 2
		}
		err = onboard.Uninstall(ctx, onboard.UninstallOptions{Home: home, Out: stdout, Agents: agents, Force: force})
	default:
		fmt.Fprintf(stderr, "reright: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "reright:", err)
		if errors.Is(err, onboard.ErrAgentsSkipped) {
			return 3
		}
		return 1
	}
	return 0
}

func runInstall(ctx context.Context, args []string, home string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	code := fs.String("code", "", "one-time setup code from the dashboard")
	server := fs.String("server", onboard.DefaultServer, "reright server URL")
	releaseURL := fs.String("release-url", release.DefaultBaseURL, "where reright-hook, checksums.txt and checksums.txt.sig are downloaded from")
	hookPath := fs.String("hook-path", "", "where to install reright-hook (default ~/.local/bin/reright-hook)")
	noTest := fs.Bool("no-test", false, "skip the test draft")
	var agents agentList
	fs.Var(&agents, "agent", "set up only this agent (repeatable): claude, codex, gemini, copilot, cursor")
	allSupported := fs.Bool("all-supported", false, "write config for supported agents that were not detected")
	noGit := fs.Bool("no-git", false, "skip the git layer")
	dryRun := fs.Bool("dry-run", false, "print the plan and change nothing")
	yes := fs.Bool("yes", false, "accepted for scripts. install never asks a question")
	force := fs.Bool("force", false, "edit read-only files and replace links into read-only folders")
	wait := fs.Duration("wait", onboard.DefaultTestWait, "how long to wait for you to approve the test draft")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *code == "" && fs.NArg() == 1 {
		*code = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return errors.New(usage)
	}
	if *code == "" && !*dryRun {
		return errors.New("a setup code is required: reright install --code CODE (copy it from the dashboard)")
	}
	var pub ed25519.PublicKey
	if releasePublicKey != "" {
		var err error
		if pub, err = release.ParsePublicKey(releasePublicKey); err != nil {
			return err
		}
	}
	return onboard.Install(ctx, onboard.Options{
		Home:         home,
		Server:       *server,
		Code:         *code,
		ReleaseURL:   *releaseURL,
		PublicKey:    pub,
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		HookPath:     *hookPath,
		SkipTest:     *noTest,
		Agents:       agents,
		AllSupported: *allSupported,
		NoGit:        *noGit,
		DryRun:       *dryRun,
		Yes:          *yes,
		Force:        *force,
		TestWait:     *wait,
		Out:          stdout,
	})
}
