package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/coder/websocket"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (p *Provider) CaptureWorkspace(ctx context.Context, resourceID string) (ports.WorkspaceCapture, error) {
	resource, err := p.Get(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceCapture{}, err
	}
	if resource.State != ports.ProviderReady {
		return ports.WorkspaceCapture{}, fmt.Errorf("%w: Capsule runtime is not Ready", domain.ErrIllegalTransition)
	}
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceCapture{}, err
	}
	capture, err := supervisor.CaptureWorkspace(ctx)
	if err != nil {
		return ports.WorkspaceCapture{}, mapRuntimeError(err)
	}
	return ports.WorkspaceCapture{
		Archive: capture.Archive,
		Metadata: ports.SnapshotMetadata{
			ImageDigest: resource.ImageDigest, GitBranch: capture.Branch,
			GitHEAD: capture.HEAD, GitDirtySummary: capture.Dirty,
		},
	}, nil
}

func (p *Provider) RestoreWorkspace(
	ctx context.Context,
	resourceID, digest string,
	size int64,
	archive io.Reader,
) error {
	if size < 0 {
		return fmt.Errorf("%w: archive size is invalid", domain.ErrInvalid)
	}
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.RestoreWorkspace(ctx, digest, io.LimitReader(archive, size+1)))
}

func (p *Provider) StartRun(ctx context.Context, request ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.StartRun(ctx, capsuleproto.RunStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Prompt: request.Prompt,
		Columns: request.Columns, Rows: request.Rows,
		Secrets: request.Secrets,
	})
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) StartStructured(
	ctx context.Context,
	request ports.RuntimeStructuredStartRequest,
) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.StartStructured(ctx, capsuleproto.StructuredStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Frame: request.Frame,
		Secrets: request.Secrets,
	})
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) GetStructured(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.StructuredStatus(ctx, string(runID))
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) SendStructured(
	ctx context.Context,
	request ports.RuntimeStructuredSendRequest,
) error {
	supervisor, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.SendStructured(ctx, string(request.RunID), request.Frame))
}

func (p *Provider) StructuredEvents(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeStructuredEvents, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeStructuredEvents{}, err
	}
	result, err := supervisor.StructuredEvents(ctx, string(runID), after, 0)
	if err != nil {
		return ports.RuntimeStructuredEvents{}, mapRuntimeError(err)
	}
	output := ports.RuntimeStructuredEvents{NextCursor: result.NextCursor, Gap: result.Gap}
	for _, event := range result.Events {
		output.Items = append(output.Items, ports.RuntimeStructuredEvent{
			Sequence: event.Sequence, Frame: event.Frame,
		})
	}
	return output, nil
}

func (p *Provider) CancelStructured(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.CancelStructured(ctx, string(runID))
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) StructuredProfiles(
	ctx context.Context,
	resourceID string,
) ([]ports.RuntimeHarnessProfile, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	response, err := supervisor.HarnessProfiles(ctx)
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	result := make([]ports.RuntimeHarnessProfile, len(response.Items))
	for index, item := range response.Items {
		result[index] = ports.RuntimeHarnessProfile{
			Name: item.Name, Structured: item.Structured, AdapterKind: item.AdapterKind,
			Protocol: item.Protocol, PTY: item.PTY,
		}
	}
	return result, nil
}

func (p *Provider) GetRun(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.RunStatus(ctx, string(runID))
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) CancelRun(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	result, err := supervisor.CancelRun(ctx, string(runID))
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) RunEvents(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeEvents, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeEvents{}, err
	}
	result, err := supervisor.RunEvents(ctx, string(runID), after)
	if err != nil {
		return ports.RuntimeEvents{}, mapRuntimeError(err)
	}
	output := ports.RuntimeEvents{NextCursor: result.NextCursor, Gap: result.Gap}
	for _, event := range result.Events {
		output.Items = append(output.Items, ports.RuntimeEvent{
			Sequence: event.Sequence, Type: event.Type,
		})
	}
	return output, nil
}

func (p *Provider) AttachRun(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeAttachment, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	connection, err := supervisor.Attach(ctx, string(runID), after)
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	return &attachment{connection: connection}, nil
}

func (p *Provider) GitStatus(ctx context.Context, resourceID string) (ports.GitResult, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.GitResult{}, err
	}
	result, err := supervisor.GitStatus(ctx)
	return ports.GitResult{Content: result.Content, Truncated: result.Truncated}, mapRuntimeError(err)
}

func (p *Provider) GitDiff(ctx context.Context, resourceID string) (ports.GitResult, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.GitResult{}, err
	}
	result, err := supervisor.GitDiff(ctx)
	return ports.GitResult{Content: result.Content, Truncated: result.Truncated}, mapRuntimeError(err)
}

func (p *Provider) ListWorkspaceFiles(
	ctx context.Context, resourceID, filePath, after string, limit int,
) (ports.WorkspaceFilePage, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceFilePage{}, err
	}
	result, err := supervisor.BrowseList(ctx, capsuleproto.BrowseListRequest{
		Path: filePath, After: after, Limit: limit,
	})
	if err != nil {
		return ports.WorkspaceFilePage{}, mapRuntimeError(err)
	}
	output := ports.WorkspaceFilePage{Path: result.Path, NextAfter: result.NextAfter}
	for _, item := range result.Items {
		output.Items = append(output.Items, ports.WorkspaceFileEntry{
			Name: item.Name, Type: item.Type, Size: item.Size, Executable: item.Executable,
		})
	}
	return output, nil
}

func (p *Provider) ReadWorkspaceFile(
	ctx context.Context, resourceID, filePath string,
) (ports.WorkspaceFile, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceFile{}, err
	}
	result, err := supervisor.BrowseRead(ctx, capsuleproto.BrowseReadRequest{Path: filePath})
	if err != nil {
		return ports.WorkspaceFile{}, mapRuntimeError(err)
	}
	return ports.WorkspaceFile{
		Path: result.Path, Content: result.Content, Size: result.Size,
		Executable: result.Executable,
	}, nil
}

func (p *Provider) InspectDelivery(
	ctx context.Context, resourceID string,
) (ports.DeliveryInspection, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.DeliveryInspection{}, err
	}
	result, err := supervisor.DeliveryState(ctx)
	return ports.DeliveryInspection{
		HEAD: result.HEAD, Branch: result.Branch, Dirty: result.Dirty,
		OriginURL: result.OriginURL, DefaultBranch: result.DefaultBranch, Tree: result.Tree,
	}, mapRuntimeError(err)
}

func (p *Provider) CommitDelivery(
	ctx context.Context, request ports.DeliveryCommitRequest,
) (ports.DeliveryCommitResult, error) {
	supervisor, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.DeliveryCommitResult{}, err
	}
	result, err := supervisor.DeliveryCommit(ctx, capsuleproto.DeliveryCommitRequest{
		Message: request.Message, AuthorName: request.AuthorName,
		AuthorEmail: request.AuthorEmail, ExpectedHEAD: request.ExpectedHEAD,
		ExpectedTree: request.ExpectedTree,
	})
	return ports.DeliveryCommitResult{Commit: result.Commit, Tree: result.Tree}, mapRuntimeError(err)
}

func (p *Provider) PushDelivery(
	ctx context.Context, request ports.DeliveryPushRequest,
) (ports.DeliveryPushResult, error) {
	supervisor, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.DeliveryPushResult{}, err
	}
	credential := &capsuleproto.GitHTTPSCredential{
		Username: request.GitCredential.Username, Password: request.GitCredential.Password,
	}
	defer func() { credential.Username, credential.Password = "", "" }()
	result, err := supervisor.DeliveryPush(ctx, capsuleproto.DeliveryPushRequest{
		SourceCommit: request.SourceCommit, DestinationRef: request.DestinationRef,
		ExpectedOldRef: request.ExpectedOldRef, GitCredential: credential,
	})
	return ports.DeliveryPushResult{
		Commit: result.Commit, DestinationRef: result.DestinationRef,
	}, mapRuntimeError(err)
}

func (p *Provider) DiscoverPreviewPorts(
	ctx context.Context,
	resourceID string,
) ([]ports.PreviewPort, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	response, err := supervisor.PreviewPorts(ctx)
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	result := make([]ports.PreviewPort, len(response.Items))
	for index, item := range response.Items {
		result[index] = ports.PreviewPort{Port: item.Port}
	}
	return result, nil
}

func (p *Provider) ForwardPreviewHTTP(
	ctx context.Context,
	resourceID string,
	port uint16,
	request *http.Request,
) (ports.PreviewResponse, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.PreviewResponse{}, err
	}
	status, header, body, err := supervisor.ForwardPreviewHTTP(ctx, port, request)
	if err != nil {
		return ports.PreviewResponse{}, mapRuntimeError(err)
	}
	return ports.PreviewResponse{StatusCode: status, Header: header, Body: body}, nil
}

func (p *Provider) AttachPreview(
	ctx context.Context,
	resourceID string,
	port uint16,
	path string,
	header http.Header,
) (ports.RuntimeAttachment, string, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, "", err
	}
	connection, err := supervisor.AttachPreview(ctx, port, path, header)
	if err != nil {
		return nil, "", mapRuntimeError(err)
	}
	return &attachment{connection: connection}, connection.Subprotocol(), nil
}

func (p *Provider) runtimeClient(ctx context.Context, resourceID string) (*capsuleproto.Client, error) {
	if !p.validResourceID(resourceID) {
		return nil, domain.ErrNotFound
	}
	inspection, err := p.engine.ContainerInspect(ctx, resourceID, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Capsule runtime: %w", err)
	}
	capsuleID, err := p.verifyLabels(inspection.Container.Config.Labels, "", "capsule")
	if err != nil {
		return nil, err
	}
	if inspection.Container.State == nil || !inspection.Container.State.Running ||
		inspection.Container.State.Paused {
		return nil, fmt.Errorf("%w: Capsule runtime is not active", domain.ErrIllegalTransition)
	}
	token, err := p.tokens.load(domain.CapsuleID(capsuleID))
	if err != nil {
		return nil, fmt.Errorf("load Capsule runtime token: %w", err)
	}
	return p.supervisorClient(inspection.Container, token)
}

func runtimeRun(value capsuleproto.RunStatusResponse) ports.RuntimeRun {
	result := ports.RuntimeRun{
		Failure: value.Failure, Cursor: value.Cursor, PTY: value.PTY,
	}
	switch value.State {
	case capsuleproto.RunStarting:
		result.State = domain.RunStarting
	case capsuleproto.RunRunning:
		result.State = domain.RunRunning
	case capsuleproto.RunSucceeded:
		result.State = domain.RunSucceeded
	case capsuleproto.RunFailed:
		result.State = domain.RunFailed
	case capsuleproto.RunCancelled:
		result.State = domain.RunCancelled
	default:
		result.State = domain.RunFailed
		result.Failure = "Capsule runtime returned an unknown Run state"
	}
	if value.ExitCode != nil {
		result.ExitStatus, result.HasExit = *value.ExitCode, true
	}
	return result
}

func structuredRuntimeRun(value capsuleproto.StructuredStatusResponse) ports.RuntimeRun {
	result := runtimeRun(value.RunStatusResponse)
	result.Structured = true
	return result
}

func mapRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var protocolError *capsuleproto.Error
	if errors.As(err, &protocolError) {
		switch protocolError.Status {
		case http.StatusNotFound:
			return domain.ErrNotFound
		case http.StatusTooManyRequests:
			return domain.ErrResourceExhausted
		case http.StatusConflict:
			return domain.ErrConflict
		case http.StatusBadRequest:
			return domain.ErrInvalid
		case http.StatusUnprocessableEntity:
			if protocolError.Code == "structured_interaction_unavailable" ||
				strings.HasPrefix(protocolError.Code, "harness_configuration_") {
				return domain.ErrUnsupported
			}
			return domain.ErrIllegalTransition
		}
	}
	return err
}

type attachment struct {
	connection *websocket.Conn
}

func (a *attachment) Read(ctx context.Context) (bool, []byte, error) {
	messageType, value, err := a.connection.Read(ctx)
	return messageType == websocket.MessageBinary, value, err
}

func (a *attachment) Write(ctx context.Context, binary bool, value []byte) error {
	messageType := websocket.MessageText
	if binary {
		messageType = websocket.MessageBinary
	}
	return a.connection.Write(ctx, messageType, value)
}

func (a *attachment) Close() error {
	return a.connection.Close(websocket.StatusNormalClosure, "")
}
