# Version information
include Makefile.version

# License targets
include license.mk

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
BUILD_DIR := ${PROJECT_DIR}/build

REGISTRY ?= ghcr.io/mellanox
IMAGE_TAG_BASE ?= $(REGISTRY)/network-operator-init-container
IMAGE_TAG ?= latest

# Image URL to use all building/pushing image targets
IMG ?= $(IMAGE_TAG_BASE):$(IMAGE_TAG)
DOCKERFILE ?= Dockerfile

# which container engine to use for image build/push
DOCKER_CMD ?= docker

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

TARGET_OS ?= $(shell go env GOOS)
TARGET_ARCH ?= $(shell go env GOARCH)

# Options for go build command
GO_BUILD_OPTS ?= CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH)
# Linker flags for go build command
GO_LDFLAGS = $(VERSION_LDFLAGS)


PKGS = $(or $(PKG),$(shell cd $(PROJECT_DIR) && go list ./...))

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean
clean: ## Remove downloaded tools and compiled binaries
	@rm -rf $(LOCALBIN)
	@rm -rf $(BUILD_DIR)

##@ Development

.PHONY: lint
lint: golangci-lint ## Lint code.
	$(GOLANGCILINT) run --timeout 10m

COVERAGE_MODE = atomic
COVER_PROFILE = $(PROJECT_DIR)/cover.out

.PHONY: unit-test
unit-test: envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test -covermode=$(COVERAGE_MODE) -coverprofile=$(COVER_PROFILE) $(PKGS)

.PHONY: test
test: lint unit-test

##@ Build

.PHONY: build
build: ## Build binary
	$(GO_BUILD_OPTS) go build -ldflags $(GO_LDFLAGS) -o $(BUILD_DIR)/network-operator-init-container ./cmd/network-operator-init-container/main.go

.PHONY: docker-build
docker-build: ## Build docker image with ipam binaries
	$(DOCKER_CMD) build -t $(IMG) -f $(DOCKERFILE) .

.PHONY: docker-push
docker-push: ## Push docker image with ipam binaries
	$(DOCKER_CMD) push $(IMG)

## Location to install dependencies to
LOCALBIN ?= $(PROJECT_DIR)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

##@ Tools

## Tool Binaries
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCILINT ?= $(LOCALBIN)/golangci-lint

## Tool Versions
GOLANGCILINT_VERSION ?= v1.63.4

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: golangci-lint
golangci-lint: $(GOLANGCILINT) ## Download golangci-lint locally if necessary.
$(GOLANGCILINT): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCILINT_VERSION)
