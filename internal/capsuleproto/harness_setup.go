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
// Capsule-created symlinks cannot escape the private home. Imported hooks are
// not executed during installation. Meridian-owned bundle files are rewritten
// on every session start so tampered setup files cannot persist across
// restarts or image upgrades while harness session files stay intact (ADR 0064).
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
	return s.materializeSetupHome(ctx, setup.Revision, setup.Bundle.Files, setup.Bundle.Dependencies)
}

// materializeSetupHome creates the private home for a validated revision
// (or refreshes Meridian-owned bundle files in an existing revision so harness
// session state survives while tampered config files are restored) and returns
// its environment (ADR 0064).
func (s *Server) materializeSetupHome(ctx context.Context, revision string, files []harnesssetup.File, dependencies []string) (map[string]string, error) {
	base := s.config.HarnessHome
	if base == "" {
		base = "/home/capsule"
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	target := ".meridian-setup-" + revision
	targetHome := filepath.Join(base, target)
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
		for _, file := range files {
			if err := writeSetupBundleFile(root, temp, targetHome, file); err != nil {
				return nil, err
			}
		}
		if err := installSetupDependencies(ctx, filepath.Join(base, temp), dependencies); err != nil {
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
	} else if len(files) > 0 {
		// Re-write every Meridian-owned setup file on every session start so
		// edits planted by a compromised turn do not survive in a persistent
		// home volume, while leaving harness session directories untouched.
		targetRoot, openErr := root.OpenRoot(target)
		if openErr != nil {
			return nil, openErr
		}
		for _, file := range files {
			if writeErr := refreshSetupBundleFile(targetRoot, targetHome, file); writeErr != nil {
				_ = targetRoot.Close()
				return nil, writeErr
			}
		}
		_ = targetRoot.Close()
	}
	if !strings.HasPrefix(revision, "connection-") {
		pruneStaleSetupHomes(base, root, target)
	}
	// Every pack manifest's root variable points into this home, so the
	// harness reads the imported files rather than an empty default.
	// Route transient caches to /tmp so they do not fill a persistent home volume.
	env := harnesssetup.CapsuleEnvironment(targetHome)
	env["HOME"] = targetHome
	env["XDG_CONFIG_HOME"] = filepath.Join(targetHome, ".config")
	env["XDG_DATA_HOME"] = filepath.Join(targetHome, ".local", "share")
	env["XDG_CACHE_HOME"] = "/tmp/meridian-cache"
	env["npm_config_cache"] = "/tmp/meridian-npm-cache"
	env["BUN_INSTALL_CACHE_DIR"] = "/tmp/meridian-bun-cache"
	return env, nil
}

func renderSetupBundleFile(targetHome string, file harnesssetup.File) (string, os.FileMode, error) {
	mode := os.FileMode(0o600)
	if file.Executable {
		mode = 0o700
	}
	content := file.Content
	if harnesssetup.IsConfig(file.Path) {
		var expandErr error
		content, expandErr = harnesssetup.ExpandConfigHome(file.Path, content, targetHome)
		if expandErr == nil {
			content, expandErr = harnesssetup.OfflineNpx(file.Path, content)
		}
		if expandErr != nil {
			return "", 0, expandErr
		}
	}
	return content, mode, nil
}

func writeSetupBundleFile(root *os.Root, dir, targetHome string, file harnesssetup.File) error {
	content, mode, err := renderSetupBundleFile(targetHome, file)
	if err != nil {
		return err
	}
	p := dir + "/" + file.Path
	if err := root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func refreshSetupBundleFile(targetRoot *os.Root, targetHome string, file harnesssetup.File) error {
	content, mode, err := renderSetupBundleFile(targetHome, file)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(file.Path); dir != "." && dir != "" {
		if err := targetRoot.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return writeRootFile(targetRoot, file.Path, content, mode)
}

func pruneStaleSetupHomes(base string, root *os.Root, activeTarget string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == activeTarget || !strings.HasPrefix(name, ".meridian-setup-") {
			continue
		}
		if strings.HasPrefix(name, ".meridian-setup-connection-") {
			continue
		}
		_ = root.RemoveAll(name)
	}
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
