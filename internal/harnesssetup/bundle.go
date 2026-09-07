// Package harnesssetup describes portable personal harness configuration.
// Bundles never contain authentication caches or execute during discovery.
package harnesssetup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxBytes = 512 << 10
const MaxFiles = 256

type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}
type Bundle struct {
	Dependencies []string `json:"dependencies,omitempty"`
	Harness      string   `json:"harness"`
	Files        []File   `json:"files"`
}
type RuntimeSetup struct {
	Revision string `json:"revision"`
	Bundle   Bundle `json:"bundle"`
}

func Supported(name string) bool {
	switch name {
	case "claude", "codex", "opencode", "pi":
		return true
	}
	return false
}
func (b Bundle) Validate() error {
	if !Supported(b.Harness) || len(b.Files) == 0 || len(b.Files) > MaxFiles {
		return errors.New("invalid harness or file count")
	}
	if len(b.Dependencies) > 16 {
		return errors.New("too many npm dependencies")
	}
	packages := map[string]bool{}
	for _, pkg := range b.Dependencies {
		if !ValidPackage(pkg) || packages[pkg] {
			return errors.New("dependencies must be unique exact npm package versions")
		}
		packages[pkg] = true
	}
	required, err := DependenciesForFiles(b.Files)
	if err != nil {
		return err
	}
	for _, pkg := range required {
		if !packages[pkg] {
			return errors.New("MCP dependency is absent from the import")
		}
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range b.Files {
		if !AllowedPath(b.Harness, f.Path) || seen[f.Path] || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return errors.New("invalid setup file")
		}
		if len(f.Content) > 128<<10 {
			return errors.New("setup file exceeds 128 KiB")
		}
		seen[f.Path] = true
		total += len(f.Content)
		if total > MaxBytes {
			return errors.New("setup exceeds 512 KiB")
		}
		if IsConfig(f.Path) {
			refs, err := ConfigReferences(f.Path, f.Content)
			if err != nil {
				return errors.New("invalid configuration")
			}
			for _, ref := range refs {
				found := false
				for _, candidate := range b.Files {
					if candidate.Path == ref || strings.HasPrefix(candidate.Path, ref+"/") {
						found = true
						break
					}
				}
				if !found {
					return errors.New("configuration references a file absent from the import")
				}
			}

			_, issues, err := Sanitize(b.Harness, f.Path, f.Content)
			if err != nil || len(issues) > 0 {
				return errors.New("configuration contains unsupported or unsafe fields")
			}
		}
		if Sensitive(f.Content) {
			return errors.New("setup contains credential-shaped content; remove it and use a provider connection")
		}
	}
	return nil
}
func AllowedPath(harness, p string) bool {
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	if p == "" || len(p) > 1024 || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00\r\n") || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	var roots []string
	switch harness {
	case "codex":
		roots = []string{".codex/config.toml", ".codex/AGENTS.md", ".codex/AGENTS.override.md", ".codex/skills/", ".codex/prompts/", ".codex/agents/", ".agents/skills/"}
	case "claude":
		roots = []string{".claude.json", ".claude/settings.json", ".claude/CLAUDE.md", ".claude/skills/", ".claude/commands/", ".claude/agents/", ".claude/hooks/"}
	case "opencode":
		roots = []string{".config/opencode/opencode.json", ".config/opencode/opencode.jsonc", ".config/opencode/AGENTS.md", ".config/opencode/agents/", ".config/opencode/commands/", ".config/opencode/skills/", ".config/opencode/plugins/", ".config/opencode/themes/", ".config/opencode/agent/", ".config/opencode/command/", ".config/opencode/plugin/", ".config/opencode/skill/"}
	case "pi":
		roots = []string{".pi/agent/settings.json", ".pi/agent/AGENTS.md", ".pi/agent/models.json", ".pi/agent/skills/", ".pi/agent/prompts/", ".pi/agent/extensions/", ".pi/agent/themes/"}
	}
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, ".env") || lower == "auth.json" || lower == ".credentials.json" || lower == "node_modules" || lower == ".git" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
			return false
		}
	}
	for _, root := range roots {
		if strings.HasSuffix(root, "/") && strings.HasPrefix(p, root) || p == root {
			return true
		}
	}
	return false
}

// Sensitive is defense in depth. Import also parses settings and excludes
// credential fields; this cannot certify arbitrary user-authored text secret-free.
func Sensitive(s string) bool {
	lower := strings.ToLower(s)
	for _, marker := range []string{"-----begin private key", "-----begin rsa private key", "sk-ant-", "sk-proj-", "ghp_", "github_pat_", "\"access_token\"", "\"refresh_token\"", "\"authorization\"", "bearer "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
func (b Bundle) Digest() string {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Connection carries only an ephemeral gateway lease, never an upstream key.
type Connection struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Token    string `json:"token"`
}
