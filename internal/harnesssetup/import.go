package harnesssetup

import (
	"encoding/json"
	"fmt"
	"github.com/goccy/go-yaml"
	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Issue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Preview struct {
	Warnings     []Issue  `json:"warnings"`
	Dependencies []string `json:"dependencies"`
	Bundle       Bundle   `json:"-"`
	Harness      string   `json:"harness"`
	Files        []string `json:"files"`
	Issues       []Issue  `json:"issues"`
	Digest       string   `json:"digest"`
}

// Discover reads only explicit harness roots, never the user's entire home.
// env is injected so tests and callers need not mutate process-global state.
func Discover(home, harness string, env func(string) string) (Preview, error) {
	result := Preview{Harness: harness, Bundle: Bundle{Harness: harness}, Files: []string{}, Issues: []Issue{}}
	m, ok := LookupManifest(harness)
	if !ok {
		return result, fmt.Errorf("unsupported harness %q", harness)
	}
	target := m.Root.Path
	rootName := ""
	if m.Root.Env != "" {
		rootName = env(m.Root.Env)
	}
	if rootName == "" {
		rootName = filepath.Join(home, filepath.FromSlash(target))
		if m.Root.XDGConfig {
			if xdg := env("XDG_CONFIG_HOME"); xdg != "" {
				rootName = filepath.Join(xdg, filepath.FromSlash(strings.TrimPrefix(target, ".config/")))
			}
		}
	}
	rootName, err := filepath.Abs(rootName)
	if err != nil {
		return result, err
	}
	root, err := os.OpenRoot(rootName)
	if err != nil {
		return result, err
	}
	defer root.Close()
	count := 0
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("cannot read configuration entry")
		}
		if p == "." {
			return nil
		}
		count++
		if count > 4096 {
			return fmt.Errorf("configuration has too many entries")
		}
		dest := target + "/" + p
		if d.Type()&os.ModeSymlink != 0 {
			result.Issues = append(result.Issues, Issue{dest, "Symbolic link excluded; copy portable source into this configuration directory"})
			return nil
		}
		if d.IsDir() {
			if !AllowedPath(harness, dest+"/entry") {
				result.Issues = append(result.Issues, Issue{dest, "Runtime state or unsupported directory excluded"})
				return fs.SkipDir
			}
			return nil
		}
		if !AllowedPath(harness, dest) {
			result.Issues = append(result.Issues, Issue{dest, "Authentication, runtime state, or unsupported file excluded"})
			return nil
		}
		if !d.Type().IsRegular() {
			result.Issues = append(result.Issues, Issue{dest, "Non-regular file excluded"})
			return nil
		}
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, (128<<10)+1))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(raw) > 128<<10 {
			result.Issues = append(result.Issues, Issue{dest, "File exceeds 128 KiB"})
			return nil
		}
		content := string(raw)
		if IsConfig(dest) {
			relocated, relocationErr := RelocateConfig(harness, dest, content, rootName, target)
			if relocationErr == nil {
				content = relocated
			}
			var issues []Issue
			content, issues, err = Sanitize(harness, dest, content)
			result.Issues = append(result.Issues, issues...)
			if err != nil {
				result.Issues = append(result.Issues, Issue{dest, "Configuration could not be parsed"})
				return nil
			}
		}
		item := File{Path: dest, Content: content, Executable: info.Mode()&0o111 != 0}
		if !IsConfig(dest) && (Bundle{Harness: harness, Files: []File{item}}).Validate() != nil {
			result.Issues = append(result.Issues, Issue{dest, "Nonportable or credential-shaped content excluded"})
			return nil
		}
		total += len(content)
		if total > MaxBytes || len(result.Bundle.Files) >= MaxFiles {
			return fmt.Errorf("configuration exceeds import limits")
		}
		result.Bundle.Files = append(result.Bundle.Files, item)
		result.Files = append(result.Files, dest)
		return nil
	})
	if err == nil && len(m.HomeFiles) > 0 {
		if homeErr := collectHomeFiles(home, harness, m.HomeFiles, &result); homeErr != nil {
			return result, homeErr
		}
	}
	if err == nil {
		for _, dir := range m.Shared {
			if sharedErr := collectShared(home, harness, dir, &result); sharedErr != nil {
				return result, sharedErr
			}
		}
	}
	if err == nil {
		for i, file := range result.Bundle.Files {
			if IsConfig(file.Path) {
				content, issues, pruneErr := pruneMissingReferences(file.Path, file.Content, result.Bundle.Files)
				if pruneErr != nil {
					return result, pruneErr
				}
				result.Bundle.Files[i].Content = content
				result.Warnings = append(result.Warnings, issues...)
				result.Warnings = append(result.Warnings, PortabilityWarnings(file.Path, content)...)
			}
		}
	}
	if err == nil {
		packages, dependencyErr := DependenciesForFiles(result.Bundle.Files)
		if dependencyErr != nil {
			return result, dependencyErr
		}
		result.Bundle.Dependencies = packages
		result.Dependencies = packages
	}
	if err == nil && len(result.Bundle.Files) > 0 {
		if validationErr := result.Bundle.Validate(); validationErr != nil {
			return result, validationErr
		}
	}
	sort.Slice(result.Bundle.Files, func(i, j int) bool { return result.Bundle.Files[i].Path < result.Bundle.Files[j].Path })
	sort.Strings(result.Files)
	result.Digest = result.Bundle.Digest()
	return result, err
}

// IsConfig reports whether any manifest parses and sanitizes this
// home-relative path as configuration.
func IsConfig(p string) bool {
	for _, m := range loadManifests().byName {
		if m.isConfig(p) {
			return true
		}
	}
	return false
}
func yamlConfig(p string) bool {
	return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")
}
func parseConfig(p, content string) (any, error) {
	if yamlConfig(p) && strings.Contains(content, "!!js") {
		return nil, fmt.Errorf("executable YAML tags")
	}
	switch {
	case strings.HasSuffix(p, ".toml"):
		value := map[string]any{}
		err := toml.Unmarshal([]byte(content), &value)
		return value, err
	case yamlConfig(p):
		var value any
		err := yaml.Unmarshal([]byte(content), &value)
		return value, err
	default:
		var value any
		err := json.Unmarshal(jsonc.ToJSON([]byte(content)), &value)
		return value, err
	}
}

// collectHomeFiles reads configuration files that live directly in the
// home, such as Claude's .claude.json. They are sanitized, never relocated.
func collectHomeFiles(home, harness string, names []string, result *Preview) error {
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer homeRoot.Close()
	for _, name := range names {
		info, statErr := homeRoot.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := homeRoot.Open(name)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(raw) > 128<<10 {
			continue
		}
		content, issues, parseErr := Sanitize(harness, name, string(raw))
		result.Issues = append(result.Issues, issues...)
		if parseErr != nil {
			result.Issues = append(result.Issues, Issue{name, "Invalid configuration excluded"})
			continue
		}
		result.Bundle.Files = append(result.Bundle.Files, File{Path: name, Content: content})
		result.Files = append(result.Files, name)
	}
	return nil
}

// collectShared reads a home directory several harnesses share, such as
// ~/.agents/skills for Codex and DeepSeek Harness.
func collectShared(home, harness, dir string, result *Preview) error {
	root, err := os.OpenRoot(filepath.Join(home, filepath.FromSlash(dir)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	count := 0
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == "." {
			return nil
		}
		count++
		if count > 4096 {
			return fmt.Errorf("shared directory exceeds entry limit")
		}
		destination := dir + "/" + p
		if d.Type()&os.ModeSymlink != 0 {
			result.Issues = append(result.Issues, Issue{destination, "Symbolic link excluded"})
			return nil
		}
		if d.IsDir() {
			if !AllowedPath(harness, destination+"/entry") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !AllowedPath(harness, destination) {
			result.Issues = append(result.Issues, Issue{destination, "Unsupported skill file excluded"})
			return nil
		}
		file, err := root.Open(p)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		item := File{Path: destination, Content: string(raw)}
		if err := (Bundle{Harness: harness, Files: []File{item}}).Validate(); err != nil {
			result.Issues = append(result.Issues, Issue{destination, "Oversized or sensitive skill file excluded"})
			return nil
		}
		result.Bundle.Files = append(result.Bundle.Files, item)
		result.Files = append(result.Files, destination)
		return nil
	})
}
