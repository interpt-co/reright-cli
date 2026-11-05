# The old git layer

reright 0.1 installed a git layer: wrapper scripts in `~/.config/reright/git-hooks`, a record in `~/.config/reright/git-install.json`, and the global `core.hooksPath` pointing at the wrappers. The `commit-msg` and `prepare-commit-msg` wrappers ran `reright-hook git NAME "$@"` and then handed over to the repository's own hook. When `core.hooksPath` was already set to something else, install left it alone and printed two lines for the user to add by hand.

This version does not install it. Commit messages are checked by the Claude Code hook (see `hook.md`). The code that is left is in `internal/githook`.

## Removal

`reright upgrade`, `reright install` and `reright uninstall` remove the layer, and so does the hook's daily self-heal, because the upgrade to this version was run by a 0.1 `reright` that does not know to do it. `reright upgrade` removes it even when the programs are already current. `reright upgrade --check` only says that it is there.

1. If `git-install.json` says reright set `core.hooksPath` and the global git config file is byte for byte what reright left, the copy taken before is written back. That brings back an earlier `core.hooksPath`, or removes a config file reright created.
2. Otherwise, if the global `core.hooksPath` still points at the wrappers, it is unset.
3. A `core.hooksPath` that points anywhere else (husky, a dotfiles hooks directory) is left as it is, and so are the files in it. The output lists any `commit-msg` or `prepare-commit-msg` there that still calls `reright-hook`, so you can take that line out.
4. Only then are the wrappers, `git-install.json` and the old mode file `git.json` deleted, so git is never left pointing at a missing directory.

`reright doctor` warns while any of it is still there, with `Fix: run reright upgrade`.

## The pass-through

Until the layer is removed, its wrappers keep calling `reright-hook git commit-msg FILE`. That call no longer checks anything and never fails a commit:

- Called from a 0.1 wrapper or a hand-added line (both set `RERIGHT_GIT_PPID`), it exits 0, because the wrapper runs the repository's own hook itself right after.
- Called any other way, it runs the repository's own hook (from its local `core.hooksPath`, else `.git/hooks`) with the same arguments and stdin and exits with its code, or 0 when there is none. It never runs a file in reright's wrapper directory, and a hook it starts that calls it again gets exit 0.
- A missing or odd hook name prints a line on stderr and exits 0.

Keep the pass-through until no 0.1 install is left. The lines people added by hand to their own hooks call it with `|| exit $?`, and self-heal removes the layer without telling them about those lines, so taking `reright-hook git` away would make a usage error fail their commits.
