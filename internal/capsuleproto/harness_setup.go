package capsuleproto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"os"
	"path/filepath"
	"strings"
)

// prepareHarnessSetup uses rooted filesystem operations: imported paths and
// Capsule-created symlinks cannot escape the private home. Imported hooks are not executed during installation. A completed revision is reused to preserve session edits.
func (s *Server) prepareHarnessSetup(ctx context.Context, setup *harnesssetup.RuntimeSetup, harness string) (map[string]string, error) {
	if setup == nil {
		return nil, nil
	}
	if strings.TrimSuffix(harness, "-structured") != setup.Bundle.Harness || len(setup.Revision) == 0 || len(setup.Revision) > 128 || strings.ContainsAny(setup.Revision, "/\\.\x00\r\n") {
		return nil, errors.New("invalid setup identity")
	}
	if err := setup.Bundle.Validate(); err != nil {
		return nil, err
	}
	base := s.config.HarnessHome
	if base == "" {
		base = "/home/capsule"
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	target := ".meridian-setup-" + setup.Revision
	if _, err := root.Stat(target + "/.complete"); errors.Is(err, os.ErrNotExist) {
		suffix := make([]byte, 16)
		if _, err := rand.Read(suffix); err != nil {
			return nil, err
		}
		temp := ".meridian-import-" + hex.EncodeToString(suffix)
		if err := root.Mkdir(temp, 0o700); err != nil {
			return nil, err
		}
		defer root.RemoveAll(temp)
		for _, file := range setup.Bundle.Files {
			p := temp + "/" + file.Path
			if err := root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return nil, err
			}
			mode := os.FileMode(0o600)
			if file.Executable {
				mode = 0o700
			}
			f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return nil, err
			}
			content := file.Content
			if harnesssetup.IsConfig(file.Path) {
				var expandErr error
				content, expandErr = harnesssetup.ExpandConfigHome(file.Path, content, filepath.Join(base, target))
				if expandErr == nil {
					content, expandErr = harnesssetup.OfflineNpx(file.Path, content)
				}
				if expandErr != nil {
					_ = f.Close()
					return nil, expandErr
				}
			}
			_, writeErr := f.WriteString(content)
			closeErr := f.Close()
			if writeErr != nil {
				return nil, writeErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		if err := installSetupDependencies(ctx, filepath.Join(base, temp), setup.Bundle.Dependencies); err != nil {
			return nil, err
		}
		f, err := root.OpenFile(temp+"/.complete", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		if err := root.Rename(temp, target); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	home := filepath.Join(base, target)
	return map[string]string{"HOME": home, "CODEX_HOME": filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "PI_CODING_AGENT_DIR": filepath.Join(home, ".pi", "agent"), "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME": filepath.Join(home, ".cache")}, nil
}
func setupEnvironment(env []string, setup map[string]string) []string {
	if len(setup) == 0 {
		return env
	}
	result := make([]string, 0, len(env)+len(setup))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if _, ok := setup[key]; !ok {
			result = append(result, item)
		}
	}
	for key, value := range setup {
		result = append(result, key+"="+value)
	}
	return result
}
