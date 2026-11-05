package onboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	tomlStart = "# reright:start"
	tomlEnd   = "# reright:end"
	mcpName   = "reright"
)

type part struct {
	Kind  string
	Tag   string
	Apply func([]byte) ([]byte, error)
	Strip func([]byte) ([]byte, error)
	Check func([]byte) error
}

type fileEdit struct {
	Path     string
	Parts    []part
	Private  bool
	Optional bool
}

func (e fileEdit) apply(cur []byte) ([]byte, error) {
	var err error
	for _, p := range e.Parts {
		if cur, err = p.Apply(cur); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func (e fileEdit) strip(cur []byte) ([]byte, error) {
	var err error
	for _, p := range e.Parts {
		if cur, err = p.Strip(cur); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func (e fileEdit) tags() []string {
	var out []string
	for _, p := range e.Parts {
		out = append(out, p.Tag)
	}
	return out
}

func (e fileEdit) kinds() []string {
	var out []string
	for _, p := range e.Parts {
		out = append(out, p.Kind)
	}
	return out
}

func addPart(edits []fileEdit, path string, private, optional bool, p part) []fileEdit {
	for i := range edits {
		if edits[i].Path == path {
			edits[i].Parts = append(edits[i].Parts, p)
			edits[i].Private = edits[i].Private || private
			return edits
		}
	}
	return append(edits, fileEdit{Path: path, Parts: []part{p}, Private: private, Optional: optional})
}

func stripHookEntries(arr []any) ([]any, bool) {
	out := make([]any, 0, len(arr))
	changed := false
	for _, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			out = append(out, el)
			continue
		}
		if leafIsOurs(m) {
			changed = true
			continue
		}
		if inner, ok := m["hooks"].([]any); ok {
			kept, ch := stripHookEntries(inner)
			if ch {
				changed = true
				if len(kept) == 0 {
					continue
				}
				m["hooks"] = kept
			}
		}
		out = append(out, m)
	}
	return out, changed
}

func leafIsOurs(m map[string]any) bool {
	for _, k := range []string{"command", "bash"} {
		if s, _ := m[k].(string); strings.Contains(s, hookMarker) {
			return true
		}
	}
	return false
}

func stripHookTree(m map[string]any) (bool, error) {
	hooks, err := hooksOf(m)
	if err != nil || hooks == nil {
		return false, err
	}
	changed := false
	for ev, v := range hooks {
		arr, ok := v.([]any)
		if !ok {
			return false, errShape
		}
		kept, ch := stripHookEntries(arr)
		if !ch {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if changed && len(hooks) == 0 {
		delete(m, "hooks")
	}
	return changed, nil
}

// stripHookJSON removes reright's entries from any agent's hook file, Claude's or an earlier version's.
func stripHookJSON(raw []byte) ([]byte, error) {
	m, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	changed, err := stripHookTree(m)
	if err != nil || !changed {
		return raw, err
	}
	return encodeSettings(m)
}

func claudeHooksPart(hookPath string) part {
	return part{
		Kind:  "hooks",
		Tag:   "hooks:" + hookMarker,
		Apply: func(raw []byte) ([]byte, error) { return MergeHooks(raw, hookPath) },
		Strip: StripHooks,
		Check: func(raw []byte) error {
			missing, err := MissingHooks(raw, hookPath)
			if err == nil && len(missing) > 0 {
				err = fmt.Errorf("missing these hooks: %s", strings.Join(missing, ", "))
			}
			return err
		},
	}
}

// stripJSONMCP removes the reright server that an earlier version added under topKey (mcpServers or
// servers) for an agent this version no longer sets up.
func stripJSONMCP(topKey string) func([]byte) ([]byte, error) {
	return func(raw []byte) ([]byte, error) {
		m, err := decodeSettings(raw)
		if err != nil {
			return nil, err
		}
		servers, err := serversOf(m, topKey)
		if err != nil || servers == nil {
			return raw, err
		}
		if _, ok := servers[mcpName]; !ok {
			return raw, nil
		}
		delete(servers, mcpName)
		if len(servers) == 0 {
			delete(m, topKey)
		}
		return encodeSettings(m)
	}
}

func serversOf(m map[string]any, key string) (map[string]any, error) {
	v, ok := m[key]
	if !ok {
		return nil, nil
	}
	s, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an object", key)
	}
	return s, nil
}

// tomlBlockPattern finds the block an earlier version wrote into ~/.codex/config.toml.
var tomlBlockPattern = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(tomlStart) + `.*?` + regexp.QuoteMeta(tomlEnd) + `\n?`)

func rulesPart(block string) part {
	return part{
		Kind: "rules",
		Tag:  "rules:" + blockStart,
		Apply: func(raw []byte) ([]byte, error) {
			return []byte(addBlockText(string(raw), block)), nil
		},
		Strip: func(raw []byte) ([]byte, error) {
			return []byte(StripBlock(string(raw))), nil
		},
		Check: func(raw []byte) error {
			if !HasBlock(string(raw)) {
				return fmt.Errorf("no reright rule block")
			}
			return nil
		},
	}
}

func addBlockText(md, block string) string {
	md = StripBlock(md)
	if md != "" && !strings.HasSuffix(md, "\n") {
		md += "\n"
	}
	if md != "" {
		md += "\n"
	}
	return md + block
}

func effectivelyEmpty(b []byte) bool {
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return true
	}
	var m map[string]any
	if json.Unmarshal(t, &m) != nil {
		return false
	}
	for k := range m {
		if k != "version" {
			return false
		}
	}
	return true
}

func stripperForTag(tag string) func([]byte) ([]byte, error) {
	kind, rest, _ := strings.Cut(tag, ":")
	switch kind {
	case "hooks":
		return stripHookJSON
	case "rules":
		return func(raw []byte) ([]byte, error) { return []byte(StripBlock(string(raw))), nil }
	case "mcp":
		top, _, _ := strings.Cut(rest, ".")
		if top == "mcp_servers" {
			return func(raw []byte) ([]byte, error) { return tomlBlockPattern.ReplaceAll(raw, nil), nil }
		}
		return stripJSONMCP(top)
	}
	return func(raw []byte) ([]byte, error) { return raw, nil }
}

func stripAll(tags []string, raw []byte) ([]byte, error) {
	var err error
	for _, t := range tags {
		if raw, err = stripperForTag(t)(raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}
