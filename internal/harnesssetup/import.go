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
	if !Supported(harness) {
		return result, fmt.Errorf("unsupported harness %q", harness)
	}
	rootName, target := "", ""
	switch harness {
	case "codex":
		rootName = env("CODEX_HOME")
		target = ".codex"
		if rootName == "" {
			rootName = filepath.Join(home, target)
		}
	case "claude":
		rootName = env("CLAUDE_CONFIG_DIR")
		target = ".claude"
		if rootName == "" {
			rootName = filepath.Join(home, target)
		}
	case "pi":
		rootName = env("PI_CODING_AGENT_DIR")
		target = ".pi/agent"
		if rootName == "" {
			rootName = filepath.Join(home, target)
		}
	case "opencode":
		rootName = env("OPENCODE_CONFIG_DIR")
		target = ".config/opencode"
		if rootName == "" {
			xdg := env("XDG_CONFIG_HOME")
			if xdg == "" {
				xdg = filepath.Join(home, ".config")
			}
			rootName = filepath.Join(xdg, "opencode")
		}
	case "dsh":
		rootName = env("DSH_HOME")
		target = ".dsh"
		if rootName == "" {
			rootName = filepath.Join(home, target)
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
	if err == nil && harness == "claude" {
		homeRoot, openErr := os.OpenRoot(home)
		if openErr != nil {
			return result, openErr
		}
		defer homeRoot.Close()
		if info, statErr := homeRoot.Lstat(".claude.json"); statErr == nil && info.Mode().IsRegular() {
			file, openErr := homeRoot.Open(".claude.json")
			if openErr != nil {
				return result, openErr
			}
			raw, readErr := io.ReadAll(io.LimitReader(file, (128<<10)+1))
			closeErr := file.Close()
			if readErr != nil {
				return result, readErr
			}
			if closeErr != nil {
				return result, closeErr
			}
			if len(raw) <= 128<<10 {
				content, issues, parseErr := Sanitize(harness, ".claude.json", string(raw))
				result.Issues = append(result.Issues, issues...)
				if parseErr == nil {
					result.Bundle.Files = append(result.Bundle.Files, File{Path: ".claude.json", Content: content})
					result.Files = append(result.Files, ".claude.json")
				} else {
					result.Issues = append(result.Issues, Issue{".claude.json", "Invalid JSON excluded"})
				}
			}
		}
	}
	if err == nil && (harness == "codex" || harness == "dsh") {
		if sharedErr := collectSharedSkills(home, harness, &result); sharedErr != nil {
			return result, sharedErr
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
func IsConfig(p string) bool {
	switch p {
	case ".claude.json", ".codex/config.toml", ".claude/settings.json", ".config/opencode/opencode.json", ".config/opencode/opencode.jsonc", ".pi/agent/settings.json", ".pi/agent/models.json":
		return true
	}
	if strings.HasPrefix(p, ".dsh/") {
		switch filepath.Base(p) {
		case "settings.yaml", "settings.yml", "cordis.patch.yml", "cordis.yml":
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
func collectSharedSkills(home, harness string, result *Preview) error {
	root, err := os.OpenRoot(filepath.Join(home, ".agents", "skills"))
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
			return fmt.Errorf("shared skills exceed entry limit")
		}
		destination := ".agents/skills/" + p
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
