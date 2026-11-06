# The hook

`reright-hook` runs from Claude Code's hooks. It is the only agent it supports. The installer writes four entries into `~/.claude/settings.json`, and the Claude Code plugin ships the same four in `dist/plugin/hooks/hooks.json`:

| Event | Command | What it does |
|---|---|---|
| PreToolUse, matcher `*` | `reright-hook pre` | Blocks a tool call whose text did not come back from reright, and almost every call after a reject (see below). |
| PostToolUse on the reright tools | `reright-hook post` | Records a pending rewrite, and sets the reject lock when the result is `rejected`. |
| UserPromptSubmit | `reright-hook prompt` | Clears the reject locks of the session, subagents included, and shows the update notice. |
| Stop | `reright-hook stop` | Keeps the agent from stopping while a rewrite it started is still pending, unless it is under a reject lock. |

The PostToolUse matcher is `mcp__reright__(rewrite|rewrite_result)`, with `plugin_reright_` before `reright` for the plugin. `rewrite` sends the text and can come back pending, approved or rejected. `rewrite_result` waits on the id while it is pending. Self-heal replaces an older matcher with this one.

`--agent claude` is still accepted next to the subcommand. `--agent codex`, `gemini`, `copilot` or `cursor`, from hook configs reright 0.1 wrote for those agents, fails open: the call exits 0 with nothing on stdout and one line on stderr, so an old config never blocks that agent. Any other agent name is a usage error, which exits 2 for `pre` (a block) and 1 for the others.

## What `pre` checks

The code is in `internal/hookcheck`. The hook finds the text in a call, hashes it with `draft.Hash` and asks the server (`GET /api/check`) whether that hash came back from reright in the last 24 hours.

Only free text is checked. A text that `draft.IsFreeText` says is not free text, such as a version, a URL, an id, a list of names or code, goes through without a server call, because the rule tells agents to use those as they are. A title and body sent together are judged as one text, so a commit with a prose body is still checked even when its subject is only a version.

| Call | Text checked |
|---|---|
| `git commit` | the message from `-m` (several are joined with a blank line), `--message`, `-F` or `--file`. A `-m "$(cat <<'EOF' ... EOF)"` heredoc counts as plain text. |
| `git tag`, `git merge` | the `-m` or `-F` message |
| `gh pr`, `gh issue` create, edit, comment, review and close | the title and body, from the flags or `--body-file` |
| `gh pr merge` | the subject and body |
| `gh release` create and edit | the title and notes |
| `gh api` | `-f` and `-F` fields named `body`, `title`, `message` or `notes`, also nested ones such as `comments[][body]` |
| Gmail MCP `send_message`, `create_draft`, `update_draft`, `reply`, `forward` | subject and body, the reply body, the forward note |

The shell parser reads chains (`&&`, `;`, `|`) and looks through simple wrappers: `env`, `sudo`, `doas`, `timeout`, `nice`, `ionice`, `stdbuf`, `nohup`, `setsid`, `time`, `command`, `builtin`, `exec` and `unbuffer`. `git -C DIR` is followed so a `-F` file is read from the right place.

A call to one of those commands is blocked, with a message that says how to fix it, when the hook can see the text is there but cannot read it: a message built from a variable or a command other than the heredoc `cat`, a message file on stdin or written in the same command line, `git commit` without a message (it would open an editor), `gh api --input` on a write, and Gmail `htmlBody`.

Everything else is let through. That includes text that reaches git or gh through another program: a script run by `bash`, `python` or `node`, `bash -c`, `eval`, `xargs`, `find -exec`, a git alias, a command whose name is a variable, `curl` and `wget`, mail commands, and the browser tools. The rule block asks the agent to send all text through reright, and the hook catches the common direct cases. It is not a sandbox.

## Fail closed

- Unparseable hook input, a checker error, a checker timeout, a panic and an unreadable lock file all deny the call. `RERIGHT_OFFLINE=allow` turns a checker error into an allow. `RERIGHT_BYPASS=1` in the hook's environment skips the text checks.
- The post hook locks a session only when the tool is one of reright's own (server `reright`, or `plugin_<marketplace>_reright`). A result from another tool that contains `{"status":"rejected"}` is ignored.
- Lock files are named by a hash of the session id. A payload without a session id uses a lock keyed by its working directory.
- Locks and pending lists belong to one agent. A call from a subagent carries `agent_id`, and its lock and pending list sit in a folder of their own next to the main thread's. A rejection seen by one subagent locks that subagent only, and the Stop hook of the main thread lists only the drafts the main thread sent.
- A reject lock still lets through `rewrite_result`, `SendMessage`, `AskUserQuestion` and `TaskStop`, which get the usual checks. The agent can check its other drafts, tell the user or other agents, and end its turn. `rewrite`, `keep_original` and every other tool stay blocked, so the rejected text cannot be sent again or used.
- The hook remembers, per session, each draft it has seen rejected, for seven days. Seeing the same rejection again arms no lock, and a batch of rejections makes one lock that names them all. A draft seen rejected counts as resolved, so the Stop hook stops asking for it.
- `reright-hook` gives itself 20 seconds. The PreToolUse entry has a 30 second timeout, so reright's own deny comes first.
- An agent may not change the switches: a shell call that runs `reright disable` or `reright enable`, or names `enforcement.json`, is denied even with every check off. See `switches.md`.

## Self-heal

Once a day, after answering, the hook brings `~/.claude/settings.json` up to the current hook list when reright installed it there and some entries are missing or out of date (an older matcher, a missing Stop hook). It replaces the text between the `reright:start` and `reright:end` markers in `~/.claude/CLAUDE.md` with the current rule block when it differs, and leaves the rest of the file and a file without both markers alone. It also removes the git hooks of reright 0.1 (see `git-layer.md`). `RERIGHT_NO_SELF_HEAL=1` turns this off.
