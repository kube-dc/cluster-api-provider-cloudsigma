# Build and Deploy Guide

## Building the Controller Image

### Prerequisites

1. **Docker** installed and running
2. **Go 1.22+** installed
3. **Git** for versioning
4. **Registry access** - Push permissions to `ghcr.io/kube-dc`

### Quick Build and Push

#### Method 1: Using Makefile (Recommended)

```bash
# Build versioned image (uses git tag/commit)
make docker-build

# Build and tag as latest
make docker-build-latest

# Push versioned image
make docker-push

# Build and push both versioned and latest
make docker-build-push
```

#### Method 2: Using Build Script

```bash
# Build and push with automatic versioning
./hack/build-and-push.sh

# Custom version
VERSION=v0.1.0 ./hack/build-and-push.sh

# Custom registry
REGISTRY=ghcr.io/myorg VERSION=v0.1.0 ./hack/build-and-push.sh

# Skip latest tag
LATEST=false ./hack/build-and-push.sh
```

### Registry Configuration

**Default Registry:** `ghcr.io/kube-dc`

**Image Name:** `cluster-api-provider-cloudsigma`

**Versioning:**
- Automatic from git: `git describe --tags --always --dirty`
- Manual: Set `VERSION=v0.1.0`

#### Login to GitHub Container Registry

```bash
# Create Personal Access Token with packages:write permission
# https://github.com/settings/tokens

export GITHUB_TOKEN="your_github_token"
echo $GITHUB_TOKEN | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
```

### Build Options

#### Custom Registry

```bash
make docker-build REGISTRY=ghcr.io/yourorg
```

#### Custom Version

```bash
make docker-build VERSION=v0.2.0
```

#### Custom Image Name

```bash
make docker-build IMAGE_NAME=capcs-provider
```

#### Full Custom

```bash
make docker-build \
  REGISTRY=ghcr.io/myorg \
  IMAGE_NAME=cloudsigma-capi \
  VERSION=v1.0.0
```

## Deploying to Cluster

### Option 1: Update Existing Deployment

```bash
# After building and pushing
make docker-build-push

# Update the deployment
kubectl set image deployment/cloudsigma-controller-manager \
  manager=ghcr.io/kube-dc/cluster-api-provider-cloudsigma:$(git describe --tags --always) \
  -n capcs-system

# Verify rollout
kubectl rollout status deployment/cloudsigma-controller-manager -n capcs-system
```

### Option 2: Update Installation Manifest

```bash
# Build and push
make docker-build-push

# Update deployment.yaml
VERSION=$(git describe --tags --always)
sed -i "s|image:.*cluster-api-provider-cloudsigma:.*|image: ghcr.io/kube-dc/cluster-api-provider-cloudsigma:${VERSION}|" \
  config/manager/deployment.yaml

# Rebuild install.yaml
cat > config/install.yaml << 'EOF'
# Install CloudSigma Cluster API Provider
EOF
echo "---" >> config/install.yaml
cat config/manager/namespace.yaml >> config/install.yaml
echo "---" >> config/install.yaml
cat config/rbac/service_account.yaml >> config/install.yaml
echo "---" >> config/install.yaml
cat config/rbac/role.yaml >> config/install.yaml
echo "---" >> config/install.yaml
cat config/rbac/role_binding.yaml >> config/install.yaml
echo "---" >> config/install.yaml
cat config/manager/service.yaml >> config/install.yaml
echo "---" >> config/install.yaml
cat config/manager/deployment.yaml >> config/install.yaml

# Apply
kubectl apply -f config/install.yaml
```

### Option 3: Fresh Install

```bash
# Build and push
make docker-build-push

# Install CRDs
kubectl apply -f config/crd/bases/

# Create credentials
kubectl create secret generic cloudsigma-credentials \
  --namespace=capcs-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-password' \
  --from-literal=region='zrh'

# Deploy provider
kubectl apply -f config/install.yaml
```

## Development Workflow

### Local Development (No Docker)

```bash
# Install CRDs
make install

# Export credentials
export CLOUDSIGMA_USERNAME="your-email@example.com"
export CLOUDSIGMA_PASSWORD="your-password"
export CLOUDSIGMA_REGION="zrh"

# Run controller locally
make run
```

### Build and Test Locally

```bash
# Build binary
make build

# Run binary
export CLOUDSIGMA_USERNAME="your-email@example.com"
export CLOUDSIGMA_PASSWORD="your-password"
export CLOUDSIGMA_REGION="zrh"
./bin/manager
```

### Quick Development Cycle

```bash
# 1. Make code changes
vim controllers/cloudsigmamachine_controller.go

# 2. Format and vet
make fmt vet

# 3. Regenerate manifests if needed
make manifests generate

# 4. Build and test locally
make build
./bin/manager  # Test locally

# 5. Build and push image
make docker-build-push

# 6. Update deployment
kubectl rollout restart deployment/cloudsigma-controller-manager -n capcs-system
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Build and Push

on:
  push:
    tags:
      - 'v*'
    branches:
      - main

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      packages: write
      contents: read
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0  # For git describe
      
      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      
      - name: Build and Push
        run: |
          make docker-build-push
```

## Versioning Strategy

### Semantic Versioning

**Format:** `v<major>.<minor>.<patch>`

**Examples:**
- `v0.1.0` - Initial release
- `v0.2.0` - New features
- `v0.2.1` - Bug fixes
- `v1.0.0` - Production ready

### Tagging Releases

```bash
# Create annotated tag
git tag -a v0.1.0 -m "Release v0.1.0: Initial CloudSigma provider"

# Push tag
git push origin v0.1.0

# Build and push with tag version
make docker-build-push
# This automatically uses the git tag as VERSION
```

### Development Builds

```bash
# Automatic dev version (uses commit hash)
make docker-build-push
# Creates: ghcr.io/kube-dc/cluster-api-provider-cloudsigma:abc1234-dirty

# Manual dev version
VERSION=dev make docker-build-push
```

## Image Verification

### Check Built Image

```bash
# List local images
docker images | grep cluster-api-provider-cloudsigma

# Inspect image
docker inspect ghcr.io/kube-dc/cluster-api-provider-cloudsigma:latest

# Test run locally
docker run --rm ghcr.io/kube-dc/cluster-api-provider-cloudsigma:latest --help
```

### Verify in Registry

```bash
# Check GHCR
# Visit: https://github.com/orgs/kube-dc/packages

# Pull image
docker pull ghcr.io/kube-dc/cluster-api-provider-cloudsigma:latest
```

### Verify in Cluster

```bash
# Check running image
kubectl get deployment cloudsigma-controller-manager -n capcs-system -o jsonpath='{.spec.template.spec.containers[0].image}'

# Check pod image
kubectl get pods -n capcs-system -o jsonpath='{.items[*].spec.containers[*].image}'
```

## Troubleshooting

### Build Fails

```bash
# Clean build cache
docker builder prune

# Check Go modules
go mod tidy
go mod verify

# Build locally first
make build
```

### Push Fails - Authentication

```bash
# Re-login to registry
echo $GITHUB_TOKEN | docker login ghcr.io -u YOUR_USERNAME --password-stdin

# Check token permissions
# Token needs: write:packages, read:packages
```

### Push Fails - Permission Denied

```bash
# Check repository ownership
# Image must be pushed to organization you have access to

# Verify you're a member of kube-dc organization
# Or use your personal registry: REGISTRY=ghcr.io/yourusername
```

### Image Won't Run

```bash
# Check logs
kubectl logs -n capcs-system -l control-plane=controller-manager

# Common issues:
# 1. Missing credentials secret
# 2. Wrong image version in deployment
# 3. ImagePullBackOff - check registry access
```

## Multi-Architecture Builds

### Build for Multiple Platforms

```bash
# Enable Docker buildx
docker buildx create --use

# Build multi-arch
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t ghcr.io/kube-dc/cluster-api-provider-cloudsigma:latest \
  --push \
  .
```

## References

- [Dockerfile](../Dockerfile)
- [Build Script](../hack/build-and-push.sh)
- [Makefile](../Makefile)
- [GitHub Container Registry Docs](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
