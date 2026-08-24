package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const (
	minPreviewPort = 1024
	maxPreviewPath = 4096
)

func (s *Service) DiscoverPreviewPorts(
	ctx context.Context,
	capsuleID domain.CapsuleID,
) ([]ports.PreviewPort, error) {
	capsule, err := s.previewCapsule(ctx, capsuleID)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	items, err := s.preview.DiscoverPreviewPorts(ctx, capsule.ProviderResourceID)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "preview", operationResult(err), time.Since(started))
	}
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if err := validatePreviewPort(item.Port); err != nil {
			return nil, fmt.Errorf("%w: preview runtime returned an invalid port", domain.ErrInvalid)
		}
	}
	return items, nil
}

func (s *Service) ForwardPreviewHTTP(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	port uint16,
	request *http.Request,
) (ports.PreviewResponse, error) {
	if err := validatePreviewRequest(port, request); err != nil {
		return ports.PreviewResponse{}, err
	}
	capsule, err := s.previewCapsule(ctx, capsuleID)
	if err != nil {
		return ports.PreviewResponse{}, err
	}
	if err := s.requireDiscoveredPreviewPort(ctx, capsule.ProviderResourceID, port); err != nil {
		return ports.PreviewResponse{}, err
	}
	started := time.Now()
	response, err := s.preview.ForwardPreviewHTTP(ctx, capsule.ProviderResourceID, port, request)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "preview", operationResult(err), time.Since(started))
	}
	return response, err
}

func (s *Service) AttachPreview(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	port uint16,
	path string,
	header http.Header,
) (ports.RuntimeAttachment, string, error) {
	if err := validatePreviewPort(port); err != nil {
		return nil, "", err
	}
	if err := validatePreviewPath(path); err != nil {
		return nil, "", err
	}
	capsule, err := s.previewCapsule(ctx, capsuleID)
	if err != nil {
		return nil, "", err
	}
	if err := s.requireDiscoveredPreviewPort(ctx, capsule.ProviderResourceID, port); err != nil {
		return nil, "", err
	}
	started := time.Now()
	attachment, protocol, err := s.preview.AttachPreview(ctx, capsule.ProviderResourceID, port, path, header)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "preview", operationResult(err), time.Since(started))
	}
	return attachment, protocol, err
}

func (s *Service) previewCapsule(
	ctx context.Context,
	capsuleID domain.CapsuleID,
) (domain.Capsule, error) {
	if s.preview == nil {
		return domain.Capsule{}, domain.ErrUnsupported
	}
	if capsuleID == "" || len(capsuleID) > 128 ||
		strings.ContainsAny(string(capsuleID), "/\\\x00\r\n") {
		return domain.Capsule{}, fmt.Errorf("%w: invalid Capsule ID", domain.ErrInvalid)
	}
	capsule, err := s.GetCapsule(ctx, capsuleID)
	if err != nil {
		return domain.Capsule{}, err
	}
	if capsule.State != domain.CapsuleReady || capsule.Maintenance != "" {
		return domain.Capsule{}, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	if capsule.ProviderResourceID == "" {
		return domain.Capsule{}, fmt.Errorf("%w: Capsule has no provider resource", domain.ErrConflict)
	}
	return capsule, nil
}

func (s *Service) requireDiscoveredPreviewPort(
	ctx context.Context,
	resourceID string,
	port uint16,
) error {
	items, err := s.preview.DiscoverPreviewPorts(ctx, resourceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Port == port {
			return nil
		}
	}
	return fmt.Errorf("%w: preview port is not published by this Capsule", domain.ErrNotFound)
}

func validatePreviewRequest(port uint16, request *http.Request) error {
	if request == nil || request.URL == nil {
		return fmt.Errorf("%w: preview request is required", domain.ErrInvalid)
	}
	if err := validatePreviewPort(port); err != nil {
		return err
	}
	return validatePreviewPath(request.URL.RequestURI())
}

func validatePreviewPort(port uint16) error {
	if port < minPreviewPort || port == 7777 {
		return fmt.Errorf("%w: preview port must be between %d and 65535", domain.ErrInvalid, minPreviewPort)
	}
	return nil
}

func validatePreviewPath(value string) error {
	if value == "" || value[0] != '/' || len(value) > maxPreviewPath ||
		strings.ContainsAny(value, "\x00\r\n\\") {
		return fmt.Errorf("%w: preview path is invalid", domain.ErrInvalid)
	}
	return nil
}
