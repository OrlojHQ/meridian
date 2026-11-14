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

rbac_rules() {
  awk '$0 == "rules:" {keep=1; next} $0 == "---" {keep=0} keep' "$1"
}
default_rbac="$(rbac_rules "$rendered")"

# Single-user defaults must not add sign-in flags or a Secret mount.
if grep -Eq -- '--oidc-|--public-url=|--audit-retention=|name: oidc-client-secret|/etc/meridian/oidc' "$rendered"; then
  echo "default chart enabled team sign-in or audit retention" >&2
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

if "$HELM" template unsafe "$CHART" --kube-version 1.36.0 --set service.type=LoadBalancer >/dev/null 2>&1; then
  echo "values schema accepted a public Service" >&2
  exit 1
fi
if "$HELM" template unsafe "$CHART" --kube-version 1.36.0 \
  --set containerSecurityContext.allowPrivilegeEscalation=true >/dev/null 2>&1; then
  echo "values schema accepted privilege escalation" >&2
  exit 1
fi

# OIDC credentials are a projected file on the control plane, never inline
# values, an environment variable, a Capsule mount, or new Secret RBAC.
"$HELM" template team "$CHART" --kube-version 1.36.0 --namespace meridian \
  --set publicUrl=https://meridian.example \
  --set auditRetention=2160h \
  --set oidc.issuer=https://id.example/realms/meridian \
  --set oidc.clientId=meridian \
  --set oidc.clientSecret.existingSecret=meridian-oidc \
  --set oidc.clientSecret.key=clientSecret \
  --set-string 'oidc.requiredGroup=Meridian team' \
  --set oidc.groupsClaim=team_groups >"$rendered"
for expected in \
  '"--public-url=https://meridian.example"' \
  '"--audit-retention=2160h"' \
  '"--oidc-issuer=https://id.example/realms/meridian"' \
  '"--oidc-client-id=meridian"' \
  '--oidc-client-secret-file=/etc/meridian/oidc/client-secret' \
  '"--oidc-required-group=Meridian team"' \
  '"--oidc-groups-claim=team_groups"' \
  'secretName: "meridian-oidc"' \
  'key: "clientSecret"' \
  'path: client-secret' \
  'defaultMode: 0440' \
  'mountPath: /etc/meridian/oidc' \
  'readOnly: true'; do
  if ! grep -Fq -- "$expected" "$rendered"; then
    echo "team chart omitted $expected" >&2
    exit 1
  fi
done
if grep -Eq '^kind: Secret$|secretKeyRef:' "$rendered"; then
  echo "team chart added inline secrets or secret environment variables" >&2
  exit 1
fi
if [[ "$(rbac_rules "$rendered")" != "$default_rbac" ]]; then
  echo "team chart changed the default RBAC permissions" >&2
  exit 1
fi
if awk '/initContainers:/ {init=1} /^      containers:/ {init=0} init' "$rendered" | grep -q 'oidc-client-secret'; then
  echo "init container must not receive the OIDC secret" >&2
  exit 1
fi

# Public clients may omit a client secret; unset optional flags stay absent.
"$HELM" template team "$CHART" --kube-version 1.36.0 \
  --set publicUrl=https://meridian.example \
  --set oidc.issuer=https://id.example/realms/meridian \
  --set oidc.clientId=meridian >"$rendered"
if grep -Eq -- '--oidc-client-secret-file|--oidc-required-group|--oidc-groups-claim|--audit-retention|name: oidc-client-secret' "$rendered"; then
  echo "team chart rendered unset optional flags or a secret mount" >&2
  exit 1
fi

# Retention works independently of OIDC and preserves an explicit zero.
"$HELM" template single "$CHART" --kube-version 1.36.0 --set-string auditRetention=0 >"$rendered"
grep -Fq -- '"--audit-retention=0"' "$rendered"

for invalid in \
  'oidc.issuer=http://id.example' \
  'publicUrl=http://meridian.example' \
  'auditRetention=-1h' \
  'auditRetention=90days' \
  'oidc.clientSecret.value=plaintext-canary' \
  'oidc.clientSecret.key=' \
  'oidc.clientSecret.existingSecret=meridian-oidc' \
  'oidc.clientId=meridian' \
  'oidc.issuer=https://id.example'; do
  if "$HELM" template invalid "$CHART" --kube-version 1.36.0 --set-string "$invalid" >/dev/null 2>&1; then
    echo "chart accepted invalid or incomplete sign-in setting ${invalid%%=*}" >&2
    exit 1
  fi
done
if "$HELM" template invalid "$CHART" --kube-version 1.36.0 --set oidc.issuer=https://id.example --set oidc.clientId=meridian >/dev/null 2>&1; then
  echo "chart accepted OIDC without a public browser origin" >&2
  exit 1
fi
