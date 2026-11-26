package agentsandbox

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

// ADR 0065: an in-cluster control plane reaches capsuled on the pod IP over
// mutual TLS. Port-forward remains for out-of-cluster daemons and for
// Sandboxes created before the pod transport existed.
const (
	podTLSPort          = 7778
	podTLSMountDir      = "/var/run/meridian/tls"
	podTLSVolume        = "pod-tls"
	podTLSRole          = "tls"
	annotationTransport = "meridian.orloj.io/transport"

	transportPodMTLS     = "pod-mtls"
	transportPortForward = "port-forward"
)

// newSandboxTransport is the transport a Sandbox created now records.
func (p *Provider) newSandboxTransport() string {
	if p.config.InCluster {
		return transportPodMTLS
	}
	return transportPortForward
}

// sandboxTransport reads the transport a Sandbox was created or upgraded
// with. A Sandbox from before ADR 0065 has none and uses port-forward.
func sandboxTransport(sandbox *sandboxv1beta1.Sandbox) (string, error) {
	switch value := sandbox.Annotations[annotationTransport]; value {
	case "", transportPortForward:
		return transportPortForward, nil
	case transportPodMTLS:
		return transportPodMTLS, nil
	default:
		return "", fmt.Errorf("%w: Sandbox transport annotation is invalid", domain.ErrCorrupt)
	}
}

func podTLSSecretName(sandboxName string) string { return sandboxName + "-tls" }

// applyPodMTLS gives a Pod template capsuled's pod-network listener: its
// flags, port, and a read-only projection of the TLS Secret that leaves the
// client key out. It is idempotent, so an upgrade can apply it again.
func applyPodMTLS(spec *corev1.PodSpec, sandboxName string) {
	defaultMode := int32(0o400)
	if !slices.ContainsFunc(spec.Volumes, func(volume corev1.Volume) bool { return volume.Name == podTLSVolume }) {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: podTLSVolume, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: podTLSSecretName(sandboxName), DefaultMode: &defaultMode,
				Items: []corev1.KeyToPath{
					{Key: capsuleproto.PodTLSServerCert, Path: capsuleproto.PodTLSServerCert},
					{Key: capsuleproto.PodTLSServerKey, Path: capsuleproto.PodTLSServerKey},
					{Key: capsuleproto.PodTLSClientCert, Path: capsuleproto.PodTLSClientCert},
				},
			},
		}})
	}
	container := &spec.Containers[0]
	if !slices.ContainsFunc(container.VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == podTLSVolume }) {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name: podTLSVolume, MountPath: podTLSMountDir, ReadOnly: true,
		})
	}
	if !slices.ContainsFunc(container.Ports, func(port corev1.ContainerPort) bool { return port.ContainerPort == podTLSPort }) {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name: "capsuled-tls", ContainerPort: podTLSPort, Protocol: corev1.ProtocolTCP,
		})
	}
	if !slices.ContainsFunc(container.Args, func(arg string) bool { return strings.HasPrefix(arg, "--tls-listen=") }) {
		container.Args = append(container.Args,
			"--tls-listen=:"+strconv.Itoa(podTLSPort),
			"--tls-cert-file="+podTLSMountDir+"/"+capsuleproto.PodTLSServerCert,
			"--tls-key-file="+podTLSMountDir+"/"+capsuleproto.PodTLSServerKey,
			"--tls-client-cert-file="+podTLSMountDir+"/"+capsuleproto.PodTLSClientCert,
		)
	}
}

// ensurePodTLSSecret returns the Sandbox's TLS Secret, creating it with fresh
// material when it does not exist. The certificates expire an hour after the
// Sandbox's shutdown time, so none outlives the Capsule it was made for.
func (p *Provider) ensurePodTLSSecret(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
) (*corev1.Secret, error) {
	existing, err := p.podTLSSecret(ctx, sandbox, capsuleID)
	if err == nil || !apierrors.IsNotFound(err) {
		return existing, err
	}
	notAfter := time.Now().UTC().Add(p.config.TTL)
	if sandbox.Spec.Lifecycle.ShutdownTime != nil {
		notAfter = sandbox.Spec.Lifecycle.ShutdownTime.Time
	}
	material, err := capsuleproto.GeneratePodTLS(
		capsuleproto.PodTLSServerName(sandbox.Name), notAfter.Add(time.Hour),
	)
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: podTLSSecretName(sandbox.Name), Namespace: p.config.Namespace,
			Labels:      p.labels(capsuleID, podTLSRole),
			Annotations: map[string]string{annotationID: string(capsuleID)},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				sandbox, schema.GroupVersionKind{Group: sandboxv1beta1.GroupVersion.Group,
					Version: sandboxv1beta1.GroupVersion.Version, Kind: "Sandbox"},
			)},
		},
		Immutable: pointer(true), Type: corev1.SecretTypeOpaque, Data: material,
	}
	created, err := p.core.CoreV1().Secrets(p.config.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return p.podTLSSecret(ctx, sandbox, capsuleID)
	}
	if err != nil {
		return nil, mapKubernetesError("create Capsule TLS Secret", err)
	}
	return created, nil
}

// podTLSSecret reads and verifies the Sandbox's TLS Secret. A missing Secret
// is returned as the Kubernetes NotFound error so callers can create it.
func (p *Provider) podTLSSecret(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
) (*corev1.Secret, error) {
	secret, err := p.core.CoreV1().Secrets(p.config.Namespace).Get(
		ctx, podTLSSecretName(sandbox.Name), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, err
	}
	if err != nil {
		return nil, mapKubernetesError("get Capsule TLS Secret", err)
	}
	if err := p.verifyOwnedMeta(secret.ObjectMeta, capsuleID, podTLSRole); err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(secret, sandbox) {
		return nil, fmt.Errorf("%w: Capsule TLS Secret owner does not match Sandbox", domain.ErrConflict)
	}
	if err := capsuleproto.ValidatePodTLS(secret.Data, capsuleproto.PodTLSServerName(sandbox.Name)); err != nil {
		return nil, fmt.Errorf("%w: Capsule TLS Secret is invalid", domain.ErrCorrupt)
	}
	return secret, nil
}

// podMTLSClient connects to capsuled on the pod IP, pinned to this Capsule's
// server certificate (ADR 0065). The pin, not the IP, identifies the Capsule:
// a reused pod IP fails the handshake instead of receiving the token.
func (p *Provider) podMTLSClient(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
	pod *corev1.Pod,
	token string,
) (*capsuleproto.Client, ForwardSession, error) {
	secret, err := p.podTLSSecret(ctx, sandbox, capsuleID)
	if apierrors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("%w: Capsule TLS Secret is missing", domain.ErrCorrupt)
	}
	if err != nil {
		return nil, nil, err
	}
	config, err := capsuleproto.ClientTLSConfig(secret.Data, capsuleproto.PodTLSServerName(sandbox.Name))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: Capsule TLS Secret is invalid", domain.ErrCorrupt)
	}
	address, err := p.podAddress(pod)
	if err != nil {
		return nil, nil, err
	}
	client, err := capsuleproto.NewTLSClient("https://"+address, token, config)
	if err != nil {
		return nil, nil, err
	}
	return client, &podSession{url: "https://" + address, client: client}, nil
}

// defaultPodAddress is the pod IP and capsuled's pod-network port.
func defaultPodAddress(pod *corev1.Pod) (string, error) {
	ip := net.ParseIP(pod.Status.PodIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", fmt.Errorf("%w: Sandbox pod has no usable IP", domain.ErrIllegalTransition)
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(podTLSPort)), nil
}

// podSession is the per-operation session for the pod transport. It owns no
// tunnel; closing it releases the operation's pooled connections.
type podSession struct {
	url    string
	client *capsuleproto.Client
}

func (s *podSession) URL() string { return s.url }

func (s *podSession) Close() error {
	s.client.CloseIdleConnections()
	return nil
}
