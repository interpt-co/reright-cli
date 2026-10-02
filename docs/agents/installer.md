# The installer

`reright install` sets reright up for every coding agent it finds on the machine, not only Claude Code. `reright doctor` checks the result and `reright uninstall` puts the files back. The code is in `internal/onboard`, and the hook configs come from `internal/agentcfg`. Nothing here was run against a real Codex, Gemini, Copilot or Cursor. Claude Code is the only agent that could be tested first hand, so the other four are documented but unproven. The list of what is unverified is at the end.

## What install does

1. Checks the setup code format, the server URL and the release key. It also reads and validates `install.json`, picks the agents (see below), and checks that every file it would edit can be written and that the folders for the hook binary and the config exist or can be made. If anything is wrong at this point, nothing has been written and the setup code is still unused.
2. Downloads `reright-hook`, checks its signed checksum, and exchanges the setup code for a device token.
3. Takes a lock on `~/.config/reright/lock`, writes the hook binary to `~/.local/bin/reright-hook` (or `--hook-path`, made absolute first) and the token and server URL to `~/.config/reright/` (folder mode 700, files mode 600).
4. For each selected agent, edits its user-level files: the hook entries, the MCP server registration and the rule text. It backs up each file before the first change. The lock is held until the manifest is saved, so two installs at the same time run one after the other.
5. Installs the git layer (see `git-layer.md`) unless you pass `--no-git`.
6. Prints a summary, then submits a test draft and waits for you to approve it in the browser. `--no-test` skips this step.

Install never asks a question. It exits 0 when everything worked, 1 on an error, and 3 when it worked but skipped an agent it had detected because that agent's config could not be edited (the message names the agent and the reason). `--yes` is accepted so scripts can pass it, and it changes nothing. The only wait is the test draft, which has a timeout (`--wait`, ten minutes by default).

## Choosing agents

Without `--agent`, install looks for each of claude, codex, gemini, copilot and cursor and sets up the ones it finds. An agent counts as found when its command is on PATH or its config directory exists: `~/.claude`, `~/.codex`, `~/.gemini`, `~/.copilot`, `~/.cursor`. Copilot is found by the `copilot` command, `~/.copilot`, or a `github.copilot*` folder under `~/.vscode/extensions`, `~/.vscode-insiders/extensions` or `~/.vscode-server/extensions`. A VS Code user directory alone does not count, because VS Code is installed on most machines. Cursor is found by `cursor-agent`, `cursor` or `~/.cursor`.

| Flag | Effect |
|---|---|
| `--agent NAME` | Only this agent. Repeat it for several. Also accepts `a,b`. |
| `--all-supported` | Write config for the selected agents even when they were not found. |
| `--no-git` | Skip the git layer. |
| `--dry-run` | Print the plan and change nothing. No setup code is needed and nothing is downloaded. |
| `--force` | Edit a read-only file, and replace a symlink that points into a read-only folder with a regular file. See "Links and read-only files". |
| `--yes` | For scripts. No effect. |

If `--agent` names an agent that is not installed and you did not pass `--all-supported`, install stops before it spends the code and says so. If no agent is found at all, it stops the same way. An agent whose existing config file is broken (invalid JSON, JSON with comments or trailing commas, invalid TOML, a TOML `reright` entry reright did not write, or a file it cannot write) is skipped with the reason when it was found automatically, and stops the install when you named it. The skipped case makes install exit 3, and `reright doctor` reports the agent as a failed `AGENT config` check until you fix the file and install it.

## The summary

Every run ends with one block per agent, in the same order, so a person or an agent can read it.

```
Summary
  claude   configured
           done: hooks (enforced) in /home/you/.claude/settings.json
           done: rules text in /home/you/.claude/CLAUDE.md
           done: MCP server registered with claude mcp add (user scope). ...
  codex    configured
           done: hooks (enforced) in /home/you/.codex/hooks.json
           ...
           unverified: hooks may not run with --dangerously-bypass-approvals-and-sandbox (not tested)
  gemini   not configured: not installed (no gemini on PATH, no ~/.gemini). Use --all-supported to write its config anyway
  git      installed in /home/you/.config/reright/git-hooks and set as the global core.hooksPath. ...
```

An agent is `configured`, `partly configured` (something could not be written, and the line says what) or `not configured` (with the reason: not installed, unsupported, or a broken config file). The git line shows what `githook.Install` returned. When a global `core.hooksPath` already exists, it is not changed, and the two lines to add by hand are printed here. Agents with no rules file (Cursor) get the rule text printed at the end, to paste.

## What is written for each agent

Only files under your home directory. Repo-level files are never written, because the agent can edit files in the repo and could remove its own hooks.

| Agent | Hooks | MCP server | Rules text |
|---|---|---|---|
| claude | `~/.claude/settings.json` (PreToolUse, PostToolUse, UserPromptSubmit) | `claude mcp add --scope user` | `~/.claude/CLAUDE.md` |
| codex | `~/.codex/hooks.json`, commands end in `--agent codex` | `[mcp_servers.reright]` in `~/.codex/config.toml` | `~/.codex/AGENTS.md` |
| gemini | `~/.gemini/settings.json` (BeforeTool, AfterTool, BeforeAgent), `--agent gemini` | `mcpServers.reright` in `~/.gemini/settings.json` | `~/.gemini/GEMINI.md` |
| copilot | `~/.copilot/hooks/reright.json`, `--agent copilot` | `~/.copilot/mcp-config.json`, and `mcp.json` in the VS Code user directory when that exists | `~/.copilot/copilot-instructions.md` |
| cursor | `~/.cursor/hooks.json` with `failClosed` on shell and MCP, `--agent cursor` | `~/.cursor/mcp.json` | none: Cursor keeps user rules in its settings, so the text is printed |

The hook entries are the ones `agentcfg.For` returns, so they keep the agent's own timeouts and fail-closed settings. The rule text is `internal/onboard/rule-block.md`, the same for every agent because it names no agent-specific tool.

The MCP token goes into a config file, not onto a command line, wherever the agent has a config file route: Codex, Gemini, Copilot and Cursor. Those files are set to mode 600. Claude Code is the exception. Its user-scope MCP registration lives in `~/.claude.json`, which Claude rewrites constantly, so reright uses `claude mcp add --header`, and the token is visible in the process list for the moment that command runs. Codex and Gemini also have `mcp add` commands, but with a header they would expose the token the same way, so reright writes their config files instead.

Codex and Gemini read the token from the file on each start. If you rotate the token, run install again.

### How merging works

- JSON files are read, merged and written back with two-space indent. Key order is not kept (keys come out sorted), and comments are not supported. A JSON file with comments or trailing commas (JSONC, which Gemini's `settings.json` allows) is reported as such and left alone, because editing it would drop the comments. The VS Code `mcp.json` often has comments. If it does, the rest of Copilot is still set up and the summary tells you to add that one entry by hand.
- `config.toml` is parsed to check it, but not rewritten. reright appends one block between `# reright:start` and `# reright:end` and removes that block on uninstall. Before writing, it parses the file without its own block. If the file is not valid TOML, or has any `reright` key under `mcp_servers` that reright did not write (`[mcp_servers.reright]`, a quoted key, a dotted key, or an inline table), install stops before spending the code and tells you to fix it. It then parses the result, so it never writes TOML that would not load (for example when `mcp_servers` is an inline table).
- Markdown files get the rule text between `<!-- reright:start -->` and `<!-- reright:end -->`.
- Claude's MCP server is added with `claude mcp add`. If claude already has a server named `reright`, install first adds a probe server under another name. Only when that works does it remove the old one and add the real one, and the summary says the old one was replaced. If the probe fails, the old server is left as it was.
- Every entry is tagged by its command (it contains `reright-hook`), its key (`reright` under `mcpServers` or `servers`) or its markers. Running install again strips these and writes them once, so there are no duplicates.

### Links and read-only files

If a config file is a symlink (stow, home-manager, a dotfiles repo), install resolves it and edits the real file, so the link stays. Uninstall restores the real file in place.

If a file is read-only, install refuses before spending the code and names the file. `--force` edits it anyway and keeps its mode. If the link points into a folder that cannot be written (the nix store), `--force` replaces the link with a regular file, records the original link, and uninstall puts the link back when the file is unchanged.

### Re-installs and your edits

Install backs up a file the first time. On a later install it compares the file with the hash it recorded. If they differ, you or the agent edited the file in between, so the backup is refreshed to the current file with reright's entries stripped. A file that install created but that now holds your text is treated as a file that existed. A file you deleted is forgotten. So install, edit, install again, uninstall keeps every edit.

## State and uninstall

`~/.config/reright/install.json` records, for each agent, every file install touched: the path, what it added (hooks, mcp, rules), the tags, whether the file existed, its permission bits, the path of the backup of its earlier content, and a hash of the file as install left it. Backups are in `~/.config/reright/backup/AGENT/`. A manifest from the earlier Claude-only installer is converted when it is read, and the modes of the files it lists are taken from the files on disk. The manifest is checked when it is read: every file path has to be absolute and inside your home, and every backup has to be inside the backup folder. A damaged manifest stops install (before the code is spent) and uninstall, with instructions. `reright uninstall --force` then removes reright's entries from the default config files, keeps the backups, and deletes the damaged record. All changes to the manifest and to the config files happen under the lock.

`reright uninstall` goes through every agent in the record, or only the ones named with `--agent`.

- If a file is byte for byte what install wrote, it is restored from the backup, or deleted if it did not exist before (and its directory, if that is now empty and lives in the agent's own config directory).
- If you edited the file since, only reright's entries are removed and your edits stay. The output says `changed after install`. A file that install created and that has nothing else left in it is deleted.
- With `--agent`, the hook binary, the token and the git layer stay, because other agents still use them. When the last agent is removed, everything goes: hook binary, token, URL, backups, lock files and the git layer.
- Without an install record, uninstall looks in the default paths and removes anything tagged as reright's. `uninstall --agent NAME` without a record changes nothing, creates no record, and says so.
- If a file cannot be written, uninstall stops and says which one. `--force` skips such files and goes on.
- The git layer is removed too. If you added reright lines by hand to an existing hooks directory, uninstall lists those files, because the lines now call a binary that is gone and would fail every commit until you remove them.

The device token still works on the server after uninstall until you revoke the device on the dashboard.

## Upgrading

`reright upgrade` replaces `reright` and `reright-hook` with the newest release. It needs no setup code, because the device already has a token, and it leaves the token and every agent's configuration alone. It downloads `checksums.txt`, `checksums.txt.sig` and `version.txt`, checks the signature against the key compiled into `reright`, checks each program against its signed checksum, and only then replaces them. The hook is replaced first, then `reright` itself, each through a temporary file and a rename. If a check fails nothing changes.

- `reright upgrade --check` only says whether a newer release exists.
- `reright upgrade --force` replaces the programs even when the release is not newer.
- A local build (`reright version` says `dev`) is replaced by the release.
- If `reright-hook` is not installed where the install record says, only `reright` is replaced.

`reright version` and `reright-hook --version` print the version. `reright doctor` shows both and warns when they differ, or when the hook is too old to report a version.

**Update notice.** Once a day, when you submit a prompt in Claude Code, the hook looks at `version.txt` on the latest release. If it names a newer version, Claude Code shows a one line warning with `Run: reright upgrade`, at most once a day for each version. Nothing is installed by the hook. The check takes at most two seconds, fails quietly, and is off when `RERIGHT_NO_UPDATE_CHECK=1` is set. State lives in `~/.local/state/reright/update.json`.

**Server nudge.** The hook sends its version in an `X-Reright-Client` header on every check. A server that has `RERIGHT_CLIENT_LATEST` set answers `X-Reright-Latest` and, for older hooks, `X-Reright-Upgrade: available`. A server with `RERIGHT_CLIENT_MIN` set answers `X-Reright-Upgrade: required` to hooks older than that minimum, and to hooks that send no version. The hook saves the answer in `client-policy.json` next to the other state, and a required upgrade is shown on every prompt until the client is upgraded. The checks themselves keep working.

## Doctor

`reright doctor` (and `reright doctor --agent NAME`) prints one line per check. The line starts with `ok`, `warn` or `FAIL`. A `FAIL` or `warn` line ends with `Fix: ...`. The exit code is 1 when any check failed. Warnings do not change the exit code.

Shared checks: `config` (token and URL present), `server` (the server accepts the token), `mcp` (the MCP endpoint lists the two tools), `hook binary` (exists, is executable, lets a harmless call through, and blocks `git commit -m x`). The last probe runs the binary against a small local checker that approves nothing, in a temporary home, with `RERIGHT_*` variables cleared. A stub that exits 0 fails it.

For each agent in the install record (or each one found, when there is no record): `hooks` (every hook command is present and carries `--agent`, and Cursor entries keep `failClosed`). Commands are compared by what `sh` makes of them (the words after quoting), not as strings, so a command quoted differently still matches. For Claude the configured PreToolUse command is also run through `sh -c` and has to block `git commit -m x`. The check also fails when hooks are switched off: `disableAllHooks: true` in Claude's settings, and `hooksConfig.enabled` or `tools.enableHooks` set to false in Gemini's (those two Gemini names are from memory of its docs, UNVERIFIED), `mcp entry` (the entry exists and holds the current URL and token, so a stale token after a re-install on another device is caught), and `rules` (the rule text is present). Codex also gets `hooks enabled`, which fails if `[features]` in `config.toml` turns hooks off. Claude's MCP check asks `claude mcp get reright`.

When there is no `--agent` flag, doctor also looks at agents that were found but are not in the record. If their config file is invalid, it reports a failed `AGENT config` check, which is how a skipped agent stays visible.

Then `git layer`: active through the global `core.hooksPath`. The line lists the limits: a repository with its own `core.hooksPath` bypasses the layer and doctor can only check the repository it runs in, and commits with no terminal (IDEs, git GUIs) count as agent commits (see `git-layer.md`, `tty_heuristic`). Run inside a repository that sets its own `core.hooksPath`, doctor adds a `git layer here` warning saying the layer does not run there. A skipped layer or an existing hooks path you have not edited by hand is a warning, and a missing layer that install claims to have made is a failure.

Warnings the doctor adds:

- `RERIGHT_BYPASS` or `RERIGHT_OFFLINE` is set in the environment that runs the doctor. An agent that inherits them skips the checks or lets text through when the server is down.
- A project-level Claude settings file that sets `disableAllHooks: true`.
- A project-level hook file in the current repository (`.claude/settings.json`, `.codex/hooks.json`, `.gemini/settings.json`, `.cursor/hooks.json`, `.github/hooks` and a few more) that defines hooks. The agent can edit it. The doctor looks from the current directory up to the repository root and never above your home directory.
- `CODEX_HOME` or `COPILOT_HOME` is set. reright writes to `~/.codex` and `~/.copilot` and does not follow those variables.
- `~/.codex/AGENTS.override.md` exists. Codex reads it instead of `AGENTS.md`, so the rule text may not load.
- One `unverified` line per item for each agent, taken from the list below. Yolo and auto-approve modes are the main ones.

## Enforced, or only told

An agent with working hooks is enforced: `reright-hook` blocks `git commit`, `gh` PR and issue text, Gmail sends and browser text entry that reright has not approved. The rule text asks the agent to use the review flow, and the hooks make it. The git layer adds a check on `git commit` for any agent that runs it in a shell, which includes agents whose hooks do not work.

An agent without working hooks only gets the rule text and the MCP tools. The rule text is a request. Nothing stops the agent from ignoring it.

## Unverified, and other limits

Everything below is from documentation, not from running the real agent. `hook-adapters.md` has the full table and the checklist to run.

| Agent | Not proven |
|---|---|
| claude | No open items in `hook-adapters.md`. Whether `--dangerously-skip-permissions` skips hooks was not checked. |
| codex | Payload and deny format match Claude's. Whether hooks run with `--dangerously-bypass-approvals-and-sandbox`. Whether the model sees the deny reason. The extra shell tool names. Codex asks you to trust new hooks, so they do nothing until you do. |
| gemini | `--yolo` may skip hooks. The `mcp_context` field names, and the AfterTool and BeforeAgent payloads. |
| copilot | `--allow-all-tools` may skip hooks. Whether hooks fire for MCP tools at all. A hook timeout fails open in the CLI. The user-level hook location for VS Code and its field names. VS Code user rules are not written, because that location is not documented well enough to write to. |
| cursor | Auto-run mode. `beforeMCPExecution` in cloud agents (documented as unsupported). The MCP payload fields. Rules have to be pasted by hand. |

Other limits:

- The MCP entry for Copilot CLI and Cursor follows the config file formats in their documentation. Neither was started with the file.
- Claude's MCP registration still puts the token on a command line for a moment.
- - Symlinked files are edited through the link. A link into a read-only folder needs `--force`.
- Two installs at the same time are serialized by a lock, but each needs its own setup code (a dashboard account has one live code).
- The JSON merge reorders keys in files it edits. A restore from backup brings the original order back, but only if you did not edit the file since.
- Installing on a second machine with a new setup code gives that machine its own token. The old token keeps working until you revoke it on the dashboard.
- Windsurf, Junie, Goose, Amp, OpenCode, Cline and Continue are not supported. `--agent` with one of those names stops with `unsupported agent`.

## Tests

`internal/onboard/agents_test.go` runs each agent through a fresh install, a second install, an install over existing user config, an exact uninstall, an uninstall after the user edited the files, a dry run, agents that are not found, and doctor failures. It uses a temporary home, `GIT_CONFIG_GLOBAL` inside that home, and fake agent commands on PATH (shell scripts that log their arguments). The fix tests are in `internal/onboard/fixes_test.go` (re-install with edits, damaged manifest, links, read-only files, parallel installs, TOML forms, skipped agents, doctor probes). `scripts/e2e-onboarding.sh` runs the same flow against a real local server with a fake claude, codex and gemini, and compares a checksum of every file in the home before install and after uninstall. It also checks that a repository's own pre-commit and post-commit hooks still run through the git layer, and that edits made before and between two installs survive an uninstall with no token left under the home.

To skip the checks for a while or for one directory, see `switches.md`.
