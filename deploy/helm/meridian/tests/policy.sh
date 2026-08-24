#!/usr/bin/env bash
set -euo pipefail

HELM="${HELM:-helm}"
CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

"$HELM" lint "$CHART" --strict
"$HELM" template policy "$CHART" \
  --namespace meridian \
  --kube-version 1.36.0 \
  --set 'networkPolicy.kubernetesAPIServerCIDRs[0]=10.96.0.1/32' \
  --set 'image.digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  --set 'capsuleImage.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  >"$rendered"

rg -q '^kind: Role$' "$rendered"
rg -q '^kind: NetworkPolicy$' "$rendered"
rg -q 'readOnlyRootFilesystem: true' "$rendered"
rg -q 'allowPrivilegeEscalation: false' "$rendered"
rg -q 'runAsNonRoot: true' "$rendered"
rg -q 'seccompProfile:' "$rendered"
rg -q 'resources: \["pods/portforward"\]' "$rendered"
rg -q 'apiGroups: \["agents.x-k8s.io"\]' "$rendered"
rg -q 'policyTypes: \[Ingress, Egress\]' "$rendered"
if rg -q 'resources: \["pods/exec"\]|verbs: \[[^]]*"list"[^]]*\].*secrets' "$rendered"; then
  echo "chart rendered forbidden exec or Secret-list authority" >&2
  exit 1
fi
if rg -q 'snapshot.storage.k8s.io' "$rendered"; then
  echo "unsupported CSI Moments must not receive RBAC" >&2
  exit 1
fi

if rg -q '^kind: (ClusterRole|ClusterRoleBinding|CustomResourceDefinition|Ingress)$' "$rendered"; then
  echo "chart rendered forbidden cluster-scoped or public-ingress resources" >&2
  exit 1
fi
if rg -q 'privileged: true|hostNetwork: true|hostPID: true|hostIPC: true|hostPath:' "$rendered"; then
  echo "chart rendered forbidden pod privileges" >&2
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
