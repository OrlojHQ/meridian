// Package agentsandbox implements Meridian Capsules with the Kubernetes Agent
// Sandbox v1beta1 API.
package agentsandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	snapshotclient "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	sandboxclient "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
	sandboxtyped "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/typed/api/v1beta1"
)

const (
	protocolPort = 7777
	workspace    = "/workspace"
	tokenPath    = "/var/run/meridian/token/token"

	labelOwned       = "app.kubernetes.io/managed-by"
	labelCapsuleHash = "meridian.orloj.io/capsule-hash"
	labelRole        = "meridian.orloj.io/role"
	annotationID     = "meridian.orloj.io/capsule-id"
	annotationImage  = "meridian.orloj.io/image-reference"
	annotationClass  = "meridian.orloj.io/class"
)

type Config struct {
	Kubeconfig       string
	Context          string
	InCluster        bool
	Namespace        string
	NamePrefix       string
	Class            string
	Template         string
	Image            string
	RuntimeClass     string
	StorageClass     string
	VolumeSize       string
	TTL              time.Duration
	MemoryLimit      string
	CPULimit         string
	EphemeralLimit   string
	OperationTimeout time.Duration
	SetupTimeout     time.Duration
}

func (c Config) withDefaults() (Config, error) {
	if c.Namespace == "" {
		c.Namespace = "meridian"
	}
	if c.NamePrefix == "" {
		c.NamePrefix = "meridian"
	}
	if c.Image == "" {
		c.Image = "meridian-capsule:dev"
	}
	if c.VolumeSize == "" {
		c.VolumeSize = "10Gi"
	}
	if c.MemoryLimit == "" {
		c.MemoryLimit = "2Gi"
	}
	if c.CPULimit == "" {
		c.CPULimit = "2"
	}
	if c.EphemeralLimit == "" {
		c.EphemeralLimit = "2Gi"
	}
	if c.TTL == 0 {
		c.TTL = 24 * time.Hour
	}
	if c.OperationTimeout == 0 {
		c.OperationTimeout = 2 * time.Minute
	}
	if c.SetupTimeout == 0 {
		c.SetupTimeout = 10 * time.Minute
	}
	for name, value := range map[string]string{
		"namespace": c.Namespace, "name prefix": c.NamePrefix, "class": c.Class,
		"template": c.Template, "image": c.Image, "runtime class": c.RuntimeClass,
		"storage class": c.StorageClass,
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return Config{}, fmt.Errorf("%w: Agent Sandbox %s is invalid", domain.ErrInvalid, name)
		}
	}
	if c.InCluster && c.Kubeconfig != "" {
		return Config{}, fmt.Errorf("%w: in-cluster and kubeconfig are mutually exclusive", domain.ErrInvalid)
	}
	if c.Context != "" && c.InCluster {
		return Config{}, fmt.Errorf("%w: context cannot be used with in-cluster configuration", domain.ErrInvalid)
	}
	if c.Template != "" {
		return Config{}, fmt.Errorf(
			"%w: agents.x-k8s.io/v1beta1 Sandbox has no templateRef; direct SandboxTemplate adoption is unsupported",
			domain.ErrUnsupported,
		)
	}
	if len(c.NamePrefix) > 46 || len(validation.IsDNS1123Label(c.NamePrefix)) > 0 ||
		len(validation.IsDNS1123Label(c.Namespace)) > 0 {
		return Config{}, fmt.Errorf("%w: namespace or name prefix is not DNS-1123", domain.ErrInvalid)
	}
	if c.RuntimeClass != "" && len(validation.IsDNS1123Subdomain(c.RuntimeClass)) > 0 {
		return Config{}, fmt.Errorf("%w: runtime class is not DNS-1123", domain.ErrInvalid)
	}
	if c.StorageClass != "" && len(validation.IsDNS1123Subdomain(c.StorageClass)) > 0 {
		return Config{}, fmt.Errorf("%w: storage class is not DNS-1123", domain.ErrInvalid)
	}
	if len(c.Class) > 256 || len(c.Image) > 1024 || len(c.Kubeconfig) > 4096 {
		return Config{}, fmt.Errorf("%w: Agent Sandbox configuration exceeds limits", domain.ErrInvalid)
	}
	if c.TTL < time.Minute || c.TTL > 30*24*time.Hour ||
		c.OperationTimeout <= 0 || c.SetupTimeout <= 0 {
		return Config{}, fmt.Errorf("%w: invalid Agent Sandbox duration", domain.ErrInvalid)
	}
	if c.OperationTimeout+c.SetupTimeout >= c.TTL {
		return Config{}, fmt.Errorf(
			"%w: Agent Sandbox TTL must exceed operation plus setup timeouts",
			domain.ErrInvalid,
		)
	}
	for name, value := range map[string]string{
		"volume size": c.VolumeSize, "memory limit": c.MemoryLimit,
		"CPU limit": c.CPULimit, "ephemeral storage limit": c.EphemeralLimit,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return Config{}, fmt.Errorf("%w: Agent Sandbox %s is invalid", domain.ErrInvalid, name)
		}
	}
	return c, nil
}

type Provider struct {
	config     Config
	restConfig *rest.Config
	sandboxes  sandboxtyped.SandboxInterface
	core       kubernetes.Interface
	discovery  discovery.DiscoveryInterface
	snapshots  snapshotclient.Interface
	forwarder  Forwarder
}

func New(config Config) (*Provider, error) {
	resolved, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	restConfig, err := buildRESTConfig(resolved)
	if err != nil {
		return nil, err
	}
	agentClient, err := sandboxclient.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Agent Sandbox client: %w", err)
	}
	coreClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	snapshotClient, err := snapshotclient.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create VolumeSnapshot client: %w", err)
	}
	return NewWithClients(
		resolved, restConfig, agentClient.AgentsV1beta1().Sandboxes(resolved.Namespace),
		coreClient, coreClient.Discovery(), snapshotClient, NewPortForwarder(restConfig),
	)
}

func NewWithClients(
	config Config,
	restConfig *rest.Config,
	sandboxes sandboxtyped.SandboxInterface,
	core kubernetes.Interface,
	discoveryClient discovery.DiscoveryInterface,
	snapshots snapshotclient.Interface,
	forwarder Forwarder,
) (*Provider, error) {
	resolved, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	if sandboxes == nil || core == nil {
		return nil, fmt.Errorf("%w: Kubernetes clients are required", domain.ErrInvalid)
	}
	return &Provider{
		config: resolved, restConfig: restConfig, sandboxes: sandboxes, core: core,
		discovery: discoveryClient, snapshots: snapshots, forwarder: forwarder,
	}, nil
}

func buildRESTConfig(config Config) (*rest.Config, error) {
	if config.InCluster {
		value, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("load in-cluster Kubernetes configuration: %w", err)
		}
		value.Timeout = config.OperationTimeout
		return value, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if config.Kubeconfig != "" {
		rules.ExplicitPath = config.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: config.Context}
	value, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	value.Timeout = config.OperationTimeout
	return value, nil
}

func (p *Provider) Close() error { return nil }

func (p *Provider) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if p.discovery != nil {
		resources, err := p.discovery.ServerResourcesForGroupVersion(
			sandboxv1beta1.GroupVersion.String(),
		)
		if err != nil {
			return ports.ProviderCapabilities{}, fmt.Errorf(
				"discover Agent Sandbox %s API: %w", sandboxv1beta1.GroupVersion, err,
			)
		}
		found := false
		for _, resource := range resources.APIResources {
			if resource.Name == "sandboxes" && resource.Kind == "Sandbox" && resource.Namespaced {
				found = true
				break
			}
		}
		if !found {
			return ports.ProviderCapabilities{}, fmt.Errorf(
				"%w: Agent Sandbox %s does not expose a namespaced Sandbox resource",
				domain.ErrUnsupported, sandboxv1beta1.GroupVersion,
			)
		}
	}
	// CSI snapshots intentionally remain unavailable to the application. Meridian
	// Moments currently require a portable archive and manifest; claiming CSI
	// while silently producing a tar archive would violate that contract.
	return ports.ProviderCapabilities{
		Version: "agentsandbox/v1beta1@v0.5.6",
		Pause:   true, Run: p.forwarder != nil, Git: p.forwarder != nil,
		Attach: p.forwarder != nil, Structured: p.forwarder != nil,
	}, nil
}

type SnapshotSupport struct {
	VolumeSnapshotAPI     bool
	StorageClass          bool
	VolumeSnapshotClass   bool
	ApplicationCompatible bool
	Reason                string
}

func (p *Provider) SnapshotSupport(ctx context.Context) SnapshotSupport {
	result := SnapshotSupport{
		ApplicationCompatible: false,
		Reason:                "Meridian Moment v1 requires portable archive metadata and cannot represent a provider-native CSI artifact",
	}
	if p.discovery == nil || p.snapshots == nil || p.config.StorageClass == "" {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	if _, err := p.discovery.ServerResourcesForGroupVersion("snapshot.storage.k8s.io/v1"); err == nil {
		result.VolumeSnapshotAPI = true
	}
	storageClass, err := p.core.StorageV1().StorageClasses().Get(
		ctx, p.config.StorageClass, metav1.GetOptions{},
	)
	if err == nil {
		result.StorageClass = true
	}
	if classes, err := p.snapshots.SnapshotV1().VolumeSnapshotClasses().List(
		ctx, metav1.ListOptions{},
	); err == nil && storageClass != nil {
		for _, class := range classes.Items {
			if class.Driver == storageClass.Provisioner {
				result.VolumeSnapshotClass = true
				break
			}
		}
	}
	return result
}

func (p *Provider) CaptureWorkspace(context.Context, string) (ports.WorkspaceCapture, error) {
	return ports.WorkspaceCapture{}, domain.ErrUnsupported
}

func (p *Provider) RestoreWorkspace(context.Context, string, string, int64, io.Reader) error {
	return domain.ErrUnsupported
}

func (p *Provider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	if request.CapsuleID == "" {
		return ports.ProviderResource{}, fmt.Errorf("%w: Capsule ID is required", domain.ErrInvalid)
	}
	name := p.resourceName(request.CapsuleID)
	image := request.ImageReference
	if image == "" {
		image = p.config.Image
	}
	if strings.TrimSpace(image) == "" || strings.ContainsAny(image, "\x00\r\n") {
		return ports.ProviderResource{}, fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	opctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	sandbox, err := p.sandboxes.Get(opctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sandbox, err = p.sandboxes.Create(opctx, p.sandbox(request.CapsuleID, name, image), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			sandbox, err = p.sandboxes.Get(opctx, name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return ports.ProviderResource{}, mapKubernetesError("create Sandbox", err)
	}
	if err := p.verifySandbox(sandbox, request.CapsuleID, image); err != nil {
		return ports.ProviderResource{}, err
	}
	token, err := p.ensureTokenSecret(opctx, sandbox, request.CapsuleID)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	ready, err := p.waitForState(opctx, name, ports.ProviderReady)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	setupContext, setupCancel := context.WithTimeout(ctx, p.config.SetupTimeout)
	defer setupCancel()
	client, session, err := p.runtimeClient(setupContext, name)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	defer session.Close()
	repositoryURL, setup := request.RepositoryURL, append([]string(nil), request.Setup...)
	if request.Restore {
		repositoryURL, setup = "", nil
	}
	_ = token // token is consumed by runtimeClient from the Secret after restart, too.
	_, err = client.Prepare(setupContext, capsuleproto.PrepareRequest{
		RepositoryURL: repositoryURL, Destination: workspace, Setup: setup,
	})
	if err != nil {
		return ports.ProviderResource{}, fmt.Errorf("prepare Capsule workspace: %w", mapRuntimeError(err))
	}
	ready.ImageDigest = p.imageIdentity(opctx, sandbox)
	return ready, nil
}

func (p *Provider) Get(ctx context.Context, id string) (ports.ProviderResource, error) {
	if !p.validResourceID(id) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	opctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	sandbox, err := p.sandboxes.Get(opctx, id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ports.ProviderResource{ID: id, State: ports.ProviderDeleted}, nil
	}
	if err != nil {
		return ports.ProviderResource{}, mapKubernetesError("get Sandbox", err)
	}
	if _, err := p.verifySandboxIdentity(sandbox, ""); err != nil {
		return ports.ProviderResource{}, err
	}
	return p.resource(opctx, sandbox)
}

func (p *Provider) Pause(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.setOperatingMode(ctx, id, sandboxv1beta1.SandboxOperatingModeSuspended)
}

func (p *Provider) Resume(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.setOperatingMode(ctx, id, sandboxv1beta1.SandboxOperatingModeRunning)
}

func (p *Provider) setOperatingMode(
	ctx context.Context,
	id string,
	mode sandboxv1beta1.SandboxOperatingMode,
) (ports.ProviderResource, error) {
	if !p.validResourceID(id) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	opctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	err := wait.ExponentialBackoffWithContext(opctx, wait.Backoff{
		Duration: 50 * time.Millisecond, Factor: 1.5, Steps: 8, Cap: time.Second,
	}, func(ctx context.Context) (bool, error) {
		sandbox, err := p.sandboxes.Get(ctx, id, metav1.GetOptions{})
		if err != nil {
			if retryableKubernetesError(err) && !apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, mapKubernetesError("get Sandbox", err)
		}
		if _, err := p.verifySandboxIdentity(sandbox, ""); err != nil {
			return false, err
		}
		if sandbox.Spec.OperatingMode == mode {
			return true, nil
		}
		copy := sandbox.DeepCopy()
		copy.Spec.OperatingMode = mode
		_, err = p.sandboxes.Update(ctx, copy, metav1.UpdateOptions{})
		if retryableKubernetesError(err) {
			return false, nil
		}
		return err == nil, mapKubernetesError("update Sandbox operating mode", err)
	})
	if err != nil {
		return ports.ProviderResource{}, err
	}
	target := ports.ProviderReady
	if mode == sandboxv1beta1.SandboxOperatingModeSuspended {
		target = ports.ProviderPaused
	}
	return p.waitForState(opctx, id, target)
}

func (p *Provider) Delete(ctx context.Context, id string) error {
	if !p.validResourceID(id) {
		return domain.ErrNotFound
	}
	opctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	sandbox, err := p.sandboxes.Get(opctx, id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return mapKubernetesError("get Sandbox before delete", err)
	}
	capsuleID, err := p.verifySandboxIdentity(sandbox, "")
	if err != nil {
		return err
	}
	propagation := metav1.DeletePropagationForeground
	if err := p.sandboxes.Delete(opctx, id, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &sandbox.UID},
		PropagationPolicy: &propagation,
	}); err != nil && !apierrors.IsNotFound(err) {
		return mapKubernetesError("delete Sandbox", err)
	}
	err = wait.PollUntilContextCancel(opctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := p.sandboxes.Get(ctx, id, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			if retryableKubernetesError(err) {
				return false, nil
			}
			return false, mapKubernetesError("wait for Sandbox deletion", err)
		}
		if _, err := p.verifySandboxIdentity(current, domain.CapsuleID(capsuleID)); err != nil {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Sandbox finalizers: %w", err)
	}
	return p.deleteTokenSecret(opctx, sandbox, domain.CapsuleID(capsuleID))
}

func (p *Provider) sandbox(capsuleID domain.CapsuleID, name, image string) *sandboxv1beta1.Sandbox {
	labels := p.labels(capsuleID, "sandbox")
	annotations := map[string]string{annotationID: string(capsuleID), annotationImage: image}
	if p.config.Class != "" {
		annotations[annotationClass] = p.config.Class
	}
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	uid, gid := int64(10001), int64(10001)
	defaultMode := int32(0o400)
	shutdown := metav1.NewTime(time.Now().UTC().Add(p.config.TTL))
	policy := sandboxv1beta1.ShutdownPolicyDelete
	storageClass := p.config.StorageClass
	return &sandboxv1beta1.Sandbox{
		TypeMeta: metav1.TypeMeta{APIVersion: sandboxv1beta1.GroupVersion.String(), Kind: "Sandbox"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: p.config.Namespace, Labels: labels, Annotations: annotations,
		},
		Spec: sandboxv1beta1.SandboxSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				Service: pointer(false),
				PodTemplate: sandboxv1beta1.PodTemplate{
					ObjectMeta: sandboxv1beta1.PodMetadata{Labels: p.labels(capsuleID, "pod")},
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: pointer(false),
						RuntimeClassName:             optionalPointer(p.config.RuntimeClass),
						SecurityContext: &corev1.PodSecurityContext{
							RunAsNonRoot: &runAsNonRoot, RunAsUser: &uid, RunAsGroup: &gid,
							FSGroup: &gid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Containers: []corev1.Container{{
							Name: "capsule", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{"/usr/local/bin/capsuled"},
							Args: []string{"serve", "--listen=127.0.0.1:7777", "--workspace=" + workspace,
								"--token-file=" + tokenPath, "--setup-timeout=" + p.config.SetupTimeout.String()},
							WorkingDir: workspace,
							Ports:      []corev1.ContainerPort{{Name: "capsuled", ContainerPort: protocolPort, Protocol: corev1.ProtocolTCP}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
								RunAsNonRoot:             &runAsNonRoot,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
								SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
							},
							Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse(p.config.CPULimit),
								corev1.ResourceMemory:           resource.MustParse(p.config.MemoryLimit),
								corev1.ResourceEphemeralStorage: resource.MustParse(p.config.EphemeralLimit),
							}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "workspace", MountPath: workspace},
								{Name: "token", MountPath: "/var/run/meridian/token", ReadOnly: true},
								{Name: "tmp", MountPath: "/tmp"},
								{Name: "home", MountPath: "/home/capsule"},
							},
							ReadinessProbe: &corev1.Probe{
								// capsuled deliberately listens only on Pod loopback. Kubernetes
								// HTTP probes target the Pod IP, so use the image's bounded local
								// healthcheck command instead of widening protocol ingress.
								ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
									"/usr/local/bin/capsuled", "healthcheck",
								}}},
								PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 6,
							},
						}},
						Volumes: []corev1.Volume{
							{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
								SecretName: name + "-token", DefaultMode: &defaultMode,
							}}},
							{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
								Medium: corev1.StorageMediumMemory, SizeLimit: quantityPointer("64Mi"),
							}}},
							{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: quantityPointer("512Mi"),
							}}},
						},
					},
				},
				VolumeClaimTemplates: []sandboxv1beta1.PersistentVolumeClaimTemplate{{
					EmbeddedObjectMetadata: sandboxv1beta1.EmbeddedObjectMetadata{
						Name: "workspace", Labels: p.labels(capsuleID, "workspace"),
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: optionalPointer(storageClass),
						Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse(p.config.VolumeSize),
						}},
					},
				}},
			},
			Lifecycle:     sandboxv1beta1.Lifecycle{ShutdownTime: &shutdown, ShutdownPolicy: &policy},
			OperatingMode: sandboxv1beta1.SandboxOperatingModeRunning,
		},
	}
}

func (p *Provider) ensureTokenSecret(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
) (string, error) {
	name := sandbox.Name + "-token"
	secrets := p.core.CoreV1().Secrets(p.config.Namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if err := p.verifyOwnedMeta(existing.ObjectMeta, capsuleID, "token"); err != nil {
			return "", err
		}
		if !metav1.IsControlledBy(existing, sandbox) {
			return "", fmt.Errorf("%w: token Secret owner does not match Sandbox", domain.ErrConflict)
		}
		token := existing.Data["token"]
		if len(token) < 32 || len(token) > 128 {
			return "", fmt.Errorf("%w: token Secret is invalid", domain.ErrCorrupt)
		}
		return string(token), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", mapKubernetesError("get token Secret", err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate Capsule protocol token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: p.config.Namespace, Labels: p.labels(capsuleID, "token"),
			Annotations: map[string]string{annotationID: string(capsuleID)},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				sandbox, schema.GroupVersionKind{Group: sandboxv1beta1.GroupVersion.Group,
					Version: sandboxv1beta1.GroupVersion.Version, Kind: "Sandbox"},
			)},
		},
		Immutable: pointer(true), Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte(token)},
	}
	created, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return p.ensureTokenSecret(ctx, sandbox, capsuleID)
	}
	if err != nil {
		return "", mapKubernetesError("create token Secret", err)
	}
	return string(created.Data["token"]), nil
}

func (p *Provider) deleteTokenSecret(
	ctx context.Context,
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
) error {
	secrets := p.core.CoreV1().Secrets(p.config.Namespace)
	secret, err := secrets.Get(ctx, sandbox.Name+"-token", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return mapKubernetesError("get token Secret for cleanup", err)
	}
	if err := p.verifyOwnedMeta(secret.ObjectMeta, capsuleID, "token"); err != nil {
		return err
	}
	if !metav1.IsControlledBy(secret, sandbox) {
		return fmt.Errorf("%w: token Secret owner does not match deleted Sandbox", domain.ErrConflict)
	}
	if err := secrets.Delete(ctx, secret.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &secret.UID},
	}); err != nil && !apierrors.IsNotFound(err) {
		return mapKubernetesError("delete token Secret", err)
	}
	return nil
}

func (p *Provider) waitForState(
	ctx context.Context,
	name string,
	target ports.ProviderState,
) (ports.ProviderResource, error) {
	var result ports.ProviderResource
	var lastError error
	err := wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		sandbox, err := p.sandboxes.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if retryableKubernetesError(err) {
				lastError = err
				return false, nil
			}
			return false, mapKubernetesError("poll Sandbox", err)
		}
		if _, err := p.verifySandboxIdentity(sandbox, ""); err != nil {
			return false, err
		}
		result, err = p.resource(ctx, sandbox)
		if err != nil {
			return false, err
		}
		return result.State == target, nil
	})
	if err != nil {
		if lastError != nil {
			return ports.ProviderResource{}, fmt.Errorf(
				"wait for Sandbox %s: %w (last API error: %v)", target, err, lastError,
			)
		}
		return ports.ProviderResource{}, fmt.Errorf("wait for Sandbox %s: %w", target, err)
	}
	return result, nil
}

func (p *Provider) resource(ctx context.Context, sandbox *sandboxv1beta1.Sandbox) (ports.ProviderResource, error) {
	result := ports.ProviderResource{
		ID: sandbox.Name, State: ports.ProviderPreparing, ImageDigest: p.imageIdentity(ctx, sandbox),
	}
	if sandbox.Spec.OperatingMode == sandboxv1beta1.SandboxOperatingModeSuspended {
		condition := conditionFor(sandbox, sandboxv1beta1.SandboxConditionSuspended)
		if condition != nil && condition.ObservedGeneration >= sandbox.Generation &&
			condition.Status == metav1.ConditionTrue {
			result.State = ports.ProviderPaused
		}
		return result, nil
	}
	ready := conditionFor(sandbox, sandboxv1beta1.SandboxConditionReady)
	if ready == nil || ready.ObservedGeneration < sandbox.Generation ||
		ready.Status == metav1.ConditionUnknown {
		return result, nil
	}
	if ready.Status == metav1.ConditionTrue {
		result.State = ports.ProviderReady
		return result, nil
	}
	switch ready.Reason {
	case sandboxv1beta1.SandboxReasonDependenciesNotReady,
		sandboxv1beta1.SandboxReasonSuspended:
		return result, nil
	case sandboxv1beta1.SandboxReasonExpired:
		return ports.ProviderResource{ID: sandbox.Name, State: ports.ProviderDeleted}, nil
	default:
		return ports.ProviderResource{}, fmt.Errorf(
			"Sandbox failed readiness: %s: %s", ready.Reason, bounded(ready.Message, 512),
		)
	}
}

func conditionFor(sandbox *sandboxv1beta1.Sandbox, kind sandboxv1beta1.ConditionType) *metav1.Condition {
	for index := range sandbox.Status.Conditions {
		if sandbox.Status.Conditions[index].Type == string(kind) {
			return &sandbox.Status.Conditions[index]
		}
	}
	return nil
}

func (p *Provider) imageIdentity(ctx context.Context, sandbox *sandboxv1beta1.Sandbox) string {
	pod, err := p.ownedPod(ctx, sandbox)
	if err == nil {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "capsule" && status.ImageID != "" {
				return status.ImageID
			}
		}
	}
	return sandbox.Annotations[annotationImage]
}

func (p *Provider) resourceName(capsuleID domain.CapsuleID) string {
	hash := sha256.Sum256([]byte(capsuleID))
	suffix := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:10]))
	return p.config.NamePrefix + "-" + suffix
}

func (p *Provider) validResourceID(id string) bool {
	return strings.HasPrefix(id, p.config.NamePrefix+"-") &&
		len(id) <= 63 && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func (p *Provider) labels(capsuleID domain.CapsuleID, role string) map[string]string {
	hash := sha256.Sum256([]byte(capsuleID))
	return map[string]string{
		labelOwned:       "meridian",
		labelCapsuleHash: base64.RawURLEncoding.EncodeToString(hash[:16]),
		labelRole:        role,
	}
}

func (p *Provider) verifySandbox(
	sandbox *sandboxv1beta1.Sandbox,
	capsuleID domain.CapsuleID,
	image string,
) error {
	if _, err := p.verifySandboxIdentity(sandbox, capsuleID); err != nil {
		return err
	}
	if sandbox.Annotations[annotationImage] != image {
		return fmt.Errorf("%w: adopted Sandbox image differs", domain.ErrConflict)
	}
	return nil
}

func (p *Provider) verifySandboxIdentity(
	sandbox *sandboxv1beta1.Sandbox,
	expected domain.CapsuleID,
) (string, error) {
	if sandbox == nil || sandbox.Namespace != p.config.Namespace {
		return "", fmt.Errorf("%w: Sandbox namespace does not match", domain.ErrConflict)
	}
	if sandbox.UID == "" {
		return "", fmt.Errorf("%w: Sandbox UID is missing", domain.ErrCorrupt)
	}
	actual := domain.CapsuleID(sandbox.Annotations[annotationID])
	if err := p.verifyOwnedMeta(sandbox.ObjectMeta, expected, "sandbox"); err != nil {
		return "", err
	}
	if actual == "" || p.resourceName(actual) != sandbox.Name {
		return "", fmt.Errorf("%w: Sandbox name and ownership identity disagree", domain.ErrConflict)
	}
	return string(actual), nil
}

func (p *Provider) verifyOwnedMeta(meta metav1.ObjectMeta, expected domain.CapsuleID, role string) error {
	if meta.Labels[labelOwned] != "meridian" || meta.Labels[labelRole] != role {
		return fmt.Errorf("%w: Kubernetes resource is not owned by Meridian", domain.ErrConflict)
	}
	actual := domain.CapsuleID(meta.Annotations[annotationID])
	if actual == "" {
		// Child resources use the collision-resistant label while the Secret's
		// full identity remains available through its controlling Sandbox.
		if expected == "" || meta.Labels[labelCapsuleHash] != p.labels(expected, role)[labelCapsuleHash] {
			return fmt.Errorf("%w: Kubernetes resource ownership identity is missing", domain.ErrConflict)
		}
		return nil
	}
	if expected != "" && actual != expected {
		return fmt.Errorf("%w: Kubernetes resource belongs to another Capsule", domain.ErrConflict)
	}
	if meta.Labels[labelCapsuleHash] != p.labels(actual, role)[labelCapsuleHash] {
		return fmt.Errorf("%w: Kubernetes resource ownership hash disagrees", domain.ErrConflict)
	}
	return nil
}

func mapKubernetesError(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case apierrors.IsNotFound(err):
		return domain.ErrNotFound
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%w: %s: %v", domain.ErrConflict, operation, err)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return fmt.Errorf("%w: %s: %v", domain.ErrInvalid, operation, err)
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func retryableKubernetesError(err error) bool {
	return err != nil &&
		!apierrors.IsForbidden(err) &&
		!apierrors.IsUnauthorized(err) &&
		!apierrors.IsInvalid(err) &&
		!apierrors.IsBadRequest(err)
}

func pointer[T any](value T) *T { return &value }

func optionalPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func quantityPointer(value string) *resource.Quantity {
	quantity := resource.MustParse(value)
	return &quantity
}

func bounded(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

var _ ports.CapsuleProvider = (*Provider)(nil)
var _ ports.CapsuleRuntime = (*Provider)(nil)
var _ ports.WorkspaceSnapshotter = (*Provider)(nil)
