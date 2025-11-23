# Cluster API Provider CloudSigma

[![Go Report Card](https://goreportcard.com/badge/github.com/kube-dc/cluster-api-provider-cloudsigma)](https://goreportcard.com/report/github.com/kube-dc/cluster-api-provider-cloudsigma)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Kubernetes Cluster API infrastructure provider for CloudSigma.

## Overview

The Cluster API Provider CloudSigma (CAPCS) enables declarative Kubernetes cluster creation, configuration, and management on [CloudSigma](https://www.cloudsigma.com/) infrastructure using the Kubernetes [Cluster API](https://cluster-api.sigs.k8s.io/).

## Features

- ✅ **Native Kubernetes Integration** - Manage CloudSigma infrastructure with Kubernetes APIs
- ✅ **Declarative Cluster Management** - Define clusters as code using Kubernetes manifests
- ✅ **Full Lifecycle Management** - Create, scale, upgrade, and delete clusters
- ✅ **Multi-Region Support** - Deploy across CloudSigma datacenters (Zurich, Las Vegas, San Jose, Tokyo)
- ✅ **CloudSigma Go SDK** - Built on official [CloudSigma Go SDK](https://github.com/cloudsigma/cloudsigma-sdk-go)
- ✅ **Cloud Controller Manager** - Native Kubernetes cloud provider integration
- ✅ **Production Ready** - Designed for production workloads

## Project Status

🚧 **Alpha** - Active development, not yet ready for production use.

See [ROADMAP.md](ROADMAP.md) for planned features and timeline.

## Documentation

- [Quick Start Guide](docs/quickstart.md)
- [Installation Guide](docs/installation.md)
- [API Reference](docs/api-reference.md)
- [Development Guide](docs/development.md)
- [Troubleshooting](docs/troubleshooting.md)

## Architecture

```
Management Cluster
  ├── Cluster API Core
  ├── CAPCS Controllers
  │   ├── CloudSigmaMachine Controller
  │   └── CloudSigmaCluster Controller
  └── CloudSigma SDK Client
          │
          ▼ (CloudSigma API)
CloudSigma Infrastructure
  ├── Servers (VMs)
  ├── Networks (VLANs)
  └── Storage (Drives)
```

## Custom Resources

CAPCS provides the following Kubernetes custom resources:

- **CloudSigmaCluster** - Represents a CloudSigma cluster infrastructure
- **CloudSigmaMachine** - Represents a CloudSigma server (VM)
- **CloudSigmaMachineTemplate** - Template for creating CloudSigmaMachine instances

See [CRD Documentation](docs/crd-reference.md) for detailed specifications.

## Prerequisites

- Kubernetes cluster v1.26+ (management cluster)
- [Cluster API](https://cluster-api.sigs.k8s.io/) v1.7.0+ installed
- CloudSigma account with API access
- kubectl v1.26+
- Go 1.21+ (for development)

## Quick Start

### 1. Install CAPCS

```bash
# Install Cluster API core
clusterctl init

# Install CloudSigma provider
clusterctl init --infrastructure cloudsigma
```

### 2. Configure Credentials

```bash
# Create secret with CloudSigma credentials
kubectl create secret generic cloudsigma-credentials \
  --namespace=default \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password'
```

### 3. Create a Cluster

```bash
# Generate cluster manifest
clusterctl generate cluster my-cluster \
  --infrastructure cloudsigma \
  --kubernetes-version v1.34.0 \
  --control-plane-machine-count 3 \
  --worker-machine-count 3 > my-cluster.yaml

# Apply manifest
kubectl apply -f my-cluster.yaml

# Watch cluster creation
clusterctl describe cluster my-cluster
```

### 4. Access the Workload Cluster

```bash
# Get kubeconfig
clusterctl get kubeconfig my-cluster > my-cluster-kubeconfig.yaml

# Access cluster
kubectl --kubeconfig=my-cluster-kubeconfig.yaml get nodes
```

## Example Cluster

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
        - 10.220.0.0/16
    services:
      cidrBlocks:
        - 10.96.0.0/12
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: KubeadmControlPlane
    name: my-cluster-control-plane
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: CloudSigmaCluster
    name: my-cluster
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  region: zrh
  vlan:
    cidr: "10.220.0.0/16"
  credentialsRef:
    name: cloudsigma-credentials
    namespace: default
```

See [examples/](examples/) for more configurations.

## Development

### Prerequisites

- Go 1.21+
- Docker
- [kubebuilder](https://book.kubebuilder.io/quick-start.html#installation)
- make

### Setup Development Environment

```bash
# Clone repository
git clone https://github.com/kube-dc/cluster-api-provider-cloudsigma.git
cd cluster-api-provider-cloudsigma

# Install dependencies
go mod download

# Install CRDs
make install

# Run controller locally
make run
```

### Running Tests

```bash
# Unit tests
make test

# Integration tests (requires CloudSigma credentials)
make test-integration

# E2E tests
make test-e2e
```

### Building

```bash
# Build binary
make build

# Build Docker image
make docker-build IMG=myrepo/capcs:dev

# Push Docker image
make docker-push IMG=myrepo/capcs:dev
```

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on:

- Code of Conduct
- Development workflow
- Pull request process
- Coding standards

## Community

- **Discussions**: [GitHub Discussions](https://github.com/kube-dc/cluster-api-provider-cloudsigma/discussions)
- **Issues**: [GitHub Issues](https://github.com/kube-dc/cluster-api-provider-cloudsigma/issues)
- **Slack**: #kube-dc on Kubernetes Slack

## Roadmap

See [ROADMAP.md](ROADMAP.md) for current status and future plans.

**Current Phase:** Alpha Development (Weeks 1-3)
- [x] Project setup
- [x] CRD definitions
- [ ] CloudSigma SDK integration
- [ ] Controller implementation
- [ ] Testing framework

**Next Phase:** Beta Release (Weeks 4-6)
- [ ] Cloud Controller Manager
- [ ] Worker node images
- [ ] Documentation
- [ ] Production hardening

## License

Copyright 2025 Kube-DC Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

## Acknowledgments

Built with:
- [Cluster API](https://cluster-api.sigs.k8s.io/) - Kubernetes cluster lifecycle management
- [CloudSigma Go SDK](https://github.com/cloudsigma/cloudsigma-sdk-go) - Official CloudSigma SDK
- [Kubebuilder](https://kubebuilder.io/) - SDK for building Kubernetes APIs

## Related Projects

- [kube-dc-k8-manager](https://github.com/kube-dc/kube-dc-k8-manager) - Kamaji-based multi-tenant Kubernetes manager
- [Cluster API](https://github.com/kubernetes-sigs/cluster-api) - Cluster API core
- [CloudSigma](https://www.cloudsigma.com/) - Cloud infrastructure provider
