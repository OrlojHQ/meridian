package workstation

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

// maxGitConfigBytes bounds each git config file this reads.
const maxGitConfigBytes = 64 << 10

// GitSource is the commit identity found in the laptop's global git config
// and where it came from (ADR 0049). Only user.name and user.email are read.
type GitSource struct {
	Identity harnesssetup.GitIdentity `json:"identity"`
	// Path is the file that set the identity, with the home shown as ~.
	Path string `json:"path"`
	// Notes say what was skipped, such as include directives.
	Notes []string `json:"notes"`
}

// DiscoverGitIdentity reads user.name and user.email from the global git
// config the way git resolves them: $GIT_CONFIG_GLOBAL alone when it is set,
// otherwise $XDG_CONFIG_HOME/git/config and then ~/.gitconfig, the later one
// winning. It never runs git and never follows include or includeIf, so a
// config cannot make this read another file or run a helper. It returns nil
// when no config sets either value, and a source with only notes when what
// it found is not usable.
func DiscoverGitIdentity(home string, env func(string) string) (*GitSource, error) {
	var files []string
	if global := env("GIT_CONFIG_GLOBAL"); global != "" {
		files = []string{global}
	} else {
		xdg := env("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		files = []string{filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig")}
	}
	var name, email, namePath, emailPath string
	notes := []string{}
	for _, path := range files {
		values, fileNotes, err := readUserSection(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		display := displayPath(home, path)
		if err != nil {
			notes = append(notes, display+": "+err.Error())
			continue
		}
		for _, note := range fileNotes {
			notes = append(notes, display+": "+note)
		}
		if value, ok := values["name"]; ok {
			name, namePath = value, display
		}
		if value, ok := values["email"]; ok {
			email, emailPath = value, display
		}
	}
	if name == "" && email == "" {
		if len(notes) > 0 {
			return &GitSource{Notes: notes}, nil
		}
		return nil, nil
	}
	if !harnesssetup.ValidGitIdentity(name, email) || name == "" {
		notes = append(notes, "user.name and user.email are not a usable commit identity, so none is imported")
		return &GitSource{Notes: notes}, nil
	}
	path := emailPath
	if namePath != emailPath {
		path = namePath + ", " + emailPath
	}
	return &GitSource{Identity: harnesssetup.GitIdentity{Name: name, Email: email}, Path: path, Notes: notes}, nil
}

func displayPath(home, path string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return path
}

// readUserSection returns the last name and email in [user] sections of one
// file. Other sections, subsections such as [user "x"], and includes are
// skipped.
func readUserSection(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file, skipped")
	}
	if info.Size() > maxGitConfigBytes {
		return nil, nil, errors.New("larger than 64 KiB, skipped")
	}
	values := map[string]string{}
	notes := []string{}
	section := ""
	skippedInclude := false
	scanner := bufio.NewScanner(io.LimitReader(f, maxGitConfigBytes))
	scanner.Buffer(make([]byte, 0, 4096), maxGitConfigBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				section = ""
				continue
			}
			header := strings.TrimSpace(line[1:end])
			base, _, hasSub := strings.Cut(header, " ")
			if dot := strings.IndexByte(base, '.'); dot >= 0 {
				base, hasSub = base[:dot], true
			}
			base = strings.ToLower(base)
			if (base == "include" || base == "includeif") && !skippedInclude {
				notes = append(notes, "include directives were not followed")
				skippedInclude = true
			}
			if base == "user" && !hasSub {
				section = "user"
			} else {
				section = ""
			}
			// A key may follow the header on the same line.
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		if section != "user" {
			continue
		}
		key, raw, hasValue := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !hasValue || (key != "name" && key != "email") {
			continue
		}
		value, ok := parseValue(raw)
		if !ok {
			notes = append(notes, "user."+key+" could not be read, skipped")
			continue
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, errors.New("could not be read, skipped")
	}
	return values, notes, nil
}

// parseValue reads one git config value: quotes group text, # and ; start a
// comment outside quotes, and \", \\, \t, and \n are escapes. A trailing
// backslash continuation is not supported.
func parseValue(raw string) (string, bool) {
	var out strings.Builder
	quoted := false
	pending := ""
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\':
			if i+1 >= len(raw) {
				return "", false
			}
			i++
			switch raw[i] {
			case '"', '\\':
				out.WriteString(pending)
				pending = ""
				out.WriteByte(raw[i])
			case 't':
				out.WriteString(pending)
				pending = ""
				out.WriteByte('\t')
			case 'n':
				out.WriteString(pending)
				pending = ""
				out.WriteByte('\n')
			default:
				return "", false
			}
		case c == '"':
			out.WriteString(pending)
			pending = ""
			quoted = !quoted
		case !quoted && (c == '#' || c == ';'):
			return strings.TrimLeft(out.String(), " \t"), !quoted
		case !quoted && (c == ' ' || c == '\t'):
			pending += string(c)
		default:
			out.WriteString(pending)
			pending = ""
			out.WriteByte(c)
		}
	}
	if quoted {
		return "", false
	}
	return strings.TrimLeft(out.String(), " \t"), true
}
