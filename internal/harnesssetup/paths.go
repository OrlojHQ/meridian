package harnesssetup

import (
	"encoding/json"
	"github.com/pelletier/go-toml/v2"
	"path/filepath"
	"strings"
)

const HomeReference = "@meridian-home@/"

func transformStrings(value any, replace func(string) string) any {
	switch v := value.(type) {
	case string:
		return replace(v)
	case map[string]any:
		for key, item := range v {
			v[key] = transformStrings(item, replace)
		}
	case []any:
		for i, item := range v {
			v[i] = transformStrings(item, replace)
		}
	}
	return value
}
func encodeConfig(p string, value map[string]any) (string, error) {
	var raw []byte
	var err error
	if strings.HasSuffix(p, ".toml") {
		raw, err = toml.Marshal(value)
	} else {
		raw, err = json.MarshalIndent(value, "", "  ")
	}
	return string(raw), err
}

// RelocateConfig rewrites whole, configuration-root-contained path values only.
// Shell command strings and external paths remain explicit portability issues.
func RelocateConfig(harness, p, content, rootName, target string) (string, error) {
	value, err := parseConfig(p, content)
	if err != nil {
		return "", err
	}
	rootName = filepath.Clean(rootName)
	transformStrings(value, func(s string) string {
		relative := ""
		if strings.HasPrefix(s, rootName+string(filepath.Separator)) {
			relative = strings.TrimPrefix(s, rootName+string(filepath.Separator))
		} else if strings.HasPrefix(s, "~/"+target+"/") {
			relative = strings.TrimPrefix(s, "~/"+target+"/")
		}
		if relative != "" {
			destination := target + "/" + filepath.ToSlash(relative)
			if AllowedPath(harness, destination) || AllowedPath(harness, destination+"/entry") {
				return HomeReference + destination
			}
		}
		return s
	})
	return encodeConfig(p, value)
}
func ConfigReferences(p, content string) ([]string, error) {
	value, err := parseConfig(p, content)
	if err != nil {
		return nil, err
	}
	var refs []string
	transformStrings(value, func(s string) string {
		if strings.HasPrefix(s, HomeReference) {
			refs = append(refs, strings.TrimPrefix(s, HomeReference))
		}
		return s
	})
	return refs, nil
}
func ExpandConfigHome(p, content, home string) (string, error) {
	value, err := parseConfig(p, content)
	if err != nil {
		return "", err
	}
	transformStrings(value, func(s string) string {
		if strings.HasPrefix(s, HomeReference) {
			return home + "/" + strings.TrimPrefix(s, HomeReference)
		}
		return s
	})
	return encodeConfig(p, value)
}

func pruneMissingReferences(p, content string, files []File) (string, []Issue, error) {
	value, err := parseConfig(p, content)
	if err != nil {
		return "", nil, err
	}
	issues := []Issue{}
	transformStrings(value, func(s string) string {
		if !strings.HasPrefix(s, HomeReference) {
			return s
		}
		ref := strings.TrimPrefix(s, HomeReference)
		for _, file := range files {
			if file.Path == ref || strings.HasPrefix(file.Path, ref+"/") {
				return s
			}
		}
		// Undo the relocation when its target was not imported. Keep the original
		// home-relative setting so the user can provide it separately.
		issues = append(issues, Issue{p, "Referenced file was not imported; home-relative path kept"})
		return "~/" + ref
	})

	raw, err := encodeConfig(p, value)
	return raw, issues, err
}
