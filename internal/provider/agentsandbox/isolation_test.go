package agentsandbox

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
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

const testRuntimeClass = "meridian-test-rc"

func seedRuntimeClass(t *testing.T, core *kubernetesfake.Clientset, handler string) {
	t.Helper()
	if handler == "" {
		return
	}
	class := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: testRuntimeClass, UID: types.UID("rc-uid-1")},
		Handler:    handler,
	}
	if _, err := core.NodeV1().RuntimeClasses().Create(
		context.Background(), class, metav1.CreateOptions{},
	); err != nil {
		t.Fatal(err)
	}
}

// isolationProvider builds a provider with an isolation declaration and a
// fake cluster holding the named RuntimeClass.
func isolationProvider(
	t *testing.T,
	config Config,
	handler string,
) (*Provider, *sandboxfake.Clientset, *kubernetesfake.Clientset) {
	t.Helper()
	core := kubernetesfake.NewSimpleClientset()
	seedRuntimeClass(t, core, handler)
	server := runtimeServer(t)
	t.Cleanup(server.Close)
	provider, agents, core := isolationProviderWithCore(t, config, core, server)
	return provider, agents, core
}

func isolationProviderWithCore(
	t *testing.T,
	config Config,
	core *kubernetesfake.Clientset,
	server *httptest.Server,
) (*Provider, *sandboxfake.Clientset, *kubernetesfake.Clientset) {
	t.Helper()
	agents := sandboxfake.NewSimpleClientset()
	namespace := "test"
	config.Namespace = namespace
	if config.OperationTimeout == 0 {
		config.OperationTimeout = 2 * time.Second
	}
	discovery := core.Discovery().(*fakediscovery.FakeDiscovery)
	discovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: sandboxv1beta1.GroupVersion.String(),
			APIResources: []metav1.APIResource{{Name: "sandboxes", Namespaced: true, Kind: "Sandbox"}},
		},
	}
	// Mirror the shared testProvider reactors so Create/Update keep the
	// Sandbox lifecycle observable.
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
				Labels: sandbox.Spec.PodTemplate.ObjectMeta.Labels,
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
		if _, err := core.CoreV1().Pods(namespace).Create(
			context.Background(), pod, metav1.CreateOptions{},
		); err != nil {
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
	provider, err := NewWithClients(
		config, nil, agents.AgentsV1beta1().Sandboxes(namespace),
		core, discovery, nil, &recordingForwarder{url: server.URL},
	)
	if err != nil {
		t.Fatal(err)
	}
	return provider, agents, core
}

func TestConfigIsolationValidation(t *testing.T) {
	valid := Config{TTL: 24 * time.Hour}
	for _, config := range []Config{
		// sandbox and microvm require a RuntimeClass to verify.
		func() Config { c := valid; c.IsolationClass = domain.IsolationSandbox; return c }(),
		func() Config { c := valid; c.IsolationClass = domain.IsolationMicroVM; return c }(),
		// Unknown classes are refused.
		func() Config { c := valid; c.IsolationClass = "vm"; return c }(),
		// Handlers need a stronger class than container.
		func() Config {
			c := valid
			c.IsolationClass = domain.IsolationContainer
			c.IsolationHandlers = []string{"custom-handler"}
			return c
		}(),
		// Handlers without any class are refused.
		func() Config { c := valid; c.IsolationHandlers = []string{"custom-handler"}; return c }(),
		// Never-isolating handlers are refused as extensions.
		func() Config {
			c := valid
			c.IsolationClass = domain.IsolationSandbox
			c.RuntimeClass = testRuntimeClass
			c.IsolationHandlers = []string{"runc"}
			return c
		}(),
		// A handler from another class's built-in set is refused.
		func() Config {
			c := valid
			c.IsolationClass = domain.IsolationMicroVM
			c.RuntimeClass = testRuntimeClass
			c.IsolationHandlers = []string{"runsc"}
			return c
		}(),
		// Not DNS-1123.
		func() Config {
			c := valid
			c.IsolationClass = domain.IsolationSandbox
			c.RuntimeClass = testRuntimeClass
			c.IsolationHandlers = []string{"Bad_Handler"}
			return c
		}(),
	} {
		if _, err := config.withDefaults(); err == nil {
			t.Fatalf("configuration accepted: %#v", config)
		}
	}
	// Empty input stays valid and handlers deduplicate and sort.
	resolved, err := Config{}.withDefaults()
	if err != nil || resolved.IsolationClass != "" || len(resolved.IsolationHandlers) != 0 {
		t.Fatalf("empty isolation configuration = %#v, %v", resolved, err)
	}
	resolved, err = Config{
		IsolationClass:    domain.IsolationMicroVM,
		RuntimeClass:      testRuntimeClass,
		IsolationHandlers: []string{"z-handler", "a-handler", "z-handler"},
	}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.IsolationHandlers) != 2 ||
		resolved.IsolationHandlers[0] != "a-handler" || resolved.IsolationHandlers[1] != "z-handler" {
		t.Fatalf("handler normalization = %#v", resolved.IsolationHandlers)
	}
}

func TestCapabilitiesIsolationUndeclared(t *testing.T) {
	provider, _, core := isolationProvider(t, Config{}, "runc")
	// No RuntimeClass is read when nothing is declared: the fake tracker
	// would record the GET.
	if _, err := provider.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, action := range core.Actions() {
		if action.GetVerb() == "get" && action.GetResource().Resource == "runtimeclasses" {
			t.Fatalf("undeclared isolation class still read the RuntimeClass: %v", action)
		}
	}
}

func TestCapabilitiesIsolationVerified(t *testing.T) {
	cases := map[string]domain.IsolationClass{
		"runsc":                domain.IsolationSandbox,
		"gvisor":               domain.IsolationSandbox,
		"kata":                 domain.IsolationMicroVM,
		"kata-qemu":            domain.IsolationMicroVM,
		"kata-clh":             domain.IsolationMicroVM,
		"kata-dragonball":      domain.IsolationMicroVM,
		"kata-qemu-runtime-rs": domain.IsolationMicroVM,
		"kata-clh-runtime-rs":  domain.IsolationMicroVM,
	}
	for handler, want := range cases {
		config := Config{IsolationClass: want, RuntimeClass: testRuntimeClass}
		provider, _, _ := isolationProvider(t, config, handler)
		capabilities, err := provider.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("handler %s: %v", handler, err)
		}
		if capabilities.Isolation != want {
			t.Fatalf("handler %s reported %q, want %q", handler, capabilities.Isolation, want)
		}
	}
}

func TestCapabilitiesIsolationRejects(t *testing.T) {
	// A handler outside the built-in set and operator extensions fails.
	config := Config{IsolationClass: domain.IsolationMicroVM, RuntimeClass: testRuntimeClass}
	provider, _, _ := isolationProvider(t, config, "kata-fc")
	_, err := provider.Capabilities(context.Background())
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("kata-fc verification error = %v", err)
	}
	want := `uses handler "kata-fc", which is not a known microvm handler`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("verification error %q does not name the offending handler", err.Error())
	}

	// A missing RuntimeClass fails with the fixed NotFound sentence.
	provider, _, _ = isolationProvider(t, config, "")
	_, err = provider.Capabilities(context.Background())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing RuntimeClass error = %v", err)
	}
	if !strings.Contains(err.Error(), `does not exist`) {
		t.Fatalf("missing RuntimeClass error = %q", err.Error())
	}

	// A Forbidden API failure surfaces the fixed sentence and never the
	// Kubernetes error text.
	core := kubernetesfake.NewSimpleClientset()
	core.PrependReactor("get", "runtimeclasses", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			nodev1.Resource("runtimeclasses"), testRuntimeClass,
			errors.New("SECRET-FORBIDDEN-TEXT"),
		)
	})
	forbidden := Config{IsolationClass: domain.IsolationSandbox, RuntimeClass: testRuntimeClass}
	provider, _, _ = isolationProviderWithCore(t, forbidden, core, runtimeServer(t))
	_, err = provider.Capabilities(context.Background())
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("forbidden verification error = %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-FORBIDDEN-TEXT") {
		t.Fatalf("forbidden error leaked Kubernetes text: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "grant get on node.k8s.io runtimeclasses") {
		t.Fatalf("forbidden error lacks remediation: %q", err.Error())
	}
}

func TestIsolationHandlerExtension(t *testing.T) {
	// An operator extension accepts a custom handler for the declared class.
	config := Config{
		IsolationClass:    domain.IsolationMicroVM,
		RuntimeClass:      testRuntimeClass,
		IsolationHandlers: []string{"acme-vm"},
	}
	provider, _, _ := isolationProvider(t, config, "acme-vm")
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil || capabilities.Isolation != domain.IsolationMicroVM {
		t.Fatalf("extension capabilities = %#v, %v", capabilities, err)
	}

	// The extension is bound to the class it was configured for: a sandbox
	// declaration without the extension cannot use the acme-vm handler.
	sandboxConfig := Config{
		IsolationClass: domain.IsolationSandbox,
		RuntimeClass:   testRuntimeClass,
	}
	provider, _, _ = isolationProvider(t, sandboxConfig, "acme-vm")
	_, err = provider.Capabilities(context.Background())
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("cross-class extension error = %v", err)
	}

	// kata-fc stays refused even as an extension.
	firecracker := Config{
		IsolationClass:    domain.IsolationMicroVM,
		RuntimeClass:      testRuntimeClass,
		IsolationHandlers: []string{"kata-fc"},
	}
	if _, err := firecracker.withDefaults(); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("kata-fc extension accepted: %v", err)
	}

	// Extensions cannot exceed the bounded list size.
	many := Config{
		IsolationClass:    domain.IsolationMicroVM,
		RuntimeClass:      testRuntimeClass,
		IsolationHandlers: make([]string, 17),
	}
	for index := range many.IsolationHandlers {
		many.IsolationHandlers[index] = "handler-" + string(rune('a'+index))
	}
	if _, err := many.withDefaults(); err == nil {
		t.Fatal("oversized handler list accepted")
	}
}

func TestCreateRechecksRuntimeClass(t *testing.T) {
	config := Config{IsolationClass: domain.IsolationMicroVM, RuntimeClass: testRuntimeClass}
	provider, _, core := isolationProvider(t, config, "kata-qemu")
	if _, err := provider.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The cluster operator swaps the RuntimeClass handler to a container
	// runtime after verification.
	class, err := core.NodeV1().RuntimeClasses().Get(
		context.Background(), testRuntimeClass, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	class.Handler = "runc"
	class.UID = types.UID("rc-uid-2")
	if err := core.Tracker().Update(
		nodev1.SchemeGroupVersion.WithResource("runtimeclasses"), class, "",
	); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-swapped"})
	if err == nil || !strings.Contains(err.Error(), "changed after meridiand started; restart meridiand") {
		t.Fatalf("swapped RuntimeClass create error = %v", err)
	}
	if len(ownedSandboxes(t, provider)) != 0 {
		t.Fatal("swapped RuntimeClass still created a Sandbox")
	}

	// A handler-swap that keeps the accepted vocabulary still fails on the
	// UID change, so the cached claim cannot outlive the object.
	class.Handler = "kata-clh"
	if err := core.Tracker().Update(
		nodev1.SchemeGroupVersion.WithResource("runtimeclasses"), class, "",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{
		CapsuleID: "capsule-swapped-uid",
	}); err == nil || !strings.Contains(err.Error(), "changed after meridiand started") {
		t.Fatalf("re-created RuntimeClass create error = %v", err)
	}

	// Restoring the exact object lets creation proceed.
	class.Handler = "kata-qemu"
	class.UID = types.UID("rc-uid-1")
	if err := core.Tracker().Update(
		nodev1.SchemeGroupVersion.WithResource("runtimeclasses"), class, "",
	); err != nil {
		t.Fatal(err)
	}
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{
		CapsuleID: "capsule-restored",
	})
	if err != nil || resource.State != ports.ProviderReady {
		t.Fatalf("restored create = %#v, %v", resource, err)
	}
}

func TestCreateRefusesAdoptedSandboxWithOtherRuntimeClass(t *testing.T) {
	config := Config{IsolationClass: domain.IsolationSandbox, RuntimeClass: testRuntimeClass}
	provider, agents, _ := isolationProvider(t, config, "runsc")
	sandbox := provider.sandbox("capsule-adopt", provider.resourceName("capsule-adopt"), provider.config.Image)
	sandbox.UID = types.UID("adopt-uid")
	other := testRuntimeClass + "-other"
	sandbox.Spec.PodTemplate.Spec.RuntimeClassName = &other
	setReady(sandbox)
	if err := agents.Tracker().Create(
		sandboxv1beta1.GroupVersion.WithResource("sandboxes"), sandbox, provider.config.Namespace,
	); err != nil {
		t.Fatal(err)
	}
	_, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-adopt"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("adopted Sandbox with another runtime class = %v", err)
	}
	if !strings.Contains(err.Error(), "adopted Sandbox runtime class differs from the configured runtime class") {
		t.Fatalf("adoption error = %q", err.Error())
	}
}

func TestResumeRefusesSandboxWithOtherRuntimeClass(t *testing.T) {
	config := Config{IsolationClass: domain.IsolationMicroVM, RuntimeClass: testRuntimeClass}
	provider, _, core := isolationProvider(t, config, "kata-qemu")
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{
		CapsuleID: "capsule-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Pause(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	// Swap the handler while the Capsule is paused.
	class, err := core.NodeV1().RuntimeClasses().Get(
		context.Background(), testRuntimeClass, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	class.Handler = "runc"
	if err := core.Tracker().Update(
		nodev1.SchemeGroupVersion.WithResource("runtimeclasses"), class, "",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Resume(context.Background(), resource.ID); err == nil ||
		!strings.Contains(err.Error(), "changed after meridiand started; restart meridiand") {
		t.Fatalf("resume on swapped RuntimeClass = %v", err)
	}
}

func TestPauseAllowsSandboxWithOtherRuntimeClass(t *testing.T) {
	config := Config{IsolationClass: domain.IsolationMicroVM, RuntimeClass: testRuntimeClass}
	provider, _, core := isolationProvider(t, config, "kata-qemu")
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{
		CapsuleID: "capsule-pause",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Swap the handler; pausing must still work so a paused Capsule survives
	// a RuntimeClass change.
	class, err := core.NodeV1().RuntimeClasses().Get(
		context.Background(), testRuntimeClass, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	class.Handler = "runc"
	if err := core.Tracker().Update(
		nodev1.SchemeGroupVersion.WithResource("runtimeclasses"), class, "",
	); err != nil {
		t.Fatal(err)
	}
	paused, err := provider.Pause(context.Background(), resource.ID)
	if err != nil || paused.State != ports.ProviderPaused {
		t.Fatalf("pause on swapped RuntimeClass = %#v, %v", paused, err)
	}
}

// TestBuiltinHandlerSets pins the ADR 0050 vocabulary: the microvm set carries
// only virtio-fs-capable hypervisor-backed handlers from kata-deploy 4.3.1,
// kata-fc is absent, and no handler appears in two sets.
func TestBuiltinHandlerSets(t *testing.T) {
	wantMicroVM := []string{
		"kata", "kata-qemu", "kata-clh", "kata-dragonball",
		"kata-qemu-runtime-rs", "kata-clh-runtime-rs",
	}
	if len(builtinHandlers[domain.IsolationMicroVM]) != len(wantMicroVM) {
		t.Fatalf("microvm handlers = %#v", builtinHandlers[domain.IsolationMicroVM])
	}
	for _, handler := range wantMicroVM {
		if !builtinHandlers[domain.IsolationMicroVM][handler] {
			t.Fatalf("microvm handler %q missing", handler)
		}
	}
	if builtinHandlers[domain.IsolationMicroVM]["kata-fc"] {
		t.Fatal("kata-fc must not verify as microvm (devmapper/block storage profile)")
	}
	wantSandbox := []string{"runsc", "gvisor"}
	if len(builtinHandlers[domain.IsolationSandbox]) != len(wantSandbox) {
		t.Fatalf("sandbox handlers = %#v", builtinHandlers[domain.IsolationSandbox])
	}
	seen := map[string]bool{}
	for class, handlers := range builtinHandlers {
		for handler := range handlers {
			if seen[handler] {
				t.Fatalf("handler %q appears in more than one class set (duplicate in %s)", handler, class)
			}
			seen[handler] = true
		}
	}
	for handler := range neverIsolatingHandlers {
		if seen[handler] {
			t.Fatalf("never-isolating handler %q also belongs to a class set", handler)
		}
	}
	if !neverIsolatingHandlers["runc"] || !neverIsolatingHandlers["crun"] || !neverIsolatingHandlers["youki"] {
		t.Fatal("never-isolating set is incomplete")
	}
}

func ownedSandboxes(t *testing.T, provider *Provider) []string {
	t.Helper()
	sandboxes, err := provider.sandboxes.List(
		context.Background(), metav1.ListOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(sandboxes.Items))
	for _, sandbox := range sandboxes.Items {
		names = append(names, sandbox.Name)
	}
	return names
}
