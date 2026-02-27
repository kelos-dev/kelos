#!/usr/bin/env bash

set -euo pipefail

CONTROLLER_GEN="${1:?Usage: update-install-manifest.sh <controller-gen-binary>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

START_MARKER="# BEGIN GENERATED: controller-rbac"
END_MARKER="# END GENERATED: controller-rbac"

has_resource() {
  local file="$1"
  local kind="$2"
  local name="$3"

  awk -v want_kind="${kind}" -v want_name="${name}" '
function reset_doc() {
  doc_kind = ""
  meta_name = ""
  in_metadata = 0
}
BEGIN {
  reset_doc()
  found = 0
}
$0 == "---" {
  if (doc_kind == want_kind && meta_name == want_name) {
    found = 1
    exit
  }
  reset_doc()
  next
}
$0 ~ /^kind:[[:space:]]+/ {
  doc_kind = $2
  next
}
$0 ~ /^metadata:[[:space:]]*$/ {
  in_metadata = 1
  next
}
in_metadata {
  if ($0 ~ /^[^[:space:]]/) {
    in_metadata = 0
    next
  }
  if ($0 ~ /^[[:space:]]+name:[[:space:]]+/) {
    meta_name = $2
    gsub(/"/, "", meta_name)
    in_metadata = 0
  }
}
END {
  if (doc_kind == want_kind && meta_name == want_name) {
    found = 1
  }
  exit(found ? 0 : 1)
}
' "${file}"
}

validate_manifest_resources() {
  local file="$1"
  local -a required=(
    "Namespace kelos-system"
    "ServiceAccount kelos-controller"
    "ClusterRole kelos-controller-role"
    "ClusterRole kelos-spawner-role"
    "ClusterRoleBinding kelos-controller-rolebinding"
    "Role kelos-leader-election-role"
    "RoleBinding kelos-leader-election-rolebinding"
    "Deployment kelos-controller-manager"
  )

  local entry
  for entry in "${required[@]}"; do
    local kind="${entry%% *}"
    local name="${entry#* }"
    if ! has_resource "${file}" "${kind}" "${name}"; then
      echo "ERROR: install.yaml missing required resource ${kind}/${name}"
      exit 1
    fi
  done
}

if [[ "$(grep -Fxc "${START_MARKER}" install.yaml)" -ne 1 ]]; then
  echo "ERROR: install.yaml must contain exactly one '${START_MARKER}' marker"
  exit 1
fi

if [[ "$(grep -Fxc "${END_MARKER}" install.yaml)" -ne 1 ]]; then
  echo "ERROR: install.yaml must contain exactly one '${END_MARKER}' marker"
  exit 1
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Regenerate CRDs before syncing manifests.
"${CONTROLLER_GEN}" crd paths="./..." output:crd:stdout >install-crd.yaml

RBAC_FILE="${TMPDIR}/rbac.yaml"
GOCACHE="${TMPDIR}/go-build-cache" "${CONTROLLER_GEN}" \
  rbac:roleName=kelos-controller-role \
  paths="./..." \
  output:rbac:stdout >"${RBAC_FILE}"

awk -v start="${START_MARKER}" -v end="${END_MARKER}" -v rbac="${RBAC_FILE}" '
$0 == start {
  print
  while ((getline line < rbac) > 0) {
    print line
  }
  close(rbac)
  in_generated_block = 1
  next
}
$0 == end {
  in_generated_block = 0
  print
  next
}
!in_generated_block {
  print
}
' install.yaml >"${TMPDIR}/install.yaml"

validate_manifest_resources "${TMPDIR}/install.yaml"

mv "${TMPDIR}/install.yaml" install.yaml
cp install-crd.yaml internal/manifests/install-crd.yaml
cp install.yaml internal/manifests/install.yaml
