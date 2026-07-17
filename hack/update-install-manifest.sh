#!/usr/bin/env bash

set -euo pipefail

CONTROLLER_GEN="${1:?Usage: update-install-manifest.sh <controller-gen-binary>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

START_MARKER="# BEGIN GENERATED: controller-rbac"
END_MARKER="# END GENERATED: controller-rbac"

CHART_RBAC="internal/manifests/charts/kelos/templates/rbac.yaml"
CHART_CRD_DIR="internal/manifests/charts/kelos/charts/kelos-crds/templates"
CHART_VALIDATING_WEBHOOK="internal/manifests/charts/kelos/templates/validating-webhook.yaml"

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
    "ValidatingWebhookConfiguration kelos-validating-webhook-configuration"
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

extract_resource_doc() {
  local file="$1"
  local kind="$2"
  local name="$3"

  awk -v want_kind="${kind}" -v want_name="${name}" '
function reset_doc() {
  doc = ""
  doc_kind = ""
  meta_name = ""
  in_metadata = 0
}
function emit_if_match() {
  if (found || doc_kind != want_kind || meta_name != want_name) {
    return 0
  }
  printf "%s", doc
  found = 1
  return 1
}
BEGIN {
  reset_doc()
  found = 0
}
$0 == "---" {
  if (emit_if_match()) {
    exit
  }
  reset_doc()
  next
}
{
  doc = doc $0 ORS
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
  if (!found) {
    emit_if_match()
  }
  exit(found ? 0 : 1)
}
' "${file}"
}

escape_helm_template_placeholders() {
  local file="$1"

  sed -E 's/\{\{(\.[A-Za-z]+)\}\}/{{ "{{\1}}" }}/g' "${file}"
}

inject_chart_crd_keep_annotation() {
  local file="$1"

  awk '
BEGIN {
  inserted = 0
  in_annotations = 0
}
/^  annotations:[[:space:]]*$/ {
  print
  in_annotations = 1
  next
}
in_annotations && /controller-gen\.kubebuilder\.io\/version:/ {
  print
  print "    {{- if .Values.keep }}"
  print "    \"helm.sh/resource-policy\": keep"
  print "    {{- end }}"
  inserted = 1
  in_annotations = 0
  next
}
{
  print
}
END {
  exit(inserted ? 0 : 1)
}
' "${file}"
}

write_chart_crd_template() {
  local source="$1"
  local kind="$2"
  local name="$3"
  local dest="$4"
  local extracted="${TMPDIR}/$(basename "${dest}").extracted"
  local content="${TMPDIR}/$(basename "${dest}").content"

  extract_resource_doc "${source}" "${kind}" "${name}" >"${extracted}"
  escape_helm_template_placeholders "${extracted}" >"${content}"

  inject_chart_crd_keep_annotation "${content}" >"${dest}"
}

# inject_kelos_conversion adds the conversion webhook config and the
# cert-manager CA-injection annotation to kelos.dev CRDs that serve multiple
# versions. CRDs with a single version (e.g. WorkerPool, TaskBudget,
# TaskRecord) are left as-is since no conversion is needed. controller-gen does
# not emit spec.conversion, so it is injected here, before the chart templates
# are derived from this file.
inject_kelos_conversion() {
  local file="$1"
  local tmp="${file}.conv.tmp"

  awk '
function flush(  i, isKelos, versionCount) {
  isKelos = 0
  versionCount = 0
  for (i = 1; i <= n; i++) {
    if (buf[i] ~ /^  name: [a-z]+\.kelos\.dev[[:space:]]*$/) { isKelos = 1 }
    if (buf[i] ~ /^  (  |- )name: v1alpha/) { versionCount++ }
  }
  for (i = 1; i <= n; i++) {
    print buf[i]
    if (isKelos && versionCount > 1 && buf[i] ~ /^  annotations:[[:space:]]*$/) {
      print "    cert-manager.io/inject-ca-from: kelos-system/kelos-serving-cert"
    }
    if (isKelos && versionCount > 1 && buf[i] ~ /^  scope: /) {
      print "  conversion:"
      print "    strategy: Webhook"
      print "    webhook:"
      print "      clientConfig:"
      print "        service:"
      print "          name: kelos-webhook"
      print "          namespace: kelos-system"
      print "          path: /convert"
      print "          port: 443"
      print "      conversionReviewVersions:"
      print "        - v1"
    }
  }
  n = 0
}
/^---$/ { flush(); print; next }
{ buf[++n] = $0 }
END { flush() }
' "${file}" >"${tmp}"
  mv "${tmp}" "${file}"
}

# verify_kelos_conversion fails fast if inject_kelos_conversion did not wire
# multi-version kelos CRDs. Single-version CRDs (like WorkerPool, TaskBudget,
# TaskRecord) correctly skip conversion. The CA annotation is injected into an
# existing annotations: block, so a change in controller-gen output shape could
# silently drop it; this guard catches that instead of shipping CRDs that fail
# conversion.
verify_kelos_conversion() {
  local file="$1"
  local multi_version anno conv
  # Count CRDs that serve both v1alpha1 and v1alpha2 (need conversion).
  multi_version="$(awk '
    /^  name: [a-z]+\.kelos\.dev/ { v=0 }
    /^  (  |- )name: v1alpha/ { v++ }
    /^---/ { if (v > 1) count++; v=0 }
    END { if (v > 1) count++; print count+0 }
  ' "${file}")"
  anno="$(grep -cF 'cert-manager.io/inject-ca-from: kelos-system/kelos-serving-cert' "${file}")"
  conv="$(grep -cE '^    strategy: Webhook[[:space:]]*$' "${file}")"
  if [[ "${multi_version}" -lt 1 || "${anno}" -ne "${multi_version}" || "${conv}" -ne "${multi_version}" ]]; then
    echo "ERROR: kelos CRD conversion wiring incomplete in ${file}: multi_version=${multi_version} ca-annotations=${anno} conversion-blocks=${conv}" >&2
    exit 1
  fi
}

generate_chart_crd_templates() {
  local source="$1"

  mkdir -p "${CHART_CRD_DIR}"

  write_chart_crd_template "${source}" "CustomResourceDefinition" "agentconfigs.kelos.dev" "${CHART_CRD_DIR}/agentconfig-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "sessions.kelos.dev" "${CHART_CRD_DIR}/session-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "taskbudgets.kelos.dev" "${CHART_CRD_DIR}/taskbudget-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "taskrecords.kelos.dev" "${CHART_CRD_DIR}/taskrecord-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "tasks.kelos.dev" "${CHART_CRD_DIR}/task-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "taskspawners.kelos.dev" "${CHART_CRD_DIR}/taskspawner-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "workerpools.kelos.dev" "${CHART_CRD_DIR}/workerpool-crd.yaml"
  write_chart_crd_template "${source}" "CustomResourceDefinition" "workspaces.kelos.dev" "${CHART_CRD_DIR}/workspace-crd.yaml"
}

inject_validating_webhook_ca_annotation() {
  local source="$1"
  local dest="$2"

  awk '
BEGIN {
  in_metadata = 0
  inserted = 0
}
/^metadata:[[:space:]]*$/ {
  print
  print "  annotations:"
  print "    cert-manager.io/inject-ca-from: kelos-system/kelos-serving-cert"
  in_metadata = 1
  inserted = 1
  next
}
in_metadata && /^  name:/ {
  print
  in_metadata = 0
  next
}
{
  print
}
END {
  exit(inserted ? 0 : 1)
}
' "${source}" >"${dest}"
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
inject_kelos_conversion internal/manifests/install-crd.yaml
verify_kelos_conversion internal/manifests/install-crd.yaml
generate_chart_crd_templates "internal/manifests/install-crd.yaml"

RBAC_FILE="${TMPDIR}/rbac.yaml"
GOCACHE="${TMPDIR}/go-build-cache" "${CONTROLLER_GEN}" \
  rbac:roleName=kelos-controller-role \
  paths="./..." \
  output:rbac:stdout >"${RBAC_FILE}"

WEBHOOK_FILE="${TMPDIR}/validating-webhook.yaml"
GOCACHE="${TMPDIR}/go-build-cache" "${CONTROLLER_GEN}" \
  webhook \
  paths="./..." \
  output:webhook:stdout >"${WEBHOOK_FILE}"
inject_validating_webhook_ca_annotation "${WEBHOOK_FILE}" "${CHART_VALIDATING_WEBHOOK}"

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
