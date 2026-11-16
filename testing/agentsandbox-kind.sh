#!/usr/bin/env bash
set -euo pipefail

if [[ "${MERIDIAN_AGENTSANDBOX_TEST:-}" != "1" ]]; then
  echo "SKIP: set MERIDIAN_AGENTSANDBOX_TEST=1 to run the destructive Kind smoke test"
  exit 0
fi

for command in docker kind kubectl go; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "SKIP: required command '$command' is unavailable"
    exit 0
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: Docker is unavailable"
  exit 0
fi

HELM="${HELM:-helm}"
if [[ ! -x "$HELM" ]] && ! command -v "$HELM" >/dev/null 2>&1; then
  echo "SKIP: Helm is unavailable"
  exit 0
fi

cluster="${MERIDIAN_AGENTSANDBOX_CLUSTER:-meridian-agentsandbox}"
namespace="${MERIDIAN_AGENTSANDBOX_NAMESPACE:-meridian-agentsandbox-test}"
created=0
# ADR 0050 RuntimeClasses applied below; cleanup removes them on any exit.
isolation_rc="meridian-it-runc"
kubernetes_rc="meridian-it-kata"
if ! kind get clusters | grep -qxF "$cluster"; then
  kind create cluster \
    --name "$cluster" \
    --image "kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5"
  created=1
fi
cleanup() {
  status=$?
  if [[ "$status" != "0" ]]; then
    kubectl -n "$namespace" get all,pvc,sandboxes.agents.x-k8s.io,secrets -o wide 2>/dev/null || true
    kubectl -n "$namespace" get events --sort-by=.lastTimestamp 2>/dev/null || true
    kubectl -n "$namespace" logs \
      -l app.kubernetes.io/name=meridian,app.kubernetes.io/component=control-plane \
      --all-containers --tail=200 2>/dev/null || true
    kubectl -n "$namespace" logs \
      -l app.kubernetes.io/managed-by=meridian \
      --all-containers --tail=200 2>/dev/null || true
  fi
  kubectl delete runtimeclass "$isolation_rc" "$kubernetes_rc" --ignore-not-found >/dev/null 2>&1 || true
  if [[ "$created" == "1" && "${MERIDIAN_AGENTSANDBOX_KEEP_CLUSTER:-}" != "1" ]]; then
    kind delete cluster --name "$cluster"
  fi
  return "$status"
}
trap cleanup EXIT

kubectl config use-context "kind-$cluster" >/dev/null
kubectl apply -f \
  "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.6/sandbox.yaml"
kubectl -n agent-sandbox-system rollout status \
  deployment/agent-sandbox-controller --timeout=180s

docker build -t meridian-capsule:agentsandbox-it -f images/capsule/Dockerfile .
docker build -t meridian:agentsandbox-it -f deploy/docker/Dockerfile.meridiand .
kind load docker-image --name "$cluster" \
  meridian-capsule:agentsandbox-it meridian:agentsandbox-it

"$HELM" upgrade --install meridian deploy/helm/meridian \
  --namespace "$namespace" \
  --create-namespace \
  --set namespace.create=false \
  --set image.repository=meridian \
  --set image.tag=agentsandbox-it \
  --set image.pullPolicy=Never \
  --set capsuleImage.repository=meridian-capsule \
  --set capsuleImage.tag=agentsandbox-it \
  --set capsuleImage.pullPolicy=Never \
  --set agentSandbox.namespace="$namespace" \
  --set networkPolicy.enabled=false \
  --wait --timeout 5m

MERIDIAN_AGENTSANDBOX_TEST=1 \
MERIDIAN_AGENTSANDBOX_NAMESPACE="$namespace" \
MERIDIAN_CAPSULE_IMAGE=meridian-capsule:agentsandbox-it \
  go test ./internal/provider/agentsandbox -run '^TestIntegrationLifecycle$' -count=1 -v -timeout=10m

# ADR 0050 RuntimeClass verification against a real API server: a runc
# handler must be refused before any Sandbox side effect, and a kata-qemu
# handler must verify. Kind has no Kata runtime, so the positive case checks
# verification and Capabilities only and never schedules a pod on it.

kubectl apply -f - <<EOF
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: $isolation_rc
handler: runc
EOF
MERIDIAN_AGENTSANDBOX_ISOLATION_MODE=refuse \
MERIDIAN_AGENTSANDBOX_RUNTIME_CLASS="$isolation_rc" \
MERIDIAN_AGENTSANDBOX_ISOLATION_CLASS=sandbox \
MERIDIAN_AGENTSANDBOX_TEST=1 \
MERIDIAN_AGENTSANDBOX_NAMESPACE="$namespace" \
MERIDIAN_CAPSULE_IMAGE=meridian-capsule:agentsandbox-it \
  go test ./internal/provider/agentsandbox -run '^TestIntegrationIsolation$' -count=1 -v -timeout=5m

kubectl apply -f - <<EOF
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: $kubernetes_rc
handler: kata-qemu
EOF
MERIDIAN_AGENTSANDBOX_ISOLATION_MODE=verify \
MERIDIAN_AGENTSANDBOX_RUNTIME_CLASS="$kubernetes_rc" \
MERIDIAN_AGENTSANDBOX_ISOLATION_CLASS=microvm \
MERIDIAN_AGENTSANDBOX_TEST=1 \
MERIDIAN_AGENTSANDBOX_NAMESPACE="$namespace" \
MERIDIAN_CAPSULE_IMAGE=meridian-capsule:agentsandbox-it \
  go test ./internal/provider/agentsandbox -run '^TestIntegrationIsolation$' -count=1 -v -timeout=5m

if kubectl -n "$namespace" get sandboxes.agents.x-k8s.io \
  -l app.kubernetes.io/managed-by=meridian -o name | grep -q .; then
  echo "owned Sandbox resources remained after the integration flow" >&2
  exit 1
fi
if kubectl -n "$namespace" get secrets \
  -l app.kubernetes.io/managed-by=meridian -o name | grep -q .; then
  echo "owned token Secrets remained after the integration flow" >&2
  exit 1
fi

echo "PASS: Agent Sandbox v0.5.6 lifecycle smoke completed"
