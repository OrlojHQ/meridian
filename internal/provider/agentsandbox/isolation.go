package agentsandbox

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

// builtinHandlers is the built-in RuntimeClass handler vocabulary (ADR 0050).
// A declared isolation class is verified only when the cluster's RuntimeClass
// handler belongs to the class's set. Source: kata-containers
// docs/kata-deploy-runtimeclasses.md at 4.3.1 together with the kata-deploy
// chart `shims` values, which define the RuntimeClass handler names kata-deploy
// creates (QEMU, Cloud Hypervisor, and Dragonball, plus their runtime-rs
// variants). Firecracker (`kata-fc`) is deliberately absent: at 4.3.1 it is
// only shipped behind a devmapper/block-storage profile that Meridian's Agent
// Sandbox provider does not provision, so a `kata-fc` pod cannot be verified
// as microvm.
var builtinHandlers = map[domain.IsolationClass]map[string]bool{
	domain.IsolationSandbox: {
		"runsc":  true,
		"gvisor": true,
	},
	domain.IsolationMicroVM: {
		"kata":                 true,
		"kata-qemu":            true,
		"kata-clh":             true,
		"kata-dragonball":      true,
		"kata-qemu-runtime-rs": true,
		"kata-clh-runtime-rs":  true,
	},
}

// neverIsolatingHandlers are container-runtime handlers that share the host
// kernel with no added user-kernel boundary. They never satisfy a declared
// sandbox or microvm class, and an operator extension may not add them.
var neverIsolatingHandlers = map[string]bool{
	"runc":  true,
	"crun":  true,
	"youki": true,
}

const maxIsolationHandlers = 16

// unsupportedExtensions are handlers Meridian refuses even as operator
// extensions. Firecracker (`kata-fc`) needs a devmapper snapshotter and block
// storage that this provider does not provision (ADR 0050), so accepting it
// would claim a class whose workspace path is unvalidated.
var unsupportedExtensions = map[string]bool{
	"kata-fc": true,
}

// verifiedRuntimeClass records what the provider verified about the
// configured RuntimeClass. Capabilities reports `class` — never the declared
// text — until a recheck proves otherwise.
type verifiedRuntimeClass struct {
	class   domain.IsolationClass
	name    string
	handler string
	uid     string
}

// verifyIsolation reads the configured RuntimeClass from the Kubernetes API
// and returns the isolation class its handler proves. An empty IsolationClass
// declaration verifies nothing and reports an empty class: Meridian never
// infers a stronger claim than the configuration declares.
func (p *Provider) verifyIsolation(ctx context.Context) (verifiedRuntimeClass, error) {
	if p.config.IsolationClass == "" {
		return verifiedRuntimeClass{}, nil
	}
	class, handler, uid, err := p.readRuntimeClass(ctx)
	if err != nil {
		return verifiedRuntimeClass{}, err
	}
	if !isolationHandlerAccepts(p.config.IsolationClass, p.config.IsolationHandlers, handler) {
		return verifiedRuntimeClass{}, fmt.Errorf(
			"%w: RuntimeClass %q uses handler %q, which is not a known %s handler",
			domain.ErrUnsupported, class.Name, handler, p.config.IsolationClass.Label(),
		)
	}
	return verifiedRuntimeClass{
		class: p.config.IsolationClass, name: class.Name, handler: handler, uid: uid,
	}, nil
}

// recheckIsolation re-reads the RuntimeClass and fails closed when it changed
// since the last verification, so a swapped handler cannot outlive a
// capabilities check. A provider that has not verified yet (no Capabilities
// call) verifies from scratch instead.
func (p *Provider) recheckIsolation(ctx context.Context) error {
	if p.config.IsolationClass == "" {
		return nil
	}
	p.isolationMu.Lock()
	verified := p.isolation
	p.isolationMu.Unlock()
	if verified.name == "" {
		verified, err := p.verifyIsolation(ctx)
		if err != nil {
			return err
		}
		p.isolationMu.Lock()
		p.isolation = verified
		p.isolationMu.Unlock()
		return nil
	}
	class, handler, _, err := p.readRuntimeClass(ctx)
	if err != nil {
		return err
	}
	if string(class.UID) != verified.uid || handler != verified.handler {
		return fmt.Errorf(
			"%w: RuntimeClass %q changed after meridiand started; restart meridiand",
			domain.ErrUnsupported, verified.name,
		)
	}
	return nil
}

// adoptIsolation compares an existing Sandbox's runtime class with the
// configured one before Meridian adopts it.
func (p *Provider) adoptIsolation(sandbox *sandboxv1beta1.Sandbox) error {
	if p.config.IsolationClass == "" || p.config.RuntimeClass == "" {
		return nil
	}
	if sandbox.Spec.PodTemplate.Spec.RuntimeClassName == nil ||
		*sandbox.Spec.PodTemplate.Spec.RuntimeClassName != p.config.RuntimeClass {
		return fmt.Errorf(
			"%w: adopted Sandbox runtime class differs from the configured runtime class",
			domain.ErrConflict,
		)
	}
	return nil
}

func (p *Provider) readRuntimeClass(ctx context.Context) (nodev1.RuntimeClass, string, string, error) {
	class, err := p.core.NodeV1().RuntimeClasses().Get(ctx, p.config.RuntimeClass, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nodev1.RuntimeClass{}, "", "", fmt.Errorf(
				"%w: RuntimeClass %q does not exist", domain.ErrNotFound, p.config.RuntimeClass,
			)
		}
		if apierrors.IsForbidden(err) {
			return nodev1.RuntimeClass{}, "", "", fmt.Errorf(
				"%w: meridiand cannot read RuntimeClass %q; grant get on node.k8s.io runtimeclasses for that name",
				domain.ErrUnsupported, p.config.RuntimeClass,
			)
		}
		return nodev1.RuntimeClass{}, "", "", fmt.Errorf("read RuntimeClass: %w", err)
	}
	if class.Handler == "" {
		return nodev1.RuntimeClass{}, "", "", fmt.Errorf(
			"%w: RuntimeClass %q declares no handler", domain.ErrCorrupt, class.Name,
		)
	}
	return *class, class.Handler, string(class.UID), nil
}

// isolationHandlerAccepts reports whether handler satisfies the declared
// class. A handler in the class's built-in set always passes. Operator
// extensions pass only when they remain valid DNS-1123 labels, never isolate
// nothing, and do not belong to a different class's built-in set.
func isolationHandlerAccepts(class domain.IsolationClass, extensions []string, handler string) bool {
	if builtinHandlers[class][handler] {
		return true
	}
	for _, extension := range extensions {
		if extension == handler {
			return !neverIsolatingHandlers[handler] && !handlerInOtherBuiltinSet(class, handler)
		}
	}
	return false
}

func handlerInOtherBuiltinSet(class domain.IsolationClass, handler string) bool {
	for other := range builtinHandlers {
		if other != class && builtinHandlers[other][handler] {
			return true
		}
	}
	return false
}

// isolationHandlersFromValues normalizes the operator extension list: it
// deduplicates, sorts, and validates each entry.
func isolationHandlersFromValues(values []string) ([]string, error) {
	seen := map[string]bool{}
	handlers := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(validation.IsDNS1123Label(value)) > 0 {
			return nil, fmt.Errorf("%w: isolation handler %q is not DNS-1123", domain.ErrInvalid, value)
		}
		if neverIsolatingHandlers[value] {
			return nil, fmt.Errorf(
				"%w: isolation handler %q never isolates a Capsule", domain.ErrUnsupported, value,
			)
		}
		if unsupportedExtensions[value] {
			return nil, fmt.Errorf(
				"%w: isolation handler %q is not supported by this provider",
				domain.ErrUnsupported, value,
			)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		handlers = append(handlers, value)
	}
	if len(handlers) > maxIsolationHandlers {
		return nil, fmt.Errorf(
			"%w: more than %d isolation handlers are not supported", domain.ErrInvalid, maxIsolationHandlers,
		)
	}
	sort.Strings(handlers)
	return handlers, nil
}
