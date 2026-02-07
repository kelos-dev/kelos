#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

make image
make push
go install github.com/gjkim42/axon/cmd/axon

kubectl apply -f install-crd.yaml
kubectl apply -f install.yaml
kubectl rollout restart deployment/axon-controller-manager -n axon-system
kubectl rollou status deployment/axon-controller-manager -n axon-system
