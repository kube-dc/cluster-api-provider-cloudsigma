.PHONY: build build-on-cloudsigma clean validate init

# Default Kubernetes version
K8S_VERSION ?= 1.34.1
UBUNTU_VERSION ?= 24.04

# CloudSigma credentials
CLOUDSIGMA_USERNAME ?= $(shell echo $$CLOUDSIGMA_USERNAME)
CLOUDSIGMA_PASSWORD ?= $(shell echo $$CLOUDSIGMA_PASSWORD)
CLOUDSIGMA_REGION ?= $(shell echo $$CLOUDSIGMA_REGION)

init:
	@echo "==> Initializing Packer"
	packer init ubuntu-k8s.pkr.hcl

validate: init
	@echo "==> Validating Packer template"
	packer validate \
		-var "k8s_version=$(K8S_VERSION)" \
		-var "ubuntu_version=$(UBUNTU_VERSION)" \
		ubuntu-k8s.pkr.hcl

build: validate
	@echo "==> Building Ubuntu $(UBUNTU_VERSION) with Kubernetes $(K8S_VERSION) locally"
	@if [ -z "$(CLOUDSIGMA_USERNAME)" ] || [ -z "$(CLOUDSIGMA_PASSWORD)" ] || [ -z "$(CLOUDSIGMA_REGION)" ]; then \
		echo "Error: CLOUDSIGMA_USERNAME, CLOUDSIGMA_PASSWORD, and CLOUDSIGMA_REGION must be set"; \
		exit 1; \
	fi
	packer build \
		-var "k8s_version=$(K8S_VERSION)" \
		-var "ubuntu_version=$(UBUNTU_VERSION)" \
		-var "cloudsigma_username=$(CLOUDSIGMA_USERNAME)" \
		-var "cloudsigma_password=$(CLOUDSIGMA_PASSWORD)" \
		-var "cloudsigma_region=$(CLOUDSIGMA_REGION)" \
		ubuntu-k8s.pkr.hcl

build-on-cloudsigma:
	@echo "==> Building Ubuntu $(UBUNTU_VERSION) with Kubernetes $(K8S_VERSION) directly on CloudSigma"
	@if [ -z "$(CLOUDSIGMA_USERNAME)" ] || [ -z "$(CLOUDSIGMA_PASSWORD)" ] || [ -z "$(CLOUDSIGMA_REGION)" ]; then \
		echo "Error: CLOUDSIGMA_USERNAME, CLOUDSIGMA_PASSWORD, and CLOUDSIGMA_REGION must be set"; \
		exit 1; \
	fi
	K8S_VERSION=$(K8S_VERSION) UBUNTU_VERSION=$(UBUNTU_VERSION) ./build-on-cloudsigma.sh

clean:
	@echo "==> Cleaning build artifacts"
	rm -rf output-ubuntu-k8s
	rm -rf packer_cache

help:
	@echo "Available targets:"
	@echo "  init                  - Initialize Packer plugins"
	@echo "  validate              - Validate Packer template"
	@echo "  build                 - Build image locally with QEMU (requires upload)"
	@echo "  build-on-cloudsigma   - Build image directly on CloudSigma (no upload needed!)"
	@echo "  clean                 - Clean build artifacts"
	@echo ""
	@echo "Environment variables:"
	@echo "  K8S_VERSION          - Kubernetes version (default: 1.34.1)"
	@echo "  UBUNTU_VERSION       - Ubuntu version (default: 24.04)"
	@echo "  CLOUDSIGMA_USERNAME  - CloudSigma username"
	@echo "  CLOUDSIGMA_PASSWORD  - CloudSigma password"
	@echo "  CLOUDSIGMA_REGION    - CloudSigma region (e.g., next)"
	@echo ""
	@echo "Examples:"
	@echo "  make build K8S_VERSION=1.34.1                    # Build locally"
	@echo "  make build-on-cloudsigma K8S_VERSION=1.34.1      # Build on CloudSigma (recommended!)"
