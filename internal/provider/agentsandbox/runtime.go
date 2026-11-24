package agentsandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/coder/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

type ForwardSession interface {
	URL() string
	Close() error
}

type Forwarder interface {
	Forward(context.Context, string, string, uint16) (ForwardSession, error)
}

func (p *Provider) runtimeClient(
	ctx context.Context,
	resourceID string,
) (*capsuleproto.Client, ForwardSession, error) {
	if !p.validResourceID(resourceID) {
		return nil, nil, domain.ErrNotFound
	}
	if p.forwarder == nil {
		return nil, nil, domain.ErrUnsupported
	}
	sandbox, err := p.sandboxes.Get(ctx, resourceID, metav1.GetOptions{})
	if err != nil {
		return nil, nil, mapKubernetesError("get Sandbox runtime", err)
	}
	capsuleID, err := p.verifySandboxIdentity(sandbox, "")
	if err != nil {
		return nil, nil, err
	}
	resource, err := p.resource(ctx, sandbox)
	if err != nil {
		return nil, nil, err
	}
	if resource.State == ports.ProviderPreparing &&
		sandbox.Spec.OperatingMode != sandboxv1beta1.SandboxOperatingModeSuspended {
		// The Sandbox should be running but its Pod is not ready, so the
		// supervisor and its sessions are gone (for example a Pod restart).
		// Kubernetes brings the Pod back; Meridian does not start it.
		return nil, nil, domain.ErrRuntimeStopped
	}
	if resource.State != ports.ProviderReady {
		return nil, nil, fmt.Errorf("%w: Capsule runtime is not active", domain.ErrIllegalTransition)
	}
	secret, err := p.core.CoreV1().Secrets(p.config.Namespace).Get(
		ctx, resourceID+"-token", metav1.GetOptions{},
	)
	if err != nil {
		return nil, nil, mapKubernetesError("get Capsule token Secret", err)
	}
	if err := p.verifyOwnedMeta(secret.ObjectMeta, domain.CapsuleID(capsuleID), "token"); err != nil {
		return nil, nil, err
	}
	if !metav1.IsControlledBy(secret, sandbox) {
		return nil, nil, fmt.Errorf("%w: Capsule token Secret owner differs", domain.ErrConflict)
	}
	token := string(secret.Data["token"])
	if len(token) < 32 || len(token) > 128 {
		return nil, nil, fmt.Errorf("%w: Capsule token Secret is invalid", domain.ErrCorrupt)
	}
	pod, err := p.ownedPod(ctx, sandbox)
	if err != nil {
		return nil, nil, err
	}
	session, err := p.forwarder.Forward(ctx, p.config.Namespace, pod.Name, protocolPort)
	if err != nil {
		return nil, nil, fmt.Errorf("port-forward capsuled: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client, err := capsuleproto.NewClient(
		session.URL(), token, &http.Client{Transport: transport},
	)
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	return client, session, nil
}

func (p *Provider) ownedPod(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
) (*corev1.Pod, error) {
	found, err := p.controlledPod(ctx, sandbox)
	if err != nil {
		return nil, err
	}
	if found.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("%w: Sandbox pod is not running", domain.ErrIllegalTransition)
	}
	return found, nil
}

func (p *Provider) controlledPod(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
) (*corev1.Pod, error) {
	capsuleID, err := p.verifySandboxIdentity(sandbox, "")
	if err != nil {
		return nil, err
	}
	identity := p.labels(domain.CapsuleID(capsuleID), "pod")[labelCapsuleHash]
	pods, err := p.core.CoreV1().Pods(p.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelOwned + "=meridian," + labelRole + "=pod," + labelCapsuleHash + "=" + identity,
	})
	if err != nil {
		return nil, mapKubernetesError("list Sandbox pods", err)
	}
	var found *corev1.Pod
	for index := range pods.Items {
		pod := &pods.Items[index]
		if !metav1.IsControlledBy(pod, sandbox) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: Sandbox controls multiple Capsule pods", domain.ErrConflict)
		}
		found = pod.DeepCopy()
	}
	if found == nil {
		return nil, fmt.Errorf("%w: Sandbox pod is unavailable", domain.ErrNotFound)
	}
	return found, nil
}

func (p *Provider) observeSandboxStartup(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	plan *ports.StartupPublisher,
) error {
	pod, err := p.controlledPod(ctx, sandbox)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "capsule" {
			continue
		}
		phase, failure := sandboxStartupPhase(status)
		if failure != nil {
			plan.Progress(domain.PhasePullingImage, 0, 0)
			return failure
		}
		if phase.Valid() {
			plan.Progress(phase, 0, 0)
		}
	}
	return nil
}

func sandboxStartupPhase(status corev1.ContainerStatus) (domain.StartupPhase, *domain.StartupFailure) {
	if status.State.Waiting != nil {
		if failure := domain.ClassifyImagePullWait(status.State.Waiting.Reason, status.State.Waiting.Message); failure != nil {
			return domain.PhasePullingImage, failure
		}
		if status.State.Waiting.Reason == "Pulling" {
			return domain.PhasePullingImage, nil
		}
	}
	if status.State.Running != nil {
		return domain.PhaseStartingHarness, nil
	}
	return "", nil
}

func (p *Provider) CaptureWorkspace(
	ctx context.Context,
	resourceID string,
) (ports.WorkspaceCapture, error) {
	resource, err := p.Get(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceCapture{}, err
	}
	if resource.State != ports.ProviderReady {
		return ports.WorkspaceCapture{}, fmt.Errorf(
			"%w: Capsule runtime is not Ready", domain.ErrIllegalTransition,
		)
	}
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceCapture{}, err
	}
	capture, err := client.CaptureWorkspace(ctx)
	if err != nil {
		_ = session.Close()
		return ports.WorkspaceCapture{}, mapRuntimeError(err)
	}
	return ports.WorkspaceCapture{
		Archive: &forwardedArchive{ReadCloser: capture.Archive, session: session},
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
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.RestoreWorkspace(ctx, digest, io.LimitReader(archive, size+1)))
}

func (p *Provider) StartRun(ctx context.Context, request ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		if request.GitHub != nil {
			request.GitHub.Wipe()
		}
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.StartRun(ctx, capsuleproto.RunStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Prompt: request.Prompt,
		Columns: request.Columns, Rows: request.Rows,
		Secrets: request.Secrets, Setup: request.Setup, Git: request.Git, Connection: request.Connection,
		GitHub: request.GitHub,
	})
	if request.GitHub != nil {
		request.GitHub.Wipe()
	}
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) StartStructured(
	ctx context.Context,
	request ports.RuntimeStructuredStartRequest,
) (ports.RuntimeRun, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		if request.GitHub != nil {
			request.GitHub.Wipe()
		}
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.StartStructured(ctx, capsuleproto.StructuredStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Frame: request.Frame,
		Secrets: request.Secrets, Setup: request.Setup, Git: request.Git, Connection: request.Connection,
		Notes: request.Notes, GitHub: request.GitHub,
	})
	if request.GitHub != nil {
		request.GitHub.Wipe()
	}
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) GetStructured(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.StructuredStatus(ctx, string(runID))
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) SendStructured(
	ctx context.Context,
	request ports.RuntimeStructuredSendRequest,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.SendStructured(ctx, string(request.RunID), request.Frame))
}

func (p *Provider) StructuredEvents(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeStructuredEvents, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeStructuredEvents{}, err
	}
	defer session.Close()
	result, err := client.StructuredEvents(ctx, string(runID), after, 0)
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
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.CancelStructured(ctx, string(runID))
	return structuredRuntimeRun(result), mapRuntimeError(err)
}

func (p *Provider) HarnessProfiles(
	ctx context.Context,
	resourceID string,
) ([]ports.RuntimeHarnessProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	response, err := client.HarnessProfiles(ctx)
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
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.RunStatus(ctx, string(runID))
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) CancelRun(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
) (ports.RuntimeRun, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.CancelRun(ctx, string(runID))
	return runtimeRun(result), mapRuntimeError(err)
}

func (p *Provider) RunEvents(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeEvents, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.RuntimeEvents{}, err
	}
	defer session.Close()
	result, err := client.RunEvents(ctx, string(runID), after)
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

func (p *Provider) StartShell(ctx context.Context, resourceID string, columns, rows uint16, github *harnesssetup.GitHubAccess) error {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		if github != nil {
			github.Wipe()
		}
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.StartShell(ctx, columns, rows, github))
}

func (p *Provider) RefreshGitHubToken(ctx context.Context, resourceID, token string) error {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.RefreshGitHubToken(ctx, token))
}

func (p *Provider) AttachShell(ctx context.Context, resourceID string) (ports.RuntimeAttachment, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	connection, err := client.AttachShell(ctx)
	if err != nil {
		_ = session.Close()
		return nil, mapRuntimeError(err)
	}
	return &attachment{connection: connection, session: session}, nil
}

func (p *Provider) AttachRun(
	ctx context.Context,
	resourceID string,
	runID domain.RunID,
	after uint64,
) (ports.RuntimeAttachment, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	connection, err := client.Attach(ctx, string(runID), after)
	if err != nil {
		_ = session.Close()
		return nil, mapRuntimeError(err)
	}
	return &attachment{connection: connection, session: session}, nil
}

func (p *Provider) GitStatus(ctx context.Context, resourceID string) (ports.GitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.GitResult{}, err
	}
	defer session.Close()
	result, err := client.GitStatus(ctx)
	return ports.GitResult{Content: result.Content, Truncated: result.Truncated}, mapRuntimeError(err)
}

func (p *Provider) GitDiff(ctx context.Context, resourceID string) (ports.GitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.GitResult{}, err
	}
	defer session.Close()
	result, err := client.GitDiff(ctx)
	return ports.GitResult{Content: result.Content, Truncated: result.Truncated}, mapRuntimeError(err)
}

func (p *Provider) GitPatch(ctx context.Context, resourceID string, request ports.GitPatchRequest) (ports.GitPatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.GitPatchResult{}, err
	}
	defer session.Close()
	result, err := client.GitPatch(ctx, capsuleproto.GitPatchRequest{
		BaseCommit: request.BaseCommit, MaxBytes: request.MaxBytes, IncludePatch: request.IncludePatch,
	})
	return ports.GitPatchResult{
		Patch: result.Patch, Truncated: result.Truncated, Head: result.Head, BaseCommit: result.BaseCommit,
		FilesChanged: result.FilesChanged, Insertions: result.Insertions, Deletions: result.Deletions,
	}, mapRuntimeError(err)
}

func (p *Provider) ListWorkspaceFiles(
	ctx context.Context, resourceID, filePath, after string, limit int,
) (ports.WorkspaceFilePage, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceFilePage{}, err
	}
	defer session.Close()
	result, err := client.BrowseList(ctx, capsuleproto.BrowseListRequest{
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
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.WorkspaceFile{}, err
	}
	defer session.Close()
	result, err := client.BrowseRead(ctx, capsuleproto.BrowseReadRequest{Path: filePath})
	if err != nil {
		return ports.WorkspaceFile{}, mapRuntimeError(err)
	}
	return ports.WorkspaceFile{
		Path: result.Path, Content: result.Content, Size: result.Size,
		Executable: result.Executable,
	}, nil
}

func (p *Provider) WriteWorkspaceFile(
	ctx context.Context, resourceID, filePath string, content []byte,
) error {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	err = client.BrowseWrite(ctx, capsuleproto.BrowseWriteRequest{Path: filePath, Content: content})
	return mapRuntimeError(err)
}

func (p *Provider) InspectDelivery(
	ctx context.Context, resourceID string,
) (ports.DeliveryInspection, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.DeliveryInspection{}, err
	}
	defer session.Close()
	result, err := client.DeliveryState(ctx)
	ahead, unpushed, deliverable := result.NormalizedCounts()
	return ports.DeliveryInspection{
		HEAD: result.HEAD, Branch: result.Branch, Dirty: result.Dirty,
		OriginURL: result.OriginURL, DefaultBranch: result.DefaultBranch, Tree: result.Tree,
		AheadCount: ahead, UnpushedCount: unpushed, HasDeliverableChanges: deliverable,
	}, mapRuntimeError(err)
}

func (p *Provider) CommitDelivery(
	ctx context.Context, request ports.DeliveryCommitRequest,
) (ports.DeliveryCommitResult, error) {
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.DeliveryCommitResult{}, err
	}
	defer session.Close()
	result, err := client.DeliveryCommit(ctx, capsuleproto.DeliveryCommitRequest{
		Message: request.Message, AuthorName: request.AuthorName,
		AuthorEmail: request.AuthorEmail, ExpectedHEAD: request.ExpectedHEAD,
		ExpectedTree: request.ExpectedTree,
	})
	return ports.DeliveryCommitResult{Commit: result.Commit, Tree: result.Tree}, mapRuntimeError(err)
}

func (p *Provider) PushDelivery(
	ctx context.Context, request ports.DeliveryPushRequest,
) (ports.DeliveryPushResult, error) {
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.DeliveryPushResult{}, err
	}
	defer session.Close()
	push, wipe := pushRequest(request)
	defer wipe()
	result, err := client.DeliveryPush(ctx, push)
	return ports.DeliveryPushResult{
		Commit: result.Commit, DestinationRef: result.DestinationRef,
	}, mapRuntimeError(err)
}

func (p *Provider) DiscoverPreviewPorts(
	ctx context.Context,
	resourceID string,
) ([]ports.PreviewPort, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	response, err := client.PreviewPorts(ctx)
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
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.PreviewResponse{}, err
	}
	defer session.Close()
	status, header, body, err := client.ForwardPreviewHTTP(ctx, port, request)
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
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, "", err
	}
	if header == nil {
		header = make(http.Header)
	}
	connection, err := client.AttachPreview(ctx, port, path, header)
	if err != nil {
		_ = session.Close()
		return nil, "", mapRuntimeError(err)
	}
	return &attachment{connection: connection, session: session}, connection.Subprotocol(), nil
}

func runtimeRun(value capsuleproto.RunStatusResponse) ports.RuntimeRun {
	result := ports.RuntimeRun{Failure: value.Failure, Cursor: value.Cursor, PTY: value.PTY}
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
	result.Negotiated = value.Capabilities != nil
	result.Models = value.Capabilities != nil && value.Capabilities.Models
	result.Effort = result.Models && value.Capabilities.Effort
	result.Notes = value.Capabilities != nil && value.Capabilities.Notes
	return result
}

func mapRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var protocolError *capsuleproto.Error
	if errors.As(err, &protocolError) {
		if mapped := domain.MapCapsuleProtocolError(protocolError.Status, protocolError.Code); mapped != nil {
			return fmt.Errorf("%w: %w", mapped, protocolError)
		}
	}
	return err
}

type forwardedArchive struct {
	io.ReadCloser
	session ForwardSession
}

func (a *forwardedArchive) Close() error {
	return errors.Join(a.ReadCloser.Close(), a.session.Close())
}

type attachment struct {
	connection *websocket.Conn
	session    ForwardSession
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
	return errors.Join(
		a.connection.Close(websocket.StatusNormalClosure, ""),
		a.session.Close(),
	)
}

func (p *Provider) RunChecks(ctx context.Context, resourceID string, request ports.CheckRequest) (ports.CheckExecution, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.CheckExecution{}, err
	}
	defer session.Close()
	input := capsuleproto.CheckRunRequest{OutputLimit: request.OutputLimit}
	argv := make(map[string][]string, len(request.Commands))
	for _, command := range request.Commands {
		seconds := int(command.Timeout / time.Second)
		if seconds <= 0 {
			seconds = 1
		}
		input.Checks = append(input.Checks, capsuleproto.CheckCommand{
			Name: command.Name, Argv: command.Argv, TimeoutSeconds: seconds,
		})
		argv[command.Name] = command.Argv
	}
	result, err := client.RunChecks(ctx, input)
	if err != nil {
		return ports.CheckExecution{}, mapRuntimeError(err)
	}
	execution := ports.CheckExecution{
		HeadBefore: result.HeadBefore, TreeBefore: result.TreeBefore,
		HeadAfter: result.HeadAfter, TreeAfter: result.TreeAfter, Stale: result.Stale,
	}
	for _, item := range result.Results {
		execution.Results = append(execution.Results, ports.CheckCommandResult{
			Name: item.Name, Argv: append([]string{}, argv[item.Name]...),
			ExitCode: item.ExitCode, Duration: time.Duration(item.Duration) * time.Millisecond,
			TimedOut: item.TimedOut, Truncated: item.Truncated, Output: []byte(item.Output),
		})
	}
	return execution, nil
}

func (p *Provider) SuggestChecks(ctx context.Context, resourceID string) (ports.CheckSuggestion, error) {
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return ports.CheckSuggestion{}, err
	}
	defer session.Close()
	result, err := client.SuggestChecks(ctx)
	if err != nil {
		return ports.CheckSuggestion{}, mapRuntimeError(err)
	}
	suggestion := ports.CheckSuggestion{Ignored: result.Ignored}
	for _, check := range result.Checks {
		suggestion.Checks = append(suggestion.Checks, ports.CheckCommand{
			Name: check.Name, Argv: append([]string{}, check.Argv...),
			Timeout: time.Duration(check.TimeoutSeconds) * time.Second,
		})
	}
	return suggestion, nil
}

// pushRequest converts a provider push request. wipe clears the copied
// credential or capability once the request is sent.
func pushRequest(request ports.DeliveryPushRequest) (capsuleproto.DeliveryPushRequest, func()) {
	push := capsuleproto.DeliveryPushRequest{
		SourceCommit: request.SourceCommit, DestinationRef: request.DestinationRef,
		ExpectedOldRef: request.ExpectedOldRef,
	}
	if request.Gateway != nil {
		push.Gateway = &capsuleproto.GitGatewayPush{
			URL: request.Gateway.URL, CA: request.Gateway.CA,
			Username: request.Gateway.Username, Capability: request.Gateway.Capability,
		}
		return push, func() { push.Gateway.Capability = "" }
	}
	push.GitCredential = &capsuleproto.GitHTTPSCredential{
		Username: request.GitCredential.Username, Password: request.GitCredential.Password,
	}
	return push, func() { push.GitCredential.Username, push.GitCredential.Password = "", "" }
}
