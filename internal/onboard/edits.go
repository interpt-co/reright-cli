package onboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
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

func mergeHookJSON(raw []byte, content string) ([]byte, error) {
	m, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	want, err := decodeSettings([]byte(content))
	if err != nil {
		return nil, err
	}
	if _, err := stripHookTree(m); err != nil {
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
	wantHooks, _ := want["hooks"].(map[string]any)
	for ev, v := range wantHooks {
		existing, _ := hooks[ev].([]any)
		add, _ := v.([]any)
		hooks[ev] = append(existing, add...)
	}
	for k, v := range want {
		if _, ok := m[k]; !ok && k != "hooks" {
			m[k] = v
		}
	}
	return encodeSettings(m)
}

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

type hookLeaf struct {
	Command    string
	FailClosed bool
}

func leavesOf(v any, out *[]hookLeaf) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range []string{"command", "bash"} {
			if s, ok := t[k].(string); ok {
				fc, _ := t["failClosed"].(bool)
				*out = append(*out, hookLeaf{s, fc})
			}
		}
		for _, c := range t {
			leavesOf(c, out)
		}
	case []any:
		for _, c := range t {
			leavesOf(c, out)
		}
	}
}

func hookLeaves(m map[string]any) []hookLeaf {
	var out []hookLeaf
	leavesOf(m["hooks"], &out)
	return out
}

func hooksCheck(content string) func([]byte) error {
	return func(raw []byte) error {
		want, err := decodeSettings([]byte(content))
		if err != nil {
			return err
		}
		have, err := decodeSettings(raw)
		if err != nil {
			return err
		}
		var got []hookLeaf
		for _, l := range hookLeaves(have) {
			got = append(got, l)
		}
		var missing, open []string
		for _, l := range hookLeaves(want) {
			var g hookLeaf
			ok := false
			for _, cand := range got {
				if sameCommand(cand.Command, l.Command) {
					g, ok = cand, true
					break
				}
			}
			switch {
			case !ok:
				missing = append(missing, l.Command)
			case l.FailClosed && !g.FailClosed:
				open = append(open, l.Command)
			}
		}
		switch {
		case len(missing) > 0:
			return fmt.Errorf("missing or different hook commands: %s", strings.Join(missing, "; "))
		case len(open) > 0:
			return fmt.Errorf("these hooks lost failClosed, so a crash or timeout would let the call through: %s", strings.Join(open, "; "))
		}
		return nil
	}
}

func jsonHooksPart(content string) part {
	return part{
		Kind:  "hooks",
		Tag:   "hooks:" + hookMarker,
		Apply: func(raw []byte) ([]byte, error) { return mergeHookJSON(raw, content) },
		Strip: stripHookJSON,
		Check: hooksCheck(content),
	}
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

func jsonMCPPart(topKey string, entry map[string]any) part {
	return part{
		Kind: "mcp",
		Tag:  "mcp:" + topKey + "." + mcpName,
		Apply: func(raw []byte) ([]byte, error) {
			m, err := decodeSettings(raw)
			if err != nil {
				return nil, err
			}
			servers, err := serversOf(m, topKey)
			if err != nil {
				return nil, err
			}
			if servers == nil {
				servers = map[string]any{}
				m[topKey] = servers
			}
			servers[mcpName] = entry
			return encodeSettings(m)
		},
		Strip: func(raw []byte) ([]byte, error) {
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
		},
		Check: func(raw []byte) error {
			m, err := decodeSettings(raw)
			if err != nil {
				return err
			}
			servers, err := serversOf(m, topKey)
			if err != nil {
				return err
			}
			have, ok := servers[mcpName]
			if !ok {
				return fmt.Errorf("no %s.%s entry", topKey, mcpName)
			}
			a, _ := json.Marshal(have)
			b, _ := json.Marshal(entry)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("the %s.%s entry differs from the one install writes (stale URL or device token)", topKey, mcpName)
			}
			return nil
		},
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

var tomlBlockPattern = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(tomlStart) + `.*?` + regexp.QuoteMeta(tomlEnd) + `\n?`)

func tomlBlock(url, token string) string {
	return fmt.Sprintf("%s\n[mcp_servers.%s]\nurl = %q\nhttp_headers = { Authorization = %q }\n%s\n", tomlStart, mcpName, url, "Bearer "+token, tomlEnd)
}

func tomlMCPPart(url, token string) part {
	block := tomlBlock(url, token)
	strip := func(raw []byte) ([]byte, error) {
		return tomlBlockPattern.ReplaceAll(raw, nil), nil
	}
	return part{
		Kind: "mcp",
		Tag:  "mcp:mcp_servers." + mcpName,
		Apply: func(raw []byte) ([]byte, error) {
			cur, _ := strip(raw)
			var doc map[string]any
			if err := toml.Unmarshal(cur, &doc); err != nil {
				return nil, fmt.Errorf("the file is not valid TOML (%v). reright will not edit it. Fix the file, or add the [mcp_servers.%s] table by hand", err, mcpName)
			}
			if servers, ok := doc["mcp_servers"]; ok {
				table, isTable := servers.(map[string]any)
				if !isTable {
					return nil, fmt.Errorf("mcp_servers is not a table, so reright cannot add [mcp_servers.%s]. Fix the file, then run install again", mcpName)
				}
				if _, found := table[mcpName]; found {
					return nil, fmt.Errorf("it already defines [mcp_servers.%s] and reright did not write it. Remove that table or rename it, then run install again", mcpName)
				}
			}
			s := string(cur)
			next := block
			if strings.TrimSpace(s) != "" {
				if !strings.HasSuffix(s, "\n") {
					s += "\n"
				}
				next = s + "\n" + block
			}
			var check map[string]any
			if err := toml.Unmarshal([]byte(next), &check); err != nil {
				return nil, fmt.Errorf("adding [mcp_servers.%s] would make the file invalid TOML (%v), so reright left it alone. Add the table by hand", mcpName, err)
			}
			return []byte(next), nil
		},
		Strip: strip,
		Check: func(raw []byte) error {
			if !bytes.Contains(raw, []byte(block)) {
				if bytes.Contains(raw, []byte(tomlStart)) {
					return fmt.Errorf("the [mcp_servers.%s] block differs from the one install writes (stale URL or device token)", mcpName)
				}
				return fmt.Errorf("no [mcp_servers.%s] block", mcpName)
			}
			return nil
		},
	}
}

var featuresSection = regexp.MustCompile(`(?ms)^\[features\]\s*\n(.*?)(?:^\[|\z)`)
var hooksOff = regexp.MustCompile(`(?m)^\s*(hooks|codex_hooks)\s*=\s*false\b`)

func codexHooksDisabled(raw []byte) bool {
	for _, m := range featuresSection.FindAllSubmatch(raw, -1) {
		if hooksOff.Match(m[1]) {
			return true
		}
	}
	return false
}

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
			return tomlMCPPart("", "").Strip
		}
		return jsonMCPPart(top, nil).Strip
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
