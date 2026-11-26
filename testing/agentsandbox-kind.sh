#!/usr/bin/env bash
set -euo pipefail

if [[ "${MERIDIAN_AGENTSANDBOX_TEST:-}" != "1" ]]; then
  echo "SKIP: set MERIDIAN_AGENTSANDBOX_TEST=1 to run the destructive Kind smoke test"
  exit 0
fi

for command in docker kind kubectl curl go jq; do
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
forward_pid=""
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
  if [[ -n "$forward_pid" ]]; then
    kill "$forward_pid" 2>/dev/null || true
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

# ADR 0050 through the deployed control plane: the release reconfigured for
# the kata handler must report the verified class on the public API, not
# merely inside the provider process. The default release is restored
# afterwards so the remaining checks keep their original configuration.
"$HELM" upgrade --install meridian deploy/helm/meridian \
  --namespace "$namespace" \
  --set namespace.create=false \
  --set image.repository=meridian \
  --set image.tag=agentsandbox-it \
  --set image.pullPolicy=Never \
  --set capsuleImage.repository=meridian-capsule \
  --set capsuleImage.tag=agentsandbox-it \
  --set capsuleImage.pullPolicy=Never \
  --set agentSandbox.namespace="$namespace" \
  --set agentSandbox.runtimeClassName="$kubernetes_rc" \
  --set agentSandbox.isolationClass=microvm \
  --set networkPolicy.enabled=false \
  --wait --timeout 5m
kubectl -n "$namespace" rollout status \
  deployment/meridian-meridian --timeout=180s
# Address the Deployment, not a listed pod: right after the rollout the
# previous pod may still be terminating and sort first.
token="$(kubectl -n "$namespace" exec deployment/meridian-meridian -- \
  /usr/local/bin/meridiand installation-token \
  --data-dir=/var/lib/meridian-volume/data)"
kubectl -n "$namespace" port-forward deployment/meridian-meridian 18080:8080 >/dev/null 2>&1 &
forward_pid=$!
capabilities=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if capabilities="$(curl -sf \
    -H "Authorization: Bearer $token" \
    http://127.0.0.1:18080/capabilities)"; then
    break
  fi
  sleep 2
done
kill "$forward_pid" 2>/dev/null || true
wait "$forward_pid" 2>/dev/null || true
forward_pid=""
if ! printf '%s' "$capabilities" | grep -q '"isolation":"microvm"'; then
  echo "capabilities = $capabilities" >&2
  echo "the control plane did not report the verified microvm isolation" >&2
  exit 1
fi

"$HELM" upgrade --install meridian deploy/helm/meridian \
  --namespace "$namespace" \
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

kubectl -n "$namespace" rollout status \
  deployment/meridian-meridian --timeout=180s

# ADR 0065 through the deployed control plane: an in-cluster meridiand
# reaches capsuled on the pod IP over mutual TLS, never port-forward. The
# Go lifecycle test above runs outside the cluster and covers port-forward.
token="$(kubectl -n "$namespace" exec deployment/meridian-meridian -- \
  /usr/local/bin/meridiand installation-token \
  --data-dir=/var/lib/meridian-volume/data)"
kubectl -n "$namespace" port-forward deployment/meridian-meridian 18081:8080 >/dev/null 2>&1 &
forward_pid=$!
base="http://127.0.0.1:18081"
run_id="$(date -u +%Y%m%d%H%M%S)-$$"
request_count=0
api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS --fail-with-body -X "$method" -H "Authorization: Bearer $token")
  if [[ "$method" != "GET" ]]; then
    [[ -n "$body" ]] || body='{}'
    request_count=$((request_count + 1))
    args+=(-H "Content-Type: application/json"
      -H "Idempotency-Key: kind-$run_id-$request_count" --data "$body")
  fi
  curl "${args[@]}" "$base$path"
}
for _ in $(seq 1 30); do
  if api GET /capabilities >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
wait_state() {
  local capsule="$1" want="$2" deadline=$((SECONDS + 600)) state=""
  while ((SECONDS < deadline)); do
    state="$(api GET "/capsules/$capsule" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "Failed" ]]; then
      echo "Capsule $capsule failed: $(api GET "/capsules/$capsule" | jq -r '.failure // ""')" >&2
      exit 1
    fi
    sleep 3
  done
  echo "Capsule $capsule did not reach $want (last state: $state)" >&2
  exit 1
}
lifecycle() {
  local version
  version="$(api GET "/capsules/$1" | jq -r .resourceVersion)"
  api POST "/capsules/$1/$2" "{\"expectedResourceVersion\":$version}" >/dev/null
}
owned() {
  kubectl -n "$namespace" get "$1" -o json |
    jq -r --arg id "$2" '.items[] | select(.metadata.annotations["meridian.orloj.io/capsule-id"] == $id) | .metadata.name'
}
project_id="$(api POST /projects "{\"name\":\"kind-pod-mtls-$run_id\"}" | jq -r .id)"
capsule="$(api POST "/projects/$project_id/capsules" '{"name":"kind-pod-mtls"}' | jq -r .id)"
wait_state "$capsule" Ready
sandbox="$(owned sandboxes.agents.x-k8s.io "$capsule")"
transport="$(kubectl -n "$namespace" get sandboxes.agents.x-k8s.io "$sandbox" \
  -o jsonpath='{.metadata.annotations.meridian\.orloj\.io/transport}')"
if [[ "$transport" != "pod-mtls" ]]; then
  echo "in-cluster Capsule Sandbox records transport '$transport', want pod-mtls" >&2
  exit 1
fi
kubectl -n "$namespace" get secret "$sandbox-tls" >/dev/null
api GET "/capsules/$capsule/files" | jq -e '.items | type == "array"' >/dev/null
archive_bytes="$(curl -sS --fail-with-body -H "Authorization: Bearer $token" \
  "$base/capsules/$capsule/workspace" | wc -c)"
if [[ "$archive_bytes" -lt 1 ]]; then
  echo "workspace export over pod mTLS returned $archive_bytes bytes" >&2
  exit 1
fi
# Pause replaces the pod, so resume dials a new pod IP with the same pin.
lifecycle "$capsule" pause
wait_state "$capsule" Paused
lifecycle "$capsule" resume
wait_state "$capsule" Ready
api GET "/capsules/$capsule/files" | jq -e '.items | type == "array"' >/dev/null
lifecycle "$capsule" delete
delete_deadline=$((SECONDS + 300))
while [[ -n "$(owned sandboxes.agents.x-k8s.io "$capsule")" ]] ||
  kubectl -n "$namespace" get secret "$sandbox-tls" >/dev/null 2>&1 ||
  kubectl -n "$namespace" get secret "$sandbox-token" >/dev/null 2>&1; do
  if ((SECONDS >= delete_deadline)); then
    echo "deleting the in-cluster Capsule left its Sandbox or Secrets behind" >&2
    exit 1
  fi
  sleep 3
done
kill "$forward_pid" 2>/dev/null || true
wait "$forward_pid" 2>/dev/null || true
forward_pid=""
echo "PASS: in-cluster Capsule over pod-IP mutual TLS (ADR 0065)"

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
