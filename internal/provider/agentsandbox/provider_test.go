package agentsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/contract"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	snapshotfake "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	sandboxfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
)

type staticSession struct {
	url    string
	closed bool
}

func (s *staticSession) URL() string { return s.url }
func (s *staticSession) Close() error {
	s.closed = true
	return nil
}

type recordingForwarder struct {
	url      string
	sessions []*staticSession
}

func (f *recordingForwarder) Forward(context.Context, string, string, uint16) (ForwardSession, error) {
	session := &staticSession{url: f.url}
	f.sessions = append(f.sessions, session)
	return session, nil
}

func runtimeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Meridian-Protocol-Version", "meridian.capsule.v1")
		switch request.URL.Path {
		case "/v1/prepare":
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ready"})
		case "/v1/status":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"version": "meridian.capsule.v1", "preparation": "ready",
			})
		case "/v1/git/status":
			_ = json.NewEncoder(writer).Encode(map[string]any{"content": "clean", "truncated": false})
		case "/v1/git/diff":
			_, _ = writer.Write([]byte("{"))
		case "/v1/structured/sessions":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"runId": "structured-run", "state": "Running",
				"startedAt": time.Now().UTC(), "pty": false, "cursor": 0,
				"protocol": adapterproto.Version, "restartCount": 0,
			})
		case "/v1/structured/sessions/structured-run":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"runId": "structured-run", "state": "Running",
				"startedAt": time.Now().UTC(), "pty": false, "cursor": 1,
				"protocol": adapterproto.Version, "restartCount": 0,
			})
		case "/v1/structured/sessions/structured-run/frames":
			writer.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "accepted"})
		case "/v1/structured/sessions/structured-run/events":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"events": []map[string]any{{
					"sequence": 1,
					"frame": map[string]any{
						"protocol": adapterproto.Version, "type": "status",
						"status": "running", "cursor": 1,
					},
				}},
				"nextCursor": 1,
			})
		case "/v1/structured/sessions/structured-run/cancel":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"runId": "structured-run", "state": "Cancelled",
				"startedAt": time.Now().UTC(), "pty": false, "cursor": 1,
				"protocol": adapterproto.Version, "restartCount": 0,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
}

func testProvider(t *testing.T) (*Provider, *sandboxfake.Clientset, *kubernetesfake.Clientset) {
	t.Helper()
	server := runtimeServer(t)
	t.Cleanup(server.Close)
	agents := sandboxfake.NewSimpleClientset()
	core := kubernetesfake.NewSimpleClientset()
	namespace := "test"

	agents.PrependReactor("create", "sandboxes", func(action ktesting.Action) (bool, runtime.Object, error) {
		create := action.(ktesting.CreateAction)
		sandbox := create.GetObject().(*sandboxv1beta1.Sandbox).DeepCopy()
		sandbox.UID = types.UID("sandbox-uid-" + sandbox.Name)
		sandbox.ResourceVersion = "1"
		setReady(sandbox)
		if err := agents.Tracker().Create(
			sandboxv1beta1.GroupVersion.WithResource("sandboxes"), sandbox, namespace,
		); err != nil {
			return true, nil, err
		}
		controller := true
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: sandbox.Name + "-pod", Namespace: namespace,
				Labels: map[string]string{
					labelOwned: "meridian", labelRole: "pod",
					labelCapsuleHash: sandbox.Labels[labelCapsuleHash],
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: sandboxv1beta1.GroupVersion.String(), Kind: "Sandbox",
					Name: sandbox.Name, UID: sandbox.UID, Controller: &controller,
				}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "capsule", ImageID: "containerd://sha256:" + strings.Repeat("a", 64),
				}},
			},
		}
		if _, err := core.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			return true, nil, err
		}
		return true, sandbox, nil
	})
	agents.PrependReactor("update", "sandboxes", func(action ktesting.Action) (bool, runtime.Object, error) {
		update := action.(ktesting.UpdateAction)
		sandbox := update.GetObject().(*sandboxv1beta1.Sandbox).DeepCopy()
		sandbox.ResourceVersion = "2"
		if sandbox.Spec.OperatingMode == sandboxv1beta1.SandboxOperatingModeSuspended {
			sandbox.Status.Conditions = []metav1.Condition{{
				Type:   string(sandboxv1beta1.SandboxConditionSuspended),
				Status: metav1.ConditionTrue, Reason: sandboxv1beta1.SandboxReasonSuspendedPodTerminated,
			}}
		} else {
			setReady(sandbox)
		}
		if err := agents.Tracker().Update(
			sandboxv1beta1.GroupVersion.WithResource("sandboxes"), sandbox, namespace,
		); err != nil {
			return true, nil, err
		}
		return true, sandbox, nil
	})
	discovery := core.Discovery().(*fakediscovery.FakeDiscovery)
	discovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: sandboxv1beta1.GroupVersion.String(),
			APIResources: []metav1.APIResource{{Name: "sandboxes", Namespaced: true, Kind: "Sandbox"}},
		},
	}
	forwarder := &recordingForwarder{url: server.URL}
	provider, err := NewWithClients(Config{
		Namespace: namespace, Image: "example.invalid/meridian@sha256:" + strings.Repeat("b", 64),
		StorageClass: "workspace", OperationTimeout: 2 * time.Second,
	}, nil, agents.AgentsV1beta1().Sandboxes(namespace), core, discovery, nil, forwarder)
	if err != nil {
		t.Fatal(err)
	}
	return provider, agents, core
}

func setReady(sandbox *sandboxv1beta1.Sandbox) {
	sandbox.Status.Conditions = []metav1.Condition{{
		Type: string(sandboxv1beta1.SandboxConditionReady), Status: metav1.ConditionTrue,
		Reason: sandboxv1beta1.SandboxReasonDependenciesReady,
	}}
}

func TestProviderContract(t *testing.T) {
	contract.Test(t, func() ports.CapsuleProvider {
		provider, _, _ := testProvider(t)
		return provider
	})
}

func TestOwnedAdoptionAndForeignRefusal(t *testing.T) {
	provider, agents, _ := testProvider(t)
	request := ports.CreateCapsuleRequest{CapsuleID: "capsule-one"}
	first, err := provider.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Create(context.Background(), request)
	if err != nil || first.ID != second.ID {
		t.Fatalf("adoption = %#v, %v", second, err)
	}
	foreign := provider.sandbox("foreign", provider.resourceName("capsule-two"), provider.config.Image)
	foreign.Labels[labelOwned] = "someone-else"
	foreign.UID = "foreign"
	setReady(foreign)
	if err := agents.Tracker().Create(
		sandboxv1beta1.GroupVersion.WithResource("sandboxes"), foreign, provider.config.Namespace,
	); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-two"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("foreign adoption error = %v", err)
	}
}

func TestRestartRecoveryAndResourceVersionConflict(t *testing.T) {
	provider, agents, core := testProvider(t)
	request := ports.CreateCapsuleRequest{CapsuleID: "capsule-restart"}
	resource, err := provider.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithClients(
		provider.config, nil, agents.AgentsV1beta1().Sandboxes(provider.config.Namespace),
		core, core.Discovery(), nil, provider.forwarder,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Get(context.Background(), resource.ID)
	if err != nil || recovered.State != ports.ProviderReady {
		t.Fatalf("restart recovery = %#v, %v", recovered, err)
	}
	conflicts := 1
	agents.PrependReactor("update", "sandboxes", func(action ktesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			return false, nil, nil
		}
		conflicts--
		return true, nil, apierrors.NewConflict(
			sandboxv1beta1.GroupVersion.WithResource("sandboxes").GroupResource(),
			resource.ID, errors.New("injected resource version conflict"),
		)
	})
	paused, err := restarted.Pause(context.Background(), resource.ID)
	if err != nil || paused.State != ports.ProviderPaused || conflicts != 0 {
		t.Fatalf("conflict retry pause = %#v, %v, conflicts=%d", paused, err, conflicts)
	}
	transientGets := 1
	agents.PrependReactor("get", "sandboxes", func(action ktesting.Action) (bool, runtime.Object, error) {
		if transientGets == 0 {
			return false, nil, nil
		}
		transientGets--
		return true, nil, apierrors.NewServiceUnavailable("injected reconnect")
	})
	resumed, err := restarted.Resume(context.Background(), resource.ID)
	if err != nil || resumed.State != ports.ProviderReady || transientGets != 0 {
		t.Fatalf("transient reconnect resume = %#v, %v, gets=%d", resumed, err, transientGets)
	}
}

func TestCreateCancellationIsBoundedAndRecoverable(t *testing.T) {
	provider, agents, _ := testProvider(t)
	agents.PrependReactor("create", "sandboxes", func(action ktesting.Action) (bool, runtime.Object, error) {
		create := action.(ktesting.CreateAction)
		sandbox := create.GetObject().(*sandboxv1beta1.Sandbox).DeepCopy()
		sandbox.UID = "cancelled-create"
		sandbox.ResourceVersion = "1"
		sandbox.Status.Conditions = []metav1.Condition{{
			Type:   string(sandboxv1beta1.SandboxConditionReady),
			Status: metav1.ConditionFalse,
			Reason: sandboxv1beta1.SandboxReasonDependenciesNotReady,
		}}
		if err := agents.Tracker().Create(
			sandboxv1beta1.GroupVersion.WithResource("sandboxes"),
			sandbox,
			provider.config.Namespace,
		); err != nil {
			return true, nil, err
		}
		return true, sandbox, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := ports.CreateCapsuleRequest{CapsuleID: "capsule-cancelled"}
	if _, err := provider.Create(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Create error = %v", err)
	}
	resource, err := provider.Get(context.Background(), provider.resourceName(request.CapsuleID))
	if err != nil || resource.State != ports.ProviderPreparing {
		t.Fatalf("recoverable partial create = %#v, %v", resource, err)
	}
}

func TestTokenSecretOwnershipAndCleanup(t *testing.T) {
	provider, _, core := testProvider(t)
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-one"})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := core.CoreV1().Secrets(provider.config.Namespace).Get(
		context.Background(), resource.ID+"-token", metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secret.StringData != nil || len(secret.Data["token"]) < 32 || !*secret.Immutable {
		t.Fatalf("unsafe token Secret: %#v", secret)
	}
	for key, value := range secret.Labels {
		if strings.Contains(strings.ToLower(key+value), "token=") {
			t.Fatalf("token leaked into labels: %q=%q", key, value)
		}
	}
	if err := provider.Delete(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "Sandbox" {
		t.Fatalf("Secret owner references = %#v", secret.OwnerReferences)
	}
	if _, err := core.CoreV1().Secrets(provider.config.Namespace).Get(
		context.Background(), resource.ID+"-token", metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("token Secret survived deletion: %v", err)
	}
}

func TestReadinessFailureAndUnsupportedSnapshots(t *testing.T) {
	provider, agents, _ := testProvider(t)
	sandbox := provider.sandbox("capsule-one", provider.resourceName("capsule-one"), provider.config.Image)
	sandbox.UID = "uid"
	sandbox.Status.Conditions = []metav1.Condition{{
		Type: string(sandboxv1beta1.SandboxConditionReady), Status: metav1.ConditionFalse,
		Reason: sandboxv1beta1.SandboxReasonPodFailed, Message: "container exited",
	}}
	if err := agents.Tracker().Create(
		sandboxv1beta1.GroupVersion.WithResource("sandboxes"), sandbox, provider.config.Namespace,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Get(context.Background(), sandbox.Name); err == nil {
		t.Fatal("failed readiness was accepted")
	}
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Snapshot || capabilities.Clone || capabilities.Preview {
		t.Fatalf("unsupported capabilities advertised: %#v", capabilities)
	}
	if !capabilities.Structured {
		t.Fatalf("configured private transport did not advertise structured support: %#v", capabilities)
	}
	provider.forwarder = nil
	withoutTransport, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if withoutTransport.Structured || withoutTransport.Run || withoutTransport.Attach ||
		withoutTransport.Git {
		t.Fatalf("runtime capabilities advertised without port-forward transport: %#v", withoutTransport)
	}
	if _, err := provider.CaptureWorkspace(context.Background(), sandbox.Name); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("snapshot error = %v", err)
	}
}

func TestStaleConditionsDoNotCompleteLifecycle(t *testing.T) {
	provider, _, _ := testProvider(t)
	sandbox := provider.sandbox("capsule-stale", provider.resourceName("capsule-stale"), provider.config.Image)
	sandbox.UID = "stale-uid"
	sandbox.Generation = 2
	sandbox.Status.Conditions = []metav1.Condition{{
		Type:               string(sandboxv1beta1.SandboxConditionReady),
		Status:             metav1.ConditionTrue,
		Reason:             sandboxv1beta1.SandboxReasonDependenciesReady,
		ObservedGeneration: 1,
	}}
	resource, err := provider.resource(context.Background(), sandbox)
	if err != nil || resource.State != ports.ProviderPreparing {
		t.Fatalf("stale Ready condition = %#v, %v", resource, err)
	}
	sandbox.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	sandbox.Status.Conditions = []metav1.Condition{{
		Type:               string(sandboxv1beta1.SandboxConditionSuspended),
		Status:             metav1.ConditionTrue,
		Reason:             sandboxv1beta1.SandboxReasonSuspendedPodTerminated,
		ObservedGeneration: 1,
	}}
	resource, err = provider.resource(context.Background(), sandbox)
	if err != nil || resource.State != ports.ProviderPreparing {
		t.Fatalf("stale Suspended condition = %#v, %v", resource, err)
	}
}

func TestSnapshotPrerequisiteDiscoveryDoesNotOverclaimMomentSupport(t *testing.T) {
	provider, _, core := testProvider(t)
	if _, err := core.StorageV1().StorageClasses().Create(context.Background(), &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: provider.config.StorageClass},
		Provisioner: "csi.example.invalid",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshots := snapshotfake.NewSimpleClientset(&snapshotv1.VolumeSnapshotClass{
		ObjectMeta:     metav1.ObjectMeta{Name: "matching"},
		Driver:         "csi.example.invalid",
		DeletionPolicy: snapshotv1.VolumeSnapshotContentDelete,
	})
	discovery := core.Discovery().(*fakediscovery.FakeDiscovery)
	discovery.Resources = append(discovery.Resources, &metav1.APIResourceList{
		GroupVersion: "snapshot.storage.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "volumesnapshots", Namespaced: true}},
	})
	provider.snapshots = snapshots
	support := provider.SnapshotSupport(context.Background())
	if !support.VolumeSnapshotAPI || !support.StorageClass || !support.VolumeSnapshotClass {
		t.Fatalf("CSI prerequisites not discovered: %#v", support)
	}
	if support.ApplicationCompatible {
		t.Fatal("CSI prerequisites incorrectly made Moment v1 compatible")
	}
}

func TestSandboxBlueprintUsesControllerPVCAndLoopbackHealthcheck(t *testing.T) {
	provider, _, _ := testProvider(t)
	sandbox := provider.sandbox("capsule-blueprint", provider.resourceName("capsule-blueprint"), provider.config.Image)
	spec := sandbox.Spec.PodTemplate.Spec
	if sandbox.Spec.Service == nil || *sandbox.Spec.Service {
		t.Fatal("Sandbox unexpectedly requests a Service")
	}
	if len(spec.Containers) != 1 {
		t.Fatalf("containers = %#v", spec.Containers)
	}
	container := spec.Containers[0]
	if container.ReadinessProbe == nil || container.ReadinessProbe.Exec == nil {
		t.Fatalf("readiness probe is not loopback exec: %#v", container.ReadinessProbe)
	}
	wantProbe := []string{"/usr/local/bin/capsuled", "healthcheck"}
	if strings.Join(container.ReadinessProbe.Exec.Command, "\x00") != strings.Join(wantProbe, "\x00") {
		t.Fatalf("readiness command = %#v", container.ReadinessProbe.Exec.Command)
	}
	for _, volume := range spec.Volumes {
		if volume.Name == "workspace" {
			t.Fatal("Pod template duplicates the workspace volume injected by Agent Sandbox")
		}
	}
	if len(sandbox.Spec.VolumeClaimTemplates) != 1 ||
		sandbox.Spec.VolumeClaimTemplates[0].Name != "workspace" {
		t.Fatalf("workspace claim templates = %#v", sandbox.Spec.VolumeClaimTemplates)
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("Capsule Pod receives a Kubernetes service-account token")
	}
}

func TestRuntimeTransportClosesOnSuccessAndProtocolFailure(t *testing.T) {
	provider, _, _ := testProvider(t)
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-runtime"})
	if err != nil {
		t.Fatal(err)
	}
	forwarder := provider.forwarder.(*recordingForwarder)
	if len(forwarder.sessions) != 1 || !forwarder.sessions[0].closed {
		t.Fatalf("Create transport was not closed: %#v", forwarder.sessions)
	}
	if result, err := provider.GitStatus(context.Background(), resource.ID); err != nil || result.Content != "clean" {
		t.Fatalf("GitStatus = %#v, %v", result, err)
	}
	if len(forwarder.sessions) != 2 || !forwarder.sessions[1].closed {
		t.Fatalf("successful runtime transport was not closed: %#v", forwarder.sessions)
	}
	if _, err := provider.GitDiff(context.Background(), resource.ID); err == nil {
		t.Fatal("malformed protocol response was accepted")
	}
	if len(forwarder.sessions) != 3 || !forwarder.sessions[2].closed {
		t.Fatalf("failed runtime transport was not closed: %#v", forwarder.sessions)
	}
}

func TestStructuredRuntimeTransportPreservesTypedFrames(t *testing.T) {
	provider, _, _ := testProvider(t)
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-structured"})
	if err != nil {
		t.Fatal(err)
	}
	frame := adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStart,
		ID: "start-1", SessionID: "session-1",
	}
	run, err := provider.StartStructured(context.Background(), ports.RuntimeStructuredStartRequest{
		ResourceID: resource.ID, RunID: "structured-run", Harness: "structured", Frame: frame,
	})
	if err != nil || !run.Structured || run.PTY {
		t.Fatalf("structured start = %#v, %v", run, err)
	}
	if err := provider.SendStructured(context.Background(), ports.RuntimeStructuredSendRequest{
		ResourceID: resource.ID, RunID: "structured-run", Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: "message-1", SessionID: "session-1",
			Role: adapterproto.RoleUser, Content: "content",
		},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := provider.StructuredEvents(context.Background(), resource.ID, "structured-run", 0)
	if err != nil || len(events.Items) != 1 ||
		events.Items[0].Frame.Status != adapterproto.StatusRunning {
		t.Fatalf("structured events = %#v, %v", events, err)
	}
	for _, session := range provider.forwarder.(*recordingForwarder).sessions {
		if !session.closed {
			t.Fatal("structured port-forward session leaked")
		}
	}
}

func TestConfigValidation(t *testing.T) {
	for _, config := range []Config{
		{InCluster: true, Kubeconfig: "config"},
		{TTL: time.Second},
		{TTL: 10 * time.Minute, OperationTimeout: time.Minute, SetupTimeout: 9 * time.Minute},
		{MemoryLimit: "invalid"},
		{Template: "template"},
	} {
		if _, err := config.withDefaults(); err == nil {
			t.Fatalf("configuration accepted: %#v", config)
		}
	}
}
