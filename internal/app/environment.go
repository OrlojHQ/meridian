package app

import (
	"context"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type ProjectEnvironment struct {
	Enabled         bool                        `json:"enabled"`
	Generation      int64                       `json:"generation"`
	Setup           []string                    `json:"setup"`
	ImageReference  string                      `json:"imageReference"`
	Latest          *domain.PreparationProgress `json:"latest,omitempty"`
	ResourceVersion domain.ResourceVersion      `json:"resourceVersion"`
}
type environmentReplay struct {
	Hash        string
	Environment ProjectEnvironment
}

type EnvironmentMutation struct {
	Enabled                 *bool                  `json:"enabled,omitempty"`
	Setup                   *[]string              `json:"setup,omitempty"`
	Rebuild                 bool                   `json:"rebuild,omitempty"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

func environmentView(ctx context.Context, reader ports.Reader, id domain.ProjectID) (ProjectEnvironment, error) {
	project, err := reader.GetProject(ctx, id)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	policy, err := reader.GetPreparationPolicy(ctx, id)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	result := ProjectEnvironment{Enabled: !policy.Disabled, Generation: policy.Generation, Setup: append([]string{}, project.Setup...), ImageReference: project.ImageReference, ResourceVersion: project.ResourceVersion}
	capsules, _, err := reader.ListCapsules(ctx, id, ports.Page{Limit: 1})
	if err != nil {
		return result, err
	}
	if len(capsules) > 0 {
		p, err := reader.GetPreparation(ctx, capsules[0].ID)
		if err == nil {
			result.Latest = p.Progress()
		} else if !errors.Is(err, domain.ErrNotFound) {
			return result, err
		}
	}
	return result, nil
}
func (s *Service) ProjectEnvironment(ctx context.Context, id domain.ProjectID) (ProjectEnvironment, error) {
	var result ProjectEnvironment
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result, err = environmentView(ctx, reader, id)
		return err
	})
	return result, err
}
func (s *Service) UpdateProjectEnvironment(ctx context.Context, id domain.ProjectID, input EnvironmentMutation, key string) (ProjectEnvironment, error) {
	if err := requireIdempotency(key); err != nil {
		return ProjectEnvironment{}, err
	}
	if input.ExpectedResourceVersion <= 0 {
		return ProjectEnvironment{}, domain.ErrInvalid
	}
	if input.Setup != nil {
		if err := validateProjectConfiguration(ProjectConfiguration{Setup: *input.Setup}); err != nil {
			return ProjectEnvironment{}, err
		}
	}
	var result ProjectEnvironment
	scope := "project:" + string(id) + ":environment"
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[environmentReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != setupFingerprint(input) {
				return domain.ErrConflict
			}
			result = replay.Environment
			return nil
		}
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			return err
		}
		if project.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		policy, err := tx.GetPreparationPolicy(ctx, id)
		if err != nil {
			return err
		}
		if input.Enabled != nil {
			policy.Disabled = !*input.Enabled
		}
		if input.Setup != nil {
			project.Setup = append([]string(nil), (*input.Setup)...)
		}
		// Every saved configuration is a new preparation generation. Existing Capsules
		// retain their frozen specification; new launches cannot select older artifacts.
		policy.Generation++
		if err := tx.PutPreparationPolicy(ctx, id, policy); err != nil {
			return err
		}
		previous := project.ResourceVersion
		project.ResourceVersion++
		project.UpdatedAt = s.clock.Now().UTC()
		if err := tx.UpdateProject(ctx, project, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "project", string(id), "project.environment_updated", project.ResourceVersion, nil); err != nil {
			return err
		}
		result, err = environmentView(ctx, tx, id)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, environmentReplay{Hash: setupFingerprint(input), Environment: result}, project.UpdatedAt)
	})
	return result, err
}
