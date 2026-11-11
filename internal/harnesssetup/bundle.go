// Package harnesssetup describes portable personal harness configuration.
// Bundles never contain authentication caches or execute during discovery.
package harnesssetup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"slices"
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

// Supported reports whether a harness has a pack manifest (ADR 0040).
func Supported(name string) bool {
	_, ok := LookupManifest(name)
	return ok
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
	if denied(p) {
		return false
	}
	m, ok := LookupManifest(harness)
	return ok && m.allowed(p)
}

// denied is the fixed deny list. A manifest can never import these names.
func denied(p string) bool {
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, ".env") || lower == "auth.json" || strings.HasPrefix(lower, ".credentials") || lower == "node_modules" || lower == ".git" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
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

// Connection carries only an ephemeral gateway lease, never an upstream key
// or a custom upstream address.
type Connection struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Model    string `json:"model,omitempty"`
	// Efforts are the reasoning effort levels the model accepts, validated
	// by ValidEfforts. Empty means the harness's own view of the model.
	Efforts []string `json:"efforts,omitempty"`
	// CA is the PEM certificate set the harness must trust for URL, set
	// only for the local provider gateway (ADR 0037).
	CA string `json:"ca,omitempty"`
}

// EffortLevels is the effort vocabulary a connection may declare, from least
// to greatest. Every harness that reads a connection's levels shares these
// names: Pi and DeepSeek Harness thinking levels, OpenCode variants, and
// Codex's reasoning efforts. "off" is always offered and never declared.
var EffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// ValidEfforts accepts a non-empty list of distinct EffortLevels and returns
// it in canonical order.
func ValidEfforts(levels []string) ([]string, bool) {
	if len(levels) == 0 || len(levels) > len(EffortLevels) {
		return nil, false
	}
	seen := map[string]bool{}
	for _, level := range levels {
		if seen[level] || !slices.Contains(EffortLevels, level) {
			return nil, false
		}
		seen[level] = true
	}
	ordered := make([]string, 0, len(levels))
	for _, level := range EffortLevels {
		if seen[level] {
			ordered = append(ordered, level)
		}
	}
	return ordered, true
}

// ValidModel bounds a configured model name. It is written into native
// harness configuration and the process environment.
func ValidModel(model string) bool {
	if len(model) == 0 || len(model) > 256 {
		return false
	}
	for i, r := range model {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._:/@+-", r)) {
			return false
		}
	}
	return true
}

// ConnectionHarness reports whether capsuled can configure the harness for a
// provider connection. Every harness with personal setups is included.
func ConnectionHarness(name string) bool {
	return Supported(name)
}

// ConnectionSupported pairs a harness with the provider connection types its
// manifest declares. OpenCode and Pi need a model name to register a custom
// OpenAI-compatible provider. DeepSeek Harness reaches the gateway only
// through a custom endpoint route, which names its model.
func ConnectionSupported(harness, provider, model string) bool {
	m, ok := LookupManifest(harness)
	return ok && m.supports(provider, model)
}
