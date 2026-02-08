#!/usr/bin/env bash

# Verify that generated files and formatting are up to date without relying
# on git status.  The script snapshots every file that the generators touch
# into a temporary directory, runs the generators in-place, diffs the result
# against the snapshot, and then restores the original files so the working
# tree is left untouched.

set -euo pipefail

CONTROLLER_GEN="${1:?Usage: verify.sh <controller-gen-binary>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

# Files explicitly written by the update / verify pipeline.
GENERATED_FILES=(
  install-crd.yaml
  internal/manifests/install-crd.yaml
  internal/manifests/install.yaml
  api/v1alpha1/zz_generated.deepcopy.go
)

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# ---------------------------------------------------------------------------
# 1. Snapshot files that will be regenerated.
# ---------------------------------------------------------------------------
for f in "${GENERATED_FILES[@]}"; do
  if [[ -f "${f}" ]]; then
    mkdir -p "${TMPDIR}/$(dirname "${f}")"
    cp "${f}" "${TMPDIR}/${f}"
  fi
done

# ---------------------------------------------------------------------------
# 2. Run the generators (same commands as `make update`).
# ---------------------------------------------------------------------------
${CONTROLLER_GEN} object:headerFile="hack/boilerplate.go.txt" paths="./..."
${CONTROLLER_GEN} crd paths="./..." output:crd:stdout > install-crd.yaml
cp install-crd.yaml internal/manifests/install-crd.yaml
cp install.yaml internal/manifests/install.yaml

# ---------------------------------------------------------------------------
# 3. Compare generated files and restore originals.
# ---------------------------------------------------------------------------
ret=0
for f in "${GENERATED_FILES[@]}"; do
  if [[ -f "${TMPDIR}/${f}" ]]; then
    if ! diff -q "${TMPDIR}/${f}" "${f}" >/dev/null 2>&1; then
      echo "ERROR: ${f} is out of date"
      diff -u "${TMPDIR}/${f}" "${f}" || true
      ret=1
    fi
    # Restore the original so we don't modify the working tree.
    cp "${TMPDIR}/${f}" "${f}"
  elif [[ -f "${f}" ]]; then
    echo "ERROR: ${f} needs to be generated (file did not exist before)"
    # Remove the newly created file to leave the working tree untouched.
    rm "${f}"
    ret=1
  fi
done

# ---------------------------------------------------------------------------
# 4. Verify go fmt (use gofmt -l to list, without modifying files).
# ---------------------------------------------------------------------------
bad_fmt=$(gofmt -l . 2>&1 | grep -v '^vendor/' || true)
if [[ -n "${bad_fmt}" ]]; then
  echo "ERROR: The following files are not properly formatted:"
  echo "${bad_fmt}"
  ret=1
fi

# ---------------------------------------------------------------------------
# 5. Verify go mod tidy (the -diff flag exits non-zero if changes are needed
#    without modifying go.mod / go.sum).
# ---------------------------------------------------------------------------
if ! go mod tidy -diff >/dev/null 2>&1; then
  echo "ERROR: go.mod/go.sum are out of date. Run 'go mod tidy'"
  go mod tidy -diff 2>&1 || true
  ret=1
fi

if [[ ${ret} -ne 0 ]]; then
  echo ""
  echo "Generated files are out of date. Run 'make update' and commit the changes."
  exit 1
fi

echo "Verification passed"
