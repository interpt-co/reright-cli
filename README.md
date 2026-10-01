# reright-cli

The command-line part of [reright](https://reright.it): the `reright` installer and the `reright-hook` program that coding agents run before they send text a person will read.

reright makes an agent show its commit messages, PR text, emails and similar writing to a person for approval before it goes out. The review server is a hosted service and is not in this repository. Everything that runs on your machine is.

## What runs on your machine

- **`reright-hook`** is started by your agent's hooks (Claude Code, Gemini CLI, Copilot, Cursor) and by a global git hook. It reads the tool call the agent is about to make, finds the text in it (a `git commit -m` message, a `gh pr create` body, a Gmail send) and asks the server whether that exact text was approved. If not, it blocks the call and tells the agent to submit the text for review.
- **`reright`** installs and removes the hook and the agent config, checks the setup (`reright doctor`), and lets you switch checks off and on (`reright disable`, `reright enable`, `reright status`).

## What it sends

The hook sends one thing to the server: the SHA-256 hash of the normalized text, with your device token, to `GET /api/check`. It never sends the text. The text reaches the server only when the agent calls `submit_for_review` itself, through the MCP server the installer registers. The installer also contacts the server to exchange your one-time setup code for a device token, and `reright doctor` checks the server and the MCP endpoint. You can read the rest in `internal/hookcli`, `internal/githook` and `internal/onboard`.

## What it writes

Only files under your home directory, and it backs each one up first:

- `~/.local/bin/reright-hook`, and `~/.config/reright/` for the token and server URL (mode 600)
- each agent's user-level hook config, MCP server entry and rule text (for Claude Code: `~/.claude/settings.json` and `~/.claude/CLAUDE.md`)
- a global `core.hooksPath` for the git layer, which chains to any hooks you already have

`reright uninstall` puts every file back as it was. See [docs/agents/installer.md](docs/agents/installer.md) for the full list.

## Checking what you download

Releases are on the [releases page](../../releases). `reright install` downloads `reright-hook`, checks its SHA-256 against `checksums.txt`, and checks the ed25519 signature of that file against a public key compiled into `reright`. It stops before it spends your setup code if either check fails.

To check a release against this source, build the same commit with the same Go version and compare:

```sh
git checkout vX.Y.Z
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o reright-hook ./cmd/reright-hook
sha256sum reright-hook      # compare with the reright-hook_linux_amd64 line in checksums.txt
```

The builds use `-trimpath` and no cgo so they should reproduce. If one does not, please open an issue.

## Building and testing

```sh
go build ./...
go test ./...
```

The release scripts in `scripts/` need the maintainer's signing key and are not for general use. A `reright` you build yourself carries no release key and refuses to install anything, unless you make a throwaway key with `go run ./cmd/reright-release keygen` and build with it.

## Agents

Claude Code is the only agent this has been run against first hand. The hooks for Gemini CLI, Copilot and Cursor are written from the vendors' documentation, and [docs/agents/hook-adapters.md](docs/agents/hook-adapters.md) lists what is unverified.

## Reporting problems

Bugs and false positives: open an issue. Security problems: see [SECURITY.md](SECURITY.md).

## Licence

MIT, see [LICENSE](LICENSE).
