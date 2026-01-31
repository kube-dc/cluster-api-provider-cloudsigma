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

### 1. Install CAPCS Provider

```bash
# Install the CloudSigma CAPI provider
kubectl apply -f https://raw.githubusercontent.com/kube-dc/cluster-api-provider-cloudsigma/main/config/install.yaml

# Or from local config
kubectl apply -f config/install.yaml
```

### 2. Configure Authentication

CAPCS supports two authentication modes. **Impersonation is the default and recommended mode**.

#### Option A: OAuth Impersonation (Default - Recommended)

Creates VMs in individual user's CloudSigma accounts using service account impersonation.

```bash
# Create secret with OAuth impersonation credentials
kubectl create secret generic cloudsigma-impersonation \
  --namespace=capcs-system \
  --from-literal=oauth-url='https://oauth.cloudsigma.com' \
  --from-literal=client-id='your-service-account-client-id' \
  --from-literal=client-secret='your-service-account-secret'

# Set environment variables in deployment
CLOUDSIGMA_OAUTH_URL=https://oauth.cloudsigma.com
CLOUDSIGMA_CLIENT_ID=your-service-account-client-id
CLOUDSIGMA_CLIENT_SECRET=your-service-account-secret
CLOUDSIGMA_REGION=zrh
```

With impersonation, each `CloudSigmaCluster` must have `spec.userEmail` set to specify which user's account to create VMs in.

#### Option B: Legacy Credentials (Must be explicitly enabled)

Uses a single CloudSigma account for all VMs. **Disabled by default** - requires explicit opt-in.

```bash
# Create secret with legacy credentials
kubectl create secret generic cloudsigma-credentials \
  --namespace=capcs-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'

# Enable legacy mode (required - disabled by default)
CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS=true
CLOUDSIGMA_USERNAME=your-email@example.com
CLOUDSIGMA_PASSWORD=your-api-password
CLOUDSIGMA_REGION=zrh
```

#### Environment Variables Reference

| Variable | Description | Required |
|----------|-------------|----------|
| `CLOUDSIGMA_OAUTH_URL` | OAuth/Keycloak URL for impersonation | For impersonation |
| `CLOUDSIGMA_CLIENT_ID` | Service account client ID | For impersonation |
| `CLOUDSIGMA_CLIENT_SECRET` | Service account client secret | For impersonation |
| `CLOUDSIGMA_REGION` | CloudSigma region (zrh, sjc, hnl, per) | Yes |
| `CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS` | Set to `true` to enable legacy auth | For legacy mode |
| `CLOUDSIGMA_USERNAME` | CloudSigma username (legacy) | For legacy mode |
| `CLOUDSIGMA_PASSWORD` | CloudSigma password (legacy) | For legacy mode |

```bash
# Verify provider is running
kubectl get pods -n capcs-system
kubectl logs -n capcs-system -l control-plane=controller-manager
```

### 3. Install CRDs

```bash
# Install CloudSigma CRDs
kubectl apply -f config/crd/bases/
```

### 4. Deploy CloudSigma Workers

```bash
# Create workers using an example
kubectl apply -f examples/cloudsigma-test-cluster.yaml

# Watch machines being created
kubectl get cloudsigmamachines -A -w
```

### 5. Access Worker Nodes

```bash
# Check worker nodes joining the cluster
kubectl get nodes

# View machine details
kubectl describe cloudsigmamachine <machine-name>
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

**Current Phase:** Alpha Development
- [x] Project setup
- [x] CRD definitions
- [x] CloudSigma SDK integration
- [x] Controller implementation
- [x] Worker node images (Ubuntu 24.04 + K8s)
- [x] Docker image build and deployment
- [x] Basic documentation
- [ ] Comprehensive testing framework
- [ ] E2E test suite

**Next Phase:** Beta Release
- [x] Cloud Controller Manager (separate project)
- [ ] Advanced networking features
- [ ] Multi-region support optimization
- [ ] Production hardening
- [ ] Performance benchmarking
- [ ] Security audit

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
