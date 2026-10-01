# Hook adapters

`reright-hook pre`, `post` and `prompt` read one agent's hook payload on stdin and answer in that agent's format. The agent is chosen with `--agent NAME`, before or after the subcommand. Without the flag it is `claude`, so existing Claude Code installs keep working. Any other flag, a second subcommand, a repeated `--agent` or an unknown agent name is a usage error. A usage error exits 2 for `pre`, for an unknown subcommand and when there is no subcommand (a block in every agent that has exit codes), and 1 for `post` and `prompt`.

Every adapter turns the payload into the same internal call: tool name, input object, working directory and session id. Shell tools become `Bash` with a `command` string, and MCP tools become `mcp__server__tool`, which is what `internal/hookcheck` already understands. The checks, the reject lock and the escape hatches (`RERIGHT_BYPASS`, `RERIGHT_OFFLINE`) are shared.

Nothing here was run against a real agent. No other agent CLI is installed on this machine. Every payload and deny format below comes from the vendors' documentation as summarised in the research file, and the tests use fixtures copied from those descriptions. Treat every row as documented, not proven, until the checklist at the end has been run.

## Events wired per agent

| Agent | Block before a tool call | Reject lock (post) | Clear lock (prompt) | Deny answer |
|---|---|---|---|---|
| claude | PreToolUse | PostToolUse on `mcp__reright__wait_for_review` | UserPromptSubmit | JSON `hookSpecificOutput`, exit 0 |
| codex | PreToolUse | PostToolUse | UserPromptSubmit | Same JSON as Claude, exit 0 |
| gemini | BeforeTool | AfterTool | BeforeAgent | JSON `{"decision":"deny","reason"}` on stdout, reason on stderr, exit 2 |
| copilot (CLI, camelCase) | preToolUse | postToolUse | userPromptSubmitted | JSON `permissionDecision` and `permissionDecisionReason`, exit 0 |
| copilot (VS Code, snake_case) | PreToolUse | PostToolUse | UserPromptSubmit | JSON `hookSpecificOutput`, exit 0 |
| cursor | beforeShellExecution, beforeMCPExecution, preToolUse | afterMCPExecution | beforeSubmitPrompt | JSON `permission`, `user_message`, `agent_message`, exit 0 |

The Copilot adapter tells the two payload styles apart by the presence of `toolName`. If the payload cannot be parsed at all, it prints both deny shapes in one JSON object.

Cursor answers `{"continue":true}` to `beforeSubmitPrompt` after clearing the lock. Allowed calls print nothing in every agent. That is on purpose: an explicit `allow` decision can skip the agent's own permission prompt, which reright must not do.

## Not wired

| Agent | What is missing | Why |
|---|---|---|
| cursor | `preToolUse` in the generated config | The adapter accepts it, but the config leaves it out so shell and MCP calls are not checked twice. Add it if the two specific events turn out not to cover something. |
| copilot | Cloud agent hooks | Repo-level files only, which the agent can edit. Out of scope by rule. |
| windsurf, junie, goose, amp, opencode, cline, continue | Everything | Not started. Windsurf and Junie payloads are UNVERIFIED in the research, and the rest need a different mechanism (delegate programs, JS plugins). |
| codex, gemini, copilot, cursor | Repo-level config | Never generated. An agent can edit files in the repo, so only user-level paths are used. |

## How tool names are mapped

| Agent | Shell | MCP |
|---|---|---|
| claude | `Bash` | `mcp__server__tool` |
| codex | `Bash`, the names in the next paragraph, and any other non-MCP tool whose input has a `command` or `cmd` | `mcp__server__tool` |
| gemini | `run_shell_command` | `mcp_context.server_name` and `mcp_context.tool_name` when present, else `mcp_<server>_<tool>` split by a heuristic |
| copilot | `bash`, `powershell`, `runTerminalCommand`, `run_in_terminal` and similar (case-insensitive) | `mcp_<server>_<tool>` or `server/tool` |
| cursor | `beforeShellExecution` has a raw `command`. `preToolUse` names it `Shell`. | `beforeMCPExecution` has `server` and `tool_name`. Falls back to `url` or `command` as the server. |

Shell tools are recognised by name (`shell`, `local_shell`, `exec_command`, `shell_command`, `container.exec`, `unified_exec`, `execute`, `runCommands`, `run_terminal_cmd` and the Copilot and Gemini names above) and also by shape: any non-MCP tool, whatever its name, whose input has a non-null `command` or `cmd` is treated as a shell call. If both keys (or `script`, `commandLine`) are present they must agree, otherwise the call is denied, and a value that is not a string or a list of strings is denied. An argv array is turned into a shell line by quoting every element, so `["git","commit","-m","a b"]` is read as one message `a b`, and wrappers such as `["bash","-ic","..."]` or `["env","X=1","bash","-c","..."]` are unwrapped by the Bash inspector.

Names of the form `mcp_<server>_<tool>` are ambiguous when the server name contains underscores. The adapter cuts at the server names it has checks for (`gmail`, `workspace`, `claude-in-chrome`, `claude_in_chrome`) and otherwise at the first underscore. That is enough for the Gmail and browser checks. A shell tool call whose arguments hold no recognisable command is denied, not passed.

## Fail closed

- The post hook locks a session only when the tool is reright's `wait_for_review` (server name `reright`, or `plugin_<marketplace>_reright` for the Claude Code plugin). A result from any other tool that happens to contain `{"status":"rejected"}` is ignored. If an agent ever omits the tool name in its post payload, no lock is written.
- Lock files are named by a hash of the session id, so long or odd ids are safe. A payload with no session id uses a lock keyed by its working directory (one shared lock if that is missing too). That can block an unrelated id-less session in the same directory after a reject, and the next prompt clears it.
- Unparseable stdin, a checker error, a checker timeout, a panic and an unreadable lock all produce a deny in the agent's own format. `RERIGHT_OFFLINE=allow` still turns a checker error into an allow, as before.
- `reright-hook` gives itself 20 seconds. The generated configs set the agent's timeout to 30 seconds, so reright's own deny comes first. Copilot's documentation says a timeout there fails open.
- Cursor: the generated `hooks.json` sets `failClosed: true` on `beforeShellExecution` and `beforeMCPExecution`. Cursor's other failing exit codes fail open without it.
- Gemini: only exit 2 blocks, so a deny uses exit 2. Any other failing exit only warns.
- Configs are user-level only: `~/.codex/hooks.json`, `~/.gemini/settings.json`, `~/.copilot/hooks/`, `~/.cursor/hooks.json`. `internal/agentcfg.For(agent, bin)` returns the text and the path relative to home. It writes nothing. Files marked merge are fragments for a file that usually has other settings.

## Verified from documentation only, and UNVERIFIED

Everything in the payload and deny columns above is documentation only. These points are the ones most likely to be wrong.

| Claim | Status |
|---|---|
| Codex payload and deny JSON match Claude Code's | Documentation only. Whether the model sees the deny reason: UNVERIFIED. |
| Codex names shell tools `Bash` | Documentation only. The extra names (`shell`, `local_shell`, `exec_command`) are guesses. |
| Codex hooks apply with `--dangerously-bypass-approvals-and-sandbox` | UNVERIFIED |
| Gemini `BeforeTool` fields `tool_name`, `tool_input`, `mcp_context` | Documentation only. The keys inside `mcp_context` are UNVERIFIED. |
| Gemini `AfterTool` carries `tool_response` and `BeforeAgent` carries `prompt` | UNVERIFIED. The adapter reads several response keys and does not need `prompt`. |
| Gemini exit 2 plus stderr reaches the model as a tool error | Documentation only. Stdout JSON printed next to it may be ignored. |
| Gemini hooks apply with `--yolo` | UNVERIFIED |
| Copilot CLI `toolArgs` is a JSON string, and an object is also accepted here | The string form is documented. The object form is a tolerance, not a claim. |
| Copilot empty stdout with exit 0 counts as allow | UNVERIFIED |
| Copilot `postToolUse` carries `toolResult.textResultForLlm` | UNVERIFIED. Any of several response keys is searched. |
| Copilot MCP tool naming (`mcp_<server>_<tool>` or `server/tool`) and whether hooks fire for MCP tools at all | UNVERIFIED |
| Copilot VS Code user-level hook location, and the field names `sessionId` and `hook_event_name` | UNVERIFIED |
| Copilot hooks apply with `--allow-all-tools` | UNVERIFIED |
| Cursor `beforeShellExecution` fields `command`, `cwd` and the deny keys | Documentation only |
| Cursor `beforeMCPExecution` carries the server name | UNVERIFIED. The research says "tool name, parameters and server config". Without an explicit name the adapter uses the server name `unknown`, and the Gmail check (by tool name: `send_message`, `create_draft`, `update_draft`, `reply`, `forward`) and the browser check both run on that call. Gmail servers are matched by a server name that contains `gmail` or `workspace`, and the browser server as `claude-in-chrome` or `claude_in_chrome`, all case-insensitively. |
| Cursor `tool_input` on MCP events is a JSON string | UNVERIFIED. Both a string and an object are accepted. |
| Cursor `afterMCPExecution` carries `result_json` | UNVERIFIED |
| Cursor `preToolUse` names the shell tool `Shell` and MCP tools `MCP:server:tool` | UNVERIFIED |
| Cursor hooks apply in auto-run mode, and `beforeMCPExecution` in cloud agents | UNVERIFIED. The research says cloud agents do not support it. |
| Codex matcher `*` | Not shown to be valid in the docs, and a regex engine can reject it. The Codex pre hook has no matcher, which matches everything. The Codex post matcher is the plain name `mcp__reright__wait_for_review`. UNVERIFIED. |
| Matchers on Cursor `afterMCPExecution` and Copilot `postToolUse` (`.*wait_for_review`) | UNVERIFIED that these events take a matcher and what it is matched against. The post hook checks the tool name itself, so a matcher that is ignored costs nothing and a matcher that never matches leaves the reject lock unset. |
| Copilot reads `~/.copilot/hooks` for both the CLI and VS Code, and VS Code accepts the camelCase shape | UNVERIFIED. Only `reright.json` (CLI shape) is generated, so the hooks cannot be registered twice. If VS Code turns out to need its own file, add it only after checking that the CLI ignores it. |
| The wait_for_review reject is detected in each agent's post payload | Tests use invented but plausible shapes. Real shapes are UNVERIFIED. |

## Checklist for a human with the real CLIs

Run each step in a scratch repo with a throwaway session. Use `RERIGHT_URL` pointing at a local test server, or a token whose approved list you control. Back up the agent's config first, then write the snippet from `agentcfg.For`.

1. Install the snippet at the user-level path and start the agent. Confirm the agent lists or trusts the hook (Codex asks for trust).
2. Ask the agent to run `git commit -m "hello"`. Expect a block, with the reright reason visible to the model. Record whether the model quotes it.
3. Approve the text in reright and repeat. Expect the commit to go through with no extra prompt caused by reright.
4. Ask for a harmless command such as `ls`. Expect no block and no server call.
5. Call a Gmail or other MCP send tool if one is configured. Record the exact tool name the agent shows and compare it with the mapping table. Fix `mcpFromUnderscores` or the Cursor server fallback if it differs.
6. Stop the reright server and repeat step 2. Expect a deny that names the connection error.
7. Make the hook slow (a sleep before the check) beyond the agent's timeout. Record whether the agent blocks (Cursor with `failClosed`) or allows (Copilot). Record the result here.
8. Have the agent call `wait_for_review` on a draft you reject. Expect the next tool call in that session to be denied with the draft id. Then type a new prompt and expect it to work again. Record the post and prompt payloads with `tee` and compare them with the fixtures in `internal/hookcli/agents_test.go`.
9. Start the agent in its no-approval mode (Codex bypass flag, Gemini `--yolo`, Copilot `--allow-all-tools`, Cursor auto-run). Repeat step 2. Record whether the hook still fires.
10. For Copilot, run once in the CLI and once in VS Code, and confirm the matching deny shape was accepted.
11. For Cursor, confirm the user-level `~/.cursor/hooks.json` is picked up when the repo has none, and that `beforeSubmitPrompt` accepts `{"continue":true}`.
12. Update the table above: change each UNVERIFIED that passed to verified, with the date and agent version.
