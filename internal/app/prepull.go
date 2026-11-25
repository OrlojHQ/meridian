package app

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const maxConcurrentPrepulls = 2

// AllowlistedOfficialPackImages collects the distinct, registry-qualified
// current catalog image references for official packs allowlisted on at least
// one existing Project (ADR 0041, ADR 0064).
func (s *Service) AllowlistedOfficialPackImages(ctx context.Context) ([]string, error) {
	if err := requireSystem(ctx); err != nil {
		return nil, err
	}
	if len(s.harnessPacks) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var refs []string
	offset := 0
	const pageSize = 100
	for {
		var projects []domain.Project
		var more bool
		err := s.store.View(ctx, func(reader ports.Reader) error {
			var listErr error
			projects, more, listErr = reader.ListProjects(ctx, "", ports.Page{Offset: offset, Limit: pageSize})
			return listErr
		})
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			for _, image := range project.HarnessImages {
				pack, ok := domain.CatalogPack(s.harnessPacks, image)
				if !ok || !domain.RegistryQualifiedImage(pack.ImageReference) {
					continue
				}
				if _, exists := seen[pack.ImageReference]; exists {
					continue
				}
				seen[pack.ImageReference] = struct{}{}
				refs = append(refs, pack.ImageReference)
			}
		}
		if !more || len(projects) == 0 {
			break
		}
		offset += len(projects)
	}
	return refs, nil
}

// PrepullOfficialPacks pulls missing official pack images allowlisted on at
// least one Project with bounded concurrency (ADR 0064). Failures are logged
// with bounded error codes and never fail daemon startup.
func (s *Service) PrepullOfficialPacks(
	ctx context.Context,
	prepuller ports.ImagePrepuller,
	logger *log.Logger,
) {
	if err := requireSystem(ctx); err != nil {
		return
	}
	if prepuller == nil {
		return
	}
	refs, err := s.AllowlistedOfficialPackImages(ctx)
	if err != nil {
		if logger != nil && ctx.Err() == nil {
			logger.Printf("official pack pre-pull skipped: could not list projects")
		}
		return
	}
	if len(refs) == 0 {
		return
	}
	sem := make(chan struct{}, maxConcurrentPrepulls)
	var wg sync.WaitGroup
	for _, ref := range refs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(imageRef string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := prepuller.PrepullImage(ctx, imageRef); err != nil && ctx.Err() == nil && logger != nil {
				logger.Printf("official pack pre-pull failed for %s: %s", imageRef, prepullErrorCode(err))
			}
		}(ref)
	}
	wg.Wait()
}

func prepullErrorCode(err error) string {
	var failure *domain.StartupFailure
	if errors.As(err, &failure) && failure.Code != "" {
		return string(failure.Code)
	}
	return "pull_failed"
}
