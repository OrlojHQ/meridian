package agentsandbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func inClusterProvider(t *testing.T) (*Provider, *[]string, func() int) {
	t.Helper()
	provider, _, core := testProviderWith(t, func(config *Config) { config.InCluster = true })
	dialed := usePodTLSRuntime(t, provider, core)
	forwarder := provider.forwarder.(*recordingForwarder)
	return provider, dialed, func() int { return len(forwarder.sessions) }
}

func TestInClusterCapsuleUsesPodMTLSEndToEnd(t *testing.T) {
	provider, dialed, forwards := inClusterProvider(t)
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-pod"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := provider.GitStatus(ctx, resource.ID); err != nil || result.Content != "clean" {
		t.Fatalf("GitStatus over pod mTLS = %#v, %v", result, err)
	}
	if forwards() != 0 {
		t.Fatalf("in-cluster Capsule opened %d port-forwards", forwards())
	}
	if len(*dialed) != 2 || (*dialed)[0] != "10.42.0.7:7778" {
		t.Fatalf("pod transport dialed %v, want the pod IP on 7778 for prepare and GitStatus", *dialed)
	}

	sandbox, err := provider.sandboxes.Get(ctx, resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Annotations[annotationTransport] != transportPodMTLS {
		t.Fatalf("transport annotation = %q", sandbox.Annotations[annotationTransport])
	}
	spec := sandbox.Spec.PodTemplate.Spec
	container := spec.Containers[0]
	for _, want := range []string{
		"--listen=127.0.0.1:7777", "--tls-listen=:7778",
		"--tls-cert-file=/var/run/meridian/tls/tls.crt",
		"--tls-key-file=/var/run/meridian/tls/tls.key",
		"--tls-client-cert-file=/var/run/meridian/tls/client.crt",
	} {
		if !slices.Contains(container.Args, want) {
			t.Errorf("capsuled args %v lack %q", container.Args, want)
		}
	}
	if !slices.ContainsFunc(container.Ports, func(port corev1.ContainerPort) bool { return port.ContainerPort == podTLSPort }) {
		t.Errorf("container ports %v lack %d", container.Ports, podTLSPort)
	}
	index := slices.IndexFunc(spec.Volumes, func(volume corev1.Volume) bool { return volume.Name == podTLSVolume })
	if index < 0 || spec.Volumes[index].Secret == nil {
		t.Fatalf("pod TLS volume missing: %#v", spec.Volumes)
	}
	projected := spec.Volumes[index].Secret
	var keys []string
	for _, item := range projected.Items {
		keys = append(keys, item.Key)
	}
	slices.Sort(keys)
	if strings.Join(keys, ",") != "client.crt,tls.crt,tls.key" || *projected.DefaultMode != 0o400 {
		t.Fatalf("pod TLS projection = %v mode %o; the client key must stay out of the pod", keys, *projected.DefaultMode)
	}
	if !slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool {
		return mount.Name == podTLSVolume && mount.ReadOnly && mount.MountPath == podTLSMountDir
	}) {
		t.Fatalf("pod TLS mount = %#v", container.VolumeMounts)
	}

	secrets := provider.core.CoreV1().Secrets(provider.config.Namespace)
	secret, err := secrets.Get(ctx, podTLSSecretName(resource.ID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !*secret.Immutable || secret.Labels[labelRole] != podTLSRole || !metav1.IsControlledBy(secret, sandbox) {
		t.Fatalf("unsafe TLS Secret: %#v", secret.ObjectMeta)
	}
	block, _ := pem.Decode(secret.Data[capsuleproto.PodTLSServerCert])
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := sandbox.Spec.Lifecycle.ShutdownTime.Add(time.Hour)
	if leaf.NotAfter.Sub(wantExpiry).Abs() > time.Second {
		t.Fatalf("server certificate expires %s, want %s", leaf.NotAfter, wantExpiry)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != resource.ID+capsuleproto.PodTLSServerNameSuffix {
		t.Fatalf("server certificate names %v", leaf.DNSNames)
	}

	if err := provider.Delete(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(ctx, podTLSSecretName(resource.ID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("TLS Secret survived deletion: %v", err)
	}
}

// A pod IP reused by another workload, even another Capsule, does not match
// the pinned server certificate, so the token is never sent there.
func TestPodMTLSRefusesAnotherListenerAtThePodIP(t *testing.T) {
	provider, _, _ := inClusterProvider(t)
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-pinned"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := capsuleproto.GeneratePodTLS(capsuleproto.PodTLSServerName(resource.ID), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	config, err := capsuleproto.ServerTLSConfig(
		other[capsuleproto.PodTLSServerCert], other[capsuleproto.PodTLSServerKey], other[capsuleproto.PodTLSClientCert],
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	impostor := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	impostor.TLS = config
	impostor.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	impostor.StartTLS()
	defer impostor.Close()
	provider.podAddress = func(*corev1.Pod) (string, error) {
		return strings.TrimPrefix(impostor.URL, "https://"), nil
	}
	if _, err := provider.GitStatus(ctx, resource.ID); err == nil {
		t.Fatal("GitStatus succeeded against another listener's certificate")
	}
	if reached {
		t.Fatal("a request reached a listener that is not this Capsule")
	}
}

func TestPodMTLSFailsClosedWithoutItsSecret(t *testing.T) {
	provider, _, _ := inClusterProvider(t)
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-no-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.core.CoreV1().Secrets(provider.config.Namespace).Delete(
		ctx, podTLSSecretName(resource.ID), metav1.DeleteOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.GitStatus(ctx, resource.ID); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("GitStatus without the TLS Secret = %v, want corrupt", err)
	}
}

func TestUnknownTransportAnnotationIsRefused(t *testing.T) {
	provider, agents, _ := testProvider(t)
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-odd"})
	if err != nil {
		t.Fatal(err)
	}
	client := agents.AgentsV1beta1().Sandboxes(provider.config.Namespace)
	sandbox, err := client.Get(ctx, resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Annotations[annotationTransport] = "pod-plaintext"
	if _, err := client.Update(ctx, sandbox, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.GitStatus(ctx, resource.ID); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("GitStatus with an unknown transport = %v", err)
	}
	if _, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-odd"}); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("adopting a Sandbox with an unknown transport = %v", err)
	}
}

func TestOutOfClusterDaemonKeepsPortForwardAndRefusesMicroVM(t *testing.T) {
	provider, _, _ := testProvider(t)
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-forward"})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := provider.sandboxes.Get(context.Background(), resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Annotations[annotationTransport] != transportPortForward ||
		slices.ContainsFunc(sandbox.Spec.PodTemplate.Spec.Containers[0].Args, func(arg string) bool {
			return strings.HasPrefix(arg, "--tls-")
		}) {
		t.Fatalf("out-of-cluster Sandbox = %v %v", sandbox.Annotations, sandbox.Spec.PodTemplate.Spec.Containers[0].Args)
	}
	if _, err := provider.core.CoreV1().Secrets(provider.config.Namespace).Get(
		context.Background(), podTLSSecretName(resource.ID), metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("out-of-cluster Capsule got a TLS Secret: %v", err)
	}

	microvm, _, _ := isolationProvider(t, Config{
		IsolationClass: domain.IsolationMicroVM, RuntimeClass: testRuntimeClass,
	}, "kata-qemu")
	_, err = microvm.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-kata"})
	if !errors.Is(err, domain.ErrUnsupported) || !strings.Contains(err.Error(), "ADR 0065") {
		t.Fatalf("out-of-cluster microvm create = %v", err)
	}
	if len(ownedSandboxes(t, microvm)) != 0 {
		t.Fatal("refused microvm create still produced a Sandbox")
	}
}

func TestInClusterCapabilitiesDoNotNeedAForwarder(t *testing.T) {
	provider, _, _ := testProviderWith(t, func(config *Config) { config.InCluster = true })
	provider.forwarder = nil
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.Run || !capabilities.Attach || !capabilities.Snapshot || !capabilities.Preview {
		t.Fatalf("in-cluster capabilities without port-forward = %#v", capabilities)
	}
}

// A Sandbox from before ADR 0065 moves to the pod transport on the image
// upgrade, with its TLS Secret created before the replacement Pod starts.
func TestUpgradeMovesPortForwardSandboxToPodMTLS(t *testing.T) {
	provider, dialed, forwards := inClusterProvider(t)
	ctx := context.Background()
	provider.config.InCluster = false
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: "capsule-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	client := provider.sandboxes
	sandbox, err := client.Get(ctx, resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	delete(sandbox.Annotations, annotationTransport) // as written before ADR 0065
	if _, err := client.Update(ctx, sandbox, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.GitStatus(ctx, resource.ID); err != nil || forwards() != 2 {
		t.Fatalf("legacy Sandbox GitStatus = %v with %d port-forwards", err, forwards())
	}

	provider.config.InCluster = true
	upgraded := "example.invalid/meridian-capsule@sha256:" + strings.Repeat("c", 64)
	if _, err := provider.UpgradeImage(ctx, ports.UpgradeCapsuleRequest{
		CapsuleID: "capsule-legacy", ResourceID: resource.ID, ImageReference: upgraded,
	}); err != nil {
		t.Fatalf("UpgradeImage: %v", err)
	}
	sandbox, err = client.Get(ctx, resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Annotations[annotationTransport] != transportPodMTLS ||
		!slices.Contains(sandbox.Spec.PodTemplate.Spec.Containers[0].Args, "--tls-listen=:7778") {
		t.Fatalf("upgraded Sandbox = %v %v", sandbox.Annotations, sandbox.Spec.PodTemplate.Spec.Containers[0].Args)
	}
	before := len(*dialed)
	if _, err := provider.GitStatus(ctx, resource.ID); err != nil {
		t.Fatalf("GitStatus after migration: %v", err)
	}
	if len(*dialed) != before+1 || forwards() != 2 {
		t.Fatalf("migrated Capsule used port-forward (%d) or skipped the pod IP (%v)", forwards(), *dialed)
	}

	// A second upgrade is idempotent: no duplicate flags, ports, or mounts.
	if _, err := provider.UpgradeImage(ctx, ports.UpgradeCapsuleRequest{
		CapsuleID: "capsule-legacy", ResourceID: resource.ID,
		ImageReference: "example.invalid/meridian-capsule@sha256:" + strings.Repeat("d", 64),
	}); err != nil {
		t.Fatal(err)
	}
	sandbox, err = client.Get(ctx, resource.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	container := sandbox.Spec.PodTemplate.Spec.Containers[0]
	count := 0
	for _, arg := range container.Args {
		if strings.HasPrefix(arg, "--tls-listen=") {
			count++
		}
	}
	if count != 1 || len(container.Ports) != 2 {
		t.Fatalf("repeated migration duplicated settings: %v %v", container.Args, container.Ports)
	}
}
