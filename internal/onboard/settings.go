package onboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const hookMarker = "reright-hook"

const waitMatcher = "mcp__reright__wait_for_review"

var errShape = errors.New("settings.json does not have the expected shape (hooks must be an object of lists)")

var safeShell = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if safeShell.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func decodeSettings(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		if isJSONC(raw) {
			return nil, errors.New("the file is not valid JSON because it has comments or trailing commas (JSONC). reright cannot edit it without losing them, so it left the file alone. Remove the comments, or add the reright entries by hand")
		}
		return nil, fmt.Errorf("the file is not valid JSON: %w", err)
	}
	if m == nil {
		return nil, errShape
	}
	return m, nil
}

func encodeSettings(m map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hooksOf(m map[string]any) (map[string]any, error) {
	v, ok := m["hooks"]
	if !ok {
		return nil, nil
	}
	h, ok := v.(map[string]any)
	if !ok {
		return nil, errShape
	}
	return h, nil
}

func isOurs(entry any) bool {
	e, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := e["command"].(string)
	return strings.Contains(cmd, hookMarker)
}

func stripOurs(m map[string]any) (bool, error) {
	hooks, err := hooksOf(m)
	if err != nil || hooks == nil {
		return false, err
	}
	changed := false
	for event, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			return false, errShape
		}
		var keptGroups []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			inner, ok := gm["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			var kept []any
			for _, h := range inner {
				if isOurs(h) {
					changed = true
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == len(inner) {
				keptGroups = append(keptGroups, g)
				continue
			}
			if len(kept) > 0 {
				gm["hooks"] = kept
				keptGroups = append(keptGroups, gm)
			}
		}
		if len(keptGroups) == 0 && len(groups) > 0 {
			delete(hooks, event)
		} else if len(keptGroups) != len(groups) || changed {
			hooks[event] = keptGroups
		}
	}
	if changed && len(hooks) == 0 {
		delete(m, "hooks")
	}
	return changed, nil
}

func StripHooks(raw []byte) ([]byte, error) {
	m, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	changed, err := stripOurs(m)
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	return encodeSettings(m)
}

type hookSpec struct {
	Event   string
	Matcher string
	Sub     string
	Timeout int
}

var hookSpecs = []hookSpec{
	{"PreToolUse", "*", "pre", 30},
	{"PostToolUse", waitMatcher, "post", 10},
	{"UserPromptSubmit", "", "prompt", 10},
}

func (s hookSpec) command(hookPath string) string {
	return shellQuote(hookPath) + " " + s.Sub
}

func MergeHooks(raw []byte, hookPath string) ([]byte, error) {
	m, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	if _, err := stripOurs(m); err != nil {
		return nil, err
	}
	hooks, err := hooksOf(m)
	if err != nil {
		return nil, err
	}
	if hooks == nil {
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	for _, s := range hookSpecs {
		group := map[string]any{}
		if s.Matcher != "" {
			group["matcher"] = s.Matcher
		}
		group["hooks"] = []any{map[string]any{"type": "command", "command": s.command(hookPath), "timeout": s.Timeout}}
		existing, _ := hooks[s.Event].([]any)
		hooks[s.Event] = append(existing, group)
	}
	return encodeSettings(m)
}

func MissingHooks(raw []byte, hookPath string) ([]string, error) {
	m, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	hooks, err := hooksOf(m)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, s := range hookSpecs {
		if !hasHook(hooks, s, hookPath) {
			missing = append(missing, s.Event+" ("+s.Sub+")")
		}
	}
	return missing, nil
}

func hasHook(hooks map[string]any, s hookSpec, hookPath string) bool {
	groups, _ := hooks[s.Event].([]any)
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if m, _ := gm["matcher"].(string); m != s.Matcher {
			continue
		}
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); sameCommand(cmd, s.command(hookPath)) {
				return true
			}
		}
	}
	return false
}
