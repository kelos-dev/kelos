# Image configuration
REGISTRY ?= gjkim42
VERSION ?= latest
IMAGE_DIRS ?= cmd/axon-controller cmd/axon-spawner cmd/axon-token-refresher claude-code codex gemini opencode

# Version injection for the axon CLI – only set ldflags when an explicit
# version is given so that dev builds fall through to runtime/debug info.
VERSION_PKG = github.com/axon-core/axon/internal/version
ifneq ($(VERSION),latest)
LDFLAGS ?= -X $(VERSION_PKG).Version=$(VERSION)
endif

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: test
test: ## Run unit tests.
	go test $$(go list ./... | grep -v /test/) --skip=E2E

.PHONY: test-integration
test-integration: envtest ## Run integration tests (envtest).
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./test/integration/... -v

.PHONY: test-e2e
test-e2e: ginkgo ## Run e2e tests (requires cluster and CLAUDE_CODE_OAUTH_TOKEN).
	$(GINKGO) -v --timeout 10m ./test/e2e/...

.PHONY: update
update: controller-gen yamlfmt shfmt ## Run all generators and formatters.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	hack/update-install-manifest.sh $(CONTROLLER_GEN)
	hack/update-codegen.sh
	go fmt ./...
	go mod tidy
	$(YAMLFMT) .
	find . -name '*.sh' -not -path './bin/*' -exec $(SHFMT) -w -i 2 -ci {} +

.PHONY: verify
verify: controller-gen yamlfmt shfmt ## Verify everything is up-to-date and correct.
	@hack/verify.sh $(CONTROLLER_GEN) $(YAMLFMT) $(SHFMT)
	go vet ./...

##@ Build

.PHONY: build
build: ## Build binaries (use WHAT=cmd/axon to build specific binary).
	@for dir in $$(go list ./$(or $(WHAT),cmd/...)); do \
		bin_name=$$(basename $$dir); \
		CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$$bin_name $$dir; \
	done

.PHONY: run
run: ## Run a controller from your host.
	go run ./cmd/axon-controller

.PHONY: image
image: ## Build docker images (use WHAT to build specific image).
	@for dir in $(filter cmd/%,$(or $(WHAT),$(IMAGE_DIRS))); do \
		GOOS=linux GOARCH=amd64 $(MAKE) build WHAT=$$dir; \
	done
	@GOOS=linux GOARCH=amd64 $(MAKE) build WHAT=cmd/axon-capture
	@for dir in $(or $(WHAT),$(IMAGE_DIRS)); do \
		docker build -t $(REGISTRY)/$$(basename $$dir):$(VERSION) -f $$dir/Dockerfile .; \
	done

.PHONY: push
push: ## Push docker images (use WHAT to push specific image).
	@for dir in $(or $(WHAT),$(IMAGE_DIRS)); do \
		docker push $(REGISTRY)/$$(basename $$dir):$(VERSION); \
	done

RELEASE_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: release-binaries
release-binaries: ## Cross-compile CLI binaries for release and generate checksums.
	@for platform in $(RELEASE_PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		GOOS=$$os GOARCH=$$arch $(MAKE) build WHAT=cmd/axon; \
		mv bin/axon "bin/axon-$${os}-$${arch}"; \
	done
	@cd bin && sha256sum axon-* > checksums.txt

.PHONY: clean
clean: ## Clean build artifacts.
	rm -rf bin/
	rm -f cover.out

##@ Tool Dependencies

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GINKGO ?= $(LOCALBIN)/ginkgo
YAMLFMT ?= $(LOCALBIN)/yamlfmt
SHFMT ?= $(LOCALBIN)/shfmt

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest

.PHONY: ginkgo
ginkgo: $(GINKGO)
$(GINKGO): $(LOCALBIN)
	test -s $(LOCALBIN)/ginkgo || GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo

.PHONY: yamlfmt
yamlfmt: $(YAMLFMT)
$(YAMLFMT): $(LOCALBIN)
	test -s $(LOCALBIN)/yamlfmt || GOBIN=$(LOCALBIN) go install github.com/google/yamlfmt/cmd/yamlfmt

.PHONY: shfmt
shfmt: $(SHFMT)
$(SHFMT): $(LOCALBIN)
	test -s $(LOCALBIN)/shfmt || GOBIN=$(LOCALBIN) go install mvdan.cc/sh/v3/cmd/shfmt
