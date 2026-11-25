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
// not executed during installation. The first session after capsuled starts
// resets every setup path the harness manifest owns, so planted harness
// configuration cannot outlive a restart or image upgrade on the persistent
// home, while harness session files stay intact (ADR 0064).
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
	return s.materializeSetupHome(ctx, setup.Bundle.Harness, setup.Revision, setup.Bundle.Files, setup.Bundle.Dependencies)
}

// materializeSetupHome creates the private home for a validated revision and
// returns its environment. A revision that already exists on the persistent
// home is reset once per capsuled process: every path the harness manifest
// owns is removed and the bundle rewritten, which matches what a fresh tmpfs
// home gave each boot before ADR 0064. Later sessions in the same process
// reuse it, preserving their edits as before. Harness session files live
// outside the manifest paths and survive.
func (s *Server) materializeSetupHome(ctx context.Context, harness, revision string, files []harnesssetup.File, dependencies []string) (map[string]string, error) {
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
		// A home this process created is already clean.
		s.markSetupReset(target)
	} else if err != nil {
		return nil, err
	} else if err := s.resetSetupHomeOnce(root, harness, target, targetHome, files); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(revision, "connection-") {
		pruneStaleSetupHomes(base, root, target)
	}
	// Every pack manifest's root variable points into this home, so the
	// harness reads the imported files rather than an empty default.
	// The cache stays in this home: setup dependencies are cached in its .npm
	// for `npx --offline`, and harness runtimes load native code from it, which
	// the noexec /tmp cannot serve.
	env := harnesssetup.CapsuleEnvironment(targetHome)
	env["HOME"] = targetHome
	env["XDG_CONFIG_HOME"] = filepath.Join(targetHome, ".config")
	env["XDG_DATA_HOME"] = filepath.Join(targetHome, ".local", "share")
	env["XDG_CACHE_HOME"] = filepath.Join(targetHome, ".cache")
	return env, nil
}

// resetSetupHomeOnce removes every path the harness manifest owns from an
// existing setup home and rewrites the bundle, the first time this capsuled
// process uses the revision (ADR 0064).
func (s *Server) resetSetupHomeOnce(root *os.Root, harness, target, targetHome string, files []harnesssetup.File) error {
	s.setupResetMu.Lock()
	defer s.setupResetMu.Unlock()
	if s.setupReset[target] {
		return nil
	}
	targetRoot, err := root.OpenRoot(target)
	if err != nil {
		return err
	}
	defer targetRoot.Close()
	for _, owned := range setupOwnedPaths(strings.TrimSuffix(harness, "-structured")) {
		if err := targetRoot.RemoveAll(owned); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := refreshSetupBundleFile(targetRoot, targetHome, file); err != nil {
			return err
		}
	}
	s.markSetupResetLocked(target)
	return nil
}

func (s *Server) markSetupReset(target string) {
	s.setupResetMu.Lock()
	defer s.setupResetMu.Unlock()
	s.markSetupResetLocked(target)
}

func (s *Server) markSetupResetLocked(target string) {
	if s.setupReset == nil {
		s.setupReset = make(map[string]bool)
	}
	s.setupReset[target] = true
}

// setupOwnedPaths lists the home-relative setup paths a harness manifest owns:
// its root paths, shared directories, and home files.
func setupOwnedPaths(harness string) []string {
	manifest, ok := harnesssetup.LookupManifest(harness)
	if !ok {
		return nil
	}
	var owned []string
	for _, entry := range manifest.Paths {
		owned = append(owned, strings.TrimSuffix(manifest.Root.Path+"/"+entry, "/"))
	}
	owned = append(owned, manifest.Shared...)
	return append(owned, manifest.HomeFiles...)
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
