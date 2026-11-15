package capsuleproto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

const identityDirectory = ".meridian-identity"

// prepareGitIdentity writes the commit identity the Capsule pinned (ADR 0049)
// as a generated git config with one [user] section, and points
// GIT_CONFIG_GLOBAL at it for the harness process. The laptop's git config is
// never sent, so no include, credential helper, or rewrite can arrive here.
// The file is rewritten at every session start, so an explicit profile
// update reaches the next session even when the setup revision is unchanged.
func (s *Server) prepareGitIdentity(identity *harnesssetup.GitIdentity, env map[string]string) (map[string]string, error) {
	if identity == nil {
		return env, nil
	}
	if identity.Name == "" || !harnesssetup.ValidGitIdentity(identity.Name, identity.Email) {
		return nil, errors.New("invalid git identity")
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
	if err := root.Mkdir(identityDirectory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := root.Lstat(identityDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("git identity directory is not a directory")
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	temp := identityDirectory + "/.gitconfig-" + hex.EncodeToString(suffix)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := f.WriteString(identity.Config())
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = root.Remove(temp)
		return nil, errors.Join(writeErr, closeErr)
	}
	if err := root.Rename(temp, identityDirectory+"/gitconfig"); err != nil {
		_ = root.Remove(temp)
		return nil, err
	}
	if env == nil {
		env = map[string]string{}
	}
	env["GIT_CONFIG_GLOBAL"] = filepath.Join(base, identityDirectory, "gitconfig")
	return env, nil
}
