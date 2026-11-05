# reright-cli

The command-line part of [reright](https://reright.it): the `reright` installer and the `reright-hook` program that Claude Code runs before it sends text a person will read.

reright rewrites the commit messages, PR text, emails and similar writing an agent produces so they sound like you, and can hold them for your approval first. The server is a hosted service and is not in this repository. Everything that runs on your machine is.

## What runs on your machine

- **`reright-hook`** is started by Claude Code's hooks. It reads the tool call the agent is about to make, finds the text in it (a `git commit -m` message, a `gh pr create` body, a Gmail send) and asks the server whether that exact text came back from reright. If not, it blocks the call and tells the agent to send the text through the reright tool `rewrite` first.
- **`reright`** installs and removes the hook and the agent config, checks the setup (`reright doctor`), and lets you switch checks off and on (`reright disable`, `reright enable`, `reright status`).

## What it sends

The hook sends one thing to the server: the SHA-256 hash of the normalized text, with your device token, to `GET /api/check`. It never sends the text. The text reaches the server only when the agent calls `rewrite` itself, through the MCP server the installer registers. The installer also contacts the server to exchange your one-time setup code for a device token, and `reright doctor` checks the server and the MCP endpoint. You can read the rest in `internal/hookcli`, `internal/githook` and `internal/onboard`.

## What it writes

Only files under your home directory, and it backs each one up first:

- `~/.local/bin/reright-hook`, and `~/.config/reright/` for the token and server URL (mode 600)
- Claude Code's user-level hook config and rule text (`~/.claude/settings.json` and `~/.claude/CLAUDE.md`), and its MCP server entry through `claude mcp add`

Versions before 0.2 also installed a git layer through the global `core.hooksPath`. `reright upgrade` removes it, see [docs/agents/git-layer.md](docs/agents/git-layer.md).

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

## Upgrading

```sh
reright upgrade --check   # is there a newer release?
reright upgrade           # replace reright and reright-hook, no setup code needed
```

`reright upgrade` checks the signature of `checksums.txt` and the checksum of each program before it replaces anything, the same way `reright install` does. Your token and agent configuration stay as they are. Once a day the hook can show a one line notice in Claude Code when a newer release exists. Set `RERIGHT_NO_UPDATE_CHECK=1` to turn that off. `reright version` and `reright-hook --version` print the version.

## Building and testing

```sh
go build ./...
go test ./...
```

The release scripts in `scripts/` need the maintainer's signing key and are not for general use. A `reright` you build yourself carries no release key and refuses to install anything, unless you make a throwaway key with `go run ./cmd/reright-release keygen` and build with it.

## Agents

Claude Code is the only agent reright sets up. [docs/agents/hook.md](docs/agents/hook.md) says what the hook checks and what it lets through.

## Reporting problems

Bugs and false positives: open an issue. Security problems: see [SECURITY.md](SECURITY.md).

## Licence

MIT, see [LICENSE](LICENSE).
