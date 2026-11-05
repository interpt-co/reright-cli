# The installer

`reright install` sets reright up for Claude Code, the only agent it supports. `reright doctor` checks the result and `reright uninstall` puts the files back. The code is in `internal/onboard`, and the hook entries come from `internal/agentcfg`. What the hook checks is in `hook.md`.

Earlier versions also wrote config for Codex, Gemini, Copilot and Cursor and installed a git layer. None of that was ever run against the real agents, so it is gone. `--agent codex` (or gemini, copilot, cursor) now stops install before the setup code is spent, other agents found on the machine are ignored, and the git layer is removed (see `git-layer.md`).

## What install does

1. Checks the setup code format, the server URL and the release key. It also reads and validates `install.json`, checks that Claude Code is there (see below), and checks that every file it would edit can be written and that the folders for the hook binary and the config exist or can be made. If anything is wrong at this point, nothing has been written and the setup code is still unused.
2. Downloads `reright-hook`, checks its signed checksum, and exchanges the setup code for a device token.
3. Takes a lock on `~/.config/reright/lock`, writes the hook binary to `~/.local/bin/reright-hook` (or `--hook-path`, made absolute first) and the token and server URL to `~/.config/reright/` (folder mode 700, files mode 600).
4. Edits Claude Code's user-level files: the hook entries in `~/.claude/settings.json`, the rule text in `~/.claude/CLAUDE.md`, and the MCP server through `claude mcp add`. It backs up each file before the first change. The lock is held until the manifest is saved, so two installs at the same time run one after the other.
5. Removes the git hooks an earlier version installed, if they are there.
6. Prints a summary, then sends a test draft ("Hello from reright, the setup works.", kind `message`) through `rewrite`. If it comes back pending, install prints the review link and calls `rewrite_result` until you decide in the browser. Install succeeds when the draft is approved. `--no-test` skips this step.

Install never asks a question. It exits 0 when everything worked and 1 on an error. `--yes` is accepted so scripts can pass it, and it changes nothing. The only wait is the test draft, which has a timeout (`--wait`, ten minutes by default).

## Finding Claude Code

Claude Code counts as found when `claude` is on PATH or `~/.claude` exists.

| Flag | Effect |
|---|---|
| `--agent claude` | Accepted. Any other name stops install before the code is spent. |
| `--all-supported` | Write Claude Code's config even when it was not found. |
| `--no-git` | Accepted for old scripts. It changes nothing. |
| `--dry-run` | Print the plan and change nothing. No setup code is needed and nothing is downloaded. |
| `--force` | Edit a read-only file, and replace a symlink that points into a read-only folder with a regular file. See "Links and read-only files". |
| `--yes` | For scripts. No effect. |

If Claude Code is not found and you did not pass `--all-supported`, install stops before it spends the code and says so. If `~/.claude/settings.json` is broken (invalid JSON, JSON with comments or trailing commas, or a file it cannot write), install stops the same way.

## The summary

Every run ends with a block a person or an agent can read:

```
Summary
  claude   configured
           done: hooks (enforced) in /home/you/.claude/settings.json
           done: rules text in /home/you/.claude/CLAUDE.md
           done: MCP server registered with claude mcp add (user scope). ...
  git      removed the git hooks of an earlier version. Commit messages are checked by the Claude Code hook
           unset the global core.hooksPath, which still pointed at /home/you/.config/reright/git-hooks
```

The agent is `configured`, `partly configured` (something could not be written, and the line says what) or `not configured` (with the reason). The `git` lines appear only when there was something to remove.

## What is written

Only files under your home directory. Repo-level files are never written, because the agent can edit files in the repo and could remove its own hooks.

| Part | Where |
|---|---|
| Hooks | `~/.claude/settings.json`: PreToolUse, PostToolUse, UserPromptSubmit and Stop, see `hook.md` |
| MCP server | `claude mcp add --scope user --transport http reright URL/mcp --header "Authorization: Bearer TOKEN"` |
| Rule text | `~/.claude/CLAUDE.md`, between `<!-- reright:start -->` and `<!-- reright:end -->` |

The rule text is `internal/onboard/rule-block.md`. The hook's daily self-heal brings an older block in `CLAUDE.md` up to date (see `hook.md`). The plugin skill in `dist/plugin/skills/reright/SKILL.md` carries the same text, and a test keeps them identical. It tells the agent to call the reright tool `rewrite` with the text, its kind, where it goes and a line of context, to use `final_text` exactly, to call `rewrite_result` while the status is `pending`, and to stop and tell the user on `rejected`.

Claude's user-scope MCP registration lives in `~/.claude.json`, which Claude rewrites constantly, so reright uses `claude mcp add --header`, and the token is visible in the process list for the moment that command runs.

### How merging works

- `settings.json` is read, merged and written back with two-space indent. Key order is not kept (keys come out sorted), and comments are not supported. A file with comments or trailing commas is reported as such and left alone, because editing it would drop the comments.
- `CLAUDE.md` gets the rule text between the markers.
- If claude already has a server named `reright`, install first adds a probe server under another name. Only when that works does it remove the old one and add the real one, and the summary says the old one was replaced. If the probe fails, the old server is left as it was.
- Every entry is tagged by its command (it contains `reright-hook`) or its markers. Running install again strips these and writes them once, so there are no duplicates.

### Links and read-only files

If a config file is a symlink (stow, home-manager, a dotfiles repo), install resolves it and edits the real file, so the link stays. Uninstall restores the real file in place.

If a file is read-only, install refuses before spending the code and names the file. `--force` edits it anyway and keeps its mode. If the link points into a folder that cannot be written (the nix store), `--force` replaces the link with a regular file, records the original link, and uninstall puts the link back when the file is unchanged.

### Re-installs and your edits

Install backs up a file the first time. On a later install it compares the file with the hash it recorded. If they differ, you or the agent edited the file in between, so the backup is refreshed to the current file with reright's entries stripped. A file that install created but that now holds your text is treated as a file that existed. A file you deleted is forgotten. So install, edit, install again, uninstall keeps every edit.

## State and uninstall

`~/.config/reright/install.json` records every file install touched: the path, what it added (hooks, rules), the tags, whether the file existed, its permission bits, the path of the backup of its earlier content, and a hash of the file as install left it. Backups are in `~/.config/reright/backup/claude/`. A manifest from the earlier Claude-only installer is converted when it is read, and the modes of the files it lists are taken from the files on disk. The manifest is checked when it is read: every file path has to be absolute and inside your home, and every backup has to be inside the backup folder. A damaged manifest stops install (before the code is spent) and uninstall, with instructions. `reright uninstall --force` then removes reright's entries from the default config files, keeps the backups, and deletes the damaged record. All changes to the manifest and to the config files happen under the lock.

`reright uninstall` goes through every agent in the record, or only the ones named with `--agent`. A record written by an earlier version can also list codex, gemini, copilot or cursor. Those records still load, `reright uninstall` (or `uninstall --agent codex` and so on) takes out what was written for them, and `reright doctor` warns about each one until then.

- If a file is byte for byte what install wrote, it is restored from the backup, or deleted if it did not exist before (and its directory, if that is now empty and lives in the agent's own config directory).
- If you edited the file since, only reright's entries are removed and your edits stay. The output says `changed after install`. A file that install created and that has nothing else left in it is deleted.
- With `--agent`, the hook binary and the token stay, because other agents in the record still use them. When the last agent is removed, everything goes: hook binary, token, URL, backups, the hook's state in `~/.local/state/reright`, lock files and the git hooks of an earlier version.
- Without an install record, uninstall looks in the default paths and removes anything tagged as reright's. `uninstall --agent NAME` without a record changes nothing, creates no record, and says so.
- If a file cannot be written, uninstall stops and says which one. `--force` skips such files and goes on.
- The git hooks an earlier version installed are removed too (see `git-layer.md`). If you added reright lines by hand to an existing hooks directory, uninstall lists those files so you can take the lines out.

The device token still works on the server after uninstall until you revoke the device on the dashboard.

## Upgrading

`reright upgrade` replaces `reright` and `reright-hook` with the newest release. It needs no setup code, because the device already has a token, and it leaves the token and every agent's configuration alone. It downloads `checksums.txt`, `checksums.txt.sig` and `version.txt`, checks the signature against the key compiled into `reright`, checks each program against its signed checksum, and only then replaces them. The hook is replaced first, then `reright` itself, each through a temporary file and a rename. If a check fails nothing changes.

- It removes the git hooks an earlier version installed, even when the programs are already current (see `git-layer.md`).
- `reright upgrade --check` only says whether a newer release exists, and whether those git hooks are still there.
- `reright upgrade --force` replaces the programs even when the release is not newer.
- A local build (`reright version` says `dev`) is replaced by the release.
- If `reright-hook` is not installed where the install record says, only `reright` is replaced.

`reright version` and `reright-hook --version` print the version. `reright doctor` shows both and warns when they differ, or when the hook is too old to report a version.

**Update notice.** Once a day, when you submit a prompt in Claude Code, the hook looks at `version.txt` on the latest release. If it names a newer version, Claude Code shows a one line warning with `Run: reright upgrade`, at most once a day for each version. Nothing is installed by the hook. The check takes at most two seconds, fails quietly, and is off when `RERIGHT_NO_UPDATE_CHECK=1` is set. State lives in `~/.local/state/reright/update.json`.

**Server nudge.** The hook sends its version in an `X-Reright-Client` header on every check. A server that has `RERIGHT_CLIENT_LATEST` set answers `X-Reright-Latest` and, for older hooks, `X-Reright-Upgrade: available`. A server with `RERIGHT_CLIENT_MIN` set answers `X-Reright-Upgrade: required` to hooks older than that minimum, and to hooks that send no version. The hook saves the answer in `client-policy.json` next to the other state, and a required upgrade is shown on every prompt until the client is upgraded. The checks themselves keep working.

## Doctor

`reright doctor` prints one line per check. The line starts with `ok`, `warn` or `FAIL`. A `FAIL` or `warn` line ends with `Fix: ...`. The exit code is 1 when any check failed. Warnings do not change the exit code.

- `config`: the token and URL are there.
- `server`: the server accepts the token.
- `mcp`: the MCP endpoint lists `rewrite` and `rewrite_result`.
- `hook binary`: it exists, is executable, lets a harmless call through, and blocks a test commit in plain words (`git commit -m 'reright doctor checks that this commit is blocked'`), since a message such as `x` is not free text and passes. The probe runs the binary against a small local checker that approves nothing, in a temporary home, with `RERIGHT_*` variables cleared. A stub that exits 0 fails it.
- `client version`: `reright` and `reright-hook` report the same version.
- `claude hooks`: every hook command is present, compared by what `sh` makes of it (the words after quoting), so a command quoted differently still matches. The configured PreToolUse command is also run through `sh -c` and has to block the same test commit. The check fails when `disableAllHooks` is true.
- `claude mcp entry`: `claude mcp get reright` finds the server.
- `claude rules`: the rule block is in `~/.claude/CLAUDE.md`.

Warnings the doctor adds:

- `git layer`: the git hooks of an earlier version are still installed. Fix: `reright upgrade`.
- An agent other than Claude Code in the install record, left by an earlier version. Fix: `reright uninstall --agent NAME`.
- `RERIGHT_BYPASS` or `RERIGHT_OFFLINE` is set in the environment that runs the doctor. An agent that inherits them skips the checks or lets text through when the server is down.
- A project-level `.claude/settings.json` or `.claude/settings.local.json` that sets `disableAllHooks: true` or defines hooks. The agent can edit it. The doctor looks from the current directory up to the repository root and never above your home directory.
- A check switched off with `reright disable`.

## Limits

- The hook is a check on the common direct cases, not a sandbox. `hook.md` lists what it lets through.
- Claude's MCP registration puts the token on a command line for a moment.
- Symlinked files are edited through the link. A link into a read-only folder needs `--force`.
- Two installs at the same time are serialized by a lock, but each needs its own setup code (a dashboard account has one live code).
- The JSON merge reorders keys in files it edits. A restore from backup brings the original order back, but only if you did not edit the file since.
- Installing on a second machine with a new setup code gives that machine its own token. The old token keeps working until you revoke it on the dashboard.

## Tests

`internal/onboard/agents_test.go` runs a fresh install, a second install, an install over existing user config, an exact uninstall, an uninstall after the user edited the files, a dry run, other agents that are found or named, the removal of an old git layer, and an old install record that lists Codex. `internal/onboard/fixes_test.go` covers re-installs with edits, a damaged manifest, links, read-only files, parallel installs and doctor probes, and `upgrade_test.go` and `heal_test.go` cover the upgrade and self-heal paths, including the git layer. They use a temporary home, `GIT_CONFIG_GLOBAL` inside that home, and fake commands on PATH. `scripts/e2e-onboarding.sh` runs the same flow against a real local server with a fake claude and compares a checksum of every file in the home before install and after uninstall.

To skip the checks for a while or for one directory, see `switches.md`.
