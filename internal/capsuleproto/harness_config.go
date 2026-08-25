package capsuleproto

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/OrlojHQ/meridian/internal/harness"
)

const defaultTrustedHarnessDirectory = "/etc/meridian/harnesses.d"

var errHarnessConfigurationUnavailable = errors.New("harness configuration unavailable")

func (s *Server) loadHarnessConfig() (harness.Config, error) {
	config := harness.Config{Version: harness.CurrentVersion}
	found := false

	entries, err := os.ReadDir(s.config.TrustedHarnessDirectory)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return harness.Config{}, fmt.Errorf("read trusted harness directory: %w", err)
	}
	if err == nil {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			if !entry.Type().IsRegular() {
				return harness.Config{}, fmt.Errorf("trusted harness manifest %q is not a regular file", entry.Name())
			}
			path := filepath.Join(s.config.TrustedHarnessDirectory, entry.Name())
			file, openErr := os.Open(path)
			if openErr != nil {
				return harness.Config{}, fmt.Errorf("open trusted harness manifest %q: %w", entry.Name(), openErr)
			}
			parsed, parseErr := harness.Parse(file)
			closeErr := file.Close()
			if parseErr != nil {
				return harness.Config{}, fmt.Errorf("parse trusted harness manifest %q: %w", entry.Name(), parseErr)
			}
			if closeErr != nil {
				return harness.Config{}, fmt.Errorf("close trusted harness manifest %q: %w", entry.Name(), closeErr)
			}
			config.Harnesses = append(config.Harnesses, parsed.Harnesses...)
			found = true
		}
	}

	projectPath := filepath.Join(s.config.Workspace, ".meridian", "project.yaml")
	project, err := os.Open(projectPath)
	if err == nil {
		parsed, parseErr := harness.Parse(project)
		closeErr := project.Close()
		if parseErr != nil {
			return harness.Config{}, fmt.Errorf("parse repository harness configuration: %w", parseErr)
		}
		if closeErr != nil {
			return harness.Config{}, fmt.Errorf("close repository harness configuration: %w", closeErr)
		}
		config.Harnesses = append(config.Harnesses, parsed.Harnesses...)
		found = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return harness.Config{}, fmt.Errorf("open repository harness configuration: %w", err)
	}

	if !found {
		return harness.Config{}, errHarnessConfigurationUnavailable
	}
	if err := config.Validate(); err != nil {
		return harness.Config{}, err
	}
	return config, nil
}
