# The git layer

This is a git hook that checks commit messages against reright. It works for any agent that runs `git commit`, including agents that have no hook system of their own. It covers commit text and nothing else. PR text, issue text, comments and email need the agent-specific hooks or the reright tools.

The code is in `internal/githook`. The hook is called as `reright-hook git commit-msg FILE` and `reright-hook git prepare-commit-msg FILE SOURCE`.

## What it does

When a commit is enforced, the hook reads the message file and cleans it up the way git does. It drops comment lines, removes trailing whitespace, collapses runs of blank lines and trims blank lines at both ends. The result goes through `draft.Hash`, so it matches the hash reright computed when the reviewer approved the text. The hook then asks the server, through the same `/api/check` call the Claude Code hook uses, whether that hash was approved in the last 24 hours.

If it was not approved, the hook prints a message to stderr and exits 1, which makes git abort the commit. The message tells the agent to call `submit_for_review` with kind `commit`, wait with `wait_for_review`, and commit again with `final_text` exactly as returned.

If the server cannot be reached, or there is no API token, the hook fails closed and aborts the commit. Setting `RERIGHT_OFFLINE=allow` lets the commit through, the same as in the Claude Code hook.

There is one exception. When the only reason a commit is enforced is that it has no terminal (rule 6 below), nothing says an agent made it. It could be a person in an IDE. For those commits the wait is capped at 3 seconds, and if the server does not answer in that time the commit goes through with a line on stderr saying so. A commit that is unapproved while the server is up is still blocked, and the message then tells a person how to set `tty_heuristic`. Commits from an agent marker, `RERIGHT_ENFORCE=1` or `mode: always` keep failing closed with the 20 second limit.

How git cleans a message depends on how the commit was made. With `-m` or `-F` it only removes trailing whitespace, so a line starting with `#` stays in the commit. With an editor it also removes comment lines. The hook follows `commit.cleanup` if it is set. Otherwise it treats a message file that contains git's "Please enter the commit message" template as an editor commit, and everything else as `-m` or `-F`. `core.commentChar` and `core.commentString` are respected.

### Commits the hook skips

- An empty message after cleanup.
- Merge commits, reverts, cherry-picks, rebases and `git am` and `git pull`. The hook looks for `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `rebase-merge` and `rebase-apply` in the git directory, for a `GIT_REFLOG_ACTION` that starts with one of those verbs, and for the parent `git` process being one of those verbs. The last check is needed because a clean `git revert` and a clean `git cherry-pick` leave no marker file behind.
- An amend where the message is unchanged. The hook compares the cleaned message with the message at `HEAD`. If the parent `git` process can be read (Linux and other systems with `/proc`), it also requires `--amend` on its command line, so a new commit that reuses the previous message is still checked. Where the parent cannot be read, equality with `HEAD` alone is enough.

## When it enforces

Humans are not blocked by default. The hook decides in this order and stops at the first match:

1. `mode` is `off` in `~/.config/reright/git.json`: never enforce.
2. `RERIGHT_BYPASS=1`: never enforce.
3. `RERIGHT_ENFORCE=1`: enforce.
4. `mode` is `always`: enforce.
5. A known agent environment marker is set: enforce.
6. The hook is not attached to a terminal: enforce.
7. Otherwise: let the commit through.

`git.json` looks like this. Every field is optional, and a missing or broken file means `agents`.

```json
{"mode": "agents", "tty_heuristic": true}
```

`mode` is `agents` (default), `always` or `off`. Setting `tty_heuristic` to `false` turns off rule 6 and keeps only the markers and the explicit variables.

### The terminal check

Git does not pass a terminal to hooks. It connects the hook's stdin to `/dev/null` and sends its stdout to stderr, so checking stdin and stdout would make every commit look non-interactive. The hook checks stderr instead, which git passes through unchanged. When a person runs `git commit` in a terminal, stderr is that terminal. When an agent runs it and captures the output, stderr is a pipe.

The check has a cost. Commits made from a GUI or an IDE (PyCharm, VS Code, git gui) have no terminal either, so in `agents` mode they are enforced. Someone who commits from an IDE should set `tty_heuristic` to `false` or `mode` to `off`. Install and `reright doctor` both say this. An agent that runs inside a pseudo-terminal looks like a person to rule 6, so it is only caught by a marker.

### Agent markers

The list is data in `internal/githook/markers.go` (`AgentMarkers`). Each entry has the variable, optional allowed values, a verified flag and the source of the claim. Only one agent could be checked first hand. The rest come from documentation summaries and from a third-party detection list (github.com/sdairs/is-ai-agent), read on 2026-09-30, and are marked UNVERIFIED in the code. A variable that an agent stops setting, or never set for shell commands, silently stops working.

| Agent | Variable | Status |
|---|---|---|
| Claude Code | `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_SESSION_ID` | Verified, present in a Claude Code Bash shell |
| Claude Cowork | `CLAUDE_CODE_IS_COWORK` | UNVERIFIED |
| OpenAI Codex CLI | `CODEX_SANDBOX`, `CODEX_SANDBOX_NETWORK_DISABLED`, `CODEX_THREAD_ID`, `CODEX_CI` | UNVERIFIED. `CODEX_SANDBOX` is not documented. The network variable is reported to be set only by the macOS seatbelt and Windows sandboxes, so Codex on Linux without a sandbox may set none of these |
| Gemini CLI | `GEMINI_CLI` | UNVERIFIED first hand. Search summaries of the shell tool docs say it sets `GEMINI_CLI=1` |
| Cursor | `CURSOR_AGENT` | UNVERIFIED first hand. A Cursor forum thread reports the Cursor CLI not setting it |
| Cursor CLI | `CURSOR_SANDBOX` | UNVERIFIED |
| GitHub Copilot | `COPILOT_AGENT_SESSION_ID`, `COPILOT_AGENT`, `COPILOT_CLI` | UNVERIFIED |
| OpenCode | `OPENCODE`, `OPENCODE_CLIENT` | UNVERIFIED |
| Goose | `GOOSE_TERMINAL` | UNVERIFIED |
| Amp | `AMP_CURRENT_THREAD_ID` | UNVERIFIED |
| Generic convention | `AGENT` with a known value (goose, amp, claude, claude-code, codex, cursor, cursor-cli, gemini-cli, augment, cline, opencode), `AI_AGENT` | UNVERIFIED. It is a proposal (agents.md issue 136) that Goose and Amp follow |
| Others | `CLINE_ACTIVE`, `ROO_CODE_TASK_ID`, `KILO`, `AUGMENT_AGENT`, `QWEN_CODE`, `ANTIGRAVITY_AGENT`, `CRUSH`, `OZ_RUN_ID`, `JUNIE_SHIM_PATH`, `KIRO_AGENT_PATH`, `TRAE_AI_SHELL_ID`, `PI_CODING_AGENT`, `HERMES_AGENT`, `OPENCLAW_SHELL`, `CODEBUDDY` | UNVERIFIED |

Agents that set no variable, such as Aider, rely on the terminal check. Aider also commits with `--no-verify` by default, which is the next problem.

## The prepare-commit-msg experiment

Question: does `prepare-commit-msg` still run under `git commit --no-verify`? The research note said so from memory and asked for a test.

Setup: git 2.47.3, a temp repo with `pre-commit`, `prepare-commit-msg`, `commit-msg` and `post-commit` hooks that each print their name and arguments.

| Command | Hooks that ran |
|---|---|
| `git commit -m two` | pre-commit, prepare-commit-msg (source `message`), commit-msg, post-commit |
| `git commit --no-verify -m one` | prepare-commit-msg (source `message`), post-commit |
| `git commit --amend --no-verify -m three` | prepare-commit-msg (source `message`), post-commit |
| `git merge --no-verify --no-ff br -m merged` | prepare-commit-msg, post-commit |
| `git cherry-pick --no-verify br` | prepare-commit-msg (source `message`), post-commit |
| `git commit-tree -m four HEAD^{tree}` | none |

Result: `--no-verify` skips `pre-commit` and `commit-msg` and nothing else. `prepare-commit-msg` still runs and can abort the commit. So it is used as a second check.

The limit is what it can see. For `-m` and `-F` git passes source `message` and the file holds the final text, so the hook checks it. For an editor commit the file holds only the template when `prepare-commit-msg` runs, and the text is typed afterwards. The hook therefore checks `prepare-commit-msg` only when the source is `message` and leaves other sources to `commit-msg`. The gap this leaves is `git commit --no-verify` with no `-m` and no `-F`, where an agent supplies the text through `GIT_EDITOR` or a template. The tests show this gap on purpose (`TestGitNoVerify`).

A normal commit is checked twice, once in each hook, which costs two requests to the server. The second one is cheap, but it is there.

## What bypasses it

- `git commit --no-verify` with the message written by an editor command. `-m` and `-F` are still caught by `prepare-commit-msg`.
- Plumbing such as `git commit-tree` and `git fast-import`. No hook runs.
- Commits made through an API, such as the GitHub web editor, `gh api`, or a GitHub MCP server. Squash-merge messages written on GitHub never reach a local hook.
- `git -c core.hooksPath=/dev/null commit`, or `git config core.hooksPath` changed in the repository or globally.
- Any repository that sets its own `core.hooksPath`, which husky does. A local setting wins over the global one, so git never calls the reright wrappers there and the layer does nothing in that repository. Only the agent hooks (and the rule text) apply. This is the main hole in the layer. `reright doctor` warns when you run it inside such a repository, but it cannot see the repositories it is not run in, and the global check says so.
- Editing or deleting the wrapper scripts in `~/.config/reright/git-hooks/`, or the hook files they chain to.
- `RERIGHT_BYPASS=1` in the environment, `mode: "off"` in `git.json`. An agent that can run a shell can set any of these.
- A wrapper path that changes: if `reright-hook` moves and the installer is not run again, the wrapper fails and blocks every enforced commit until it is reinstalled.

## Other limits

- The heuristic can be spoofed or missed. An agent can unset its marker variables, run under a pseudo-terminal, or be started by a tool that sets nothing. A human in a GUI can be blocked by mistake.
- Trailers added by git are part of the message. `git commit -s` and `--trailer` append lines to the file before `commit-msg` runs, so the hash no longer matches the text that was approved unless the approved text already has those lines.
- Approval is for the exact text. If the agent rewraps or edits the approved message, the hash changes and the commit is blocked again. That is intended.
- This layer is a safety net for mistakes and careless paths. It does not stop an agent that wants to get around it.

## Install and uninstall

`internal/githook` exposes three functions for the installer. They do not touch anything outside the home directory they are given.

- `Install(homeDir, hookBinaryPath)` writes a wrapper for every standard git hook into `~/.config/reright/git-hooks/`, because a global `core.hooksPath` replaces each repository's `.git/hooks` and would otherwise silence all of them. The binary path has to be absolute. The wrapped hooks are commit-msg, prepare-commit-msg, applypatch-msg, pre-applypatch, post-applypatch, pre-commit, pre-merge-commit, post-commit, pre-rebase, post-checkout, post-merge, pre-push, pre-receive, update, post-receive, post-update, reference-transaction, pre-auto-gc, post-rewrite, sendemail-validate and post-index-change.
  - Only `commit-msg` and `prepare-commit-msg` call reright. Before doing so, the wrapper skips the check without starting the binary when `RERIGHT_BYPASS=1` or `git.json` has `"mode": "off"`. If the binary is missing or not executable, it prints a warning and goes on, so a removed binary cannot break commits in every repository. If the binary runs and fails, the commit is aborted.
  - Every wrapper then runs the repository's own hook of the same name with the original arguments and standard input (`exec`, so its exit code and stdin are kept). That hook is found in the local `core.hooksPath` when it is set but not pointing at the wrapper directory, or else in `<git common dir>/hooks`. The wrapper does not run a hook that is the wrapper itself. This keeps pre-commit, pre-push, post-commit, post-checkout, git-lfs and similar hooks working in normal repositories. A repository with a local `core.hooksPath` does not use the wrappers at all (see above).
  - Not wrapped: `fsmonitor-watchman`, `push-to-checkout`, `proc-receive` and the `p4-*` hooks. Git treats an existing hook of those names as a replacement for built-in behavior, so a wrapper that does nothing would break it. A repository's own version of those is silenced while the layer is active.
  - Each wrapper costs one or two extra `git` calls. `reference-transaction` runs several times per commit, so it is the most noticeable.
- If no global `core.hooksPath` is set, `Install` sets it to that directory. If one is already set, it changes nothing and returns the lines to add to the existing hooks directory in `Result.Manual` (only for `commit-msg` and `prepare-commit-msg`, written to skip themselves when the binary is missing). The directory is also stored as `ManualDir`.
- `Uninstall(homeDir)` removes the wrappers and the state file. If it set `core.hooksPath`, it restores the global git config to the bytes it had before. If the file was edited after install, it removes only the `core.hooksPath` key and leaves the edits. Whatever the state file says, it finally unsets the global `core.hooksPath` when that still points at the wrapper directory, so a damaged or missing state file cannot leave it pointing at a deleted folder. `UninstallNotes` does the same and also returns notes: a damaged state file that was recovered from, and any hook file in the hooks directory you added lines to by hand that still call reright-hook. Remove those lines, because they now call a binary that is gone.
- `Status(homeDir)` reports whether the wrappers are installed, whether git is using them, the current global `core.hooksPath` and the `git.json` mode.

The record of the previous state is `~/.config/reright/git-install.json`, written with mode 600 in a folder of mode 700 that `Install` creates first. A damaged state file is an error for `Install` (it says to run uninstall first). `Install` refuses to overwrite a file in the hooks directory that it did not write.

Tests run against a temporary HOME with `GIT_CONFIG_GLOBAL` pointing at a file in it, and use real git in temporary repositories: a normal commit, `-m`, `-F`, an editor commit, amend, merge, cherry-pick, revert, rebase with a conflict, `--no-verify`, `commit-tree`, an empty message, repository hooks, and a local `core.hooksPath`. The chaining tests check with real git (and a local bare remote) that pre-commit, post-commit, post-checkout, pre-push with its stdin, and reference-transaction still run after install and stop being wrapped after uninstall, that a missing or failing binary does not start under bypass or mode off, that a damaged state file is recovered, and that manual lines are listed.

A test that sets a local `core.hooksPath` shows the hole: the repository's hook runs, but only because git calls it directly, not through the wrapper. The layer itself does nothing there. There is no `git-hook adopt` command. Husky-style directories are often generated, so editing them is not safe to automate. To cover such a repository, add `if [ -x BIN ]; then RERIGHT_GIT_PPID=$PPID BIN git commit-msg "$@" || exit $?; fi` (and the same for `prepare-commit-msg`) to its hook files by hand, with `BIN` the path of `reright-hook`.
