#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind}"
REGISTRY="${REGISTRY:-gjkim42}"
LOCAL_IMAGE_TAG="${LOCAL_IMAGE_TAG:-local-dev}"
if ! command -v kind >/dev/null 2>&1; then
  echo "Kind CLI not found in PATH" >&2
  exit 1
fi

if ! kind get clusters | grep -Fxq "${KIND_CLUSTER_NAME}"; then
  echo "Kind cluster ${KIND_CLUSTER_NAME} not found" >&2
  exit 1
fi

make image REGISTRY="${REGISTRY}" VERSION="${LOCAL_IMAGE_TAG}"

images=(
  "${REGISTRY}/kelos-controller:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-spawner:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/kelos-token-refresher:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/claude-code:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/codex:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/gemini:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/opencode:${LOCAL_IMAGE_TAG}"
)

for image in "${images[@]}"; do
  kind load docker-image --name "${KIND_CLUSTER_NAME}" "${image}"
done

go install github.com/kelos-dev/kelos/cmd/kelos

kelos install --version "${LOCAL_IMAGE_TAG}" --image-pull-policy IfNotPresent
kubectl rollout restart deployment/kelos-controller-manager -n kelos-system
kubectl rollout status deployment/kelos-controller-manager -n kelos-system
