package capsuleproto

import "github.com/OrlojHQ/meridian/internal/harnesssetup"

const identityDirectory = ".meridian-identity"

// prepareGitIdentity writes the commit identity the Capsule pinned (ADR 0049)
// as a generated git config with one [user] section, and points
// GIT_CONFIG_GLOBAL at it for the harness process. The laptop's git config is
// never sent, so no include, credential helper, or rewrite can arrive from
// the laptop. The file is rewritten at every session start, so an explicit
// profile update reaches the next session even when the setup revision is
// unchanged.
func (s *Server) prepareGitIdentity(identity *harnesssetup.GitIdentity, env map[string]string) (map[string]string, error) {
	return s.prepareSessionGit(identity, nil, env)
}
