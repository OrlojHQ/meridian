package harnesssetup

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

var pinnedPackage = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*@[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)

func ValidPackage(value string) bool { return len(value) <= 256 && pinnedPackage.MatchString(value) }
func npxPackage(server any) (string, bool, error) {
	object, ok := server.(map[string]any)
	if !ok {
		return "", false, nil
	}
	var command string
	var args []any
	switch c := object["command"].(type) {
	case string:
		command = c
		args, _ = object["args"].([]any)
	case []any:
		if len(c) > 0 {
			command, _ = c[0].(string)
			args = c[1:]
		}
	}
	if command != "npx" && command != "/opt/meridian-node/bin/npx" {
		return "", false, nil
	}
	for len(args) > 0 {
		value, ok := args[0].(string)
		if !ok {
			return "", true, errors.New("invalid npx arguments")
		}
		if value == "--yes" || value == "-y" || value == "--offline" {
			args = args[1:]
			continue
		}
		if !ValidPackage(value) {
			return "", true, errors.New("npx requires an exact package@version")
		}
		return value, true, nil
	}
	return "", true, errors.New("npx requires a pinned package")
}
func DependenciesForFiles(files []File) ([]string, error) {
	unique := map[string]bool{}
	for _, file := range files {
		if !IsConfig(file.Path) {
			continue
		}
		config, err := parseConfig(file.Path, file.Content)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"mcp", "mcp_servers", "mcpServers"} {
			servers, _ := config[key].(map[string]any)
			for _, server := range servers {
				pkg, found, err := npxPackage(server)
				if err != nil {
					return nil, err
				}
				if found {
					unique[pkg] = true
				}
			}
		}
	}
	result := make([]string, 0, len(unique))
	for pkg := range unique {
		result = append(result, pkg)
	}
	sort.Strings(result)
	return result, nil
}

// OfflineNpx adds offline resolution only to imported npx tool definitions.
// Other npm commands in the user's development session retain normal behavior.
func OfflineNpx(p, content string) (string, error) {
	value, err := parseConfig(p, content)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"mcp", "mcp_servers", "mcpServers"} {
		servers, _ := value[key].(map[string]any)
		for _, server := range servers {
			_, found, err := npxPackage(server)
			if err != nil {
				return "", err
			}
			if !found {
				continue
			}
			object := server.(map[string]any)
			if command, ok := object["command"].(string); ok && strings.Contains(command, "npx") {
				args, _ := object["args"].([]any)
				object["command"] = "/opt/meridian-node/bin/npx"
				object["args"] = append([]any{"--offline"}, args...)
			} else if args, ok := object["command"].([]any); ok {
				object["command"] = append([]any{"/opt/meridian-node/bin/npx", "--offline"}, args[1:]...)
			}
		}
	}
	return encodeConfig(p, value)
}
