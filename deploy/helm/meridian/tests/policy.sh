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

if "$HELM" template unsafe "$CHART" --set service.type=LoadBalancer >/dev/null 2>&1; then
  echo "values schema accepted a public Service" >&2
  exit 1
fi
if "$HELM" template unsafe "$CHART" \
  --set containerSecurityContext.allowPrivilegeEscalation=true >/dev/null 2>&1; then
  echo "values schema accepted privilege escalation" >&2
  exit 1
fi
