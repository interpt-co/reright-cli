package onboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct{ Home string }

func (p Paths) ConfigDir() string    { return filepath.Join(p.Home, ".config", "reright") }
func (p Paths) TokenFile() string    { return filepath.Join(p.ConfigDir(), "token") }
func (p Paths) URLFile() string      { return filepath.Join(p.ConfigDir(), "url") }
func (p Paths) ManifestFile() string { return filepath.Join(p.ConfigDir(), "install.json") }
func (p Paths) BackupDir() string    { return filepath.Join(p.ConfigDir(), "backup") }
func (p Paths) StateDir() string     { return filepath.Join(p.Home, ".local", "state", "reright") }
func (p Paths) Settings() string     { return filepath.Join(p.Home, ".claude", "settings.json") }
func (p Paths) ClaudeMD() string     { return filepath.Join(p.Home, ".claude", "CLAUDE.md") }
func (p Paths) DefaultHook() string  { return filepath.Join(p.Home, ".local", "bin", "reright-hook") }

type fileRecord struct {
	Existed  bool   `json:"existed"`
	AfterSHA string `json:"after_sha"`
}

type FileRecord struct {
	Path     string   `json:"path"`
	Kinds    []string `json:"kinds"`
	Tags     []string `json:"tags"`
	Existed  bool     `json:"existed"`
	Mode     uint32   `json:"mode,omitempty"`
	Backup   string   `json:"backup,omitempty"`
	AfterSHA string   `json:"after_sha"`
	Link     string   `json:"link,omitempty"`
}

type AgentRecord struct {
	Files  []FileRecord `json:"files"`
	MCPCLI bool         `json:"mcp_cli,omitempty"`
	Manual []string     `json:"manual,omitempty"`
}

type Manifest struct {
	Server   string                  `json:"server"`
	HookPath string                  `json:"hook_path"`
	Settings *fileRecord             `json:"settings,omitempty"`
	ClaudeMD *fileRecord             `json:"claude_md,omitempty"`
	Agents   map[string]*AgentRecord `json:"agents,omitempty"`
	Git      string                  `json:"git,omitempty"`
}

func (m *Manifest) migrate(p Paths) {
	if m.Settings == nil && m.ClaudeMD == nil {
		return
	}
	if m.Agents == nil {
		m.Agents = map[string]*AgentRecord{}
	}
	if _, ok := m.Agents[agentClaude]; !ok {
		rec := &AgentRecord{MCPCLI: true}
		if m.Settings != nil {
			rec.Files = append(rec.Files, FileRecord{Path: p.Settings(), Kinds: []string{"hooks"}, Tags: []string{"hooks:" + hookMarker}, Existed: m.Settings.Existed, Mode: currentMode(p.Settings()), Backup: filepath.Join(p.BackupDir(), "settings.json"), AfterSHA: m.Settings.AfterSHA})
		}
		if m.ClaudeMD != nil {
			rec.Files = append(rec.Files, FileRecord{Path: p.ClaudeMD(), Kinds: []string{"rules"}, Tags: []string{"rules:" + blockStart}, Existed: m.ClaudeMD.Existed, Mode: currentMode(p.ClaudeMD()), Backup: filepath.Join(p.BackupDir(), "CLAUDE.md"), AfterSHA: m.ClaudeMD.AfterSHA})
		}
		m.Agents[agentClaude] = rec
	}
	m.Settings, m.ClaudeMD = nil, nil
}

func currentMode(path string) uint32 {
	if info, err := os.Stat(path); err == nil {
		return uint32(info.Mode().Perm())
	}
	return 0
}

func (m Manifest) validate(p Paths) error {
	if m.HookPath != "" && !filepath.IsAbs(m.HookPath) {
		return fmt.Errorf("hook_path %q is not absolute", m.HookPath)
	}
	home := filepath.Clean(p.Home) + string(filepath.Separator)
	for name, rec := range m.Agents {
		if !validAgent(name) {
			return fmt.Errorf("unknown agent %q", name)
		}
		if rec == nil {
			return fmt.Errorf("agent %q has no record", name)
		}
		for _, f := range rec.Files {
			clean := filepath.Clean(f.Path)
			if f.Path == "" || !filepath.IsAbs(f.Path) || !strings.HasPrefix(clean, home) {
				return fmt.Errorf("agent %q lists the file %q, which is not an absolute path inside %s", name, f.Path, p.Home)
			}
			if f.Existed && f.Backup != "" && !strings.HasPrefix(filepath.Clean(f.Backup), filepath.Clean(p.BackupDir())+string(filepath.Separator)) {
				return fmt.Errorf("agent %q lists the backup %q, which is outside %s", name, f.Backup, p.BackupDir())
			}
		}
	}
	return nil
}

func (m *Manifest) record(agent string) *AgentRecord {
	if m.Agents == nil {
		m.Agents = map[string]*AgentRecord{}
	}
	if m.Agents[agent] == nil {
		m.Agents[agent] = &AgentRecord{}
	}
	return m.Agents[agent]
}

func (a *AgentRecord) file(path string) *FileRecord {
	for i := range a.Files {
		if a.Files[i].Path == path {
			return &a.Files[i]
		}
	}
	return nil
}

func (m Manifest) agentNames() []string {
	var out []string
	for _, n := range agentOrder {
		if _, ok := m.Agents[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func readOptional(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reright-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFile(path, data, 0o600)
}

var errBadManifest = errors.New("the install record is damaged")

func loadManifest(p Paths) (Manifest, bool, error) {
	b, ok, err := readOptional(p.ManifestFile())
	if err != nil || !ok {
		return Manifest{}, false, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("%w: %v", errBadManifest, err)
	}
	m.migrate(p)
	if err := m.validate(p); err != nil {
		return Manifest{}, false, fmt.Errorf("%w: %v", errBadManifest, err)
	}
	return m, true, nil
}

func saveManifest(p Paths, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(p.ManifestFile(), append(b, '\n'))
}

func readSecret(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
