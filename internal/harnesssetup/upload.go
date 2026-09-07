package harnesssetup

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// ReviewFiles sanitizes explicitly selected browser files without accessing disk,
// executing code, or persisting the original content. Paths are canonical harness
// destinations, never paths to read on the server.
func ReviewFiles(harness string, files []File) (Preview, error) {
	result := Preview{Harness: harness, Bundle: Bundle{Harness: harness, Files: []File{}}, Files: []string{}, Issues: []Issue{}, Dependencies: []string{}}
	if !Supported(harness) || len(files) > MaxFiles {
		return result, errors.New("unsupported harness or too many selected files")
	}
	total := 0
	seen := map[string]bool{}
	for _, file := range files {
		total += len(file.Content)
		if total > MaxBytes {
			return result, errors.New("selected configuration exceeds 512 KiB")
		}
		if len(file.Content) > 128<<10 || !utf8.ValidString(file.Content) || strings.ContainsRune(file.Content, 0) {
			result.Issues = append(result.Issues, Issue{file.Path, "File is too large or is not UTF-8 text"})
			continue
		}
		if !AllowedPath(harness, file.Path) {
			result.Issues = append(result.Issues, Issue{file.Path, "Authentication, runtime state, or unsupported file excluded"})
			continue
		}
		if seen[file.Path] {
			return result, errors.New("duplicate destination; select each configuration file once")
		}
		seen[file.Path] = true
		if IsConfig(file.Path) {
			// Browser selection does not expose the laptop's absolute configuration root.
			// Only portable ~/ references are relocated; external paths are retained with warnings.
			target := map[string]string{"codex": ".codex", "claude": ".claude", "opencode": ".config/opencode", "pi": ".pi/agent"}[harness]
			relocated, err := RelocateConfig(harness, file.Path, file.Content, "/@meridian-browser-root", target)
			if err != nil {
				result.Issues = append(result.Issues, Issue{file.Path, "Configuration could not be parsed"})
				continue
			}
			content, issues, err := Sanitize(harness, file.Path, relocated)
			result.Issues = append(result.Issues, issues...)
			if err != nil {
				result.Issues = append(result.Issues, Issue{file.Path, "Configuration could not be parsed"})
				continue
			}
			file.Content = content
		}
		if Sensitive(file.Content) {
			result.Issues = append(result.Issues, Issue{file.Path, "Credential-shaped content excluded; use a provider connection for authentication"})
			continue
		}
		result.Bundle.Files = append(result.Bundle.Files, file)
	}
	for i, file := range result.Bundle.Files {
		if IsConfig(file.Path) {
			content, issues, err := pruneMissingReferences(file.Path, file.Content, result.Bundle.Files)
			if err != nil {
				return result, errors.New("configuration references could not be reviewed")
			}
			result.Bundle.Files[i].Content = content
			result.Warnings = append(result.Warnings, issues...)
			result.Warnings = append(result.Warnings, PortabilityWarnings(file.Path, content)...)
		}
	}
	dependencies, err := DependenciesForFiles(result.Bundle.Files)
	if err != nil {
		return result, errors.New("portable dependencies could not be reviewed")
	}
	result.Bundle.Dependencies = dependencies
	result.Dependencies = dependencies
	if len(result.Bundle.Files) > 0 {
		if err := result.Bundle.Validate(); err != nil {
			return result, err
		}
	}
	sort.Slice(result.Bundle.Files, func(i, j int) bool { return result.Bundle.Files[i].Path < result.Bundle.Files[j].Path })
	for _, file := range result.Bundle.Files {
		result.Files = append(result.Files, file.Path)
	}
	result.Digest = result.Bundle.Digest()
	return result, nil
}
