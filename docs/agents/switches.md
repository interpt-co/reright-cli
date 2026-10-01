# Switching checks off and on

Some work does not need the review step: an internal repo where nobody reads the commit messages, a burst of scratch commits. `reright disable` turns the checks off for the kinds of text you name, and `reright enable` turns them back on. A change applies to running sessions at once, with no restart.

```
reright status                                  what is on and what is off
reright disable commit                          commit messages, everywhere, until you enable them
reright disable commit --for 2h                 switches itself back on in two hours
reright disable commit --here                   only inside the current directory tree
reright disable commit gh --dir ~/work/internal only inside that directory (repeatable)
reright disable all --for 30m                   everything, including blocks no single kind describes
reright enable commit                           back on
reright enable all
```

## Kinds

| Kind | Covers |
|---|---|
| `commit` | git commit messages, from agent hooks and from the git layer |
| `gh` | text in `gh pr`, `gh issue`, `gh release` and `gh api` calls |
| `email` | Gmail send, draft, reply and forward |
| `browser` | text typed or inserted in the browser |
| `http` | `curl` and `wget` requests that send a body to a remote host |
| `all` | every kind above |

## What a switch does and does not do

- Only the text checks are skipped for the kinds that are off. A reject lock still blocks the session, because a rejection is a stop, not a text check.
- A block caused by something the hook cannot read (a variable in a commit message, say) is lifted when every kind it might have been sending is off. `reright disable all` lifts any block that no single kind describes.
- With `--dir`, a call counts only when its working directory is inside one of the directories. The git layer uses the directory `git commit` runs in.
- If the state file is damaged, every kind stays on. `reright enable all` rewrites it.

## Only the user can change it

The agent cannot turn checks off. The hook denies a shell call that runs `reright disable` or `reright enable`, or that mentions `enforcement.json`, and tells the agent to ask you. Run the command in your own terminal. In Claude Code, `! reright disable commit --for 1h` runs it in the session without going through the hook.

When the hook blocks a call it cannot read, its message now names the construct and what to do, and says which `reright disable` command would lift it. The agent passes that on to you.

The state is `~/.config/reright/enforcement.json`, mode 600. An agent with a file-edit tool, rather than a shell, can still write to it. The hook only sees shell and MCP calls, so that path is not covered. `reright doctor` warns while anything is switched off, so a forgotten switch shows up.

`RERIGHT_BYPASS=1` still works, but only when it is in the environment the hook runs in. Putting it in front of a command does nothing, because the hook is a separate process started by the agent.
