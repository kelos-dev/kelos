#!/usr/bin/env bash

set -euo pipefail

CONTROLLER_GEN="${1:?Usage: update-install-manifest.sh <controller-gen-binary>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

START_MARKER="# BEGIN GENERATED: controller-rbac"
END_MARKER="# END GENERATED: controller-rbac"

CHART_RBAC="internal/manifests/charts/kelos/templates/rbac.yaml"

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

validate_chart_resources() {
  local dir="$1"
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
    local found=0
    for f in "${dir}"/templates/*.yaml; do
      if has_resource "${f}" "${kind}" "${name}"; then
        found=1
        break
      fi
    done
    if [[ "${found}" -eq 0 ]]; then
      echo "ERROR: chart templates missing required resource ${kind}/${name}"
      exit 1
    fi
  done
}

if [[ "$(grep -Fxc "${START_MARKER}" "${CHART_RBAC}")" -ne 1 ]]; then
  echo "ERROR: ${CHART_RBAC} must contain exactly one '${START_MARKER}' marker"
  exit 1
fi

if [[ "$(grep -Fxc "${END_MARKER}" "${CHART_RBAC}")" -ne 1 ]]; then
  echo "ERROR: ${CHART_RBAC} must contain exactly one '${END_MARKER}' marker"
  exit 1
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Regenerate CRDs before syncing manifests.
"${CONTROLLER_GEN}" crd paths="./..." output:crd:stdout >internal/manifests/install-crd.yaml

RBAC_FILE="${TMPDIR}/rbac.yaml"
GOCACHE="${TMPDIR}/go-build-cache" "${CONTROLLER_GEN}" \
  rbac:roleName=kelos-controller-role \
  paths="./..." \
  output:rbac:stdout >"${RBAC_FILE}"

# Splice generated RBAC into the chart's rbac.yaml template.
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
' "${CHART_RBAC}" >"${TMPDIR}/rbac.yaml.new"

mv "${TMPDIR}/rbac.yaml.new" "${CHART_RBAC}"

validate_chart_resources "internal/manifests/charts/kelos"
