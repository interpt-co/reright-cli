// Command reright-hook is run by Claude Code hooks. It blocks human-facing
// text that has not been approved in reright, and stops a session after a reject.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/interpt-co/reright-cli/internal/githook"
	"github.com/interpt-co/reright-cli/internal/hookcli"
)

const defaultURL = "https://app.reright.it"

func main() {
	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".config", "reright")
	url := firstNonEmpty(os.Getenv("RERIGHT_URL"), readTrim(filepath.Join(cfgDir, "url")), defaultURL)
	token := firstNonEmpty(os.Getenv("RERIGHT_TOKEN"), readTrim(filepath.Join(cfgDir, "token")))
	if len(os.Args) > 1 && os.Args[1] == "git" {
		os.Exit(githook.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, githook.Env{
			StateDir:  filepath.Join(home, ".local", "state", "reright"),
			Checker:   hookcli.HTTPChecker{BaseURL: url, Token: token},
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
	code := hookcli.RunAgent(ctx, agent, sub, os.Stdin, os.Stdout, os.Stderr, hookcli.Env{
		StateDir:  filepath.Join(home, ".local", "state", "reright"),
		Checker:   hookcli.HTTPChecker{BaseURL: url, Token: token},
		Getenv:    os.Getenv,
		ConfigDir: cfgDir,
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
