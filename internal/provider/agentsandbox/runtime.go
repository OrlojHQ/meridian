package agentsandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
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
	if found.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("%w: Sandbox pod is not running", domain.ErrIllegalTransition)
	}
	return found, nil
}

func (p *Provider) StartRun(ctx context.Context, request ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, request.ResourceID)
	if err != nil {
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.StartRun(ctx, capsuleproto.RunStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Prompt: request.Prompt,
		Columns: request.Columns, Rows: request.Rows,
	})
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
		return ports.RuntimeRun{}, err
	}
	defer session.Close()
	result, err := client.StartStructured(ctx, capsuleproto.StructuredStartRequest{
		RunID: string(request.RunID), Harness: request.Harness, Frame: request.Frame,
	})
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

func (p *Provider) StructuredProfiles(
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
