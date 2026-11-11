package harnesssetup

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"github.com/OrlojHQ/meridian/images"
)

// Manifest is one harness pack's personal-setup layout (ADR 0040). Paths in
// Paths and ConfigFiles are relative to Root.Path. HomeFiles and Shared are
// relative to the home. Manifests are compiled in from images/capsule-*/ and
// cannot widen the fixed deny list in AllowedPath.
type Manifest struct {
	Version     string       `yaml:"version"`
	Name        string       `yaml:"name"`
	Root        ManifestRoot `yaml:"root"`
	Paths       []string     `yaml:"paths"`
	ConfigFiles []string     `yaml:"configFiles"`
	ConfigNames []string     `yaml:"configNames"`
	HomeFiles   []string     `yaml:"homeFiles"`
	Shared      []string     `yaml:"shared"`
	// Instructions is the root-relative file capsuled creates empty when a
	// provider connection needs a home and no setup was saved. Empty means
	// the connection gets an empty home.
	Instructions string             `yaml:"instructions"`
	Connection   ManifestConnection `yaml:"connection"`
}

// ManifestRoot is the harness configuration directory. Env names the
// variable that overrides it on the laptop and that capsuled sets in the
// Capsule. With XDGConfig, Path is under .config, the laptop default follows
// XDG_CONFIG_HOME, and the Capsule relies on XDG_CONFIG_HOME instead of Env.
type ManifestRoot struct {
	Path      string `yaml:"path"`
	Env       string `yaml:"env"`
	XDGConfig bool   `yaml:"xdgConfig"`
}

type ManifestConnection struct {
	Providers []ManifestProvider `yaml:"providers"`
}

// ManifestProvider is a provider connection type the harness can use.
// RequiresModel rejects a connection without a default model.
type ManifestProvider struct {
	Type          string `yaml:"type"`
	RequiresModel bool   `yaml:"requiresModel"`
}

var (
	manifestName = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)
	manifestEnv  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// providerTypes are the provider connection types Meridian stores.
	providerTypes = []string{"anthropic", "anthropic-compatible", "chatgpt", "openai", "openai-compatible"}
)

type manifestSet struct {
	byName map[string]Manifest
	names  []string
	errors []error
}

var loadManifests = sync.OnceValue(func() manifestSet {
	return parseManifests(images.Manifests)
})

// parseManifests reads capsule-*/manifest.yaml. An invalid manifest is
// dropped and recorded: its harness is then unsupported, which fails closed.
func parseManifests(fsys fs.FS) manifestSet {
	set := manifestSet{byName: map[string]Manifest{}}
	matches, err := fs.Glob(fsys, "capsule-*/manifest.yaml")
	if err != nil {
		set.errors = append(set.errors, err)
		return set
	}
	envs := map[string]string{}
	for _, file := range matches {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			set.errors = append(set.errors, fmt.Errorf("%s: %w", file, err))
			continue
		}
		var m Manifest
		if err := yaml.UnmarshalWithOptions(raw, &m, yaml.Strict()); err != nil {
			set.errors = append(set.errors, fmt.Errorf("%s: %w", file, err))
			continue
		}
		if dir := path.Dir(file); dir != "capsule-"+m.Name {
			set.errors = append(set.errors, fmt.Errorf("%s: name %q does not match its pack directory", file, m.Name))
			continue
		}
		if err := m.validate(); err != nil {
			set.errors = append(set.errors, fmt.Errorf("%s: %w", file, err))
			continue
		}
		if m.Root.Env != "" {
			if other, ok := envs[m.Root.Env]; ok {
				set.errors = append(set.errors, fmt.Errorf("%s: env %s is also used by %s", file, m.Root.Env, other))
				continue
			}
			envs[m.Root.Env] = m.Name
		}
		set.byName[m.Name] = m
		set.names = append(set.names, m.Name)
	}
	slices.Sort(set.names)
	return set
}

// homePath accepts a clean, relative path that stays in the home and has no
// segment the fixed deny list would refuse.
func homePath(p string) bool {
	if p == "" || len(p) > 256 || path.IsAbs(p) || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	return !denied(p)
}

func configExtension(p string) bool {
	switch path.Ext(p) {
	case ".toml", ".json", ".jsonc", ".yaml", ".yml":
		return true
	}
	return false
}

// reservedEnv would let a manifest redirect the process rather than a
// harness configuration directory.
func reservedEnv(name string) bool {
	switch name {
	case "HOME", "PATH", "SHELL", "USER", "TMPDIR", "PWD":
		return true
	}
	for _, prefix := range []string{"XDG_", "MERIDIAN_", "LD_", "DYLD_", "NODE_", "SSL_", "GIT_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (m Manifest) validate() error {
	if m.Version != "v1" {
		return fmt.Errorf("unsupported manifest version %q", m.Version)
	}
	if !manifestName.MatchString(m.Name) {
		return fmt.Errorf("invalid harness name %q", m.Name)
	}
	if !homePath(m.Root.Path) {
		return fmt.Errorf("root path %q leaves the home or is denied", m.Root.Path)
	}
	if m.Root.Env != "" && (!manifestEnv.MatchString(m.Root.Env) || reservedEnv(m.Root.Env)) {
		return fmt.Errorf("invalid root env %q", m.Root.Env)
	}
	if m.Root.XDGConfig && !strings.HasPrefix(m.Root.Path, ".config/") {
		return fmt.Errorf("xdgConfig root must be under .config/")
	}
	if len(m.Paths) == 0 || len(m.Paths) > 64 {
		return fmt.Errorf("paths must list 1 to 64 entries")
	}
	files := map[string]bool{}
	for _, entry := range m.Paths {
		trimmed := strings.TrimSuffix(entry, "/")
		if !homePath(trimmed) || strings.Contains(entry, "//") {
			return fmt.Errorf("path %q leaves the root or is denied", entry)
		}
		if files[entry] {
			return fmt.Errorf("duplicate path %q", entry)
		}
		files[entry] = true
	}
	for _, p := range m.ConfigFiles {
		if !files[p] || strings.HasSuffix(p, "/") || !configExtension(p) {
			return fmt.Errorf("config file %q must be a listed file with a toml, json, jsonc, or yaml extension", p)
		}
	}
	for _, name := range m.ConfigNames {
		if strings.Contains(name, "/") || !homePath(name) || !configExtension(name) {
			return fmt.Errorf("config name %q must be a file name with a toml, json, jsonc, or yaml extension", name)
		}
	}
	for _, p := range m.HomeFiles {
		if strings.Contains(p, "/") || !homePath(p) || !configExtension(p) || p == m.Root.Path {
			return fmt.Errorf("home file %q must be a configuration file directly in the home", p)
		}
	}
	for _, p := range m.Shared {
		if !homePath(p) || p == m.Root.Path || strings.HasPrefix(m.Root.Path, p+"/") || strings.HasPrefix(p, m.Root.Path+"/") {
			return fmt.Errorf("shared directory %q must be a separate directory in the home", p)
		}
	}
	if m.Instructions != "" && (!files[m.Instructions] || strings.HasSuffix(m.Instructions, "/") || m.isConfig(m.Root.Path+"/"+m.Instructions)) {
		return fmt.Errorf("instructions %q must be a listed, non-configuration file", m.Instructions)
	}
	seen := map[string]bool{}
	for _, provider := range m.Connection.Providers {
		if !slices.Contains(providerTypes, provider.Type) || seen[provider.Type] {
			return fmt.Errorf("invalid or duplicate connection provider %q", provider.Type)
		}
		seen[provider.Type] = true
	}
	return nil
}

// allowed reports whether a home-relative path is one this manifest imports.
// The caller applies the fixed checks first.
func (m Manifest) allowed(p string) bool {
	for _, entry := range m.Paths {
		full := m.Root.Path + "/" + entry
		if strings.HasSuffix(full, "/") && strings.HasPrefix(p, full) || p == full {
			return true
		}
	}
	for _, dir := range m.Shared {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return slices.Contains(m.HomeFiles, p)
}

func (m Manifest) isConfig(p string) bool {
	if slices.Contains(m.HomeFiles, p) {
		return true
	}
	relative, ok := strings.CutPrefix(p, m.Root.Path+"/")
	if !ok {
		return false
	}
	return slices.Contains(m.ConfigFiles, relative) || slices.Contains(m.ConfigNames, path.Base(relative))
}

// supports reports whether this harness can use a provider connection type.
func (m Manifest) supports(provider, model string) bool {
	for _, p := range m.Connection.Providers {
		if p.Type == provider {
			return !p.RequiresModel || model != ""
		}
	}
	return false
}

// LookupManifest returns the manifest for an official harness name.
func LookupManifest(name string) (Manifest, bool) {
	m, ok := loadManifests().byName[name]
	return m, ok
}

// Harnesses lists the harnesses with a manifest, sorted.
func Harnesses() []string {
	return slices.Clone(loadManifests().names)
}

// CapsuleEnvironment is each manifest's root variable pointing into a
// private Capsule home. XDG-based roots are covered by XDG_CONFIG_HOME.
func CapsuleEnvironment(home string) map[string]string {
	env := map[string]string{}
	for _, m := range loadManifests().byName {
		if m.Root.Env != "" && !m.Root.XDGConfig {
			env[m.Root.Env] = home + "/" + m.Root.Path
		}
	}
	return env
}
