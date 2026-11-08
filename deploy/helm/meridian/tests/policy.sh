#!/usr/bin/env bash
set -euo pipefail

HELM="${HELM:-helm}"
CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

digest_args=(
  --set 'image.digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  --set 'capsuleImage.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
)

"$HELM" lint "$CHART" --strict
"$HELM" template policy "$CHART" \
  --namespace meridian \
  --kube-version 1.36.0 \
  --set 'networkPolicy.kubernetesAPIServerCIDRs[0]=10.96.0.1/32' \
  "${digest_args[@]}" \
  >"$rendered"

grep -Eq '^kind: Role$' "$rendered"
grep -Eq '^kind: NetworkPolicy$' "$rendered"
grep -Eq 'readOnlyRootFilesystem: true' "$rendered"
grep -Eq 'allowPrivilegeEscalation: false' "$rendered"
grep -Eq 'runAsNonRoot: true' "$rendered"
grep -Eq 'seccompProfile:' "$rendered"
grep -Eq 'resources: \["pods/portforward"\]' "$rendered"
grep -Eq 'apiGroups: \["agents.x-k8s.io"\]' "$rendered"
grep -Eq -- '--official-pack-tag=dev' "$rendered"
grep -Eq 'policyTypes: \[Ingress, Egress\]' "$rendered"
grep -Eq 'app.kubernetes.io/component: control-plane' "$rendered"
grep -Eq 'port: 8080' "$rendered"
if grep -Eq 'resources: \["pods/exec"\]|verbs: \[[^]]*"list"[^]]*\].*secrets' "$rendered"; then
  echo "chart rendered forbidden exec or Secret-list authority" >&2
  exit 1
fi
if grep -Eq 'snapshot.storage.k8s.io' "$rendered"; then
  echo "unsupported CSI Moments must not receive RBAC" >&2
  exit 1
fi

if grep -Eq '^kind: (ClusterRole|ClusterRoleBinding|CustomResourceDefinition|Ingress)$' "$rendered"; then
  echo "chart rendered forbidden cluster-scoped or public-ingress resources" >&2
  exit 1
fi
if grep -Eq 'privileged: true|hostNetwork: true|hostPID: true|hostIPC: true|hostPath:' "$rendered"; then
  echo "chart rendered forbidden pod privileges" >&2
  exit 1
fi

if grep -Eq '0\.0\.0\.0/0|::/0|docker\.sock|169\.254\.169\.254' "$rendered"; then
  echo "chart rendered unrelated egress, metadata access, or a Docker socket" >&2
  exit 1
fi

extract_policy() {
  local name="$1" file="$2"
  awk -v want="$name" '
    $0 == "---" {
      if (keep) printf "%s", buf
      buf=""; keep=0; next
    }
    { buf = buf $0 "\n" }
    $0 ~ "^  name: " want "$" { keep=1 }
    END { if (keep) printf "%s", buf }
  ' "$file"
}

capsules="$(extract_policy "policy-meridian-capsules" "$rendered")"
control="$(extract_policy "policy-meridian-control-plane" "$rendered")"
if [[ -z "$capsules" || -z "$control" ]]; then
  echo "failed to extract Capsule or control-plane NetworkPolicy" >&2
  exit 1
fi
if ! grep -Eq 'app.kubernetes.io/component: control-plane' <<<"$capsules"; then
  echo "Capsule NetworkPolicy must allow in-namespace provider-gateway egress" >&2
  exit 1
fi
if grep -Eq '10\.96\.0\.1/32' <<<"$capsules"; then
  echo "API-server CIDRs must not apply to Capsules" >&2
  exit 1
fi
if ! grep -Eq '10\.96\.0\.1/32' <<<"$control"; then
  echo "control-plane NetworkPolicy must keep API-server egress" >&2
  exit 1
fi

provider_rendered="$(mktemp)"
trap 'rm -f "$rendered" "$provider_rendered"' EXIT
"$HELM" template policy "$CHART" \
  --namespace meridian \
  --kube-version 1.36.0 \
  --set 'networkPolicy.kubernetesAPIServerCIDRs[0]=10.96.0.1/32' \
  --set 'networkPolicy.providerEgressCIDRs[0]=192.0.2.1/32' \
  --set 'networkPolicy.capsuleEgressCIDRs[0]=198.51.100.0/24' \
  --set providerGatewayURL=https://meridian.example \
  "${digest_args[@]}" \
  >"$provider_rendered"

if grep -Eq '0\.0\.0\.0/0|::/0|docker\.sock|169\.254\.169\.254' "$provider_rendered"; then
  echo "provider egress render opened unrelated destinations or a Docker socket" >&2
  exit 1
fi

capsules="$(extract_policy "policy-meridian-capsules" "$provider_rendered")"
control="$(extract_policy "policy-meridian-control-plane" "$provider_rendered")"
if ! grep -Eq '192\.0\.2\.1/32' <<<"$control"; then
  echo "control-plane NetworkPolicy must allow provider CIDRs on TCP 443" >&2
  exit 1
fi
if grep -Eq '192\.0\.2\.1/32' <<<"$capsules"; then
  echo "provider CIDRs must not be granted to Capsules" >&2
  exit 1
fi
if ! grep -Eq '198\.51\.100\.0/24' <<<"$capsules"; then
  echo "Capsule NetworkPolicy must keep operator package/origin CIDRs" >&2
  exit 1
fi
if grep -Eq '198\.51\.100\.0/24' <<<"$control"; then
  echo "Capsule package/origin CIDRs must not apply to the control plane" >&2
  exit 1
fi

if "$HELM" template unsafe "$CHART" --set service.type=LoadBalancer >/dev/null 2>&1; then
  echo "values schema accepted a public Service" >&2
  exit 1
fi
if "$HELM" template unsafe "$CHART" \
  --set containerSecurityContext.allowPrivilegeEscalation=true >/dev/null 2>&1; then
  echo "values schema accepted privilege escalation" >&2
  exit 1
fi
