#!/usr/bin/env bash
# Kata microVM validation for the Agent Sandbox provider (ADR 0050, #86).
#
# This runs against a real cluster with KVM nodes and Kata installed. CI
# cannot run it, and a Kind run does not validate isolation. It installs a
# throwaway Meridian release in its own namespace, drives two Capsules through
# the public API, checks that each pod is a VM behind the NetworkPolicy, and
# removes everything it created on success or failure.
#
# The exec-based checks use the operator's own kubectl credentials. Meridian's
# Role still grants no pod exec; nothing here widens it.
set -euo pipefail

if [[ "${MERIDIAN_KATA_VALIDATION:-}" != "1" ]]; then
  echo "SKIP: set MERIDIAN_KATA_VALIDATION=1 to run the Kata validation against a real cluster"
  exit 0
fi

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
pass() {
  echo "PASS $*"
}

cd "$(dirname "$0")/.."
HELM="${HELM:-helm}"
for command in kubectl curl jq openssl "$HELM"; do
  command -v "$command" >/dev/null 2>&1 || fail "required command '$command' is unavailable"
done

# Refuse to run unless the operator names the exact context, so a stale
# kubeconfig cannot point this at the wrong cluster. Every call pins it.
context="$(kubectl config current-context 2>/dev/null || true)"
echo "kubectl context: ${context:-<none>}"
if [[ -z "${MERIDIAN_KATA_CONTEXT:-}" ]]; then
  fail "set MERIDIAN_KATA_CONTEXT to the current context name to confirm the target cluster"
fi
if [[ "$context" != "$MERIDIAN_KATA_CONTEXT" ]]; then
  fail "MERIDIAN_KATA_CONTEXT=$MERIDIAN_KATA_CONTEXT does not match the current context '$context'"
fi
k() { kubectl --context "$MERIDIAN_KATA_CONTEXT" "$@"; }

runtime_class="${MERIDIAN_KATA_RUNTIME_CLASS:-kata}"
namespace="${MERIDIAN_KATA_NAMESPACE:-meridian-kata-validation}"
release="${MERIDIAN_KATA_RELEASE:-meridian-kata}"
deployment="$release-meridian"
local_port="${MERIDIAN_KATA_LOCAL_PORT:-18089}"
ready_timeout="${MERIDIAN_KATA_READY_TIMEOUT:-900}"
run_id="$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 3)"
[[ -n "${MERIDIAN_KATA_MERIDIAN_IMAGE:-}" ]] ||
  fail "set MERIDIAN_KATA_MERIDIAN_IMAGE to a meridiand image as repository:tag@sha256:digest"
[[ -n "${MERIDIAN_KATA_CAPSULE_IMAGE:-}" ]] ||
  fail "set MERIDIAN_KATA_CAPSULE_IMAGE to a Capsule image as repository:tag@sha256:digest"
[[ -n "${MERIDIAN_KATA_API_SERVER_CIDRS:-}" ]] ||
  fail "set MERIDIAN_KATA_API_SERVER_CIDRS to a JSON array covering the kubernetes Service ClusterIP and the API server endpoints"
jq -e 'type == "array" and length > 0' <<<"$MERIDIAN_KATA_API_SERVER_CIDRS" >/dev/null ||
  fail "MERIDIAN_KATA_API_SERVER_CIDRS must be a non-empty JSON array"
# 443 for the Service address, 6443 for a kubeadm API server endpoint.
api_server_ports="${MERIDIAN_KATA_API_SERVER_PORTS:-[443,6443]}"
jq -e 'type == "array" and length > 0 and all(type == "number")' <<<"$api_server_ports" >/dev/null ||
  fail "MERIDIAN_KATA_API_SERVER_PORTS must be a JSON array of port numbers"

for reference in "$MERIDIAN_KATA_MERIDIAN_IMAGE" "$MERIDIAN_KATA_CAPSULE_IMAGE"; do
  [[ "$reference" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] ||
    fail "image '$reference' must be pinned as repository:tag@sha256:<64 hex>"
done
# split_image REF prints "repository tag digest" for repository:tag@sha256:hex.
split_image() {
  local reference="$1" name digest tag
  name="${reference%@*}"
  digest="${reference#*@}"
  tag="latest"
  if [[ "${name##*/}" == *:* ]]; then
    tag="${name##*:}"
    name="${name%:*}"
  fi
  echo "$name" "$tag" "$digest"
}
read -r meridian_repo meridian_tag meridian_digest < <(split_image "$MERIDIAN_KATA_MERIDIAN_IMAGE")
read -r capsule_repo capsule_tag capsule_digest < <(split_image "$MERIDIAN_KATA_CAPSULE_IMAGE")

created_namespace=0
forward_pid=""
forward_reported=0
# forward_exited is true once the API port-forward has stopped. It is never
# restarted: it usually dies because the control-plane pod restarted, and
# the run must report that, not paper over it.
forward_exited() {
  [[ -n "$forward_pid" ]] && ! kill -0 "$forward_pid" 2>/dev/null
}
forward_failure() {
  forward_reported=1
  fail "the kubectl port-forward to deployment/$deployment exited, so the API stopped answering; check whether the control-plane pod restarted or local port $local_port is taken"
}
cleanup() {
  local status=$?
  # A single API call that failed ends the run under set -e, before any
  # FAIL line. Name the port-forward when that is why.
  if [[ "$status" != "0" && "$forward_reported" == "0" ]] && forward_exited; then
    echo "FAIL: the kubectl port-forward to deployment/$deployment had exited; check whether the control-plane pod restarted" >&2
  fi
  if [[ -n "$forward_pid" ]]; then
    kill "$forward_pid" 2>/dev/null || true
    wait "$forward_pid" 2>/dev/null || true
  fi
  if [[ "$status" != "0" ]]; then
    k -n "$namespace" get all,pvc,sandboxes.agents.x-k8s.io,secrets -o wide 2>/dev/null || true
    k -n "$namespace" get events --sort-by=.lastTimestamp 2>/dev/null | tail -60 || true
    k -n "$namespace" logs "deployment/$deployment" --all-containers --tail=200 2>/dev/null || true
  fi
  if [[ "${MERIDIAN_KATA_KEEP:-}" == "1" ]]; then
    echo "MERIDIAN_KATA_KEEP=1: leaving release $release in namespace $namespace" >&2
    return "$status"
  fi
  "$HELM" --kube-context "$MERIDIAN_KATA_CONTEXT" uninstall "$release" \
    --namespace "$namespace" --wait --timeout 5m >/dev/null 2>&1 || true
  if [[ "$created_namespace" == "1" ]]; then
    # Namespace deletion removes any Sandbox, pod, PVC, and Secret the
    # Capsule steps left behind after a failure.
    k delete namespace "$namespace" --ignore-not-found --wait --timeout=10m >/dev/null 2>&1 ||
      echo "WARNING: namespace $namespace did not finish deleting; check it by hand" >&2
  fi
  return "$status"
}
trap cleanup EXIT

# --- Preflight ---------------------------------------------------------------

handler="$(k get runtimeclass "$runtime_class" -o jsonpath='{.handler}' 2>/dev/null)" ||
  fail "RuntimeClass $runtime_class does not exist"
echo "RuntimeClass $runtime_class handler: $handler"
node_selector="$(k get runtimeclass "$runtime_class" -o json |
  jq -r '.scheduling.nodeSelector // {} | to_entries | map("\(.key)=\(.value)") | join(",")')"
if [[ -n "$node_selector" ]]; then
  echo "RuntimeClass nodeSelector: $node_selector"
  schedulable="$(k get nodes -l "$node_selector" -o json |
    jq '[.items[] | select(.spec.unschedulable != true)] | length')"
else
  echo "RuntimeClass has no nodeSelector; every schedulable node must run Kata"
  schedulable="$(k get nodes -o json | jq '[.items[] | select(.spec.unschedulable != true)] | length')"
fi
[[ "$schedulable" -ge 1 ]] || fail "no schedulable node matches RuntimeClass $runtime_class"
tolerations="$(k get runtimeclass "$runtime_class" -o json | jq -c '.scheduling.tolerations // []')"
echo "RuntimeClass tolerations: $tolerations"
k get crd sandboxes.agents.x-k8s.io -o json |
  jq -e '.spec.versions[] | select(.name == "v1beta1" and .served)' >/dev/null ||
  fail "the Agent Sandbox CRD does not serve v1beta1"
pass preflight

# A namespace that already exists is used only when an earlier run of this
# script labelled it, so the cleanup can never delete someone else's.
if k get namespace "$namespace" >/dev/null 2>&1; then
  owner="$(k get namespace "$namespace" -o jsonpath='{.metadata.labels.meridian\.orloj\.io/kata-validation}')"
  [[ "$owner" == "true" ]] ||
    fail "namespace $namespace exists and was not created by this script; choose MERIDIAN_KATA_NAMESPACE"
else
  k create namespace "$namespace" >/dev/null
  k label namespace "$namespace" meridian.orloj.io/kata-validation=true >/dev/null
fi
created_namespace=1

# --- 1. Install with a verified microvm class and the installation floor ----

storage_args=()
if [[ -n "${MERIDIAN_KATA_STORAGE_CLASS:-}" ]]; then
  storage_args=(--set "agentSandbox.storageClassName=$MERIDIAN_KATA_STORAGE_CLASS")
fi
"$HELM" --kube-context "$MERIDIAN_KATA_CONTEXT" upgrade --install "$release" deploy/helm/meridian \
  --namespace "$namespace" \
  --set namespace.create=false \
  --set image.repository="$meridian_repo" \
  --set image.tag="$meridian_tag" \
  --set image.digest="$meridian_digest" \
  --set capsuleImage.repository="$capsule_repo" \
  --set capsuleImage.tag="$capsule_tag" \
  --set capsuleImage.digest="$capsule_digest" \
  --set agentSandbox.namespace="$namespace" \
  --set agentSandbox.runtimeClassName="$runtime_class" \
  --set agentSandbox.isolationClass=microvm \
  --set requireIsolation=microvm \
  --set-json "networkPolicy.kubernetesAPIServerCIDRs=$MERIDIAN_KATA_API_SERVER_CIDRS" \
  --set-json "networkPolicy.kubernetesAPIServerPorts=$api_server_ports" \
  ${storage_args[@]+"${storage_args[@]}"} \
  --wait --timeout 10m ||
  fail "the release did not become ready; if meridiand logs an i/o timeout to the API, MERIDIAN_KATA_API_SERVER_CIDRS must cover the kubernetes Service ClusterIP ($(k -n default get service kubernetes -o jsonpath='{.spec.clusterIP}')) and the API server endpoints, and MERIDIAN_KATA_API_SERVER_PORTS ($api_server_ports) their ports"
k -n "$namespace" rollout status "deployment/$deployment" --timeout=5m
# The release image is distroless (no shell or cat), so meridiand prints it.
token="$(k -n "$namespace" exec "deployment/$deployment" -- \
  /usr/local/bin/meridiand installation-token \
  --data-dir=/var/lib/meridian-volume/data)"
# kubectl directly, not the k function: backgrounding a function forks a
# subshell, so $! would name that subshell and cleanup's kill would leave the
# port-forward running.
kubectl --context "$MERIDIAN_KATA_CONTEXT" -n "$namespace" \
  port-forward "deployment/$deployment" "$local_port:8080" >/dev/null 2>&1 &
forward_pid=$!
base="http://127.0.0.1:$local_port"

# api METHOD PATH [JSON] calls the public API. Each mutation's
# Idempotency-Key is derived from the run, path, and body, so two different
# creates on the same path never replay each other.
api() {
  local method="$1" path="$2" body="${3:-}" key
  # --max-time: a port-forward can stall without exiting.
  local args=(-sS --fail-with-body --max-time 30 -X "$method" -H "Authorization: Bearer $token")
  if [[ "$method" != "GET" ]]; then
    [[ -n "$body" ]] || body='{}'
    key="$(printf '%s %s %s %s' "$run_id" "$method" "$path" "$body" | openssl dgst -sha256 -r | cut -c1-32)"
    args+=(-H "Content-Type: application/json" -H "Idempotency-Key: kata-$key" --data "$body")
  fi
  curl "${args[@]}" "$base$path"
}
for _ in $(seq 1 30); do
  if capabilities="$(api GET /capabilities 2>/dev/null)"; then
    break
  fi
  sleep 2
done
if [[ -z "${capabilities:-}" ]]; then
  forward_exited && forward_failure
  fail "the control plane API did not answer on the port-forward"
fi
jq -e '.isolation == "microvm"' <<<"$capabilities" >/dev/null ||
  fail "capabilities report isolation $(jq -r .isolation <<<"$capabilities"), want microvm"
pass "install (startup verified $runtime_class and the microvm floor)"

# --- Capsule helpers -----------------------------------------------------------

# wait_state CAPSULE STATE SECONDS waits for a Capsule state, failing fast on
# Failed. A failed status call is retried until the deadline (curl prints
# each error), unless the port-forward itself has exited.
wait_state() {
  local capsule="$1" want="$2" limit="$3" deadline state=""
  deadline=$((SECONDS + limit))
  while ((SECONDS < deadline)); do
    if ! state="$(api GET "/capsules/$capsule" | jq -r .state)"; then
      forward_exited && forward_failure
      state="unknown (the last API call failed)"
    fi
    if [[ "$state" == "$want" ]]; then
      return 0
    fi
    if [[ "$state" == "Failed" ]]; then
      fail "Capsule $capsule failed: $(api GET "/capsules/$capsule" | jq -r '.failure // ""')"
    fi
    sleep 3
  done
  fail "Capsule $capsule did not reach $want within ${limit}s (last state: $state)"
}
resource_version() {
  api GET "/capsules/$1" | jq -r .resourceVersion
}
lifecycle() {
  api POST "/capsules/$1/$2" "{\"expectedResourceVersion\":$(resource_version "$1")}" >/dev/null
}
# capsule_hash matches the provider's meridian.orloj.io/capsule-hash label:
# unpadded base64url of the first 16 bytes of SHA-256(Capsule ID).
capsule_hash() {
  printf '%s' "$1" | openssl dgst -sha256 -binary | head -c 16 |
    openssl base64 -A | tr '+/' '-_' | tr -d '='
}
# pod_for prints the Capsule's Running pod, or nothing. items[*], not
# items[0]: kubectl fails an out-of-range index, and set -e would then end
# the run without saying which pod was missing.
pod_for() {
  local names
  names="$(k -n "$namespace" get pods \
    -l "meridian.orloj.io/capsule-hash=$(capsule_hash "$1"),meridian.orloj.io/role=pod" \
    --field-selector=status.phase=Running -o jsonpath='{.items[*].metadata.name}')"
  echo "${names%% *}"
}
in_capsule() {
  local pod="$1"
  shift
  k -n "$namespace" exec "$pod" -c capsule -- "$@"
}
# connect POD HOST PORT prints the exit status of a 5s TCP connect from the
# Capsule: 0 connected, 124 timed out (dropped), anything else refused/failed.
connect() {
  local status=0
  in_capsule "$1" timeout 5 bash -c "exec 3<>/dev/tcp/$2/$3" >/dev/null 2>&1 || status=$?
  echo "$status"
}

# --- 2. Project and Capsule ---------------------------------------------------

project="$(api POST /projects "{\"name\":\"kata-validation-$run_id\",\"requiredIsolation\":\"microvm\"}")"
project_id="$(jq -r .id <<<"$project")"
jq -e '.requiredIsolation == "microvm"' <<<"$project" >/dev/null ||
  fail "the Project did not record requiredIsolation=microvm"
image_state="warm (image already on the node)"
if [[ "${MERIDIAN_KATA_COLD:-}" == "1" ]]; then
  image_state="cold (operator removed the image from the node first)"
fi
create_started=$SECONDS
capsule_a="$(api POST "/projects/$project_id/capsules" '{"name":"kata-a"}' | jq -r .id)"
wait_state "$capsule_a" Ready "$ready_timeout"
create_seconds=$((SECONDS - create_started))
pass "project and Capsule (create to Ready ${create_seconds}s, $image_state)"

# --- 3. The pod uses the RuntimeClass -----------------------------------------

pod_a="$(pod_for "$capsule_a")"
[[ -n "$pod_a" ]] || fail "no running pod found for Capsule $capsule_a"
pod_runtime_class="$(k -n "$namespace" get pod "$pod_a" -o jsonpath='{.spec.runtimeClassName}')"
[[ "$pod_runtime_class" == "$runtime_class" ]] ||
  fail "pod $pod_a runs RuntimeClass '$pod_runtime_class', want $runtime_class"
pass "pod uses RuntimeClass $runtime_class"

# --- 4. It is a VM ----------------------------------------------------------------

node="$(k -n "$namespace" get pod "$pod_a" -o jsonpath='{.spec.nodeName}')"
node_kernel="$(k get node "$node" -o jsonpath='{.status.nodeInfo.kernelVersion}')"
guest_kernel="$(in_capsule "$pod_a" uname -r)"
echo "node $node kernel: $node_kernel; guest kernel: $guest_kernel"
[[ "$guest_kernel" != "$node_kernel" ]] ||
  fail "the Capsule reports the node's kernel $node_kernel; it is not running in a guest VM"
hypervisor_flags="$(in_capsule "$pod_a" grep -c hypervisor /proc/cpuinfo || true)"
if [[ "${hypervisor_flags:-0}" -lt 1 ]]; then
  in_capsule "$pod_a" head -30 /proc/cpuinfo >&2 || true
  fail "/proc/cpuinfo in the Capsule has no hypervisor flag"
fi
pass "Capsule runs in a guest kernel ($hypervisor_flags vCPUs flag a hypervisor)"

# --- 5. capsuled's pod listener takes only the pinned client (ADR 0065) -----

# Ready already proves the pod-IP mutual TLS transport works: prepare runs
# over it. The Sandbox must record that transport, not port-forward.
sandbox_a="$(k -n "$namespace" get sandboxes.agents.x-k8s.io \
  -l "meridian.orloj.io/capsule-hash=$(capsule_hash "$capsule_a"),meridian.orloj.io/role=sandbox" \
  -o jsonpath='{.items[*].metadata.annotations.meridian\.orloj\.io/transport}')"
[[ "$sandbox_a" == "pod-mtls" ]] ||
  fail "the Capsule's Sandbox records transport '$sandbox_a', want pod-mtls"
pod_ip_a="$(k -n "$namespace" get pod "$pod_a" -o jsonpath='{.status.podIP}')"
if [[ "$(connect "$pod_a" "$pod_ip_a" 7777)" == "0" ]]; then
  fail "capsuled accepted a connection on the pod IP $pod_ip_a:7777; the plaintext listener must stay on loopback"
fi
# From inside the Capsule its own pod IP is reachable whatever the
# NetworkPolicy says, so this reaches the listener. Without the client
# certificate the handshake must fail: 35/56 are TLS failures; 7 means
# nothing listens on 7778 and 28 a timeout, so neither proves anything.
tls_status=0
in_capsule "$pod_a" curl -sk --max-time 5 -o /dev/null "https://$pod_ip_a:7778/healthz" || tls_status=$?
case "$tls_status" in
  0) fail "capsuled answered on $pod_ip_a:7778 without a client certificate" ;;
  7 | 28) fail "nothing answered on $pod_ip_a:7778 (curl exit $tls_status); the pod TLS listener is not up" ;;
esac
# -f: capsuled's TLS server answers plain HTTP with a 400, which plain curl
# reports as success.
plain_status=0
in_capsule "$pod_a" curl -sf --max-time 5 -o /dev/null "http://$pod_ip_a:7778/healthz" || plain_status=$?
[[ "$plain_status" != "0" ]] || fail "capsuled answered plain HTTP on $pod_ip_a:7778"
pass "capsuled keeps 7777 on loopback and refuses $pod_ip_a:7778 without the pinned client certificate (curl exit $tls_status)"

# --- 6. Pause, resume, and workspace data -----------------------------------

probe="$(openssl rand -hex 16)"
in_capsule "$pod_a" sh -c "printf %s '$probe' > /workspace/.meridian-kata-probe"
lifecycle "$capsule_a" pause
wait_state "$capsule_a" Paused 300
gone_deadline=$((SECONDS + 300))
while [[ -n "$(k -n "$namespace" get pods \
  -l "meridian.orloj.io/capsule-hash=$(capsule_hash "$capsule_a"),meridian.orloj.io/role=pod" -o name)" ]]; do
  ((SECONDS < gone_deadline)) || fail "the paused Capsule's pod (and VM) still exists"
  sleep 3
done
lifecycle "$capsule_a" resume
wait_state "$capsule_a" Ready "$ready_timeout"
pod_a="$(pod_for "$capsule_a")"
[[ -n "$pod_a" ]] || fail "no running pod found for Capsule $capsule_a after resume"
read_back="$(in_capsule "$pod_a" cat /workspace/.meridian-kata-probe)"
[[ "$read_back" == "$probe" ]] || fail "workspace data did not survive pause and resume"
pass "pause ends the VM and resume keeps the workspace PVC"

# --- 7. Egress ------------------------------------------------------------------

capsule_b="$(api POST "/projects/$project_id/capsules" '{"name":"kata-b"}' | jq -r .id)"
wait_state "$capsule_b" Ready "$ready_timeout"
pod_b="$(pod_for "$capsule_b")"
[[ -n "$pod_b" ]] || fail "no running pod found for Capsule $capsule_b"
pod_ip_b="$(k -n "$namespace" get pod "$pod_b" -o jsonpath='{.status.podIP}')"
api_ip="$(k -n default get service kubernetes -o jsonpath='{.spec.clusterIP}')"
[[ "$(connect "$pod_a" "$api_ip" 443)" != "0" ]] ||
  fail "a Capsule reached the Kubernetes API at $api_ip:443"
[[ "$(connect "$pod_a" 169.254.169.254 80)" != "0" ]] ||
  fail "a Capsule reached cloud metadata at 169.254.169.254:80"
# A CNI enforces a denied connection either by dropping the packet (a
# timeout, 124) or by rejecting it (refused, like kube-router on k3s). On
# 7777 capsuled listens only on loopback, so a packet that arrives is refused
# too; only a connection fails there.
cross="$(connect "$pod_a" "$pod_ip_b" 7777)"
[[ "$cross" != "0" ]] ||
  fail "a Capsule connected to another Capsule's pod at $pod_ip_b:7777"
# ADR 0065: the pod listener on 7778 really listens, which Capsule B shows by
# reaching it on its own pod IP (policy does not apply to a pod's own
# address). With the listener up, a drop or a refusal from Capsule A can only
# be the NetworkPolicy keeping a pod that is not the control plane away.
own_tls="$(connect "$pod_b" "$pod_ip_b" 7778)"
[[ "$own_tls" == "0" ]] ||
  fail "Capsule B cannot reach its own pod listener on $pod_ip_b:7778 (exit $own_tls), so the cross-Capsule check would prove nothing"
cross_tls="$(connect "$pod_a" "$pod_ip_b" 7778)"
case "$cross_tls" in
  0) fail "a Capsule connected to another Capsule's pod listener at $pod_ip_b:7778: Capsule ingress is not limited to the control plane; check that the CNI enforces NetworkPolicy" ;;
  124) cross_enforcement="dropped" ;;
  *) cross_enforcement="rejected" ;;
esac
pass "egress denied to the API server, cloud metadata, and another Capsule (7777 and 7778; the listening 7778 was $cross_enforcement)"

# --- 8. Delete ---------------------------------------------------------------------

for capsule in "$capsule_a" "$capsule_b"; do
  lifecycle "$capsule" delete
done
delete_deadline=$((SECONDS + 600))
for capsule in "$capsule_a" "$capsule_b"; do
  label="$(capsule_hash "$capsule")"
  while [[ -n "$(k -n "$namespace" get sandboxes.agents.x-k8s.io,pods,pvc,secrets \
    -l "meridian.orloj.io/capsule-hash=$label" -o name --ignore-not-found)" ]]; do
    ((SECONDS < delete_deadline)) || fail "Capsule $capsule left Kubernetes resources behind"
    sleep 5
  done
done
leftover_pvcs="$(k -n "$namespace" get pvc -o name | grep -vxF "persistentvolumeclaim/$deployment-data" || true)"
[[ -z "$leftover_pvcs" ]] || fail "workspace PVCs remain after delete: $leftover_pvcs"
pass "delete removes the Sandbox, pod, workspace PVC, token Secret, and TLS Secret"

# --- 9. Summary ----------------------------------------------------------------------

kata_version="${MERIDIAN_KATA_VERSION:-unknown (set MERIDIAN_KATA_VERSION)}"
instance_type="$(k get node "$node" -o jsonpath='{.metadata.labels.node\.kubernetes\.io/instance-type}')"
cat <<EOF

Kata validation summary (paste into docs/kubernetes-agentsandbox.md)
  RuntimeClass:        $runtime_class
  Handler:             $handler
  Kata version:        $kata_version
  Node:                $node${instance_type:+ ($instance_type)}
  Node kernel:         $node_kernel
  Guest kernel:        $guest_kernel
  Image:               $image_state
  Create to Ready:     ${create_seconds}s
EOF
echo "PASS: Kata microVM validation completed"
