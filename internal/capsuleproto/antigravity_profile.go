package capsuleproto

import (
	"context"
	"errors"
	"os"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

// prepareAntigravityProfile writes an antigravity connection's agy sign-in
// into the runtime's home before agy starts (ADR 0061). agy reads it from
// $HOME/.gemini/antigravity-cli, and capsuled sets HOME to the setup home.
// The profile is rewritten on every start, so a new sign-in reaches the next
// runtime; nothing is read back.
func (s *Server) prepareAntigravityProfile(ctx context.Context, connection *harnesssetup.Connection, setup *harnesssetup.RuntimeSetup, harness string, env map[string]string) (map[string]string, error) {
	if connection.URL != "" || connection.Token != "" || connection.CA != "" || connection.Model != "" ||
		len(connection.Efforts) != 0 || len(connection.Fold) != 0 {
		return nil, errors.New("invalid provider connection")
	}
	if !harnesssetup.ConnectionSupported(harness, connection.Provider, "") {
		return nil, errors.New("provider does not support this harness")
	}
	if _, err := antigravityauth.Check(connection.Profile); err != nil {
		return nil, errors.New("invalid provider connection")
	}
	revision := "connection-" + harness
	var err error
	if setup != nil {
		revision = setup.Revision
	} else if env, err = s.materializeSetupHome(ctx, revision, nil, nil); err != nil {
		return nil, err
	}
	root, err := s.setupRoot(revision)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, file := range connection.Profile {
		if err := replaceSetupFile(root, antigravityauth.Dir+"/"+file.Path, file.Content); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// setupRoot opens a materialized setup home under the harness home.
func (s *Server) setupRoot(revision string) (*os.Root, error) {
	base := s.config.HarnessHome
	if base == "" {
		base = "/home/capsule"
	}
	outer, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer outer.Close()
	return outer.OpenRoot(".meridian-setup-" + revision)
}
