package githook

type Marker struct {
	Agent    string
	Var      string
	Values   []string
	Verified bool
	Source   string
}

var AgentMarkers = []Marker{
	{Agent: "Claude Code", Var: "CLAUDECODE", Verified: true, Source: "present in a Claude Code Bash tool shell, checked 2026-09-30"},
	{Agent: "Claude Code", Var: "CLAUDE_CODE_ENTRYPOINT", Verified: true, Source: "present in a Claude Code Bash tool shell, checked 2026-09-30"},
	{Agent: "Claude Code", Var: "CLAUDE_CODE_SESSION_ID", Verified: true, Source: "present in a Claude Code Bash tool shell, checked 2026-09-30"},
	{Agent: "Claude Cowork", Var: "CLAUDE_CODE_IS_COWORK", Source: "UNVERIFIED: third-party detection list (github.com/sdairs/is-ai-agent)"},
	{Agent: "OpenAI Codex CLI", Var: "CODEX_SANDBOX", Source: "UNVERIFIED: not in the official docs; third-party detection list"},
	{Agent: "OpenAI Codex CLI", Var: "CODEX_SANDBOX_NETWORK_DISABLED", Source: "UNVERIFIED: reported in openai/codex issues (#10390, #30356), set by the seatbelt and Windows sandboxes only"},
	{Agent: "OpenAI Codex CLI", Var: "CODEX_THREAD_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "OpenAI Codex CLI", Var: "CODEX_CI", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Gemini CLI", Var: "GEMINI_CLI", Source: "UNVERIFIED first hand: search summaries of the run_shell_command docs (geminicli.com/docs/tools/shell/) say it sets GEMINI_CLI=1"},
	{Agent: "Cursor", Var: "CURSOR_AGENT", Source: "UNVERIFIED first hand: Cursor forum threads; one reports the Cursor CLI not setting it"},
	{Agent: "Cursor CLI", Var: "CURSOR_SANDBOX", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "GitHub Copilot", Var: "COPILOT_AGENT_SESSION_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "GitHub Copilot", Var: "COPILOT_AGENT", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "GitHub Copilot CLI", Var: "COPILOT_CLI", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "OpenCode", Var: "OPENCODE", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "OpenCode", Var: "OPENCODE_CLIENT", Source: "UNVERIFIED: agents.md issue #136"},
	{Agent: "Goose", Var: "GOOSE_TERMINAL", Source: "UNVERIFIED: agents.md issue #136 and third-party detection list"},
	{Agent: "Amp", Var: "AMP_CURRENT_THREAD_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Generic AGENT convention", Var: "AGENT", Values: []string{"goose", "amp", "claude", "claude-code", "codex", "cursor", "cursor-cli", "gemini-cli", "augment", "cline", "opencode"}, Source: "UNVERIFIED: agents.md issue #136 proposal; Goose and Amp set it, Claude Code has an open request (anthropics/claude-code #24838)"},
	{Agent: "Generic AI_AGENT convention", Var: "AI_AGENT", Source: "UNVERIFIED: agent detection libraries"},
	{Agent: "Cline", Var: "CLINE_ACTIVE", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Roo Code", Var: "ROO_CODE_TASK_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Kilo Code", Var: "KILO", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Augment", Var: "AUGMENT_AGENT", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Qwen Code", Var: "QWEN_CODE", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Antigravity", Var: "ANTIGRAVITY_AGENT", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Crush", Var: "CRUSH", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Warp agent", Var: "OZ_RUN_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Junie", Var: "JUNIE_SHIM_PATH", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Kiro", Var: "KIRO_AGENT_PATH", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Trae", Var: "TRAE_AI_SHELL_ID", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Pi", Var: "PI_CODING_AGENT", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "Hermes Agent", Var: "HERMES_AGENT", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "OpenClaw", Var: "OPENCLAW_SHELL", Source: "UNVERIFIED: third-party detection list"},
	{Agent: "CodeBuddy", Var: "CODEBUDDY", Source: "UNVERIFIED: third-party detection list"},
}

func DetectAgent(getenv func(string) string) (string, bool) {
	for _, m := range AgentMarkers {
		v := getenv(m.Var)
		if v == "" {
			continue
		}
		if len(m.Values) == 0 {
			return m.Agent + " (" + m.Var + ")", true
		}
		for _, want := range m.Values {
			if v == want {
				return m.Agent + " (" + m.Var + "=" + v + ")", true
			}
		}
	}
	return "", false
}
