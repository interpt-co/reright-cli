// Command reright-hook is run by Claude Code hooks. It blocks human-facing
// text that has not been approved in reright, and stops a session after a reject.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/githook"
	"github.com/interpt-co/reright-cli/internal/hookcli"
)

const defaultURL = "https://app.reright.it"

// version is set at release time with -X main.version. A local build reports "dev".
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("reright-hook %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".config", "reright")
	url := firstNonEmpty(os.Getenv("RERIGHT_URL"), readTrim(filepath.Join(cfgDir, "url")), defaultURL)
	token := firstNonEmpty(os.Getenv("RERIGHT_TOKEN"), readTrim(filepath.Join(cfgDir, "token")))
	if len(os.Args) > 1 && os.Args[1] == "git" {
		os.Exit(githook.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, githook.Env{
			StateDir:  filepath.Join(home, ".local", "state", "reright"),
			Checker:   hookcli.HTTPChecker{BaseURL: url, Token: token, Version: version},
			Getenv:    os.Getenv,
			ConfigDir: cfgDir,
		}))
	}
	agent, sub, err := hookcli.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "reright-hook:", err)
		os.Exit(hookcli.UsageExit(sub))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	stateDir := filepath.Join(home, ".local", "state", "reright")
	code := hookcli.RunAgent(ctx, agent, sub, os.Stdin, os.Stdout, os.Stderr, hookcli.Env{
		StateDir:   stateDir,
		Checker:    hookcli.HTTPChecker{BaseURL: url, Token: token, Version: version, Policy: hookcli.WritePolicy(stateDir)},
		Getenv:     os.Getenv,
		ConfigDir:  cfgDir,
		Version:    version,
		ReleaseURL: os.Getenv("RERIGHT_RELEASE_URL"),
	})
	cancel()
	os.Exit(code)
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
