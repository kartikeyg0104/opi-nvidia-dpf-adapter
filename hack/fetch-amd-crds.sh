#!/usr/bin/env bash
# Fetch the real AMD Pensando CRDs (and any live "golden" CRs) off lab host dh1,
# to replace the stubs in test/conformance/crds/amd/.
#
# READ-ONLY on dh1: it only lists, reads and greps. It installs nothing and
# changes nothing. Provisioning dh1 is a separate, explicit step.
#
# Requires the lab SSH key to be unlocked in your agent first:
#     ssh-add ~/.ssh/opi_lab_key
#
# Usage: hack/fetch-amd-crds.sh [outdir]      (default: ./amd-crd-harvest)
set -euo pipefail

HOST="${DH1_HOST:-root@172.22.1.1}"
OUT="${1:-amd-crd-harvest}"

# KexAlgorithms: the lab VPN has a path-MTU blackhole below its 1399 MTU, and
# the default post-quantum KEX reply is large enough to be silently dropped --
# the session then hangs at SSH2_MSG_KEX_ECDH_REPLY. curve25519 keeps it small.
SSH_OPTS=(-o KexAlgorithms=curve25519-sha256 -o ConnectTimeout=15
          -o ServerAliveInterval=15 -o StrictHostKeyChecking=accept-new)

say() { printf '\n== %s\n' "$*"; }
rsh() { ssh "${SSH_OPTS[@]}" "$HOST" "$@"; }

mkdir -p "$OUT/crds" "$OUT/golden" "$OUT/report"

say "dh1 reachability and identity"
rsh 'hostname; grep PRETTY /etc/os-release; uptime' | tee "$OUT/report/host.txt"

say "Is there a Kubernetes cluster on dh1?"
rsh 'bash -s' <<'REMOTE' | tee "$OUT/report/cluster.txt"
for b in kubectl k3s crictl docker podman kubeadm microk8s; do
  printf '%-10s %s\n' "$b" "$(command -v $b 2>/dev/null || echo '-')"
done
echo "--- systemd units ---"
systemctl list-units --type=service --all --no-pager 2>/dev/null \
  | grep -Ei 'k3s|kubelet|kube-apiserver|rke|microk8s|containerd|crio' || echo '(none)'
echo "--- kubectl reachable? ---"
if command -v kubectl >/dev/null 2>&1; then
  kubectl version -o yaml 2>&1 | head -20 || true
  kubectl get nodes -o wide 2>&1 | head -10 || true
else
  echo '(kubectl not installed)'
fi
REMOTE

say "CRDs on the cluster (if any)"
rsh 'command -v kubectl >/dev/null 2>&1 && kubectl get crd -o name 2>/dev/null || echo "(no kubectl / no cluster)"' \
  | tee "$OUT/report/all-crds.txt"

# Vendor CRDs, by API group. Cast wider than "amd": the operator may live under
# pensando.io, dsc.*, psm.* or an OEM group we have not guessed.
VENDOR_RE='amd|pensando|dsc|psm|ionic|elba'
# Portable to bash 3.2 (macOS /bin/bash): no mapfile.
CRDS=()
while IFS= read -r line; do
  [ -n "$line" ] && CRDS+=("$line")
done < <(grep -Ei "$VENDOR_RE" "$OUT/report/all-crds.txt" 2>/dev/null \
         | sed 's|^customresourcedefinition.apiextensions.k8s.io/||' || true)

if [ "${#CRDS[@]}" -gt 0 ]; then
  say "Found ${#CRDS[@]} vendor CRD(s) -- pulling manifests and live CRs"
  for c in "${CRDS[@]}"; do
    echo "  crd: $c"
    rsh "kubectl get crd '$c' -o yaml" > "$OUT/crds/$c.yaml"
    # Golden CRs: what the operator actually accepts, not just what the schema allows.
    kind=$(rsh "kubectl get crd '$c' -o jsonpath='{.spec.names.kind}'")
    rsh "kubectl get '$kind' -A -o yaml 2>/dev/null" > "$OUT/golden/$kind.yaml" || true
    echo "    golden CRs -> $OUT/golden/$kind.yaml ($(grep -c '^  - apiVersion' "$OUT/golden/$kind.yaml" 2>/dev/null || echo 0) item(s))"
  done
else
  say "No vendor CRDs on a live cluster -- searching the filesystem for operator manifests"
  rsh 'bash -s' <<'REMOTE' | tee "$OUT/report/filesystem-search.txt"
echo "--- YAML mentioning a vendor API group ---"
grep -rlEi 'kind:[[:space:]]*CustomResourceDefinition' \
     --include='*.yaml' --include='*.yml' \
     /root /home /opt /srv /etc /usr/local 2>/dev/null \
  | xargs -r grep -lEi 'amd|pensando|dsc|psm' 2>/dev/null | head -50 || echo '(none)'
echo "--- operator-ish directories ---"
ls -la /opt /srv /root 2>/dev/null | head -60
echo "--- container images cached locally (operator bundle may be in one) ---"
(crictl images 2>/dev/null || docker images 2>/dev/null || podman images 2>/dev/null) \
  | grep -Ei 'amd|pensando|dsc|psm|dpu' || echo '(none)'
echo "--- helm releases ---"
(helm list -A 2>/dev/null || echo '(helm not installed)')
REMOTE
fi

say "Harvest written to $OUT"
find "$OUT" -type f -size +0 | sort
