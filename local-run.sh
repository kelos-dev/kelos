#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind}"
REGISTRY="${REGISTRY:-gjkim42}"
LOCAL_IMAGE_TAG="${LOCAL_IMAGE_TAG:-local-dev}"
VERSION_PKG="github.com/axon-core/axon/internal/version.Version"

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
  "${REGISTRY}/axon-controller:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/axon-spawner:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/axon-token-refresher:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/claude-code:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/codex:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/gemini:${LOCAL_IMAGE_TAG}"
  "${REGISTRY}/opencode:${LOCAL_IMAGE_TAG}"
)

for image in "${images[@]}"; do
  kind load docker-image --name "${KIND_CLUSTER_NAME}" "${image}"
done

go install -ldflags "-X ${VERSION_PKG}=${LOCAL_IMAGE_TAG}" github.com/axon-core/axon/cmd/axon

axon install
kubectl patch deployment/axon-controller-manager \
  -n axon-system \
  --type=strategic \
  -p "$(
    cat <<EOF
{
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {
            "name": "manager",
            "imagePullPolicy": "IfNotPresent",
            "args": [
              "--leader-elect",
              "--claude-code-image=${REGISTRY}/claude-code:${LOCAL_IMAGE_TAG}",
              "--codex-image=${REGISTRY}/codex:${LOCAL_IMAGE_TAG}",
              "--gemini-image=${REGISTRY}/gemini:${LOCAL_IMAGE_TAG}",
              "--opencode-image=${REGISTRY}/opencode:${LOCAL_IMAGE_TAG}",
              "--spawner-image=${REGISTRY}/axon-spawner:${LOCAL_IMAGE_TAG}",
              "--token-refresher-image=${REGISTRY}/axon-token-refresher:${LOCAL_IMAGE_TAG}"
            ]
          }
        ]
      }
    }
  }
}
EOF
  )"
kubectl rollout restart deployment/axon-controller-manager -n axon-system
kubectl rollout status deployment/axon-controller-manager -n axon-system
