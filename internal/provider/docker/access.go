package docker

import (
	"errors"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func repositoryAccess(err error) bool {
	if errors.Is(err, domain.ErrRepositoryAccess) {
		return true
	}
	var protocol *capsuleproto.Error
	return errors.As(err, &protocol) && protocol.Code == "repository_access"
}
